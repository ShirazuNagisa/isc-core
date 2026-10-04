package remote

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件把"内核在**用户自己的**域名下建一个指向自己的子域名"这件事做完。
//
// # 它为什么比"再开一个公共服务"好
//
// 用户的域名本来就在那里、本来就有 DNS 服务商凭据、本来就有动态解析。
// 借它建一条随机子域名，就不需要任何人再运行一台 7×24 的会合服务器，
// 也不需要把"谁在连谁"这件事交给第三方。
//
// # 三条硬约束
//
//  1. **只用 AAAA。** 公网 IPv4 在多数家用宽带上已经是大内网（CGNAT），
//     写一条指向共享地址的 A 记录不但没用，还有害：客户端默认先试 IPv4，
//     于是它会先卡在一个永远连不上的地址上。A 记录只在**实测**过
//     IPv4 可达之后才写。
//  2. **只碰自己那一条。** 记录 ID 落库，删除时按 ID 删；改动前先确认
//     那条记录确实是我们建的。用户的区域里有他自己的记录，
//     一次"顺手清理"就可能毁掉他的站点。
//  3. **子域名必须长期不变。** 它一旦变化，手机上的二维码、书签、
//     防火墙规则全部失效。因此随机标签生成一次就落盘，之后永远复用。

// PublicState 是公网面的台账。
//
// 它落在 `<root>/remote/public.json`，而不是设置表里：标签、记录 ID、
// 上次探测结论都是**内核自己的账**，不是用户配置。把它们塞进设置会让
// 设置界面多出一堆用户看不懂也不该改的项。
type PublicState struct {
	// Label 是子域名最左边那一段（不含前缀与区域），例如 a7f3k9x2n4bd5e6f。
	Label string `json:"label"`
	// Zone 是区域名，例如 example.com。
	Zone string `json:"zone"`
	// Records 是"记录类型 → 服务商侧的记录 ID"。
	//
	// 存 ID 而不是存名字：删除与修改都必须**精确**指向我们自己建的那一条，
	// 而按名字找记录会在用户也有一条同名记录时删错对象。
	Records map[string]string `json:"records,omitempty"`
	// Addresses 是我们实际写进去的地址值，便于界面显示与排查。
	Addresses map[string]string `json:"addresses,omitempty"`
	// LastCheck 是最近一次可达性自检的结论。
	LastCheck *PublicCheck `json:"last_check,omitempty"`
	// CreatedAt 是这条子域名第一次生成的时间。
	CreatedAt time.Time `json:"created_at"`
}

// 可达性自检的三种结论。
//
// 三值而不是布尔：**"没测过"与"测了但不通"是两件事**，而用户该做的事
// 完全不同 —— 前者是"去测一下"，后者是"去查路由器"。
const (
	// PublicVerdictUnknown 还没测过，或者测的时候客户端自己就在
	// 同一个局域网里（那时"连上了"是假阳性，什么也证明不了）。
	PublicVerdictUnknown = "unknown"
	// PublicVerdictReachable 外部客户端真的连上了。
	PublicVerdictReachable = "reachable"
	// PublicVerdictUnreachable 外部客户端连不上。
	PublicVerdictUnreachable = "unreachable"
)

// PublicCheck 是一次可达性自检的结论。
type PublicCheck struct {
	At time.Time `json:"at"`
	// Verdict 是 reachable / unreachable / unknown 之一。
	Verdict string `json:"verdict"`
	// Detail 是给用户看的一句话。
	Detail string `json:"detail"`
	// Family 是被验证的地址族（ipv6 / ipv4）。
	Family string `json:"family,omitempty"`
}

// Host 返回完整的子域名。
func (s PublicState) Host() string {
	if s.Label == "" || s.Zone == "" {
		return ""
	}
	return s.Label + "." + s.Zone
}

// Empty 报告台账是否还没有内容。
func (s PublicState) Empty() bool { return s.Label == "" }

