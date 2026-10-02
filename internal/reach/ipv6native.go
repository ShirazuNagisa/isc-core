package reach

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/change"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// 本文件实现"IPv6 原生"这种可达方式。
//
// 它是本产品的默认路径：国内家宽普遍有 IPv6 委派前缀，因此不需要公网
// IPv4、不需要中转服务器、流量不经过任何第三方。代价是这条路上有若干
// 不可控的失败点，而且它们**看起来一模一样** —— 用户在每一层看到的
// 都是"手机打不开"。
//
// 因此这个文件的主要价值不在"开放端口"（那只是加一条防火墙规则），
// 而在 Probe：把这条链路逐层拆开，指出到底断在哪一环。

// IPProvider 提供当前网卡地址快照。
//
// 与 ddns 包用的是同一份数据源，抽成窄接口是为了让本包的测试
// 不必去碰真实网卡。
type IPProvider interface {
	Snapshot(ctx context.Context) ([]platform.InterfaceAddrs, error)
}

// IPv6Native 是 IPv6 直连的可达方式。
type IPv6Native struct {
	monitor  IPProvider
	firewall platform.Firewall

	// firewallState 是防火墙后端的就绪状态（含"未实现"的情形）。
	firewallState platform.ImplState

	// lowPort 用于判断本机能否绑定特权端口。
	//
	// 允许为 nil：那就跳过这一项检测。做成可选而不是构造参数，
	// 是因为它与防火墙无关 —— 未来接别的可达方式时不必都带上它。
	lowPort platform.LowPortBinder
}

// SetLowPortBinder 设置低端口权限检测器。
//
// 单独一步而不是构造参数：它只影响**诊断**，不影响计划本身能否生成。
func (p *IPv6Native) SetLowPortBinder(b platform.LowPortBinder) {
	p.lowPort = b
}

// NewIPv6Native 构造 IPv6 原生插件。
//
// firewall 允许为 nil：平台后端未实现时（见 PLAN 的 R2），
// Probe 会如实报告"防火墙后端不可用"，而不是假装端口已经开放。
func NewIPv6Native(monitor IPProvider, firewall platform.Firewall, state platform.ImplState) *IPv6Native {
	return &IPv6Native{monitor: monitor, firewall: firewall, firewallState: state}
}

// Meta 实现 Provider。
func (p *IPv6Native) Meta() Meta {
	return Meta{
		Name:        "ipv6-native",
		DisplayName: "IPv6 直连",
		Description: "直接用本机的公网 IPv6 地址对外提供服务。" +
			"不需要公网 IPv4、不需要中转服务器、流量不经过任何第三方。" +
			"需要运营商下发了 IPv6 前缀，且路由器放行了入站连接。",
		NeedsExternalServer: false,
		Tier:                1,
	}
}

// ---------------------------------------------------------------------------
// 探测
// ---------------------------------------------------------------------------

// Probe 实现 Provider。
//
// 检测顺序刻意是"从下到上"：本机地址 → 委派前缀 → 防火墙后端 →
// 服务监听 → 上游可达性。用户按这个顺序逐项修复即可，
// 而颠倒顺序会让他去修一个当前根本用不上的环节。
func (p *IPv6Native) Probe(ctx context.Context) (Readiness, error) {
	var checks []Check

	// --- 1. 全局 IPv6 地址 ---
	addrs, globalCount, prefixes := p.inspectAddresses(ctx)
	checks = append(checks, addressCheck(addrs, globalCount))
	checks = append(checks, prefixCheck(addrs, prefixes))

	// --- 2. 防火墙后端 ---
	checks = append(checks, p.firewallCheck())

	// --- 3. 上游可达性 ---
	//
	// **这一项永远是 unknown**，因为本机无法自测。
	//
	// 从本机访问自己的公网地址通常会走回环（NAT 发夹），
	// 因此无论运营商是否放行都会"成功"。一个在本机自测通过的端口
	// 完全可能被上游封着 —— 而让用户以为"已经通了"比不检查更糟。
	checks = append(checks, Check{
		Name:   "上游可达性",
		Scope:  ScopeUpstream,
		Status: CheckUnknown,
		Detail: "本机无法自测：从本机访问自己的公网地址通常走回环，" +
			"因此无论上游是否放行都会显示成功",
		Hint: "用手机 4G/5G 打开验证地址进行确认。" +
			"这一步不能省 —— 它是区分「本机没配好」与「运营商封了」的唯一手段",
	})

	// Viable 只看本机检测：上游不通不代表本机配置有问题。
	viable := true
	for _, c := range checks {
		if c.Scope == ScopeLocal && c.Status == CheckFail {
			viable = false
			break
		}
	}

	return Readiness{
		Viable:  viable,
		Checks:  checks,
		Summary: summarize(checks),
	}, nil
}

