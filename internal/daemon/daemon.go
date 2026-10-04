// Package daemon 组装并运行 ISC 内核守护进程。
//
// 启动顺序见 docs/ARCHITECTURE.md §8.1。M0 阶段只走到"建立本地通道并写出
// runtime.json"，数据库、密钥库、调度器等随里程碑接入。
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/acme"
	"github.com/ShirazuNagisa/isc-core/internal/api"
	appsvc "github.com/ShirazuNagisa/isc-core/internal/apps"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/change"
	"github.com/ShirazuNagisa/isc-core/internal/configio"
	"github.com/ShirazuNagisa/isc-core/internal/credential"
	"github.com/ShirazuNagisa/isc-core/internal/ddns"
	"github.com/ShirazuNagisa/isc-core/internal/dns"
	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/job"
	"github.com/ShirazuNagisa/isc-core/internal/logx"
	"github.com/ShirazuNagisa/isc-core/internal/metrics"
	"github.com/ShirazuNagisa/isc-core/internal/notify"
	"github.com/ShirazuNagisa/isc-core/internal/paths"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
	"github.com/ShirazuNagisa/isc-core/internal/provider"
	"github.com/ShirazuNagisa/isc-core/internal/proxy"
	"github.com/ShirazuNagisa/isc-core/internal/reach"
	"github.com/ShirazuNagisa/isc-core/internal/remote"
	hosting "github.com/ShirazuNagisa/isc-core/internal/runtime"
	"github.com/ShirazuNagisa/isc-core/internal/runtimeinfo"
	"github.com/ShirazuNagisa/isc-core/internal/secret"
	"github.com/ShirazuNagisa/isc-core/internal/settings"
	"github.com/ShirazuNagisa/isc-core/internal/store"
	"github.com/ShirazuNagisa/isc-core/internal/sysproxy"
	"github.com/ShirazuNagisa/isc-core/internal/verify"
	"github.com/ShirazuNagisa/isc-core/internal/version"
)

// tokenBytes 是访问令牌的随机字节数。
//
// 32 字节 = 256 位，远超暴力枚举的可能；base64url 编码后 43 个字符。
const tokenBytes = 32

// defaultLoopbackAddr 是回环监听地址。
//
// 端口 0 让内核自动挑选空闲端口 —— 固定端口会与用户机器上其它程序冲突，
// 而客户端本来就是通过 runtime.json 发现端口的，不需要固定值。
//
// 显式绑定 127.0.0.1 而不是 localhost 或 :0：后者可能同时绑定到
// 0.0.0.0 或 ::，把管理面暴露到局域网。
const defaultLoopbackAddr = "127.0.0.1:0"

// shutdownTimeout 是优雅关闭的总时限。
const shutdownTimeout = 10 * time.Second

// Options 是守护进程的启动参数。
type Options struct {
	// Paths 是数据目录布局。
	Paths paths.Paths

	// LogHandler 是日志处理器。
	//
	// 由调用方（CLI）提前创建，这样进程最早期的日志也能被捕获；
	// 守护进程会在事件总线就绪后把总线接上去，使日志同时进入事件流。
	LogHandler *logx.BusHandler

	// Lang 是用户界面语言。
	//
	// **只在命令行显式指定时才填**：留空表示沿用已保存的设置。
	// 若这里总是填一个默认值，用户通过接口改成 en 之后，每次重启
	// 都会被重新改回 zh-CN。
	Lang i18n.Lang

	// LoopbackAddr 是回环监听地址；为空时用 defaultLoopbackAddr。
	LoopbackAddr string

	// DisableLoopback 关闭回环监听，只保留命名管道 / Unix 套接字。
	//
	// 默认必须开启：浏览器（验证控制台）无法连接命名管道，
	// 关了它等于关掉了控制台。
	DisableLoopback bool

	// AllowedOrigins 是 WebSocket 允许的 Origin 模式，供 Vite 开发调试使用。
	AllowedOrigins []string
}

// Daemon 是内核守护进程。
type Daemon struct {
	opts   Options
	log    *slog.Logger
	bundle *platform.Bundle
	bus    *event.Bus
	jobs   *job.Engine
	api    *api.Server
	token  string

	// store 是持久化层。为 nil 表示数据库不可用 ——
	// 这种情况在 M1 之后不应出现，因为凭据必须落库。
	store *store.Store

	settings    *settings.Service
	registry    *provider.Registry
	credentials *credential.Service
	auditor     *audit.Recorder
	configio    *configio.Service

	taskRepo   *store.Tasks
	ddnsEngine *ddns.Engine
	scheduler  *ddns.Scheduler
	tasks      *ddns.Service
	dnsService *dns.Service

	changeRunner *change.Runner
	reach        *reach.Registry
	verifyMgr    *verify.Manager
	proxyMgr     *proxy.Manager

	notifier     *notify.Manager
	notifyConfig *notify.ConfigManager
	certStore    *acme.Store
	runtimes     *hosting.Manager
	apps         *appsvc.Manager
	metrics      *metrics.Sampler

	// remote 是远程管理面（ISC Mizar）。它为 nil 表示这一块没起来 ——
	// 那时本地接口的 /v1/remote/* 会返回"未装配"，而不是伪装成功。
	remote *remote.Service
	// pushNotifier 把内核事件翻译成手机推送通知。
	//
	// 与 `notifier`（notify.Manager，内核往外发消息的通道）是两件事：
	// 那个是用户配置的 webhook 之类，这个是发给已配对的手机。
	pushNotifier *remote.Notifier

	certMgr      *acme.Manager
	certProvider *acme.StoreProvider
	acmeResolver *acme.Resolver
	// monitorCancel 停掉 IP 监控与调度器的后台 goroutine。
	monitorCancel context.CancelFunc

	servers   []*http.Server
	listeners []net.Listener
	// listenerInfos 记录实际建立的通道，供日志与 runtime.json 使用。
	localEndpoint string
	tcpEndpoint   string

	// ready 在守护进程完全就绪后关闭，供测试等待。
	ready     chan struct{}
	readyOnce sync.Once

	// handler 是本地管理接口的处理器，构造一次后缓存。
	// 库（cmd/libisc）要在本进程内直接派发请求，缓存避免每次重建路由表。
	handler http.Handler
}

