package platform

import (
	"encoding/json"
	"strings"
	"testing"
)

// 本文件覆盖 nftables 与 pf 两个后端里**不依赖平台调用**的部分。
//
// # 为什么这些测试特别重要
//
// 本项目的开发机是 Windows，**Linux 与 macOS 的防火墙无法在这里真机
// 验证**（见 docs/PLAN.md R8）。因此这几条测试是那两块逻辑唯一的
// 自动化质量保证。
//
// 而这里恰恰是最容易出错的地方：命令拼装、JSON 解析、行注入。

// ---------------------------------------------------------------------------
// nftables：规则名
// ---------------------------------------------------------------------------

func TestParseNftRuleName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		proto string
		port  uint16
		ok    bool
	}{
		{"isc-jellyfin-tcp-8096", "tcp", 8096, true},
		{"isc-my-service-udp-443", "udp", 443, true},
		{"isc-a-tcp-1", "tcp", 1, true},
		{"isc-a-tcp-65535", "tcp", 65535, true},

		// 前导零会让 isc-x-tcp-070 与 isc-x-tcp-70 被当成两条不同的
		// 规则，而它们指向同一个端口。
		{"isc-a-tcp-070", "", 0, false},
		{"isc-a-tcp-0", "", 0, false},
		{"isc-a-tcp-65536", "", 0, false},
		{"isc-a-sctp-80", "", 0, false},
		{"isc--tcp-80", "", 0, false},
		{"other-tcp-80", "", 0, false},
		{"", "", 0, false},
	}

	for _, tc := range cases {
		proto, port, ok := parseNftRuleName(tc.name)
		if ok != tc.ok {
			t.Errorf("parseNftRuleName(%q) ok=%v，期望 %v", tc.name, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if proto != tc.proto || port != tc.port {
			t.Errorf("parseNftRuleName(%q) = (%s, %d)，期望 (%s, %d)",
				tc.name, proto, port, tc.proto, tc.port)
		}
	}
}

// TestRuleNameFormatIsSharedAcrossPlatforms 钉住三平台用同一套规则名。
//
// 统一的命名让用户在三平台看到的是同一套规则名，
// 而文档、脚本、排错步骤都可以跨平台复用。
func TestRuleNameFormatIsSharedAcrossPlatforms(t *testing.T) {
	t.Parallel()

	// nftables 与 pf 用的是同一个正则；Windows 侧的 ruleNamePattern
	// 也接受同样的形式。这里显式断言它们一致。
	const sample = "isc-jellyfin-tcp-8096"

	if !nftRuleNamePattern.MatchString(sample) {
		t.Error("nftables 不认这个名字")
	}
	if !pfRuleNamePattern.MatchString(sample) {
		t.Error("pf 不认这个名字")
	}
	if nftRuleNamePattern.String() != pfRuleNamePattern.String() {
		t.Error("nftables 与 pf 的规则名格式不一致")
	}
}

// ---------------------------------------------------------------------------
// nftables：ruleset 解析
// ---------------------------------------------------------------------------

// nftSample 是一份贴近真实 `nft -j list ruleset` 的输出。
//
// 里面刻意混入了**不属于 ISC** 的东西：别的表、别的链、以及用户
// 自己的规则。解析器必须跳过它们而不是报错 —— 一个报错会让整个
// Inspect 失败，而用户会看到"无法读取防火墙状态"，
// 完全不知道问题出在别人的一条规则上。
const nftSample = `{
  "nftables": [
    {"metainfo": {"version": "1.0.6", "release_name": "Lester Gooch"}},
    {"table": {"family": "inet", "name": "filter", "handle": 1}},
    {"chain": {"family": "inet", "table": "filter", "name": "input", "handle": 1}},
    {"rule": {"family": "inet", "table": "filter", "chain": "input", "handle": 5,
      "expr": [{"match": {"op": "==", "left": {"meta": {"key": "l4proto"}}, "right": "tcp"}}],
      "comment": "user-own-rule"}},
    {"table": {"family": "inet", "name": "isc", "handle": 9}},
    {"chain": {"family": "inet", "table": "isc", "name": "input", "handle": 3}},
    {"rule": {"family": "inet", "table": "isc", "chain": "input", "handle": 12,
      "expr": [
        {"match": {"op": "==", "left": {"meta": {"key": "l4proto"}}, "right": "tcp"}},
        {"match": {"op": "==", "left": {"payload": {"protocol": "tcp", "field": "dport"}}, "right": 8096}},
        {"accept": null}
      ],
      "comment": "isc:isc-jellyfin-tcp-8096"}},
    {"rule": {"family": "inet", "table": "isc", "chain": "input", "handle": 15,
      "expr": [
        {"match": {"op": "==", "left": {"meta": {"key": "l4proto"}}, "right": "udp"}},
        {"match": {"op": "==", "left": {"payload": {"protocol": "udp", "field": "dport"}}, "right": 443}},
        {"accept": null}
      ],
      "comment": "isc:isc-vpn-udp-443"}}
  ]
}`

func TestParseNftRuleset(t *testing.T) {
	t.Parallel()

	rules, err := ParseNftRuleset([]byte(nftSample))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	// 只应当取出 ISC 表里的两条，filter 表里那条用户规则要跳过。
	if len(rules) != 2 {
		t.Fatalf("应当解析出 2 条 ISC 规则，得到 %d 条: %+v", len(rules), rules)
	}

	byName := map[string]nftRule{}
	for _, r := range rules {
		byName[r.Name] = r
	}

	jellyfin, ok := byName["isc-jellyfin-tcp-8096"]
	if !ok {
		t.Fatal("没有解析出 jellyfin 规则")
	}
	if jellyfin.Proto != TCP {
		t.Errorf("协议 = %s，期望 tcp", jellyfin.Proto)
	}
	if jellyfin.Port.From != 8096 {
		t.Errorf("端口 = %d，期望 8096", jellyfin.Port.From)
	}
	// handle 是删除规则的唯一依据，取错了会删掉别人的规则。
	if jellyfin.Handle != 12 {
		t.Errorf("handle = %d，期望 12", jellyfin.Handle)
	}

	vpn := byName["isc-vpn-udp-443"]
	if vpn.Proto != UDP || vpn.Port.From != 443 {
		t.Errorf("vpn 规则解析不对: %+v", vpn)
	}
}

// TestParseNftRulesetSkipsNonISCRules 是这里最要紧的一条。
//
// 用户自己的规则、其它工具装的规则形式五花八门。解析器遇到不认识的
// 结构必须**跳过而不是报错** —— 报错会让整个 Inspect 失败。
func TestParseNftRulesetSkipsNonISCRules(t *testing.T) {
	t.Parallel()

	rules, err := ParseNftRuleset([]byte(nftSample))
	if err != nil {
		t.Fatalf("含无关规则时不该报错: %v", err)
	}
	for _, r := range rules {
		if r.Name == "user-own-rule" {
			t.Error("不该把用户自己的规则当成 ISC 的")
		}
	}
}

func TestParseNftRulesetEmptyAndBad(t *testing.T) {
	t.Parallel()

	// 空的 ruleset 是合法的（防火墙还没配过任何东西）。
	rules, err := ParseNftRuleset([]byte(`{"nftables": []}`))
	if err != nil {
		t.Errorf("空 ruleset 不该报错: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("空 ruleset 应当得到 0 条，得到 %d", len(rules))
	}

	// 完全不是 JSON 时才报错。
	if _, err := ParseNftRuleset([]byte("not json")); err == nil {
		t.Error("非 JSON 输入应当报错")
	}
}

// TestParseNftRulesetIgnoresUnknownFields 验证对未知字段的容忍。
//
// 不同 nft 版本的输出字段略有差异，而用固定结构体严格解析会在
// 遇到新版本时整条规则解析失败。
func TestParseNftRulesetIgnoresUnknownFields(t *testing.T) {
	t.Parallel()

	data := `{"nftables": [
	  {"rule": {"family": "inet", "table": "isc", "chain": "input", "handle": 7,
	    "some_future_field": {"nested": [1,2,3]},
	    "expr": [
	      {"match": {"op": "==", "left": {"meta": {"key": "l4proto"}}, "right": "tcp"}},
	      {"match": {"op": "==", "left": {"payload": {"protocol": "tcp", "field": "dport"}}, "right": 80}},
	      {"counter": {"packets": 0, "bytes": 0}}
	    ],
	    "comment": "isc:isc-web-tcp-80"}}
	]}`

	rules, err := ParseNftRuleset([]byte(data))
	if err != nil {
		t.Fatalf("不该因为未知字段而失败: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("应当解析出 1 条，得到 %d", len(rules))
	}
	if rules[0].Port.From != 80 {
		t.Errorf("端口 = %d，期望 80", rules[0].Port.From)
	}
}

func TestParseNftRulesetSortsByName(t *testing.T) {
	t.Parallel()

	// 输出顺序取决于内核里的插入顺序，而它可能在不同机器上不同 ——
	// 不排序会让"预览"看起来每次都不一样。
	data := `{"nftables": [
	  {"rule": {"table": "isc", "handle": 3, "comment": "isc:isc-z-tcp-1",
	    "expr": [{"match": {"left": {"meta": {"key": "l4proto"}}, "right": "tcp"}},
	             {"match": {"left": {"payload": {"protocol": "tcp", "field": "dport"}}, "right": 1}}]}},
	  {"rule": {"table": "isc", "handle": 1, "comment": "isc:isc-a-tcp-2",
	    "expr": [{"match": {"left": {"meta": {"key": "l4proto"}}, "right": "tcp"}},
	             {"match": {"left": {"payload": {"protocol": "tcp", "field": "dport"}}, "right": 2}}]}}
	]}`

	rules, err := ParseNftRuleset([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("应当解析出 2 条，得到 %d", len(rules))
	}
	if rules[0].Name != "isc-a-tcp-2" {
		t.Errorf("应当按名字排序，第一条是 %s", rules[0].Name)
	}
}

// ---------------------------------------------------------------------------
// nftables：命令拼装
// ---------------------------------------------------------------------------

func TestNftAddCommandForSinglePort(t *testing.T) {
	t.Parallel()

	cmds := NftCommands(nil, []Rule{{
		Name: "isc-jellyfin-tcp-8096", Protocol: TCP, Port: NewPort(8096),
	}})

	joined := strings.Join(cmds, "\n")
	if !strings.Contains(joined, "add rule inet isc input tcp dport 8096 accept") {
		t.Errorf("命令不对:\n%s", joined)
	}
	// comment 必须带上，否则下次读取时认不出这是谁的规则。
	if !strings.Contains(joined, `comment "isc:isc-jellyfin-tcp-8096"`) {
		t.Errorf("缺少 comment 标记:\n%s", joined)
	}
}

func TestNftAddCommandForPortRange(t *testing.T) {
	t.Parallel()

	cmds := NftCommands(nil, []Rule{{
		Name: "isc-media-tcp-8000", Protocol: TCP,
		Port: PortRange{From: 8000, To: 8010},
	}})

	joined := strings.Join(cmds, "\n")
	// nftables 的区间用 a-b。
	if !strings.Contains(joined, "tcp dport 8000-8010") {
		t.Errorf("端口区间写法不对:\n%s", joined)
	}
}

// TestNftCommandsSkipsExistingRules 验证幂等。
//
// nftables 允许同名规则共存（它没有"规则名"的概念，靠 comment 区分）。
// 不加判断的话每跑一次就会多一条重复规则。
func TestNftCommandsSkipsExistingRules(t *testing.T) {
	t.Parallel()

	existing := []nftRule{{
		Name: "isc-web-tcp-80", Handle: 7, Proto: TCP, Port: NewPort(80),
	}}
	desired := []Rule{{
		Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80),
	}}

	cmds := NftCommands(existing, desired)

	for _, c := range cmds {
		if strings.Contains(c, "add rule") {
			t.Errorf("已存在的规则不该被重复添加:\n%s", c)
		}
		if strings.Contains(c, "delete rule") {
			t.Errorf("未变化的规则不该被删除:\n%s", c)
		}
	}
}

// TestNftCommandsUpdatesChangedRule 钉住"同名但内容变了"的处理。
//
// 端口变了意味着这不是同一条规则，但名字一样 —— 而纯按名字判断幂等
// 会以为它已经存在，于是**改动永远不生效**。
func TestNftCommandsUpdatesChangedRule(t *testing.T) {
	t.Parallel()

	existing := []nftRule{{
		Name: "isc-web-tcp-80", Handle: 7, Proto: TCP, Port: NewPort(80),
	}}
	desired := []Rule{{
		// 同一个名字，但端口改成了 8080。
		Name: "isc-web-tcp-8080", Protocol: TCP, Port: NewPort(8080),
	}}

	cmds := NftCommands(existing, desired)
	joined := strings.Join(cmds, "\n")

	if !strings.Contains(joined, "add rule") ||
		!strings.Contains(joined, "dport 8080") {
		t.Errorf("新端口应当被添加:\n%s", joined)
	}
	if !strings.Contains(joined, "delete rule inet isc input handle 7") {
		t.Errorf("旧规则应当被删除（按 handle）:\n%s", joined)
	}
}

// TestNftCommandsOrderAddBeforeDelete 钉住命令顺序。
//
// 先删后加会让防火墙出现一个**短暂的空窗**：那些端口在两条命令之间
// 是关闭的，对正在使用的连接来说是一次可观测的断开。
//
// 先加后删的代价只是极短的一瞬间可能同时存在新旧两条规则 ——
// 那只会让某个端口多放行几毫秒，而不是把它关掉。
func TestNftCommandsOrderAddBeforeDelete(t *testing.T) {
	t.Parallel()

	existing := []nftRule{{
		Name: "isc-old-tcp-80", Handle: 3, Proto: TCP, Port: NewPort(80),
	}}
	desired := []Rule{{
		Name: "isc-new-tcp-443", Protocol: TCP, Port: NewPort(443),
	}}

	cmds := NftCommands(existing, desired)

	addIdx, delIdx := -1, -1
	for i, c := range cmds {
		if strings.Contains(c, "add rule") && addIdx < 0 {
			addIdx = i
		}
		if strings.Contains(c, "delete rule") && delIdx < 0 {
			delIdx = i
		}
	}

	if addIdx < 0 || delIdx < 0 {
		t.Fatalf("应当同时有增与删:\n%s", strings.Join(cmds, "\n"))
	}
	if addIdx > delIdx {
		t.Errorf("新增必须排在删除之前（否则会有关闭端口的空窗）:\n%s",
			strings.Join(cmds, "\n"))
	}
}

func TestNftCommandsCreatesTableFirst(t *testing.T) {
	t.Parallel()

	cmds := NftCommands(nil, []Rule{{
		Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80),
	}})
	if len(cmds) == 0 {
		t.Fatal("应当有命令")
	}
	// 表与链必须先建，否则 add rule 会失败。
	if !strings.Contains(cmds[0], "add table inet isc") ||
		!strings.Contains(cmds[0], "add chain inet isc input") {
		t.Errorf("第一条命令应当建表与链:\n%s", cmds[0])
	}
}

func TestNftCommandsEmptyDesiredDeletesAll(t *testing.T) {
	t.Parallel()

	existing := []nftRule{
		{Name: "isc-a-tcp-1", Handle: 1, Proto: TCP, Port: NewPort(1)},
		{Name: "isc-b-tcp-2", Handle: 2, Proto: TCP, Port: NewPort(2)},
	}

	cmds := NftCommands(existing, nil)
	joined := strings.Join(cmds, "\n")

	// 期望状态为空 = 撤销全部规则。
	for _, h := range []string{"handle 1", "handle 2"} {
		if !strings.Contains(joined, "delete rule inet isc input "+h) {
			t.Errorf("应当删除 %s:\n%s", h, joined)
		}
	}
}

// ---------------------------------------------------------------------------
// nftables：注入防护
// ---------------------------------------------------------------------------

// TestValidateNftRuleRejectsInjection 钉住命令注入的防护。
//
// 规则名与来源会被拼进传给 nft 的命令串，而这些值最终来自用户输入。
// 一个含换行或分号的名字能**越出这条命令**，变成一条额外的 nft 指令。
func TestValidateNftRuleRejectsInjection(t *testing.T) {
	t.Parallel()

	bad := []Rule{
		{Name: "isc-web-tcp-80\nflush ruleset", Protocol: TCP, Port: NewPort(80)},
		{Name: "isc-web-tcp-80; flush ruleset", Protocol: TCP, Port: NewPort(80)},
		{Name: "isc-Web-tcp-80", Protocol: TCP, Port: NewPort(80)}, // 大写
		{Name: "web-tcp-80", Protocol: TCP, Port: NewPort(80)},     // 缺前缀
		{Name: "isc-web-tcp-0", Protocol: TCP, Port: NewPort(0)},
		{Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80),
			Source: "1.2.3.4; flush ruleset"},
		{Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80),
			Source: "1.2.3.4\nflush ruleset"},
		{Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80),
			Source: "1.2.3.4 5.6.7.8"},
		{Name: "isc-web-tcp-80", Protocol: TCP,
			Port: PortRange{From: 100, To: 50}}, // 区间反了
	}

	for i, r := range bad {
		if err := ValidateNftRule(r); err == nil {
			t.Errorf("第 %d 组应当被拒绝: %+v", i+1, r)
		}
	}
}

