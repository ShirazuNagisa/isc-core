// Package platform 是 ISC 内核中唯一允许出现平台分支的地方。
//
// 设计约束（见 docs/DECISIONS.md D11）：
//
//   - 内核是**一套 Go 代码**，通过 GOOS 条件编译产出各平台二进制，
//     不存在"Windows 内核 / Linux 内核"两份实现；
//   - 平台差异全部收敛在本包的接口与各平台的实现文件中；
//   - 未实现的平台后端必须提供 stub（编译可通过，调用返回 ErrNotImplemented），
//     上层据此降级为"引导模式"（告诉用户该做什么，但不代其动手）；
//   - 本包**禁止引入需要 cgo 的依赖**（见 docs/PLAN.md §0）。
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net"
	"net/netip"
	"strings"
	"time"
)

// ErrNotImplemented 表示当前平台的该后端尚未实现。
//
// 上层应当捕获此错误并降级为引导模式，而不是把它当作致命错误。
var ErrNotImplemented = errors.New(i18n.T("platform.plain_not_implemented"))

// Bundle 聚合当前平台的全部后端实现。
//
// 由 Current 返回；测试中可替换为假实现。
type Bundle struct {
	Firewall       Firewall
	ServiceManager ServiceManager
	IPMonitor      IPMonitor
	SecretStore    SecretStore
	Transport      Transport
	LowPortBinder  LowPortBinder

	// OS 是运行平台的 GOOS 值，用于日志与 /v1/meta。
	OS string
	// Arch 是运行平台的 GOARCH 值。
	Arch string
}

// Capabilities 报告各后端的实现状态，供 /v1/meta 输出。
//
// 下游 GUI 与 CLI 依据它提示用户"此平台功能降级"，
// 而不需要自己去猜 runtime.GOOS。
func (b *Bundle) Capabilities() Capabilities {
	return Capabilities{
		Firewall:       stateOf(b.Firewall),
		ServiceManager: stateOf(b.ServiceManager),
		IPMonitor:      stateOf(b.IPMonitor),
		SecretStore:    stateOf(b.SecretStore),
		Transport:      stateOf(b.Transport),
		LowPortBinder:  stateOf(b.LowPortBinder),
	}
}

// Capabilities 各子系统的能力矩阵。
type Capabilities struct {
	Firewall       ImplState `json:"firewall"`
	ServiceManager ImplState `json:"service_manager"`
	IPMonitor      ImplState `json:"ip_monitor"`
	SecretStore    ImplState `json:"secret_store"`
	Transport      ImplState `json:"transport"`
	LowPortBinder  ImplState `json:"low_port_binder"`
}

// ImplState 描述一个后端的实现状态。
type ImplState struct {
	// Available 为 false 时，上层应降级为引导模式。
	Available bool `json:"available"`
	// Backend 是后端名称，例如 "nftables" / "ufw" / "netsh" / "unsupported"。
	Backend string `json:"backend"`
	// Note 是给用户看的补充说明（已本地化）。
	Note string `json:"note,omitempty"`
}

// describer 是各后端可选实现的接口，用于自报后端名称与可用性。
type describer interface {
	Describe() ImplState
}

func stateOf(v any) ImplState {
	if v == nil {
		return ImplState{Available: false, Backend: "nil", Note: i18n.T("platform.backend_missing")}
	}
	if d, ok := v.(describer); ok {
		return d.Describe()
	}
	return ImplState{Available: true, Backend: "unknown"}
}

// ---------------------------------------------------------------------------
// 防火墙
// ---------------------------------------------------------------------------

// Firewall 是防火墙编排后端。
//
// 实现必须是幂等的：对同一个 Change 重复调用 Apply 不得产生额外副作用。
// 这是"计划 → 预览 → 应用 → 回滚"模型的基础（见 docs/DECISIONS.md D10）。
type Firewall interface {
	// Inspect 读取当前生效的入站规则，只读、无副作用。
	Inspect(ctx context.Context) ([]Rule, error)

	// Plan 计算从当前状态到达期望状态的差异，**不得产生任何副作用**。
	Plan(ctx context.Context, desired []Rule) (Change, error)

	// Apply 应用变更。必须幂等。
	Apply(ctx context.Context, ch Change) error

	// Rollback 回滚变更，必须能恢复到 Apply 之前的状态。
	Rollback(ctx context.Context, ch Change) error

	describer
}

// Protocol 是传输层协议。
type Protocol string

const (
	TCP Protocol = "tcp"
	UDP Protocol = "udp"
)