// addressSnapshot 汇总地址探测的结果。
type addressSnapshot struct {
	globalIPv6 int
	prefixes   int
	ifaces     []string
}

func (p *IPv6Native) inspectAddresses(ctx context.Context) (addressSnapshot, int, int) {
	var snap addressSnapshot

	if p.monitor == nil {
		return snap, 0, 0
	}
	list, err := p.monitor.Snapshot(ctx)
	if err != nil {
		return snap, 0, 0
	}

	for _, iface := range list {
		if iface.IsLoopback {
			continue
		}
		g := iface.GlobalIPv6()
		if len(g) == 0 && len(iface.Prefixes) == 0 {
			continue
		}
		snap.ifaces = append(snap.ifaces, iface.Name)
		snap.globalIPv6 += len(g)
		snap.prefixes += len(iface.Prefixes)
	}
	return snap, snap.globalIPv6, snap.prefixes
}

func addressCheck(snap addressSnapshot, globalCount int) Check {
	if globalCount > 0 {
		return Check{
			Name:   "全局 IPv6 地址",
			Scope:  ScopeLocal,
			Status: CheckPass,
			Detail: fmt.Sprintf("在 %s 上找到 %d 个可用于公网访问的 IPv6 地址",
				strings.Join(snap.ifaces, "、"), globalCount),
		}
	}
	return Check{
		Name:   "全局 IPv6 地址",
		Scope:  ScopeLocal,
		Status: CheckFail,
		Detail: "没有找到可用于公网访问的 IPv6 地址" +
			"（链路本地 fe80:: 与私有 fd00:: 不算）",
		Hint: "确认运营商已开通 IPv6（多数家宽默认开通，可打客服确认）；" +
			"再进路由器的 IPv6 设置，确认已开启且为「Native / 原生」模式而非隧道模式",
	}
}

func prefixCheck(snap addressSnapshot, prefixCount int) Check {
	if prefixCount > 0 {
		return Check{
			Name:   "IPv6 委派前缀",
			Scope:  ScopeLocal,
			Status: CheckPass,
			Detail: fmt.Sprintf("检测到 %d 个委派前缀（/64 或更粗）", prefixCount),
		}
	}
	if snap.globalIPv6 == 0 {
		// 地址都没有，前缀这一项就没有单独的诊断价值 ——
		// 重复报同一个根因只会让用户以为有两个问题。
		return Check{
			Name:   "IPv6 委派前缀",
			Scope:  ScopeLocal,
			Status: CheckUnknown,
			Detail: "没有全局 IPv6 地址，无法判断前缀",
		}
	}
	return Check{
		Name:   "IPv6 委派前缀",
		Scope:  ScopeLocal,
		Status: CheckWarn,
		Detail: "有全局 IPv6 地址，但没有检测到 /64 或更粗的委派前缀。" +
			"这通常意味着地址是运营商逐台分配的（/128），" +
			"重启或换设备后地址会变",
		Hint: "这种情形下动态解析仍然可用，但地址变化会更频繁。" +
			"建议把检测周期调短一些",
	}
}

func (p *IPv6Native) firewallCheck() Check {
	if p.firewall == nil || !p.firewallState.Available {
		backend := p.firewallState.Backend
		if backend == "" {
			backend = "未实现"
		}
		return Check{
			Name:   "本机防火墙后端",
			Scope:  ScopeLocal,
			Status: CheckFail,
			Detail: fmt.Sprintf("当前平台（%s）的防火墙后端尚未实现，内核无法自动放行端口", backend),
			Hint: "这是内核的能力缺口，不是你的配置问题。" +
				"请手动在系统防火墙中放行需要的端口，或等待后续版本",
		}
	}
	return Check{
		Name:   "本机防火墙后端",
		Scope:  ScopeLocal,
		Status: CheckPass,
		Detail: fmt.Sprintf("已接入 %s；放行操作会先生成可预览的计划，"+
			"并在失败时自动回滚", p.firewallState.Backend),
	}
}

