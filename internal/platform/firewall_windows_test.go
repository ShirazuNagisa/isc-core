//go:build windows

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// 本文件覆盖 Windows 防火墙后端。
//
// 用假 runner 而不是真的读写系统防火墙，原因有三：
//
//  1. 写规则需要管理员权限，而测试不该要求提权；
//  2. 在开发者机器上真的创建防火墙规则是不可接受的副作用；
//  3. 真正容易出错的不是"调用 PowerShell"，而是**差异计算**、
//     **脚本生成**与**注入防御** —— 那些都能用假 runner 覆盖。
//
// 唯一一条触碰真实 PowerShell 的测试只做只读操作（Inspect）。

// fakeRunner 记录脚本并返回可编程的输出。
type fakeRunner struct {
	mu      sync.Mutex
	scripts []string
	output  string
	err     error
}

func (f *fakeRunner) Run(_ context.Context, script string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, script)
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.output), nil
}

func (f *fakeRunner) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.scripts) == 0 {
		return ""
	}
	return f.scripts[len(f.scripts)-1]
}

func (f *fakeRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.scripts)
}

func rulesJSON(rules ...fwRuleJSON) string {
	byt, _ := json.Marshal(map[string]any{"items": rules})
	return string(byt)
}

// ---------------------------------------------------------------------------
// 读取与解析
// ---------------------------------------------------------------------------

