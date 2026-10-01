// Package verify 提供"引导式外部验证"。
//
// # 它解决什么问题
//
// 前面的可达性探测能确认本机配置是否正确，但**无法**回答最后一个问题：
// 从外面到底能不能连上。原因见 reach 包的说明 —— 从本机访问自己的
// 公网地址通常走回环（NAT 发夹），无论运营商是否放行都会"成功"。
//
// 于是唯一可靠的办法是让一台**真正在外面的设备**去访问一次。
// 用户手边就有这样一台设备：他的手机（关掉 Wi-Fi，走 4G/5G）。
//
// # 它凭什么能区分"本机没通"与"运营商封了"
//
// 本包不猜，它靠**来源地址**判断：
//
//   - 请求来自公网地址  → 外部确实能连上，链路是通的
//   - 请求只来自本机/内网地址 → 那是发夹路径，**什么也证明不了**
//   - 一直没有请求      → 结合本机检测全绿，结论指向上游封禁
//
// 第二类单独成一档而不是并进"成功"，是这里最关键的设计：把它当成成功
// 会让用户以为已经通了，然后在真正的手机访问失败时彻底摸不着头脑。
package verify

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Status 是一次验证的状态。
type Status string

const (
	// StatusWaiting 正在等待外部访问。
	StatusWaiting Status = "waiting"
	// StatusReachable 收到了来自公网的访问 —— 链路确实通。
	StatusReachable Status = "reachable"
	// StatusHairpinOnly 只收到了来自本机或内网的访问。
	//
	// **这什么也证明不了**：用户在自己电脑上打开那个地址、或者在同一
	// 个局域网内用手机（还连着 Wi-Fi）打开，都会走发夹路径或内网直连，
	// 而那条路径不经过运营商。这条记录的用户提示必须说清楚。
	StatusHairpinOnly Status = "hairpin_only"
	// StatusUnreachable 超时，始终没有收到任何访问。
	StatusUnreachable Status = "unreachable"
	// StatusStopped 被用户主动停止。
	StatusStopped Status = "stopped"
)

// SourceKind 是访问来源的分类。
type SourceKind string

const (
	// SourcePublic 公网地址 —— 唯一能证明链路可用的来源。
	SourcePublic SourceKind = "public"
	// SourceSelf 请求来自**本机自己拥有的地址**。
	//
	// 这一档是必需的，而且是最容易漏掉的一档：IPv6 没有 NAT，
	// 从本机访问自己的公网 IPv6 地址时，连接是直连的，
	// 来源地址就是那个**全局单播地址** —— 光看地址类型会判成公网。
	//
	// 真机上实测到过：用户"在自己电脑上试一下"，页面显示"链路是通的"，
	// 而他关掉电脑去打手机时才发现根本连不上。
	SourceSelf SourceKind = "self"
	// SourceLoopback 本机回环。
	SourceLoopback SourceKind = "loopback"
	// SourcePrivate 内网（含运营商级 NAT 网段、IPv6 ULA）。
	SourcePrivate SourceKind = "private"
	// SourceLinkLocal 链路本地。
	SourceLinkLocal SourceKind = "link_local"
)

// ClassifySource 判断一个来源地址能不能证明链路可用。
//
// isSelf 报告该地址是否是本机自己拥有的地址。传 nil 表示无法判断，
// 此时只按地址类型分类 —— 但调用方应当知道那样会漏掉"自己访问自己"
// 这一情形（见 SourceSelf 的说明）。
func ClassifySource(addr netip.Addr, isSelf func(netip.Addr) bool) SourceKind {
	addr = addr.Unmap()

	// 先做"是不是自己"的判断，再做类型判断。
	//
	// 顺序不能反：本机的公网 IPv6 地址在类型上就是公网地址，
	// 类型判断会把它放行。
	if isSelf != nil && isSelf(addr) {
		return SourceSelf
	}

	switch {
	case addr.IsLoopback():
		return SourceLoopback
	case addr.IsLinkLocalUnicast(), addr.IsLinkLocalMulticast():
		return SourceLinkLocal
	case addr.IsPrivate():
		return SourcePrivate
	case isCarrierNAT(addr):
		// 100.64.0.0/10（RFC 6598）：运营商级 NAT 网段。
		//
		// netip 的 IsPrivate() **不**覆盖它，而它是家宽场景里最常见的
		// "看起来像公网地址"的地址 —— 把来自它的请求当成公网访问，
		// 会让用户在自己家里得到"验证成功"，然后在外网彻底连不上。
		return SourcePrivate
	default:
		return SourcePublic
	}
}

