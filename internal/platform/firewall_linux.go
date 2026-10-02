//go:build linux

package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"os/exec"
	"strings"
	"time"
)

// 本文件是 Linux 防火墙后端（nftables）。
//
// 纯逻辑（命令拼装、ruleset 解析、校验）在 firewall_nft_def.go 里，
// 可以跨平台测试；这里只有"执行 nft 命令"这一层。
//
// # 为什么是 nftables 而不是 iptables
//
// nftables 是 iptables 的正式继任者，Debian 10+ / RHEL 8+ / Ubuntu 20.04+
// 都默认用它。而两者的规则模型不同（iptables 按"链 + 规则序号"，
// nftables 按"表 + 链 + handle"），同时支持两套会让代码量与出错面翻倍。
//
// 对老系统（只有 iptables 的）这里会报"后端不可用"，并提示用户手动
// 放行 —— 那比假装支持、然后在用户的机器上失败要好。

const nftTimeout = 20 * time.Second

// execRunner 抽象"执行一条命令"，便于测试。
type nftRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type realNftRunner struct{}

func (realNftRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

type nftablesFirewall struct {
	runner nftRunner
	// bin 是 nft 可执行文件的路径；空表示走 PATH。
	bin string
}

// NewNftablesFirewall 构造 nftables 后端。
func newNftablesFirewall() Firewall {
	return &nftablesFirewall{runner: realNftRunner{}, bin: "nft"}
}

func (f *nftablesFirewall) Describe() ImplState {
	return ImplState{
		Available: true,
		Backend:   "nftables",
		Note:      i18n.T("platform.nft_note"),
	}
}

func (f *nftablesFirewall) binPath() string {
	if f.bin != "" {
		return f.bin
	}
	return "nft"
}

// Inspect 读取 ISC 表里当前的规则。
func (f *nftablesFirewall) Inspect(ctx context.Context) ([]Rule, error) {
	// -j 输出 JSON，-a 带上 handle（删除规则必须用它）。
	//
	// 少了 -a 就没有 handle，而**没有 handle 就删不掉规则** ——
	// 症状是"撤销失败"，且错误信息里看不出原因。
	out, err := f.run(ctx, "-j", "-a", "list", "ruleset")
	if err != nil {
		// "表不存在"是正常状态（还没配过任何东西），不是错误。
		if isNftTableMissing(out) {
			return nil, nil
		}
		return nil, fmt.Errorf(i18n.T("platform.nft_read_failed"),
			err, strings.TrimSpace(string(out)))
	}

	parsed, err := ParseNftRuleset(out)
	if err != nil {
		// 落到这里说明 nft 的输出不是我们认识的 JSON ——
		// 可能是版本差异。把原始输出的开头带上，便于定位。
		return nil, fmt.Errorf(i18n.T("platform.nft_output_head"), err, head(string(out), 200))
	}

	rules := make([]Rule, 0, len(parsed))
	for _, p := range parsed {
		rules = append(rules, Rule{
			Name:        p.Name,
			Protocol:    p.Proto,
			Port:        p.Port,
			Source:      p.Source,
			Description: i18n.T("platform.pf_rule_desc"),
		})
	}
	return rules, nil
}

// Plan 计算差异。
func (f *nftablesFirewall) Plan(ctx context.Context, desired []Rule) (Change, error) {
	for _, r := range desired {
		if err := ValidateNftRule(r); err != nil {
			return Change{}, err
		}
	}

	current, err := f.Inspect(ctx)
	if err != nil {
		return Change{}, err
	}

	// 把已存在的规则转回 nftRule（带回 handle），供 NftCommands 用。
	currentParsed, err := f.inspectParsed(ctx)
	if err != nil {
		return Change{}, err
	}

	cmds := NftCommands(currentParsed, desired)

	payload := nftPayload{
		Commands: cmds,
		// 记下变更前的规则名：回滚时要恢复成这个集合。
		//
		// 它是**跨进程撤销的唯一依据** —— 内核重启后没有任何别的
		// 办法知道当初是什么状态。
		Before: ruleNames(current),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Change{}, fmt.Errorf(i18n.T("platform.pf_marshal"), err)
	}

	added := diffRuleNames(desired, current)
	removed := diffRuleNames(current, desired)

	var summary string
	switch {
	case len(added) == 0 && len(removed) == 0:
		summary = i18n.T("platform.pf_nochange")
	case len(removed) == 0:
		summary = fmt.Sprintf(i18n.T("platform.pf_add"), len(added))
	case len(added) == 0:
		summary = fmt.Sprintf(i18n.T("platform.pf_remove"), len(removed))
	default:
		summary = fmt.Sprintf(i18n.T("platform.pf_addremove"),
			len(added), len(removed))
	}

	return Change{
		ID:       newChangeID("firewall"),
		Platform: "linux",
		Backend:  "nftables",
		Kind:     "firewall.rules",
		Summary:  summary,
		Diff:     renderRuleDiff(added, removed),
		Payload:  raw,
		// nftables 的变更是可撤销的：删掉我们加的那些规则即可，
		// 而用户的规则我们从未碰过（独立的表）。
		Reversible: true,
	}, nil
}

// Apply 应用变更。
//
// 命令按**一次 nft 调用**执行：nft 支持在一条命令里传多行指令，
// 而逐条执行会产生多次进程启动，也失去了"要么全成、要么全不成"的
// 原子性（nft 对单次输入是事务性的）。
func (f *nftablesFirewall) Apply(ctx context.Context, ch Change) error {
	var payload nftPayload
	if err := json.Unmarshal(ch.Payload, &payload); err != nil {
		return fmt.Errorf(i18n.T("platform.pf_unmarshal"), err)
	}
	if len(payload.Commands) == 0 {
		return nil // 无需改动
	}

	return f.applyCommands(ctx, payload.Commands)
}

// Rollback 撤销变更：把规则集合恢复成变更前的样子。
//
// # 为什么是"重新计算"而不是"反向执行"
//
// 反向执行需要精确记住每一步做了什么，而中间可能有人手工改过规则、
// 或者内核重启过。重新计算差异更稳：
//
//	读当前状态 → 算出"回到 Before"需要做什么 → 执行
//
// 它对中间发生的任何变化都成立，而反向执行只对"什么都没变"成立。
func (f *nftablesFirewall) Rollback(ctx context.Context, ch Change) error {
	var payload nftPayload
	if err := json.Unmarshal(ch.Payload, &payload); err != nil {
		return fmt.Errorf(i18n.T("platform.pf_unmarshal"), err)
	}

	// 目标状态：变更前的那些规则名。
	// 我们只能恢复出名字、协议、端口 —— 那些信息编码在名字里
	//（这也是统一命名约定的价值之一）。
	want := make([]Rule, 0, len(payload.Before))
	for _, name := range payload.Before {
		proto, port, ok := parseNftRuleName(name)
		if !ok {
			continue // 名字不合法：跳过而不是猜
		}
		want = append(want, Rule{
			Name:     name,
			Protocol: Protocol(proto),
			Port:     NewPort(port),
		})
	}

	current, err := f.inspectParsed(ctx)
	if err != nil {
		return err
	}

	cmds := NftCommands(current, want)
	return f.applyCommands(ctx, cmds)
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

func (f *nftablesFirewall) applyCommands(ctx context.Context, cmds []string) error {
	if len(cmds) == 0 {
		return nil
	}
	script := strings.Join(cmds, "\n") + "\n"

	out, err := f.runStdin(ctx, script, "-f", "-")
	if err != nil {
		return fmt.Errorf(i18n.T("platform.nft_apply_failed"),
			err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (f *nftablesFirewall) inspectParsed(ctx context.Context) ([]nftRule, error) {
	out, err := f.run(ctx, "-j", "-a", "list", "ruleset")
	if err != nil {
		if isNftTableMissing(out) {
			return nil, nil
		}
		return nil, fmt.Errorf(i18n.T("platform.nft_read_failed2"), err)
	}
	return ParseNftRuleset(out)
}

func (f *nftablesFirewall) run(ctx context.Context, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, nftTimeout)
	defer cancel()
	return f.runner.Run(cctx, f.binPath(), args...)
}

func (f *nftablesFirewall) runStdin(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, nftTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, f.binPath(), args...)
	cmd.Stdin = strings.NewReader(stdin)
	return cmd.CombinedOutput()
}

// nftPayload 是 nftables 变更的私有载荷。
type nftPayload struct {
	// Commands 是这次变更要执行的 nft 指令。
	Commands []string `json:"commands"`
	// Before 是变更前的规则名集合，回滚时用来重建。
	Before []string `json:"before"`
}

// isNftTableMissing 判断输出是否为"表不存在"。
//
// 那**不是错误**：还没配过任何规则时表就不存在，而报错会让用户
// 在第一次使用时看到一个红字。
func isNftTableMissing(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "no such file or directory") ||
		strings.Contains(s, "does not exist") ||
		strings.Contains(s, "no such table")
}

func head(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
