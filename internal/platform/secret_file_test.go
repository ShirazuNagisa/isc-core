package platform

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 本文件覆盖**兜底**密钥存储：把密钥以 0600 权限写在受保护目录下。
//
// # 它为什么必需
//
// internal/secret/manager_test.go 里有一句注释：
//
//	各平台的后端差异由 internal/platform 自己的测试覆盖
//
// 而**那些测试此前并不存在**。这个文件把其中跨平台的那一半补上。
//
// 兜底实现值得单独测，因为它是**所有平台**共用的那条退路：
// 无桌面会话的 Linux 服务器、容器、找不到 security 命令的 macOS ——
// 都会落到它这里。而它保护的是主密钥：主密钥丢了，所有已加密的凭据
// 就永久无法解密。

// newFileStore 构造一个落在临时目录里的兜底存储。
func newFileStore(t *testing.T) *fileSecretStore {
	t.Helper()
	return newFileSecretStore(filepath.Join(t.TempDir(), "secrets"), "测试用")
}

// TestFileStoreRoundTrip 是最基本的一条：写进去、读出来。
func TestFileStoreRoundTrip(t *testing.T) {
	t.Parallel()

	s := newFileStore(t)
	ctx := context.Background()

	want := []byte("master-key-material")
	if err := s.Put(ctx, "master", want); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	got, found, err := s.Get(ctx, "master")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !found {
		t.Fatal("刚写进去的密钥没找到")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("读回来的是 %q，期望 %q", got, want)
	}
}

// TestFileStoreHandlesBinaryValues 钉住一个**关键**性质。
//
// 主密钥是 32 字节随机值，**很可能含 NUL 字节与非法 UTF-8 序列**。
// 任何把它当字符串处理的环节（trim、编码转换、按行读写）都会损坏它 ——
// 而症状是"凭据突然全部解不开"，且没有任何线索指向密钥存储。
//
// 真随机值里出现 0x00 的概率约 12%（32 字节中至少一个），因此这不是
// 理论问题。
func TestFileStoreHandlesBinaryValues(t *testing.T) {
	t.Parallel()

	s := newFileStore(t)
	ctx := context.Background()

	cases := map[string][]byte{
		"含 NUL":      {0x00, 0x01, 0x00, 0xff},
		"全零":         make([]byte, 32),
		"非法 UTF-8":   {0xff, 0xfe, 0xfd, 0x80},
		"含换行":        []byte("line1\nline2\r\n"),
		"首尾空白":       []byte("  padded  "),
		"空值":         {},
		"32 字节真随机形态": bytes.Repeat([]byte{0x00, 0xff}, 16),
	}

	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			if err := s.Put(ctx, "k", want); err != nil {
				t.Fatalf("写入失败: %v", err)
			}
			got, found, err := s.Get(ctx, "k")
			if err != nil {
				t.Fatalf("读取失败: %v", err)
			}
			if !found {
				t.Fatal("没找到")
			}
			if !bytes.Equal(got, want) {
				t.Errorf("读回来的是 %v，期望 %v", got, want)
			}
		})
	}
}

// TestFileStorePermissions 钉住文件与目录的权限。
//
// 这是兜底实现的**全部**保护：没有操作系统密钥库时，安全性就等于
// 文件系统权限。0600 之外任何一个值都意味着同机器上的其它账号能读走主密钥。
func TestFileStorePermissions(t *testing.T) {
	t.Parallel()

	// Windows 不按 POSIX 权限位报告（一律 0666/0777），因此这条断言
	// 只在类 Unix 上有意义。用 runtime.GOOS 而不是环境变量 ——
	// 后者在运行期根本不存在，写成 os.Getenv("GOOS") 会让跳过条件
	// **永远不成立**，于是测试在 Windows 上必然失败。
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不使用 POSIX 权限位")
	}

	s := newFileStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "master", []byte("x")); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(filepath.Join(s.dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("密钥文件权限是 %o，期望 600 —— "+
			"没有系统密钥库时，权限就是全部的保护", perm)
	}

	di, err := os.Stat(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("密钥目录权限是 %o，期望 700", perm)
	}
}