// New 构造守护进程。此时不会产生任何副作用。
func New(opts Options) *Daemon {
	if opts.LogHandler == nil {
		opts.LogHandler = logx.New(os.Stderr, slog.LevelInfo, nil)
	}
	if opts.LoopbackAddr == "" {
		opts.LoopbackAddr = defaultLoopbackAddr
	}
	return &Daemon{
		opts:  opts,
		log:   slog.New(opts.LogHandler),
		ready: make(chan struct{}),
	}
}

// Handler 返回本地管理接口的处理器。
//
// 供**库**（cmd/libisc）在本进程内直接派发请求：对 GUI 来说"调用内核功能"
// 就是一次函数调用，不经过套接字、也不经过 TCP。
//
// 复用同一个 handler 而不是另写一套库 API，是刻意的：接口面由
// api/openapi.yaml 的契约定，契约有 spec-drift 检查守着，因此"库能调的"
// 与"CLI / 控制台能调的"永远是同一份，不会长出两套实现。
//
// 未就绪时返回 nil（调用方应把它当作"内核没起来"）。
func (d *Daemon) Handler() http.Handler { return d.handler }

// Events 返回事件总线，供库做事件订阅（游标式长轮询）。
//
// 未就绪时返回 nil。
func (d *Daemon) Events() *event.Bus { return d.bus }

// Ready 返回一个在守护进程完全就绪后关闭的通道，供测试同步。
func (d *Daemon) Ready() <-chan struct{} { return d.ready }

// RuntimeFile 返回运行时描述文件路径。
func (d *Daemon) RuntimeFile() string { return d.opts.Paths.RuntimeFile() }

