//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 本文件实现 Windows 的密钥存储：DPAPI 保护 + 文件承载。
//
// 为什么需要文件：DPAPI（CryptProtectData）是**保护器**而不是存储 ——
// 它把明文变成只有同一用户能解开的密文，但不负责保存。因此组合方式是
// "DPAPI 保护后写入受保护目录下的文件"。
//
// 为什么这仍然是好方案：DPAPI 的密文与用户的登录凭据绑定，
// 把文件拷到另一台机器或另一个用户下都解不开。
// 内核以服务身份（LocalSystem）运行时，只有 LocalSystem 能解开 ——
// 这正是我们想要的（凭据属于服务，不属于任何交互用户）。

// secretsDirName 是密钥文件所在的子目录名。
const secretsDirName = "secrets"

// dpapiSecretStore 是基于 DPAPI 的实现。
type dpapiSecretStore struct {
	dir string
}

// newPlatformSecretStore 返回 Windows 的密钥存储。
func newPlatformSecretStore(root string) SecretStore {
	return &dpapiSecretStore{dir: filepath.Join(root, secretsDirName)}
}

// Describe 实现 describer。
func (s *dpapiSecretStore) Describe() ImplState {
	return ImplState{
		Available: true,
		Backend:   "windows-dpapi",
		Note: "主密钥由 DPAPI 保护（与运行账户绑定）；" +
			"加密后的密钥文件位于数据目录，拷到其它机器或账户下无法解开",
	}
}

// Put 用 DPAPI 保护后写入文件。
func (s *dpapiSecretStore) Put(_ context.Context, name string, value []byte) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("platform: 创建密钥目录失败: %w", err)
	}

	sealed, err := dpapiProtect(value)
	if err != nil {
		return err
	}

	// 先写临时文件再重命名，避免中途失败留下截断的密钥文件。
	tmp, err := os.CreateTemp(s.dir, ".key-*")
	if err != nil {
		return fmt.Errorf("platform: 创建密钥临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(sealed); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("platform: 写入密钥失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("platform: 密钥落盘失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("platform: 关闭密钥临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("platform: 替换密钥文件失败: %w", err)
	}
	return nil
}

// Get 读取文件并用 DPAPI 解开。
func (s *dpapiSecretStore) Get(_ context.Context, name string) ([]byte, bool, error) {
	path, err := s.path(name)
	if err != nil {
		return nil, false, err
	}
	sealed, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("platform: 读取密钥失败: %w", err)
	}
	value, err := dpapiUnprotect(sealed)
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

// Delete 删除密钥文件。
func (s *dpapiSecretStore) Delete(_ context.Context, name string) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("platform: 删除密钥失败: %w", err)
	}
	return nil
}

func (s *dpapiSecretStore) path(name string) (string, error) {
	p, err := keyFileName(s.dir, name)
	if err != nil {
		return "", err
	}
	return p + ".dpapi", nil
}

// ---------------------------------------------------------------------------
// DPAPI
// ---------------------------------------------------------------------------

// dpapiProtect 用当前用户的凭据加密数据。
//
// 不设置 optionalEntropy：加了熵意味着还要额外保存熵，
// 而熵本身又需要一个安全的地方存 —— 收益为零。
func dpapiProtect(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("platform: 待保护的密钥为空")
	}
	in := windows.DataBlob{Size: uint32(len(plaintext)), Data: &plaintext[0]}
	var out windows.DataBlob

	err := windows.CryptProtectData(
		&in,
		nil, // 描述文本，仅用于界面提示
		nil, // optionalEntropy
		0,   // reserved
		nil, // promptStruct
		// UI_FORBIDDEN 是必须的：内核以服务身份运行，没有交互桌面。
		// 不加这个标志，DPAPI 在需要提示时会直接失败或挂起。
		windows.CRYPTPROTECT_UI_FORBIDDEN,
		&out,
	)
	if err != nil {
		return nil, fmt.Errorf("platform: DPAPI 加密失败: %w", err)
	}
	defer func() {
		_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) //nolint:govet // Windows API 要求
	}()

	return blobBytes(&out), nil
}

// dpapiUnprotect 解开由 dpapiProtect 产生的密文。
func dpapiUnprotect(sealed []byte) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, errors.New("platform: 待解密的密钥为空")
	}
	in := windows.DataBlob{Size: uint32(len(sealed)), Data: &sealed[0]}
	var out windows.DataBlob

	err := windows.CryptUnprotectData(
		&in,
		nil, // 不关心描述文本
		nil, // optionalEntropy
		0,   // reserved
		nil, // promptStruct
		windows.CRYPTPROTECT_UI_FORBIDDEN,
		&out,
	)
	if err != nil {
		// 这里最常见的失败原因是"换了账户或换了机器"——
		// 错误信息必须点明这一点，否则用户只会看到一个语义不明的
		// "参数错误"，然后完全不知道该怎么办。
		return nil, fmt.Errorf(
			"platform: DPAPI 解密失败（主密钥可能由其它账户或其它机器加密，"+
				"无法在当前账户下解开；请删除密钥文件后重新录入凭据）: %w", err)
	}
	defer func() {
		_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) //nolint:govet // Windows API 要求
	}()

	return blobBytes(&out), nil
}

// blobBytes 把 Windows 返回的数据块拷贝成 Go 切片。
//
// 必须拷贝：数据块由 LocalAlloc 分配，调用方紧接着就要 LocalFree，
// 直接引用会变成悬垂指针。
func blobBytes(b *windows.DataBlob) []byte {
	if b == nil || b.Data == nil || b.Size == 0 {
		return nil
	}
	src := unsafe.Slice(b.Data, b.Size)
	out := make([]byte, len(src))
	copy(out, src)
	return out
}
