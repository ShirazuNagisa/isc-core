//go:build windows

package platform

import (
	"os"
	"strings"
	"testing"
)

// 本文件覆盖四个**把外部数据变成决策**的函数。
//
// # 为什么单独一组
//
// firewall_windows.go 里的端口解析、编码解码已经有测试（firewall_windows_test.go），
// 而下面这四个此前一条都没有。它们的共同特征是：**输入来自外部命令的输出**，
// 输出则直接决定"这条规则算不算 ISC 的规则"或"给用户看什么错误"。
//
// 这类函数的缺陷形状很特别 —— 解析错了不会报错，只会**静默地产生错误的
// 结论**：把别人的防火墙规则当成自己的删掉，或者把一句噪声当成错误原因
// 展示给用户。

// ---------------------------------------------------------------------------
// convertRule
// ---------------------------------------------------------------------------

// TestConvertRuleAcceptsOnlyExplicitTCPUDP 钉住筛选口径。
//
// 探测阶段会把**系统里所有**防火墙规则都读回来，而 ISC 只应认领自己
// 能表达的那些：明确的协议 + 明确的端口。
//
// 任何"协议是 Any / 端口是 Any / 端口是个列表"的规则都必须被跳过 ——
// 否则回滚时会把别的软件的规则删掉，而那是一个**破坏性**的后果。
func TestConvertRuleAcceptsOnlyExplicitTCPUDP(t *testing.T) {
	t.Parallel()

	skip := []struct {
		name string
		in   fwRuleJSON
	}{
		{"协议 Any", fwRuleJSON{Protocol: "Any", LocalPort: "443"}},
		{"协议 ICMPv4", fwRuleJSON{Protocol: "ICMPv4", LocalPort: "443"}},
		{"协议为空", fwRuleJSON{Protocol: "", LocalPort: "443"}},
		{"端口 Any", fwRuleJSON{Protocol: "TCP", LocalPort: "Any"}},
		{"端口为空", fwRuleJSON{Protocol: "TCP", LocalPort: ""}},
		{"端口是列表", fwRuleJSON{Protocol: "TCP", LocalPort: "80,443"}},
		{"端口非法", fwRuleJSON{Protocol: "TCP", LocalPort: "abc"}},
		{"端口超范围", fwRuleJSON{Protocol: "TCP", LocalPort: "70000"}},
		{"范围颠倒", fwRuleJSON{Protocol: "TCP", LocalPort: "900-100"}},
	}
	for _, tc := range skip {
		if _, ok := convertRule(tc.in); ok {
			t.Errorf("%s 的规则不该被认领（会被误删）", tc.name)
		}
	}

	accept := []struct {
		name string
		in   fwRuleJSON
		port PortRange
	}{
		{"TCP 单端口", fwRuleJSON{Protocol: "TCP", LocalPort: "443"}, PortRange{From: 443, To: 443}},
		{"UDP 单端口", fwRuleJSON{Protocol: "UDP", LocalPort: "53"}, PortRange{From: 53, To: 53}},
		{"小写协议", fwRuleJSON{Protocol: "tcp", LocalPort: "8080"}, PortRange{From: 8080, To: 8080}},
		{"范围", fwRuleJSON{Protocol: "TCP", LocalPort: "8000-8100"}, PortRange{From: 8000, To: 8100}},
	}
	for _, tc := range accept {
		got, ok := convertRule(tc.in)
		if !ok {
			t.Errorf("%s 应当被认领", tc.name)
			continue
		}
		if got.Protocol != Protocol(strings.ToLower(tc.in.Protocol)) {
			t.Errorf("%s: 协议是 %q", tc.name, got.Protocol)
		}
		if got.Port != tc.port {
			t.Errorf("%s: 端口是 %+v，期望 %+v", tc.name, got.Port, tc.port)
		}
	}
}

