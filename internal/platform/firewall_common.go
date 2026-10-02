package platform

import (
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"sort"
	"strings"
	"time"
)

// 本文件是**三个防火墙后端共用的**辅助函数。
//
// # 为什么必须放在没有构建标签的文件里
//
// 早先 ruleNames 与 newChangeID 定义在 firewall_windows.go 里，而那个
// 文件带 `//go:build windows` —— 于是**它们只在 Windows 上存在**。
// 写 Linux 后端时才发现拿不到它们。
//
// 这类问题的表现很隐蔽：在开发机（Windows）上编译一切正常，而
// CI 的 Linux 构建才会失败。因此共用逻辑必须有明确的落点，
// 而不是"碰巧和 Windows 后端写在一起"。

// ruleNames 取出规则的名字列表。
func ruleNames(rules []Rule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Name)
	}
	return out
}

// newChangeID 生成变更 ID。
//
// 用纳秒时间戳：变更 ID 只需要在同一次内核运行内唯一，而它会被
// 写进计划表供审计与回滚定位。加随机后缀会让日志更难读，
// 而纳秒精度已经足够。
func newChangeID(kind string) string {
	return fmt.Sprintf("%s-%d", kind, time.Now().UTC().UnixNano())
}

// diffRuleNames 返回 a 里有而 b 里没有的规则名（按名字排序）。
//
// 按名字比较而不是整体比较：规则名是**身份**，而协议 / 端口 / 来源
// 是它的内容。同名但内容不同的情形由各后端自己处理（它们知道怎么
// 更新一条规则），这里只回答"哪些身份是新增的、哪些是要移除的"。
func diffRuleNames(a, b []Rule) []Rule {
	have := make(map[string]bool, len(b))
	for _, r := range b {
		have[r.Name] = true
	}

	var out []Rule
	for _, r := range a {
		if !have[r.Name] {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// renderRuleDiff 把规则差异渲染成给人看的文本。
//
// 放在共用文件里而不是某一个后端里：三个后端的差异展示应当**完全一致** ——
// 用户在 Windows 与 Linux 上看到的预览不该长得不一样。
//
// 这一条是编译器抓到的：早先它叫 renderNftDiff 且定义在 firewall_linux.go
// （带 linux 构建标签），于是写 macOS 后端时才发现拿不到它。
func renderRuleDiff(added, removed []Rule) string {
	var b strings.Builder
	for _, r := range added {
		fmt.Fprintf(&b, i18n.T("platform.fw_add"), r.Name, r.Protocol, r.Port)
	}
	for _, r := range removed {
		fmt.Fprintf(&b, i18n.T("platform.fw_remove"), r.Name, r.Protocol, r.Port)
	}
	if b.Len() == 0 {
		return i18n.T("platform.fw_nochange")
	}
	return b.String()
}
