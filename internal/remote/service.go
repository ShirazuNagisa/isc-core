package remote

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件把证书、设备、配对与限流组装成一条可用的监听。
//
// 分工是刻意的：
//
//	internal/remote   拥有监听、TLS、设备与配对的**状态**
//	internal/api      拥有路由与权限矩阵（谁能在什么路径上做什么）
//
// 之所以把路由放在 API 包：那 82 条路由的处理器都在那里，而远程面要用
// 的是**同一批处理器**加一层白名单。把路由表复制到本包意味着两处会漂移，
// 而漂移的方向恰好是"远程面暴露了一条本地才该有的路径"。

// State 是远程监听的运行状态。
type State string

const (
	// StateDisabled 表示总开关关闭。
	StateDisabled State = "disabled"
	// StateStarting 表示正在启动。
	StateStarting State = "starting"
	// StateRunning 表示正在监听。
	StateRunning State = "running"
	// StateFailed 表示启动失败（原因见 LastError）。
	StateFailed State = "failed"
)

// DefaultPort 是远程监听的默认端口。
//
// 8788 是一个高位端口：绑定它不需要 root（而这正是"打开 Phecda 就能用"
// 的前提），同时它离 8080/8888 这些常见占用足够远。
const DefaultPort = 8788

// Options 是构造 Service 的输入。
type Options struct {
	// Dir 是本子系统的私有目录（证书存放处）。
	Dir string
	// Port 是监听端口。
	Port int
	// Store 是设备持久化。
	Store Store
	// Log 是日志器。
	Log *slog.Logger
	// Name 是服务器显示名（出现在手机上）。
	Name string
	// Version / APIVersion 是内核版本信息，客户端据此判断兼容性。
	Version    string
	APIVersion string

	// APNSStore 是 APNs 凭据的存放处。为 nil 表示不支持推送
	// （那时三个推送相关的接口会明确报错，而不是假装成功）。
	APNSStore *CredentialStore
	// PublicCert 按 SNI 名字取出**受信任**的证书。
	//
	// 为 nil 表示只有自签那一条路。有它时，远程监听会按客户端请求的
	// 名字选证书：公网域名给 Let's Encrypt 签的那张，其余（IP、.local）
	// 仍然给自签的那张。
	PublicCert PublicCertificateFunc

	// PublicFace 是公网访问的编排（子域名 + DNS 记录 + 自检）。
	//
	// 为 nil 表示不支持：那时 `/v1/remote/public/*` 会明确报错，
	// 而不是假装成功。
	PublicFace *PublicFace
	// PublicSettings 读取公网访问相关的设置。
	// 定义成接口是为了让 remote 包不依赖 settings 包的具体类型。
	PublicSettings PublicSettings
	// PublicZones 按域名反查凭据与区域。为 nil 表示不支持公网访问
	// （那时 SyncPublic 会明确报错，而不是静默什么都不做）。
	PublicZones PublicZoneResolver
	// APNSHost **强制覆盖**所有的 APNs 接入点；空串表示按每台设备登记的
	// 环境自动选（沙箱 / 生产）。
	//
	// 它是"覆盖"而不是"默认值"，因为它的实际用途只有一个：验收脚本
	// 用一个本地的假 APNs 替换真实端点。那种场景下**所有**设备都必须
	// 走那个假服务器 —— 按环境分流会把一半设备发到 Apple 的真实接入点。
	//
	// 真实环境的选择在 pusherCache.hostFor：它读的是设备登记令牌时自报的
	// 环境（`Device.APNSEnvironment`），而那正是决定令牌属于哪个环境的
	// 东西。把它写死会让"Debug 版装到真机上"这条最常见的路径永远收不到
	// 推送 —— 而 Apple 回的错误看起来像"令牌不对"。
	APNSHost string
}

// Status 是远程面的完整状态快照。
type Status struct {
	State                State
	Enabled              bool
	Port                 int
	Listening            bool
	Addresses            []string
	Hostname             string
	SPKI                 string
	FingerprintShort     string
	TLSNotAfter          time.Time
	DeviceCount          int
	Pairing              *PairingSession
	APNSConfigured       bool
	APNSKeyID            string
	APNSBundleID         string
	APNSTeamID           string
	NotificationsEnabled bool
	LastError            string

	// Public 是公网访问的状态。Enabled 为假时其余字段为空。
	Public PublicStatus
}

// PublicStatus 是公网访问的对外状态。
type PublicStatus struct {
	Enabled bool
	// Domain 是子域名挂在哪个域名下（用户选的），例如 example.com。
	Domain string
	// Host 是完整的子域名；还没生成时为空。
	Host string
	// Records 是"记录类型 → 地址值"，便于界面直接显示。
	Records map[string]string
	// LastCheck 是最近一次可达性自检的结论。
	LastCheck *PublicCheck
	// Ready 表示凭据与区域都配好了，可以开始同步。
	Ready bool
}