// Run 启动守护进程并阻塞直到 ctx 被取消或发生致命错误。
//
// 返回 nil 表示正常关闭。
func (d *Daemon) Run(ctx context.Context) error {
	// 1. 数据目录
	warnings, err := d.opts.Paths.EnsureDirs()
	if err != nil {
		return err
	}
	for _, w := range warnings {
		d.log.Warn(w)
	}

	// 2. 平台后端。
	// 必须在数据库之前：密钥存储的落点由数据根目录决定。
	d.bundle = platform.Current(d.opts.Paths.Root())

	// 3. 数据库。
	// 迁移在这里执行，因此后面所有子系统都可以假定表结构就绪。
	st, err := store.Open(ctx, d.opts.Paths.DBFile())
	if err != nil {
		return err
	}
	d.store = st

	// 任何提前返回路径都必须关掉数据库。
	//
	// 这不是洁癖：在 Windows 上未关闭的 SQLite 文件句柄会让数据目录
	// 无法删除，表现为"卸载/重装时提示文件被占用"，而用户完全无从下手。
	// 正常路径下 shutdown() 会把 d.store 置空，这里的 defer 就成了空操作。
	defer func() {
		if d.store != nil {
			_ = d.store.Close()
			d.store = nil
		}
	}()

	// 4. 主密钥。
	secrets, err := secret.Open(ctx, d.bundle.SecretStore)
	if err != nil {
		return err
	}
	if secrets.Created() {
		d.log.Info("已生成新的主密钥",
			"backend", d.bundle.SecretStore.Describe().Backend)
	}

	// 5. 设置。
	// 必须在日志与语言之前：事件缓冲容量与语言都由它决定。
	settingsSvc, err := settings.Load(ctx, st)
	if err != nil {
		return err
	}
	d.settings = settingsSvc

	// 命令行显式指定的语言优先于已保存的设置，且只在这里生效一次：
	// CLI 仅在用户真的传了 --lang 时才会填 opts.Lang，因此这不会在
	// 每次重启时覆盖用户后来通过接口改成的语言。
	if d.opts.Lang != "" {
		want := string(i18n.Parse(string(d.opts.Lang)))
		if want != settingsSvc.Get().Lang {
			if _, err := settingsSvc.Update(ctx, settings.Patch{Lang: &want}); err != nil {
				d.log.Warn("应用命令行指定的语言失败", "err", err)
			}
		}
	}
	i18n.SetDefault(i18n.Parse(settingsSvc.Get().Lang))

	// 语言设置**在运行期改了也要立刻生效**。
	//
	// 早先只在启动时应用过一次，于是用户在界面上把语言改成 English 之后：
	// 命令行立刻变了（CLI 每次都重新读设置），而**服务端生成的内容仍是
	// 中文** —— 例如 /v1/providers 里的凭据字段标签与说明。两端不一致，
	// 而用户唯一的办法是重启内核。
	//
	// 这是真机上对照出来的：把守护进程的语言改成 en 之后，
	// `isc credential fields cloudflare` 里 CLI 自己的串变成了英文
	//（(required) [secret]），而服务端解析的字段说明仍是中文。
	settingsSvc.SetOnChange(func(s settings.Settings) {
		i18n.SetDefault(i18n.Parse(s.Lang))
	})

	d.log.Info(i18n.T("daemon.starting"),
		"version", version.Version,
		"os", d.bundle.OS, "arch", d.bundle.Arch,
		"paths", d.opts.Paths.String())

	// 6. 领域服务。
	d.registry = provider.Default()
	d.credentials = credential.NewService(
		st.Credentials(), secrets, specLookup(d.registry), d.log)
	d.auditor = audit.NewRecorder(st, d.log)
	d.configio = configio.New(d.credentials, d.settings, d.registry)

	// 动态解析：引擎负责"做一次"，调度器负责"什么时候做"。
	//
	// 注意引用检查的接线顺序：凭据服务需要知道谁在引用它（删除前检查），
	// 而任务服务又需要凭据服务来解密凭据。用 SetUsageChecker 打破这个环，
	// 而不是给两边都塞一个可空的相互引用。
	d.taskRepo = st.Tasks()
	d.ddnsEngine = ddns.NewEngine(
		d.taskRepo, credentialResolver{svc: d.credentials}, dynamicLookup(d.registry),
		d.bundle.IPMonitor, nil, d.log) // bus 稍后设置
	d.scheduler = ddns.NewScheduler(d.taskRepo, d.ddnsEngine, nil, d.log)
	d.tasks = ddns.NewService(d.taskRepo, d.ddnsEngine, d.scheduler)
	d.credentials.SetUsageChecker(d.taskRepo)

	// 记录管理（Tier-1 服务商）。
	//
	// 它复用同一个 credentialResolver：那个适配器的签名同时满足
	// ddns.CredentialResolver 与 dns.CredentialResolver，
	// 因此不必写两遍 —— 这是把两个领域的接口定义成相同形状的好处。
	d.dnsService = dns.NewService(credentialResolver{svc: d.credentials}, d.registry.Lookup)

	// 系统变更编排与可达性。
	//
	// change.Runner 是**所有**系统级修改的唯一入口：防火墙规则、
	// 将来的服务注册与证书文件都走它。这样"预览 → 应用 → 失败自动
	// 回滚 → 事后撤销"这套保证只有一份实现，而不是每个后端各写一遍。
	d.changeRunner = change.NewRunner(st.Changes(), nil, d.log) // bus 稍后设置
	d.reach = reach.NewRegistry()
	ipv6Native := reach.NewIPv6Native(
		d.bundle.IPMonitor, d.bundle.Firewall, d.bundle.Capabilities().Firewall)
	// 低端口权限检测器：让"生成计划"能在用户动手之前就告诉他
	// 443 这类端口在本机绑不绑得上。
	ipv6Native.SetLowPortBinder(d.bundle.LowPortBinder)
	// 把外部验证的结论接进可达性判断。
	//
	// 这一步闭合了 M3 验收里那条"必须能区分「本机没通」与「运营商封了」"：
	// 在此之前 CheckBlocked 定义了却没有任何地方会设置它，
	// 于是 doctor 里那个"上游挡住了"的结论分支永远不会被走到。
	ipv6Native.SetExternalVerdict(func() (bool, bool, string) {
		if d.verifyMgr == nil {
			return false, false, ""
		}
		v, ok := d.verifyMgr.LastVerdict()
		if !ok {
			return false, false, ""
		}
		return v.Blocked, true, v.Detail
	})
	d.reach.Register(ipv6Native)

	// 反向代理与证书。
	//
	// 反代默认**不启动**：它监听在公网上，开启是一个需要用户明确
	// 决定的动作。默认开着会让"我只是想用动态解析"的用户莫名其妙地
	// 多出一个对外的监听端口。
	d.proxyMgr = proxy.NewManager(st.ProxyRoutes(), d.log)

	// 通知中心。
	//
	// 日志通道**始终登记**：用户还没配任何外部通道时，通知至少会
	// 出现在日志与事件流里，而不是无声无息地消失。
	d.notifier = notify.NewManager(d.log)
	d.notifyConfig = notify.NewConfigManager(st.NotifyChannels(), d.notifier,
		func(format string, args ...any) { d.log.Info(fmt.Sprintf(format, args...)) })
	d.notifier.AddChannel(notify.NewLogChannel(func(msg notify.Message) {
		if d.bus != nil {
			d.bus.Publish("notify.sent", map[string]any{
				"event": msg.Event, "title": msg.Title,
				"severity": string(msg.Severity),
			})
		}
	}))

	// 证书存储与管理器。
	//
	// 证书放在数据目录下的 certs/：用户能直接检查（openssl x509 -text）、
	// 能在出问题时手工替换、也能被其它工具复用。
	d.certStore = acme.NewStore(filepath.Join(d.opts.Paths.Root(), "certs"))
	d.acmeResolver = acme.NewResolver()
	d.certProvider = acme.NewStoreProvider(d.certStore, d.acmeResolver.Lookup, d.log)
	d.certMgr = acme.NewManager(d.certStore, d.newACMEClient, nil, d.log) // bus 稍后设置
	d.certMgr.SetEmail(settingsSvc.Get().ACMEEmail)

	// 引导式外部验证。
	//
	// 目标地址取当前的主全局 IPv6：那是用户要用手机打开的那个地址。
	// 每次开始时现取而不是缓存 —— 前缀一晚上能变好几次，而缓存的
	// 地址会让用户拿着一个已经失效的 URL 反复尝试。
	d.verifyMgr = verify.NewManager(d.currentTargetIP, func(format string, args ...any) {
		d.log.Info(fmt.Sprintf(format, args...))
	})
	// 验证结束后把结论存下来，供可达性检查读取。
	//
	// 会话本身是短命的（十分钟就过期），而结论应当比会话活得久 ——
	// 用户验过一次之后再跑 doctor，应当还能看到那个结论，
	// 而不是回到"未知"。
	d.verifyMgr.SetVerdictSink(func(v verify.Verdict) {
		d.verifyMgr.SetLastVerdict(v)
		d.log.Info("外部验证得出结论",
			"blocked", v.Blocked, "reachable", v.Reachable, "port", v.Port)
	})

	// 7. 清理上一次的残留运行时文件。
	if err := d.cleanupStaleRuntime(); err != nil {
		return err
	}

	// 8. 事件总线与任务引擎。
	d.bus = event.NewBus(settingsSvc.Get().EventBufferSize)
	d.opts.LogHandler.SetPublisher(d.bus)

	// 事件总线就绪后回填给动态解析 —— 执行结果要发布事件，
	// 而总线依赖设置（缓冲容量），设置在更早的一步才加载完。
	d.ddnsEngine.SetBus(d.bus)
	d.scheduler.SetBus(d.bus)
	d.scheduler.SetResultHook(d.recordTaskRun)

	// 变更执行器也要总线：变更的结果（成功 / 失败 / 已撤销）是
	// 控制台上最需要实时看到的事件之一。
	d.changeRunner.SetBus(d.bus)

	// 证书管理器也要总线：签发与续期的结果应当实时推到控制台。
	d.certMgr.SetBus(d.bus)

	// 登记各变更类型的撤销器。
	//
	// 这一步让"跨进程撤销"成为可能：闭包无法持久化，因此内核重启后
	// 只能由制造变更的后端依据日志记录重新推导撤销动作。
	for _, p := range d.reach.List() {
		if rev, ok := p.(change.Reverter); ok {
			d.changeRunner.Register(rev)
		}
	}

	// 任务引擎的父上下文是本次运行的生命周期，而不是调用方的 ctx：
	// 调用方的 ctx 可能在 HTTP 请求结束时就被取消。
	runCtx, cancelRun := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRun()

	// 任务状态落进 SQLite：进程重启后历史任务仍可查询。
	d.jobs = job.NewEngine(runCtx, d.bus, st.Jobs(), d.log)

	// 运行时供给：在数据目录下落 cache/ 与 runtimes/（D28）。
	d.runtimes = hosting.NewManager(d.opts.Paths.Root(), goruntime.GOOS, goruntime.GOARCH)

	// 托管站点（D25）：静态站点由内核自己托管，其余走平台进程控制。
	d.apps = appsvc.NewManager(appsvc.Deps{
		Store: st.Apps(),
		Binder: newAppBinder(d.proxyMgr, d.tasks, func(context.Context) string {
			if d.settings != nil {
				return d.settings.Get().ACMEDNSCredentialID
			}
			return ""
		}, d.log),
		Runtimes:  d.runtimes,
		Processes: d.bundle.Processes,
		Logs:      appsvc.NewLogStore(filepath.Join(d.opts.Paths.LogDir(), "apps")),
		Bus:       d.bus,
		Log:       d.log,
		DataRoot:  d.opts.Paths.Root(),
	})

	// 指标采样（v0.2.0）：主机与托管站点的资源占用。
	//
	// 只采样、不落库：这些数字的意义在"现在"，重启之后留着它们没有用。
	d.metrics = metrics.NewSampler(metrics.NewSource(), metrics.DefaultInterval, metrics.DefaultHistory)

	// 登记内核支持的任务类型。
	//
	// 登记之后，拼错的 kind 会在提交时被拒绝，而不是留下一个永远失败、
	// 又查不出原因的任务。新增任务类型的子系统必须在这里补一行。
	d.jobs.RegisterKinds("debug.noop", api.JobKindRuntimeProvision,
		api.JobKindAppDeploy, api.JobKindAppStart)

	// 把上次运行遗留的 pending / running 任务归位。
	//
	// 任务体是进程内的 goroutine，而状态在 SQLite 里：内核退出后那些行会
	// 永远停在 running，界面于是显示一堆"正在执行"的任务，而实际上什么都
	// 没在跑。必须在传输通道开始监听之前做 —— 否则新任务会与归位竞争。
	if recovered, err := d.jobs.RecoverInterrupted(runCtx); err != nil {
		d.log.Error(i18n.T("daemon.recover_jobs_failed"), "err", err)
	} else if recovered > 0 {
		d.log.Warn(i18n.T("daemon.recovered_jobs"), "count", recovered)
	}

	// 9. 访问令牌
	if d.token, err = newToken(); err != nil {
		return err
	}

	// 9.5 远程管理面（ISC Mizar）。
	//
	// 构造放在 API 之前：接口层要用它做鉴权与设备管理。证书在这里就绪 ——
	// 状态页要显示公钥指纹，而指纹在总开关还没打开时也必须能显示，
	// 否则用户点开页面看到的是一片空白，无法判断它准备好没有。
	//
	// 构造失败**不让内核起不来**：远程访问是附加能力，而一个写不进去的
	// 证书目录不该让本机的 DNS 管理也用不了。失败时留下 nil，
	// 接口层会据此返回"未装配"。
	//
	// APNs 的私钥用主密钥加密之后落在 remote/ 目录里 —— 数据目录里
	// 已经有一把主密钥，没有理由让一把能给所有用户发推送的钥匙明文躺着。
	if svc, err := remote.New(remote.Options{
		Dir:        d.opts.Paths.RemoteDir(),
		Port:       d.settings.Get().RemotePort,
		Store:      st,
		Log:        d.log,
		Name:       "",
		Version:    version.Version,
		APIVersion: version.APIVersion,
		APNSStore:  remote.NewCredentialStore(d.opts.Paths.RemoteDir(), secrets),
		APNSHost:   os.Getenv("ISC_APNS_HOST"),
	}); err != nil {
		d.log.Error(i18n.T("daemon.remote_init_failed"), "err", err)
	} else {
		d.remote = svc
		d.pushNotifier = remote.NewNotifier(svc,
			remote.NewCredentialStore(d.opts.Paths.RemoteDir(), secrets),
			os.Getenv("ISC_APNS_HOST"))
	}

	// 10. API 服务
	d.api = api.New(api.Deps{
		Bus:            d.bus,
		Jobs:           d.jobs,
		Platform:       d.bundle,
		Log:            d.log,
		StartedAt:      time.Now().UTC(),
		AllowedOrigins: d.opts.AllowedOrigins,
		Token:          d.token,
		Providers:      d.registry,
		Credentials:    d.credentials,
		Settings:       d.settings,
		Audit:          d.auditor,
		AuditWriter:    st,
		Config:         d.configio,
		Tasks:          d.tasks,
		DNS:            d.dnsService,
		Reach:          d.reach,
		Changes:        d.changeRunner,
		Verify:         d.verifyMgr,
		Proxy:          d.proxyMgr,
		ProxyRoutes:    d.proxyMgr.RouteStore(),
		Notify:         d.notifier,
		NotifyConfig:   d.notifyConfig,
		Runtimes:       d.runtimes,
		Apps:           d.apps,
		Metrics:        d.metrics,
		CertProvider:   d.certProvider,
		CertInvalidate: d.certProvider.Invalidate,
		Certs:          d.certMgr,
		CertRequests:   d.certRequests,
		Remote:         d.remote,
	})

	// 10.5 缓存接口处理器：库会在进程内直接用它，不必每次重建路由表。
	d.handler = d.api.Routes()

	// 10.6 把远程面的处理器交给监听。
	//
	// 分两步是因为两边互相依赖：接口层需要本服务做鉴权，而本服务需要
	// 接口层构造出来的路由。先构造、后注入，避免把两个包耦成一个环。
	if d.remote != nil {
		d.remote.SetHandler(d.api.RemoteRoutes())
	}

	// 11. 建立传输通道
	if err := d.listen(ctx); err != nil {
		return err
	}

	// 12. 写出 runtime.json —— 此刻起客户端才能发现内核。
	if err := d.writeRuntimeInfo(); err != nil {
		d.closeListeners()
		return err
	}

	// 13. 起服务
	d.serve(cancelRun)

	// 13.5 远程监听（若设置里开着）。
	//
	// 放在本地通道之后：本地通道是内核的**基本可用性**，而远程面是附加的。
	// 顺序反过来的话，一个被占用的远程端口会让用户连本地界面都打不开。
	d.startRemote(runCtx)

	// 14. 后台：网卡监控 + 动态解析调度
	d.startBackground(runCtx)

	// 15. 检查上次是否有未走完的系统变更。
	//
	// 只报告、**不自动撤销**：一次执行中的变更可能已经部分生效，
	// 而用户可能正依赖那部分（例如他已经通过新开的端口连上了服务）。
	// 内核擅自撤掉会把用户正在用的东西拿走，而他完全不知道发生了什么。
	d.reportInterruptedChanges(runCtx)

	// 16. 指标采样。
	//
	// 与站点归位一样放在后台：采样要起短命进程，不该拖慢内核就绪。
	go d.metrics.Run(runCtx, func() []metrics.AppRef {
		if d.apps == nil {
			return nil
		}
		running := d.apps.Running()
		refs := make([]metrics.AppRef, 0, len(running))
		for _, item := range running {
			refs = append(refs, metrics.AppRef{AppID: item.ID, PID: item.PID, StartedAt: item.StartedAt})
		}
		return refs
	})

	// 17.5 推送转发。
	//
	// 放在后台：它订阅事件总线并一直等到 ctx 结束，不该拖慢内核就绪。
	if d.pushNotifier != nil {
		go d.pushNotifier.Run(runCtx, d.bus)
	}

	// 18. 归位托管站点。
	//
	// 放在后台：拉起一个站点要等健康检查（最长 60 秒），而"内核是否可用"
	// 不该被某个用户站点拖住 —— 用户要能立刻打开界面看它卡在哪。
	go d.recoverApps(runCtx)

	d.readyOnce.Do(func() { close(d.ready) })
	// 把内核眼中的代理配置写进日志。
	//
	// 用户报"凭据验证失败"时，第一件要排除的事就是
	// "请求到底有没有走他配的代理"—— 有这一行就不用再猜。
	d.log.Info(i18n.T("daemon.outbound_proxy", sysproxy.Describe()))
	d.log.Info(i18n.T("daemon.started"))

	// 16. 等待退出信号
	<-ctx.Done()
	d.log.Info(i18n.T("daemon.stopping"))

	return d.shutdown()
}

