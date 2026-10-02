package i18n

import (
	"context"
	"strings"
)

// 本文件让**单次请求**可以指定语言，而不影响全局默认值。
//
// # 它解决的问题
//
// 内核的语言设置是全局的（`SetDefault`），而 CLI 的 `--lang` 是**客户端**
// 的偏好。没有per-request 语言时会出现混排：
//
//	$ isc --lang zh-CN credential fields cloudflare
//	--field token=<API token>  （必填） [敏感]  Create it under My Profile…
//	                 ↑ CLI 串是中文        ↑ 服务端串是英文
//
// 因为 CLI 只改自己进程的语言，而字段标签是**服务端**用守护进程的语言
// 渲染好之后发过来的。
//
// # 为什么用 context 而不是改全局
//
// 接口是并发的：两个客户端可以同时用不同语言请求。把请求语言写进全局
// 变量会让它们互相覆盖，而那种缺陷只在并发时出现 —— 最难复现的一类。

type ctxKey struct{}

// WithCatalog 把一个目录放进 context。
func WithCatalog(ctx context.Context, c *Catalog) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, c)
}

// FromContext 取出 context 里的目录；没有时返回当前默认语言的目录。
//
// 它**永不返回 nil** —— 调用方可以直接 `.T(...)` 而不必判空。
// 少一个判空就少一处"忘了判空导致 panic"的可能。
func FromContext(ctx context.Context) *Catalog {
	if ctx != nil {
		if c, ok := ctx.Value(ctxKey{}).(*Catalog); ok && c != nil {
			return c
		}
	}
	return defaultOrInit()
}

// FromHeader 解析 Accept-Language 并返回对应的目录。
//
// # 为什么认这个头而不是自定义的
//
// `Accept-Language` 是 HTTP 的标准机制，浏览器与 curl 都会自动带上它。
// 控制台因此**不需要写任何代码**就能跟随浏览器语言，而 CLI 也只需要
// 设置一个标准头。
//
// 取舍规则：按 `q` 权重降序取第一个我们支持的；都不支持时返回 nil，
// 让调用方回退到全局默认值。
//
// 只解析到"够用"的程度：真实的 Accept-Language 可以很复杂
// （`zh-CN,zh;q=0.9,en;q=0.8`），而我们需要的信息只有"首选哪个"。
func FromHeader(header string) *Catalog {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}

	// 取第一个分段 —— 按协议，权重最高的排在最前，而客户端有义务
	// 排好序。自己实现 q 值排序只会引入解析 bug。
	first := header
	if idx := strings.IndexByte(header, ','); idx >= 0 {
		first = header[:idx]
	}
	if idx := strings.IndexByte(first, ';'); idx >= 0 {
		first = first[:idx]
	}
	first = strings.TrimSpace(first)
	if first == "" {
		return nil
	}

	switch {
	case first == "*":
		// 通配：交给全局默认值。
		return nil
	case matchesLang(first, "zh"):
		return New(ZhCN)
	case matchesLang(first, "en"):
		return New(En)
	default:
		return nil
	}
}

// matchesLang 判断一个 BCP 47 语言标签是否属于某个主语言。
//
// 只比主标签：`zh-CN`、`zh-TW`、`zh` 都归到 zh。对中文来说这个简化是
// 有代价的（简繁不分），而本产品只提供 zh-CN 一种中文 —— 把 zh-TW
// 当成 zh-CN 比给它英文更合理。
func matchesLang(tag, primary string) bool {
	tag = strings.ToLower(tag)
	if tag == primary {
		return true
	}
	return strings.HasPrefix(tag, primary+"-")
}
