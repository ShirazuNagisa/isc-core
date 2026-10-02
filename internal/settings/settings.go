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
	Lang             *string `json:"lang,omitempty"`
	LogLevel         *string `json:"log_level,omitempty"`
	EventBufferSize  *int    `json:"event_buffer_size,omitempty"`
	NotifyOnIPChange *bool   `json:"notify_on_ip_change,omitempty"`
	ProxyEnabled     *bool   `json:"proxy_enabled,omitempty"`
	ProxyPort        *int    `json:"proxy_port,omitempty"`
}

// Service 提供设置的读写。
//
// 并发安全：内存在一份快照，读路径不碰数据库 —— 设置会被
// 每次请求读取（例如输出语言），每次都查库是没有必要的开销。
type Service struct {
	store Store

	mu      sync.RWMutex
	current Settings
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
	s.mu.Unlock()
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
		return fmt.Errorf("settings: 不支持的语言 %q", s.Lang)
	}

	switch s.LogLevel {
	case LevelDebug, LevelInfo, LevelWarn, LevelError:
	default:
		return fmt.Errorf("settings: 不支持的日志级别 %q", s.LogLevel)
	}

	if s.EventBufferSize < MinEventBufferSize || s.EventBufferSize > MaxEventBufferSize {
		return fmt.Errorf("settings: 事件缓冲容量 %d 超出允许范围 [%d, %d]",
			s.EventBufferSize, MinEventBufferSize, MaxEventBufferSize)
	}

	if s.ProxyPort < 0 || s.ProxyPort > 65535 {
		return fmt.Errorf("settings: 代理端口 %d 不合法（0-65535）", s.ProxyPort)
	}
	if s.ProxyEnabled && s.ProxyPort == 0 {
		// 开启代理却不给端口：那不是"用默认值"，而是一个明确的矛盾 ——
		// 静默补一个默认值会让用户以为自己选了端口。
		return errors.New("settings: 开启反向代理时必须指定监听端口")
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
	}
}