// startRemote 按设置启动远程监听。
//
// 与反向代理同一套取舍：失败只记日志、不影响内核启动。用户在界面上
// 看到的是"远程访问未运行 + 原因"，而不是"内核起不来"。
func (d *Daemon) startRemote(ctx context.Context) {
	if d.remote == nil || d.settings == nil {
		return
	}
	s := d.settings.Get()
	d.remote.SetNotificationsEnabled(s.RemoteNotifications)
	if !s.RemoteEnabled {
		d.log.Info(i18n.T("daemon.remote_disabled"))
		return
	}
	if err := d.remote.Apply(ctx, true, s.RemotePort); err != nil {
		d.log.Error(i18n.T("daemon.remote_start_failed"), "err", err)
	}
}

// startProxy 按设置启动反向代理。
func (d *Daemon) startProxy(ctx context.Context) {
	if d.proxyMgr == nil || d.settings == nil {
		return
	}

	s := d.settings.Get()
	if !s.ProxyEnabled {
		d.log.Info("反向代理未开启（可在设置中启用）")
		return
	}

	// 同步一次证书路由映射，再决定用明文还是 HTTPS。
	d.applyCertRoutes()

	if s.ProxyTLS {
		// HTTPS：证书由 StoreProvider 在握手时按 SNI 提供。
		//
		// 先签一次证书。签不出来时**不阻断启动** —— 代理会以
		// "没有证书可用"的状态运行，而用户需要界面可用才能去改配置。
		d.ensureCerts(ctx)

		if err := d.proxyMgr.ServeTLS(ctx, s.ProxyPort, proxy.TLSOptions{
			Provider: d.certProvider,
		}); err != nil {
			d.log.Error("反向代理（HTTPS）启动失败",
				"port", s.ProxyPort, "err", err)
			return
		}
		return
	}

	if err := d.proxyMgr.Start(ctx, s.ProxyPort); err != nil {
		// 启动失败**不阻断内核**：动态解析等其它功能仍然可用，
		// 而用户需要界面可用才能去改端口。
		//
		// 但错误必须留在状态里（Manager.Status().Error），
		// 否则用户在界面上只看到"代理没开"，不知道为什么。
		d.log.Error("反向代理启动失败", "port", s.ProxyPort, "err", err)
		return
	}
}

