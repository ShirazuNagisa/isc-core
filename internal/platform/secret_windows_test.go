//go:build windows

package platform

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件覆盖 Windows 的密钥存储：DPAPI 保护 + 文件承载。
//
// # 它为什么必需
//
// internal/secret/manager_test.go 里有一句注释：
//
//	各平台的后端差异由 internal/platform 自己的测试覆盖
//
// 而**那些测试此前并不存在**。此前 DPAPI 只被 manager 的测试**间接**跑到
// —— 那条路径确实走真实现，但它验证的是"管理器能用"，而不是"DPAPI 这一层
// 自己是对的"。
//
// 而这一层保护的是主密钥：主密钥丢了，所有已加密的凭据永久无法解密。

func newDPAPIStore(t *testing.T) *dpapiSecretStore {
	t.Helper()
	return &dpapiSecretStore{dir: filepath.Join(t.TempDir(), secretsDirName)}
}

// TestDPAPIRoundTrip 是最基本的一条。
func TestDPAPIRoundTrip(t *testing.T) {
	t.Parallel()

	s := newDPAPIStore(t)
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

// TestDPAPIPlaintextNeverTouchesDisk 是本文件**最重要**的一条。
//
// DPAPI 的全部意义在于"磁盘上的字节离开这台机器的这个账户就不再有意义"。
// 如果实现不小心把明文也写进了文件（或者写了一份备份），那么加密就只是
// 一层装饰 —— 而**从外部看不出任何区别**：功能一切正常，读写都对。
//
// 因此这条测试不看接口的返回值，而是直接去读磁盘上的字节。
func TestDPAPIPlaintextNeverTouchesDisk(t *testing.T) {
	t.Parallel()

	s := newDPAPIStore(t)
	ctx := context.Background()

	// 一段足够长、足够独特的明文 —— 短串可能碰巧出现在密文里。
	secret := []byte("SUPER-SECRET-MASTER-KEY-DO-NOT-LEAK-0123456789abcdef")
	if err := s.Put(ctx, "master", secret); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(s.dir, "master.dpapi")
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读密钥文件失败: %v", err)
	}

	if bytes.Contains(onDisk, secret) {
		t.Fatal("磁盘上的文件里**含有明文主密钥** —— " +
			"DPAPI 保护没有生效，而这从接口上是看不出来的")
	}

	// 也不该含有明文里有意义的片段 —— 防止"只加密了前后各一段"这种实现。
	for _, frag := range [][]byte{
		[]byte("SUPER-SECRET"),
		[]byte("MASTER-KEY"),
		[]byte("0123456789abcdef"),
	} {
		if bytes.Contains(onDisk, frag) {
			t.Errorf("磁盘文件里含有明文片段 %q", frag)
		}
	}
}

// TestDPAPICiphertextIsBoundToThisUser 验证密文确实"打了封条"。
//
// 一次篡改（模拟把文件拷到别处、或被人改过）必须让解密失败，而不是
// 返回一段垃圾 —— 后者会被当成主密钥去解密凭据，症状是"所有凭据都解不开"
// 而没有任何线索。
func TestDPAPICiphertextIsBoundToThisUser(t *testing.T) {
	t.Parallel()

	s := newDPAPIStore(t)
	ctx := context.Background()

	if err := s.Put(ctx, "master", []byte("real-key")); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(s.dir, "master.dpapi")
	sealed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) < 8 {
		t.Fatalf("密文太短（%d 字节），不像 DPAPI 的输出", len(sealed))
	}

	// 翻掉中间一个字节。
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)/2] ^= 0xff
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Get(ctx, "master"); err == nil {
		t.Error("被篡改的密文必须解密失败，而不是返回一段垃圾")
	}
}

// TestDPAPIDeleteAndMissing 覆盖删除与"没找到"。
//
// 主密钥尚不存在是**正常路径**（首次启动），不是错误。
func TestDPAPIDeleteAndMissing(t *testing.T) {
	t.Parallel()

	s := newDPAPIStore(t)
	ctx := context.Background()

	if _, found, err := s.Get(ctx, "master"); err != nil || found {
		t.Fatalf("缺失时应当 (false, nil)，得到 found=%v err=%v", found, err)
	}

	if err := s.Put(ctx, "master", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "master"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Get(ctx, "master"); found {
		t.Error("删除之后仍然读得到")
	}
	// 幂等。
	if err := s.Delete(ctx, "master"); err != nil {
		t.Errorf("重复删除不该报错: %v", err)
	}
}

// TestDPAPIRejectsBadNames 验证名称校验在这一层也生效。
func TestDPAPIRejectsBadNames(t *testing.T) {
	t.Parallel()

	s := newDPAPIStore(t)
	ctx := context.Background()

	for _, bad := range []string{"", "..", "../escape", "a/b", `a\b`} {
		if err := s.Put(ctx, bad, []byte("x")); err == nil {
			t.Errorf("Put(%q) 应当被拒绝", bad)
		}
		if _, _, err := s.Get(ctx, bad); err == nil {
			t.Errorf("Get(%q) 应当被拒绝", bad)
		}
		if err := s.Delete(ctx, bad); err == nil {
			t.Errorf("Delete(%q) 应当被拒绝", bad)
		}
	}
}

// TestDPAPIDescribeIsHonest 钉住"如实报告保护级别"。
func TestDPAPIDescribeIsHonest(t *testing.T) {
	t.Parallel()

	st := newDPAPIStore(t).Describe()
	if !st.Available {
		t.Error("DPAPI 在 Windows 上是可用的")
	}
	if st.Backend != "windows-dpapi" {
		t.Errorf("后端标识是 %q，期望 windows-dpapi", st.Backend)
	}
	if !strings.Contains(st.Note, "DPAPI") {
		t.Errorf("说明里应当点明用的是 DPAPI，得到 %q", st.Note)
	}
	// 说明里应当讲清保护边界。
	//
	// 断言**不依赖语言**：文案现在跟着全局默认语言走（见 internal/i18n），
	// 而并行测试可能把它设成英文。写死一个中文片段会让这条测试在
	// 语言被改动时莫名其妙地变红 —— 那与它要验证的东西无关。
	if len([]rune(st.Note)) < 20 {
		t.Errorf("说明太短，没有讲清保护边界: %q", st.Note)
	}
	if !strings.Contains(st.Note, "DPAPI") {
		t.Errorf("说明里应当点明用的是 DPAPI，得到 %q", st.Note)
	}
}

// TestDPAPILeavesNoTempFile 验证原子写不留残渣。
func TestDPAPILeavesNoTempFile(t *testing.T) {
	t.Parallel()

	s := newDPAPIStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := s.Put(ctx, "master", []byte("v")); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".key-") {
			t.Errorf("留下了临时文件 %q", e.Name())
		}
	}
}