// TestFileStoreMissingKeyIsNotAnError 钉住"没找到"与"出错"是两回事。
//
// 主密钥尚不存在是**正常路径**（首次启动），而不是错误。把它当错误会
// 让首次启动失败；把它当"找到但为空"会让内核拿着一个空密钥去加密。
func TestFileStoreMissingKeyIsNotAnError(t *testing.T) {
	t.Parallel()

	s := newFileStore(t)

	got, found, err := s.Get(context.Background(), "nope")
	if err != nil {
		t.Errorf("缺失的密钥不该报错，得到: %v", err)
	}
	if found {
		t.Error("缺失的密钥不该报告为找到了")
	}
	if got != nil {
		t.Errorf("缺失时应当返回 nil，得到 %v", got)
	}
}

// TestFileStoreOverwrite 验证覆盖写。
//
// 它必须是**原子**的：中途失败留下一个被截断的密钥文件，就意味着主密钥
// 丢失、所有已加密的凭据永久无法解密。实现用"临时文件 + 重命名"来保证
// 这一点，这条测试盯住它的结果。
func TestFileStoreOverwrite(t *testing.T) {
	t.Parallel()

	s := newFileStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := s.Put(ctx, "master", []byte("value")); err != nil {
			t.Fatal(err)
		}
	}

	got, _, err := s.Get(ctx, "master")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "value" {
		t.Errorf("反复覆盖后读到 %q", got)
	}

	// 而且不该留下临时文件。
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".key-") {
			t.Errorf("留下了临时文件 %q —— 重命名成功后应当被清理", e.Name())
		}
	}
}

// TestFileStoreDeleteIsIdempotent 验证删除。
func TestFileStoreDeleteIsIdempotent(t *testing.T) {
	t.Parallel()

	s := newFileStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "master", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "master"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, found, _ := s.Get(ctx, "master"); found {
		t.Error("删除之后仍然读得到")
	}

	// 再删一次不该报错 —— 卸载/重装这类脚本会重复调用。
	if err := s.Delete(ctx, "master"); err != nil {
		t.Errorf("重复删除不该报错，得到: %v", err)
	}
}

// TestKeyNameRejectsPathTraversal 钉住一条**安全边界**。
//
// 密钥名最终来自代码而非用户输入，但只要有人不小心把外部字符串传进来，
// 未校验的名称就会变成路径穿越（`../../etc/passwd`）。而在**写**的路径上
// 那意味着可以覆盖任意文件。
func TestKeyNameRejectsPathTraversal(t *testing.T) {
	t.Parallel()

	bad := []string{
		"",                   // 空
		"..",                 // 父目录
		"../escape",          // 经典穿越
		"a/../../etc/passwd", // 混在中间
		"sub/key",            // 子路径（会落到目录外）
		"/abs/path",          // 绝对路径
		".hidden",            // 以点开头 → 可能与临时文件混淆
		"key with space",     // 空格
		"key\nnewline",       // 换行
		"key\x00nul",         // NUL（会被底层截断）
		"中文名",                // 非 ASCII
		"key:colon",          // 冒号（Windows 上非法）
		`key\backslash`,      // 反斜杠（Windows 上分隔符）
	}

	dir := t.TempDir()
	for _, name := range bad {
		if _, err := keyFileName(dir, name); err == nil {
			t.Errorf("密钥名 %q 应当被拒绝", name)
		}
	}

	// 合法的名字要能通过 —— 否则上面那组断言可能只是"全都拒绝"。
	good := []string{"master", "MASTER", "k1", "a.b", "a_b", "a-b", "x.y.z"}
	for _, name := range good {
		if _, err := keyFileName(dir, name); err != nil {
			t.Errorf("密钥名 %q 应当被接受，得到: %v", name, err)
		}
	}
}