// carrierNAT 是 RFC 6598 定义的运营商级 NAT 网段。
var carrierNAT = netip.MustParsePrefix("100.64.0.0/10")

func isCarrierNAT(a netip.Addr) bool {
	return a.Is4() && carrierNAT.Contains(a)
}

// ProvesReachability 报告某个来源能否证明"外部可以连上"。
func (k SourceKind) ProvesReachability() bool { return k == SourcePublic }

// ---------------------------------------------------------------------------
// 会话
// ---------------------------------------------------------------------------

// Session 是一次外部验证。
type Session struct {
	// ID 是会话标识。
	ID string `json:"id"`
	// Port 是监听的端口。
	Port int `json:"port"`
	// Token 是 URL 里的随机路径段。
	//
	// 它必须不可猜测：一个可猜的路径会让**任意扫描流量**都触发
	// "验证成功"，而那条结论会让用户以为自己配好了。
	Token string `json:"token"`
	// TargetIP 是给用户打开的公网地址（本机当前的全局 IPv6）。
	TargetIP string `json:"target_ip"`

	Status Status `json:"status"`
	// Hits 是收到的全部访问记录。
	Hits []Hit `json:"hits"`

	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Message 是给用户看的结论（已本地化）。
	Message string `json:"message"`
}

// Hit 是一次访问记录。
type Hit struct {
	// RemoteAddr 是 TCP 层的对端地址。
	RemoteAddr string `json:"remote_addr"`
	// Kind 是来源分类。
	Kind SourceKind `json:"kind"`
	// UserAgent 便于用户辨认"这是不是我手机发的"。
	UserAgent string    `json:"user_agent,omitempty"`
	At        time.Time `json:"at"`
}

// URL 返回给用户打开的完整地址。
//
// IPv6 字面量必须加方括号，否则 URL 解析会把地址里的冒号当成端口分隔符。
// 这是 IPv6 场景里最常见的低级错误之一。
func (s Session) URL() string {
	if s.TargetIP == "" {
		return ""
	}
	host := s.TargetIP
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d/%s", host, s.Port, s.Token)
}

// Reachable 报告是否已确认可达。
func (s Session) Reachable() bool { return s.Status == StatusReachable }

// ---------------------------------------------------------------------------
// 管理器
// ---------------------------------------------------------------------------

// defaultTTL 是验证会话的默认有效期。
//
// 5 分钟的依据：用户要拿起手机、关掉 Wi-Fi、输入地址 —— 比这更长会
// 让监听端口白开很久，比这更短会让用户手忙脚乱。
const defaultTTL = 5 * time.Minute

// Manager 管理验证会话。
//
// 同时只允许一个活跃会话：验证要占用一个真实端口，而多个会话会让
// "手机打开的是哪一个"变得无法判断。
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	order    []string

	// targetIP 由外部提供（当前的全局 IPv6）。
	targetIP func(ctx context.Context) string
	// isSelf 报告一个地址是否是本机自己拥有的。
	//
	// 它是识别"自己访问自己"的唯一手段。见 SourceSelf 的说明：
	// IPv6 没有 NAT，本机访问自己的公网地址时来源就是那个公网地址。
	isSelf func(netip.Addr) bool
	// log 用于记录异常，允许为 nil。
	logf func(format string, args ...any)

	ttl time.Duration
}

// NewManager 构造管理器。
//
// targetIP 返回当前用来给用户打开的公网地址（通常是全局 IPv6）。
// 返回空串时生成的 URL 也为空，界面应当据此提示"没有可用的公网地址"。
func NewManager(targetIP func(ctx context.Context) string, logf func(string, ...any)) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		targetIP: targetIP,
		isSelf:   isLocalAddress,
		logf:     logf,
		ttl:      defaultTTL,
	}
}

// SetSelfChecker 覆盖"是不是本机地址"的判断，供测试使用。
func (m *Manager) SetSelfChecker(fn func(netip.Addr) bool) {
	if fn != nil {
		m.isSelf = fn
	}
}

// isLocalAddress 报告一个地址是否属于本机。
//
// 每次调用都重新枚举网卡：地址是会变的（IPv6 前缀一晚上能变好几次），
// 而缓存它意味着"地址刚变过"的那段时间里判断是错的 —— 恰好是用户
// 最可能在测试的时候。
//
// 代价是每个验证请求多一次网卡枚举，而验证请求一天也不会有几次。
func isLocalAddress(addr netip.Addr) bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			local, ok := netip.AddrFromSlice(ipNet.IP)
			if ok && local.Unmap() == addr.Unmap() {
				return true
			}
		}
	}
	return false
}