// PublicCertificateFunc 按 SNI 名字取出一张受信任的证书。
//
// 返回 (nil, nil) 表示"这个名字我没有证书"，调用方会回落到自签那张 ——
// 而那不是错误：手机在局域网里用 IP 连的时候本来就没有 SNI 名字。
type PublicCertificateFunc func(host string) (*tls.Certificate, error)

// PublicSettings 是公网访问用到的设置读取。
type PublicSettings interface {
	// PublicConfig 返回"是否开启"与"挂在哪个域名下"。
	//
	// 它**不返回**凭据 ID 与区域 ID：那两件事由域名唯一决定，
	// 内核自己反查得出来（见 PublicZoneResolver）。
	PublicConfig() (enabled bool, domain string)
}

// PublicZoneResolver 按域名反查该用哪把凭据、哪个区域。
//
// 与证书签发用的是同一个反查器（dns.ZoneFinder）—— 两处需要的
// 是同一个答案，而各写一份会让它们迟早给出不同的结果。
type PublicZoneResolver interface {
	Find(ctx context.Context, domain string) (credentialID string, zoneID, zoneName string, err error)
}

// Service 是远程管理面。
type Service struct {
	opts  Options
	cert  *Certificate
	pair  *pairingManager
	pairL *limiter
	pingL *limiter
	devL  *limiter

	mu            sync.Mutex
	handler       http.Handler
	srv           *http.Server
	ln            net.Listener
	state         State
	port          int
	enabled       bool
	notifications bool
	lastError     string

	apns *pusherCache

	public *PublicFace
	pset   PublicSettings
	pzones PublicZoneResolver
}

// New 构造远程管理面。
//
// 证书在这里就绪（而不是等到启动监听）：状态页要显示指纹，而指纹在
// 总开关还没打开时也必须能显示 —— 否则用户点开页面看到的是一片空白，
// 无法判断"它到底准备好没有"。
func New(opts Options) (*Service, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Port == 0 {
		opts.Port = DefaultPort
	}
	if opts.Name == "" {
		opts.Name = hostname()
	}

	cert, err := LoadOrCreateCertificate(opts.Dir)
	if err != nil {
		return nil, err
	}

	svc := &Service{
		opts:      opts,
		cert:      cert,
		pair:      newPairingManager(),
		pairL:     newLimiter(pairPerMinute, pairBurst),
		pingL:     newLimiter(pingPerMinute, pingBurst),
		devL:      newLimiter(devicePerMinute, deviceBurst),
		state:     StateDisabled,
		port:      opts.Port,
		lastError: "",
	}
	// 推送的发射器按凭据缓存：每次发通知都新建一个的话 JWT 的缓存就没了，
	// 而 APNs 会因为我们频繁换 token 而拒绝连接（TooManyProviderTokenUpdates）
	// —— 那是一个"偶尔漏几条通知"的故障，极难查。
	if opts.APNSStore != nil {
		svc.apns = newPusherCache(opts.APNSStore, opts.APNSHost)
	}
	svc.public = opts.PublicFace
	svc.pset = opts.PublicSettings
	svc.pzones = opts.PublicZones
	return svc, nil
}