func TestInspectParsesRules(t *testing.T) {
	t.Parallel()

	f := &fakeRunner{output: rulesJSON(
		fwRuleJSON{
			Name: "isc-jellyfin-tcp-8096", DisplayName: "isc-jellyfin-tcp-8096",
			Enabled: "True", Action: "Allow", Direction: "Inbound", Profile: "Any",
			Protocol: "TCP", LocalPort: "8096",
		},
		fwRuleJSON{
			Name: "isc-nas-udp-9000-9100", DisplayName: "isc-nas-udp-9000-9100",
			Enabled: "True", Action: "Allow", Direction: "Inbound", Profile: "Private",
			Protocol: "UDP", LocalPort: "9000-9100",
		},
	)}
	w := &windowsFirewall{runner: f}

	rules, err := w.Inspect(context.Background())
	if err != nil {
		t.Fatalf("读取规则失败: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("应当解析出 2 条规则，得到 %d", len(rules))
	}

	if rules[0].Name != "isc-jellyfin-tcp-8096" {
		t.Errorf("规则名 = %q", rules[0].Name)
	}
	if rules[0].Protocol != TCP {
		t.Errorf("协议 = %s", rules[0].Protocol)
	}
	if rules[0].Port.From != 8096 || rules[0].Port.To != 8096 {
		t.Errorf("端口 = %+v", rules[0].Port)
	}
	// Profile=Any 时不该设置 Profiles —— 留空表示"全部配置文件"。
	if len(rules[0].Profiles) != 0 {
		t.Errorf("Profile=Any 不该设置 Profiles，得到 %v", rules[0].Profiles)
	}

	if rules[1].Port.From != 9000 || rules[1].Port.To != 9100 {
		t.Errorf("端口范围 = %+v", rules[1].Port)
	}
	if len(rules[1].Profiles) != 1 || rules[1].Profiles[0] != "private" {
		t.Errorf("Profiles = %v", rules[1].Profiles)
	}
}

// TestInspectSkipsUnrepresentableRules 验证形状不符的规则被跳过而不是报错。
//
// 报错会让 ISC 完全无法工作，而跳过最多是少显示一条 —— 后者是可以
// 接受的降级。这些规则可能来自用户的手工创建或其它工具。
func TestInspectSkipsUnrepresentableRules(t *testing.T) {
	t.Parallel()

	f := &fakeRunner{output: rulesJSON(
		// 协议为 Any：没有端口语义。
		fwRuleJSON{DisplayName: "isc-any-tcp-1", Protocol: "Any", LocalPort: "Any"},
		// 端口列表：PortRange 表达不了。
		fwRuleJSON{DisplayName: "isc-list-tcp-2", Protocol: "TCP", LocalPort: "80,443"},
		// 协议不支持。
		fwRuleJSON{DisplayName: "isc-icmp-tcp-3", Protocol: "ICMPv4", LocalPort: "1"},
		// 端口为空。
		fwRuleJSON{DisplayName: "isc-empty-tcp-4", Protocol: "TCP", LocalPort: ""},
		// 这是唯一一条合法的。
		fwRuleJSON{DisplayName: "isc-good-tcp-5", Protocol: "TCP", LocalPort: "8080"},
	)}
	w := &windowsFirewall{runner: f}

	rules, err := w.Inspect(context.Background())
	if err != nil {
		t.Fatalf("不该整体报错: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("应当只保留 1 条可表达的规则，得到 %d: %+v", len(rules), rules)
	}
	if rules[0].Name != "isc-good-tcp-5" {
		t.Errorf("保留的规则 = %q", rules[0].Name)
	}
}

func TestParsePortRange(t *testing.T) {
	t.Parallel()

	ok := map[string]PortRange{
		"80":        {80, 80},
		" 8080 ":    {8080, 8080},
		"9000-9100": {9000, 9100},
		"1-65535":   {1, 65535},
	}
	for in, want := range ok {
		got, valid := parsePortRange(in)
		if !valid {
			t.Errorf("parsePortRange(%q) 应当成功", in)
			continue
		}
		if got != want {
			t.Errorf("parsePortRange(%q) = %+v，期望 %+v", in, got, want)
		}
	}

	bad := []string{
		"", "Any", "any", "80,443", "9100-9000", "abc", "0x50", "80-",
		"-80", "70000", "80,", "1-2-3",
	}
	for _, in := range bad {
		if _, valid := parsePortRange(in); valid {
			t.Errorf("parsePortRange(%q) 应当失败", in)
		}
	}
}

func TestInspectReportsRunnerError(t *testing.T) {
	t.Parallel()

	f := &fakeRunner{err: errors.New("访问被拒绝")}
	w := &windowsFirewall{runner: f}

	_, err := w.Inspect(context.Background())
	if err == nil {
		t.Fatal("应当把执行错误传出来")
	}
	if !strings.Contains(err.Error(), "访问被拒绝") {
		t.Errorf("错误信息丢失了底层原因: %v", err)
	}
}

func TestInspectReportsMalformedJSON(t *testing.T) {
	t.Parallel()

	f := &fakeRunner{output: "这不是 JSON"}
	w := &windowsFirewall{runner: f}

	_, err := w.Inspect(context.Background())
	if err == nil {
		t.Fatal("畸形输出应当报错，而不是静默返回空列表")
	}
}

// ---------------------------------------------------------------------------
// 差异计算
// ---------------------------------------------------------------------------

func TestPlanCreatesMissingRule(t *testing.T) {
	t.Parallel()

	f := &fakeRunner{output: rulesJSON()} // 当前没有任何 ISC 规则
	w := &windowsFirewall{runner: f}

	ch, err := w.Plan(context.Background(), []Rule{{
		Name: "isc-jellyfin-tcp-8096", Protocol: TCP, Port: NewPort(8096),
	}})
	if err != nil {
		t.Fatalf("计算差异失败: %v", err)
	}

	if ch.Summary == "" {
		t.Error("变更摘要不能为空 —— 它是用户看到的唯一说明")
	}
	if !ch.Reversible {
		t.Error("新增规则是可撤销的")
	}
	// 差异必须是给人看的。
	if !strings.Contains(ch.Diff, "isc-jellyfin-tcp-8096") {
		t.Errorf("差异里没有规则名: %s", ch.Diff)
	}
	if !strings.Contains(ch.Diff, "+") {
		t.Errorf("差异应当标出这是新增: %s", ch.Diff)
	}

	var payload fwPayload
	if err := json.Unmarshal(ch.Payload, &payload); err != nil {
		t.Fatalf("payload 不是合法 JSON: %v", err)
	}
	if len(payload.Create) != 1 || payload.Create[0] != "isc-jellyfin-tcp-8096" {
		t.Errorf("payload.Create = %v", payload.Create)
	}
}

// TestPlanIsEmptyWhenRuleExists 验证已存在时不产生变更。
//
// 这一条决定了二次点击"开放端口"时的行为：应当是"无需改动"，
// 而不是又创建一条同名规则。
func TestPlanIsEmptyWhenRuleExists(t *testing.T) {
	t.Parallel()

	f := &fakeRunner{output: rulesJSON(fwRuleJSON{
		DisplayName: "isc-jellyfin-tcp-8096", Protocol: "TCP", LocalPort: "8096",
		Profile: "Any", Direction: "Inbound",
	})}
	w := &windowsFirewall{runner: f}

	ch, err := w.Plan(context.Background(), []Rule{{
		Name: "isc-jellyfin-tcp-8096", Protocol: TCP, Port: NewPort(8096),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Summary != "" || len(ch.Payload) != 0 {
		t.Errorf("规则已存在时不该产生变更: %+v", ch)
	}
}

// TestPlanDoesNotModifyExistingRules 验证同名的已有规则不被改动。
//
// 用户可能手工调整过它的作用域或配置文件，我们按自己的理解去改
// 会把他的调整抹掉。而"改错了别人的规则"比"少开一个端口"严重得多。
func TestPlanDoesNotModifyExistingRules(t *testing.T) {
	t.Parallel()

	f := &fakeRunner{output: rulesJSON(fwRuleJSON{
		DisplayName: "isc-jellyfin-tcp-8096", Protocol: "TCP", LocalPort: "8096",
		// 用户把它限制在专用网络 —— 与我们期望的 Any 不同。
		Profile: "Private", Direction: "Inbound",
	})}
	w := &windowsFirewall{runner: f}

	ch, err := w.Plan(context.Background(), []Rule{{
		Name: "isc-jellyfin-tcp-8096", Protocol: TCP, Port: NewPort(8096),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Payload) != 0 {
		t.Error("不该去改动用户已有的规则")
	}
}

// ---------------------------------------------------------------------------
// 脚本生成与注入防御
// ---------------------------------------------------------------------------

func TestCreateScriptGeneratesExpectedCommand(t *testing.T) {
	t.Parallel()

	script := createScript([]string{"isc-jellyfin-tcp-8096"})

	for _, want := range []string{
		"New-NetFirewallRule",
		"-DisplayName 'isc-jellyfin-tcp-8096'",
		"-Group 'ISC'",
		"-Direction Inbound",
		"-Action Allow",
		"-Protocol TCP",
		"-LocalPort 8096",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("生成的脚本缺少 %q:\n%s", want, script)
		}
	}
}

// TestScriptGenerationRejectsInjection 是本文件最重要的一条。
//
// 这段脚本会以**管理员身份**执行。一次注入就能拿到整台机器的控制权，
// 因此生成脚本时绝不能假设输入是可信的 —— 即便调用方已经校验过一遍。
func TestScriptGenerationRejectsInjection(t *testing.T) {
	t.Parallel()

	malicious := []string{
		`isc-x-tcp-80'; Remove-Item -Recurse -Force C:\ ; '`,
		"isc-x-tcp-80\nRemove-Item -Recurse -Force C:\\",
		`isc-x-tcp-80" ; shutdown /r ; "`,
		"isc-x-tcp-80$(Get-Process)",
		"isc-x-tcp-80`; whoami",
		"isc-x-tcp-80 && format c:",
		"isc-x-tcp-80|iex",
		"../isc-x-tcp-80",
		"isc-x-tcp-80; calc",
		// 端口部分被污染。
		"isc-x-tcp-80;calc",
		"isc-x-tcp-99999",
		"isc-x-tcp-0",
	}

	for _, name := range malicious {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, script := range []string{createScript([]string{name}), removeScript([]string{name})} {
				// 注入的特征：脚本里出现了我们**没写**的命令。
				//
				// 注意不能把 "\nRemove" 当成特征 —— 合法的删除脚本里
				// 本来就有换行后的 Remove-NetFirewallRule。
				// 这类"过宽的断言"会让测试在正确实现上失败，
				// 而修它的诱惑是去放宽实现。
				for _, forbidden := range []string{
					"-Recurse", "shutdown", "format ", "Invoke-Expression",
					"iex ", "calc", "whoami", "Get-Process", "&&", "||",
					";", "$(", "`",
				} {
					if strings.Contains(script, forbidden) {
						t.Errorf("生成的脚本含有注入内容 %q:\n%s", forbidden, script)
					}
				}
				// 除了我们自己写的那两行，不该有任何其它语句。
				for _, line := range strings.Split(script, "\n") {
					line = strings.TrimSpace(line)
					if line == "" || strings.HasPrefix(line, "$ErrorActionPreference") {
						continue
					}
					if !strings.HasPrefix(line, "New-NetFirewallRule") &&
						!strings.HasPrefix(line, "Remove-NetFirewallRule") {
						t.Errorf("脚本里出现了预期之外的行: %q", line)
					}
				}
			}
		})
	}
}

func TestValidRuleName(t *testing.T) {
	t.Parallel()

	good := []string{
		"isc-jellyfin-tcp-8096",
		"isc-my-service-udp-53",
		"isc-a-tcp-1",
		"isc-service-tcp-65535",
	}
	for _, n := range good {
		if !validRuleName(n) {
			t.Errorf("%q 应当是合法规则名", n)
		}
	}

	bad := []string{
		"", "jellyfin", "isc-jellyfin-sctp-8096", "isc-jellyfin-tcp-",
		"isc--tcp-8096", "isc-JELLYFIN-tcp-8096", "isc-jellyfin-tcp-abc",
		"isc-jellyfin tcp 8096", "isc-jellyfin-tcp-8096'",
		"ISC-jellyfin-tcp-8096", "isc-jellyfin-tcp-070",
	}
	for _, n := range bad {
		if validRuleName(n) {
			t.Errorf("%q 不该是合法规则名", n)
		}
	}
}

func TestParseRuleName(t *testing.T) {
	t.Parallel()

	proto, port, ok := parseRuleName("isc-jellyfin-tcp-8096")
	if !ok || proto != "tcp" || port != 8096 {
		t.Errorf("parseRuleName = (%q, %d, %v)", proto, port, ok)
	}

	if _, _, ok := parseRuleName("isc-x-tcp-0"); ok {
		t.Error("端口 0 应当被拒绝")
	}
	if _, _, ok := parseRuleName("garbage"); ok {
		t.Error("不合约定的名字应当被拒绝")
	}
}

func TestEscapePS(t *testing.T) {
	t.Parallel()

	if got := escapePS("it's"); got != "it''s" {
		t.Errorf("escapePS(\"it's\") = %q", got)
	}
	if got := escapePS("plain"); got != "plain" {
		t.Errorf("escapePS(\"plain\") = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 应用与回滚
// ---------------------------------------------------------------------------

func TestApplyAndRollbackUsePayload(t *testing.T) {
	t.Parallel()

	f := &fakeRunner{output: rulesJSON()}
	w := &windowsFirewall{runner: f}
	ctx := context.Background()

	ch, err := w.Plan(ctx, []Rule{{
		Name: "isc-jellyfin-tcp-8096", Protocol: TCP, Port: NewPort(8096),
	}})
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Apply(ctx, ch); err != nil {
		t.Fatalf("应用失败: %v", err)
	}
	if s := f.last(); !strings.Contains(s, "New-NetFirewallRule") {
		t.Errorf("应用时应当创建规则:\n%s", s)
	}

	if err := w.Rollback(ctx, ch); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if s := f.last(); !strings.Contains(s, "Remove-NetFirewallRule") {
		t.Errorf("回滚时应当删除规则:\n%s", s)
	}
	// 删除的必须是当初创建的那一条。
	if s := f.last(); !strings.Contains(s, "isc-jellyfin-tcp-8096") {
		t.Errorf("回滚目标不对:\n%s", s)
	}
}

// TestRollbackIsIdempotent 验证重复撤销不报错。
//
// 用户重试是常见操作（网络卡了、按钮点重了）。把重试当失败会让他
// 以为撤销没生效，进而做出更激进的处置。
func TestRollbackIsIdempotent(t *testing.T) {
	t.Parallel()

	f := &fakeRunner{output: rulesJSON()}
	w := &windowsFirewall{runner: f}
	ctx := context.Background()

	ch, _ := w.Plan(ctx, []Rule{{Name: "isc-x-tcp-80", Protocol: TCP, Port: NewPort(80)}})

	if err := w.Rollback(ctx, ch); err != nil {
		t.Fatalf("第一次回滚: %v", err)
	}
	if err := w.Rollback(ctx, ch); err != nil {
		t.Fatalf("第二次回滚应当成功（幂等）: %v", err)
	}

	// 删除脚本必须用 SilentlyContinue：规则不存在不是错误。
	if !strings.Contains(removeScript([]string{"isc-x-tcp-80"}), "SilentlyContinue") {
		t.Error("删除脚本应当忽略「规则不存在」")
	}
}

func TestApplyRejectsCorruptPayload(t *testing.T) {
	t.Parallel()

	f := &fakeRunner{}
	w := &windowsFirewall{runner: f}
	ctx := context.Background()

	bad := Change{Payload: json.RawMessage(`{not json`)}
	if err := w.Apply(ctx, bad); err == nil {
		t.Error("畸形的 payload 应当被拒绝")
	}
	if f.count() != 0 {
		t.Error("payload 解析失败时不该执行任何命令")
	}
}

func TestApplyWithEmptyPayloadIsNoop(t *testing.T) {
	t.Parallel()

	f := &fakeRunner{}
	w := &windowsFirewall{runner: f}

	if err := w.Apply(context.Background(), Change{}); err != nil {
		t.Fatalf("空变更不该报错: %v", err)
	}
	if f.count() != 0 {
		t.Error("空变更不该执行命令")
	}
}

func TestApplySurfacesPermissionError(t *testing.T) {
	t.Parallel()

	f := &fakeRunner{err: errors.New("拒绝访问")}
	w := &windowsFirewall{runner: f}
	ctx := context.Background()

	ch := Change{Payload: json.RawMessage(`{"create":["isc-x-tcp-80"]}`)}
	err := w.Apply(ctx, ch)
	if err == nil {
		t.Fatal("应当报错")
	}
	// 错误信息必须提示需要管理员权限 —— 那是用户唯一能采取的行动。
	if !strings.Contains(err.Error(), "管理员") {
		t.Errorf("错误信息应当提示需要管理员权限: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 编码
// ---------------------------------------------------------------------------

// TestEncodePowerShellProducesUTF16LE 验证 -EncodedCommand 的编码正确。
//
// 编码错了的症状是 PowerShell 报"无法解码命令"或执行出莫名其妙的
// 东西，而脚本里含中文（我们的规则描述就是中文）时尤其容易出错。
func TestEncodePowerShellProducesUTF16LE(t *testing.T) {
	t.Parallel()

	encoded := encodePowerShell("abc")
	// "abc" 的 UTF-16LE 是 61 00 62 00 63 00，base64 后是 YQBiAGMA
	if encoded != "YQBiAGMA" {
		t.Errorf("encodePowerShell(\"abc\") = %q，期望 YQBiAGMA", encoded)
	}

	// 中文也必须能正确编码（描述文字里有中文）。
	if got := encodePowerShell("由 ISC 管理"); got == "" {
		t.Error("中文脚本编码失败")
	}
}

func TestDescribeReportsAvailable(t *testing.T) {
	t.Parallel()

	state := newWindowsFirewall().Describe()
	if !state.Available {
		t.Error("Windows 防火墙后端应当报告为可用")
	}
	if state.Backend == "" {
		t.Error("必须报告后端名称")
	}
	// 说明里要提到权限要求 —— 用户遇到"创建失败"时才知道该怎么办。
	if !strings.Contains(state.Note, "管理员") {
		t.Errorf("说明里应当提到权限要求: %s", state.Note)
	}
}

// ---------------------------------------------------------------------------
// 真实 PowerShell（只读）
// ---------------------------------------------------------------------------

// TestInspectAgainstRealPowerShell 是唯一一条触碰真实系统的断言。
//
// 它只做**只读**操作，因此不需要管理员权限，也不会在开发机上留下任何
// 副作用。它验证的是假 runner 覆盖不到的一段：PowerShell 能否被正确
// 定位、-EncodedCommand 编码是否被接受、真实输出能否被解析。
//
// 不断言具体规则数量：机器上千差万别，而且用户可能本来就有 isc- 规则。
func TestInspectAgainstRealPowerShell(t *testing.T) {
	if testing.Short() {
		t.Skip("需要调用真实的 PowerShell")
	}

	w := newWindowsFirewall()
	rules, err := w.Inspect(context.Background())
	if err != nil {
		t.Fatalf("读取真实防火墙规则失败: %v\n"+
			"（这条路径不需要管理员权限；失败通常意味着 "+
			"powershell.exe 定位不到或 NetSecurity 模块不可用）", err)
	}

	// 读回来的每条规则都必须形状正确 —— 解析错的话会在差异计算里
	// 表现为"规则不存在"，于是重复创建一堆同名规则。
	for _, r := range rules {
		if !validRuleName(r.Name) {
			t.Errorf("读回了不符合命名约定的规则: %q", r.Name)
		}
		if r.Protocol != TCP && r.Protocol != UDP {
			t.Errorf("规则 %s 的协议异常: %s", r.Name, r.Protocol)
		}
		if r.Port.From == 0 {
			t.Errorf("规则 %s 的端口为 0", r.Name)
		}
	}

	t.Logf("真实环境中读到 %d 条 ISC 管理的规则", len(rules))
}
