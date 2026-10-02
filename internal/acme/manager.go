package acme

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/event"
)

// 本文件是证书的**生命周期管理**：什么时候该申请、什么时候该续期、
// 什么时候什么都不用做。
//
// # 判断的分量
//
// 这三件事的时机都很要紧，而且是**双向**的：
//
//	续期太晚    证书过期 → 站点在某天突然打不开，而那天你可能正在外面
//	续期太早    浪费失败配额。Let's Encrypt 生产环境的失败配额是
//	            每小时 5 次，反复重试很容易把它用光，而用光之后要等一小时
//
// 因此判断依据必须是**证书本身的有效期**，而不是"距离上次签发过了多久"。

// RenewBeforeRatio 是续期阈值：剩余有效期低于总有效期的这个比例时续期。
//
// 三分之一是业界惯例，对 Let's Encrypt 的 90 天证书就是提前 30 天。
// 它留出了足够多的重试机会：即使连续几天续期失败，也还有时间让用户
// 发现问题并处理。
const RenewBeforeRatio = 3

// minRenewBefore 是续期阈值下的下限。
//
// 有些 CA 签发很短期的证书（例如 7 天）。按三分之一算就只有 2.3 天，
// 而一次失败加上周末就可能过期。下限保证任何证书都至少有这么多余量。
const minRenewBefore = 3 * 24 * time.Hour

// CertRequest 描述"需要一张覆盖这些域名的证书"。
type CertRequest struct {
	// Domains 是证书要覆盖的域名。
	Domains []string `json:"domains"`
	// CredentialID 是做 DNS-01 校验用的凭据。
	CredentialID string `json:"credential_id"`
}

// Key 返回这个请求的稳定标识。
//
// 用**排序后的域名**：证书覆盖哪些域名与顺序无关，而用户调换顺序
// 不该被当成"换了一张证书"从而触发一次无谓的签发。
func (r CertRequest) Key() string {
	domains := append([]string(nil), r.Domains...)
	for i := range domains {
		domains[i] = strings.ToLower(strings.TrimSpace(domains[i]))
	}
	sort.Strings(domains)
	return strings.Join(domains, ",")
}

// Normalize 清理域名列表。
func (r CertRequest) Normalize() CertRequest {
	seen := make(map[string]bool)
	out := make([]string, 0, len(r.Domains))
	for _, d := range r.Domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Strings(out)
	return CertRequest{Domains: out, CredentialID: r.CredentialID}
}

// Validate 检查请求是否可用。
func (r CertRequest) Validate() error {
	if len(r.Domains) == 0 {
		return errors.New(i18n.T("acme.need_domain"))
	}
	if r.CredentialID == "" {
		return errors.New(i18n.T("acme.need_cred"))
	}
	for _, d := range r.Domains {
		if err := validateDomain(d); err != nil {
			return err
		}
	}
	return nil
}

// validateDomain 做基本的域名格式检查。
//
// 只挡明显不合法的形式。真正的校验是 ACME 服务器做的 ——
// 它才知道这个域名是否可签发。
func validateDomain(d string) error {
	d = strings.TrimPrefix(d, "*.")
	if d == "" {
		return errors.New(i18n.T("acme.manager.empty_domain"))
	}
	if strings.ContainsAny(d, " /\\") {
		return fmt.Errorf(i18n.T("acme.manager.bad_chars"), d)
	}
	if !strings.Contains(d, ".") {
		// 单标签域名（例如 "localhost"）无法通过 ACME 签发 ——
		// 公网 CA 不为它们签证书。早一点说清楚比让用户等一轮校验好。
		return fmt.Errorf(
			i18n.T("acme.manager.single_label"), d)
	}
	if strings.Contains(d, "*") {
		return fmt.Errorf(
			i18n.T("acme.manager.wildcard_pos"), d)
	}
	return nil
}

// CertStatus 是一张证书的当前状态。
type CertStatus struct {
	// Name 是证书在存储里的名字。
	Name string `json:"name"`
	// Domains 是它覆盖的域名。
	Domains []string `json:"domains"`
	// IssuedAt / ExpiresAt 是有效期。
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Staging 标记它是否来自测试环境。
	//
	// 必须暴露出来：用测试环境签的证书**不被浏览器信任**，
	// 而用户在界面上只会看到"证书无效"。
	Staging bool `json:"staging"`
	// NeedsRenew 表示是否需要（重新）签发。
	NeedsRenew bool `json:"needs_renew"`
	// Reason 解释为什么需要续期。
	Reason string `json:"reason,omitempty"`
	// Error 是最近一次签发失败的原因。
	Error string `json:"error,omitempty"`
}

