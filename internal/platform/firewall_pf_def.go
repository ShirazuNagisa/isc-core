package platform

import (
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"sort"
	"strconv"
	"strings"
)

// 本文件是 macOS pf 后端的**纯逻辑**。
//
// # pf 与 nftables 的根本差异
//
// 这个差异值得说清楚，因为它决定了后端怎么写：
//
//	nftables   增量。每条规则是一个对象，有内核分配的 handle，
//	           可以单独增删。
//	pf         声明式。配置是**一整份文本**，pfctl 读进去之后
//	           整体替换。规则没有身份，也没有 handle。
//
// 因此 pf 这边**没有"差集"这回事**：期望状态直接渲染成 anchor 文件的
// 全文，写进去、重载即可。看起来"更简单"，但代价是：
//
//	任何一次改动都会重载全部规则
//	重载的瞬间，pf 会短暂地按"没有这些规则"处理（见 Apply 的说明）
//
// # 为什么用 anchor 而不是直接改 /etc/pf.conf
//
// /etc/pf.conf 是用户的文件，里面可能有他自己写的规则、也可能被其它
// 工具管理。直接往里追加内容会：
//
//	多次启用之后堆叠重复规则
//	卸载时无法干净地摘掉自己那部分
//	用户的文件被意外改动
//
// anchor 是 pf 为这个场景提供的机制：一个独立的规则文件，由主配置
// 里的一行 `anchor "isc"` 引入。摘掉 anchor 行就等于干净地卸载。

const (
	pfAnchorName = "isc"
	pfAnchorPath = "/etc/pf.anchors/" + pfAnchorName

	// pfRulePrefix 是 ISC 规则的注释标记。
	//
	// pf 的规则语法没有注释，但**支持一整行以 # 开头的注释**。
	// 因此标记写在规则上方单独一行，而不是行尾。
	pfRulePrefix = "# isc:"
)

// pfRuleNamePattern 与 Windows / nftables 保持同一个格式。
var pfRuleNamePattern = nftRuleNamePattern

// PfAnchorContent 把期望状态渲染成 anchor 文件的全文。
//
// # 为什么整体渲染而不是拼接
//
// 见文件头的说明：pf 的模型就是声明式的。拼接式的实现会产生
// "多次启用之后规则堆叠"这种问题，而用户看到的是一份越来越长的
// 配置文件，却找不到哪几条是重复的。
//
// 渲染时**排序**：同样的一组规则应当产出逐字节相同的文件，
// 这样"内容没变就不重载"才成立 —— 而重载 pf 有可见的代价。
func PfAnchorContent(desired []Rule) string {
	rules := append([]Rule(nil), desired...)
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })

	var b strings.Builder
	b.WriteString(i18n.T("platform.pf_header", pfAnchorName))
	b.WriteString("\n")

	if len(rules) == 0 {
		// 空 anchor 是合法的，而它正是"撤销全部规则"的结果。
		b.WriteString(i18n.T("platform.pf_empty"))
		return b.String()
	}

	for _, r := range rules {
		b.WriteString(pfRulePrefix + r.Name + "\n")
		b.WriteString(pfRuleLine(r) + "\n")
	}
	return b.String()
}

// pfRuleLine 生成一条 pf 规则。
//
// pf 的语法与 nftables 差别很大，几处容易写错的地方：
//
//	proto 必须显式写（tcp/udp），否则会被当成"所有协议"
//	port 关键字要跟 proto 匹配（tcp 用 port，udp 也用 port）
//	IPv6 要放在 inet6 规则里，或者用 inet 家族同时覆盖两者
func pfRuleLine(r Rule) string {
	proto := strings.ToLower(string(r.Protocol))
	if proto == "" {
		proto = "tcp"
	}

	port := strconv.Itoa(int(r.Port.From))
	if r.Port.To > r.Port.From {
		// pf 的端口区间用 `port a:b` 而不是 a-b。
		//
		// 写成 a-b 是一个语法错误，而 pfctl 的报错只有一句
		// "syntax error" 加行号 —— 看不出是区间写法的问题。
		port = fmt.Sprintf("%d:%d", r.Port.From, r.Port.To)
	}

	src := "any"
	if s := strings.TrimSpace(r.Source); s != "" {
		src = s
	}

	// 用 inet 而不是 inet6：它同时匹配 IPv4 与 IPv6，
	// 与 nftables 那边用的 inet 族保持一致。
	return fmt.Sprintf("pass in inet proto %s from %s to any port %s keep state",
		proto, src, port)
}

