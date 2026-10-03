package secret

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/platform"
	"github.com/ShirazuNagisa/isc-core/internal/testsupport"
)

// TestMain 让整个测试二进制不碰开发机的系统钥匙串（见 testsupport 的说明）。
func TestMain(m *testing.M) { os.Exit(testsupport.IsolateSecretStore(m)) }

// 本测试使用真实的平台密钥存储（Windows 上是 DPAPI + 文件）。
// 刻意不做替身：主密钥的生成与持久化正是这一层要验证的东西，
// 换成内存替身就把被测对象换掉了。

func TestEncryptDecryptRoundTrip(t *testing.T) {
	ctx := context.Background()
	m, err := Open(ctx, newTestStore(t))
	if err != nil {
		t.Fatalf("打开主密钥失败: %v", err)
	}
	if !m.Created() {
		t.Error("首次打开应当生成新主密钥")
	}

	// 覆盖多种输入形态：空、短、长、含 NUL、非 UTF-8。
	cases := [][]byte{
		{},
		[]byte("x"),
		[]byte("a normal API token"),
		bytes.Repeat([]byte("A"), 10000),
		{0x00, 0x01, 0x02, 0xff, 0xfe},
		[]byte("中文与 emoji 🎉"),
	}

	for _, want := range cases {
		env, err := m.Encrypt(want)
		if err != nil {
			t.Fatalf("加密 %d 字节失败: %v", len(want), err)
		}
		got, err := m.Decrypt(env)
		if err != nil {
			t.Fatalf("解密 %d 字节失败: %v", len(want), err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("往返不一致：%q != %q", got, want)
		}
	}
}

// TestCiphertextIsNotPlaintext 是最基本的一条保证。
//
// 它看起来是废话，但把"忘了加密就落库"这类错误挡住的价值极高 ——
// 那种错误不会让任何测试失败，只会让用户的密钥明文躺在磁盘上。
func TestCiphertextIsNotPlaintext(t *testing.T) {
	ctx := context.Background()
	m, _ := Open(ctx, newTestStore(t))

	const plaintext = "super-secret-api-token-value"
	env, err := m.Encrypt([]byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(env, []byte(plaintext)) {
		t.Fatal("密文中出现了明文片段 —— 加密没有真正生效")
	}
	if bytes.Contains(env, []byte("super-secret")) {
		t.Fatal("密文中出现了明文前缀")
	}
}

// TestNonceIsUnique 验证同一明文每次加密结果不同。
//
// GCM 在同一密钥下重用 nonce 会灾难性地泄漏明文异或值，
// 并让攻击者能够伪造消息。这条测试守住它。
func TestNonceIsUnique(t *testing.T) {
	ctx := context.Background()
	m, _ := Open(ctx, newTestStore(t))

	const plaintext = "same input every time"
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		env, err := m.Encrypt([]byte(plaintext))
		if err != nil {
			t.Fatal(err)
		}
		key := string(env)
		if seen[key] {
			t.Fatalf("第 %d 次加密得到了完全相同的密文 —— nonce 没有重新生成", i)
		}
		seen[key] = true
	}
}

// TestTamperedCiphertextIsRejected 验证完整性校验生效。
func TestTamperedCiphertextIsRejected(t *testing.T) {
	ctx := context.Background()
	m, _ := Open(ctx, newTestStore(t))

	env, err := m.Encrypt([]byte("original value"))
	if err != nil {
		t.Fatal(err)
	}

	// 逐字节翻转，每一次都必须解密失败。
	for i := 0; i < len(env); i++ {
		tampered := make([]byte, len(env))
		copy(tampered, env)
		tampered[i] ^= 0x01

		if _, err := m.Decrypt(tampered); err == nil {
			t.Fatalf("翻转第 %d 字节后仍能解密 —— 完整性校验没有生效", i)
		}
	}
}

func TestMalformedEnvelopeRejected(t *testing.T) {
	ctx := context.Background()
	m, _ := Open(ctx, newTestStore(t))

	cases := [][]byte{
		nil,
		{},
		{1},                                     // 只有版本号
		{1, 2, 3},                               // 短于 nonce
		append([]byte{99}, make([]byte, 40)...), // 不认识的格式版本
	}
	for i, env := range cases {
		if _, err := m.Decrypt(env); err == nil {
			t.Errorf("第 %d 个畸形信封应当被拒绝", i)
		}
	}
}

// TestKeyPersistsAcrossReopen 验证主密钥被真正保存下来。
//
// 这条要是坏了，症状是"重启后所有凭据都解不开" —— 而用户会以为
// 是自己的密钥填错了。
func TestKeyPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	m1, err := Open(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	env, err := m1.Encrypt([]byte("persisted secret"))
	if err != nil {
		t.Fatal(err)
	}

	// 重新打开：模拟内核重启。
	m2, err := Open(ctx, store)
	if err != nil {
		t.Fatalf("重新打开主密钥失败: %v", err)
	}
	if m2.Created() {
		t.Error("重新打开不应生成新主密钥")
	}

	got, err := m2.Decrypt(env)
	if err != nil {
		t.Fatalf("重启后无法解密原有密文: %v", err)
	}
	if string(got) != "persisted secret" {
		t.Errorf("解密结果 = %q", got)
	}
}

// TestCorruptedKeyIsRejected 验证长度异常的主密钥被拒绝。
//
// "凑合用一把长度不对的密钥"会静默降低安全性，而且用户永远不会发现。
func TestCorruptedKeyIsRejected(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if err := store.Put(ctx, masterKeyName, []byte("too short")); err != nil {
		t.Fatal(err)
	}
	_, err := Open(ctx, store)
	if err == nil {
		t.Fatal("长度异常的主密钥应当被拒绝")
	}
	if !strings.Contains(err.Error(), "长度") {
		t.Errorf("错误信息应当说明长度异常，得到: %v", err)
	}
}

func TestOpenWithNilStoreFails(t *testing.T) {
	if _, err := Open(context.Background(), nil); err == nil {
		t.Fatal("密钥存储为 nil 时应当报错，而不是静默地不加密")
	}
}

// newTestStore 返回一个指向临时目录的真实平台密钥存储。
//
// 刻意不做替身：主密钥的生成与持久化正是这一层要验证的东西，
// 换成内存替身就把被测对象换掉了。各平台的后端差异由
// internal/platform 自己的测试覆盖。
func newTestStore(t *testing.T) platform.SecretStore {
	t.Helper()
	return platform.NewSecretStore(t.TempDir())
}