// Manager 管理证书的申请与续期。
type Manager struct {
	store *Store
	bus   *event.Bus
	log   *slog.Logger

	// newClient 按需构造 ACME 客户端。
	//
	// 做成工厂而不是持有一个：DNS-01 用的凭据取决于请求，
	// 而客户端与求解器是绑定的。
	newClient func(req CertRequest) (*Client, error)

	// checkInterval 是定期检查的间隔。
	checkInterval time.Duration

	// now 便于测试控制时间。
	now func() time.Time

	// email 是 ACME 账户的联系邮箱。
	email string

	mu       sync.Mutex
	inflight map[string]bool
	lastErr  map[string]string
}

// NewManager 构造证书管理器。
func NewManager(store *Store, newClient func(CertRequest) (*Client, error),
	bus *event.Bus, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		store:         store,
		bus:           bus,
		log:           log,
		newClient:     newClient,
		checkInterval: 12 * time.Hour,
		now:           func() time.Time { return time.Now().UTC() },
		inflight:      make(map[string]bool),
		lastErr:       make(map[string]string),
	}
}

// SetBus 设置事件总线。
//
// 单独一步而不是构造参数：总线依赖设置（缓冲容量），而设置在本包
// 之前就要加载完。与其把构造顺序扭成一个环，不如留一个显式的回填点。
func (m *Manager) SetBus(bus *event.Bus) { m.bus = bus }

// SetCheckInterval 覆盖检查间隔，供测试使用。
func (m *Manager) SetCheckInterval(d time.Duration) {
	if d > 0 {
		m.checkInterval = d
	}
}

// ---------------------------------------------------------------------------
// 续期判断
// ---------------------------------------------------------------------------

// RenewDecision 解释为什么要（或不要）续期。
//
// 把理由单独返回而不是只给一个 bool：用户在界面上看到"需要续期"时
// 第一个问题就是"为什么"。少了理由，他只能猜。
func RenewDecision(cert Cert, want []string, now time.Time) (bool, string) {
	// --- 1. 域名覆盖 ---
	//
	// 放在最前面：用户给一条路由加了域名之后，现有证书不覆盖新域名，
	// 而**这与剩余有效期无关** —— 证书可能刚签了三天。
	// 只按时间判断的话，新增的域名要等三个月才会生效。
	if missing := missingDomains(cert.Domains, want); len(missing) > 0 {
		return true, fmt.Sprintf(i18n.T("acme.manager.not_covering"), strings.Join(missing, "、"))
	}

	// --- 2. 有效期 ---
	if cert.ExpiresAt.IsZero() {
		// 读不出有效期（用户手工拷来的文件可能格式特殊）。
		//
		// 这里选择**续期**而不是跳过：跳过会让一张读不出有效期的
		// 证书一直用到过期，而那时站点会突然打不开。
		return true, i18n.T("acme.manager.no_expiry")
	}

	remaining := cert.ExpiresAt.Sub(now)
	if remaining <= 0 {
		return true, fmt.Sprintf(i18n.T("acme.manager.expired"),
			cert.ExpiresAt.Format("2006-01-02"))
	}

	threshold := renewalThreshold(cert)
	if remaining <= threshold {
		return true, fmt.Sprintf(i18n.T("acme.manager.below_threshold"),
			int(remaining.Hours()/24), int(threshold.Hours()/24))
	}

	return false, ""
}

// fallbackThreshold 是读不出有效期时的保守阈值。
//
// 30 天 = Let's Encrypt 的 90 天除以 3。对更短期的证书来说这个值
// 偏大（会早续），而早续的代价只是多用一次配额，比过期小得多。
const fallbackThreshold = 30 * 24 * time.Hour

// renewalThreshold 计算一张证书的续期阈值。
func renewalThreshold(cert Cert) time.Duration {
	// 签发时间为零时必须走兜底。
	//
	// 只看 `lifetime <= 0` 是不够的：零值时间的 Unix 时间是 1 年 1 月，
	// 于是 `ExpiresAt.Sub(IssuedAt)` 会得到一个**约 97 年**的寿命，
	// 阈值随之变成一个永远达不到的数 —— 表现为"证书永远不续期"，
	// 直到它过期。
	if cert.IssuedAt.IsZero() || cert.ExpiresAt.IsZero() {
		return fallbackThreshold
	}

	lifetime := cert.ExpiresAt.Sub(cert.IssuedAt)
	if lifetime <= 0 {
		return fallbackThreshold
	}

	threshold := lifetime / RenewBeforeRatio
	if threshold < minRenewBefore {
		threshold = minRenewBefore
	}
	// 阈值不能超过总寿命的一半，否则短期证书会陷入"刚签完就要续"的循环。
	if half := lifetime / 2; threshold > half {
		threshold = half
	}
	return threshold
}