// SetHandler 注入远程面的路由处理器。
//
// 必须在 Start 之前调用。之所以分两步：处理器由 internal/api 构造，
// 而它反过来依赖本服务做鉴权 —— 直接互相构造会形成环。
func (s *Service) SetHandler(h http.Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

// SetNotificationsEnabled 设置推送总开关。
func (s *Service) SetNotificationsEnabled(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = on
}

// SetAPNSCredentials 保存 APNs 凭据。
//
// 校验**在写盘之前**做：一份解析不出私钥的凭据存进去之后，症状是
// "推送发不出去"，而那时用户已经看不到自己填错了什么。
func (s *Service) SetAPNSCredentials(credentials Credentials) error {
	if s.opts.APNSStore == nil {
		return errors.New(i18n.T("remote.err.apns_no_cipher"))
	}
	return s.opts.APNSStore.Set(credentials)
}

// DeleteAPNSCredentials 删除 APNs 凭据。
func (s *Service) DeleteAPNSCredentials() error {
	if s.opts.APNSStore == nil {
		return nil
	}
	return s.opts.APNSStore.Delete()
}

// APNSStatus 返回不含敏感字段的状态。
func (s *Service) APNSStatus() (configured bool, teamID, keyID, bundleID string, err error) {
	if s.opts.APNSStore == nil {
		return false, "", "", "", nil
	}
	return s.opts.APNSStore.Status()
}

// APNSTopic 返回当前应当登记的通知 topic（未配置时为空）。
func (s *Service) APNSTopic() string {
	if s.opts.APNSStore == nil {
		return ""
	}
	_, _, _, bundleID, err := s.opts.APNSStore.Status()
	if err != nil {
		return ""
	}
	return bundleID
}

// TestPush 给一台设备发一条测试推送。
//
// 这个接口是必需的，不是锦上添花：推送的失败现场全在 Apple 那一侧
// （凭据、topic、令牌、环境搞混），而"现在给我发一条"是唯一能在
// 三十秒内把问题定位到某一环的办法。
func (s *Service) TestPush(ctx context.Context, deviceID string) (Delivery, error) {
	if s.apns == nil {
		return Delivery{}, errors.New(i18n.T("remote.err.apns_no_cipher"))
	}
	device, err := s.Device(ctx, deviceID)
	if err != nil {
		return Delivery{}, err
	}
	// 与真实推送走**同一个**接入点判据：测试推送的全部价值就在于它复现
	// 真实那条路。两处各写一份的话，"测试通了、真推不通"会变成常态。
	pusher, err := s.apns.get(device)
	if err != nil {
		return Delivery{}, err
	}
	if pusher == nil {
		return Delivery{}, errors.New(i18n.T("remote.msg.push_not_configured"))
	}

	delivery := pusher.Push(ctx, device, Notification{
		Title:      i18n.T("remote.push.test_title"),
		Body:       i18n.T("remote.push.test_body"),
		ThreadID:   "isc-" + s.opts.Name,
		CollapseID: "isc-test",
	})
	s.RecordPushDelivery(ctx, PushDelivery{
		TS: time.Now().UTC(), DeviceID: device.ID, Kind: "test",
		DedupeKey: "test", Status: delivery.Status,
		HTTPStatus: delivery.HTTPStatus, Reason: delivery.Reason, APNSID: delivery.APNSID,
	})
	return delivery, nil
}

// Port 返回当前端口。
func (s *Service) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// Enabled 报告总开关状态。
func (s *Service) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// NotificationsEnabled 报告推送总开关状态。
func (s *Service) NotificationsEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notifications
}

// Apply 按给定设置启动/停止/重启监听。
//
// 返回值只表示"设置本身是否被接受"；监听起不来（端口被占）通过 LastError
// 暴露 —— 那种失败必须是**可以在界面上显示的状态**，而不是一个让整页
// 报错的错误码。
func (s *Service) Apply(ctx context.Context, enabled bool, port int) error {
	// 端口 0 表示"让操作系统挑一个空闲端口"。
	//
	// 它有两个用途，都不是常规路径：测试需要一个不会与别的测试（或这台
	// 机器上真正在跑的内核）撞车的端口；诊断时则可以用它确认"监听本身
	// 能不能起来"，从而把"端口被占"与"TLS 配置有问题"分开。
	// 设置里不会存 0 —— 那样每次重启端口都会变，二维码里的地址随即失效。
	if port < 0 || port > 65535 {
		return fmt.Errorf(i18n.T("remote.err.port_range"), port)
	}

	s.mu.Lock()
	wasRunning := s.srv != nil
	portChanged := s.port != port
	s.port = port
	s.enabled = enabled
	s.mu.Unlock()

	if !enabled {
		return s.Stop(ctx)
	}
	if !wasRunning || portChanged {
		if wasRunning {
			if err := s.Stop(ctx); err != nil {
				return err
			}
		}
		return s.Start(ctx)
	}
	return nil
}

