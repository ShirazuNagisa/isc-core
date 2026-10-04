// Package apps 是"用户托管的一个站点/服务"的领域层。
//
// 它把三件事串起来：一份源码目录（presets 给出的构建方案）、一个运行时
// （runtime 负责让它可用）、以及一个被内核守护的进程或内置静态服务器。
// 对外只暴露一个应用对象与它的生命周期操作。
//
// # 为什么状态要落库
//
// 进程句柄活不过内核重启，但"这个应用应该在哪个端口上跑、用哪份构建方案"
// 必须活过去 —— 否则重启后既无法恢复，也无法如实告诉用户"上次它没跑完"。
// 因此 App 是可序列化的，运行期句柄单独放在内存里。
package apps

import (
	"context"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/presets"
	"github.com/ShirazuNagisa/isc-core/internal/runtime"
)

// State 是应用的生命周期状态。
//
// 枚举值与 API 契约里的 App.state 一一对应；前端按它分支，因此**不要**
// 在这里加一个契约里没有的值。
type State string

const (
	StateDraft        State = "draft"
	StateProvisioning State = "provisioning"
	StateInstalling   State = "installing"
	StateBuilding     State = "building"
	StateStarting     State = "starting"
	StateRunning      State = "running"
	StateStopping     State = "stopping"
	StateStopped      State = "stopped"
	StateFailed       State = "failed"
)

// Busy 报告该状态是否处于"正在变化"中。
//
// 处于 busy 的应用不该被再次部署/启动 —— 两个并发的部署会争同一个端口
// 与同一个工作目录，而结果是两者都失败。
func (s State) Busy() bool {
	switch s {
	case StateProvisioning, StateInstalling, StateBuilding, StateStarting, StateStopping:
		return true
	default:
		return false
	}
}

// Active 报告该状态是否意味着"占着资源"。
func (s State) Active() bool {
	return s == StateRunning || s.Busy()
}

// Health 是应用的可用性判断。
//
// 与 State 分开：进程活着但站点返回 500 是"运行中但不健康"，
// 这两件事对用户的含义不同，合成一个字段会丢掉这个区别。
type Health string

const (
	HealthUnknown   Health = "unknown"
	HealthStarting  Health = "starting"
	HealthHealthy   Health = "healthy"
	HealthUnhealthy Health = "unhealthy"
)

// App 是一个被托管的站点。
type App struct {
	ID       string
	Name     string
	PresetID string
	Kind     runtime.Kind
	// SourcePath 是用户给的源码目录（绝对路径）。
	SourcePath string
	// LocalPort 是分配到的本地端口。启动参数里的 {port} 在计划阶段就替换成它。
	LocalPort int
	State     State
	Health    Health
	// HealthDetail 说明健康判断的依据（已本地化）。
	HealthDetail string
	// AutoStart 表示内核启动时是否自动拉起它。
	AutoStart bool
	// MaxRestarts 是崩溃后自动重启的次数上限；0 表示不自动重启。
	MaxRestarts int
	// RestartCount 记录当前这次运行已经重启过几次。
	RestartCount int
	// LastError 是最近一次失败的原因（已本地化或来自底层工具的错误原文）。
	LastError string
	// Plan 是落库的构建与启动方案。
	Plan presets.Plan
	// Domains 是要公开的域名。
	Domains []string
	// RouteID / DDNSTaskID 指向公网侧的配置对象，可为空。
	RouteID    string
	DDNSTaskID string

	CreatedAt time.Time
	UpdatedAt time.Time

	// toolchain 是解析到的运行时，**运行期状态，不落库**。
	//
	// 它由部署/启动时重新解析得到（系统解释器可能被卸载、托管运行时
	// 可能被删除），因此持久化它只会制造一个会过期的副本。
	toolchain runtime.Installed
}

// CreateSpec 是创建一个应用的输入。
type CreateSpec struct {
	Name     string
	PresetID string
	// SourcePath 是源码目录。
	SourcePath string
	// Port 为 0 时自动分配。
	Port int
	// Domains 是要公开的域名。
	Domains []string
	// AutoStart 默认为 true。
	AutoStart *bool
	// MaxRestarts 为 nil 时用默认值。
	MaxRestarts *int
	// CustomRun 供自定义服务器使用（预设给不出命令时由用户提供）。
	CustomRun *presets.Step
}

// Binder 把应用接到公网侧（反向代理规则、证书、DNS 记录）。
//
// 它是接口而不是直接调用：应用领域层不该知道反向代理、ACME 与 DNS 的
// 存在。实现放在 daemon 装配处（那里同时握着这几个管理器）。
//
// 实现必须**幂等**：部署可能被重跑，而重复的代理规则会让同一个域名
// 出现两条互相冲突的转发。
type Binder interface {
	// EnsureRoute 保证某个域名指向本应用的本地端口，返回规则 id。
	EnsureRoute(ctx context.Context, app App, domain string) (string, error)
	// RemoveRoute 撤销某个域名的公网绑定。
	//
	// 带上 app 是为了**只**撤销属于它的规则：同一个域名可能被用户手工
	// 配过一条指向别处的规则，删应用时把它一并删掉是越权。
	RemoveRoute(ctx context.Context, app App, domain string) error

	// EnsureDNS 保证这些域名有动态解析在维护，返回任务 id。
	//
	// 没有可用的 DNS 凭据时返回空 id 与 nil 错误：那不是失败，只是这件事
	// 现在做不了（建议引擎会说明原因）。
	EnsureDNS(ctx context.Context, app App) (string, error)

	// RemoveDNS 撤销某个域名的动态解析。
	//
	// 与 RemoveRoute 同理，只动这个应用相关的部分：任务上可能还有别的
	// 域名在靠它更新。
	RemoveDNS(ctx context.Context, app App, domain string) error
}

// Store 是应用的持久化后端。
//
// 接口定义在这里、实现放在 internal/store：依赖方向是单向的
// （store → apps），与应用领域层的其它实现无关。
type Store interface {
	SaveApp(ctx context.Context, app App) error
	GetApp(ctx context.Context, id string) (App, bool, error)
	ListApps(ctx context.Context) ([]App, error)
	DeleteApp(ctx context.Context, id string) (bool, error)
}

// DefaultMaxRestarts 是崩溃后自动重启的默认次数。
//
// 取 3：一次崩溃可能是偶发（端口刚被释放、依赖服务还没起来），而三次
// 连续失败更像是一个需要人看一眼的问题 —— 继续重试只会刷日志。
const DefaultMaxRestarts = 3

// LogTailDefault / LogTailMax 限制一次取回多少行日志。
const (
	LogTailDefault = 200
	LogTailMax     = 2000
)
