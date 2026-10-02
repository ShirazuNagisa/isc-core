package platform

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"os"
	"path/filepath"
	"strings"
)

// 本文件提供各平台共用的兜底密钥存储：把密钥以 0600 权限写在受保护目录下。
//
// 它是**兜底而非首选**。使用它意味着没有可用的操作系统密钥库
// （例如无桌面会话的 Linux 服务器上没有 Secret Service），
// 此时密钥的保护级别退化为"文件系统权限"。
//
// Describe 必须如实报告这一点，绝不能静默降级 —— 用户以为自己受到了
// 操作系统级保护、实际却只有一个文件，是比"明确不可用"更危险的状态。

// NewSecretStore 返回当前平台的密钥存储实现。
//
// dataRoot 是内核的数据根目录 —— 需要落盘保存（兜底实现、以及
// Windows 上 DPAPI 密文的承载文件）的实现会在此目录下建子目录。
func NewSecretStore(dataRoot string) SecretStore {
	return newPlatformSecretStore(dataRoot)
}

// keyFileName 校验密钥名并把映射为文件路径。
//
// 名称校验是**安全边界**：密钥名最终来自代码而非用户输入，但一旦
// 有人不小心把外部字符串传进来，未校验的名称会变成路径穿越
// （`../../etc/passwd`）。因此这里只接受保守的字符集。
func keyFileName(dir, name string) (string, error) {
	if name == "" {
		return "", errors.New(i18n.T("platform.keyname_empty"))
	}
	if strings.Contains(name, "..") {
		return "", fmt.Errorf(i18n.T("platform.keyname_dots"), name)
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return "", fmt.Errorf(i18n.T("platform.keyname_badchar"), name, r)
		}
	}
	if strings.HasPrefix(name, ".") {
		return "", fmt.Errorf(i18n.T("platform.keyname_dot"), name)
	}
	return filepath.Join(dir, name), nil
}

// fileSecretStore 是基于文件的兜底实现。
type fileSecretStore struct {
	dir    string
	reason string
}

// newFileSecretStore 构造兜底实现。
//
// reason 说明为何回退，会出现在 Describe 的 Note 里。
func newFileSecretStore(dir, reason string) *fileSecretStore {
	return &fileSecretStore{dir: dir, reason: reason}
}

// Describe 实现 describer。
func (s *fileSecretStore) Describe() ImplState {
	note := i18n.T("platform.file_store_note", s.reason)
	return ImplState{Available: true, Backend: "file", Note: note}
}

// Put 写入密钥。
func (s *fileSecretStore) Put(_ context.Context, name string, value []byte) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf(i18n.T("platform.mkdir_failed"), err)
	}

	// 先写临时文件再重命名：中途失败时不会留下一个被截断的密钥文件，
	// 那会导致主密钥丢失、所有已加密的凭据永久无法解密。
	tmp, err := os.CreateTemp(s.dir, ".key-*")
	if err != nil {
		return fmt.Errorf(i18n.T("platform.tmp_failed"), err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // 重命名成功后此调用无副作用

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf(i18n.T("platform.chmod_failed"), err)
	}
	if _, err := tmp.Write(value); err != nil {
		_ = tmp.Close()
		return fmt.Errorf(i18n.T("platform.write_failed"), err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf(i18n.T("platform.sync_failed"), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf(i18n.T("platform.close_failed"), err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf(i18n.T("platform.replace_failed"), err)
	}
	return nil
}

// Get 读取密钥。
func (s *fileSecretStore) Get(_ context.Context, name string) ([]byte, bool, error) {
	path, err := s.path(name)
	if err != nil {
		return nil, false, err
	}
	value, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf(i18n.T("platform.read_failed"), err)
	}
	return value, true, nil
}

// Delete 删除密钥。
func (s *fileSecretStore) Delete(_ context.Context, name string) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf(i18n.T("platform.delete_failed"), err)
	}
	return nil
}

// path 把密钥名映射为文件路径。
func (s *fileSecretStore) path(name string) (string, error) {
	p, err := keyFileName(s.dir, name)
	if err != nil {
		return "", err
	}
	return p + ".key", nil
}