// Start 启动监听。已经在跑时是空操作（幂等）。
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.srv != nil {
		s.mu.Unlock()
		return nil
	}
	if s.handler == nil {
		s.mu.Unlock()
		return errors.New(i18n.T("remote.err.no_handler"))
	}
	port := s.port
	s.state = StateStarting
	s.mu.Unlock()

	// 绑定全部接口：远程面的可达性就是它的用途，而"只绑某一个网卡"
	// 在用户换网段（有线/无线切换、VPN 起来）之后会静默失效 ——
	// 那种失败在手机上表现为"连不上"，而在电脑上一切正常。
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		s.fail(i18n.T("remote.err.listen", err))
		return err
	}

	// 回读真实端口：传 0 时由内核分配，而**必须**把它记住 ——
	// 状态、二维码里的候选地址、以及界面上的端口号全都取自它。
	actualPort := port
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		actualPort = addr.Port
	}

	s.mu.Lock()
	srv := &http.Server{
		Handler: s.handler,
		TLSConfig: &tls.Config{
			// 按 SNI 选证书，而不是只放一张。
			//
			// # 为什么必须两条路并存
			//
			// 局域网路径用的是自签证书，它的**公钥指纹**是手机在配对时
			// 固定下来的（D38）。换成受信任证书会让所有已配对的手机
			// 立刻连不上，而用户完全不知道该重新配对 —— 那条路必须
			// 一个字都不变。
			//
			// 公网路径用的是 Let's Encrypt 签的证书。它**不能**沿用
			// 固定指纹：证书续期会换密钥，固定之后每次续期都会连不上，
			// 而症状是"过一阵子连不上，重启一下又好了"。
			//
			// 两者靠 SNI 区分：手机连域名时给受信任那张，连 IP 或
			// `.local` 时给自签那张。
			GetCertificate: s.getCertificate,
			// TLS 1.2 是下限：1.0/1.1 已经不被任何现代系统接受，
			// 而允许它们只会让误配置更难发现。
			MinVersion: tls.VersionTLS12,
		},
		ReadHeaderTimeout: 10 * time.Second,
		// 刻意不设 WriteTimeout / IdleTimeout：长轮询要在没有事件时
		// 挂住 25 秒，写超时会把那条连接掐断，表现为"手机时不时掉线"。
		ErrorLog: slog.NewLogLogger(s.opts.Log.Handler(), slog.LevelWarn),
	}
	s.srv = srv
	s.ln = ln
	s.port = actualPort
	s.state = StateRunning
	s.lastError = ""
	s.mu.Unlock()

	go func() {
		// 传空的证书路径：证书来自上面的 TLSConfig.Certificates。
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.fail(i18n.T("remote.err.serve", err))
		}
	}()

	s.opts.Log.Info(i18n.T("remote.msg.listening"), "port", actualPort, "fingerprint", s.cert.FingerprintShort())
	return nil
}

// Stop 停止监听。没有在跑时是空操作（幂等）。
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	srv := s.srv
	s.srv = nil
	s.ln = nil
	if s.state == StateRunning || s.state == StateStarting {
		s.state = StateDisabled
	}
	s.mu.Unlock()

	if srv == nil {
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// 关闭超时不改变状态：所有连接都还会被 srv.Close 掐掉，
		// 而此时把状态写成"失败"会让界面显示一个不存在的问题。
		s.opts.Log.Warn(i18n.T("remote.msg.shutdown"), "err", err)
	}
	return nil
}

// fail 记录一次启动/运行失败。
func (s *Service) fail(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = StateFailed
	s.lastError = msg
	s.opts.Log.Error(i18n.T("remote.msg.failed"), "err", msg)
}

// Status 返回状态快照。
func (s *Service) Status(ctx context.Context) Status {
	s.mu.Lock()
	state, port, lastErr := s.state, s.port, s.lastError
	enabled, notifications := s.enabled, s.notifications
	listening := s.srv != nil
	s.mu.Unlock()

	// APNs 的状态从凭据存储读：它是唯一的事实来源，而在内存里再存一份
	// 只会在"删除凭据之后状态页还显示已配置"这类地方出错。
	apnsConfigured, apnsTeamID, apnsKeyID, apnsBundleID := false, "", "", ""
	if s.opts.APNSStore != nil {
		if configured, team, key, bundle, err := s.opts.APNSStore.Status(); err == nil {
			apnsConfigured, apnsTeamID, apnsKeyID, apnsBundleID = configured, team, key, bundle
		}
	}

	st := Status{
		State:                state,
		Enabled:              enabled,
		Port:                 port,
		Listening:            listening,
		Addresses:            Candidates(port),
		Hostname:             hostname(),
		SPKI:                 s.cert.SPKIBase64(),
		FingerprintShort:     s.cert.FingerprintShort(),
		TLSNotAfter:          s.cert.NotAfter(),
		NotificationsEnabled: notifications,
		LastError:            lastErr,
		APNSConfigured:       apnsConfigured,
		APNSKeyID:            apnsKeyID,
		APNSBundleID:         apnsBundleID,
		APNSTeamID:           apnsTeamID,
	}

	// 公网访问的状态从台账读，不缓存：子域名一旦变化就是一次故障，
	// 而缓存只会让"改了没生效"多一种可能。
	//
	// 它做两次加锁（读台账、读设置）而不是把锁合并：这两份状态
	// 属于不同的所有者，合并锁会让"写设置时阻塞读状态"这类
	// 无关的耦合出现。
	if s.public != nil {
		state := s.public.State()
		st.Public.Host = state.Host()
		st.Public.Records = state.Addresses
		st.Public.LastCheck = state.LastCheck
	}
	if s.pset != nil {
		publicEnabled, domain := s.pset.PublicConfig()
		st.Public.Enabled = publicEnabled
		st.Public.Domain = domain
		st.Public.Ready = publicEnabled && domain != "" && s.public != nil && s.pzones != nil
	}

	if session, ok := s.pair.current(time.Now()); ok {
		st.Pairing = &session
	}

	if s.opts.Store != nil {
		if devices, err := s.opts.Store.ListDevices(ctx); err == nil {
			for _, d := range devices {
				if d.Active() {
					st.DeviceCount++
				}
			}
		}
	}
	return st
}

