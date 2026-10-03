//go:build !windows

package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// 本文件实现类 Unix 平台的密钥存储。
//
//	macOS  Keychain（通过 /usr/bin/security）
//	Linux  Secret Service（通过 secret-tool，即 libsecret）
//	兜底    受保护目录下的 0600 文件
//
// 全部通过命令行工具而非 C 库实现，因为本项目禁止 cgo
// （见 docs/PLAN.md §0）—— 链接 Security.framework 或 libsecret
// 都会让三平台交叉编译彻底失去意义。
//
// 密钥值一律 **base64 编码**后再交给外部命令：
//
//   - 主密钥是 32 字节随机值，可能含 NUL 与非法 UTF-8 序列，
//     直接作为 argv 传递会被截断或损坏；
//   - base64 之后既便于作为 argv（macOS），也便于走 stdin（Linux）。

// serviceName 是 Keychain / Secret Service 中的服务标识。
const serviceName = "isc-core"

// newPlatformSecretStore 返回类 Unix 平台的密钥存储。
func newPlatformSecretStore(root string) SecretStore {
	dir := root + "/" + secretsDirName
	scope := keyScope(root)

	// 显式指定优先于任何自动判断。
	if forcedFileStore() {
		return newFileSecretStore(dir, i18n.T("platform.forced_file"))
	}

	switch runtime.GOOS {
	case "darwin":
		if bin, err := exec.LookPath("security"); err == nil {
			return &cliSecretStore{dir: dir, bin: bin, kind: kindKeychain, scope: scope}
		}
		return newFileSecretStore(dir, i18n.T("platform.nokeychain"))

	case "linux", "freebsd", "openbsd", "netbsd":
		if !hasSecretServiceSession() {
			return newFileSecretStore(dir,
				i18n.T("platform.nosecretservice"))
		}
		if bin, err := exec.LookPath("secret-tool"); err == nil {
			return &cliSecretStore{dir: dir, bin: bin, kind: kindSecretTool, scope: scope}
		}
		return newFileSecretStore(dir, i18n.T("platform.nosecrettool"))

	default:
		return newFileSecretStore(dir, i18n.T("platform.nokeystore"))
	}
}

// keyScope 返回数据目录的短指纹，用来把密钥库里的条目与数据目录绑定。
func keyScope(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:4])
}

// scopedKeyName 把密钥名与数据目录绑定。
//
// # 为什么必须绑定
//
// Keychain / Secret Service 的条目名是**全局**的，而内核的其余一切都以
// 数据目录为界。这个不对称有两个后果，第二个是严重的那一个：
//
//  1. 两个数据目录会共用一把主密钥 —— 复制一份数据目录就能解密另一份的
//     凭据，而"密钥属于哪个安装"这件事变得没有定义；
//  2. **测试会覆盖真实安装的主密钥**。测试各自建临时数据目录，却都写
//     同一个全局条目：实测中 `go test ./...` 把用户登录钥匙串里那条
//     `isc-core/master` 写成了 9 字节的测试值（`TestCorruptedKeyIsRejected`
//     故意写的"too short"），于是**所有**以临时目录启动内核的测试集体报
//     "主密钥长度异常"，而真实安装的凭据会因此永久解不开。
//
// 绑定之后，每个数据目录各自一条，前面两件事都不再成立。
func scopedKeyName(name, scope string) string {
	if scope == "" {
		return name
	}
	return name + "@" + scope
}

// legacyKeyName 返回绑定之前用过的条目名。
//
// 读的时候必须回退到它：否则升级之后内核找不到主密钥，会**生成一把新的**，
// 用户已有的全部凭据当场变成解不开的密文。回退只影响读 —— 写永远写到
// 绑定后的名字上，因此不会再有新的"全局条目"产生。
func legacyKeyName(name string) string { return name }

// hasSecretServiceSession 用廉价的环境判断是否存在可用的 Secret Service。
//
// 为什么需要它：secret-tool 在无 D-Bus 会话时会失败或长时间等待。
// 与其在每次读写时才失败，不如在启动时就通过环境判断并直接走兜底，
// 这样 Describe 报告的可用性也是准确的。
func hasSecretServiceSession() bool {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}
	// systemd 用户会话的默认总线路径。
	if uid := os.Getuid(); uid >= 0 {
		if _, err := os.Stat(fmt.Sprintf("/run/user/%d/bus", uid)); err == nil {
			return true
		}
	}
	return false
}

type cliKind int

const (
	kindKeychain cliKind = iota
	kindSecretTool
)

// cliSecretStore 通过外部命令访问系统密钥库。
type cliSecretStore struct {
	dir  string
	bin  string
	kind cliKind
	// scope 把本实例的条目与数据目录绑定（见 scopedKeyName）。
	scope string
	// fellBack 记录是否曾因密钥库不可用而降级到文件存储。
	fellBack bool
}

// Describe 实现 describer。
func (s *cliSecretStore) Describe() ImplState {
	backend := "macos-keychain"
	if s.kind == kindSecretTool {
		backend = "linux-secret-service"
	}
	note := i18n.T("platform.keystore_note")
	if s.fellBack {
		note = i18n.T("platform.keystore_fallback")
		backend += "+file-fallback"
	}
	return ImplState{Available: true, Backend: backend, Note: note}
}

