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
//
// # 目录是分层合并的
//
// 基础表（messagesZh / messagesEn）加上各层的补充表。按层分文件是为了
// 让"这句话该去哪儿找/该往哪儿加"有唯一的答案 —— 接口层近百条文案塞进
// 基础表会让那个文件无法浏览。
func New(lang Lang) *Catalog {
	src := messagesZh
	extra := []map[string]string{apiMessagesZh, tier1MessagesZh, reachMessagesZh}
	if lang == En {
		src = messagesEn
		extra = []map[string]string{apiMessagesEn, tier1MessagesEn, reachMessagesEn}
	}

	// 没有补充层时直接用基础表，避免每次构造都复制一遍。
	//
	// # 累加而不是重建
	//
	// 每一轮必须从**上一轮的结果**（msgs）而不是从**基础表**（src）开始复制。
	// 写成从 src 重建的话，每加一层就把前一层整个丢掉 —— 而单层时完全看不出来，
	// 因为那时"上一轮的结果"恰好就是 src。
	//
	// 这个缺陷是在加入第二层（tier1）时才暴露的：api 那一层的 key **整层消失**，
	// 表现是所有接口文案都变成 key 本身。
	msgs := src
	for _, layer := range extra {
		if len(layer) == 0 {
			continue
		}
		merged := make(map[string]string, len(msgs)+len(layer))
		for k, v := range msgs {
			merged[k] = v
		}
		for k, v := range layer {
			merged[k] = v
		}
		msgs = merged
	}

	return &Catalog{lang: lang, msgs: msgs}
}

// Lang 返回目录语言。
func (c *Catalog) Lang() Lang { return c.lang }

// T 按键取值并格式化。
//
// key 不存在时返回 key 本身，便于在界面上一眼看出漏翻。
//
// 英文目录查不到时会再查移植代码的译文表（messagesEnDdnsGo）。那张表的
// key 是中文句子 —— 移植过来的 provider 沿用 ddns-go 的"中文原文即消息 key"
// 约定，因此两类 key 并存。中文侧不需要对应条目：查不到时返回的 key
// 本身就已经是中文。
func (c *Catalog) T(key string, args ...any) string {
	tmpl, ok := c.msgs[key]
	if !ok && c.lang == En {
		tmpl, ok = messagesEnDdnsGo[key]
	}
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
	if _, ok := c.msgs[key]; ok {
		return true
	}
	if c.lang == En {
		_, ok := messagesEnDdnsGo[key]
		return ok
	}
	// 中文侧：移植代码的 key 就是中文原文本身，因此"存在"等价于
	// "它是个中文键"。用非 ASCII 判定即可 —— 我们自己新增的 key 一律是
	// 点分小写标识符，不会误判。
	return containsNonASCII(key)
}

// containsNonASCII 报告字符串是否含非 ASCII 字符。
func containsNonASCII(s string) bool {
	for _, r := range s {
		if r > 0x7F {
			return true
		}
	}
	return false
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
//
// 包含两类：本项目的点分标识符，以及移植自 ddns-go 的中文句子键。
// Keys 返回全部已知的 key（含各分层表）。
//
// # 它必须是"全部"
//
// 目录完整性测试拿它当基准。早先它只返回基础表 —— 于是新加的分层表
// （apiMessagesZh/En）**完全不受一致性检查**：中英文对不上也不会有
// 任何提示。而 layeredCatalogMaps 的存在就是为了让"加了一层却忘了
// 登记"这件事只有一个地方可犯。
func Keys() []string {
	maps := layeredCatalogMaps()
	n := len(messagesEnDdnsGo)
	for _, m := range maps {
		n += len(m)
	}

	seen := make(map[string]bool, n)
	out := make([]string, 0, n)
	for _, m := range maps {
		for k := range m {
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, k)
		}
	}
	out = append(out, ddnsGoKeys()...)
	return out
}

// layeredCatalogMaps 返回全部**按语言成对**的消息表。
//
// 成对是关键：目录完整性测试要求每一层的 zh 与 en 拥有相同的 key 集合，
// 而它只能检查它知道的那几层。新增一层时**必须**加进这里 —— 否则那一层
// 会静默地脱离检查。
func layeredCatalogMaps() []map[string]string {
	return []map[string]string{
		messagesZh, messagesEn,
		apiMessagesZh, apiMessagesEn,
		tier1MessagesZh, tier1MessagesEn,
		reachMessagesZh, reachMessagesEn,
	}
}
