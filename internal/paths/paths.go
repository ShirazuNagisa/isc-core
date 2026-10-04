// Package paths 解析内核的数据目录、配置目录与运行时文件位置。
//
// 目录约定见 docs/DECISIONS.md D20：
//
//	Windows  %ProgramData%\ISC\
//	Linux    /etc/isc（配置） + /var/lib/isc（数据）
//	macOS    /Library/Application Support/ISC/
//
// 开发与测试可用 ISC_DATA_DIR 环境变量覆盖数据根目录，
// 这样无需管理员权限即可在任意位置跑起来。
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// EnvDataDir 是覆盖数据根目录的环境变量名。
const EnvDataDir = "ISC_DATA_DIR"

// EnvConfigDir 是覆盖配置目录的环境变量名。
const EnvConfigDir = "ISC_CONFIG_DIR"

// Paths 是内核使用的一组目录与文件位置。
type Paths struct {
	// root 是数据根目录（数据库、运行时文件、日志）。
	root string
	// config 是配置目录。Linux 上它与 root 不同（/etc/isc vs /var/lib/isc）。
	config string
}

// Resolve 解析当前环境的目录布局。
//
// 解析顺序：环境变量覆盖 → 平台默认值。
// 本函数只做路径推导，**不创建任何目录**；创建由 EnsureDirs 负责。
func Resolve() (Paths, error) {
	root := os.Getenv(EnvDataDir)
	cfg := os.Getenv(EnvConfigDir)

	if root == "" {
		def, err := defaultRoots()
		if err != nil {
			return Paths{}, err
		}
		root = def.data
		if cfg == "" {
			cfg = def.config
		}
	}
	if cfg == "" {
		cfg = root
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Paths{}, fmt.Errorf(i18n.T("paths.err.data_dir"), root, err)
	}
	absCfg, err := filepath.Abs(cfg)
	if err != nil {
		return Paths{}, fmt.Errorf(i18n.T("paths.err.config_dir"), cfg, err)
	}
	return Paths{root: absRoot, config: absCfg}, nil
}

// Root 返回数据根目录。
func (p Paths) Root() string { return p.root }

// Config 返回配置目录。
func (p Paths) Config() string { return p.config }

// RunDir 返回运行时文件目录。
//
// 该目录包含 runtime.json（内含访问令牌），其访问权限必须收紧至
// 仅本机用户可读。见 docs/DECISIONS.md D09。
func (p Paths) RunDir() string { return filepath.Join(p.root, "run") }

// RuntimeFile 返回 runtime.json 的完整路径。
func (p Paths) RuntimeFile() string { return filepath.Join(p.RunDir(), "runtime.json") }

// DBFile 返回 SQLite 数据库文件路径。
func (p Paths) DBFile() string { return filepath.Join(p.root, "isc.db") }

// LogDir 返回日志目录。
func (p Paths) LogDir() string { return filepath.Join(p.root, "logs") }

// SecretsDir 返回密钥存储目录。
//
// 内容形态由平台决定（Windows 是 DPAPI 密文文件，macOS/Linux 通常
// 只有系统密钥库，目录可能为空）。纳入 EnsureDirs 是为了让它与
// run/ 目录一样受到收紧后的访问权限保护。
func (p Paths) SecretsDir() string { return filepath.Join(p.root, "secrets") }

// RemoteDir 返回远程管理面的私有目录。
//
// 里面放的是自签证书与私钥（见 internal/remote）。它**必须**与 run/
// 分开：run/ 里的 runtime.json 装的是本地访问令牌，而远程面的凭据
// 一旦与它混在一起，任何读到其中一份的人就同时拿到了两套权限。
//
// 目录本身由 EnsureDirs 创建为 0700，私钥文件另有 0600。
func (p Paths) RemoteDir() string { return filepath.Join(p.root, "remote") }

// ACMEDir 存放 ACME 账户私钥。
//
// 它不是可以随时重建的缓存：私钥就是账户的身份，而 DNS-01 的挑战摘要
// 由这个身份决定。丢掉它等于换一个身份 —— 上一轮写进用户 DNS 的 TXT
// 会与这一轮的订单对不上，同时 CA 的"新注册账户"配额会被烧光。
func (p Paths) ACMEDir() string { return filepath.Join(p.root, "acme") }

// ConfigFile 返回配置文件路径。
func (p Paths) ConfigFile() string { return filepath.Join(p.config, "isc.yaml") }

// CacheDir 返回下载中转目录。
//
// 里面放的是**尚未校验完成**的下载产物（运行时归档）。它与 run/、secrets/
// 一样按 0700 收紧：内容是外部来源的字节，在通过 SHA-256 校验之前不应
// 被同机其它用户读取或替换。校验通过后会被移出本目录。
func (p Paths) CacheDir() string { return filepath.Join(p.root, "cache") }

// RuntimesDir 返回托管运行时目录。
//
// 结构为 <root>/runtimes/<kind>/<version>/，每个版本目录内含
// .isc-runtime.json 标记文件（记录来源 URL 与摘要）。
//
// 内容是**不可信输入**：整份解释器/工具链来自网络。因此该目录同样
// 按 0700 收紧，解压时必须拒绝路径穿越与逃逸符号链接。
func (p Paths) RuntimesDir() string { return filepath.Join(p.root, "runtimes") }

// String 实现 fmt.Stringer，用于日志。
func (p Paths) String() string {
	return fmt.Sprintf("data=%s config=%s", p.root, p.config)
}

// EnsureDirs 创建全部必需目录，并尽力收紧运行时目录的访问权限。
//
// 权限收紧失败不会导致启动失败（某些文件系统不支持），但会返回一个
// 非致命的告警字符串供调用方记录 —— 静默降级是不可接受的。
func (p Paths) EnsureDirs() (warnings []string, err error) {
	for _, dir := range []string{p.root, p.config, p.RunDir(), p.LogDir(), p.SecretsDir(), p.CacheDir(), p.RuntimesDir(), p.RemoteDir(), p.ACMEDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return warnings, fmt.Errorf(i18n.T("paths.err.mkdir"), dir, err)
		}
	}
	// run/ 与 secrets/ 含机密（访问令牌、主密钥密文）；cache/ 与 runtimes/
	// 含外部来源的字节；remote/ 含自签证书的私钥。五者都必须与数据根目录
	// 一样收紧到显式白名单。
	for _, dir := range []string{p.RunDir(), p.SecretsDir(), p.CacheDir(), p.RuntimesDir(), p.RemoteDir(), p.ACMEDir()} {
		if w := tightenDir(dir); w != "" {
			warnings = append(warnings, w)
		}
	}
	return warnings, nil
}

// unsupportedPlatformError 构造平台不支持的错误。
func unsupportedPlatformError() error {
	lang := i18n.T("paths.not_supported", runtime.GOOS)
	return fmt.Errorf("paths: %s (%s)", lang, runtime.GOOS)
}