// SetTTL 覆盖会话有效期，供测试使用。
func (m *Manager) SetTTL(d time.Duration) {
	if d > 0 {
		m.ttl = d
	}
}

// StartRequest 是开始一次验证的请求。
type StartRequest struct {
	// Port 是要监听的端口。为 0 表示由内核分配一个空闲端口。
	//
	// 用内核分配的随机端口有个好处：它几乎不可能与用户已有的服务
	// 冲突，也不需要用户在验证前先去关掉什么。
	Port int
}

// Start 开始一次外部验证。
func (m *Manager) Start(ctx context.Context, req StartRequest) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 先停掉过期的会话，释放它们占用的端口。
	m.sweepLocked()

	address := fmt.Sprintf(":%d", req.Port)
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return Session{}, fmt.Errorf(
			"verify: 无法监听端口 %d：%w"+
				"（该端口可能已被其它程序占用）", req.Port, err)
	}

	// 读回真实端口（req.Port 为 0 时由系统分配）。
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return Session{}, errors.New("verify: 监听地址不是 TCP 地址")
	}

	token, err := newToken()
	if err != nil {
		_ = ln.Close()
		return Session{}, err
	}

	id, err := newID()
	if err != nil {
		_ = ln.Close()
		return Session{}, err
	}

	target := ""
	if m.targetIP != nil {
		target = m.targetIP(ctx)
	}

	now := time.Now().UTC()
	sess := &Session{
		ID:        id,
		Port:      tcpAddr.Port,
		Token:     token,
		TargetIP:  target,
		Status:    StatusWaiting,
		CreatedAt: now,
		ExpiresAt: now.Add(m.ttl),
		Message:   "等待外部访问。请用手机（关闭 Wi-Fi，走 4G/5G）打开下面的地址。",
	}

	m.sessions[id] = sess
	m.order = append(m.order, id)

	go m.serve(ln, id)

	if m.logf != nil {
		m.logf("外部验证会话已开始：端口 %d，地址 %s", sess.Port, sess.URL())
	}
	return *sess, nil
}

// serve 在监听上等待外部访问，直到会话结束或端口被关闭。
func (m *Manager) serve(ln net.Listener, id string) {
	// 会话到期时关闭监听，让 Accept 返回错误并结束这个 goroutine。
	timer := time.AfterFunc(m.ttl, func() {
		_ = ln.Close()
		m.expire(id)
	})
	defer timer.Stop()

	srv := &http.Server{
		// 只处理验证请求，因此超时给得很短。
		ReadHeaderTimeout: 5 * time.Second,
		Handler:           m.handler(id),
	}
	// 关闭时不等待 —— 验证服务没有任何需要优雅收尾的状态。
	defer func() { _ = srv.Close() }()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, net.ErrClosed) {
		if m.logf != nil {
			m.logf("外部验证监听异常结束：%v", err)
		}
	}
}

// handler 处理一次验证请求。
func (m *Manager) handler(id string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := m.session(id)
		if !ok || sess.Token == "" {
			http.NotFound(w, r)
			return
		}

		// 路径不匹配就当作普通 404。
		//
		// 这让端口上的扫描流量看不到任何特征 —— 一个固定路径的
		// "验证端点"会被扫描器发现，而它的响应会告诉扫描者
		// "这台机器上跑着 ISC"。
		if strings.Trim(r.URL.Path, "/") != sess.Token {
			http.NotFound(w, r)
			return
		}

		kind := m.recordHit(id, r)
		writeResultPage(w, kind, sess)
	})
}

// recordHit 记录一次访问并更新会话状态。
func (m *Manager) recordHit(id string, r *http.Request) SourceKind {
	addr := remoteAddr(r)
	kind := ClassifySource(addr, m.isSelf)

	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[id]
	if !ok {
		return kind
	}

	sess.Hits = append(sess.Hits, Hit{
		RemoteAddr: addr.String(),
		Kind:       kind,
		UserAgent:  truncate(r.UserAgent(), 200),
		At:         time.Now().UTC(),
	})

	// 状态只升不降：一旦确认公网可达，之后的内网访问不该把它降级。
	if kind.ProvesReachability() {
		if sess.Status != StatusReachable {
			sess.Status = StatusReachable
			sess.Message = "外部访问成功 —— 链路是通的。"
		}
	} else if sess.Status == StatusWaiting {
		sess.Status = StatusHairpinOnly
		sess.Message = hairpinMessage(kind)
	}
	return kind
}

