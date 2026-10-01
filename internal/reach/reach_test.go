package reach

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/change"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// 本文件覆盖可达性探测的**结论质量**。
//
// 这里测的不是"函数返回了什么"，而是"用户看到的东西对不对"：
// 探错方向比不探测更糟 —— 用户会照着错误的提示去改一堆无关的设置。
//
// 尤其重要的是最后那几条：探测**不能**声称上游可达，
// 因为本机根本没有能力验证它。

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

type fakeMonitor struct {
	ifaces []platform.InterfaceAddrs
	err    error
}

func (m *fakeMonitor) Snapshot(context.Context) ([]platform.InterfaceAddrs, error) {
	return m.ifaces, m.err
}

// fakeFirewall 是一个可编程的防火墙后端。
type fakeFirewall struct {
	planChange platform.Change
	planErr    error

	applied   []platform.Change
	rolledBAK []platform.Change
	applyErr  error
	revertErr error
}

func (f *fakeFirewall) Inspect(context.Context) ([]platform.Rule, error) { return nil, nil }

func (f *fakeFirewall) Plan(context.Context, []platform.Rule) (platform.Change, error) {
	return f.planChange, f.planErr
}

func (f *fakeFirewall) Apply(_ context.Context, ch platform.Change) error {
	f.applied = append(f.applied, ch)
	return f.applyErr
}

func (f *fakeFirewall) Rollback(_ context.Context, ch platform.Change) error {
	f.rolledBAK = append(f.rolledBAK, ch)
	return f.revertErr
}

func (f *fakeFirewall) Describe() platform.ImplState {
	return platform.ImplState{Available: true, Backend: "fake"}
}

func ifaceWith(name string, ipv6 []string, prefixes []string) platform.InterfaceAddrs {
	out := platform.InterfaceAddrs{Name: name, IsUp: true}
	for _, s := range ipv6 {
		out.IPv6 = append(out.IPv6, mustAddr(s))
	}
	for _, s := range prefixes {
		out.Prefixes = append(out.Prefixes, mustPrefix(s))
	}
	return out
}

func healthyMonitor() *fakeMonitor {
	return &fakeMonitor{ifaces: []platform.InterfaceAddrs{
		ifaceWith("eth0",
			[]string{"240e:3b0:1111:2200::1", "fe80::1"},
			[]string{"240e:3b0:1111:2200::/64"}),
	}}
}

func availableFirewallState() platform.ImplState {
	return platform.ImplState{Available: true, Backend: "测试后端"}
}

func findCheck(t *testing.T, r Readiness, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("没有找到名为 %q 的检测项；实际有 %d 项", name, len(r.Checks))
	return Check{}
}

// ---------------------------------------------------------------------------
// 探测
// ---------------------------------------------------------------------------

// TestProbePassesWhenEverythingIsReady 验证全绿时不误报。
func TestProbePassesWhenEverythingIsReady(t *testing.T) {
	t.Parallel()

	p := NewIPv6Native(healthyMonitor(), &fakeFirewall{}, availableFirewallState())
	r, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("探测失败: %v", err)
	}

	if !r.Viable {
		t.Errorf("应当判定为可用；阻塞项: %+v", r.Checks)
	}
	if c := findCheck(t, r, "全局 IPv6 地址"); c.Status != CheckPass {
		t.Errorf("全局 IPv6 地址 = %s: %s", c.Status, c.Detail)
	}
	if c := findCheck(t, r, "IPv6 委派前缀"); c.Status != CheckPass {
		t.Errorf("委派前缀 = %s: %s", c.Status, c.Detail)
	}
	if c := findCheck(t, r, "本机防火墙后端"); c.Status != CheckPass {
		t.Errorf("防火墙后端 = %s: %s", c.Status, c.Detail)
	}
}

