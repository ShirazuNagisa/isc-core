//go:build !windows

package platform

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 本文件守住"密钥条目与数据目录绑定"这条性质。
//
// 它修的是一个真实发生过的故障：Keychain / Secret Service 的条目名是全局的，
// 而测试各自建临时数据目录 —— 于是 `go test ./...` 把登录钥匙串里那条
// `isc-core/master` 覆盖成测试值（`TestCorruptedKeyIsRejected` 故意写的
// "too short"），接着**所有**以临时目录启动内核的测试集体报"主密钥长度异常"。
// 而在装了真实内核的机器上，同样的机制会把用户的主密钥换掉 —— 那等价于
// 把全部凭据变成解不开的密文。

// TestKeyScopeIsStableAndDistinct 钉住指纹的两个方向。
//
// 同值同指纹（否则每次启动都换条目名，等于每次都读不到主密钥）；
// 异值异指纹（否则绑定没有意义）。
func TestKeyScopeIsStableAndDistinct(t *testing.T) {
	t.Parallel()

	a := t.TempDir()
	b := t.TempDir()

	if keyScope(a) != keyScope(a) {
		t.Error("同一个目录两次算出的指纹不同 —— 那会让内核每次都读不到主密钥")
	}
	if keyScope(a) == keyScope(b) {
		t.Error("两个不同目录算出了同一个指纹 —— 绑定失效")
	}
	// 相对路径与绝对路径必须归一化到同一个指纹：内核可能以不同的
	// 工作目录启动，而那不该改变"密钥住在哪"。
	if keyScope(a) != keyScope(filepath.Clean(a)) {
		t.Error("同一路径的不同写法应当得到同一个指纹")
	}
	if scope := keyScope(a); len(scope) != 8 || strings.ContainsAny(scope, "@/\\ ") {
		t.Errorf("指纹 %q 的形状不对（要是 8 个十六进制字符，且不含分隔符）", scope)
	}
}

// newCLIStoreForTest 构造**真实的**命令行密钥库后端，没有就跳过。
//
// 这几条测试刻意绕开 NewSecretStore：测试默认走文件存储
// （见 testsupport.IsolateSecretStore），而这里要验证的恰恰是钥匙串那一侧
// 的绑定行为。它们自己清理写进去的条目。
func newCLIStoreForTest(t *testing.T, dir string) *cliSecretStore {
	t.Helper()

	switch runtime.GOOS {
	case "darwin":
		bin, err := exec.LookPath("security")
		if err != nil {
			t.Skip("这台机器上没有 security（Keychain），跳过")
		}
		return &cliSecretStore{dir: dir, bin: bin, kind: kindKeychain, scope: keyScope(dir)}
	case "linux", "freebsd", "openbsd", "netbsd":
		if !hasSecretServiceSession() {
			t.Skip("没有可用的 Secret Service 会话，跳过")
		}
		bin, err := exec.LookPath("secret-tool")
		if err != nil {
			t.Skip("这台机器上没有 secret-tool，跳过")
		}
		return &cliSecretStore{dir: dir, bin: bin, kind: kindSecretTool, scope: keyScope(dir)}
	default:
		t.Skip("这个平台没有命令行密钥库后端")
		return nil
	}
}

// TestSecretStoreIsScopedByDataDir 是这条性质的核心断言。
//
// 两个数据目录必须互不可见。它在文件后端上本来就成立（文件在目录里），
// 而在 Keychain / Secret Service 上只有绑定之后才成立 —— 这条测试在
// 绑定之前**一定**失败，它就是为此写的。
func TestSecretStoreIsScopedByDataDir(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dirA := t.TempDir()
	dirB := t.TempDir()

	storeA := newCLIStoreForTest(t, dirA)
	storeB := newCLIStoreForTest(t, dirB)

	const name = "scope-probe"
	want := []byte("value-of-A")

	if err := storeA.Put(ctx, name, want); err != nil {
		t.Skipf("这台机器上没有可用的密钥库，跳过：%v", err)
	}
	t.Cleanup(func() { _ = storeA.Delete(ctx, name) }) //nolint:errcheck // 测试清理
	t.Cleanup(func() { _ = storeB.Delete(ctx, name) }) //nolint:errcheck // 测试清理

	got, found, err := storeA.Get(ctx, name)
	if err != nil {
		t.Fatalf("读回自己写的值失败: %v", err)
	}
	if !found || string(got) != string(want) {
		t.Fatalf("同一个数据目录里读不回自己的值: got=%q found=%v", got, found)
	}

	// 另一个数据目录**必须看不到**它。
	other, found, err := storeB.Get(ctx, name)
	if err != nil {
		t.Fatalf("另一个数据目录读取时报错: %v", err)
	}
	if found {
		t.Errorf("另一个数据目录读到了不属于它的密钥（%q）—— "+
			"条目没有与数据目录绑定，两个安装会共用一把主密钥，"+
			"测试也会互相覆盖", other)
	}
}

// TestLegacyKeyIsStillRead 钉住升级路径。
//
// 绑定之前写下的条目叫 `master`（没有指纹后缀）。升级上来的安装必须还能
// 读到它 —— 否则内核会认为"没有主密钥"并生成一把新的，用户已有的凭据
// 当场全部作废。
func TestLegacyKeyIsStillRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()

	cli := newCLIStoreForTest(t, dir)
	store := SecretStore(cli)

	// 模拟"旧版本写下的全局条目"：直接用不带指纹的名字写。
	legacy := scopedKeyName("legacy-probe", "")
	want := []byte("written-by-the-old-version")
	if err := cli.putAccount(ctx, legacy, want); err != nil {
		t.Skipf("写不进密钥库，跳过：%v", err)
	}
	t.Cleanup(func() {
		_ = cli.deleteAccount(ctx, legacy)                                   //nolint:errcheck // 测试清理
		_ = cli.deleteAccount(ctx, scopedKeyName("legacy-probe", cli.scope)) //nolint:errcheck // 测试清理
	})

	got, found, err := store.Get(ctx, "legacy-probe")
	if err != nil {
		t.Fatalf("读旧条目失败: %v", err)
	}
	if !found || string(got) != string(want) {
		t.Fatalf("旧条目没有被回退读到: got=%q found=%v —— "+
			"升级之后内核会生成新主密钥，用户的凭据全部解不开", got, found)
	}

	// 顺带确认：它被搬到了绑定后的名字上（否则会永远停在读旧条目）。
	moved, found, err := cli.getAccount(ctx, scopedKeyName("legacy-probe", cli.scope))
	if err != nil {
		t.Fatalf("读绑定后的条目失败: %v", err)
	}
	if !found || string(moved) != string(want) {
		t.Errorf("旧条目没有被搬到绑定后的名字上: got=%q found=%v", moved, found)
	}
}

// TestScopedDeleteRemovesBoth 钉住删除的两条路径。
//
// 只删绑定后的那条，读取会从旧条目回退捡回来 —— 用户看到的是
// "删了又回来了"。
func TestScopedDeleteRemovesBoth(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	cli := newCLIStoreForTest(t, dir)
	store := SecretStore(cli)

	legacy := scopedKeyName("delete-probe", "")
	if err := cli.putAccount(ctx, legacy, []byte("old")); err != nil {
		t.Skipf("写不进密钥库，跳过：%v", err)
	}
	if err := store.Put(ctx, "delete-probe", []byte("new")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	if err := store.Delete(ctx, "delete-probe"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	if _, found, err := store.Get(ctx, "delete-probe"); err != nil || found {
		t.Errorf("删除之后仍然读得到（found=%v err=%v）—— "+
			"旧条目没被一起删掉", found, err)
	}
}