// TestFileStoreRejectsBadNamesOnEveryPath 验证校验在三个入口都生效。
//
// 只在 Put 上校验而漏掉 Get/Delete，会让"读"与"删"成为穿越的入口 ——
// 而删掉一个任意文件同样是灾难。
func TestFileStoreRejectsBadNamesOnEveryPath(t *testing.T) {
	t.Parallel()

	s := newFileStore(t)
	ctx := context.Background()
	const bad = "../escape"

	if err := s.Put(ctx, bad, []byte("x")); err == nil {
		t.Error("Put 应当拒绝非法名称")
	}
	if _, _, err := s.Get(ctx, bad); err == nil {
		t.Error("Get 应当拒绝非法名称")
	}
	if err := s.Delete(ctx, bad); err == nil {
		t.Error("Delete 应当拒绝非法名称")
	}
}

// TestFileStoreDescribeIsHonest 钉住"如实报告降级"。
//
// 实现文件的注释写得很明确：
//
//	Describe 必须如实报告这一点，绝不能静默降级 —— 用户以为自己受到了
//	操作系统级保护、实际却只有一个文件，是比"明确不可用"更危险的状态。
//
// 这条测试就是把那句话钉住。
func TestFileStoreDescribeIsHonest(t *testing.T) {
	t.Parallel()

	const reason = "当前环境没有 Secret Service 会话"
	s := newFileSecretStore(t.TempDir(), reason)

	st := s.Describe()
	if !st.Available {
		t.Error("兜底实现是可用的，不该报不可用")
	}
	if st.Backend != "file" {
		t.Errorf("后端标识是 %q，期望 file —— "+
			"它会被展示在界面上，不能让用户以为在用系统密钥库", st.Backend)
	}
	if st.Note == "" {
		t.Fatal("必须给出说明 —— 静默降级是本文件明确禁止的")
	}
	if !strings.Contains(st.Note, reason) {
		t.Errorf("说明里应当包含回退原因 %q，得到 %q", reason, st.Note)
	}
	// 同上：断言不依赖语言。
	//
	// 这里能查的是**结构性**的东西 —— 说明必须包含回退原因（那来自调用方，
	// 不是译文），且长度足够讲清保护级别。
	if len([]rune(st.Note)) < 20 {
		t.Errorf("说明太短，没有讲清保护级别: %q", st.Note)
	}
}

// TestNewSecretStoreAlwaysReturnsSomething 验证分发不会返回 nil。
//
// NewSecretStore 是跨平台入口，而"某个平台上忘了实现"会让它返回 nil，
// 然后在**第一次加密凭据时**才 panic。
func TestNewSecretStoreAlwaysReturnsSomething(t *testing.T) {
	t.Parallel()

	s := NewSecretStore(t.TempDir())
	if s == nil {
		t.Fatal("NewSecretStore 返回了 nil")
	}
	if d, ok := s.(describer); ok {
		if st := d.Describe(); st.Backend == "" {
			t.Error("后端标识为空")
		}
	}

	// 而且它必须真的能用 —— 不只是非 nil。
	ctx := context.Background()
	if err := s.Put(ctx, "probe", []byte("v")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	got, found, err := s.Get(ctx, "probe")
	if err != nil || !found {
		t.Fatalf("读回失败: found=%v err=%v", found, err)
	}
	if !bytes.Equal(got, []byte("v")) {
		t.Errorf("读回 %q", got)
	}
	if err := s.Delete(ctx, "probe"); err != nil {
		t.Errorf("删除失败: %v", err)
	}
}

// TestFileStoreGetDoesNotCreateDirectory 钉住一个细节。
//
// Get 不该有副作用：一次纯粹的"读一下有没有主密钥"不该在磁盘上建出目录。
// 那会让"内核是否初始化过"变得难以判断。
func TestFileStoreGetDoesNotCreateDirectory(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "not-yet")
	s := newFileSecretStore(dir, "测试用")

	if _, found, err := s.Get(context.Background(), "master"); err != nil || found {
		t.Fatalf("读缺失的密钥: found=%v err=%v", found, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Get 不该创建目录，但 %s 存在了", dir)
	}
}
