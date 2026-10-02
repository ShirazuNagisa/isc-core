//go:build darwin

package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"os"
	"os/exec"
	"strings"
	"time"
)

// 本文件是 macOS 防火墙后端（pf）。
//
// 纯逻辑（anchor 渲染、解析、pf.conf 处理、校验）在 firewall_pf_def.go
// 里，可以跨平台测试；这里只有"读写文件、执行 pfctl"这一层。
//
// # pf 与 nftables 的根本差异
//
// 见 firewall_pf_def.go 的文件头：pf 是**声明式**的，配置是一整份文本，
// pfctl 读进去之后整体替换。因此这里没有"差集"这回事 ——
// 期望状态直接渲染成 anchor 全文，写进去、重载。
//
// 代价是任何一次改动都会重载**全部**规则，而重载有可见的代价：
// pfctl 在重载期间对新建连接的处理会短暂异常。因此本后端会先比较
// 文件内容，**没变就不重载**。

const (
	pfConfPath   = "/etc/pf.conf"
	pfctlTimeout = 20 * time.Second

	// pfConfBackupSuffix 是改动 /etc/pf.conf 前的备份后缀。
	pfConfBackupSuffix = ".isc-backup"
)

type pfRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type realPfRunner struct{}

func (realPfRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

type pfFirewall struct {
	runner     pfRunner
	confPath   string
	anchorPath string
}

// newPfFirewall 构造 pf 后端。
func newPfFirewall() Firewall {
	return &pfFirewall{
		runner:     realPfRunner{},
		confPath:   pfConfPath,
		anchorPath: pfAnchorPath,
	}
}

func (f *pfFirewall) Describe() ImplState {
	return ImplState{
		Available: true,
		Backend:   "pf",
		Note: "/etc/pf.anchors/" + pfAnchorName +
			i18n.T("platform.pf_note"),
	}
}

// Inspect 读取 anchor 文件里当前的 ISC 规则。
func (f *pfFirewall) Inspect(_ context.Context) ([]Rule, error) {
	byt, err := os.ReadFile(f.anchorPath)
	if err != nil {
		if os.IsNotExist(err) {
			// anchor 文件还不存在 = 还没配过任何规则。不是错误。
			return nil, nil
		}
		return nil, fmt.Errorf(i18n.T("platform.pf_read_failed"), f.anchorPath, err)
	}

	// 只解析出规则名 —— 完整的 pf 语法相当复杂，而我们只需要知道
	// 自己写了哪些规则（那是自己生成的、格式已知）。
	//
	// 协议与端口从名字里还原：这正是统一命名约定的价值。
	var out []Rule
	for _, name := range ParsePfAnchor(string(byt)) {
		proto, port, ok := parseNftRuleName(name)
		if !ok {
			continue
		}
		out = append(out, Rule{
			Name:        name,
			Protocol:    Protocol(proto),
			Port:        NewPort(port),
			Description: i18n.T("platform.pf_rule_desc"),
		})
	}
	return out, nil
}

// Plan 计算差异。
//
// 内容没变时返回一个**空计划**：pf 的重载有可见代价，而不必要的重载
// 会让正在使用的连接受到影响。
func (f *pfFirewall) Plan(ctx context.Context, desired []Rule) (Change, error) {
	for _, r := range desired {
		if err := ValidatePfRule(r); err != nil {
			return Change{}, err
		}
	}

	current, err := f.Inspect(ctx)
	if err != nil {
		return Change{}, err
	}

	content := PfAnchorContent(desired)

	// 内容与磁盘上一致 → 无需改动。
	//
	// 比较**渲染后的全文**而不是规则集合：全文包含了顺序与格式，
	// 而它们也影响 pf 的行为（虽然不大）。用全文比较更保守，
	// 也更容易解释。
	if existing, err := os.ReadFile(f.anchorPath); err == nil {
		if string(existing) == content {
			return Change{
				ID:         newChangeID("firewall"),
				Platform:   "darwin",
				Backend:    "pf",
				Kind:       "firewall.rules",
				Summary:    i18n.T("platform.pf_nochange"),
				Diff:       i18n.T("platform.fw_nochange"),
				Reversible: true,
			}, nil
		}
	}

	payload := pfPayload{
		Content: content,
		Before:  ruleNames(current),
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
		Platform: "darwin",
		Backend:  "pf",
		Kind:     "firewall.rules",
		Summary:  summary,
		Diff:     renderRuleDiff(added, removed),
		Payload:  raw,
		// 可撤销：把 anchor 写回变更前的内容即可。
		Reversible: true,
	}, nil
}

// Apply 写入 anchor 并重载 pf。
func (f *pfFirewall) Apply(ctx context.Context, ch Change) error {
	var payload pfPayload
	if err := json.Unmarshal(ch.Payload, &payload); err != nil {
		return fmt.Errorf(i18n.T("platform.pf_unmarshal"), err)
	}
	return f.writeAndReload(ctx, payload.Content)
}

// Rollback 恢复成变更前的规则集合。
func (f *pfFirewall) Rollback(ctx context.Context, ch Change) error {
	var payload pfPayload
	if err := json.Unmarshal(ch.Payload, &payload); err != nil {
		return fmt.Errorf(i18n.T("platform.pf_unmarshal"), err)
	}

	// 由名字还原出规则。
	//
	// 与 nftables 侧同样的做法：我们只能恢复出名字、协议、端口 ——
	// 那些信息编码在名字里。来源地址恢复不出来，而它在 IPv6 直连
	// 场景里通常为空（不限制来源），因此实际影响很小。
	//
	// 这一点值得如实说明：如果用户当初配了来源限制，撤销之后的规则
	// 会变成"任意来源"。这是一个**已知的降级**，写进了文档。
	var want []Rule
	for _, name := range payload.Before {
		proto, port, ok := parseNftRuleName(name)
		if !ok {
			continue
		}
		want = append(want, Rule{
			Name:     name,
			Protocol: Protocol(proto),
			Port:     NewPort(port),
		})
	}

	return f.writeAndReload(ctx, PfAnchorContent(want))
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

func (f *pfFirewall) writeAndReload(ctx context.Context, content string) error {
	if err := f.ensureAnchorDeclared(); err != nil {
		return err
	}

	// 写 anchor 文件。
	//
	// 它必须对所有用户可读：pfctl 以 root 读它，而让用户能查看
	// 自己的防火墙规则是有价值的（0644）。
	if err := os.WriteFile(f.anchorPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf(i18n.T("platform.pf_write_failed"), f.anchorPath, err)
	}

	// 校验语法再启用。
	//
	// 一份语法错误的 pf.conf 会让 `pfctl -f` 失败，而更糟的情况是
	// **把已经在生效的规则全部清掉**。先 -n（只解析不加载）能把
	// 问题挡在造成影响之前。
	if out, err := f.run(ctx, "-n", "-f", f.confPath); err != nil {
		return fmt.Errorf(i18n.T("platform.pf_syntax_failed"),
			f.confPath, err, strings.TrimSpace(string(out)))
	}

	// -f 重新加载配置；-E 确保 pf 处于启用状态。
	//
	// 两个都要：只 -f 在 pf 未启用时不会让它开始工作，而只 -E 不会
	// 读到新写的 anchor。
	if out, err := f.run(ctx, "-f", f.confPath); err != nil {
		return fmt.Errorf(i18n.T("platform.pf_reload_failed"),
			f.confPath, err, strings.TrimSpace(string(out)))
	}
	if out, err := f.run(ctx, "-E"); err != nil {
		return fmt.Errorf(i18n.T("platform.pf_enable_failed"),
			err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ensureAnchorDeclared 确保 /etc/pf.conf 引入了 ISC 的 anchor。
func (f *pfFirewall) ensureAnchorDeclared() error {
	byt, err := os.ReadFile(f.confPath)
	if err != nil {
		return fmt.Errorf(i18n.T("platform.pf_read_failed"), f.confPath, err)
	}

	updated := AppendPfAnchor(string(byt))
	if updated == string(byt) {
		return nil // 已经有了
	}

	// 改动前先备份。
	//
	// /etc/pf.conf 是系统的关键配置：改坏了会让**整个防火墙失效**，
	// 而那时用户可能连 SSH 都进不去（pf 的规则会影响入站连接）。
	// 备份让它至少可以手工恢复。
	//
	// 备份失败**不阻断**：用户在只有只读文件系统的环境里仍然
	// 应当能继续，而那时他会看到我们打印的备份失败提示。
	if err := os.WriteFile(f.confPath+pfConfBackupSuffix, byt, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, i18n.T("platform.pf_backup_failed"),
			f.confPath, err, pfAnchorName)
	}

	if err := os.WriteFile(f.confPath, []byte(updated), 0o644); err != nil {
		return fmt.Errorf(i18n.T("platform.pf_write_failed"), f.confPath, err)
	}
	return nil
}

func (f *pfFirewall) run(ctx context.Context, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, pfctlTimeout)
	defer cancel()
	return f.runner.Run(cctx, "pfctl", args...)
}

// pfPayload 是 pf 变更的私有载荷。
type pfPayload struct {
	// Content 是 anchor 文件的全文。
	Content string `json:"content"`
	// Before 是变更前的规则名集合，回滚时用来重建。
	Before []string `json:"before"`
}
