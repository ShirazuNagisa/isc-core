package i18n

import "strings"

// 本文件把"注释"从控制台前端资源里剥掉，供棘轮计数使用。
//
// # 为什么必须剥
//
// Go 侧的计数器只统计**字符串字面量**，注释里的中文是好的（它们解释"为什么"）。
// 而控制台资源是文本文件，最初的计数是"含中文的行数" —— 于是注释也被算了进去。
//
// 后果很具体：**那个数字永远降不到 0**。注释本来就该留着，而且越多越好。
// 一个降不到 0 的达标线不是达标线，它只是一条永远在报错的噪声。
//
// 这与 Go 侧"日志被当成文案"是同一类错误：**计数器在测量一个 D21 不关心的
// 东西**。那一次是改成按调用排除，这一次是按注释剥离。

// stripConsoleComments 去掉 HTML 与 JS 里的注释，保留其余内容与**行号**。
//
// 行号必须保留：棘轮按行比对，行号错位会让报告指向错误的行。
// 因此注释被替换成等量的空行，而不是删掉。
func stripConsoleComments(name, src string) string {
	if strings.HasSuffix(name, ".html") {
		return stripHTMLComments(src)
	}
	if strings.HasSuffix(name, ".js") {
		return stripJSComments(src)
	}
	return src
}

// stripHTMLComments 去掉 <!-- --> 注释。
//
// HTML 注释不能嵌套，因此顺序扫描就够了。
func stripHTMLComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))

	for i := 0; i < len(src); {
		if strings.HasPrefix(src[i:], "<!--") {
			end := strings.Index(src[i+4:], "-->")
			if end < 0 {
				// 未闭合：剩下的全是注释。
				b.WriteString(blankLines(src[i:]))
				break
			}
			b.WriteString(blankLines(src[i : i+4+end+3]))
			i += 4 + end + 3
			continue
		}
		b.WriteByte(src[i])
		i++
	}
	return b.String()
}

// stripJSComments 去掉 // 与 /* */ 注释。
//
// # 为什么不能简单地按行找 //
//
// `https://example.com` 里的 `//` 在**字符串里**，剥掉它会把那行剩下的
// 内容也一起吃掉 —— 而那正是我们需要计数的东西（URL 会出现在提示文案里）。
//
// 因此这里扫一遍并跟踪字符串状态：单引号、双引号、反引号（模板串）。
// 反引号里允许换行，因此跨行状态也要跟着走。
func stripJSComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))

	var quote byte // 0 表示不在字符串里
	for i := 0; i < len(src); {
		c := src[i]

		// 字符串内部：只找结束引号，并处理转义。
		if quote != 0 {
			if c == '\\' && i+1 < len(src) {
				b.WriteByte(c)
				b.WriteByte(src[i+1])
				i += 2
				continue
			}
			if c == quote {
				quote = 0
			}
			b.WriteByte(c)
			i++
			continue
		}

		switch {
		case c == '\'' || c == '"' || c == '`':
			quote = c
			b.WriteByte(c)
			i++

		case strings.HasPrefix(src[i:], "//"):
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				b.WriteString(blankLines(src[i:]))
				i = len(src)
				break
			}
			b.WriteString(blankLines(src[i : i+end]))
			i += end

		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				b.WriteString(blankLines(src[i:]))
				i = len(src)
				break
			}
			b.WriteString(blankLines(src[i : i+2+end+2]))
			i += 2 + end + 2

		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// blankLines 返回与 s 行数相同的一段空白，用于替换注释。
//
// 保留换行是刻意的：行号必须与原文对齐，否则棘轮的报告会指向错误的行。
func blankLines(s string) string {
	n := strings.Count(s, "\n")
	if n == 0 {
		// 行内注释：用一个空格占位，避免把前后两个 token 粘在一起。
		return " "
	}
	return strings.Repeat("\n", n)
}