// newACMEClient 按证书请求构造 ACME 客户端。
//
// 每次现构造而不是持有一个：DNS-01 用的凭据取决于当前设置，
// 而用户可能中途换了凭据 —— 持有旧的会让签发一直用错凭据。
func (d *Daemon) newACMEClient(req acme.CertRequest) (*acme.Client, error) {
	dynLookup := func(name string) (dns.Provider, bool) {
		return d.registry.Lookup(name)
	}
	solver := acme.NewDNS01Provider(
		credentialResolver{svc: d.credentials}, dynLookup, req.CredentialID)

	return acme.NewClient(solver, d.certStore), nil
}

// certRequests 返回当前需要证书的域名集合。
//
// 它来自**启用了 HTTPS 的代理路由** —— 那是用户表达"这些域名要走
// HTTPS"的唯一地方，因此证书需要覆盖什么由它决定。
func (d *Daemon) certRequests() []acme.CertRequest {
	if d.proxyMgr == nil || d.settings == nil {
		return nil
	}
	credID := d.settings.Get().ACMEDNSCredentialID
	if credID == "" {
		return nil
	}

	routes := d.proxyMgr.Routes()
	var out []acme.CertRequest
	for _, r := range routes {
		if !r.TLS || len(r.Hosts) == 0 {
			continue
		}
		out = append(out, acme.CertRequest{
			Domains:      r.Hosts,
			CredentialID: credID,
		})
	}
	return out
}

