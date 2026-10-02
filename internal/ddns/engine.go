package ddns

import (
	"context"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"log/slog"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/ddnsgo"
	"github.com/ShirazuNagisa/isc-core/internal/dns"
	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// CredentialResolver 按 ID 取出解密后的凭据。
//
// 用窄接口而不是直接依赖 credential.Service：本包只需要"给我凭据"这一件事，
// 而 credential 包依赖 secret 与 provider，直接依赖会把一大串东西
// 拖进本包的编译单元与测试替身里。
type CredentialResolver interface {
	Resolve(ctx context.Context, id string) (dns.Credential, error)
}

// ProviderLookup 按服务商名取出动态更新实现。
type ProviderLookup interface {
	// DynamicUpdater 返回该服务商的动态解析实现；不支持时 found 为 false。
	DynamicUpdater(name string) (updater dns.DynamicUpdater, found bool)
}

// IPProvider 提供当前网卡地址快照。
//
// 它就是 platform.IPMonitor，抽成窄接口是为了让引擎的测试
// 不必去碰真实网卡。
type IPProvider interface {
	Snapshot(ctx context.Context) ([]platform.InterfaceAddrs, error)
}

// Engine 执行动态解析任务。
//
// 并发安全：调度器可能同时从定时器与地址变化事件触发，
// 而缓存与任务状态都必须保持一致。
type Engine struct {
	repo      Repository
	creds     CredentialResolver
	providers ProviderLookup
	monitor   IPProvider
	bus       *event.Bus
	log       *slog.Logger

	// mu 保护 caches。刻意用窄临界区（只在读写缓存时持锁），
	// 而不是"整个 RunTask 期间持锁"—— 后者会让一次慢的服务商请求
	// 阻塞住其它任务的调度，而它们之间毫无关系。
	mu     sync.Mutex
	caches map[cacheKey]*ddnsgo.IpCache
}

// cacheKey 标识一路地址的防抖缓存。
//
// 按 (任务, 记录类型) 而不是只按任务：A 与 AAAA 的变化节奏
// 完全独立（IPv4 地址在 CGNAT 下可能长期不变，而 IPv6 前缀一天变几次），
// 共用一个计数器会让两者互相干扰。
type cacheKey struct {
	taskID     string
	recordType string
}

// NewEngine 构造执行引擎。
func NewEngine(
	repo Repository, creds CredentialResolver, providers ProviderLookup,
	monitor IPProvider, bus *event.Bus, log *slog.Logger,
) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		repo:      repo,
		creds:     creds,
		providers: providers,
		monitor:   monitor,
		bus:       bus,
		log:       log,
		caches:    make(map[cacheKey]*ddnsgo.IpCache),
	}
}

// SetBus 设置事件总线。
//
// 单独一步而不是构造参数：总线的缓冲容量来自设置，而设置在凭据之前
// 就要加载完（语言与日志级别都靠它），于是构造顺序上总线晚于设置、
// 早于引擎 —— 与其把构造顺序扭成一个环，不如留一个显式的回填点。
func (e *Engine) SetBus(bus *event.Bus) { e.bus = bus }

// TaskRun 是一次任务执行的完整结果。
type TaskRun struct {
	TaskID string
	// Status 是本次执行的总体结果。
	Status Status
	// Message 是给用户看的摘要（已本地化）。
	Message string
	// Dynamic 是各记录类型的详细结果。
	Dynamic []dns.DynamicResult
	// Skipped 为 true 表示本次未与任何服务商通信（地址未变化且未到比对时机）。
	Skipped bool

	// IPv4 / IPv6 是本次实际使用的地址；为空表示该记录类型没有执行。
	//
	// 由引擎填进结果、而不是让调用方从任务对象上读 ——
	// 任务对象上的 LastIPv4 是**上一次**的值，用它回写等于永远慢一拍，
	// 界面上会一直显示上一轮的地址。
	IPv4 string
	IPv6 string
}

