// Package settings 管理内核的运行时设置。
//
// 设置以键值对落库（见 internal/store 的说明），但**对外是一份带类型
// 与默认值的结构** —— 键值对是存储细节，领域层不该让调用方去处理
// "这个键不存在时该用什么默认值"。
package settings

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"strconv"
	"sync"
)

// 设置的键名。它们是持久化格式的一部分，**改名等同于数据迁移**。
const (
	KeyLang             = "lang"
	KeyLogLevel         = "log_level"
	KeyEventBufferSize  = "event_buffer_size"
	KeyNotifyOnIPChange = "notify_on_ip_change"
	KeyProxyEnabled     = "proxy_enabled"
	KeyProxyPort        = "proxy_port"
	KeyProxyTLS         = "proxy_tls"
	KeyACMEEmail        = "acme_email"
	KeyACMEDirectory    = "acme_directory"
	KeyACMEDNSCred      = "acme_dns_credential_id"
)

// 允许的取值。
const (
	LangZhCN = "zh-CN"
	LangEn   = "en"

	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// 事件缓冲的边界。
//
// 下限 100：再小的话一次"前缀变化触发几十条记录更新"的突发就会
// 直接把订阅者挤爆，导致客户端不断重连。
// 上限 100000：每条事件几十字节，十万条约几 MB，是可以接受的量级。
const (
	MinEventBufferSize     = 100
	MaxEventBufferSize     = 100000
	DefaultEventBufferSize = 1000

	// DefaultProxyPort 是反向代理的默认监听端口。
	//
	// 用 443 而不是 8080：用户访问的地址里不该带端口号，而 443 是
	// 浏览器默认补的那个。非标端口意味着每个链接都要手写端口，
	// 而分享出去的链接很容易忘。
	DefaultProxyPort = 443
)

// Settings 是完整的设置快照。
type Settings struct {
	Lang             string `json:"lang"`
	LogLevel         string `json:"log_level"`
	EventBufferSize  int    `json:"event_buffer_size"`
	NotifyOnIPChange bool   `json:"notify_on_ip_change"`

	// ProxyEnabled 控制是否启动反向代理的监听。
	//
	// 默认**关闭**：反代监听在公网上，开启它是一个需要用户明确决定的
	// 动作。默认开着会让"我只是想用动态解析"的用户莫名其妙地多出一个
	// 对外的监听端口。
	ProxyEnabled bool `json:"proxy_enabled"`

	// ProxyTLS 表示反向代理是否用 HTTPS 提供服务。
	//
	// 开启它需要同时配置 ACME（邮箱 + DNS-01 凭据），否则证书签不出来，
	// 而症状是"浏览器报证书错误"。
	ProxyTLS bool `json:"proxy_tls"`

	// ACMEEmail 是 ACME 账户的联系邮箱。
	//
	// 它很重要：证书快要过期而自动续期失败时，Let's Encrypt 会用它
	// 来提醒。不填会让"续期静默失败"变成"站点某天突然打不开"。
	ACMEEmail string `json:"acme_email"`

	// ACMEDirectory 是 ACME 目录地址。
	//
	// 留空用生产环境。测试环境（staging）签发的证书**不被浏览器信任**，
	// 但配额宽松得多 —— 首次配置时值得用它试一遍，因为生产环境的
	// 失败配额是每小时 5 次，调配置很容易把它用光。
	ACMEDirectory string `json:"acme_directory"`

	// ACMEDNSCredentialID 是做 DNS-01 校验用的凭据。
	//
	// 该凭据对应的服务商必须支持完整的记录管理（Tier-1 六家之一）——
	// DNS-01 需要在用户的 DNS 里创建一条 TXT 记录。
	ACMEDNSCredentialID string `json:"acme_dns_credential_id"`

	// ProxyPort 是反代监听的端口。
	//
	// 默认 443 而不是 8080：用户访问的地址里不该带端口号 ——
	// 而 443 是浏览器默认补的那个。用非标端口意味着每个链接都要
	// 手写端口，而用户分享出去的链接很容易忘。
	ProxyPort int `json:"proxy_port"`
}

// Default 返回默认设置。
func Default() Settings {
	return Settings{
		Lang:             LangZhCN,
		LogLevel:         LevelInfo,
		EventBufferSize:  DefaultEventBufferSize,
		NotifyOnIPChange: true,
		ProxyEnabled:     false,
		ProxyPort:        DefaultProxyPort,
		ProxyTLS:         false,
		ACMEDirectory:    "", // 空 = 生产环境
	}
}

// Store 是设置的持久化接口。
type Store interface {
	LoadSettings(ctx context.Context) (map[string]string, error)
	SaveSettings(ctx context.Context, kv map[string]string) error
}

// Patch 描述一次部分更新。
//
// 用指针字段区分"没提交"与"提交了零值"—— 若用值类型，
// 客户端想把 NotifyOnIPChange 设为 false 就会被当成"没改"。
type Patch struct {
	Lang                *string `json:"lang,omitempty"`
	LogLevel            *string `json:"log_level,omitempty"`
	EventBufferSize     *int    `json:"event_buffer_size,omitempty"`
	NotifyOnIPChange    *bool   `json:"notify_on_ip_change,omitempty"`
	ProxyEnabled        *bool   `json:"proxy_enabled,omitempty"`
	ProxyPort           *int    `json:"proxy_port,omitempty"`
	ProxyTLS            *bool   `json:"proxy_tls,omitempty"`
	ACMEEmail           *string `json:"acme_email,omitempty"`
	ACMEDirectory       *string `json:"acme_directory,omitempty"`
	ACMEDNSCredentialID *string `json:"acme_dns_credential_id,omitempty"`
}

// Service 提供设置的读写。
//
// 并发安全：内存在一份快照，读路径不碰数据库 —— 设置会被
// 每次请求读取（例如输出语言），每次都查库是没有必要的开销。
type Service struct {
	store Store

	mu      sync.RWMutex
	current Settings

	// onChange 在更新成功后按**新设置**回调。
	//
	// 它存在的理由是：有些设置项的生效方式不在本包能力范围内 ——
	// 例如语言要调 i18n.SetDefault，而那会让本包依赖 i18n。
	// 与其把那条依赖硬塞进来，不如留一个回调点，由装配方接上。
	onChange func(Settings)
}

// SetOnChange 设置更新回调。
//
// 它会被**同步**调用（在 Update 返回之前）—— 调用方应当只做轻量的
// 内存操作。异步化会让"改完设置立刻发一个请求"看到旧值。
func (s *Service) SetOnChange(fn func(Settings)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = fn
}

// Load 从存储读取设置并与默认值合并。
//
// 合并而不是覆盖：新增设置项后，老数据库里没有对应的键，
// 直接覆盖会让新项变成零值（例如 EventBufferSize 变成 0，
// 进而让事件缓冲退化成"每条都挤爆订阅者"）。
func Load(ctx context.Context, store Store) (*Service, error) {
	s := &Service{store: store, current: Default()}

	if store == nil {
		return s, nil
	}

	kv, err := store.LoadSettings(ctx)
	if err != nil {
		return nil, err
	}
	s.current = merge(s.current, kv)
	return s, nil
}

// Get 返回当前设置快照。
func (s *Service) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Update 应用一次部分更新并落库。
func (s *Service) Update(ctx context.Context, p Patch) (Settings, error) {
	s.mu.Lock()
	next := s.current

	if p.Lang != nil {
		next.Lang = *p.Lang
	}
	if p.LogLevel != nil {
		next.LogLevel = *p.LogLevel
	}
	if p.EventBufferSize != nil {
		next.EventBufferSize = *p.EventBufferSize
	}
	if p.NotifyOnIPChange != nil {
		next.NotifyOnIPChange = *p.NotifyOnIPChange
	}
	if p.ProxyEnabled != nil {
		next.ProxyEnabled = *p.ProxyEnabled
	}
	if p.ProxyPort != nil {
		next.ProxyPort = *p.ProxyPort
	}
	if p.ProxyTLS != nil {
		next.ProxyTLS = *p.ProxyTLS
	}
	if p.ACMEEmail != nil {
		next.ACMEEmail = *p.ACMEEmail
	}
	if p.ACMEDirectory != nil {
		next.ACMEDirectory = *p.ACMEDirectory
	}
	if p.ACMEDNSCredentialID != nil {
		next.ACMEDNSCredentialID = *p.ACMEDNSCredentialID
	}
	if err := next.Validate(); err != nil {
		s.mu.Unlock()
		return s.current, err
	}
	s.mu.Unlock()

	if s.store != nil {
		if err := s.store.SaveSettings(ctx, encode(next)); err != nil {
			return s.current, err
		}
	}

	s.mu.Lock()
	s.current = next
	// 取回调时**已经持有锁**，因此直接读即可。
	cb := s.onChange
	s.mu.Unlock()

	// 回调放在**落库与生效之后、返回之前**。
	//
	// 顺序有讲究：
	//   · 在落库之前回调，会让一次失败的保存留下已经生效的副作用；
	//   · 在返回之后（异步）回调，会让"改完设置立刻发一个请求"看到旧值
	//     —— 而那正是用户会做的事（改语言，然后跑一条命令）。
	if cb != nil {
		cb(next)
	}

	return next, nil
}

// Validate 校验取值合法性。
//
// 之所以要显式校验而不是"存进去再说"：这些值直接影响内核行为
// （语言决定输出、事件缓冲决定内存与可靠性），存下一个非法值
// 会让问题在很久以后以完全无关的症状出现。
func (s Settings) Validate() error {
	switch s.Lang {
	case LangZhCN, LangEn:
	default:
		return fmt.Errorf(i18n.T("settings.bad_lang"), s.Lang)
	}

	switch s.LogLevel {
	case LevelDebug, LevelInfo, LevelWarn, LevelError:
	default:
		return fmt.Errorf(i18n.T("settings.bad_level"), s.LogLevel)
	}

	if s.EventBufferSize < MinEventBufferSize || s.EventBufferSize > MaxEventBufferSize {
		return fmt.Errorf(i18n.T("settings.bad_buffer"),
			s.EventBufferSize, MinEventBufferSize, MaxEventBufferSize)
	}

	if s.ProxyPort < 0 || s.ProxyPort > 65535 {
		return fmt.Errorf(i18n.T("settings.bad_port"), s.ProxyPort)
	}
	if s.ProxyEnabled && s.ProxyPort == 0 {
		// 开启代理却不给端口：那不是"用默认值"，而是一个明确的矛盾 ——
		// 静默补一个默认值会让用户以为自己选了端口。
		return errors.New(i18n.T("settings.need_port"))
	}
	if s.ProxyTLS {
		// HTTPS 必须有证书来源，而签证书需要这两样。
		//
		// 在这里挡住而不是等签发失败：后者的症状是"浏览器报证书错误"，
		// 而用户完全不知道是设置少填了一项。
		if s.ACMEDNSCredentialID == "" {
			return errors.New(i18n.T("settings.need_dns01"))
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

// merge 把存储中的键值对叠加到默认值上。
//
// **无法解析的值被忽略并保留默认值**，而不是报错让内核起不来：
// 一份被手工改坏的 settings 表不该导致服务无法启动 ——
// 那会让用户连修好它的界面都打不开。
func merge(base Settings, kv map[string]string) Settings {
	if v, ok := kv[KeyLang]; ok {
		switch v {
		case LangZhCN, LangEn:
			base.Lang = v
		}
	}
	if v, ok := kv[KeyLogLevel]; ok {
		switch v {
		case LevelDebug, LevelInfo, LevelWarn, LevelError:
			base.LogLevel = v
		}
	}
	if v, ok := kv[KeyEventBufferSize]; ok {
		if n, err := strconv.Atoi(v); err == nil &&
			n >= MinEventBufferSize && n <= MaxEventBufferSize {
			base.EventBufferSize = n
		}
	}
	if v, ok := kv[KeyNotifyOnIPChange]; ok {
		if b, err := strconv.ParseBool(v); err == nil {
			base.NotifyOnIPChange = b
		}
	}
	if v, ok := kv[KeyProxyEnabled]; ok {
		if b, err := strconv.ParseBool(v); err == nil {
			base.ProxyEnabled = b
		}
	}
	if v, ok := kv[KeyProxyPort]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 65535 {
			base.ProxyPort = n
		}
	}
	if v, ok := kv[KeyProxyTLS]; ok {
		if b, err := strconv.ParseBool(v); err == nil {
			base.ProxyTLS = b
		}
	}
	// 邮箱与目录地址直接取用，不做格式校验 ——
	// 校验交给 ACME 服务器，它的错误信息比我们的猜测准确。
	if v, ok := kv[KeyACMEEmail]; ok {
		base.ACMEEmail = v
	}
	if v, ok := kv[KeyACMEDirectory]; ok {
		base.ACMEDirectory = v
	}
	if v, ok := kv[KeyACMEDNSCred]; ok {
		base.ACMEDNSCredentialID = v
	}
	return base
}

// encode 把设置编码为键值对。
func encode(s Settings) map[string]string {
	return map[string]string{
		KeyLang:             s.Lang,
		KeyLogLevel:         s.LogLevel,
		KeyEventBufferSize:  strconv.Itoa(s.EventBufferSize),
		KeyNotifyOnIPChange: strconv.FormatBool(s.NotifyOnIPChange),
		KeyProxyEnabled:     strconv.FormatBool(s.ProxyEnabled),
		KeyProxyPort:        strconv.Itoa(s.ProxyPort),
		KeyProxyTLS:         strconv.FormatBool(s.ProxyTLS),
		KeyACMEEmail:        s.ACMEEmail,
		KeyACMEDirectory:    s.ACMEDirectory,
		KeyACMEDNSCred:      s.ACMEDNSCredentialID,
	}
}
