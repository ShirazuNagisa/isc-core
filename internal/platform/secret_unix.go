//go:build !windows

package platform

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"os"
	"os/exec"
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

	switch runtime.GOOS {
	case "darwin":
		if bin, err := exec.LookPath("security"); err == nil {
			return &cliSecretStore{dir: dir, bin: bin, kind: kindKeychain}
		}
		return newFileSecretStore(dir, i18n.T("platform.nokeychain"))

	case "linux", "freebsd", "openbsd", "netbsd":
		if !hasSecretServiceSession() {
			return newFileSecretStore(dir,
				i18n.T("platform.nosecretservice"))
		}
		if bin, err := exec.LookPath("secret-tool"); err == nil {
			return &cliSecretStore{dir: dir, bin: bin, kind: kindSecretTool}
		}
		return newFileSecretStore(dir, i18n.T("platform.nosecrettool"))

	default:
		return newFileSecretStore(dir, i18n.T("platform.nokeystore"))
	}
}

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
func (s *cliSecretStore) Put(ctx context.Context, name string, value []byte) error {
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
			"-a", name, "-s", serviceName, "-w", encoded)
	case kindSecretTool:
		// secret-tool 从 stdin 读取密钥值 —— 不经 argv，无暴露窗口。
		cmd = exec.CommandContext(ctx, s.bin,
			"store", "--label=ISC "+name, "isc-name", name)
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
	var cmd *exec.Cmd
	switch s.kind {
	case kindKeychain:
		cmd = exec.CommandContext(ctx, s.bin,
			"find-generic-password", "-a", name, "-s", serviceName, "-w")
	case kindSecretTool:
		cmd = exec.CommandContext(ctx, s.bin, "lookup", "isc-name", name)
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
func (s *cliSecretStore) Delete(ctx context.Context, name string) error {
	var cmd *exec.Cmd
	switch s.kind {
	case kindKeychain:
		cmd = exec.CommandContext(ctx, s.bin,
			"delete-generic-password", "-a", name, "-s", serviceName)
	case kindSecretTool:
		cmd = exec.CommandContext(ctx, s.bin, "clear", "isc-name", name)
	}
	// 条目不存在时同样返回非零码，这不是错误。
	_ = cmd.Run()
	return nil
}

// secretsDirName 是兜底密钥文件所在的子目录名。
const secretsDirName = "secrets"