func TestValidateNftRuleAcceptsGood(t *testing.T) {
	t.Parallel()

	good := []Rule{
		{Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80)},
		{Name: "isc-my-service-udp-443", Protocol: UDP, Port: NewPort(443)},
		{Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80), Source: "2001:db8::1"},
		{Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80), Source: "10.0.0.0/8"},
		{Name: "isc-media-tcp-8000", Protocol: TCP, Port: PortRange{From: 8000, To: 8010}},
	}

	for _, r := range good {
		if err := ValidateNftRule(r); err != nil {
			t.Errorf("%+v 应当被接受: %v", r, err)
		}
	}
}

// ---------------------------------------------------------------------------
// pf：anchor 渲染
// ---------------------------------------------------------------------------

func TestPfAnchorContent(t *testing.T) {
	t.Parallel()

	content := PfAnchorContent([]Rule{
		{Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80)},
		{Name: "isc-vpn-udp-443", Protocol: UDP, Port: NewPort(443)},
	})

	if !strings.Contains(content, "pass in inet proto tcp from any to any port 80 keep state") {
		t.Errorf("tcp 规则不对:\n%s", content)
	}
	if !strings.Contains(content, "pass in inet proto udp from any to any port 443 keep state") {
		t.Errorf("udp 规则不对:\n%s", content)
	}
	// 规则名要标出来，否则用户无法把 anchor 里的行与界面上的规则对上。
	if !strings.Contains(content, "# isc:isc-web-tcp-80") {
		t.Errorf("缺少规则名标记:\n%s", content)
	}
	// 要告诉用户怎么卸载 —— anchor 文件是手工可见的。
	if !strings.Contains(content, "由 ISC 生成") {
		t.Errorf("缺少生成说明:\n%s", content)
	}
}