// hairpinMessage 解释为什么这次访问证明不了什么。
func hairpinMessage(kind SourceKind) string {
	switch kind {
	case SourceSelf:
		// 最容易被误判成成功的一类：IPv6 没有 NAT，从本机访问自己的
		// 公网地址时来源就是那个公网地址，光看地址类型完全正常。
		return "这次访问来自这台机器自己 —— IPv6 没有 NAT，" +
			"本机访问自己的公网地址是直连的，不经过运营商，" +
			"因此什么也证明不了。请用手机（关闭 Wi-Fi，走 4G/5G）重新打开。"
	case SourceLoopback:
		return "只收到了来自本机的访问 —— 那是回环路径，不经过网络，" +
			"什么也证明不了。请用手机（关闭 Wi-Fi）重新打开。"
	case SourceLinkLocal:
		return "只收到了链路本地地址的访问 —— 不经过运营商。" +
			"请用手机（关闭 Wi-Fi，走 4G/5G）重新打开。"
	default:
		return "只收到了来自内网的访问 —— 手机可能还连着 Wi-Fi，" +
			"或者这是个运营商级 NAT 地址。" +
			"请关闭 Wi-Fi、走移动数据重新打开。"
	}
}

// expire 把一个等待中的会话标成超时。
func (m *Manager) expire(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[id]
	if !ok || sess.Status != StatusWaiting {
		return
	}
	sess.Status = StatusUnreachable
	sess.Message = "在有效期内没有收到任何外部访问。若本机检测全部通过，" +
		"这一条指向**上游封禁**（运营商或路由器防火墙）—— " +
		"本机已经没得改了。"
}

// Get 取出一个会话。
func (m *Manager) Get(id string) (Session, bool) {
	sess, ok := m.session(id)
	if !ok {
		return Session{}, false
	}
	return *sess, true
}

func (m *Manager) session(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[id]
	return sess, ok
}

// List 返回全部会话（最近的在前）。
//
// 返回副本：调用方会把它序列化成 JSON，而共享底层切片会让
// "读取"与"记录新访问"并发时产生数据竞争。
func (m *Manager) List() []Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Session, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		if sess, ok := m.sessions[m.order[i]]; ok {
			out = append(out, cloneSession(*sess))
		}
	}
	return out
}

// Stop 结束一个会话。
//
// 它会关闭监听并保留记录 —— 用户可能想回看"上次验证的结果是什么"。
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[id]
	if !ok {
		return ErrNotFound
	}
	if sess.Status == StatusWaiting {
		sess.Status = StatusStopped
		sess.Message = "验证已被手动停止。"
	}
	return nil
}

// sweepLocked 清理超期的会话。
//
// 目前只标记状态；监听由各自的定时器关闭。保留记录是刻意的 ——
// 用户需要能看到"上一次验证的结果"。
func (m *Manager) sweepLocked() {
	now := time.Now().UTC()
	active := 0
	for _, sess := range m.sessions {
		if sess.Status == StatusWaiting && now.After(sess.ExpiresAt) {
			sess.Status = StatusUnreachable
			sess.Message = "在有效期内没有收到任何外部访问。"
		}
		if sess.Status == StatusWaiting {
			active++
		}
	}

	// 超过 50 条时丢掉最旧的，避免长时间运行后无限增长。
	const maxKeep = 50
	for len(m.order) > maxKeep {
		oldest := m.order[0]
		m.order = m.order[1:]
		delete(m.sessions, oldest)
	}
	_ = active
}

// ErrNotFound 表示会话不存在。
var ErrNotFound = errors.New("verify: 验证会话不存在")

// ErrBusy 表示已有活跃会话。
var ErrBusy = errors.New("verify: 已有正在进行的验证")

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// remoteAddr 取出请求的对端地址。
func remoteAddr(r *http.Request) netip.Addr {
	// 优先用 RemoteAddr。**不用** X-Forwarded-For：那个头是客户端
	// 可以随意伪造的，而这里判断的正是"请求从哪来" —— 用它等于
	// 让任何人都能声称自己来自公网，从而伪造一次成功的验证。
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

// newToken 生成 URL 里的随机路径段。
//
// 16 字节（128 位）随机：不可猜测是必须的，否则任意扫描流量都可能
// 撞上这个路径并触发"验证成功"—— 而那条错误的结论会让用户以为
// 自己已经配好了。
func newToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("verify: 生成验证令牌失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func newID() (string, error) {
	buf := make([]byte, 9)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("verify: 生成会话 ID 失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func cloneSession(s Session) Session {
	out := s
	out.Hits = append([]Hit(nil), s.Hits...)
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