// ensureCerts 为当前全部 TLS 路由申请（或续期）证书。
//
// 单个域名失败不中断其余的：一个域名配错了不该让其它域名也拿不到证书。
func (d *Daemon) ensureCerts(ctx context.Context) {
	if d.certMgr == nil {
		return
	}
	for _, req := range d.certRequests() {
		if ctx.Err() != nil {
			return
		}
		if _, issued, err := d.certMgr.Ensure(ctx, req); err != nil {
			d.log.Error("证书签发失败", "domains", req.Domains, "err", err)
		} else if issued {
			// 新证书已经写进磁盘，而缓存里还是旧的 —— 不清的话
			// 用户会看到"续期成功了但浏览器仍然报证书过期"。
			d.certProvider.Invalidate()
		}
	}
}

// applyCertRoutes 把 TLS 路由同步到证书解析器。
//
// 映射变了意味着"哪个域名用哪张证书"变了，因此必须同时清掉
// StoreProvider 的缓存 —— 不清会让旧映射继续生效到缓存过期。
func (d *Daemon) applyCertRoutes() {
	if d.acmeResolver == nil {
		return
	}

	// 重建解析器：路由是整体替换的，映射也应当整体重建。
	d.acmeResolver = acme.NewResolver()
	for _, req := range d.certRequests() {
		d.acmeResolver.Add(acme.CertName(req.Domains), req.Domains)
	}
	d.certProvider.SetResolve(d.acmeResolver.Lookup)
}

// currentTargetIP 返回给用户用手机打开的那个地址。
//
// 取第一个可用的全局 IPv6：那是家用场景下唯一能从公网访问的地址。
// 每次现取而不是缓存 —— 运营商前缀一晚上能变好几次，而缓存的地址
// 会让用户拿着一个已经失效的 URL 反复尝试，最后得出"验证不通过"的
// 错误结论。
func (d *Daemon) currentTargetIP(ctx context.Context) string {
	if d.bundle == nil || d.bundle.IPMonitor == nil {
		return ""
	}
	list, err := d.bundle.IPMonitor.Snapshot(ctx)
	if err != nil {
		return ""
	}
	for _, iface := range list {
		if iface.IsLoopback {
			continue
		}
		if g := iface.GlobalIPv6(); len(g) > 0 {
			return g[0].String()
		}
	}
	return ""
}

// reportInterruptedChanges 检查并报告上次未走完的系统变更。
func (d *Daemon) reportInterruptedChanges(ctx context.Context) {
	if d.changeRunner == nil {
		return
	}
	interrupted, err := d.changeRunner.RecoverInterrupted(ctx)
	if err != nil {
		d.log.Warn("检查未完成的系统变更失败", "err", err)
		return
	}
	if len(interrupted) == 0 {
		return
	}

	// 用 Warn 而不是 Error：这不是错误，是一件需要用户知情的事情。
	// 用 Error 会让日志监控把它当成故障，而它可能完全无害
	//（例如内核在执行完最后一步后、写日志前被强杀）。
	for _, it := range interrupted {
		d.log.Warn("发现未完成的系统变更，请确认是否需要撤销",
			"plan", it.Record.PlanID,
			"kind", it.Record.Kind,
			"title", it.Record.Title,
			"reason", it.Reason)
	}
	d.log.Warn(i18n.T("change.interrupted_found", len(interrupted)))
}

// specLookup 把服务商注册表适配成 credential.SpecLookup。
//
// 这层薄适配是为了打破依赖环：provider 需要 credential 的字段类型，
// 因此 credential 不能反过来依赖 provider。
func specLookup(reg *provider.Registry) credential.SpecLookup {
	return func(name string) ([]credential.FieldSpec, bool) {
		p, ok := reg.Get(name)
		if !ok {
			return nil, false
		}
		return p.CredentialFields, true
	}
}

// dynamicLookup 把服务商注册表适配成 ddns.ProviderLookup。
func dynamicLookup(reg *provider.Registry) ddns.ProviderLookup {
	return registryDynamic{reg: reg}
}

type registryDynamic struct{ reg *provider.Registry }

func (r registryDynamic) DynamicUpdater(name string) (dns.DynamicUpdater, bool) {
	return r.reg.DynamicUpdater(name)
}

// credentialResolver 把凭据服务适配成 ddns.CredentialResolver。
//
// 需要这一层是因为两边用的是各自的领域类型：credential 包不该知道
// dns 包的存在（它只负责"把凭据安全地存下来"），而 dns 包也不该知道
// 凭据是从数据库里解密出来的。转换放在装配处，两个领域保持互不知情。
type credentialResolver struct{ svc *credential.Service }

func (r credentialResolver) Resolve(ctx context.Context, id string) (dns.Credential, error) {
	c, err := r.svc.Resolve(ctx, id)
	if err != nil {
		return dns.Credential{}, err
	}
	return dns.Credential{
		ID:       c.ID,
		Provider: c.Provider,
		Fields:   c.Fields,
	}, nil
}

// recordTaskRun 把一次执行结果写回任务。
//
// 落库失败只记日志：用户真正关心的是 DNS 记录被更新了，
// 而不是统计数字漂了一位。
func (d *Daemon) recordTaskRun(ctx context.Context, t ddns.Task, run ddns.TaskRun) {
	// 用 run 里的地址而不是 t 上的：t 是执行**之前**读出来的快照，
	// 它的 LastIPv4 是上一轮的值。用它回写会让界面永远慢一拍。
	if err := d.ddnsEngine.MarkRun(ctx, t, run, run.IPv4, run.IPv6); err != nil {
		d.log.Warn("写入任务运行状态失败", "task", t.ID, "err", err)
	}
}