// summarize 生成一句话结论。
//
// 它的写法刻意是"先给结论、再给下一步"：用户扫一眼就该知道
// 现在该做什么，而不是自己去读五项检测。
func summarize(checks []Check) string {
	var firstFail *Check
	for i := range checks {
		c := checks[i]
		if c.Scope == ScopeLocal && c.Status == CheckFail {
			firstFail = &checks[i]
			break
		}
	}
	if firstFail != nil {
		return "本机还差一步：" + firstFail.Name
	}
	return "本机已具备 IPv6 直连的条件；能否从外网访问需要用手机流量验证"
}

// ---------------------------------------------------------------------------
// 计划
// ---------------------------------------------------------------------------

// Plan 实现 Provider。
//
// 它把"开放端口"翻译成一条防火墙规则，并交给平台后端去计算差异。
// 本方法自身**不修改任何系统状态** —— 差异计算必须是只读的，
// 否则"预览"就变成了"已经动手了"。
func (p *IPv6Native) Plan(ctx context.Context, req Request) (change.Plan, error) {
	if req.Port <= 0 || req.Port > 65535 {
		return change.Plan{}, fmt.Errorf("reach: 端口 %d 不合法", req.Port)
	}
	proto, err := parseProtocol(req.Protocol)
	if err != nil {
		return change.Plan{}, err
	}

	if p.firewall == nil || !p.firewallState.Available {
		return change.Plan{}, fmt.Errorf(
			"reach: 本平台（%s）的防火墙后端尚未实现，无法自动放行端口；"+
				"请手动在系统防火墙中放行 %d/%s",
			p.firewallState.Backend, req.Port, proto)
	}

	// 端口层诊断。
	//
	// 它在这里（而不是 Probe）做，是因为只有 Plan 拿得到端口号 ——
	// 而"那个端口上有没有服务"正是最值得在动手之前说清楚的事。
	portChecks := DiagnosePort(ctx, req, p.lowPort)

	rule := platform.Rule{
		Name:        ruleName(req.Label, proto, req.Port),
		Protocol:    proto,
		Port:        platform.NewPort(uint16(req.Port)),
		Description: "由 ISC 管理 —— 可在 ISC 中一键撤销",
	}

	// 让平台后端去算差异：它才知道自己那边"当前状态"长什么样
	//（Windows 要按配置文件区分、nftables 要看表与链）。
	ch, err := p.firewall.Plan(ctx, []platform.Rule{rule})
	if err != nil {
		return change.Plan{}, fmt.Errorf("reach: 计算防火墙差异失败: %w", err)
	}

	plan := change.Plan{
		ID:        newPlanID("firewall"),
		Kind:      KindFirewallExpose,
		Title:     fmt.Sprintf("放行 %d/%s 的入站连接", req.Port, proto),
		Risk:      RiskForExpose(req.Port),
		CreatedAt: time.Now().UTC(),
		Notes: []string{
			"规则只放行这一个端口，不会改动其它规则。",
			"撤销时会恢复成添加之前的状态。",
		},
	}

	if !ch.Reversible {
		plan.Warnings = append(plan.Warnings,
			"该后端报告此变更不可撤销；应用后需要手动清理。")
	}

	// 把诊断结果分成"拦住"与"提醒"两类。
	//
	// 失败项**直接拒绝生成计划**：没有权限绑低端口这种事，
	// 重试、等一会儿、改配置都不会让它变好 —— 让用户先应用再看它失败
	// 只是浪费一次系统变更。
	//
	// 警告项放进 Warnings：它们描述的是"你可能不想继续"的情形，
	// 而不是"一定不行"。端口上没有服务就是典型 —— 用户可能正要启动它。
	for _, c := range portChecks {
		switch c.Status {
		case CheckFail:
			return change.Plan{}, fmt.Errorf("reach: %s",
				joinCheckText(c.Detail, c.Hint))

		case CheckWarn:
			plan.Warnings = append(plan.Warnings,
				joinCheckText(c.Detail, c.Hint))
		}
	}

	// 平台后端没算出差异 = 规则已存在。
	if len(ch.Payload) == 0 && ch.Summary == "" {
		return plan, nil
	}

	// 把后端私有的数据带上：它是**跨进程撤销**的唯一依据。
	//
	// 不带的话，内核重启后就没有任何办法知道当初创建了哪几条规则，
	// 只能让用户手动去防火墙界面里找。
	plan.Payload = ch.Payload

	// 用闭包把平台后端的 Apply / Rollback 包成计划步骤。
	//
	// 这样无论变更来自哪个插件，"预览 → 应用 → 失败自动回滚 → 事后撤销"
	// 这套保证都是同一份代码在提供，而不是每个插件各写一遍。
	plan.Steps = []change.Step{change.NewStep(
		"firewall-rule",
		ch.Summary,
		func(ctx context.Context) error { return p.firewall.Apply(ctx, ch) },
		func(ctx context.Context) error { return p.firewall.Rollback(ctx, ch) },
		change.DiffLine{Op: change.OpAdd, Text: describeRule(rule)},
	)}
	plan.Steps[0].Details = ch.Diff

	return plan, nil
}