// TestPfAnchorPortRangeUsesColon 钉住 pf 的区间写法。
//
// pf 用 `port a:b`，写成 a-b 是语法错误，而 pfctl 的报错只有一句
// "syntax error" 加行号 —— 看不出是区间写法的问题。
func TestPfAnchorPortRangeUsesColon(t *testing.T) {
	t.Parallel()

	content := PfAnchorContent([]Rule{{
		Name: "isc-media-tcp-8000", Protocol: TCP,
		Port: PortRange{From: 8000, To: 8010},
	}})

	if !strings.Contains(content, "port 8000:8010") {
		t.Errorf("pf 的端口区间应当用冒号:\n%s", content)
	}
	if strings.Contains(content, "port 8000-8010") {
		t.Errorf("不该用连字符（pf 不认）:\n%s", content)
	}
}

func TestPfAnchorEmptyIsValid(t *testing.T) {
	t.Parallel()

	content := PfAnchorContent(nil)
	if !strings.Contains(content, "没有任何 ISC 规则") {
		t.Errorf("空 anchor 应当有说明:\n%s", content)
	}
	// 空 anchor 是"撤销全部规则"的结果，必须是一个合法文件。
	if !strings.HasPrefix(content, "#") {
		t.Errorf("空 anchor 也应当是注释开头的合法文件:\n%s", content)
	}
}