// TestProbeFailsWithoutGlobalIPv6 验证缺地址时给出**可执行**的提示。
//
// Detail 说"是什么"，Hint 说"该怎么办"。没有 Hint 的检测结果
// 对用户几乎没有价值 —— 他能自己看到"没有 IPv6"，但不知道该去改哪里。
func TestProbeFailsWithoutGlobalIPv6(t *testing.T) {
	t.Parallel()

	m := &fakeMonitor{ifaces: []platform.InterfaceAddrs{
		// 只有链路本地与私有地址 —— 它们不能用于公网访问。
		ifaceWith("eth0", []string{"fe80::1", "fd00::1"}, nil),
	}}
	p := NewIPv6Native(m, &fakeFirewall{}, availableFirewallState())

	r, err := p.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Viable {
		t.Error("没有全局 IPv6 时不该判定为可用")
	}

	c := findCheck(t, r, "全局 IPv6 地址")
	if c.Status != CheckFail {
		t.Errorf("状态 = %s，期望 fail", c.Status)
	}
	if c.Hint == "" {
		t.Fatal("必须给出「该怎么办」—— 否则用户只知道有问题，不知道改哪里")
	}
	// 提示必须提到路由器，因为那是用户唯一能自己动手的地方。
	if !strings.Contains(c.Hint, "路由器") {
		t.Errorf("提示没有指向用户可操作的环节: %s", c.Hint)
	}

	// 前缀那一项不该重复报同一个根因。
	if pc := findCheck(t, r, "IPv6 委派前缀"); pc.Status == CheckFail {
		t.Error("没有地址时，前缀不该单独报失败 —— 那会让用户以为有两个问题")
	}

	blocker, ok := r.BlockingCheck()
	if !ok {
		t.Fatal("应当能找出阻塞项")
	}
	if blocker.Name != "全局 IPv6 地址" {
		t.Errorf("阻塞项 = %s，期望「全局 IPv6 地址」", blocker.Name)
	}
}

// TestProbeReportsMissingPrefixAsWarning 验证"有地址但无委派前缀"的分寸。
//
// 这种情形下动态解析**仍然可用**（只是地址变化更频繁），
// 因此报 CheckFail 会让用户以为功能坏了。报 warn 才准确。
func TestProbeReportsMissingPrefixAsWarning(t *testing.T) {
	t.Parallel()

	m := &fakeMonitor{ifaces: []platform.InterfaceAddrs{
		ifaceWith("eth0", []string{"240e:3b0:1111:2200::1"}, nil),
	}}
	p := NewIPv6Native(m, &fakeFirewall{}, availableFirewallState())

	r, err := p.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(t, r, "IPv6 委派前缀")
	if c.Status != CheckWarn {
		t.Errorf("状态 = %s，期望 warn（功能可用，只是地址变化更频繁）", c.Status)
	}
	if !r.Viable {
		t.Error("没有委派前缀不该判为不可用")
	}
}

// TestProbeReportsUnimplementedFirewallHonestly 验证能力缺口被如实说明。
//
// 这一条尤其重要：用户看到"防火墙后端尚未实现"时，第一反应会是
// "是不是我哪里配错了"。Detail 必须明确说这是**内核的能力缺口**，
// 而不是他的配置问题。
func TestProbeReportsUnimplementedFirewallHonestly(t *testing.T) {
	t.Parallel()

	p := NewIPv6Native(healthyMonitor(), nil, platform.ImplState{
		Available: false, Backend: "stub",
		Note: "该平台后端尚未实现",
	})

	r, err := p.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Viable {
		t.Error("防火墙后端不可用时不该判定为可用（端口放不出去）")
	}

	c := findCheck(t, r, "本机防火墙后端")
	if c.Status != CheckFail {
		t.Errorf("状态 = %s，期望 fail", c.Status)
	}
	if !strings.Contains(c.Hint, "内核的能力缺口") {
		t.Errorf("提示必须明确这是内核的问题而不是用户的配置问题: %s", c.Hint)
	}
	if !strings.Contains(c.Hint, "手动") {
		t.Errorf("必须告诉用户可以手动放行作为替代: %s", c.Hint)
	}
}