// KindFirewallExpose 是"放行端口"这类变更的标识。
const KindFirewallExpose = "firewall.expose_port"

// RiskForExpose 给出放行端口这件事的风险等级。
//
// 它对全部网络配置文件生效时是高风险：那意味着笔记本在咖啡厅的
// 公共 Wi-Fi 上也会把这个服务暴露出去。
func RiskForExpose(port int) change.Risk {
	// 常见的高危管理端口单独提级：开放它们等于把管理界面交出去。
	switch port {
	case 22, 23, 135, 139, 445, 3389, 5900, 5985, 5986:
		return change.RiskHigh
	}
	return change.RiskMedium
}

// Revert 实现 change.Reverter。
//
// # 它依赖什么
//
// 撤销一条防火墙规则需要知道"当初创建了哪几条"。那个信息由平台后端
// 序列化进 `change.Record.Payload`（见 change.Plan.Payload 的说明），
// 因此**跨越内核重启仍然可用**。
//
// 上一版没有 payload 这个字段，只能报错让用户手动清理 —— 那是把框架的
// 设计缺口转嫁给了用户。现在补上了。
//
// 仍然有两条路径会明确报错，而不是假装成功：
//
//   - 记录来自更早的内核版本，那时还没有 payload；
//   - 当前平台的防火墙后端不可用（例如把数据目录搬到另一台机器）。
//
// 两种情况下都给出规则名前缀，用户据此能在系统防火墙里找到它们。
func (p *IPv6Native) Revert(ctx context.Context, rec change.Record) error {
	if p.firewall == nil || !p.firewallState.Available {
		return fmt.Errorf(
			"reach: 无法自动撤销 %s：当前平台的防火墙后端不可用。"+
				"请在系统防火墙中手动删除以 %q 开头的规则",
			rec.PlanID, rulePrefix)
	}

	if len(rec.Payload) == 0 {
		return fmt.Errorf(
			"reach: 无法自动撤销 %s：这条变更没有保存回滚数据"+
				"（可能由更早的内核版本创建）。"+
				"请在系统防火墙中手动删除以 %q 开头的规则",
			rec.PlanID, rulePrefix)
	}

	ch := platform.Change{
		ID:         rec.PlanID,
		Kind:       "firewall.rules",
		Payload:    rec.Payload,
		Reversible: true,
	}
	if err := p.firewall.Rollback(ctx, ch); err != nil {
		return fmt.Errorf("reach: 撤销防火墙变更 %s 失败: %w", rec.PlanID, err)
	}
	return nil
}

