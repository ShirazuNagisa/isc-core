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
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api"
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
	"github.com/ShirazuNagisa/isc-core/internal/paths"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
	"github.com/ShirazuNagisa/isc-core/internal/provider"
	"github.com/ShirazuNagisa/isc-core/internal/proxy"
	"github.com/ShirazuNagisa/isc-core/internal/reach"
	"github.com/ShirazuNagisa/isc-core/internal/runtimeinfo"
	"github.com/ShirazuNagisa/isc-core/internal/secret"
	"github.com/ShirazuNagisa/isc-core/internal/settings"
	"github.com/ShirazuNagisa/isc-core/internal/store"
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
	d.reach.Register(reach.NewIPv6Native(
		d.bundle.IPMonitor, d.bundle.Firewall, d.bundle.Capabilities().Firewall))

	// 反向代理。
	//
	// 它默认**不启动**：反代监听在公网上，开启它是一个需要用户明确
	// 决定的动作。默认开着会让"我只是想用动态解析"的用户莫名其妙地
	// 多出一个对外的监听端口。
	d.proxyMgr = proxy.NewManager(st.ProxyRoutes(), d.log)

	// 引导式外部验证。
	//
	// 目标地址取当前的主全局 IPv6：那是用户要用手机打开的那个地址。
	// 每次开始时现取而不是缓存 —— 前缀一晚上能变好几次，而缓存的
	// 地址会让用户拿着一个已经失效的 URL 反复尝试。
	d.verifyMgr = verify.NewManager(d.currentTargetIP, func(format string, args ...any) {
		d.log.Info(fmt.Sprintf(format, args...))
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

	// 9. 访问令牌
	if d.token, err = newToken(); err != nil {
		return err
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
	})

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

	// 14. 后台：网卡监控 + 动态解析调度
	d.startBackground(runCtx)

	// 15. 检查上次是否有未走完的系统变更。
	//
	// 只报告、**不自动撤销**：一次执行中的变更可能已经部分生效，
	// 而用户可能正依赖那部分（例如他已经通过新开的端口连上了服务）。
	// 内核擅自撤掉会把用户正在用的东西拿走，而他完全不知道发生了什么。
	d.reportInterruptedChanges(runCtx)

	d.readyOnce.Do(func() { close(d.ready) })
	d.log.Info(i18n.T("daemon.started"))

	// 16. 等待退出信号
	<-ctx.Done()
	d.log.Info(i18n.T("daemon.stopping"))

	return d.shutdown()
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
		return fmt.Errorf("%s", i18n.T("daemon.transport_failed", "无可用通道"))
	}
	return nil
}

// addServer 为一个监听器创建 http.Server。
func (d *Daemon) addServer(ln net.Listener) {
	srv := &http.Server{
		Handler: d.api.Routes(),
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

	// 停掉反向代理：它会等待在途请求结束（可能有正在传输的大文件），
	// 因此有自己的超时上限，不会让关闭流程无限期挂住。
	//
	// 用一个独立于调用方 ctx 的上下文：关闭流程本身可能就是被
	// "取消 ctx"触发的，而用一个已取消的 ctx 去关停会让代理
	// 来不及把在途请求收尾。
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
		return "", fmt.Errorf("daemon: 生成访问令牌失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