// startBackground 启动 IP 监控与调度器。
//
// 两件事在这里汇合：
//
//	IP 监控 → 事件总线  把网卡变化变成 ip.changed / ip.prefix_changed 事件
//	调度器  → 订阅总线  收到 IP 事件后立刻跑一次动态解析
//
// 中间过一道事件总线而不是直接调用：事件总线上还挂着 WebSocket 订阅者
// （控制台与下游 GUI），它们需要看到地址变化；而调度器只是消费者之一。
func (d *Daemon) startBackground(parent context.Context) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	d.monitorCancel = cancel

	go func() {
		if err := d.scheduler.Run(ctx); err != nil {
			d.log.Error("调度器退出", "err", err)
		}
	}()

	go d.watchInterfaces(ctx)

	// 通知中心：先加载用户配置的通道，再订阅事件总线。
	//
	// 顺序不能反：先订阅的话，启动瞬间的事件会在通道加载完成之前
	// 到达，而那些通知会被静默丢掉。
	if err := d.notifyConfig.Load(ctx); err != nil {
		// 加载失败不该阻断内核：还有日志通道可用。
		d.log.Warn("加载通知通道配置失败，将只使用日志通道", "err", err)
	}
	d.startNotifier(ctx)

	// 证书的定期检查与续期。
	//
	// 它一直在跑（不管代理是否开启）：证书可能在代理关闭期间进入
	// 续期窗口，而用户下次打开代理时不该看到一张过期的证书。
	go func() {
		if err := d.certMgr.Run(ctx, d.certRequests); err != nil {
			d.log.Error("证书续期循环退出", "err", err)
		}
	}()

	// 反向代理：只在设置里开启时才启动。
	//
	// 默认关闭是刻意的 —— 反代监听在公网上，开启它是一个需要用户
	// 明确决定的动作。
	d.startProxy(ctx)

	// 启动后立刻跑一轮。
	//
	// 不做这一步的话，内核重启后要等满一个定时周期（默认 5 分钟）才会
	// 第一次解析。而用户按下"重启服务"时的期待恰恰是"它马上恢复工作"。
	//
	// 延迟 2 秒是为了让启动日志先落完 —— 解析任务的日志混在启动序列里
	// 会让排查问题时的阅读顺序变得混乱。
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		d.log.Info("开始首轮动态解析")
		d.scheduler.RunAll(ctx)
	}()
}

// watchInterfaces 把网卡变化发布到事件总线。
func (d *Daemon) watchInterfaces(ctx context.Context) {
	monitor := d.bundle.IPMonitor
	if monitor == nil {
		return
	}

	events, err := monitor.Watch(ctx)
	if err != nil {
		// 监控不可用不是致命错误：定时轮询仍会兜底。
		d.log.Warn("启动网卡监控失败，将只使用定时触发", "err", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			// 前缀事件单列：它是本项目的核心信号 ——
			// 前缀变化意味着该前缀下**所有** AAAA 记录都要重写。
			typ := event.TypeIPChanged
			if ev.IsPrefixEvent() {
				typ = event.TypeIPPrefixChanged
			}
			d.bus.Publish(typ, map[string]any{
				"kind":   string(ev.Kind),
				"iface":  ev.Iface,
				"addr":   ev.Addr.String(),
				"prefix": ev.Prefix.String(),
				"at":     ev.At,
			})
			d.log.Debug("网卡变化", "event", ev.String())
		}
	}
}

// cleanupStaleRuntime 清理上一次非正常退出留下的 runtime.json。
//
// 不做这件事的话，客户端会读到一个指向已死进程的地址，
// 得到"连接被拒绝"这种难以归因的报错，而不是清晰的"内核未运行"。
func (d *Daemon) cleanupStaleRuntime() error {
	path := d.opts.Paths.RuntimeFile()
	info, err := runtimeinfo.Read(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		// 文件损坏：直接删掉比让所有客户端持续解析失败要好。
		d.log.Warn("运行时文件无法解析，已删除", "path", path, "err", err)
		return runtimeinfo.Remove(path)
	}
	if info.IsStale() {
		d.log.Warn(i18n.T("daemon.stale_runtime_file", info.PID), "path", path)
		return runtimeinfo.Remove(path)
	}
	return fmt.Errorf("%s", i18n.T("daemon.already_running", info.PID))
}

// listen 建立本地管理通道。
//
// 策略：
//
//   - 命名管道 / Unix 套接字是首选（ACL 层防护，不占 TCP 端口）；
//   - 回环 TCP 始终一并开启，因为浏览器无法连接前两者。
//
// 前者失败不是致命错误：降级为"仅回环"仍可用，只是少了一层防护，
// 因此记 Warn 而不是直接退出。
func (d *Daemon) listen(_ context.Context) error {
	localEP := platform.LocalEndpoint(d.opts.Paths.RunDir())
	ln, err := localEP.Listen(context.Background())
	if err != nil {
		d.log.Warn("本地管理通道建立失败，降级为仅回环",
			"endpoint", localEP.String(), "err", err)
	} else {
		d.localEndpoint = localEP.String()
		d.addServer(ln)
		d.log.Info(i18n.T("transport.desc", localEP.String(), localEP.TransportName()))
	}

	if !d.opts.DisableLoopback {
		tcpEP := platform.Endpoint(platform.SchemeTCP + "://" + d.opts.LoopbackAddr)
		tcpLn, err := tcpEP.Listen(context.Background())
		if err != nil {
			d.closeListeners()
			return fmt.Errorf("%s: %w", i18n.T("daemon.transport_failed", tcpEP.String()), err)
		}
		// 端口 0 表示自动分配，必须回读真实端口才能告诉客户端。
		d.tcpEndpoint = platform.SchemeTCP + "://" + tcpLn.Addr().String()
		d.addServer(tcpLn)
		d.log.Info(i18n.T("transport.desc", d.tcpEndpoint,
			platform.Endpoint(d.tcpEndpoint).TransportName()))
	}

	if len(d.servers) == 0 {
		return fmt.Errorf("%s", i18n.T("daemon.transport_failed", i18n.T("daemon.err.no_channel")))
	}
	return nil
}

