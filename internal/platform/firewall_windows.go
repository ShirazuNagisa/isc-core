//go:build windows

package platform

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// 本文件是 Windows Defender 防火墙后端。
//
// # 为什么不用 netsh，而用 PowerShell
//
// `netsh advfirewall firewall show rule name=all` 的输出是**给人看的
// 本地化文本**：中文 Windows 上字段名是"规则名称""已启用""操作"，
// 英文系统上是 "Rule Name" / "Enabled" / "Action"。解析它意味着要对
// 每一种系统语言各写一套解析器，而漏掉一种语言的症状是"规则没读到、
// 于是重复创建了一堆"——这个坑不值得踩。
//
// PowerShell 的 NetSecurity 模块返回**结构化对象**，再经 ConvertTo-Json
// 变成稳定的 JSON。字段名与系统语言无关。
//
// 脚本通过 `-EncodedCommand` 传入（UTF-16LE + base64），因此不存在
// 任何命令行引号转义问题 —— 这是把参数拼进命令行最容易出错的地方。
//
// # 读不需要管理员，写需要
//
// `Get-NetFirewallRule` 普通用户即可执行，因此 Inspect / Plan / doctor
// 在没有提权时也能工作。`New-NetFirewallRule` 与 `Remove-NetFirewallRule`
// 需要管理员权限，失败时 PowerShell 会返回明确的错误，我们原样转达。

// scriptTimeout 是单次 PowerShell 调用的超时。
//
// 30 秒的依据：Windows PowerShell 启动本身要 200~500 毫秒，
// 而读写防火墙规则通常在一秒内完成。30 秒还没回来基本可以断定是卡住了
// （例如被组策略阻塞），那时应当失败而不是一直等。
const scriptTimeout = 30 * time.Second

// ruleGroup 是 ISC 创建的规则的分组名。
//
// 分组的作用是让用户在"高级安全 Windows Defender 防火墙"界面里
// 一眼看到 ISC 管理的全部规则，并成组启用/禁用。
const ruleGroup = "ISC"

// scriptRunner 执行一段 PowerShell 脚本并返回标准输出。
//
// 抽成接口是为了让本文件的**全部逻辑**都能在没有管理员权限、
// 甚至不在 Windows 上被测试。真实执行只占其中很小一部分，
// 而差异计算、规则名校验、错误翻译才是容易出错的地方。
type scriptRunner interface {
	Run(ctx context.Context, script string) ([]byte, error)
}

// windowsFirewall 是 Windows Defender 防火墙后端。
type windowsFirewall struct {
	runner scriptRunner
}

// newWindowsFirewall 构造后端。
func newWindowsFirewall() Firewall {
	return &windowsFirewall{runner: &powershellRunner{}}
}

// Describe 实现 describer。
func (w *windowsFirewall) Describe() ImplState {
	return ImplState{
		Available: true,
		Backend:   "Windows Defender 防火墙",
		Note: "通过 PowerShell 的 NetSecurity 模块读写规则；" +
			"读取无需提权，创建与删除规则需要管理员权限。" +
			"规则统一归入「" + ruleGroup + "」分组，便于在系统防火墙界面中识别",
	}
}

// ---------------------------------------------------------------------------
// PowerShell 执行
// ---------------------------------------------------------------------------

// powershellRunner 通过 -EncodedCommand 执行脚本。
type powershellRunner struct{}

// Run 实现 scriptRunner。
func (p *powershellRunner) Run(ctx context.Context, script string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, scriptTimeout)
	defer cancel()

	exe, err := powershellPath()
	if err != nil {
		return nil, err
	}

	// -NoProfile 避免加载用户的配置文件：那里面可能有任意代码，
	// 会改变我们的输出格式，也会拖慢每次调用。
	// -NonInteractive 避免脚本意外弹出交互提示而永久挂起。
	cmd := exec.CommandContext(ctx, exe,
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodePowerShell(script))

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("执行防火墙命令超时（%s）", scriptTimeout)
		}
		msg := unwrapPowerShellError(strings.TrimSpace(stderr.String()))
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return []byte(stdout.String()), nil
}