// TestConvertRuleNormalizesProfile 钉住 Profile 的归一化。
//
// "Any" 表示"所有配置文件"，而空的 Profiles 切片正是本项目的表达方式。
// 若把 "Any" 原样塞进去，规则匹配时会因为找不到名为 any 的配置文件而失配。
func TestConvertRuleNormalizesProfile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		profile string
		want    []string
	}{
		{"Any", nil},
		{"", nil},
		{"Domain", []string{"domain"}},
		{"Private", []string{"private"}},
		{"Public", []string{"public"}},
	}
	for _, tc := range cases {
		got, ok := convertRule(fwRuleJSON{Protocol: "TCP", LocalPort: "443", Profile: tc.profile})
		if !ok {
			t.Fatalf("Profile=%q 应当被认领", tc.profile)
		}
		if len(got.Profiles) != len(tc.want) {
			t.Errorf("Profile=%q → %v，期望 %v", tc.profile, got.Profiles, tc.want)
			continue
		}
		for i := range tc.want {
			if got.Profiles[i] != tc.want[i] {
				t.Errorf("Profile=%q → %v，期望 %v", tc.profile, got.Profiles, tc.want)
			}
		}
	}
}

// TestConvertRuleUsesDisplayName 钉住名字取自 DisplayName。
//
// 规则名是本项目用来识别"这条规则是不是 ISC 建的"的依据（见 parseRuleName）。
// 取错字段会让回滚找不到自己的规则。
func TestConvertRuleUsesDisplayName(t *testing.T) {
	t.Parallel()

	got, ok := convertRule(fwRuleJSON{
		Name:        "internal-id",
		DisplayName: "isc-home-tcp-443",
		Protocol:    "TCP",
		LocalPort:   "443",
	})
	if !ok {
		t.Fatal("应当被认领")
	}
	if got.Name != "isc-home-tcp-443" {
		t.Errorf("名字是 %q，期望取自 DisplayName", got.Name)
	}
}

// ---------------------------------------------------------------------------
// isPowerShellNoise
// ---------------------------------------------------------------------------

// TestIsPowerShellNoise 覆盖四个前缀。
func TestIsPowerShellNoise(t *testing.T) {
	t.Parallel()

	noise := []string{
		"At line:1 char:1",
		"+ New-NetFirewallRule ...",
		"CategoryInfo          : PermissionDenied",
		"FullyQualifiedErrorId : AccessDenied",
	}
	for _, s := range noise {
		if !isPowerShellNoise(s) {
			t.Errorf("%q 应当被判定为定位信息", s)
		}
	}

	// 真正的错误信息不该被误判 —— 否则用户看不到原因，
	// 只能看到一句"操作失败"。
	real := []string{
		"Access is denied.",
		"New-NetFirewallRule : 无法创建规则",
		"需要管理员权限",
		"The parameter is incorrect.",
		"",
	}
	for _, s := range real {
		if isPowerShellNoise(s) {
			t.Errorf("%q 是错误信息，不该被判为噪声", s)
		}
	}
}

// ---------------------------------------------------------------------------
// extractCLIXMLErrors
// ---------------------------------------------------------------------------

// TestExtractCLIXMLErrorsStopsAtLocationInfo 是**最重要**的一条。
//
// PowerShell 的错误格式是固定的：真正的信息在前，随后是定位信息
// （At line / + / CategoryInfo / FullyQualifiedErrorId），全部是噪声。
//
// 而真机上发现过：**长行会被 PowerShell 折成多个 `<S>` 节点**，于是
// "逐个过滤噪声"会把续行片段（`tFirewallRule], CimException`）留下来 ——
// 那段文字毫无意义，却会让用户以为出了别的问题。实现因此改成"按位置
// 截断"：遇到第一条定位信息就停下。
//
// 这条测试就是钉住"按位置截断"这个行为 —— 用折行的真实形态。
func TestExtractCLIXMLErrorsStopsAtLocationInfo(t *testing.T) {
	t.Parallel()

	raw := `<Objs Version="1.1.0.1"><S S="Error">New-NetFirewallRule : 拒绝访问。</S>` +
		`<S S="Error">At line:1 char:1</S>` +
		`<S S="Error">+ New-NetFirewallRule -DisplayName "isc-x" -Direction Inbound -</S>` +
		`<S S="Error">tFirewallRule], CimException</S>` +
		`<S S="Error">+ CategoryInfo : PermissionDenied: (MSFT_NetFirewallR</S>` +
		`<S S="Error">ule:root/standardcimv2/MSFT_NetFirewallRule) [New-Ne</S>` +
		`<S S="Error">tFirewallRule], CimException</S>` +
		`<S S="Error">+ FullyQualifiedErrorId : AccessDenied</S></Objs>`

	got := extractCLIXMLErrors(raw)
	if len(got) != 1 {
		t.Fatalf("应当只抽出一条真正的错误，得到 %d 条: %q", len(got), got)
	}
	if !strings.Contains(got[0], "拒绝访问") {
		t.Errorf("抽出的错误是 %q，应当含真正的信息", got[0])
	}
	// 折行的续行片段绝不能出现在结果里 —— 那正是这个实现要挡的东西。
	for _, junk := range []string{"CategoryInfo", "FullyQualifiedErrorId", "At line:", "CimException"} {
		for _, line := range got {
			if strings.Contains(line, junk) {
				t.Errorf("结果里含定位信息 %q: %q", junk, line)
			}
		}
	}
}