// missingDomains 返回 want 里有而 have 里没有的域名。
func missingDomains(have, want []string) []string {
	if len(want) == 0 {
		return nil
	}

	covered := make(map[string]bool, len(have))
	for _, d := range have {
		covered[strings.ToLower(d)] = true
	}

	var out []string
	for _, w := range want {
		w = strings.ToLower(w)
		if covered[w] {
			continue
		}
		// 通配证书覆盖它的任意一级子域名，因此子域名算已覆盖。
		if wildcardCovers(covered, w) {
			continue
		}
		out = append(out, w)
	}
	return out
}

// wildcardCovers 报告已覆盖的集合里是否有能覆盖该域名的通配项。
//
// 只匹配**一级**：*.example.com 覆盖 a.example.com，但不覆盖
// a.b.example.com —— 这与 TLS 证书的通配规则一致。
func wildcardCovers(covered map[string]bool, domain string) bool {
	idx := strings.Index(domain, ".")
	if idx < 0 {
		return false
	}
	parent := domain[idx+1:]
	return covered["*."+parent]
}

// ---------------------------------------------------------------------------
// 申请与续期
// ---------------------------------------------------------------------------

// Ensure 保证某张证书存在且有效；需要时立即签发。
//
// 返回的 bool 表示这次是否真的签发了（false = 现有证书仍然可用）。
func (m *Manager) Ensure(ctx context.Context, req CertRequest) (CertStatus, bool, error) {
	req = req.Normalize()
	if err := req.Validate(); err != nil {
		return CertStatus{}, false, err
	}

	name := certName(req.Domains)

	// 防重入：同一个域名集合同时只允许一次签发。
	//
	// 并发的签发会创建两个 ACME 订单，而**失败配额是按订单计的**。
	// 并发的重试很容易把配额用光，而用光之后要等一小时。
	if !m.acquire(name) {
		return CertStatus{}, false, fmt.Errorf(
			i18n.T("acme.manager.issuing"), name)
	}
	defer m.release(name)

	// 先看现有证书够不够用。
	//
	// hadCert 记录"这次之前是否已经有一张证书"，用来区分
	// **首次签发**与**续期** —— 两者发出的事件不同。
	var hadCert bool

	if cur, err := m.store.Load(name); err == nil {
		hadCert = true

		need, reason := RenewDecision(cur, req.Domains, m.now())
		if !need {
			return statusOf(name, cur, false, "", ""), false, nil
		}
		m.log.Info("证书需要续期", "name", name, "reason", reason)
	} else if !errors.Is(err, os.ErrNotExist) {
		// 读取失败的原因不是"不存在"时仍然继续尝试签发 ——
		// 那比直接失败更可能让用户恢复到可用状态。
		//
		// **但也算"已有证书"**：文件在那里，只是读不出来。
		// 把它当成首次签发会让用户收到一条"证书已签发"，
		// 而他期待的是"续期失败"的告警。
		hadCert = true
		m.log.Warn("读取现有证书失败，将尝试重新签发", "name", name, "err", err)
	}

	// 签发。
	client, err := m.newClient(req)
	if err != nil {
		m.recordErr(name, err)
		return CertStatus{Name: name, Domains: req.Domains, NeedsRenew: true, Error: err.Error()},
			false, err
	}

	m.log.Info("开始签发证书", "name", name, "domains", req.Domains)

	res, err := client.Obtain(ctx, Config{
		Domains: req.Domains,
		Email:   m.email,
	})
	if err != nil {
		m.recordErr(name, err)
		m.publish(event.TypeCertFailed, name, req.Domains, err)
		return CertStatus{Name: name, Domains: req.Domains, NeedsRenew: true, Error: err.Error()},
			false, err
	}

	m.clearErr(name)

	// 首次签发与续期发**不同的事件**。
	//
	// 早先只发 TypeCertIssued，而 TypeCertRenewed 定义了却从未被发出来 ——
	// 于是"证书到期前自动续期并推送 cert.renewed"这条验收标准实际上
	// 满足不了：用户配了只收续期通知的规则，什么都不会收到。
	//
	// 这个缺口是对照验收标准逐条核实时发现的，而不是从代码里看出来的 ——
	// 常量存在、名字也对，只有"从来没有地方发出它"这一点能说明问题。
	evt := event.TypeCertIssued
	if hadCert {
		evt = event.TypeCertRenewed
	}
	m.publish(evt, name, req.Domains, nil)

	return statusOf(name, res.Cert, true, "", ""), true, nil
}