// Rule 是一条入站放行规则。
type Rule struct {
	// Name 是规则标识，用于幂等判断与回滚定位。
	// 约定格式：isc-<服务名>-<协议>-<端口>，例如 isc-jellyfin-tcp-8096。
	Name string `json:"name"`

	Protocol Protocol  `json:"protocol"`
	Port     PortRange `json:"port"`

	// Source 留空表示任意来源。IPv6 原生场景通常留空。
	Source string `json:"source,omitempty"`

	// Profiles 仅 Windows 有意义：domain / private / public。
	// 留空表示全部配置文件。
	Profiles []string `json:"profiles,omitempty"`

	// Description 是给用户在防火墙界面里看的说明。
	Description string `json:"description,omitempty"`
}

// PortRange 是一个闭区间的端口范围；From == To 表示单个端口。
type PortRange struct {
	From uint16 `json:"from"`
	To   uint16 `json:"to"`
}

// NewPort 构造单个端口的 PortRange。
func NewPort(p uint16) PortRange { return PortRange{From: p, To: p} }

// String 实现 fmt.Stringer。
func (p PortRange) String() string {
	if p.From == p.To {
		return fmt.Sprintf("%d", p.From)
	}
	return fmt.Sprintf("%d-%d", p.From, p.To)
}

// Valid 报告端口范围是否合法。
func (p PortRange) Valid() bool { return p.From > 0 && p.To >= p.From }

// Change 是一次待应用的系统变更。
//
// 它是可序列化的：会被写入 plans 表，供审计与回滚使用。
// Payload 对计划框架是不透明的，只有产生它的后端能解释。
type Change struct {
	// ID 是变更的唯一标识。
	ID string `json:"id"`

	// Platform / Backend 标识产生它的后端，回滚时据此路由。
	Platform string `json:"platform"`
	Backend  string `json:"backend"`

	// Kind 说明变更类别，例如 "firewall.rules"。
	Kind string `json:"kind"`

	// Summary 是一句话摘要，直接展示给用户。
	Summary string `json:"summary"`

	// Diff 是人类可读的差异描述（要改什么、为什么、影响哪些端口）。
	Diff string `json:"diff"`

	// Payload 是后端私有的变更数据。
	Payload json.RawMessage `json:"payload,omitempty"`

	// Reversible 报告该变更是否支持回滚。
	Reversible bool `json:"reversible"`

	// CreatedAt 是计划生成时间。
	CreatedAt time.Time `json:"created_at"`
}

// ---------------------------------------------------------------------------
// 服务管理
// ---------------------------------------------------------------------------

// ServiceManager 负责内核自身的系统服务安装与生命周期。
type ServiceManager interface {
	Install(ctx context.Context, cfg ServiceConfig) error
	Uninstall(ctx context.Context) error
	Status(ctx context.Context) (ServiceStatus, error)
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	describer
}

// ServiceConfig 是安装系统服务所需的参数。
type ServiceConfig struct {
	Name        string
	DisplayName string
	Description string
	// Executable 是服务要启动的可执行文件路径。
	Executable string
	// Arguments 是传给可执行文件的参数（通常为 ["daemon", "run"]）。
	Arguments []string
	// WorkingDirectory 留空表示使用可执行文件所在目录。
	WorkingDirectory string
	// AutoStart 为 true 时开机自启（Windows 使用延迟自启，见 ddns-go 的做法）。
	AutoStart bool
	// RestartOnFailure 为 true 时崩溃自动重启。
	RestartOnFailure bool
}

// ServiceStatus 是服务当前状态。
type ServiceStatus string

const (
	ServiceUnknown ServiceStatus = "unknown"
	ServiceStopped ServiceStatus = "stopped"
	ServiceRunning ServiceStatus = "running"
)

// ---------------------------------------------------------------------------
// 网络地址与前缀监控
// ---------------------------------------------------------------------------

// IPMonitor 监控本机网络地址与 IPv6 委派前缀的变化。
//
// 这是本项目的核心子系统：ISP 重拨后变化的是**前缀**（通常 /64），
// 不是单个地址，因此 Prefixes 是一等公民而非附属信息。
type IPMonitor interface {
	// Snapshot 返回当前全部接口的地址与前缀，只读、无副作用。
	Snapshot(ctx context.Context) ([]InterfaceAddrs, error)

	// Watch 持续推送地址/前缀变化事件，直到 ctx 被取消。
	//
	// 实现可以基于内核通知（Linux netlink）或轮询（Windows），
	// 但**语义必须一致**：只在有效变化时发事件，不做无差别心跳。
	Watch(ctx context.Context) (<-chan AddrEvent, error)

	describer
}