// Authenticate 校验一个设备令牌并返回设备。
//
// 返回值分三种情形，调用方据此给出不同的响应：
//   - 令牌不认识 → ErrDeviceNotFound（401，但要与"已吊销"区分开：
//     前者只说明这串东西不是我们的，后者说明这台设备被主动断开了，
//     而用户需要看到"它被断开了"这个事实）；
//   - 令牌有效但设备已吊销 → ErrDeviceRevoked（401，客户端应转为
//     "需要重新配对"）；
//   - 正常 → 设备。
func (s *Service) Authenticate(ctx context.Context, token string) (Device, error) {
	if token == "" || s.opts.Store == nil {
		return Device{}, ErrDeviceNotFound
	}

	device, err := s.opts.Store.DeviceByTokenHash(ctx, tokenHash(token))
	if err != nil {
		if errors.Is(err, ErrDeviceNotFound) {
			return Device{}, ErrDeviceNotFound
		}
		return Device{}, err
	}
	// 哈希相等用常数时间比较。上面那次查询是按哈希索引查的，
	// 因此这一层严格来说是冗余的；留着它是为了让"比对凭据"这条规则
	// 不依赖于"存储层恰好没做前缀匹配"这种外部假设。
	if !hashEqual(device.TokenHash, tokenHash(token)) {
		return Device{}, ErrDeviceNotFound
	}
	if !device.Active() {
		return Device{}, ErrDeviceRevoked
	}
	return device, nil
}

// AllowPair 报告某个来源现在是否可以尝试配对。
func (s *Service) AllowPair(source string, now time.Time) bool {
	return s.pairL.allow(SourceKey(source), now)
}

// AllowPing 报告某个来源现在是否可以打探针端点。
//
// 与 AllowPair 分开计数：共用一个桶时，攻击者狂打 ping 就能把
// 正常用户的配对额度耗光 —— 而那是一条不用配对就能发动的拒绝服务。
func (s *Service) AllowPing(source string, now time.Time) bool {
	return s.pingL.allow(SourceKey(source), now)
}

// getCertificate 按 SNI 选证书。
//
// 回落规则是**先公网、后自签**，而回落本身不是错误：手机在局域网里
// 用 IP 或 `.local` 连的时候根本没有 SNI 名字，那时自签那张才是对的。
func (s *Service) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if s.opts.PublicCert != nil && hello != nil && hello.ServerName != "" {
		cert, err := s.opts.PublicCert(hello.ServerName)
		// `len(cert.Certificate) > 0` 这条不能省：一个**非 nil 但空**的
		// `tls.Certificate`（没有证书链）会被 TLS 栈接受，然后在握手时
		// 报 "no certificates" —— 而那是一个只在运行时才出现的失败，
		// 症状是"公网域名连不上，局域网正常"，看起来像 DNS 问题。
		if err == nil && cert != nil && len(cert.Certificate) > 0 {
			return cert, nil
		}
		// 取不到就回落，**不报错**：一张证书没签下来不该让局域网
		// 那条路也跟着断 —— 那会让"公网还没配好"变成"手机完全连不上"。
	}
	if s.cert == nil {
		return nil, errors.New(i18n.T("remote.err.no_cert"))
	}
	return &s.cert.TLS, nil
}

// AllowRequest 报告某台设备现在是否可以发请求。
func (s *Service) AllowRequest(deviceID string, now time.Time) bool {
	return s.devL.allow(deviceID, now)
}

// BeginPairing 开一个配对会话。
//
// source 是请求来源（由调用方从连接里取，**不看任何请求头** ——
// X-Forwarded-For 之类是可以随便伪造的，而它在这里决定限流与锁定）。
func (s *Service) BeginPairing(role Role, label, source string, now time.Time) (PairingSession, error) {
	if !role.Valid() {
		role = RoleViewer
	}
	return s.pair.start(role, label, SourceKey(source), now)
}

// CancelPairing 取消一个配对会话。
func (s *Service) CancelPairing(id string) bool { return s.pair.cancel(id) }

// CurrentPairing 返回当前会话。
func (s *Service) CurrentPairing(now time.Time) (PairingSession, bool) { return s.pair.current(now) }