// TestProbeNeverClaimsUpstreamReachable 是本文件最重要的一条。
//
// 从本机访问自己的公网地址通常会走回环（NAT 发夹），因此无论运营商
// 是否放行都会"成功"。如果探测声称"上游可达"，用户就会以为已经通了，
// 然后在手机打不开时去排查一堆本来没问题的东西。
//
// 正确做法是**承认测不了**，并把用户引向唯一有效的手段：外部验证。
func TestProbeNeverClaimsUpstreamReachable(t *testing.T) {
	t.Parallel()

	// 两种极端配置下，上游那一项都必须是 unknown。
	cases := []struct {
		name    string
		monitor *fakeMonitor
		fw      platform.ImplState
	}{
		{"全绿", healthyMonitor(), availableFirewallState()},
		{"什么都没有", &fakeMonitor{}, platform.ImplState{Available: false}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := NewIPv6Native(tc.monitor, &fakeFirewall{}, tc.fw)
			r, err := p.Probe(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			c := findCheck(t, r, "上游可达性")
			if c.Status != CheckUnknown {
				t.Errorf("上游可达性 = %s —— 本机没有能力验证它，必须是 unknown", c.Status)
			}
			if c.Scope != ScopeUpstream {
				t.Errorf("作用域 = %s，期望 upstream", c.Scope)
			}
			if !strings.Contains(c.Hint, "4G") && !strings.Contains(c.Hint, "手机") {
				t.Errorf("必须把用户引向外部验证: %s", c.Hint)
			}
		})
	}
}

// TestProbeIsReadOnly 验证探测不产生任何系统变更。
//
// 用户点"检查"时不该有任何东西被改动 —— 那会让他不敢点。
func TestProbeIsReadOnly(t *testing.T) {
	t.Parallel()

	fw := &fakeFirewall{}
	p := NewIPv6Native(healthyMonitor(), fw, availableFirewallState())

	if _, err := p.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fw.applied) != 0 || len(fw.rolledBAK) != 0 {
		t.Error("探测过程中不该有防火墙操作")
	}
}

func TestProbeSurvivesMonitorFailure(t *testing.T) {
	t.Parallel()

	m := &fakeMonitor{err: errors.New("网卡读取失败")}
	p := NewIPv6Native(m, &fakeFirewall{}, availableFirewallState())

	r, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("探测不该因网卡读取失败而整体报错（其他项仍然有价值）: %v", err)
	}
	if r.Viable {
		t.Error("读不到网卡时不该判定为可用")
	}
	if c := findCheck(t, r, "全局 IPv6 地址"); c.Status != CheckFail {
		t.Errorf("状态 = %s", c.Status)
	}
}

// ---------------------------------------------------------------------------
// 计划
// ---------------------------------------------------------------------------