// unwrapPowerShellError 把 PowerShell 的错误流还原成人能读的文本。
//
// # 为什么需要它
//
// 当 stderr 被重定向（我们正是这么做的）时，Windows PowerShell 会把
// 错误写成 **CLIXML** —— 一段带命名空间的 XML，形如：
//
//	#< CLIXML
//	<Objs Version="1.1.0.1" xmlns="...">
//	  <S S="Error">New-NetFirewallRule : Access is denied. _x000D__x000A_</S>
//	  <S S="Error">At line:2 char:1_x000D__x000A_</S>
//	  <S S="Error">+ FullyQualifiedErrorId : ...</S>
//	</Objs>
//
// 把它原样交给用户，他看到的就是一整屏 XML，而真正有用的那句
// "Access is denied" 埋在中间。真机上实测到过这一幕。
//
// 这里把 <S S="Error"> 的内容抽出来、逐条还原，并丢掉 PowerShell 的
// 定位行（它们指向的是我们生成的脚本，对用户没有意义）。
func unwrapPowerShellError(raw string) string {
	if raw == "" {
		return ""
	}

	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "#< CLIXML"))

	if !strings.Contains(raw, "<Objs") {
		// 不是 CLIXML：可能是普通文本，也可能是 PowerShell 7 的输出。
		return raw
	}

	msgs := extractCLIXMLErrors(raw)
	if len(msgs) == 0 {
		// 解析不出内容时**不返回原始 XML** —— 一整屏 XML 比一句笼统的
		// 话更糟：它看起来像内核崩了，而实际问题可能只是缺权限。
		return "PowerShell 返回了无法解析的错误输出（可能与权限或执行策略有关）"
	}
	return strings.Join(msgs, " ")
}

// clixmlErrorRe 匹配 CLIXML 里的错误字符串节点。
//
// 用正则而不是完整的 XML 解析：这段输出的结构由 PowerShell 固定生成，
// 而它偶尔会把非 XML 的内容（例如原生命令的输出）混进来 ——
// 那种情况下严格解析会整个失败，正则还能把有用的部分捞出来。
var clixmlErrorRe = regexp.MustCompile(`(?s)<S S="Error">(.*?)</S>`)

// psEscapeRe 匹配 PowerShell 的 _xHHHH_ 控制字符转义。
var psEscapeRe = regexp.MustCompile(`_x([0-9A-Fa-f]{4})_`)

func extractCLIXMLErrors(raw string) []string {
	matches := clixmlErrorRe.FindAllStringSubmatch(raw, -1)

	// 按**位置**截断，而不是逐个过滤。
	//
	// PowerShell 的错误格式是固定的：
	//
	//	<真正的错误信息>
	//	At line:N char:M
	//	+ <出错的那一行>
	//	+ CategoryInfo ...
	//	+ FullyQualifiedErrorId ...
	//
	// 从 "At line:" 或 "+" 开始的全是定位信息。真机上发现：长行会被
	// PowerShell 折成多个 <S> 节点，逐节点过滤会把续行片段
	//（"tFirewallRule], CimException"）留下来 —— 那段文字毫无意义，
	// 却会让用户以为出了别的问题。
	var out []string
	seen := make(map[string]bool)
	for _, m := range matches {
		text := strings.TrimSpace(decodePowerShellText(m[1]))
		if text == "" {
			continue
		}
		// 遇到定位信息的开头就停下：后面全是噪声。
		if isPowerShellNoise(text) {
			break
		}
		if seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
	}
	return out
}

// decodePowerShellText 还原 CLIXML 里的文本。
func decodePowerShellText(s string) string {
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&apos;", "'")
	s = strings.ReplaceAll(s, "&amp;", "&")

	s = psEscapeRe.ReplaceAllStringFunc(s, func(match string) string {
		hex := match[2 : len(match)-1]
		v, err := strconv.ParseUint(hex, 16, 32)
		if err != nil || v > 0x10FFFF {
			return match
		}
		return string(rune(v))
	})

	// 折叠空白：CLIXML 里的换行会变成一堆空行。
	return strings.Join(strings.Fields(s), " ")
}

