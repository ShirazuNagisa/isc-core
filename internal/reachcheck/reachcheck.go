// Package reachcheck 定期从**公网那一侧**确认每个站点还活着。
//
// # 它回答的问题与"本机能不能打开"不同
//
// 站点进程活着、反向代理也配好了，域名却可能从外面打不开：隧道断了、
// DNS 记录被人改掉、证书过期、上游把人挡了。这些都不会反映在本机的
// 健康检查里 —— 内核能连上 127.0.0.1:端口，而公网上的用户看到的是一个
// 打不开的域名。这个包补的就是这一段。
//
// # 为什么"从本机访问自己的公网域名"是有意义的
//
// 直连模式下它**没有**意义：请求会走 NAT 发夹回到本机，运营商放不放行
// 都会"成功"（见 internal/verify 的说明）。但隧道模式下完全不同 ——
// 请求真的会出机器、到 Cloudflare 边缘、再顺着隧道回来，途经的每一段
// 都是公网用户会经过的那一段。因此 Result.Trustworthy 把这两种情况
// 分开：不可信的结果仍然记录（能说明 DNS 解析和证书是否正常），
// 但不会被当成"公网可达"的证据。
package reachcheck

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Target 是一个要检查的站点。
type Target struct {
	AppID  string
	Name   string
	Domain string
}