// InterfaceAddrs 是一个网络接口的地址快照。
type InterfaceAddrs struct {
	Name         string       `json:"name"`
	Index        int          `json:"index"`
	HardwareAddr string       `json:"hardware_addr,omitempty"`
	MTU          int          `json:"mtu,omitempty"`
	IPv4         []netip.Addr `json:"ipv4,omitempty"`
	IPv6         []netip.Addr `json:"ipv6,omitempty"`
	// Prefixes 是委派给本接口的 IPv6 前缀。
	// 例如 240e:3b0:1234:5600::/64。这是动态解析真正要跟踪的东西。
	Prefixes []netip.Prefix `json:"prefixes,omitempty"`
	IsUp     bool           `json:"is_up"`
	// IsLoopback 为 true 的接口应当被上层忽略。
	IsLoopback bool `json:"is_loopback"`
	// IsVirtual 标记虚拟/隧道接口（VPN、Docker 网桥等），默认不参与动态解析。
	IsVirtual bool `json:"is_virtual,omitempty"`
}

// GlobalIPv6 返回该接口上的全局单播 IPv6 地址（排除链路本地、回环、ULA）。
func (i InterfaceAddrs) GlobalIPv6() []netip.Addr {
	out := make([]netip.Addr, 0, len(i.IPv6))
	for _, a := range i.IPv6 {
		if IsGlobalIPv6(a) {
			out = append(out, a)
		}
	}
	return out
}

// IsGlobalIPv6 报告地址是否为可用于公网访问的全局单播 IPv6。
//
// 排除：回环、链路本地（fe80::/10）、唯一本地地址（fc00::/7）、
// 组播、以及 IPv4-mapped 地址。
func IsGlobalIPv6(a netip.Addr) bool {
	if !a.Is6() || a.Is4In6() || a.IsLoopback() || a.IsMulticast() || a.IsLinkLocalUnicast() {
		return false
	}
	// fc00::/7 唯一本地地址
	if a.IsPrivate() {
		return false
	}
	// 特殊用途的全局单播网段。
	//
	// `IsGlobalUnicast()` 会在这些网段上返回 true，因为它们确实落在
	// 2000::/3 里 —— 但它们**不能用于接受入站连接**，因此把它们当成
	// "可用于公网访问的地址"会把用户引向一条走不通的路。
	//
	// 这一点是在真机上发现的：Windows 默认启用 Teredo，
	// 于是某个"伪接口"上报出 4 个 2001:0:... 地址，探测显示
	// "找到 4 个可用于公网访问的 IPv6 地址"，而它们一个都用不了。
	if isSpecialPurposeIPv6(a) {
		return false
	}
	return a.IsGlobalUnicast()
}

// specialPurposeIPv6 是不能用于入站连接的特殊用途网段。
var specialPurposeIPv6 = []netip.Prefix{
	// Teredo 隧道：地址由 Teredo 服务器分配，只能用于**出站**穿透。
	// 它出现在 Windows 的"Teredo Tunneling Pseudo-Interface"上，
	// 而那个接口根本不接受入站连接。
	netip.MustParsePrefix("2001:0::/32"),
	// 6to4：把 IPv6 包封装在 IPv4 里，需要公网 IPv4 才能工作，
	// 且已被 RFC 7526 弃用。用它做动态解析只会在地址变化时失效。
	netip.MustParsePrefix("2002::/16"),
	// 文档与示例专用网段，永远不该出现在真实链路上。
	netip.MustParsePrefix("2001:db8::/32"),
	// ORCHIDv2：用于加密生成标识符，不是可路由的地址。
	netip.MustParsePrefix("2001:20::/28"),
}