// Put 写入密钥。
//
// 永远写到**绑定后的**条目名上：绑定之前的全局条目只会被读（见 Get），
// 因此不会再有新的安装往那上面写。
func (s *cliSecretStore) Put(ctx context.Context, name string, value []byte) error {
	// 名称校验与文件后端共用同一条规则：契约不能因为后端不同而不同
	//（Keychain 与 secret-tool 都接受任意名字，放过去就意味着
	// "同一个调用在 macOS 上成功、在 Linux 上失败"）。
	if err := validateKeyName(name); err != nil {
		return err
	}
	return s.putAccount(ctx, scopedKeyName(name, s.scope), value)
}

// putAccount 按**完整条目名**写入。
func (s *cliSecretStore) putAccount(ctx context.Context, account string, value []byte) error {
	encoded := base64.StdEncoding.EncodeToString(value)

	var cmd *exec.Cmd
	switch s.kind {
	case kindKeychain:
		// -U 表示已存在则更新。
		//
		// ⚠️ 已知局限：密码作为 argv 传递，在极短的窗口内可能被同机的
		// 其它进程通过 ps 观察到。工具本身不提供从 stdin 读取密码的
		// 可靠方式，而链接 Security.framework 需要 cgo（本项目禁止）。
		// 该窗口只在**首次创建主密钥**时出现一次，且目标场景是单用户机器。
		// 待办：等 macOS 侧可验证时改为 stdin 或原生实现。
		cmd = exec.CommandContext(ctx, s.bin,
			"add-generic-password", "-U",
			"-a", account, "-s", serviceName, "-w", encoded)
	case kindSecretTool:
		// secret-tool 从 stdin 读取密钥值 —— 不经 argv，无暴露窗口。
		cmd = exec.CommandContext(ctx, s.bin,
			"store", "--label=ISC "+account, "isc-name", account)
		cmd.Stdin = strings.NewReader(encoded)
	}

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf(i18n.T("platform.keystore_write"),
			s.bin, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Get 读取密钥。
func (s *cliSecretStore) Get(ctx context.Context, name string) ([]byte, bool, error) {
	if err := validateKeyName(name); err != nil {
		return nil, false, err
	}

	value, found, err := s.getAccount(ctx, scopedKeyName(name, s.scope))
	if err != nil || found {
		return value, found, err
	}

	// 回退到绑定之前的名字：升级上来的安装，主密钥还在那条全局条目里。
	// 不读它会直接导致"生成新主密钥 → 既有凭据全部作废"。
	value, found, err = s.getAccount(ctx, legacyKeyName(name))
	if err != nil || !found {
		return value, found, err
	}

	// 顺手搬一次家（尽力而为）：搬不动也不影响这次读取 —— 值已经在手上。
	// 不搬的话，这台机器会永远停在"读旧条目"的状态上，绑定的意义也就
	// 只剩下一半。
	if putErr := s.putAccount(ctx, scopedKeyName(name, s.scope), value); putErr == nil {
		_ = s.deleteAccount(ctx, legacyKeyName(name))
	}
	return value, true, nil
}

// getAccount 按**完整条目名**读取，不做任何绑定或回退。
func (s *cliSecretStore) getAccount(ctx context.Context, account string) ([]byte, bool, error) {
	var cmd *exec.Cmd
	switch s.kind {
	case kindKeychain:
		cmd = exec.CommandContext(ctx, s.bin,
			"find-generic-password", "-a", account, "-s", serviceName, "-w")
	case kindSecretTool:
		cmd = exec.CommandContext(ctx, s.bin, "lookup", "isc-name", account)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// 两个工具在"找不到条目"时都以非零码退出且不输出。
		// 把它当作"没有"而不是错误 —— 首次启动时这是正常路径，
		// 报错会让调用方以为系统坏了。
		if stdout.Len() == 0 {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf(i18n.T("platform.keystore_read"),
			s.bin, err, strings.TrimSpace(stderr.String()))
	}

	encoded := strings.TrimSpace(stdout.String())
	if encoded == "" {
		return nil, false, nil
	}
	value, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false, fmt.Errorf(i18n.T("platform.keystore_badb64"), err)
	}
	return value, true, nil
}

// Delete 删除密钥。
//
// 两条都删：绑定后的，以及绑定之前的全局条目。只删前者的话，下一次读取
// 会从后者回退捡回来 —— 用户看到的将是"删了又回来了"。
func (s *cliSecretStore) Delete(ctx context.Context, name string) error {
	if err := validateKeyName(name); err != nil {
		return err
	}
	if err := s.deleteAccount(ctx, scopedKeyName(name, s.scope)); err != nil {
		return err
	}
	return s.deleteAccount(ctx, legacyKeyName(name))
}

// deleteAccount 按**完整条目名**删除。
func (s *cliSecretStore) deleteAccount(ctx context.Context, account string) error {
	var cmd *exec.Cmd
	switch s.kind {
	case kindKeychain:
		cmd = exec.CommandContext(ctx, s.bin,
			"delete-generic-password", "-a", account, "-s", serviceName)
	case kindSecretTool:
		cmd = exec.CommandContext(ctx, s.bin, "clear", "isc-name", account)
	}
	// 条目不存在时同样返回非零码，这不是错误。
	_ = cmd.Run()
	return nil
}

// secretsDirName 是兜底密钥文件所在的子目录名。
const secretsDirName = "secrets"