// Result 是一次检查的结果。
type Result struct {
	AppID  string `json:"app_id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
	// OK 表示拿到了 2xx/3xx。
	OK         bool `json:"ok"`
	StatusCode int  `json:"status_code,omitempty"`
	// LatencyMS 是从发出请求到拿到响应头的时间。
	LatencyMS int64  `json:"latency_ms,omitempty"`
	CheckedAt string `json:"checked_at"`
	// Error 是失败原因（连接超时、证书错误等）。
	Error string `json:"error,omitempty"`
	// Trustworthy 表示这次检查是否真的走了公网路径。
	//
	// 隧道模式下为 true；没有隧道时为 false（发夹路径什么也证明不了）。
	// 界面据此决定要不要把"可达"当成结论说出去。
	Trustworthy bool `json:"trustworthy"`
	// ConsecutiveFailures 是连续失败的次数。
	//
	// 单次失败很常见（网络抖动、Cloudflare 边缘切换），不值得报警；
	// 连续失败才是"真的坏了"。界面用它决定要不要把这条标红。
	ConsecutiveFailures int `json:"consecutive_failures"`
}

// Config 是探测器需要的外部依赖。
type Config struct {
	// Targets 返回当前要检查的站点。每次检查都重新取：站点会被增删，
	// 拿一份启动时的快照会让新站点永远不被检查。
	Targets func(ctx context.Context) []Target
	// Tunneled 报告当前是否在用隧道发布。
	//
	// 它决定结果可不可信（见包注释）。做成函数而不是一个布尔值：
	// 用户可能中途开关隧道，而检查结果的可信度要跟着变。
	Tunneled func() bool
	// Interval 是检查间隔；<= 0 时用 DefaultInterval。
	Interval time.Duration
	// Timeout 是单次请求的上限。
	Timeout time.Duration
	// Log 是日志。
	Log *slog.Logger
	// Now 便于测试。
	Now func() time.Time
	// Client 覆盖默认的 HTTP 客户端。
	//
	// 存在的理由与其它注入点一样：检查逻辑要能被测到，而测"成功"这条
	// 路径需要访问一个自签证书的测试服务器 —— 那就必须能换掉客户端。
	// 生产路径留空，用下面构造的那个。
	Client *http.Client
}

// DefaultInterval 是默认检查间隔。
//
// 5 分钟：这个检查打的是用户自己的站点（真流量），太频繁没有必要；
// 而"站点挂了多久才发现"的容忍度通常是分钟级而不是秒级。
const DefaultInterval = 5 * time.Minute

// DefaultTimeout 是单次请求的上限。
//
// 10 秒：隧道模式下一次请求要绕 Cloudflare 一圈，本机网络不好时
// 会比局域网慢得多。太短会把"慢"误报成"挂了"，而那是最容易让人
// 不相信这个功能的一类假警报。
const DefaultTimeout = 10 * time.Second

// Prober 定期检查所有站点。
type Prober struct {
	cfg    Config
	log    *slog.Logger
	client *http.Client

	mu      sync.RWMutex
	results map[string]Result
}

// New 构造探测器。
func New(cfg Config) *Prober {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{
			Timeout: cfg.Timeout,
			// 不跟随跳转？**要跟随** —— 用户看到的是浏览器里的最终结果，
			// 而一个 301 到 https 的站点是完全正常的。
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		}
	}
	return &Prober{cfg: cfg, log: log, results: map[string]Result{}, client: client}
}

// Run 周期检查，直到 ctx 结束。
func (p *Prober) Run(ctx context.Context) {
	if p.cfg.Targets == nil {
		return
	}
	// 先等一小会儿再查第一次：内核刚起来时反代和隧道都还在起，
	// 立刻查会得到一片"不可达"，而那是假的。
	select {
	case <-ctx.Done():
		return
	case <-time.After(startDelay):
	}

	p.checkAll(ctx)
	ticker := time.NewTicker(p.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.checkAll(ctx)
		}
	}
}

// startDelay 是启动后第一次检查的延迟。
const startDelay = 30 * time.Second

// checkAll 检查一轮。
func (p *Prober) checkAll(ctx context.Context) {
	targets := p.cfg.Targets(ctx)
	// 串行而不是并发：站点通常只有几个，而并发打自己的站点在隧道模式下
	// 会挤占同一条连接，让每个结果都变慢 —— 那样测出来的是互相干扰，
	// 不是真实延迟。
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		p.check(ctx, target)
	}
}

func (p *Prober) check(ctx context.Context, target Target) {
	trustworthy := p.cfg.Tunneled != nil && p.cfg.Tunneled()
	url := "https://" + target.Domain + "/"

	reqCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		p.record(target, Result{Error: err.Error(), Trustworthy: trustworthy})
		return
	}
	// 只要响应头就够了：这个检查问的是"通不通"，不是"内容对不对"。
	// 拉完整个页面会在站点返回大文件时白白占满带宽。
	req.Header.Set("User-Agent", "ISC-Phecda/reachcheck")

	start := p.cfg.Now()
	resp, err := p.client.Do(req)
	latency := p.cfg.Now().Sub(start).Milliseconds()
	if err != nil {
		p.record(target, Result{Error: describe(err), LatencyMS: latency, Trustworthy: trustworthy})
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()

	p.record(target, Result{
		OK:          resp.StatusCode >= 200 && resp.StatusCode < 400,
		StatusCode:  resp.StatusCode,
		LatencyMS:   latency,
		Trustworthy: trustworthy,
	})
}

// record 写入结果并维护连续失败计数。
func (p *Prober) record(target Target, r Result) {
	r.AppID = target.AppID
	r.Name = target.Name
	r.Domain = target.Domain
	r.CheckedAt = p.cfg.Now().Format(time.RFC3339)

	p.mu.Lock()
	defer p.mu.Unlock()
	if !r.OK {
		// 连续失败才值得报警：单次失败太常见（网络抖动、边缘切换），
		// 每次都报会让用户很快学会忽略这个提示 —— 那比不提示更糟。
		//
		// 第一次失败是 1 而不是 0：写成"有上一次才累加"会让首次失败
		// 显示成"还没失败过"，而那一刻恰恰是用户最需要被告知的。
		r.ConsecutiveFailures = 1
		if prev, ok := p.results[target.Domain]; ok {
			r.ConsecutiveFailures = prev.ConsecutiveFailures + 1
		}
	}
	p.results[target.Domain] = r
	if !r.OK {
		p.log.Warn("site is not reachable from the internet",
			"app_id", r.AppID, "domain", r.Domain,
			"status", r.StatusCode, "err", r.Error, "failures", r.ConsecutiveFailures)
	}
}

// Latest 返回最近一轮的结果。
func (p *Prober) Latest() []Result {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Result, 0, len(p.results))
	for _, r := range p.results {
		out = append(out, r)
	}
	return out
}

// describe 把网络错误翻译成一句用户能用的说明。
//
// 原始错误里最常见的是证书与 DNS 两类，而它们的**下一步动作完全不同**
// （查证书续期 / 查 DNS 记录）。直接抛 Go 的原文会让用户去搜索
// "x509: certificate has expired"。
func describe(err error) string {
	var certErr *tls.CertificateVerificationError
	if ok := asCertError(err, &certErr); ok {
		return "certificate problem: " + certErr.Error()
	}
	return err.Error()
}

func asCertError(err error, target **tls.CertificateVerificationError) bool {
	for err != nil {
		if ce, ok := err.(*tls.CertificateVerificationError); ok {
			*target = ce
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
