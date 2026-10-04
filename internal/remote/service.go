package remote

import (
	"context"
	"crypto/tls"
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
	// APNSHost 是 APNs 的接入点；空串表示生产环境。
	//
	// 由调用方给而不是写死：测试要指向一个假服务器，而用户可能
	// 在用沙箱环境 —— 把它写死会让这两件事都做不到。
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
}

// Service 是远程管理面。
type Service struct {
	opts  Options
	cert  *Certificate
	pair  *pairingManager
	pairL *limiter
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
	pusher, err := s.apns.get()
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
			Certificates: []tls.Certificate{s.cert.TLS},
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
	return s.pairL.allow(source, now)
}

// AllowRequest 报告某台设备现在是否可以发请求。
func (s *Service) AllowRequest(deviceID string, now time.Time) bool {
	return s.devL.allow(deviceID, now)
}

// BeginPairing 开一个配对会话。
func (s *Service) BeginPairing(role Role, label string, now time.Time) (PairingSession, error) {
	if !role.Valid() {
		role = RoleViewer
	}
	return s.pair.start(role, label, now)
}

// CancelPairing 取消一个配对会话。
func (s *Service) CancelPairing(id string) bool { return s.pair.cancel(id) }

// CurrentPairing 返回当前会话。
func (s *Service) CurrentPairing(now time.Time) (PairingSession, bool) { return s.pair.current(now) }

// ClaimPairing 用一个密钥或六位码认领会话。
func (s *Service) ClaimPairing(credential string, now time.Time) (PairingSession, error) {
	return s.pair.claim(credential, now)
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
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf(i18n.T("remote.err.qr_payload"), err)
	}
	return string(raw), nil
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
}