// ClaimPairing 用一个密钥或六位码认领会话。
func (s *Service) ClaimPairing(credential, source string, now time.Time) (PairingSession, error) {
	return s.pair.claim(credential, SourceKey(source), now)
}

// DeviceInfo 是客户端自报的设备信息。
//
// 它们只用于展示与排查，**不参与任何判定** —— 一台自称"iPad"的设备
// 在权限上与手机完全等价。把它当作判定依据是那种"看起来很安全、
// 实际上只需要改一个字段就能绕过"的设计。
type DeviceInfo struct {
	Name       string
	Platform   string
	Model      string
	OSVersion  string
	AppVersion string
}

// IssueDevice 为一次成功的配对签发设备。
//
// 返回的令牌**只在这里出现一次**：服务端存的是它的哈希。
func (s *Service) IssueDevice(ctx context.Context, session PairingSession, info DeviceInfo, now time.Time) (Device, string, error) {
	return s.issue(ctx, session.Role, "", session.Label, info, now)
}

// DeriveDevice 从一台已有设备派生一台新设备。
//
// 角色的下界由**服务端**强制：派生设备的权限不得高于父设备。
// 这条规则存在的原因是手表 —— 它没有摄像头也没有键盘，只能复用手机的
// 凭据，而"手表只读"这条约束如果只写在客户端里，改一个字段就没了。
func (s *Service) DeriveDevice(ctx context.Context, parent Device, role Role, label string, info DeviceInfo, now time.Time) (Device, string, error) {
	if !role.Valid() {
		return Device{}, "", errors.New(i18n.T("remote.err.role_invalid"))
	}
	if !parent.Role.AtLeast(role) {
		return Device{}, "", errors.New(i18n.T("remote.err.role_escalation"))
	}
	return s.issue(ctx, role, parent.ID, label, info, now)
}