// DNSWriter 是公网面用到的 DNS 写操作。
//
// 定义成接口而不是直接依赖 `*dns.Service`：测试里要能注入一个假的，
// 而那个假实现能在**不改真实 DNS** 的前提下断言"我们只碰了自己那条记录"
// —— 这一条恰恰是最需要被钉住的。
type DNSWriter interface {
	ListZones(ctx context.Context, credentialID string) ([]dns.Zone, error)
	ListRecords(ctx context.Context, credentialID, zoneID string, filter dns.RecordFilter) ([]dns.Record, error)
	CreateRecord(ctx context.Context, credentialID, zoneID string, rec dns.Record) (dns.Record, error)
	UpdateRecord(ctx context.Context, credentialID, zoneID, recordID string, rec dns.Record) (dns.Record, error)
	DeleteRecord(ctx context.Context, credentialID, zoneID, recordID string) error
}

// PublicIPProber 告诉我们"外网看到的我们"是什么地址。
type PublicIPProber interface {
	// PublicIPv4 返回外网看到的 IPv4。没有 IPv4 出口时返回错误。
	PublicIPv4(ctx context.Context) (net.IP, error)
}

// DNSLabelPrefix 是子域名的固定前缀。
//
// 固定而不是可配：它唯一的用途是让人一眼看出这条记录是谁建的，
// 以及让清理逻辑有一致的判据。可配只会让"我该填什么"变成一个新问题。
const DNSLabelPrefix = "mizar-"

// crockford 是 Crockford base32 的字母表。
//
// 去掉 I / L / O / U 的理由和配对码一样：它们与 1 / 0 以及彼此
// 在手写、口述、某些字体下无法区分。这里虽然主要给机器读，
// 但用户排查问题时**会**把它抄下来。
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// LabelEntropy 是随机标签的字符数。
//
// 16 个 Crockford 字符 = 80 位。它防的是**撞名**（两个用户在同一台
// 机器上、或同一个人重装后），不是被发现 —— 域名会出现在
// Certificate Transparency 日志里，全世界都能查到。
const LabelEntropy = 16

// NewPublicLabel 生成一个随机标签（不含前缀）。
//
// reader 为 nil 时用 crypto/rand。
func NewPublicLabel(reader io.Reader) (string, error) {
	if reader == nil {
		reader = rand.Reader
	}
	buf := make([]byte, LabelEntropy)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", fmt.Errorf(i18n.T("remote.public.err.rand"), err)
	}
	// 用取模会有偏（256 不是 32 的倍数？—— 是，因此这里其实无偏；
	// 但保留掩码写法，将来换字母表长度时不会悄悄引入偏差）。
	out := make([]byte, LabelEntropy)
	for i, b := range buf {
		out[i] = crockford[b&0x1f]
	}
	// DNS 大小写不敏感，但小写是惯例，也让"抄下来"时不会看错。
	return strings.ToLower(string(out)), nil
}

// PublicFace 负责子域名的生成、记录的生命周期与台账的持久化。
type PublicFace struct {
	path   string
	writer DNSWriter
	prober PublicIPProber
	now    func() time.Time

	mu    sync.Mutex
	state PublicState
	// loaded 记录是否已经从磁盘读过。
	loaded bool
}

// NewPublicFace 构造公网面。
//
// writer 为 nil 时所有需要 DNS 的操作都会明确报错，而不是静默失败 ——
// "公网访问开着但什么都没建"是最难查的一种状态。
func NewPublicFace(dir string, writer DNSWriter, prober PublicIPProber) *PublicFace {
	return &PublicFace{
		path:   filepath.Join(dir, "public.json"),
		writer: writer,
		prober: prober,
		now:    time.Now,
	}
}

// State 返回台账的副本。
func (p *PublicFace) State() PublicState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.loaded {
		p.load()
	}
	return p.state
}