// Kind 实现 change.Reverter。
func (p *IPv6Native) Kind() string { return KindFirewallExpose }

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// joinCheckText 把"观察到的事实"与"该怎么办"拼成一段可读的话。
//
// 直接用一个空格拼接会读成连体句：事实那句通常不以标点结尾，
// 而提示那句是一个完整句子 —— 拼出来就是
// "…没有服务在监听 开放这个端口之前…"。
//
// 标点也要补：事实句没有句号时补一个，否则两句会粘在一起。
//
// 这是真机上跑出来才看出来的 —— 两条文案各自都通顺，
// 拼在一起才发现重复又拗口。
func joinCheckText(detail, hint string) string {
	detail = strings.TrimSpace(detail)
	hint = strings.TrimSpace(hint)

	if hint == "" {
		return detail
	}
	if detail == "" {
		return hint
	}

	runes := []rune(detail)
	// 结尾已经是标点时不重复添加。
	if !strings.ContainsRune("。！？.!?", runes[len(runes)-1]) {
		detail += "。"
	}
	return detail + " " + hint
}

// rulePrefix 是 ISC 创建的防火墙规则的统一前缀。
//
// 存在的意义是让用户在系统防火墙界面里一眼认出哪些规则属于 ISC，
// 也是上面那条"手动删除"提示的依据。
const rulePrefix = "isc-"

func ruleName(label string, proto platform.Protocol, port int) string {
	name := strings.TrimSpace(label)
	if name == "" {
		name = "service"
	}
	// 规则名会进入系统防火墙的命令行，因此必须清掉可能被解释为
	// 参数或分隔符的字符。
	name = sanitizeName(name)
	return fmt.Sprintf("%s%s-%s-%d", rulePrefix, name, proto, port)
}

// RuleLabel 返回给定参数会生成的规则名。
//
// 导出它是为了让接口层在写审计日志时用**同一个**名字 —— 若那边自己
// 拼一个，用户拿着审计里的名字去系统防火墙里找会找不到。
func RuleLabel(label, proto string, port int) string {
	p, err := parseProtocol(proto)
	if err != nil {
		p = platform.TCP
	}
	return ruleName(label, p, port)
}

func sanitizeName(s string) string {
	var b strings.Builder
	// lastDash 用于折叠连续的短横线。
	//
	// 不只是为了好看：`--` 在命令行里是"选项结束"标记，而规则名
	// 会出现在 netsh / nft 的命令行里。名字里出现 `--` 本身不会
	// 立刻出问题，但一个能产生 `--` 的命名规则是一个随时可能
	// 变成参数注入的隐患，而折叠掉它没有任何代价。
	lastDash := false

	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == ' ' || r == '_' || r == '.':
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		default:
			// 非 ASCII（例如中文服务名）直接丢弃：防火墙规则的名称
			// 在不同平台上对字符集的要求不一致，塞进去会在某些平台
			// 上变成一条无法创建、也无法删除的幽灵规则。
		}
	}

	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "service"
	}
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	if out == "" {
		return "service"
	}
	return out
}

func parseProtocol(s string) (platform.Protocol, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "tcp":
		return platform.TCP, nil
	case "udp":
		return platform.UDP, nil
	default:
		return "", fmt.Errorf("reach: 不支持的协议 %q（只支持 tcp / udp）", s)
	}
}

func describeRule(r platform.Rule) string {
	return fmt.Sprintf("入站 %s %s（来源：任意）", r.Protocol, r.Port)
}

func newPlanID(kind string) string {
	// 用纳秒时间戳 + 类型：计划 ID 只需要在单机内不重复，
	// 而它会被写进日志供人阅读，因此可读性比随机性更重要。
	return fmt.Sprintf("%s-%d", kind, time.Now().UTC().UnixNano())
}

// PortListening 报告本机某端口上是否有服务在监听。
//
// 用"连一下"而不是"试着绑定"：绑定会短暂占住端口，而如果有服务
// 正在用这个端口，绑定失败并不能区分"被自己占用"与"被别的程序占用"。
// 连接测试是只读的，且语义明确 —— 能连上就是有人在听。
func PortListening(ctx context.Context, port int) bool {
	d := net.Dialer{Timeout: 700 * time.Millisecond}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ErrNotViable 表示当前方式不可用。
var ErrNotViable = errors.New("reach: 当前方式不可用")