// RunTask 执行一条任务。
//
// 返回的 error 只表示"任务本身无法执行"（凭据取不到、服务商不支持动态解析）；
// 服务商返回的失败记在 TaskRun.Status 里 —— 那属于业务结果，不是程序错误，
// 需要落库并在界面上展示，而不是变成一个异常。
func (e *Engine) RunTask(ctx context.Context, t Task) (TaskRun, error) {
	run := TaskRun{TaskID: t.ID, Status: StatusUnchanged}

	cred, err := e.creds.Resolve(ctx, t.CredentialID)
	if err != nil {
		return run, fmt.Errorf(i18n.T("ddns.err.cred_failed"), err)
	}

	updater, ok := e.providers.DynamicUpdater(cred.Provider)
	if !ok {
		return run, fmt.Errorf(i18n.T("ddns.err.no_dynamic"), cred.Provider)
	}

	var (
		attempted int
		succeeded int
		failed    int
		lastErr   string
	)

	for _, recordType := range t.RecordTypes() {
		src := t.IPv4
		dnsType := dns.TypeA
		if recordType == "AAAA" {
			src = t.IPv6
			dnsType = dns.TypeAAAA
		}

		addr := e.resolveAddr(ctx, src, dnsType == dns.TypeAAAA)
		if addr == "" {
			// 取不到地址不等于任务失败：可能是网卡暂时没有全局 IPv6，
			// 或者外部接口超时。如实记下来，下一轮会重试。
			lastErr = fmt.Sprintf(i18n.T("ddns.err.no_addr"), recordType)
			failed++
			continue
		}

		// 记录本次实际用的地址，供上层回写任务状态。
		if dnsType == dns.TypeAAAA {
			run.IPv6 = addr
		} else {
			run.IPv4 = addr
		}

		// 防抖：地址没变且还没攒够次数时，跳过这次服务商比对。
		if !e.cacheCheck(t.ID, recordType, addr) {
			e.log.Debug("地址未变化，跳过一次服务商比对",
				"task", t.ID, "record_type", recordType, "addr", addr)
			continue
		}

		attempted++
		res, updErr := updater.UpdateDynamic(ctx, cred, dns.DynamicRequest{
			Domains:       NormalizeDomains(src.Domains),
			RecordType:    dnsType,
			IP:            addr,
			TTL:           t.TTL,
			HTTPInterface: t.HTTPInterface,
		})
		if updErr != nil {
			failed++
			lastErr = updErr.Error()
			e.publishUpdate(t, dnsType, addr, nil, updErr)
			continue
		}

		run.Dynamic = append(run.Dynamic, res)
		if res.HasFailure() {
			failed++
			lastErr = firstFailureMessage(res)
			e.publishUpdate(t, dnsType, addr, &res, nil)
			continue
		}
		succeeded += res.Changed()
		e.publishUpdate(t, dnsType, addr, &res, nil)
	}

	switch {
	case attempted == 0 && failed == 0:
		run.Skipped = true
		run.Status = StatusUnchanged
		run.Message = i18n.T("ddns.msg.unchanged")
	case failed > 0:
		run.Status = StatusFailed
		run.Message = lastErr
	case succeeded > 0:
		run.Status = StatusSuccess
		run.Message = fmt.Sprintf(i18n.T("ddns.msg.updated"), succeeded)
	default:
		run.Status = StatusUnchanged
		run.Message = i18n.T("ddns.msg.no_change")
	}
	return run, nil
}

// publishUpdate 发布一次更新的事件。
//
// 事件里**不含凭据**，只含域名、记录类型与地址 —— 事件会流向
// 控制台、日志与将来的通知通道，任何一处泄漏凭据都是不可接受的。
func (e *Engine) publishUpdate(t Task, recordType dns.RecordType, ip string,
	res *dns.DynamicResult, err error) {

	if e.bus == nil {
		return
	}

	payload := map[string]any{
		"task_id":     t.ID,
		"label":       t.Label,
		"record_type": string(recordType),
		"ip":          ip,
	}

	if err != nil {
		payload["error"] = err.Error()
		e.bus.Publish(event.TypeDNSUpdateFailed, payload)
		return
	}

	domains := make([]map[string]string, 0, len(res.Domains))
	for _, d := range res.Domains {
		domains = append(domains, map[string]string{
			"domain": d.Domain,
			"status": string(d.Status),
		})
	}
	payload["domains"] = domains

	if res.HasFailure() {
		payload["error"] = firstFailureMessage(*res)
		e.bus.Publish(event.TypeDNSUpdateFailed, payload)
		return
	}
	e.bus.Publish(event.TypeDNSRecordUpdated, payload)
}

// firstFailureMessage 取第一条失败域名的说明。
func firstFailureMessage(res dns.DynamicResult) string {
	for _, d := range res.Domains {
		if d.Status == dns.StatusFailed {
			if d.Message != "" {
				return d.Message
			}
			return fmt.Sprintf(i18n.T("ddns.err.domain_failed"), d.Domain)
		}
	}
	return i18n.T("ddns.err.update_failed")
}

// cacheCheck 判断是否需要去服务商那边比对。
func (e *Engine) cacheCheck(taskID, recordType, addr string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	key := cacheKey{taskID: taskID, recordType: recordType}
	cache, ok := e.caches[key]
	if !ok {
		cache = &ddnsgo.IpCache{}
		e.caches[key] = cache
	}
	return cache.Check(addr)
}

// ResetCache 清空某个任务的防抖缓存。
//
// 用于"用户刚保存了配置"或"上次执行失败需要立刻重试"——
// 这正是上游那个被到处改写的 util.ForceCompareGlobal 想解决的问题，
// 只是这里的作用域精确到了单个任务。
func (e *Engine) ResetCache(taskID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k := range e.caches {
		if k.taskID == taskID {
			delete(e.caches, k)
		}
	}
}

// ---------------------------------------------------------------------------
// 地址解析
// ---------------------------------------------------------------------------