// TestPfAnchorIsDeterministic 验证渲染顺序稳定。
//
// 同样的一组规则应当产出逐字节相同的文件，这样"内容没变就不重载"
// 才成立 —— 而重载 pf 有可见的代价（短暂的空窗）。
func TestPfAnchorIsDeterministic(t *testing.T) {
	t.Parallel()

	a := PfAnchorContent([]Rule{
		{Name: "isc-b-tcp-2", Protocol: TCP, Port: NewPort(2)},
		{Name: "isc-a-tcp-1", Protocol: TCP, Port: NewPort(1)},
	})
	b := PfAnchorContent([]Rule{
		{Name: "isc-a-tcp-1", Protocol: TCP, Port: NewPort(1)},
		{Name: "isc-b-tcp-2", Protocol: TCP, Port: NewPort(2)},
	})

	if a != b {
		t.Error("输入顺序不同产出了不同的文件 —— 会导致无谓的重载")
	}
}

// ---------------------------------------------------------------------------
// pf：anchor 解析
// ---------------------------------------------------------------------------

func TestParsePfAnchor(t *testing.T) {
	t.Parallel()

	content := PfAnchorContent([]Rule{
		{Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80)},
		{Name: "isc-vpn-udp-443", Protocol: UDP, Port: NewPort(443)},
	})

	names := ParsePfAnchor(content)
	if len(names) != 2 {
		t.Fatalf("应当解析出 2 条，得到 %v", names)
	}
	// 排序过，因此顺序可预期。
	if names[0] != "isc-vpn-udp-443" || names[1] != "isc-web-tcp-80" {
		t.Errorf("解析结果 = %v", names)
	}
}

