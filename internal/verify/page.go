package verify

import (
	"html"
	"net/http"
	"strings"
)

// 本文件是验证成功后在**手机上**显示的那个页面。
//
// 设计约束与内核的其它界面完全不同：
//
//   - 它在别人的手机上、通过移动网络打开，因此必须极小、无外部资源，
//     并且在慢速网络下也要立刻可用；
//   - 用户此刻关心的是"到底通没通"，因此结论必须在第一屏、
//     用最大的字号说清楚；
//   - 它是这个项目里唯一一个会被**外部设备**看到的页面，
//     因此不能泄漏任何关于这台机器的信息（版本、路径、内部地址）。
//
// 页面里出现的两样东西都是刻意的：来源分类与来源地址。用户需要据此
// 判断"我这次访问到底算不算数"—— 尤其是当他发现地址是自家内网的时候。

// writeResultPage 返回验证结果页。
func writeResultPage(w http.ResponseWriter, kind SourceKind, sess *Session) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 明确禁止缓存与嵌入：这是一次性页面，被缓存下来会让用户
	// 在下一次验证时看到上一次的结论。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(http.StatusOK)

	_, _ = w.Write([]byte(renderPage(kind, sess)))
}

// renderPage 生成完整页面。
func renderPage(kind SourceKind, sess *Session) string {
	ok := kind.ProvesReachability()

	accent := "#e0a33e"
	icon := "⚠️"
	headline := "这次访问不能作为凭据"
	body := hairpinMessage(kind)

	if ok {
		accent = "#3ecf8e"
		icon = "✅"
		headline = "链路是通的"
		body = "这台机器能从公网被访问到。回到 ISC 控制台即可看到结果，" +
			"并可以关闭这个临时端口。"
	}

	var b strings.Builder
	b.WriteString(pageHead)
	b.WriteString("<style>:root{--accent:" + accent + "}</style>")
	b.WriteString(`</head><body><div class="card">`)
	b.WriteString(`<div class="icon">` + icon + `</div>`)
	b.WriteString(`<h1>` + html.EscapeString(headline) + `</h1>`)
	b.WriteString(`<p>` + html.EscapeString(body) + `</p>`)

	b.WriteString(`<div class="meta">`)
	writeMetaRow(&b, "本次来源", kindLabel(kind))
	if len(sess.Hits) > 0 {
		writeMetaRow(&b, "来源地址", sess.Hits[len(sess.Hits)-1].RemoteAddr)
	}
	b.WriteString(`</div>`)

	b.WriteString(`</div>`)
	b.WriteString(`<p class="foot">ISC · 一次性验证页面，可以关闭</p>`)
	b.WriteString(`</body></html>`)

	return b.String()
}

func writeMetaRow(b *strings.Builder, label, value string) {
	b.WriteString(`<div><span>` + html.EscapeString(label) + `</span><code>` +
		html.EscapeString(value) + `</code></div>`)
}

func kindLabel(k SourceKind) string {
	switch k {
	case SourcePublic:
		return "公网地址（有效凭据）"
	case SourceSelf:
		return "本机自己的地址（无效凭据）"
	case SourceLoopback:
		return "本机回环（无效凭据）"
	case SourceLinkLocal:
		return "链路本地（无效凭据）"
	case SourcePrivate:
		return "内网或运营商级 NAT（无效凭据）"
	default:
		return "无法识别（无效凭据）"
	}
}

// pageHead 是页面的固定开头。
//
// 全部内联：手机上通过移动网络打开，多一次外部请求就多一次失败机会，
// 而失败的表现是"页面一片空白"—— 用户会以为验证没成功。
const pageHead = `<!DOCTYPE html>
<html lang="zh-CN"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>ISC 外部验证</title>
<style>
:root{--accent:#8b93a4;--bg:#14161a;--card:#1c1f26;--text:#dfe3ea;--dim:#8b93a4}
*{box-sizing:border-box}
html,body{margin:0;min-height:100%;background:var(--bg);color:var(--text);
  font:16px/1.6 system-ui,-apple-system,"Segoe UI","Microsoft YaHei",sans-serif}
body{display:flex;align-items:center;justify-content:center;padding:22px}
.card{background:var(--card);border:1px solid #2e323c;border-radius:14px;
  padding:28px 22px;max-width:420px;width:100%;text-align:center;
  border-top:4px solid var(--accent)}
.icon{font-size:52px;line-height:1;margin-bottom:14px}
h1{font-size:23px;margin:0 0 12px;font-weight:600}
p{margin:0 0 18px;color:var(--dim);font-size:15px}
.meta{text-align:left;border-top:1px solid #2e323c;padding-top:14px;
  font-size:13px}
.meta div{display:flex;justify-content:space-between;gap:12px;padding:4px 0}
.meta span{color:var(--dim);flex:none}
.meta code{font-family:ui-monospace,Consolas,monospace;word-break:break-all;
  text-align:right}
.foot{text-align:center;color:#5b6273;font-size:12px;margin:18px 0 0}
</style>
`