// isPowerShellNoise 报告这一段是否是定位信息的开头。
//
// 它同时是"从这里开始全是噪声"的判据，因此只匹配开头 ——
// 中间出现这些词是正常的（例如错误信息里提到了 CategoryInfo）。
func isPowerShellNoise(s string) bool {
	for _, prefix := range []string{
		"At line:", "+", "CategoryInfo", "FullyQualifiedErrorId",
	} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// powershellPath 定位 Windows PowerShell。
//
// 用 System32 下的绝对路径而不是依赖 PATH：内核以服务方式运行时
// PATH 可能与交互式登录时不同，而"找不到 powershell"会让防火墙功能
// 静默失效 —— 那个症状极难排查。
func powershellPath() (string, error) {
	if p, err := exec.LookPath("powershell.exe"); err == nil {
		return p, nil
	}
	// %SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe
	//
	// Windows 的 System32 在 32 位进程里会被重定向到 SysWOW64，
	// 而 SysWOW64 下同样有 powershell.exe，因此这个路径在两种位数下
	// 都成立。
	if root := os.Getenv("SystemRoot"); root != "" {
		p := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("找不到 powershell.exe，无法管理防火墙")
}

// encodePowerShell 把脚本编码成 -EncodedCommand 需要的形式。
//
// PowerShell 要求 UTF-16LE 的 base64。用这种方式传脚本而不是拼命令行，
// 意味着脚本里出现引号、换行、中文都不需要任何转义 —— 而拼命令行时
// 那些正是最容易出错的地方。
func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(buf[i*2:], u)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// ---------------------------------------------------------------------------
// 读取
// ---------------------------------------------------------------------------

// fwRuleJSON 是 PowerShell 返回的一条规则。
type fwRuleJSON struct {
	Name        string `json:"Name"`
	DisplayName string `json:"DisplayName"`
	Enabled     string `json:"Enabled"`
	Action      string `json:"Action"`
	Direction   string `json:"Direction"`
	Profile     string `json:"Profile"`
	Protocol    string `json:"Protocol"`
	LocalPort   string `json:"LocalPort"`
	RemoteAddr  string `json:"RemoteAddr"`
}

// Inspect 实现 Firewall。
//
// 它列出**由 ISC 管理**的入站规则，而不是系统里的全部规则：
// 用户机器上通常有几百条别人的规则（真机实测 566 条），
// 把它们全部读回来既慢又毫无用处 —— 我们只关心自己创建的那些。
func (w *windowsFirewall) Inspect(ctx context.Context) ([]Rule, error) {
	raw, err := w.runner.Run(ctx, inspectScript)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Items []fwRuleJSON `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("platform: 解析防火墙规则失败: %w", err)
	}

	out := make([]Rule, 0, len(payload.Items))
	for _, item := range payload.Items {
		rule, ok := convertRule(item)
		if !ok {
			// 转换不了的规则跳过而不是报错：它可能是用户手工创建的、
			// 形状与我们的约定不符的规则。为它让整个读取失败会让
			// ISC 完全无法工作，而跳过它最多是少显示一条。
			continue
		}
		out = append(out, rule)
	}
	return out, nil
}

// convertRule 把 PowerShell 的规则转成我们的表示。
func convertRule(item fwRuleJSON) (Rule, bool) {
	proto := Protocol(strings.ToLower(item.Protocol))
	if proto == "any" {
		// 协议为 Any 时端口过滤没有意义，而我们的 Rule 要求一个端口范围。
		// 跳过它：ISC 创建的规则永远是明确的协议 + 端口。
		return Rule{}, false
	}
	if proto != TCP && proto != UDP {
		return Rule{}, false
	}

	pr, ok := parsePortRange(item.LocalPort)
	if !ok {
		return Rule{}, false
	}

	rule := Rule{
		Name:        item.DisplayName,
		Protocol:    proto,
		Port:        pr,
		Description: "",
	}
	// Profile 为 Any 时留空表示"全部配置文件"，与约定一致。
	if p := strings.ToLower(item.Profile); p != "" && p != "any" {
		rule.Profiles = []string{p}
	}
	return rule, true
}

// parsePortRange 解析 Windows 返回的端口表示。
//
// 它可能是 "80"、"8000-8100"、"Any"，也可能是逗号分隔的多个值。
// 只接受前两种：逗号分隔的多个端口用 PortRange 表达不了，
// 硬塞进去会让差异计算错乱。
func parsePortRange(s string) (PortRange, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "Any") {
		return PortRange{}, false
	}
	if strings.ContainsAny(s, ",") {
		return PortRange{}, false
	}

	if from, to, found := strings.Cut(s, "-"); found {
		f, err1 := strconv.ParseUint(strings.TrimSpace(from), 10, 16)
		t, err2 := strconv.ParseUint(strings.TrimSpace(to), 10, 16)
		if err1 != nil || err2 != nil || f > t {
			return PortRange{}, false
		}
		return PortRange{From: uint16(f), To: uint16(t)}, true
	}

	p, err := strconv.ParseUint(s, 10, 16)
	if err != nil {
		return PortRange{}, false
	}
	return NewPort(uint16(p)), true
}

// ---------------------------------------------------------------------------
// 差异计算
// ---------------------------------------------------------------------------

// fwPayload 是随变更一起保存、供回滚使用的数据。
//
// 它被序列化进 change.Record.Payload，因此**跨进程撤销**才成立 ——
// 内核重启后仍然知道当初创建了哪几条规则。
type fwPayload struct {
	// Create 是本次要创建的规则名。
	Create []string `json:"create,omitempty"`
	// Delete 是本次要删除的规则名；回滚时要把它们恢复回来。
	//
	// 当前恒为空：ISC 只创建规则、不删除别人的规则。留着它是为了让
	// 将来加入"撤销时恢复被覆盖的同名规则"时不必改 payload 格式。
	Delete []string `json:"delete,omitempty"`
}

// Plan 实现 Firewall。
func (w *windowsFirewall) Plan(ctx context.Context, desired []Rule) (Change, error) {
	current, err := w.Inspect(ctx)
	if err != nil {
		return Change{}, err
	}

	have := make(map[string]Rule, len(current))
	for _, r := range current {
		have[r.Name] = r
	}

	var (
		toCreate []Rule
		diff     strings.Builder
	)
	for _, want := range desired {
		if got, exists := have[want.Name]; exists {
			// 同名规则已存在。**不比对细节、直接视为满足**。
			//
			// 理由是"改动已有规则"的风险远大于"新建一条"：用户可能
			// 手工调整过它的作用域或配置文件，我们按自己的理解去改
			// 会把他的调整抹掉。因此只报告"已存在"，让它保持原样。
			_ = got
			fmt.Fprintf(&diff, "  = 已存在  %s（%s %s）\n",
				want.Name, want.Protocol, want.Port)
			continue
		}
		toCreate = append(toCreate, want)
		fmt.Fprintf(&diff, "  + 新增    %s（入站 %s %s，来源任意）\n",
			want.Name, want.Protocol, want.Port)
	}

	if len(toCreate) == 0 {
		// 无差异：返回零值 Change，调用方据此生成空计划。
		return Change{}, nil
	}

	payload, err := json.Marshal(fwPayload{Create: ruleNames(toCreate)})
	if err != nil {
		return Change{}, fmt.Errorf("platform: 序列化防火墙变更失败: %w", err)
	}

	return Change{
		ID:       newChangeID("firewall"),
		Platform: "windows",
		Backend:  "windows-defender-firewall",
		Kind:     "firewall.rules",
		Summary:  fmt.Sprintf("新增 %d 条入站规则", len(toCreate)),
		Diff:     strings.TrimRight(diff.String(), "\n"),
		// 存的是**规则名**而不是整个 Rule：回滚只需要知道删哪几条，
		// 而规则名是由端口与协议确定的，不依赖任何运行期状态。
		Payload:    payload,
		Reversible: true,
	}, nil
}

// Apply 实现 Firewall。
func (w *windowsFirewall) Apply(ctx context.Context, ch Change) error {
	// 没有 payload 表示"无需改动"，不是错误。
	//
	// 把 nil 交给 json.Unmarshal 会得到 "unexpected end of JSON input"，
	// 而那个错误会一路冒到用户面前，说一个本来完全正常的空计划"解析失败"。
	if len(ch.Payload) == 0 {
		return nil
	}
	var payload fwPayload
	if err := json.Unmarshal(ch.Payload, &payload); err != nil {
		return fmt.Errorf("platform: 解析防火墙变更数据失败: %w", err)
	}
	if len(payload.Create) == 0 {
		return nil
	}

	if _, err := w.runner.Run(ctx, createScript(payload.Create)); err != nil {
		return fmt.Errorf("platform: 创建防火墙规则失败（需要以管理员身份运行）: %w", err)
	}
	return nil
}

// Rollback 实现 Firewall。
//
// 幂等：规则已经不存在时删除脚本不会报错。用户重试撤销是常见操作
// （网络卡了、按钮点重了），把重试当失败会让他以为撤销没生效。
func (w *windowsFirewall) Rollback(ctx context.Context, ch Change) error {
	if len(ch.Payload) == 0 {
		return nil
	}
	var payload fwPayload
	if err := json.Unmarshal(ch.Payload, &payload); err != nil {
		return fmt.Errorf("platform: 解析防火墙变更数据失败: %w", err)
	}
	if len(payload.Create) == 0 {
		return nil
	}

	if _, err := w.runner.Run(ctx, removeScript(payload.Create)); err != nil {
		return fmt.Errorf("platform: 撤销防火墙规则失败（需要以管理员身份运行）: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 脚本生成
// ---------------------------------------------------------------------------

const inspectScript = `
$ErrorActionPreference = 'SilentlyContinue'
$rules = @(Get-NetFirewallRule -Direction Inbound |
    Where-Object { $_.DisplayName -like 'isc-*' })
$items = foreach ($r in $rules) {
    $pf = $r | Get-NetFirewallPortFilter
    $af = $r | Get-NetFirewallAddressFilter
    [pscustomobject]@{
        Name        = [string]$r.Name
        DisplayName = [string]$r.DisplayName
        Enabled     = [string]$r.Enabled
        Action      = [string]$r.Action
        Direction   = [string]$r.Direction
        Profile     = [string]$r.Profile
        Protocol    = [string]$pf.Protocol
        LocalPort   = [string]$pf.LocalPort
        RemoteAddr  = [string]($af.RemoteAddress -join ',')
    }
}
ConvertTo-Json -InputObject @{ items = @($items) } -Depth 4 -Compress
`

// createScript 生成创建规则的脚本。
func createScript(names []string) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	for _, name := range names {
		proto, port, ok := parseRuleName(name)
		if !ok {
			// 名字不合约定：跳过而不是把原样拼进脚本。
			//
			// 这一层校验是**冗余的**（调用方已经校验过一遍），
			// 但拼 PowerShell 脚本这件事必须假设输入是不可信的：
			// 一次注入就能以管理员身份执行任意命令。
			continue
		}
		fmt.Fprintf(&b,
			"New-NetFirewallRule -DisplayName '%s' -Group '%s' -Direction Inbound"+
				" -Action Allow -Protocol %s -LocalPort %d -Profile Any"+
				" -Description '%s' | Out-Null\n",
			escapePS(name), escapePS(ruleGroup), strings.ToUpper(proto), port,
			escapePS("由 ISC 管理 —— 可在 ISC 中一键撤销"))
	}
	return b.String()
}

// removeScript 生成删除规则的脚本。
func removeScript(names []string) string {
	var b strings.Builder
	// 用 SilentlyContinue：规则不存在不是错误（幂等）。
	b.WriteString("$ErrorActionPreference = 'SilentlyContinue'\n")
	for _, name := range names {
		if !validRuleName(name) {
			continue
		}
		fmt.Fprintf(&b, "Remove-NetFirewallRule -DisplayName '%s'\n", escapePS(name))
	}
	return b.String()
}

// ruleNamePattern 是 ISC 规则名的严格形式。
//
// 与 reach 包生成的名字保持一致：isc-<服务名>-<协议>-<端口>。
// 服务名部分只允许小写字母、数字与短横线 —— 这一条同时挡住了
// 引号、分号、换行等一切能在 PowerShell 里起作用的字符。
var ruleNamePattern = regexp.MustCompile(`^isc-[a-z0-9-]+-(tcp|udp)-([1-9][0-9]{0,4})$`)

// validRuleName 报告规则名是否完全合我们的约定。
//
// 它直接复用 parseRuleName 而不是单独跑一遍正则：两处判断必须**完全
// 一致**，否则会出现"能删不能建"或"能建不能删"的名字 —— 前者会在
// 撤销时删掉一条并不存在的规则（无害但脏），后者会在撤销时静默跳过
// 一条真实存在的规则（留下垃圾规则，用户得手动清理）。
//
// 曾经 validRuleName 只跑正则，于是 isc-x-tcp-99999（端口超出 uint16）
// 被判为合法，而 parseRuleName 拒绝它。
func validRuleName(name string) bool {
	_, _, ok := parseRuleName(name)
	return ok
}

// parseRuleName 从规则名里解出协议与端口。
//
// 正则里有**两个**捕获组，因此 FindStringSubmatch 返回的长度是 3：
//
//	m[0] 整个匹配   m[1] 协议   m[2] 端口
//
// 曾经这里写成了 m[2] / m[3] —— 于是任何一条真实的规则名都会让
// 索引越界 panic，而调用它的 createScript 是"开放端口"的必经之路。
// 那条路径在写测试之前从未被跑到过。
func parseRuleName(name string) (proto string, port uint16, ok bool) {
	const (
		idxProto = 1
		idxPort  = 2
	)

	m := ruleNamePattern.FindStringSubmatch(name)
	if len(m) <= idxPort {
		return "", 0, false
	}

	p, err := strconv.ParseUint(m[idxPort], 10, 16)
	if err != nil || p == 0 {
		return "", 0, false
	}
	return m[idxProto], uint16(p), true
}

// escapePS 对单引号字符串做转义。
//
// PowerShell 的单引号字符串里，转义方式是把 `'` 写成 `”`。
// 由于 validRuleName 已经把所有非 [a-z0-9-] 字符挡在外面，
// 这里实际上永远不会改到东西 —— 但它是第二道防线，
// 而"以为上游校验过所以这里不用管"正是注入漏洞的经典成因。
func escapePS(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
