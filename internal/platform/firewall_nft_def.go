package platform

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 本文件是 nftables 后端的**纯逻辑**：规则名的解析与生成、命令的拼装、
// 以及 `nft -j list ruleset` 输出的解析。
//
// # 为什么把它单独放在没有构建标签的文件里
//
// 与 systemd / launchd 那两块同样的理由（见 service_def.go）：
// 本项目的开发机是 Windows，**Linux 防火墙无法在本机真机验证**。
// 把不依赖平台调用的部分抽出来，是那部分逻辑唯一的自动化质量保证。
//
// 而这里恰恰是最容易出错的地方：
//
//	命令拼装     端口与协议要插进命令串，拼错了 nft 会报语法错误
//	JSON 解析    nftables 的输出结构嵌套很深，取错一层就解析不到
//	handle       删除规则必须按 handle，而它是内核分配的、不在期望状态里
//
// 真正平台相关的只有"执行 nft 命令"与"读文件"。

// nftTable / nftChain 是 ISC 在 nftables 里使用的表与链。
//
// 用**独立的表**而不是往 filter 表里塞规则：这样"ISC 管了哪些规则"
// 一目了然，卸载时整表删掉即可，也不会与用户自己的规则或其它工具的
// 规则混在一起。
//
// 用 inet 族而不是 ip 族：inet 同时处理 IPv4 与 IPv6，
// 而这正是本产品的核心场景（IPv6 直连）。
const (
	nftTable = "isc"
	nftChain = "input"
)

// 规则注释前缀。nftables 的规则可以有 comment，而那是我们标记
// "这条规则属于 ISC"的唯一手段 —— nftables 本身没有规则名的概念。
const nftCommentPrefix = "isc:"

// nftRuleNamePattern 与 Windows 侧保持**同一个格式**。
//
// 刻意统一：用户在三平台看到的是同一套规则名，
// 而文档、脚本、排错步骤都可以跨平台复用。
var nftRuleNamePattern = regexp.MustCompile(`^isc-[a-z0-9-]+-(tcp|udp)-([1-9][0-9]{0,4})$`)

// nftHandle 是内核分配给一条规则的句柄。
type nftHandle int64

// nftRule 是从 ruleset JSON 里解析出来的一条 ISC 规则。
type nftRule struct {
	// Name 是 ISC 的规则名（从 comment 里取回）。
	Name string
	// Handle 是内核分配的句柄 —— 删除时必须用它。
	Handle nftHandle
	// Proto 是协议。
	Proto Protocol
	// Port 是端口。
	Port PortRange
	// Source 是来源限制，空表示任意。
	Source string
}

// nftExpr 是 ruleset JSON 里的表达式节点。
//
// nftables 的输出是一串**嵌套的表达式数组**，而每种匹配都是一层
// 形如 {"match": {"op": "==", "left": {...}, "right": {...}}} 的结构。
// 这里用 map 而不是固定结构体：不同 nft 版本的字段略有差异，
// 而用固定结构体解析会在遇到新版本时整条规则解析失败。
type nftExpr map[string]any