// email 是 ACME 账户的联系邮箱，由调用方通过 SetEmail 设置。
func (m *Manager) SetEmail(email string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.email = email
}

// Status 列出全部已保存证书的状态。
//
// want 是可选的"期望覆盖的域名"映射（证书名 → 域名列表），
// 用于判断现有证书是否还够用。传 nil 时只按有效期判断。
func (m *Manager) Status(want map[string][]string) ([]CertStatus, error) {
	names, err := m.store.List()
	if err != nil {
		return nil, err
	}

	out := make([]CertStatus, 0, len(names))
	for _, name := range names {
		cert, err := m.store.Load(name)
		if err != nil {
			out = append(out, CertStatus{
				Name: name, NeedsRenew: true,
				Error: i18n.T("acme.manager.read_failed") + err.Error(),
			})
			continue
		}

		need, reason := RenewDecision(cert, want[name], m.now())
		out = append(out, statusOf(name, cert, need, reason, m.lastError(name)))
	}
	return out, nil
}

// NeedsWork 返回需要签发或续期的证书。
//
// 它是 Run 的核心，单独抽出来是为了让它能被直接测试 ——
// "该不该动手"这个判断比"怎么动手"更需要被钉住。
func (m *Manager) NeedsWork(reqs []CertRequest) ([]CertRequest, error) {
	// 按证书名去重：两条路由可能落在同一个域名集合上
	//（例如分别配了 example.com 与 www.example.com 各自一条，
	// 而它们其实可以共用一张证书）。
	seen := make(map[string]bool)
	var out []CertRequest

	for _, req := range reqs {
		req = req.Normalize()
		if err := req.Validate(); err != nil {
			m.log.Warn("跳过不合法的证书请求", "domains", req.Domains, "err", err)
			continue
		}

		name := certName(req.Domains)
		if seen[name] {
			continue
		}
		seen[name] = true

		cur, err := m.store.Load(name)
		if err != nil {
			// 证书不存在 —— 需要签发。
			out = append(out, req)
			continue
		}
		if need, _ := RenewDecision(cur, req.Domains, m.now()); need {
			out = append(out, req)
		}
	}
	return out, nil
}

// Run 定期检查并续期，直到 ctx 被取消。
func (m *Manager) Run(ctx context.Context, reqs func() []CertRequest) error {
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()

	// 启动时先检查一次：内核可能停机了很久，期间证书已经进入续期窗口。
	m.sweep(ctx, reqs)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.sweep(ctx, reqs)
		}
	}
}

func (m *Manager) sweep(ctx context.Context, reqs func() []CertRequest) {
	if reqs == nil {
		return
	}

	pending, err := m.NeedsWork(reqs())
	if err != nil {
		m.log.Error("检查证书状态失败", "err", err)
		return
	}
	if len(pending) == 0 {
		return
	}

	m.log.Info("有证书需要签发或续期", "count", len(pending))
	for _, req := range pending {
		if ctx.Err() != nil {
			return
		}
		// 单个失败不中断其余的：一个域名配错了不该让其它域名
		// 也拿不到证书。
		if _, _, err := m.Ensure(ctx, req); err != nil {
			m.log.Error("证书签发失败",
				"domains", req.Domains, "err", err)
		}
	}
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

func (m *Manager) acquire(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight[name] {
		return false
	}
	m.inflight[name] = true
	return true
}

func (m *Manager) release(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.inflight, name)
}

func (m *Manager) recordErr(name string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastErr[name] = err.Error()
}

func (m *Manager) clearErr(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.lastErr, name)
}

func (m *Manager) lastError(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr[name]
}

func (m *Manager) publish(typ, name string, domains []string, err error) {
	if m.bus == nil {
		return
	}
	payload := map[string]any{"name": name, "domains": domains}
	if err != nil {
		payload["error"] = err.Error()
	}
	m.bus.Publish(typ, payload)
}

func statusOf(name string, cert Cert, needs bool, reason, lastErr string) CertStatus {
	return CertStatus{
		Name:       name,
		Domains:    cert.Domains,
		IssuedAt:   cert.IssuedAt,
		ExpiresAt:  cert.ExpiresAt,
		Staging:    cert.Staging,
		NeedsRenew: needs,
		Reason:     reason,
		Error:      lastErr,
	}
}