func TestPlanBuildsRevertableStep(t *testing.T) {
	t.Parallel()

	ch := platform.Change{
		ID: "chg-1", Kind: "firewall.rules",
		Summary: "新增 1 条入站规则", Diff: "  + 入站 TCP 8096",
		Reversible: true, Payload: json.RawMessage(`{"rules":[]}`),
	}
	fw := &fakeFirewall{planChange: ch}
	p := NewIPv6Native(healthyMonitor(), fw, availableFirewallState())

	plan, err := p.Plan(context.Background(), Request{
		Port: 8096, Protocol: "tcp", Label: "Jellyfin",
	})
	if err != nil {
		t.Fatalf("生成计划失败: %v", err)
	}

	if plan.Kind != KindFirewallExpose {
		t.Errorf("Kind = %s", plan.Kind)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("生成的计划不合法: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("步骤数 = %d", len(plan.Steps))
	}
	if plan.Risk != change.RiskMedium {
		t.Errorf("风险 = %s，期望 medium", plan.Risk)
	}

	// 差异必须是给人看的。
	if len(plan.Diff()) == 0 {
		t.Error("计划必须带有可读的差异")
	}
}

func TestPlanForDangerousPortIsHighRisk(t *testing.T) {
	t.Parallel()

	fw := &fakeFirewall{planChange: platform.Change{
		Summary: "新增规则", Reversible: true,
		Payload: json.RawMessage(`{}`),
	}}
	p := NewIPv6Native(healthyMonitor(), fw, availableFirewallState())

	// 3389（远程桌面）开放出去等于把管理入口交出去。
	plan, err := p.Plan(context.Background(), Request{Port: 3389, Protocol: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Risk != change.RiskHigh {
		t.Errorf("开放 3389 的风险 = %s，期望 high", plan.Risk)
	}
}

// TestPlanMarksIrreversibleChange 验证不可撤销的变更被显著提示。
func TestPlanMarksIrreversibleChange(t *testing.T) {
	t.Parallel()

	fw := &fakeFirewall{planChange: platform.Change{
		Summary: "新增规则", Reversible: false,
		Payload: json.RawMessage(`{}`),
	}}
	p := NewIPv6Native(healthyMonitor(), fw, availableFirewallState())

	plan, err := p.Plan(context.Background(), Request{Port: 8096, Protocol: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) == 0 {
		t.Error("不可撤销的变更必须显式警告 —— 用户要知道没有退路")
	}
}

// TestPlanRejectsWhenFirewallUnavailable 验证能力缺口给出明确错误。
//
// 返回一个空计划是**错误**的做法：界面会显示"预览：无变更"，
// 用户以为已经放行了，实际什么都没发生。
func TestPlanRejectsWhenFirewallUnavailable(t *testing.T) {
	t.Parallel()

	p := NewIPv6Native(healthyMonitor(), nil, platform.ImplState{
		Available: false, Backend: "stub",
	})

	plan, err := p.Plan(context.Background(), Request{Port: 8096, Protocol: "tcp"})
	if err == nil {
		t.Fatal("防火墙后端不可用时必须报错，而不是返回空计划")
	}
	if !plan.Empty() {
		t.Error("报错时不该返回步骤")
	}
	// 错误信息必须包含用户可执行的替代做法。
	if !strings.Contains(err.Error(), "手动") {
		t.Errorf("错误信息应当告诉用户可以手动放行: %v", err)
	}
}

func TestPlanRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	p := NewIPv6Native(healthyMonitor(), &fakeFirewall{}, availableFirewallState())

	cases := []struct {
		name string
		req  Request
	}{
		{"端口为 0", Request{Port: 0, Protocol: "tcp"}},
		{"端口超范围", Request{Port: 70000, Protocol: "tcp"}},
		{"协议不支持", Request{Port: 8096, Protocol: "sctp"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := p.Plan(context.Background(), tc.req); err == nil {
				t.Error("应当被拒绝")
			}
		})
	}
}

// TestPlanPropagatesBackendError 验证后端的差异计算失败被如实转达。
func TestPlanPropagatesBackendError(t *testing.T) {
	t.Parallel()

	fw := &fakeFirewall{planErr: errors.New("需要管理员权限")}
	p := NewIPv6Native(healthyMonitor(), fw, availableFirewallState())

	_, err := p.Plan(context.Background(), Request{Port: 8096, Protocol: "tcp"})
	if err == nil {
		t.Fatal("应当报错")
	}
	// 后端的原因必须原样带上 —— 那是用户唯一能据此行动的线索。
	if !strings.Contains(err.Error(), "需要管理员权限") {
		t.Errorf("错误信息丢失了后端原因: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 撤销
// ---------------------------------------------------------------------------

// TestRevertReportsHonestlyInsteadOfPretending 验证撤销缺口被如实报告。
//
// 撤销一条防火墙规则需要当初那个 platform.Change（带着后端私有 payload），
// 而它没有持久化。与其假装做到（例如"删掉所有 isc- 开头的规则"，
// 那会误删用户手动加的），不如明确报错并告诉用户该做什么。
func TestRevertUsesPersistedPayload(t *testing.T) {
	t.Parallel()

	fw := &fakeFirewall{planChange: platform.Change{
		Summary: "新增 1 条入站规则", Diff: "  + 新增 isc-jellyfin-tcp-8096",
		Reversible: true,
		Payload:    json.RawMessage(`{"create":["isc-jellyfin-tcp-8096"]}`),
	}}
	p := NewIPv6Native(healthyMonitor(), fw, availableFirewallState())
	ctx := context.Background()

	// 生成计划时，后端私有的数据必须被带上。
	plan, err := p.Plan(ctx, Request{Port: 8096, Protocol: "tcp", Label: "jellyfin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Payload) == 0 {
		t.Fatal("计划必须带上后端私有的回滚数据 —— 否则跨进程撤销做不了")
	}

	// 模拟"内核重启后依据日志里的记录撤销"：
	// 这里只用 record，不依赖任何内存状态。
	rec := change.Record{
		PlanID: plan.ID, Kind: KindFirewallExpose, Title: plan.Title,
		Payload: plan.Payload,
	}
	if err := p.Revert(ctx, rec); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if len(fw.rolledBAK) != 1 {
		t.Fatalf("应当调用一次后端回滚，实际 %d 次", len(fw.rolledBAK))
	}
	// 回滚时必须把原始 payload 交还给后端 —— 那是它定位规则的唯一依据。
	if string(fw.rolledBAK[0].Payload) != string(plan.Payload) {
		t.Errorf("回滚时没有把 payload 交还给后端: %s", fw.rolledBAK[0].Payload)
	}
}

// TestRevertReportsHonestlyWhenPayloadMissing 验证缺口被如实报告。
//
// 旧版本的记录里没有 payload，此时**不能**假装成功，也不能猜
// （"删掉所有 isc- 开头的规则"会误删用户手动加的）。
func TestRevertReportsHonestlyWhenPayloadMissing(t *testing.T) {
	t.Parallel()

	fw := &fakeFirewall{}
	p := NewIPv6Native(healthyMonitor(), fw, availableFirewallState())

	err := p.Revert(context.Background(), change.Record{
		PlanID: "old-plan", Kind: KindFirewallExpose, Title: "旧版本的记录",
	})
	if err == nil {
		t.Fatal("没有回滚数据时必须报错而不是假装成功")
	}
	if len(fw.rolledBAK) != 0 {
		t.Error("没有回滚数据时不该调用后端")
	}

	msg := err.Error()
	if !strings.Contains(msg, "手动") {
		t.Errorf("必须告诉用户该怎么做: %s", msg)
	}
	if !strings.Contains(msg, rulePrefix) {
		t.Errorf("必须给出规则名前缀，用户才能在防火墙界面里找到它: %s", msg)
	}
}

// TestRevertReportsUnavailableBackend 验证换机器后的情形。
func TestRevertReportsUnavailableBackend(t *testing.T) {
	t.Parallel()

	p := NewIPv6Native(healthyMonitor(), nil, platform.ImplState{Available: false})

	err := p.Revert(context.Background(), change.Record{
		PlanID: "p1", Kind: KindFirewallExpose,
		Payload: json.RawMessage(`{"create":["isc-x-tcp-80"]}`),
	})
	if err == nil {
		t.Fatal("后端不可用时必须报错")
	}
	if !strings.Contains(err.Error(), "手动") {
		t.Errorf("必须告诉用户可以手动清理: %s", err)
	}
}

func TestReverterKindMatchesPlanKind(t *testing.T) {
	t.Parallel()

	p := NewIPv6Native(healthyMonitor(), &fakeFirewall{}, availableFirewallState())
	if p.Kind() != KindFirewallExpose {
		t.Errorf("Reverter 的 Kind = %s，与 Plan 的 Kind %s 不一致 —— "+
			"跨进程撤销会找不到对应的撤销器", p.Kind(), KindFirewallExpose)
	}
}

// ---------------------------------------------------------------------------
// 规则命名
// ---------------------------------------------------------------------------

// TestRuleNameIsSafe 验证规则名不会把危险字符带进系统命令。
//
// 规则名最终会出现在防火墙的命令行（netsh / nft）里。
// 中文服务名很常见（"我的网盘"），而它在某些平台的防火墙里
// 会变成一条既创建不了、也删不掉的幽灵规则。
func TestRuleNameIsSafe(t *testing.T) {
	t.Parallel()

	cases := []struct {
		label string
		want  string
	}{
		{"Jellyfin", "isc-jellyfin-tcp-8096"},
		{"my service", "isc-my-service-tcp-8096"},
		{"我的网盘", "isc-service-tcp-8096"},        // 全非 ASCII → 兜底名
		{"nas 备份", "isc-nas-tcp-8096"},          // 混合 → 丢掉非 ASCII
		{"a; rm -rf /", "isc-a-rm-rf-tcp-8096"}, // 命令分隔符被清掉，连续短横线被折叠
		{"", "isc-service-tcp-8096"},            // 空 → 兜底名
		{"...", "isc-service-tcp-8096"},         // 清完为空 → 兜底名
		{"a--b", "isc-a-b-tcp-8096"},            // 连续短横线折叠
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := ruleName(tc.label, platform.TCP, 8096)
			if got != tc.want {
				t.Errorf("ruleName(%q) = %q, 期望 %q", tc.label, got, tc.want)
			}
			// 任何情况下都不能含空格或命令分隔符。
			for _, bad := range []string{" ", ";", "&", "|", "$", "`", "\"", "'"} {
				if strings.Contains(got, bad) {
					t.Errorf("规则名含有危险字符 %q: %q", bad, got)
				}
			}
			// `--` 在命令行里是"选项结束"标记，而规则名会进
			// netsh / nft 的命令行，因此不能出现。
			if strings.Contains(got, "--") {
				t.Errorf("规则名含有连续短横线（命令行里的选项结束标记）: %q", got)
			}
		})
	}
}

func TestRuleNameIsTruncated(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 200)
	got := ruleName(long, platform.TCP, 80)
	if len(got) > 64 {
		t.Errorf("规则名过长（%d 字符），某些平台会拒绝: %s", len(got), got)
	}
}

func TestParseProtocol(t *testing.T) {
	t.Parallel()

	ok := map[string]platform.Protocol{
		"":      platform.TCP,
		"tcp":   platform.TCP,
		"TCP":   platform.TCP,
		" tcp ": platform.TCP,
		"udp":   platform.UDP,
		"UDP":   platform.UDP,
	}
	for in, want := range ok {
		got, err := parseProtocol(in)
		if err != nil {
			t.Errorf("parseProtocol(%q) 报错: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseProtocol(%q) = %s，期望 %s", in, got, want)
		}
	}

	for _, bad := range []string{"sctp", "icmp", "tcp6", "http"} {
		if _, err := parseProtocol(bad); err == nil {
			t.Errorf("parseProtocol(%q) 应当报错", bad)
		}
	}
}

func TestMetaDeclaresNoExternalServer(t *testing.T) {
	t.Parallel()

	p := NewIPv6Native(healthyMonitor(), &fakeFirewall{}, availableFirewallState())
	m := p.Meta()

	if m.Name != "ipv6-native" {
		t.Errorf("Name = %s", m.Name)
	}
	if m.NeedsExternalServer {
		t.Error("IPv6 直连不需要外部服务器 —— 这是它与中转方案最关键的区别")
	}
	if m.Description == "" {
		t.Error("必须说明工作原理，用户要据此在多种方式间做选择")
	}
}

// ---------------------------------------------------------------------------
// 端口探测
// ---------------------------------------------------------------------------

func TestPortListening(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// 起一个真实监听，再验证探测能发现它。
	//
	// 用真实监听而不是 mock：端口探测的全部价值就在于"它是否真的
	// 能观察到系统状态"，mock 掉这一层等于什么都没测。
	ln, port, err := netListenLocal()
	if err != nil {
		t.Fatalf("无法建立测试监听: %v", err)
	}
	defer ln.Close() //nolint:errcheck // 测试清理

	if !PortListening(ctx, port) {
		t.Errorf("端口 %d 上有监听，却报告没有", port)
	}

	// 关掉之后应当报告没有。
	_ = ln.Close()
	// 给内核一点时间回收。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && PortListening(ctx, port) {
		time.Sleep(20 * time.Millisecond)
	}
	if PortListening(ctx, port) {
		t.Errorf("端口 %d 已关闭，却仍报告有监听", port)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func mustAddr(s string) netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil {
		panic("测试数据里的地址不合法: " + s)
	}
	return a
}

func mustPrefix(s string) netip.Prefix {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		panic("测试数据里的前缀不合法: " + s)
	}
	return p
}

// netListenLocal 起一个监听回环随机端口的 TCP 服务，返回它与端口号。
func netListenLocal() (net.Listener, int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return nil, 0, errors.New("监听地址不是 TCP 地址")
	}
	return ln, addr.Port, nil
}