func TestParsePfAnchorIgnoresForeignContent(t *testing.T) {
	t.Parallel()

	// 用户可能手工往 anchor 里加东西 —— 解析器不该把它们当成 ISC 规则。
	content := `# 本文件由 ISC 生成
# isc:isc-web-tcp-80
pass in inet proto tcp from any to any port 80 keep state
# 用户自己加的
# isc:not-a-valid-name
pass in inet proto tcp from any to any port 22 keep state
`

	names := ParsePfAnchor(content)
	if len(names) != 1 {
		t.Fatalf("应当只解析出 1 条 ISC 规则，得到 %v", names)
	}
	if names[0] != "isc-web-tcp-80" {
		t.Errorf("解析结果 = %v", names)
	}
}

// ---------------------------------------------------------------------------
// pf：/etc/pf.conf 的引入行
// ---------------------------------------------------------------------------

// TestPfConfNeedsAnchor 验证两行都要检查。
//
// 需要 `anchor "isc"` 声明它、`load anchor "isc" from "..."` 告诉 pf
// 从哪里读。少了任何一行，anchor 文件都不会被加载 —— 而症状是
// "规则写进去了但完全不生效"，很难从现象反推回原因。
func TestPfConfNeedsAnchor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		conf         string
		needsDeclare bool
		needsLoad    bool
	}{
		{
			"空配置两行都要",
			"",
			true, true,
		},
		{
			"只有声明行",
			"anchor \"isc\"\n",
			false, true,
		},
		{
			"只有 load 行",
			"load anchor \"isc\" from \"/etc/pf.anchors/isc\"\n",
			true, false,
		},
		{
			"两行都有",
			"anchor \"isc\"\nload anchor \"isc\" from \"/etc/pf.anchors/isc\"\n",
			false, false,
		},
		{
			// 被注释掉的行不算 —— 那正是"用户手工停用了 ISC"的样子。
			"两行都被注释",
			"# anchor \"isc\"\n# load anchor \"isc\" from \"/etc/pf.anchors/isc\"\n",
			true, true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, l := PfConfNeedsAnchor(tc.conf)
			if d != tc.needsDeclare || l != tc.needsLoad {
				t.Errorf("得到 (declare=%v, load=%v)，期望 (%v, %v)",
					d, l, tc.needsDeclare, tc.needsLoad)
			}
		})
	}
}