// ParseNftRuleset 从 `nft -j list ruleset` 的输出里取出 ISC 规则的句柄。
//
// # 它为什么必须能容忍未知结构
//
// ruleset 里**大部分规则不属于 ISC**（用户自己的、其它工具装的），
// 而它们的形式五花八门。解析器遇到不认识的结构必须**跳过而不是报错** ——
// 一个报错会让整个 Inspect 失败，而用户会看到"无法读取防火墙状态"，
// 完全不知道问题出在别人的一条规则上。
func ParseNftRuleset(data []byte) ([]nftRule, error) {
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("platform: 解析 nftables 输出失败: %w", err)
	}

	var out []nftRule
	for _, item := range doc.Nftables {
		raw, ok := item["rule"]
		if !ok {
			continue // 表、链、集合之类的节点，跳过
		}

		var r struct {
			Family  string    `json:"family"`
			Table   string    `json:"table"`
			Comment string    `json:"comment"`
			Handle  int64     `json:"handle"`
			Expr    []nftExpr `json:"expr"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			// 单条规则解析失败不该让整次读取失败 —— 见上面的说明。
			continue
		}

		if r.Table != nftTable {
			continue
		}
		rule, ok := convertNftRule(r.Comment, nftHandle(r.Handle), r.Expr)
		if !ok {
			continue
		}
		out = append(out, rule)
	}

	// 按名字排序：nftables 的输出顺序取决于内核里的插入顺序，
	// 而它可能在不同机器上不同 —— 不排序会让"预览"看起来每次都不一样。
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// convertNftRule 把一条 nft 规则转成 ISC 的 Rule。
func convertNftRule(comment string, handle nftHandle, exprs []nftExpr) (nftRule, bool) {
	name := strings.TrimPrefix(comment, nftCommentPrefix)
	if name == comment || name == "" {
		return nftRule{}, false // 不是 ISC 的规则
	}
	if !nftRuleNamePattern.MatchString(name) {
		// 名字格式不对：可能是早期版本留下的，或者用户手工改过。
		// 跳过而不是猜 —— 猜错会让回滚删掉不该删的规则。
		return nftRule{}, false
	}
	_, portNum, _ := parseNftRuleName(name)

	out := nftRule{
		Name:   name,
		Handle: handle,
		Port:   NewPort(uint16(portNum)),
	}

	// 从表达式里取协议与来源。
	//
	// nftables 把 "tcp dport 8096" 拆成两个 match 表达式：一个匹配
	// 协议（meta l4proto），一个匹配端口（tcp dport）。取错一个
	// 就会得到"协议对了但端口为空"这种半截结果。
	for _, e := range exprs {
		match, ok := e["match"].(map[string]any)
		if !ok {
			continue
		}
		left, _ := match["left"].(map[string]any)
		right, _ := match["right"].(any)

		for kind, val := range left {
			switch kind {
			case "meta":
				// {"meta": {"key": "l4proto"}}
				if m, ok := val.(map[string]any); ok {
					if key, _ := m["key"].(string); key == "l4proto" {
						out.Proto = Protocol(strings.ToLower(fmt.Sprint(right)))
					}
				}
			case "payload":
				// {"payload": {"protocol": "tcp", "field": "dport"}}
				if m, ok := val.(map[string]any); ok {
					proto, _ := m["protocol"].(string)
					field, _ := m["field"].(string)
					if field == "dport" && out.Proto == "" {
						out.Proto = Protocol(strings.ToLower(proto))
					}
				}
			case "ip", "ip6":
				// 来源限制：{"ip": {"key": "saddr"}}
				if m, ok := val.(map[string]any); ok {
					if key, _ := m["key"].(string); key == "saddr" {
						out.Source = strings.TrimSuffix(
							fmt.Sprint(right), "/32")
					}
				}
			}
		}
	}

	if out.Proto == "" {
		return nftRule{}, false
	}
	return out, true
}

// parseNftRuleName 从规则名里取出协议与端口。
//
// 复用与 Windows 侧一致的命名约定：isc-<服务名>-<协议>-<端口>。
// 服务名里可能含连字符，因此**从右边**解析。
func parseNftRuleName(name string) (proto string, port uint16, ok bool) {
	m := nftRuleNamePattern.FindStringSubmatch(name)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.ParseUint(m[2], 10, 16)
	if err != nil || n == 0 {
		return "", 0, false
	}
	// 前导零会让 isc-x-tcp-070 与 isc-x-tcp-70 被当成两条不同的规则，
	// 而它们实际指向同一个端口 —— 正则里的 [1-9] 已经挡住了这一点，
	// 这里是第二道保险。
	if len(m[2]) > 1 && m[2][0] == '0' {
		return "", 0, false
	}
	return m[1], uint16(n), true
}

// NftComment 返回一条 ISC 规则在 nftables 里的注释标记。
func NftComment(ruleName string) string { return nftCommentPrefix + ruleName }

// ---------------------------------------------------------------------------
// 命令拼装
// ---------------------------------------------------------------------------

// NftCommands 生成把期望状态落到 nftables 所需的命令序列。
//
// existing 是当前已在 ISC 表里的规则，desired 是期望的规则集合。
//
// # 为什么用"差集"而不是"清空重建"
//
// 清空重建会让**防火墙出现一个短暂的空窗**：删掉旧规则到加上新规则
// 之间，那些端口是关闭的。对正在使用的连接来说，那是一次可观测的断开。
// 而算差集只动真正需要变的那几条。
//
// 返回的命令顺序是**先增后删**（与直觉相反）：
//
//	先增：新的放行规则立即生效，不会出现"旧规则已删、新规则未加"的空窗
//	后删：被移除的规则在最后才消失
//
// 代价是极短的一瞬间可能同时存在新旧两条规则 —— 那只会让某个端口
// 多放行几毫秒，而不是把它关掉。
func NftCommands(existing []nftRule, desired []Rule) []string {
	have := make(map[string]nftRule, len(existing))
	for _, r := range existing {
		have[r.Name] = r
	}

	want := make(map[string]Rule, len(desired))
	for _, r := range desired {
		want[r.Name] = r
	}

	// 表与链不存在时先建。用 `;` 串起来而不是分开执行：
	// nft 支持一次传多条命令，而那比逐条执行少几轮进程启动。
	var cmds []string
	cmds = append(cmds, fmt.Sprintf(
		"add table inet %s; add chain inet %s %s { type filter hook input priority 0; policy accept; }",
		nftTable, nftTable, nftChain))

	// --- 先增 ---
	//
	// 已经存在的规则不动：nftables 允许同名规则共存，而我们靠
	// comment 里的名字区分。不加判断的话每跑一次就会多一条重复规则。
	for _, r := range desired {
		if _, ok := have[r.Name]; ok {
			continue // 已在（或需要更新），下面单独处理
		}
		cmds = append(cmds, nftAddCommand(r))
	}

	// 需要更新的规则：先删旧的再加新的。
	//
	// 端口或协议变了意味着这不是"同一条规则"，但名字一样 ——
	// 而按名字判断幂等会以为它已经存在，于是改动永远不生效。
	for _, r := range desired {
		old, ok := have[r.Name]
		if !ok {
			continue
		}
		if old.Proto == nftProtoOf(r) && old.Port == r.Port && old.Source == r.Source {
			continue
		}
		cmds = append(cmds,
			fmt.Sprintf("delete rule inet %s %s handle %d", nftTable, nftChain, old.Handle),
			nftAddCommand(r))
	}

	// --- 后删 ---
	for _, old := range existing {
		if _, stillWanted := want[old.Name]; stillWanted {
			continue
		}
		cmds = append(cmds, fmt.Sprintf(
			"delete rule inet %s %s handle %d", nftTable, nftChain, old.Handle))
	}

	return cmds
}

// nftAddCommand 生成新增一条规则的命令。
func nftAddCommand(r Rule) string {
	proto := strings.ToLower(string(r.Protocol))
	if proto == "" {
		proto = "tcp"
	}

	// 端口区间与单端口的写法不同。
	portExpr := ""
	if r.Port.To > r.Port.From {
		portExpr = fmt.Sprintf("%s dport %d-%d", proto, r.Port.From, r.Port.To)
	} else {
		portExpr = fmt.Sprintf("%s dport %d", proto, r.Port.From)
	}

	// 来源限制。
	src := ""
	if s := strings.TrimSpace(r.Source); s != "" {
		// IPv6 地址在 nft 里要写成 saddr，而族由 inet 表决定。
		src = fmt.Sprintf("ip saddr %s ", s)
	}

	return fmt.Sprintf(
		"add rule inet %s %s %s%s accept comment %q",
		nftTable, nftChain, src, portExpr, NftComment(r.Name))
}

func nftProtoOf(r Rule) Protocol {
	if r.Protocol == "" {
		return TCP
	}
	return r.Protocol
}

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

// ValidateNftRule 检查一条规则能否安全地拼进命令。
//
// # 为什么要校验
//
// 规则名与来源会被拼进传给 nft 的命令串，而这些值最终来自用户输入
// （服务名、来源地址）。一个含换行或分号的名字能**越出这条命令**，
// 变成一条额外的 nft 指令 —— 那是一个能改写整张防火墙表的注入点。
func ValidateNftRule(r Rule) error {
	if !nftRuleNamePattern.MatchString(r.Name) {
		return fmt.Errorf(
			"platform: 规则名 %q 不合法。"+
				"要求形如 isc-<服务名>-<tcp|udp>-<端口>，"+
				"服务名只能用小写字母、数字与连字符", r.Name)
	}
	if r.Port.From == 0 {
		return fmt.Errorf("platform: 规则 %s 的端口不能为 0", r.Name)
	}
	if r.Port.To != 0 && r.Port.To < r.Port.From {
		return fmt.Errorf("platform: 规则 %s 的端口区间是反的（%d > %d）",
			r.Name, r.Port.From, r.Port.To)
	}

	if s := strings.TrimSpace(r.Source); s != "" {
		// 换行与分号会越出命令串；空格会改变参数结构。
		if strings.ContainsAny(s, "\n\r;\"'`$&|") {
			return fmt.Errorf("platform: 规则 %s 的来源地址含有非法字符: %q",
				r.Name, s)
		}
		if strings.ContainsAny(s, " \t") {
			return fmt.Errorf("platform: 规则 %s 的来源地址含有空白: %q",
				r.Name, s)
		}
	}
	return nil
}