// issue 是签发设备的公共实现。
func (s *Service) issue(ctx context.Context, role Role, parentID, label string, info DeviceInfo, now time.Time) (Device, string, error) {
	if s.opts.Store == nil {
		return Device{}, "", errors.New(i18n.T("remote.err.no_store"))
	}

	id, err := randomID(12)
	if err != nil {
		return Device{}, "", err
	}
	token, hash, err := newToken()
	if err != nil {
		return Device{}, "", err
	}

	if label == "" {
		label = info.Name
	}
	if label == "" {
		label = i18n.T("remote.device.unnamed")
	}

	device := Device{
		ID:             id,
		Label:          label,
		Role:           role,
		ParentDeviceID: parentID,
		TokenHash:      hash,
		Platform:       info.Platform,
		Model:          info.Model,
		OSVersion:      info.OSVersion,
		AppVersion:     info.AppVersion,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.opts.Store.CreateDevice(ctx, device); err != nil {
		return Device{}, "", err
	}
	return device, token, nil
}

// ListDevices 返回全部设备（含已吊销的）。
func (s *Service) ListDevices(ctx context.Context) ([]Device, error) {
	if s.opts.Store == nil {
		return nil, nil
	}
	return s.opts.Store.ListDevices(ctx)
}

// Device 返回一台设备。
func (s *Service) Device(ctx context.Context, id string) (Device, error) {
	if s.opts.Store == nil {
		return Device{}, ErrDeviceNotFound
	}
	return s.opts.Store.Device(ctx, id)
}

// UpdateDevice 部分更新一台设备。
func (s *Service) UpdateDevice(ctx context.Context, id string, patch DevicePatch) (Device, error) {
	if s.opts.Store == nil {
		return Device{}, ErrDeviceNotFound
	}
	if patch.Role != nil && !patch.Role.Valid() {
		return Device{}, errors.New(i18n.T("remote.err.role_invalid"))
	}
	return s.opts.Store.UpdateDevice(ctx, id, patch)
}

// RevokeDevice 吊销一台设备并级联吊销它的子设备。
func (s *Service) RevokeDevice(ctx context.Context, id string, now time.Time) (int, error) {
	if s.opts.Store == nil {
		return 0, ErrDeviceNotFound
	}
	return s.opts.Store.RevokeDevice(ctx, id, now)
}

// TouchDevice 记录一次成功的访问。
func (s *Service) TouchDevice(ctx context.Context, id string, now time.Time, ip string) {
	if s.opts.Store == nil {
		return
	}
	if err := s.opts.Store.TouchDevice(ctx, id, now, ip); err != nil {
		// 记不下来不是错误：它只是一个展示字段，
		// 而让"记录最后访问时间失败"去影响一次正常的请求是荒唐的。
		s.opts.Log.Debug(i18n.T("remote.msg.touch_failed"), "device", id, "err", err)
	}
}

// SetPushToken 登记或清除一台设备的 APNs 令牌。
func (s *Service) SetPushToken(ctx context.Context, deviceID, token, environment, topic string) error {
	if s.opts.Store == nil {
		return errors.New(i18n.T("remote.err.no_store"))
	}
	return s.opts.Store.SetPushToken(ctx, deviceID, token, environment, topic)
}

// RecordPushDelivery 记一条推送投递。
func (s *Service) RecordPushDelivery(ctx context.Context, d PushDelivery) {
	if s.opts.Store == nil {
		return
	}
	if err := s.opts.Store.AppendPushDelivery(ctx, d); err != nil {
		s.opts.Log.Debug(i18n.T("remote.msg.push_log_failed"), "err", err)
	}
}

// Certificate 返回证书（供 APNs 之外的地方读取指纹）。
func (s *Service) Certificate() *Certificate { return s.cert }

// Name 返回服务器显示名。
func (s *Service) Name() string { return s.opts.Name }

// Version 返回内核版本。
func (s *Service) Version() string { return s.opts.Version }

// APIVersion 返回契约版本。
func (s *Service) APIVersion() string { return s.opts.APIVersion }

// QRPayload 生成二维码里要编码的原文。
//
// **由内核生成**，GUI 只负责把字符串渲染成像素：payload 是契约的一部分，
// 两个实现意味着两处会漂移，而漂移的后果是"某个版本的 App 扫不出来"，
// 那种问题只在特定的版本组合上出现。
func (s *Service) QRPayload(session PairingSession) (string, error) {
	payload := qrPayload{
		Version:   1,
		Kind:      qrKind,
		Name:      s.opts.Name,
		Scheme:    "https",
		Addresses: Candidates(s.Port()),
		SPKI:      s.cert.SPKIBase64(),
		Secret:    session.Secret,
		ExpiresAt: session.ExpiresAt.Unix(),
	}

	// 公网子域名也要带上。
	//
	// # 少了它会怎样
	//
	// `Addresses` 只有局域网候选（那是**扫码时**手机所在的网络）。
	// 手机配好之后在家里能用，出门之后手上只有那几条 `192.168.x.x`
	// 和 `.local`，全部超时 —— 而内核其实在公网上好好地问候着。
	//
	// 它会在第一次连上之后自愈（`/v1/remote/self` 会带回公网域名），
	// 但"扫一次，从此在哪都能用"这件事必须由载荷本身保证，
	// 而不是指望用户先回一次家。
	if host := s.PublicHostname(); host != "" {
		payload.PublicHost = host
		payload.PublicPort = s.Port()
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf(i18n.T("remote.err.qr_payload"), err)
	}
	return string(raw), nil
}

// QRLink 返回载荷的可复制形式。
//
// `isc-remote://pair?d=<base64url>` —— 与二维码里的**是同一份载荷**，
// 只是换了个载体：二维码给人扫，链接给人粘。
//
// 由内核生成而不是让 GUI 自己拼：链接格式是契约的一部分，两个实现
// 意味着两处会漂移，而漂移的症状是"某个版本的 App 粘不进去"。
func (s *Service) QRLink(session PairingSession) (string, error) {
	raw, err := s.QRPayload(session)
	if err != nil {
		return "", err
	}
	return "isc-remote://pair?d=" + base64.RawURLEncoding.EncodeToString([]byte(raw)), nil
}

// qrKind 是二维码 payload 的类型标识。
//
// 客户端据此拒绝不是本产品的码（相机里全是别的二维码，而扫错的
// 表现是一个无法解释的解析错误）。
const qrKind = "isc-remote"

// qrPayload 是二维码里编码的结构。
//
// 版本化是必须的：这个结构一定会演进（多一种地址、多一个字段），
// 而客户端只能通过 `v` 知道自己面不面对着未来的格式。
type qrPayload struct {
	Version   int      `json:"v"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Scheme    string   `json:"scheme"`
	Addresses []string `json:"addresses"`
	SPKI      string   `json:"spki"`
	Secret    string   `json:"secret"`
	ExpiresAt int64    `json:"exp"`

	// PublicHost / PublicPort 是公网子域名（未开启公网访问时为空）。
	//
	// 它是一条**独立的**候选，而不是塞进 `Addresses`：它用的信任模型
	// 与局域网那条不同（受信任证书 vs 固定的公钥指纹），客户端必须
	// 分得清哪条该用哪个。
	PublicHost string `json:"public_host,omitempty"`
	PublicPort int    `json:"public_port,omitempty"`
}

// SyncPublic 让分域名与 DNS 记录与本机当前的公网地址一致。
//
// 它是幂等的：地址没变就不发写请求，因此可以被反复调用
// （开机时、地址变化时、用户点"立即同步"时）。
func (s *Service) SyncPublic(ctx context.Context) (PublicStatus, error) {
	if s.public == nil || s.pset == nil {
		return PublicStatus{}, errors.New(i18n.T("remote.public.err.no_writer"))
	}
	enabled, domain := s.pset.PublicConfig()
	if !enabled {
		return PublicStatus{}, errors.New(i18n.T("remote.public.err.disabled"))
	}
	if domain == "" {
		return PublicStatus{}, errors.New(i18n.T("remote.public.err.no_domain"))
	}
	if s.pzones == nil {
		return PublicStatus{}, errors.New(i18n.T("remote.public.err.no_resolver"))
	}

	// 用户只说了"挂在哪个域名下"。哪把凭据、哪个区域由这个域名
	// 唯一决定 —— 而那是内核该自己回答的问题。
	credentialID, zoneID, zoneName, err := s.pzones.Find(ctx, domain)
	if err != nil {
		return PublicStatus{}, err
	}

	plan := PublicPlan{
		CredentialID: credentialID,
		ZoneID:       zoneID,
		Zone:         zoneName,
	}

	// IPv6：从**本机地址**里挑稳定的那一条，而不是问回显服务。
	//
	// 回显服务看到的是出站用的地址，而系统默认用会轮换的临时地址出站 ——
	// 把它写进 DNS 就是一条几小时后就失效的记录。
	if addrs, err := localScopedAddresses(); err == nil {
		if ip, _, ok := SelectPublicIPv6(addrs); ok {
			plan.IPv6 = ip
		}
	}

	// IPv4：只有**实测过**可达才写。
	//
	// 探测到公网 IPv4 只能说明"我们有 IPv4 出口"，说明不了
	// "外面能连进来" —— 家用宽带上后者通常不成立（大内网）。
	// 写一条连不上的 A 记录比不写更糟：客户端默认先试 IPv4。
	last := s.public.State().LastCheck
	if last != nil && last.Verdict == PublicVerdictReachable && last.Family == "ipv4" && s.public.prober != nil {
		if ip, err := s.public.prober.PublicIPv4(ctx); err == nil {
			plan.IPv4 = ip
		}
	}

	if _, err := s.public.Apply(ctx, plan); err != nil {
		return PublicStatus{}, err
	}
	return s.Status(ctx).Public, nil
}

// PublicProbe 返回当前的可达性探测计划。
func (s *Service) PublicProbe() PublicProbe {
	if s.public == nil {
		return BuildPublicProbe("", s.opts.Port, "https", nil, nil, nil)
	}
	state := s.public.State()

	// 局域网候选由 Candidates 给出（它已经包含 IPv4、IPv6 与 .local）。
	// 手机先试这些：通了就说明它和内核在同一个网络里。
	lan := Candidates(s.opts.Port)

	var ipv6, ipv4 net.IP
	for rtype, value := range state.Addresses {
		ip := net.ParseIP(value)
		switch rtype {
		case "AAAA":
			ipv6 = ip
		case "A":
			ipv4 = ip
		}
	}
	return BuildPublicProbe(state.Host(), s.opts.Port, "https", ipv6, ipv4, lan)
}

// PublicEnabled 报告用户是否开启了公网访问。
func (s *Service) PublicEnabled() bool {
	if s.pset == nil {
		return false
	}
	enabled, _ := s.pset.PublicConfig()
	return enabled
}

// PublicHostname 返回公网子域名（未启用或还没建立时为空）。
//
// 供证书签发用它去申请一张**受信任**的证书 —— 公网路径不能沿用
// 局域网那套自签 + 固定指纹，因为续期会换密钥。
func (s *Service) PublicHostname() string {
	if s.public == nil || s.pset == nil {
		return ""
	}
	enabled, _ := s.pset.PublicConfig()
	if !enabled {
		return ""
	}
	return s.public.State().Host()
}

// RecordPublicCheck 记下一次可达性自检的结论。
func (s *Service) RecordPublicCheck(check PublicCheck) error {
	if s.public == nil {
		return errors.New(i18n.T("remote.public.err.no_writer"))
	}
	return s.public.SetCheck(check)
}

// TeardownPublic 删掉公网面建的全部记录。
func (s *Service) TeardownPublic(ctx context.Context) error {
	if s.public == nil || s.pset == nil || s.pzones == nil {
		return nil
	}
	enabled, domain := s.pset.PublicConfig()
	if !enabled || domain == "" {
		return nil
	}
	// 拆除也要反查：台账里只有域名与记录 ID，而删除记录需要区域 ID。
	credentialID, zoneID, _, err := s.pzones.Find(ctx, domain)
	if err != nil {
		return err
	}
	return s.public.Teardown(ctx, credentialID, zoneID)
}