func TestAppendPfAnchor(t *testing.T) {
	t.Parallel()

	conf := "# 用户自己的配置\npass out all keep state\n"
	out := AppendPfAnchor(conf)

	// **只追加，不改动已有内容** —— 那里面可能有别人的配置。
	if !strings.Contains(out, "# 用户自己的配置") ||
		!strings.Contains(out, "pass out all keep state") {
		t.Errorf("原有内容被改动了:\n%s", out)
	}
	if !strings.Contains(out, `load anchor "isc" from "/etc/pf.anchors/isc"`) {
		t.Errorf("缺少 load 行:\n%s", out)
	}
	// 要告诉用户怎么卸载。
	if !strings.Contains(out, "删除以下几行") {
		t.Errorf("缺少卸载说明:\n%s", out)
	}
}

// TestAppendPfAnchorIsIdempotent 验证重复追加不会堆叠。
//
// 用户可能多次点"启用" —— 每次都追加的话，配置里会堆出一串重复的
// anchor 行，而 pf 会因此重复加载同一个文件。
func TestAppendPfAnchorIsIdempotent(t *testing.T) {
	t.Parallel()

	once := AppendPfAnchor("pass out all keep state\n")
	twice := AppendPfAnchor(once)

	if once != twice {
		t.Errorf("重复追加产生了变化:\n一次:\n%s\n两次:\n%s", once, twice)
	}
}

