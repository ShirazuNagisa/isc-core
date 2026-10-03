package platform

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/testsupport"
)

// TestMain 让整个测试二进制默认不碰开发机的系统钥匙串。
//
// 需要真实验证钥匙串后端的少数几条测试会自己构造后端
// （见 newCLIStoreForTest），它们自己清理。理由见 testsupport.IsolateSecretStore
// 与 docs/DECISIONS.md D23。
func TestMain(m *testing.M) { os.Exit(testsupport.IsolateSecretStore(m)) }

// TestSecretStoreEnvIsPinned 钉住环境变量名与取值。
//
// testsupport 里用的是**字面量**而不是这两个常量：它被本包的测试文件引用，
// 而引用本包会形成测试里的导入环。因此这里钉一次，改了一边就会失败 ——
// 否则"测试不碰钥匙串"这条隔离会静默失效（testsupport 设了一个没人认的
// 变量，测试照样往钥匙串里写）。
func TestSecretStoreEnvIsPinned(t *testing.T) {
	t.Parallel()

	if EnvSecretStore != "ISC_SECRET_STORE" {
		t.Errorf("EnvSecretStore = %q，testsupport 里写的是 %q —— "+
			"两者必须一致", EnvSecretStore, "ISC_SECRET_STORE")
	}
	if SecretStoreFile != "file" {
		t.Errorf("SecretStoreFile = %q，testsupport 里写的是 %q",
			SecretStoreFile, "file")
	}
}

// TestForcedFileStore 钉住 ISC_SECRET_STORE=file 的行为。
//
// 它的用途是让测试与无人值守的机器不去依赖钥匙串（那种依赖的失败发生在
// **写入时**，而不是启动时）。这里同时断言 Describe 会如实说明降级 ——
// "用户以为自己受系统密钥库保护、实际只有一个文件"是比不可用更危险的状态。
func TestForcedFileStore(t *testing.T) {
	// 不并行：t.Setenv 不允许与并行测试共存。
	t.Setenv(EnvSecretStore, SecretStoreFile)

	dir := t.TempDir()
	store := NewSecretStore(dir)

	state := store.Describe()
	if state.Backend != "file" {
		t.Errorf("Backend = %q，期望 file", state.Backend)
	}
	if state.Note == "" {
		t.Error("降级到文件存储时必须给出说明（Describe 的 Note 不能为空）")
	}

	// 它必须真的能读写，而不是只报个 backend 名字。
	ctx := context.Background()
	if err := store.Put(ctx, "probe", []byte("v")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	got, found, err := store.Get(ctx, "probe")
	if err != nil || !found || string(got) != "v" {
		t.Fatalf("读回失败: got=%q found=%v err=%v", got, found, err)
	}

	// 落点必须在数据目录里 —— 否则"强制文件存储"就只是换了个说法。
	// 文件名带 .key 后缀（见 fileSecretStore.path）。
	if _, err := os.Stat(filepath.Join(dir, secretsDirName, "probe.key")); err != nil {
		t.Errorf("密钥没有落在数据目录的 %s 下: %v", secretsDirName, err)
	}

	// 大小写与空白不该改变行为（环境变量常常是手工敲的）。
	t.Setenv(EnvSecretStore, "  FILE  ")
	if got := NewSecretStore(dir).Describe().Backend; got != "file" {
		t.Errorf("大小写/空白不同的取值应当同样生效，得到 Backend=%q", got)
	}
}