// resolveAddr 按来源配置取出一个地址。
//
// netInterface 走 IPMonitor 的快照，而不是直接读网卡：
// 快照已经过滤掉了链路本地与 ULA 地址（见 platform.IsGlobalIPv6），
// 而上游的网卡读取会直接返回第一个地址 —— 在开启隐私扩展或存在
// 多个地址时，那个"第一个"很可能是一个永远无法从公网访问的地址。
// 把 fd00:: 或 fe80:: 写进 AAAA 记录是这类工具最常见的故障之一。
func (e *Engine) resolveAddr(ctx context.Context, src Source, wantIPv6 bool) string {
	switch src.GetType {
	case GetTypeNetInterface:
		if addr := e.addrFromMonitor(ctx, src, wantIPv6); addr != "" {
			return addr
		}
		// 快照里没有（网卡名写错、或快照失败）时退回上游实现，
		// 让用户至少能得到一个与 ddns-go 行为一致的结果。
		return e.addrFromDdnsGo(src, wantIPv6)

	case GetTypeURL, GetTypeCmd:
		return e.addrFromDdnsGo(src, wantIPv6)

	default:
		return ""
	}
}

// addrFromMonitor 从 IPMonitor 的快照里取地址。
func (e *Engine) addrFromMonitor(ctx context.Context, src Source, wantIPv6 bool) string {
	if e.monitor == nil {
		return ""
	}
	snapshot, err := e.monitor.Snapshot(ctx)
	if err != nil {
		e.log.Warn("读取网卡快照失败，回退到直接读网卡", "err", err)
		return ""
	}

	for _, iface := range snapshot {
		if iface.Name != src.Value {
			continue
		}
		if wantIPv6 {
			return selectIPv6(iface.GlobalIPv6(), src.Selector)
		}
		if len(iface.IPv4) > 0 {
			return iface.IPv4[0].String()
		}
	}
	return ""
}

// selectIPv6 按选择器从候选地址里挑一个。
//
// 支持两种写法，与 ddns-go 一致：
//
//	"@2"        取第 2 个（从 1 开始）
//	"^240e:.*"  正则筛选，取第一个匹配的
//	""          取第一个
func selectIPv6(addrs []netip.Addr, selector string) string {
	selector = strings.TrimSpace(selector)
	if len(addrs) == 0 {
		return ""
	}
	if selector == "" {
		return addrs[0].String()
	}

	// "@N" 形式。
	if strings.HasPrefix(selector, "@") {
		n, err := strconv.Atoi(selector[1:])
		if err != nil || n < 1 {
			return addrs[0].String()
		}
		if n > len(addrs) {
			// 越界时退回第一个而不是返回空：返回空会让整个任务
			// 静默地不更新，而用户只看到"没有报错但域名没变"。
			return addrs[0].String()
		}
		return addrs[n-1].String()
	}

	// 正则形式。
	re, err := regexp.Compile(selector)
	if err != nil {
		return addrs[0].String()
	}
	for _, a := range addrs {
		if re.MatchString(a.String()) {
			return a.String()
		}
	}
	return ""
}

// addrFromDdnsGo 用移植过来的上游实现取地址。
//
// 它的能力比 IPMonitor 更全（支持外部接口查询与命令），
// 代价是不做全局地址过滤 —— 因此只在 netInterface 快照取不到、
// 或来源本身就是 url / cmd 时使用。
func (e *Engine) addrFromDdnsGo(src Source, wantIPv6 bool) string {
	conf := &ddnsgo.DnsConfig{}
	if wantIPv6 {
		conf.Ipv6.Enable = true
		conf.Ipv6.GetType = string(src.GetType)
		conf.Ipv6.NetInterface = src.Value
		conf.Ipv6.Ipv6Reg = src.Selector
		if src.GetType == GetTypeURL {
			conf.Ipv6.URL = src.Value
		}
		if src.GetType == GetTypeCmd {
			conf.Ipv6.Cmd = src.Value
		}
		return conf.GetIpv6Addr()
	}

	conf.Ipv4.Enable = true
	conf.Ipv4.GetType = string(src.GetType)
	conf.Ipv4.NetInterface = src.Value
	if src.GetType == GetTypeURL {
		conf.Ipv4.URL = src.Value
	}
	if src.GetType == GetTypeCmd {
		conf.Ipv4.Cmd = src.Value
	}
	return conf.GetIpv4Addr()
}

// MarkRun 把一次执行的结果写回任务并落库。
//
// 与 RunTask 分开：RunTask 只负责"做事"，MarkRun 只负责"记账"。
// 这样调度器可以在记账失败时只记日志而不影响已完成的工作 ——
// 用户真正关心的是 DNS 记录被更新了，而不是统计数字漂了一位。
func (e *Engine) MarkRun(ctx context.Context, t Task, run TaskRun, ipv4, ipv6 string) error {
	now := time.Now().UTC()
	t.LastRunAt = &now
	t.LastStatus = run.Status
	t.LastMessage = run.Message
	if ipv4 != "" {
		t.LastIPv4 = ipv4
	}
	if ipv6 != "" {
		t.LastIPv6 = ipv6
	}
	t.UpdatedAt = now
	return e.repo.Update(ctx, t)
}