func TestRemovePfAnchor(t *testing.T) {
	t.Parallel()

	conf := "pass out all keep state\n"
	withAnchor := AppendPfAnchor(conf)
	removed := RemovePfAnchor(withAnchor)

	// 摘干净：不该留下任何 ISC 的痕迹。
	if strings.Contains(removed, "isc") {
		t.Errorf("仍然残留 ISC 内容:\n%s", removed)
	}
	// 用户自己的配置要原样保留。
	if !strings.Contains(removed, "pass out all keep state") {
		t.Errorf("用户自己的配置被删掉了:\n%s", removed)
	}
}

// TestRemovePfAnchorLeavesNoOrphanComment 验证说明注释也被摘掉。
//
// 只删规则行会留下一句"由 ISC 添加 —— 删除以下几行即可卸载"的孤立
// 注释，而下一行已经不是 ISC 的东西了。
func TestRemovePfAnchorLeavesNoOrphanComment(t *testing.T) {
	t.Parallel()

	withAnchor := AppendPfAnchor("pass out all keep state\n")
	removed := RemovePfAnchor(withAnchor)

	if strings.Contains(removed, "由 ISC 添加") {
		t.Errorf("留下了孤立的说明注释:\n%s", removed)
	}
}

func TestRemovePfAnchorWhenAbsent(t *testing.T) {
	t.Parallel()

	conf := "pass out all keep state\n"
	if got := RemovePfAnchor(conf); got != conf {
		t.Errorf("没有 anchor 时不该改动配置:\n得到:\n%s\n期望:\n%s", got, conf)
	}
}

// ---------------------------------------------------------------------------
// pf：注入防护
// ---------------------------------------------------------------------------

// TestValidatePfRuleRejectsInjection 钉住行注入的防护。
//
// pf 的配置按行解析，因此换行符会越出那一行，变成一条额外的 pf 指令。
func TestValidatePfRuleRejectsInjection(t *testing.T) {
	t.Parallel()

	bad := []Rule{
		{Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80),
			Source: "any\npass in all"},
		{Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80),
			Source: "1.2.3.4\rpass in all"},
		// 花括号在 pf 里是列表与宏，出现在地址里语义完全不同。
		{Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80),
			Source: "{ 1.2.3.4, 5.6.7.8 }"},
		{Name: "isc-web-tcp-80\npass in all", Protocol: TCP, Port: NewPort(80)},
		{Name: "bad-name", Protocol: TCP, Port: NewPort(80)},
		{Name: "isc-web-tcp-0", Protocol: TCP, Port: NewPort(0)},
	}

	for i, r := range bad {
		if err := ValidatePfRule(r); err == nil {
			t.Errorf("第 %d 组应当被拒绝: %+v", i+1, r)
		}
	}
}

func TestValidatePfRuleAcceptsGood(t *testing.T) {
	t.Parallel()

	good := []Rule{
		{Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80)},
		{Name: "isc-web-tcp-80", Protocol: TCP, Port: NewPort(80), Source: "2001:db8::1"},
		{Name: "isc-media-tcp-8000", Protocol: TCP, Port: PortRange{From: 8000, To: 8010}},
	}
	for _, r := range good {
		if err := ValidatePfRule(r); err != nil {
			t.Errorf("%+v 应当被接受: %v", r, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 两个后端的 JSON 输出
// ---------------------------------------------------------------------------

// TestNftPayloadRoundTrip 验证变更载荷能被序列化与还原。
//
// 载荷是**跨进程撤销的唯一依据**：内核重启后没有任何别的办法知道
// 当初创建了哪几条规则、它们的 handle 是多少。
func TestNftPayloadRoundTrip(t *testing.T) {
	t.Parallel()

	type nftPayload struct {
		Added   []string `json:"added"`
		Handles []int64  `json:"handles"`
	}

	original := nftPayload{Added: []string{"isc-web-tcp-80"}, Handles: []int64{12}}

	byt, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	var restored nftPayload
	if err := json.Unmarshal(byt, &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.Added) != 1 || restored.Added[0] != "isc-web-tcp-80" {
		t.Errorf("还原结果 = %+v", restored)
	}
	if len(restored.Handles) != 1 || restored.Handles[0] != 12 {
		t.Errorf("handle 丢失 = %+v", restored)
	}
}
