package notify

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"strings"
	"sync"
)

// ChannelStore 是通道配置的持久化接口。
//
// 定义在这里而不是存储包：通知中心因此不依赖任何具体的存储实现，
// 测试可以用一个内存实现。
type ChannelStore interface {
	List(ctx context.Context) ([]ChannelConfig, error)
	Replace(ctx context.Context, configs []ChannelConfig) error
}

// ChannelConfig 是一条通道的配置。
type ChannelConfig struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`

	URL          string            `json:"url,omitempty"`
	Method       string            `json:"method,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	BodyTemplate string            `json:"body_template,omitempty"`

	MinSeverity Severity `json:"min_severity,omitempty"`
}

// Validate 检查配置是否可用。
//
// 它挡的是"一定构造不出来"的配置：缺少名称、类型不支持、Webhook
// 没有地址。模板的语法错误由 NewWebhookChannel 在构造时暴露 ——
// 那比在这里重复一遍正则式的检查可靠。
func (c ChannelConfig) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return errors.New(i18n.T("notify.cfg.no_id"))
	}
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf(i18n.T("notify.cfg.no_name"), c.ID)
	}
	switch c.Kind {
	case "webhook", "log":
	default:
		return fmt.Errorf(i18n.T("notify.cfg.bad_kind"), c.Kind)
	}
	if c.Kind == "webhook" && strings.TrimSpace(c.URL) == "" {
		return fmt.Errorf(i18n.T("notify.cfg.no_url"), c.Name)
	}
	switch c.MinSeverity {
	case "", SeverityInfo, SeverityWarning, SeverityError:
	default:
		return fmt.Errorf(i18n.T("notify.cfg.bad_min_level"), c.MinSeverity)
	}
	return nil
}

// Build 由配置构造一个通道。
func (c ChannelConfig) Build(logSink func(Message)) (Channel, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	switch c.Kind {
	case "log":
		return NewLogChannel(logSink), nil

	case "webhook":
		return NewWebhookChannel(WebhookOptions{
			Name:         c.Name,
			URL:          c.URL,
			Method:       c.Method,
			Headers:      c.Headers,
			BodyTemplate: c.BodyTemplate,
		})

	default:
		return nil, fmt.Errorf(i18n.T("notify.cfg.bad_kind"), c.Kind)
	}
}

// ---------------------------------------------------------------------------
// 配置驱动的通道管理
// ---------------------------------------------------------------------------

// levelFilter 是一个带级别过滤的通道包装。
//
// 级别过滤做成**包装**而不是放进 Manager：那样每个通道可以有自己的
// 阈值（"错误发到手机，全部发到日志"），而不是全局一刀切。
type levelFilter struct {
	inner Channel
	min   Severity
}

func (f levelFilter) Name() string { return f.inner.Name() }
func (f levelFilter) Kind() string { return f.inner.Kind() }

func (f levelFilter) Send(ctx context.Context, msg Message) error {
	if severityRank(msg.Severity) < severityRank(f.min) {
		// 低于阈值：**返回成功**而不是一个"被过滤"的错误。
		//
		// 返回错误会在投递记录里留下一堆"失败"，而用户看到那些
		// 会去查一个根本不存在的问题。
		return nil
	}
	return f.inner.Send(ctx, msg)
}

// ConfigManager 在 Manager 之上管理"从配置构造出来的通道"。
//
// 它把"用户配了什么"与"当前生效的是哪些"分开：
//
//	配置在存储里，随时可变
//	生效的通道在内存里，由 Reload 重建
//
// 分开的好处是 Reload 失败时**旧通道仍然可用** —— 用户改坏了一处配置
// 不该让他连通知都收不到。
type ConfigManager struct {
	store ChannelStore
	base  *Manager
	log   func(format string, args ...any)

	mu sync.RWMutex
	// dynamic 是当前由配置构造出来的通道。
	dynamic []Channel
	// configs 是当前生效的配置（用于展示与排查）。
	configs []ChannelConfig
}

// NewConfigManager 构造配置驱动的通道管理。
func NewConfigManager(store ChannelStore, base *Manager,
	logf func(string, ...any)) *ConfigManager {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &ConfigManager{store: store, base: base, log: logf}
}

// Load 从存储加载并应用配置。
//
// 构造失败的通道被**跳过并记录**，而不是让整个加载失败：一条通道
// 配错了不该让其它通道也失效。
func (c *ConfigManager) Load(ctx context.Context) error {
	configs, err := c.store.List(ctx)
	if err != nil {
		return fmt.Errorf(i18n.T("notify.cfg.read_failed"), err)
	}
	return c.apply(ctx, configs, false)
}

// Save 校验并保存配置，成功后立即生效。
//
// 与代理路由同样的顺序：**先校验再落库**。反过来的话，一份不合法的
// 配置会被写进数据库而接口返回失败 —— 用户以为"没保存成功"，但内核
// 下次启动时会因为这份数据而丢掉通道。
func (c *ConfigManager) Save(ctx context.Context, configs []ChannelConfig) error {
	for _, cfg := range configs {
		if err := cfg.Validate(); err != nil {
			return err
		}
	}
	// 先构造一遍，把模板语法错误之类的"构造期才暴露"的问题提前出来。
	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		if _, err := cfg.Build(nil); err != nil {
			return err
		}
	}

	if err := c.store.Replace(ctx, configs); err != nil {
		return err
	}
	return c.apply(ctx, configs, true)
}

// apply 重建生效的通道。
func (c *ConfigManager) apply(_ context.Context, configs []ChannelConfig, _ bool) error {
	var (
		built []Channel
		kept  []ChannelConfig
	)

	for _, cfg := range configs {
		if !cfg.Enabled {
			// 停用的通道仍然保留在配置里，只是不生效。
			kept = append(kept, cfg)
			continue
		}

		ch, err := cfg.Build(nil)
		if err != nil {
			// 跳过并记录，不让整个加载失败。
			c.log(i18n.T("notify.cfg.build_failed"), cfg.Name, err)
			kept = append(kept, cfg)
			continue
		}

		if cfg.MinSeverity != "" && cfg.MinSeverity != SeverityInfo {
			ch = levelFilter{inner: ch, min: cfg.MinSeverity}
		}
		built = append(built, ch)
		kept = append(kept, cfg)
	}

	c.mu.Lock()
	c.dynamic = built
	c.configs = kept
	c.mu.Unlock()

	c.base.SetDynamicChannels(built)

	c.log(i18n.T("notify.cfg.loaded"), len(built))
	return nil
}

// Configs 返回当前生效的配置。
func (c *ConfigManager) Configs() []ChannelConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]ChannelConfig(nil), c.configs...)
}

// SetDynamicChannels 替换由配置驱动的通道。
//
// 它**保留**那些在代码里登记的通道（例如日志通道）—— 那些是
// "总有一个可用通道"的保证，不该因为用户改了配置就消失。
func (m *Manager) SetDynamicChannels(channels []Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 保留固定通道（目前只有代码里登记的那些）。
	var fixed []Channel
	for _, ch := range m.channels {
		if _, isDynamic := ch.(dynamicMarker); !isDynamic {
			fixed = append(fixed, ch)
		}
	}

	out := make([]Channel, 0, len(fixed)+len(channels))
	out = append(out, fixed...)
	for _, ch := range channels {
		out = append(out, dynamicMarker{Channel: ch})
	}
	m.channels = out
}

// dynamicMarker 标记一个通道来自配置。
//
// 用嵌入接口而不是记录在单独的切片里：这样 Manager 只需维护一份
// 通道列表，而"哪些来自配置"通过类型断言就能判断。
type dynamicMarker struct{ Channel }