// isSpecialPurposeIPv6 报告地址是否落在不能用于入站连接的特殊网段里。
func isSpecialPurposeIPv6(a netip.Addr) bool {
	for _, p := range specialPurposeIPv6 {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// AddrEventKind 是地址事件的类别。
type AddrEventKind string

const (
	AddrAdded   AddrEventKind = "added"
	AddrRemoved AddrEventKind = "removed"
	AddrChanged AddrEventKind = "changed"
)

// AddrEvent 是一次地址或前缀变化。
type AddrEvent struct {
	Kind       AddrEventKind `json:"kind"`
	Iface      string        `json:"iface"`
	IfaceIndex int           `json:"iface_index"`
	Addr       netip.Addr    `json:"addr,omitempty"`
	Prefix     netip.Prefix  `json:"prefix,omitempty"`
	At         time.Time     `json:"at"`
}

// IsPrefixEvent 报告该事件是否与 IPv6 前缀相关。
//
// 前缀变化是触发全量 AAAA 重写的信号，优先级高于单个地址变化。
func (e AddrEvent) IsPrefixEvent() bool {
	return e.Prefix.IsValid() && e.Prefix.Addr().Is6() && !e.Prefix.Addr().Is4In6()
}

// String 实现 fmt.Stringer，用于日志。
func (e AddrEvent) String() string {
	var b strings.Builder
	b.WriteString(string(e.Kind))
	b.WriteString(" iface=")
	b.WriteString(e.Iface)
	if e.Prefix.IsValid() {
		b.WriteString(" prefix=")
		b.WriteString(e.Prefix.String())
	}
	if e.Addr.IsValid() {
		b.WriteString(" addr=")
		b.WriteString(e.Addr.String())
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 密钥库
// ---------------------------------------------------------------------------

// SecretStore 提供一个小型、平台原生的命名密钥存储。
//
// 用途只有一个但很关键：保存 ISC 的**主密钥**。所有凭据（DNS 服务商的
// API Key）都用主密钥做 AES-256-GCM 加密后落库，主密钥本身则交给操作
// 系统保护的存储 —— 这样即使数据库文件泄漏，没有主密钥也读不出任何凭据。
//
// 之所以设计成"命名存储"而不是"加解密函数"：各平台的可信存储形态不同，
// 让实现自己决定"东西放哪"比强迫它们把存储细节塞进一个 seal/unseal 接口
// 更自然：
//
//	Windows  DPAPI 保护后写入受保护目录下的文件
//	         （DPAPI 是保护器而非存储，必须配合文件）
//	macOS    Keychain（真正的存储）
//	Linux    Secret Service / D-Bus（真正的存储）
//	兜底      受保护目录下的 0600 文件
//
// **兜底不是失败**，但必须在 Describe 的 Note 里明确告警 ——
// 静默降级会让用户以为自己的密钥受到了操作系统级保护。
type SecretStore interface {
	// Put 保存一段命名密钥。同名已存在时覆盖。
	Put(ctx context.Context, name string, value []byte) error

	// Get 取出命名密钥。不存在时返回 (nil, false, nil) ——
	// "没有"是正常状态（首次启动），不应作为错误。
	Get(ctx context.Context, name string) (value []byte, found bool, err error)

	// Delete 删除命名密钥。不存在时返回 nil。
	Delete(ctx context.Context, name string) error

	describer
}

// ---------------------------------------------------------------------------
// 本地传输
// ---------------------------------------------------------------------------

// Transport 提供仅供本机访问的管理通道。
//
// 优先级：命名管道（Windows）/ Unix socket（类 Unix）> 回环 TCP。
// 回环 TCP 是回退方案，仅在前者不可用时启用。
type Transport interface {
	// Listen 在 endpoint 上建立监听。
	//
	// endpoint 的格式见本包 Endpoint 相关函数：
	//
	//	npipe://./pipe/isc-core            Windows 命名管道
	//	unix:///var/lib/isc/run/isc.sock   Unix 域套接字
	//	tcp://127.0.0.1:0                  回环 TCP（端口 0 表示自动分配）
	Listen(ctx context.Context, endpoint string) (net.Listener, error)

	describer
}

// ---------------------------------------------------------------------------
// 低端口绑定
// ---------------------------------------------------------------------------

// LowPortBinder 报告低端口（<1024）绑定能力。
//
// Linux 上非 root 进程需要 CAP_NET_BIND_SERVICE；Windows 与 macOS 无此限制。
// 上层据此决定是否提示用户提权或改用高位端口。
type LowPortBinder interface {
	// CanBindLowPorts 报告当前进程能否直接绑定 <1024 端口。
	CanBindLowPorts() bool
	describer
}

// ---------------------------------------------------------------------------
// 公共 stub 实现
// ---------------------------------------------------------------------------

// Unsupported 是未实现后端的通用实现。
//
// 各平台在 M0 阶段统一返回它；随里程碑推进逐步替换为真实实现。
type Unsupported struct {
	// Name 是后端名，例如 "firewall"。
	Name string
	// Reason 说明为何不可用（已本地化）。
	Reason string
}

// Describe 实现 describer。
func (u Unsupported) Describe() ImplState {
	reason := u.Reason
	if reason == "" {
		reason = i18n.T("platform.unsupported")
	}
	return ImplState{Available: false, Backend: "unsupported:" + u.Name, Note: reason}
}

// unimplemented 构造一个带上下文的 ErrNotImplemented。
func unimplemented(name string) error {
	return fmt.Errorf("%w: %s", ErrNotImplemented, name)
}