// ParsePfAnchor 从 anchor 文件内容里取出 ISC 的规则名。
//
// 用途是**幂等判断**：内容没变就不重载 pf（重载有可见代价）。
// 它不去解析完整的 pf 语法 —— 那没有必要，而且 pf 的语法相当复杂。
// 我们只需要知道自己写了哪些规则，而那是自己生成的、格式已知。
func ParsePfAnchor(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, pfRulePrefix) {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, pfRulePrefix))
		if name == "" || !pfRuleNamePattern.MatchString(name) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// PfConfNeedsAnchor 判断 /etc/pf.conf 里是否已经引入了 ISC 的 anchor。
//
// 需要两行：`anchor "isc"` 声明它，`load anchor "isc" from "..."` 告诉
// pf 从哪里读。少了任何一行，anchor 文件都不会被加载 —— 而症状是
// "规则写进去了但完全不生效"，那是很难从现象反推回原因的。
func PfConfNeedsAnchor(conf string) (needsDeclare, needsLoad bool) {
	needsDeclare, needsLoad = true, true

	for _, line := range strings.Split(conf, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // 注释行不算
		}
		if strings.Contains(trimmed, `anchor "`+pfAnchorName+`"`) {
			if strings.HasPrefix(trimmed, "load ") {
				needsLoad = false
			} else {
				needsDeclare = false
			}
		}
	}
	return needsDeclare, needsLoad
}

// AppendPfAnchor 把 anchor 的引入行追加到 /etc/pf.conf 末尾。
//
// **只追加，不改动已有内容**：那个文件里可能有用户自己写的规则、
// 也可能被其它工具管理。重写它（哪怕只是"规范化格式"）都可能
// 破坏别人的配置，而那种破坏很难被归因到 ISC 头上。
func AppendPfAnchor(conf string) string {
	needsDeclare, needsLoad := PfConfNeedsAnchor(conf)
	if !needsDeclare && !needsLoad {
		return conf
	}

	var b strings.Builder
	b.WriteString(conf)
	if !strings.HasSuffix(conf, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(i18n.T("platform.pf_anchor"))
	if needsDeclare {
		b.WriteString(`anchor "` + pfAnchorName + `"` + "\n")
	}
	if needsLoad {
		b.WriteString(`load anchor "` + pfAnchorName + `" from "` + pfAnchorPath + `"` + "\n")
	}
	return b.String()
}

// RemovePfAnchor 从 /etc/pf.conf 里摘掉 ISC 的 anchor 引入行。
//
// 同时摘掉紧邻的说明注释 —— 只删规则行会留下一句
// "由 ISC 添加 —— 删除以下几行即可卸载" 的孤立注释，
// 而下一行已经不是 ISC 的东西了。
func RemovePfAnchor(conf string) string {
	lines := strings.Split(conf, "\n")
	out := make([]string, 0, len(lines))

	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])

		if strings.Contains(trimmed, i18n.T("platform.pf_anchor_k")) {
			// 跳过这句注释，以及紧随其后的 anchor 行。
			j := i + 1
			for j < len(lines) {
				t := strings.TrimSpace(lines[j])
				if t == "" {
					j++
					continue
				}
				if strings.Contains(t, `anchor "`+pfAnchorName+`"`) {
					j++
					continue
				}
				break
			}
			i = j - 1
			continue
		}

		out = append(out, lines[i])
	}

	res := strings.Join(out, "\n")
	// 收尾：别留下连续的空行。
	for strings.Contains(res, "\n\n\n") {
		res = strings.ReplaceAll(res, "\n\n\n", "\n\n")
	}
	return res
}

// ValidatePfRule 检查一条规则能否安全地写进 anchor 文件。
//
// pf 的配置文件是**按行解析**的，因此换行符会越出那一行，
// 变成一条额外的 pf 指令 —— 那是一个能改写整份防火墙配置的注入点。
func ValidatePfRule(r Rule) error {
	if !pfRuleNamePattern.MatchString(r.Name) {
		return fmt.Errorf(
			i18n.T("platform.rule_bad_name")+
				i18n.T("platform.rule_bad_name_b"), r.Name)
	}
	if !r.Port.Valid() {
		return fmt.Errorf(i18n.T("platform.rule_bad_port"),
			r.Name, r.Port)
	}

	if s := strings.TrimSpace(r.Source); s != "" {
		if strings.ContainsAny(s, "\n\r") {
			return fmt.Errorf(i18n.T("platform.rule_src_newline"), r.Name)
		}
		// pf 的地址列表用花括号，而它们也用于宏展开 ——
		// 让它出现在地址里会让语义完全不同。
		if strings.ContainsAny(s, "{}") {
			return fmt.Errorf(
				i18n.T("platform.rule_src_brace"),
				r.Name, s)
		}
	}
	return nil
}