// TestExtractCLIXMLErrorsDeduplicates 验证去重。
//
// PowerShell 常把同一条错误在多个节点里重复一次。
func TestExtractCLIXMLErrorsDeduplicates(t *testing.T) {
	t.Parallel()

	raw := `<Objs><S S="Error">同一个错误</S><S S="Error">同一个错误</S></Objs>`
	got := extractCLIXMLErrors(raw)
	if len(got) != 1 {
		t.Errorf("重复的错误应当被合并，得到 %q", got)
	}
}

// TestExtractCLIXMLErrorsOnGarbage 验证非 CLIXML 输入不会 panic。
//
// 输出为空、或者根本不是 XML 都是常见情况（命令没跑起来、被别的东西
// 抢先写了 stderr），而那时不该崩，只该返回空。
func TestExtractCLIXMLErrorsOnGarbage(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"",
		"not xml at all",
		"<Objs></Objs>",
		"<S S=\"Error\"></S>",
		"<Objs><S S=\"Error\">   </S></Objs>",
	} {
		if got := extractCLIXMLErrors(raw); len(got) != 0 {
			t.Errorf("输入 %q 应当抽出 0 条，得到 %q", raw, got)
		}
	}
}

// TestExtractCLIXMLErrorsDecodesEntities 验证实体与转义被还原。
func TestExtractCLIXMLErrorsDecodesEntities(t *testing.T) {
	t.Parallel()

	raw := `<Objs><S S="Error">路径 &quot;C:\Program Files&quot; 不可写 &amp; 已存在</S></Objs>`
	got := extractCLIXMLErrors(raw)
	if len(got) != 1 {
		t.Fatalf("应当抽出 1 条，得到 %q", got)
	}
	if !strings.Contains(got[0], `"C:\Program Files"`) {
		t.Errorf("实体没有被解码: %q", got[0])
	}
	if !strings.Contains(got[0], "&") {
		t.Errorf("&amp; 没有被解码: %q", got[0])
	}
}

// ---------------------------------------------------------------------------
// powershellPath
// ---------------------------------------------------------------------------

// TestPowershellPathResolvesOnWindows 验证本机能找到 PowerShell。
//
// 找不到它意味着防火墙功能**静默失效** —— 而实现里的注释指出那个症状
// 极难排查（内核以服务方式运行时 PATH 与交互式登录不同）。
// 因此这里断言：要么找到，要么给出一句说清原因的错。
func TestPowershellPathResolvesOnWindows(t *testing.T) {
	t.Parallel()

	p, err := powershellPath()
	if err != nil {
		// Windows 上找不到 PowerShell 是异常环境，但不该 panic，
		// 也不该返回一个空路径配一个 nil 错误。
		if p != "" {
			t.Errorf("出错时不该返回路径 %q", p)
		}
		if !strings.Contains(err.Error(), "powershell.exe") {
			t.Errorf("错误信息应当点明 powershell.exe，得到: %v", err)
		}
		t.Skipf("本机找不到 powershell.exe: %v", err)
	}

	if !strings.HasSuffix(strings.ToLower(p), "powershell.exe") {
		t.Errorf("解析出的路径是 %q", p)
	}
	fi, serr := os.Stat(p)
	if serr != nil {
		t.Errorf("解析出的路径不存在: %v", serr)
	} else if fi.IsDir() {
		t.Errorf("解析出的是一个目录: %q", p)
	}
}