// load 从磁盘读台账。调用方必须持有锁。
func (p *PublicFace) load() {
	p.loaded = true
	raw, err := os.ReadFile(p.path)
	if err != nil {
		// 不存在是正常状态（还没配过）。其它读取错误也让 State 返回空 ——
		// 上层会因此走"重新生成"的路径，而那条路是幂等的。
		return
	}
	var state PublicState
	if json.Unmarshal(raw, &state) != nil {
		return
	}
	p.state = state
}

// save 把台账写盘。调用方必须持有锁。
func (p *PublicFace) save() error {
	raw, err := json.MarshalIndent(p.state, "", "  ")
	if err != nil {
		return fmt.Errorf(i18n.T("remote.public.err.save"), err)
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return fmt.Errorf(i18n.T("remote.public.err.save"), err)
	}
	// 0600：内容不是秘密（域名本来就公开），但它属于内核自己的账，
	// 没有理由让同机其它用户改动它 —— 改掉标签就等于把手机指向别处。
	if err := os.WriteFile(p.path, raw, 0o600); err != nil {
		return fmt.Errorf(i18n.T("remote.public.err.save"), err)
	}
	return nil
}

// EnsureLabel 保证台账里有一个标签，返回完整的子域名。
//
// **已经生成过就永远复用。** 标签一变，手机上的配对、书签、
// 防火墙规则全部失效 —— 而用户不会知道为什么。
func (p *PublicFace) EnsureLabel(zone string) (string, error) {
	zone = strings.TrimSpace(strings.TrimSuffix(zone, "."))
	if zone == "" {
		return "", errors.New(i18n.T("remote.public.err.no_zone"))
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.loaded {
		p.load()
	}

	if p.state.Label != "" && p.state.Zone == zone {
		return p.state.Host(), nil
	}

	// 区域变了（用户换了域名）→ 旧标签作废，重新生成一个。
	// 沿用旧标签会让"换了域名"看起来像"域名没生效"。
	label, err := NewPublicLabel(nil)
	if err != nil {
		return "", err
	}
	p.state = PublicState{
		Label:     DNSLabelPrefix + label,
		Zone:      zone,
		Records:   map[string]string{},
		Addresses: map[string]string{},
		CreatedAt: p.now().UTC(),
	}
	if err := p.save(); err != nil {
		return "", err
	}
	return p.state.Host(), nil
}

// PublicPlan 描述一次"把子域名指向本机"的意图。
type PublicPlan struct {
	CredentialID string
	ZoneID       string
	Zone         string
	// IPv6 是要写进 AAAA 的地址。为空表示本机没有可用的公网 IPv6。
	IPv6 net.IP
	// IPv4 是要写进 A 的地址。为空表示**不写 A**。
	//
	// 调用方必须只在实测过 IPv4 可达之后才填它 —— 写一条连不上的 A
	// 记录比不写更糟，因为客户端默认先试 IPv4。
	IPv4 net.IP
}

// Apply 让 DNS 记录与 plan 一致。
//
// 它只碰台账里记着的那几条记录：先按 ID 更新，ID 不存在才新建。
// 用户的区域里有他自己的记录，因此**任何按名字搜索再改写的做法都是
// 不可接受的** —— 一次重名就可能改掉他的站点。
func (p *PublicFace) Apply(ctx context.Context, plan PublicPlan) (PublicState, error) {
	if p.writer == nil {
		return PublicState{}, errors.New(i18n.T("remote.public.err.no_writer"))
	}
	if strings.TrimSpace(plan.CredentialID) == "" || strings.TrimSpace(plan.ZoneID) == "" {
		return PublicState{}, errors.New(i18n.T("remote.public.err.no_zone"))
	}

	host, err := p.EnsureLabel(plan.Zone)
	if err != nil {
		return PublicState{}, err
	}

	// 想写什么，按记录类型列出来。
	want := map[dns.RecordType]string{}
	if plan.IPv6 != nil {
		want[dns.TypeAAAA] = plan.IPv6.String()
	}
	if plan.IPv4 != nil {
		want[dns.TypeA] = plan.IPv4.String()
	}
	// 没有 IPv6 时不写 AAAA —— 但也不删已有的，见下。
	if len(want) == 0 {
		return p.State(), errors.New(i18n.T("remote.public.err.no_address"))
	}

	for rtype, value := range want {
		if err := p.upsert(ctx, plan, host, rtype, value); err != nil {
			return p.State(), err
		}
	}

	// 之前写过、这次不再需要的类型要删掉。
	//
	// 最实际的场景：IPv4 那次是通的（写了 A），后来宽带换成了大内网
	// （A 不再可达）—— 那条 A 记录必须消失，否则手机会一直先试 IPv4。
	p.mu.Lock()
	stale := make([]dns.RecordType, 0, 2)
	for key := range p.state.Records {
		if _, keep := want[dns.RecordType(key)]; !keep {
			stale = append(stale, dns.RecordType(key))
		}
	}
	p.mu.Unlock()

	for _, rtype := range stale {
		if err := p.deleteRecord(ctx, plan, rtype); err != nil {
			return p.State(), err
		}
	}

	return p.State(), nil
}

// upsert 写一条记录：有 ID 就更新，没有就新建。
func (p *PublicFace) upsert(ctx context.Context, plan PublicPlan, host string,
	rtype dns.RecordType, value string) error {

	p.mu.Lock()
	if !p.loaded {
		p.load()
	}
	existingID := p.state.Records[string(rtype)]
	// 已经写对了就不动 —— 每次同步都发一次写请求会让服务商的
	// 审计日志里堆满无意义的改动。
	same := p.state.Addresses[string(rtype)] == value
	p.mu.Unlock()

	if existingID != "" && same {
		return nil
	}

	rec := dns.Record{
		Name:    host,
		Type:    rtype,
		Content: value,
		// TTL 60：地址轮换（ISP 重新分配前缀）之后，缓存最多拖一分钟。
		// 更长会让"换了地址、手机连不上"持续几十分钟。
		TTL: 60,
	}

	var (
		saved dns.Record
		err   error
	)
	if existingID == "" {
		saved, err = p.writer.CreateRecord(ctx, plan.CredentialID, plan.ZoneID, rec)
	} else {
		saved, err = p.writer.UpdateRecord(ctx, plan.CredentialID, plan.ZoneID, existingID, rec)
		if err != nil {
			// 记录被用户在服务商后台删掉了：ID 还在我们账上，但对象没了。
			// 这时**新建**才是对的，而不是把错误抛给用户。
			saved, err = p.writer.CreateRecord(ctx, plan.CredentialID, plan.ZoneID, rec)
		}
	}
	if err != nil {
		return fmt.Errorf(i18n.T("remote.public.err.write"), host, err)
	}

	id := saved.ID
	if id == "" {
		id = existingID
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.Records == nil {
		p.state.Records = map[string]string{}
	}
	if p.state.Addresses == nil {
		p.state.Addresses = map[string]string{}
	}
	p.state.Records[string(rtype)] = id
	p.state.Addresses[string(rtype)] = value
	return p.save()
}

// deleteRecord 按 ID 删掉自己那一条。
func (p *PublicFace) deleteRecord(ctx context.Context, plan PublicPlan, rtype dns.RecordType) error {
	p.mu.Lock()
	if !p.loaded {
		p.load()
	}
	id := p.state.Records[string(rtype)]
	p.mu.Unlock()

	if id != "" && p.writer != nil {
		// 删除失败不阻断：记录可能已经被用户在服务商后台删掉了。
		// 台账照清，下次 Apply 会重新建。
		_ = p.writer.DeleteRecord(ctx, plan.CredentialID, plan.ZoneID, id)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.state.Records, string(rtype))
	delete(p.state.Addresses, string(rtype))
	return p.save()
}

// Teardown 删掉我们建的全部记录，并清空台账。
//
// 用户关闭公网访问时调用。**只删台账里记着的那些 ID** ——
// 绝不按名字或前缀去"扫一遍然后清理"：用户的区域里可能有
// 别人建的、名字恰好也以 mizar- 开头的记录。
func (p *PublicFace) Teardown(ctx context.Context, credentialID, zoneID string) error {
	p.mu.Lock()
	if !p.loaded {
		p.load()
	}
	ids := make(map[string]string, len(p.state.Records))
	for k, v := range p.state.Records {
		ids[k] = v
	}
	p.mu.Unlock()

	// 无论删除成功与否，台账都要清空（否则这个开关就关不掉了），
	// 但失败会被报出去：用户的区域里可能留下一条指向"已经不再服务"
	// 的地址的记录，界面应当据此提示他去 DNS 后台确认。
	var firstErr error
	for _, id := range ids {
		if id == "" || p.writer == nil {
			continue
		}
		if err := p.writer.DeleteRecord(ctx, credentialID, zoneID, id); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.Records = map[string]string{}
	p.state.Addresses = map[string]string{}
	if err := p.save(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// SetCheck 记录一次可达性自检的结论。
func (p *PublicFace) SetCheck(check PublicCheck) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.loaded {
		p.load()
	}
	check.At = p.now().UTC()
	p.state.LastCheck = &check
	return p.save()
}

// ---------------------------------------------------------------------------
// 公网 IPv4 探测
// ---------------------------------------------------------------------------

// EchoEndpoints 是默认的公网 IP 回显服务。
//
// 用两个而不是一个：单个服务挂了会让"探测失败"看起来像"网络不通"，
// 而这两件事用户该做的事完全不同。
var EchoEndpoints = []string{
	"https://api.ipify.org",
	"https://ipv4.icanhazip.com",
}

// HTTPProber 通过公网回显服务探测本机的公网 IPv4。
type HTTPProber struct {
	Endpoints []string
	Client    *http.Client
}

// NewHTTPProber 构造一个探测器。
func NewHTTPProber() *HTTPProber {
	return &HTTPProber{
		Endpoints: EchoEndpoints,
		Client: &http.Client{
			Timeout: 8 * time.Second,
			Transport: &http.Transport{
				// **强制 IPv4**：这些端点都有 AAAA 记录，而默认的
				// Happy Eyeballs 会先走 IPv6 —— 那样拿到的就是 IPv6
				// 出口地址，不是我们要的 IPv4。
				//
				// 用 tcp4 而不是只靠端点没有 AAAA：端点什么时候加
				// AAAA 不由我们决定，而那时这个函数会安静地返回一个
				// IPv4 解析失败的奇怪结果。
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp4", addr)
				},
			},
		},
	}
}

// PublicIPv4 返回外网看到的 IPv4。
func (h *HTTPProber) PublicIPv4(ctx context.Context) (net.IP, error) {
	client := h.Client
	if client == nil {
		client = NewHTTPProber().Client
	}
	endpoints := h.Endpoints
	if len(endpoints) == 0 {
		endpoints = EchoEndpoints
	}

	var lastErr error
	for _, endpoint := range endpoints {
		ip, err := h.probe(ctx, client, endpoint)
		if err == nil {
			return ip, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf(i18n.T("remote.public.err.probe"), lastErr)
}

func (h *HTTPProber) probe(ctx context.Context, client *http.Client, endpoint string) (net.IP, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // 只读响应体

	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(strings.TrimSpace(string(body)))
	if ip == nil {
		return nil, fmt.Errorf(i18n.T("remote.public.err.probe_body"), string(body))
	}
	if ip.To4() == nil {
		return nil, fmt.Errorf(i18n.T("remote.public.err.probe_not_v4"), ip)
	}
	return ip.To4(), nil
}
