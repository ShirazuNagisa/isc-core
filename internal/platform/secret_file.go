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

// EnvSecretStore 用来**显式**选择密钥存储后端。
//
// 目前只认一个值：SecretStoreFile。
//
// 它存在的理由有三个，后两个是硬的：
//
//  1. 用户就是想自己管这把密钥（比如把它放进自己的密码管理器）；
//  2. 无人值守的机器（CI、无桌面会话的服务器）上钥匙串要么不可用、
//     要么会被锁住 —— 那种失败发生在**写入时**，而不是启动时；
//  3. **测试不该碰开发机的钥匙串**。实测中 macOS 上跑一次
//     `go test ./...` 会往登录钥匙串里写 60 条条目，而更早的版本
//     直接把真实安装的主密钥覆盖掉（见 D23）。
//
// 放在没有构建标签的文件里：它是**跨平台**的开关，而"某个平台编译不过"
// 正是这类常量最容易造成的回归（四个目标一起 vet 才看得见）。
const EnvSecretStore = "ISC_SECRET_STORE"

// SecretStoreFile 是 EnvSecretStore 的取值：强制使用文件存储。
//
// 它**降低**保护级别（只有文件权限，没有系统密钥库），因此 Describe 必须
// 如实说明 —— "用户以为自己受系统密钥库保护、实际只有一个文件"是比
// 明确不可用更危险的状态。
const SecretStoreFile = "file"

// forcedFileStore 报告调用方是否显式要求文件存储。
func forcedFileStore() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvSecretStore)),
		SecretStoreFile)
}

// NewSecretStore 返回当前平台的密钥存储实现。
//
// dataRoot 是内核的数据根目录 —— 需要落盘保存（兜底实现、以及
// Windows 上 DPAPI 密文的承载文件）的实现会在此目录下建子目录。
func NewSecretStore(dataRoot string) SecretStore {
	return newPlatformSecretStore(dataRoot)
}

// validateKeyName 校验密钥名。
//
// 名称校验是**安全边界**：密钥名最终来自代码而非用户输入，但一旦
// 有人不小心把外部字符串传进来，未校验的名称会变成路径穿越
// （`../../etc/passwd`）。因此这里只接受保守的字符集。
//
// 它对**所有**后端生效，而不只是文件后端：接口的契约必须是"这个名字
// 合不合法"与后端无关。否则同一个调用在 macOS 上成功、在 Linux 上失败，
// 而失败看起来像平台坏了 —— 实测就是如此：
// internal/platform 的 TestSecretStoreRejectsUnsafeNames 在 macOS 上
// 全军覆没，因为 Keychain 接受任意 account 名。
func validateKeyName(name string) error {
	if name == "" {
		return errors.New(i18n.T("platform.keyname_empty"))
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf(i18n.T("platform.keyname_dots"), name)
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf(i18n.T("platform.keyname_badchar"), name, r)
		}
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf(i18n.T("platform.keyname_dot"), name)
	}
	return nil
}

// keyFileName 校验密钥名并把映射为文件路径。
func keyFileName(dir, name string) (string, error) {
	if err := validateKeyName(name); err != nil {
		return "", err
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
