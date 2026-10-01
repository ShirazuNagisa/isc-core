// Package i18n 是内核的消息目录。
//
// 设计约束（见 docs/DECISIONS.md D21）：
//
//   - **禁止硬编码用户可见字符串**。所有面向用户的文案（API 错误、CLI 输出、
//     通知模板、控制台）都必须经由本包的消息 key 获取；
//   - 第一天就支持 zh-CN（默认）与 en；
//   - 消息 key 使用点分层级命名，例如 "error.job_not_found"。
//
// 缺失的 key 不会 panic，而是回退到 key 本身并记录一次告警 ——
// 这样漏翻一条文案不会让服务崩溃，但能在测试中被发现。
package i18n

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Lang 是语言标识（BCP 47）。
type Lang string

const (
	// ZhCN 简体中文，默认语言。
	ZhCN Lang = "zh-CN"
	// En 英文。
	En Lang = "en"
)

// Default 是默认语言。
const Default = ZhCN

// Parse 把用户输入规范化为受支持的 Lang。
//
// 无法识别时返回 Default，而不是报错 —— 语言配置不该让内核起不来。
func Parse(s string) Lang {
	switch s {
	case string(ZhCN), "zh", "zh-Hans", "zh_CN", "zh-cn":
		return ZhCN
	case string(En), "en-US", "en_US", "en-GB":
		return En
	default:
		return Default
	}
}

// String 实现 fmt.Stringer。
func (l Lang) String() string { return string(l) }

// Catalog 是某一语言的已编译消息目录。
type Catalog struct {
	lang Lang
	msgs map[string]string
}

// New 构造指定语言的目录。
func New(lang Lang) *Catalog {
	src := messagesZh
	if lang == En {
		src = messagesEn
	}
	return &Catalog{lang: lang, msgs: src}
}

// Lang 返回目录语言。
func (c *Catalog) Lang() Lang { return c.lang }

// T 按键取值并格式化。
//
// key 不存在时返回 key 本身，便于在界面上一眼看出漏翻。
func (c *Catalog) T(key string, args ...any) string {
	tmpl, ok := c.msgs[key]
	if !ok {
		return key
	}
	if len(args) == 0 {
		return tmpl
	}
	return fmt.Sprintf(tmpl, args...)
}

// Has 报告 key 是否存在，供测试用来抓漏翻。
func (c *Catalog) Has(key string) bool {
	_, ok := c.msgs[key]
	return ok
}

// ---------------------------------------------------------------------------
// 包级默认目录
// ---------------------------------------------------------------------------

var (
	defaultCatalog atomic.Pointer[Catalog]
	setOnce        sync.Once
)

// SetDefault 设置包级默认语言。
//
// 进程启动时调用一次；之后 T 都走这个目录。
func SetDefault(lang Lang) {
	setOnce.Do(func() {
		defaultCatalog.Store(New(lang))
		return
	})
	defaultCatalog.Store(New(lang))
}

// Default2 返回当前默认目录。
func defaultOrInit() *Catalog {
	if c := defaultCatalog.Load(); c != nil {
		return c
	}
	c := New(Default)
	defaultCatalog.Store(c)
	return c
}

// T 使用包级默认语言翻译。
func T(key string, args ...any) string {
	return defaultOrInit().T(key, args...)
}

// Keys 返回全部消息 key，供 i18n 完整性测试使用。
func Keys() []string {
	out := make([]string, 0, len(messagesZh))
	for k := range messagesZh {
		out = append(out, k)
	}
	return out
}