// addServer 为一个监听器创建 http.Server。
func (d *Daemon) addServer(ln net.Listener) {
	srv := &http.Server{
		Handler: d.handler,
		// ReadHeaderTimeout 必须设置：否则一个只连不发的客户端就能
		// 占住连接不放（Slowloris）。服务端一旦被拖住，
		// 用户界面上的所有操作都会一起卡死。
		ReadHeaderTimeout: 10 * time.Second,
		// 刻意不设 WriteTimeout / IdleTimeout：事件流是长连接，
		// 写超时会在客户端长时间无事件时把连接掐断。
		ErrorLog: slog.NewLogLogger(d.log.Handler(), slog.LevelWarn),
	}
	d.servers = append(d.servers, srv)
	d.listeners = append(d.listeners, ln)
}

// serve 为每个监听器启动一个服务 goroutine。
func (d *Daemon) serve(cancelRun context.CancelFunc) {
	for i := range d.servers {
		srv, ln := d.servers[i], d.listeners[i]
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				d.log.Error("HTTP 服务异常退出", "err", err, "addr", ln.Addr().String())
				// 服务异常是致命的：继续运行只会让客户端连不上却以为内核还活着。
				cancelRun()
			}
		}()
	}
}

// writeRuntimeInfo 写出 runtime.json。
func (d *Daemon) writeRuntimeInfo() error {
	info := runtimeinfo.Info{
		PID:              os.Getpid(),
		Version:          version.Version,
		Endpoint:         d.localEndpoint,
		FallbackEndpoint: d.tcpEndpoint,
		Token:            d.token,
		StartedAt:        time.Now().UTC(),
	}
	if err := runtimeinfo.Write(d.opts.Paths.RuntimeFile(), info); err != nil {
		d.log.Error(i18n.T("daemon.runtime_write_failed", err.Error()))
		return err
	}
	return nil
}

// shutdown 优雅关闭。
//
// 顺序有讲究：
//
//  1. 先删 runtime.json —— 让新客户端立刻停止发现内核，
//     避免它们在关闭过程中连进来又被拒；
//  2. 关闭事件总线 —— 断开全部 WebSocket 订阅，让事件流的 handler 返回；
//  3. 取消并等待在途任务 —— 任务可能正在写 DNS 记录，中途打断会留下
//     半完成的状态；
//  4. 关闭 HTTP 服务并等待在途请求。
//
// recoverApps 归位托管站点。
func (d *Daemon) recoverApps(ctx context.Context) {
	if d.apps == nil {
		return
	}
	started, err := d.apps.Recover(ctx)
	if err != nil {
		d.log.Error(i18n.T("daemon.recover_apps_failed"), "err", err)
		return
	}
	if started > 0 {
		d.log.Info(i18n.T("daemon.recovered_apps"), "count", started)
	}
}

func (d *Daemon) shutdown() error {
	var firstErr error

	// 管理接口先停：让新请求不再进来。
	if err := runtimeinfo.Remove(d.opts.Paths.RuntimeFile()); err != nil {
		d.log.Warn("删除运行时文件失败", "err", err)
		firstErr = err
	}

	// 停掉网卡监控与调度器，避免它们在收尾过程中又发起一次解析更新 ——
	// 那会在关闭流程里产生一次到服务商的请求，让关闭看起来"卡住了"。
	if d.monitorCancel != nil {
		d.monitorCancel()
		d.monitorCancel = nil
	}

	// 停掉远程监听。它同样要等在途请求结束 —— 手机上可能正挂着一条
	// 25 秒的长轮询，而"内核已关闭"与"内核卡住了"在用户那里长得一样。
	if d.remote != nil {
		_ = d.remote.Stop(context.Background())
	}

	// 停掉反向代理：它会等待在途请求结束（可能有正在传输的大文件），
	// 因此有自己的超时上限，不会让关闭流程无限期挂住。
	//
	// 用一个独立于调用方 ctx 的上下文：关闭流程本身可能就是被
	// "取消 ctx"触发的，而用一个已取消的 ctx 去关停会让代理
	// 来不及把在途请求收尾。
	// 先停业务进程再停反代：反过来的话，反代在停止过程中仍会把请求
	// 转给正在退出的应用。
	if d.apps != nil {
		// 自带内部超时，不依赖本次 drain 的预算：逐个优雅停止可能要等
		// 好几秒，而 daemon 的总预算是 10 秒。
		appCtx, cancelApps := context.WithTimeout(context.Background(), 20*time.Second)
		d.apps.Shutdown(appCtx)
		cancelApps()
	}
	if d.proxyMgr != nil {
		_ = d.proxyMgr.Stop(context.Background())
	}

	if d.bus != nil {
		d.bus.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if d.jobs != nil {
		if err := d.jobs.Shutdown(ctx); err != nil {
			d.log.Warn("等待任务收尾超时", "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	for _, srv := range d.servers {
		if err := srv.Shutdown(ctx); err != nil {
			d.log.Warn("关闭 HTTP 服务超时", "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	d.closeListeners()

	// 数据库最后关：前面的子系统在收尾过程中仍可能写入任务状态。
	if d.store != nil {
		if err := d.store.Close(); err != nil {
			d.log.Warn("关闭数据库失败", "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
		// 置空让 Run 里的兜底 defer 成为空操作，避免重复关闭。
		d.store = nil
	}

	d.log.Info(i18n.T("daemon.stopped"))
	return firstErr
}

func (d *Daemon) closeListeners() {
	for _, ln := range d.listeners {
		_ = ln.Close()
	}
}

// newToken 生成访问令牌。
func newToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf(i18n.T("daemon.err.token"), err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
