// Package change 提供"计划 → 预览 → 应用 → 回滚"的变更编排。
//
// # 为什么需要这一层
//
// M3 之后内核会开始修改**系统状态**：防火墙规则、服务注册、证书文件。
// 这类操作与"改一条 DNS 记录"有本质区别：
//
//   - 失败的影响面不同。一条 DNS 记录写错，用户改回来即可；
//     一条防火墙规则写错，可能把用户自己锁在门外 —— 包括锁掉
//     远程桌面，而那时用户已经没有别的办法连上这台机器。
//   - 状态在系统那边，不在我们这边。数据库可以回滚事务，
//     防火墙不行；我们必须自己记住改了什么，才能撤销。
//   - 用户无法凭直觉判断后果。"开放 443 端口"听起来无害，
//     但如果规则被加到错误的网络配置文件上，它可能在咖啡厅的
//     公共 Wi-Fi 上把服务暴露出去。
//
// 因此所有系统变更都必须能**在动手之前看清楚**（Preview）、
// **失败时自动退回去**（Apply 的内部回滚）、以及**事后能撤销**
// （Rollback）。这三件事由本包统一保证，而不是指望每个调用点自觉。
//
// # 与"引导模式"的关系
//
// 平台后端未实现时（见 docs/RISKS 的 R2），生成计划的调用会返回
// 一个明确说明"该平台尚未实现"的错误，而不是一个空计划。
// 空计划会让用户看到"预览：无变更"然后点应用，以为成功了。
package change

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Risk 是变更的风险等级。
//
// 它决定界面上的提示强度。刻意只有三档：档位太多会让用户对
// 中间的档位失去判断力，而"危险"与"注意"的区别在实践中够用。
type Risk string

const (
	// RiskLow 影响面小、可轻易撤销，例如新增一条明确限定来源的规则。
	RiskLow Risk = "low"
	// RiskMedium 会影响服务的可达性，例如开放一个端口。
	RiskMedium Risk = "medium"
	// RiskHigh 可能切断用户与机器的连接，例如修改默认策略、
	// 或把规则应用到全部网络配置文件。
	//
	// 这类变更在界面上必须要求显式确认，且回滚入口必须显著。
	RiskHigh Risk = "high"
)

// DiffOp 是一行差异的操作类型。
type DiffOp string

const (
	// OpAdd 新增。
	OpAdd DiffOp = "add"
	// OpRemove 移除。
	OpRemove DiffOp = "remove"
	// OpKeep 保持不变（只读展示，用于上下文）。
	OpKeep DiffOp = "keep"
	// OpChange 修改。
	OpChange DiffOp = "change"
)

// DiffLine 是一行人类可读的差异。
//
// 用自然语言而不是结构化字段：这段文本会原样显示在控制台与 CLI 上，
// 而"把结构化差异渲染成人话"是每个调用点都要重复一遍的负担，
// 且很容易渲染得含混。
type DiffLine struct {
	Op   DiffOp `json:"op"`
	Text string `json:"text"`
}

// Operation 是一次实际的系统操作。
//
// 返回 error 表示这次操作没有生效或状态未知 —— 两种情况都按
// "需要回滚"处理：宁可多退一次，也不要留下一个未知状态。
type Operation func(ctx context.Context) error

// Step 是计划中的一个原子步骤。
type Step struct {
	// ID 在计划内唯一，用于日志、事件与回滚定位。
	ID string
	// Title 是人类可读的一行说明（已本地化）。
	Title string
	// Diff 是这一步造成的差异。
	Diff []DiffLine
	// Details 是补充说明，例如"需要管理员权限"。
	Details string

	// apply 执行这一步。成功后才允许进入下一步。
	apply Operation
	// revert 撤销这一步。
	//
	// 允许为 nil —— 表示"这一步无需撤销"（例如一条只读探测）。
	// 为 nil 时回滚会跳过它并记录一条说明，而不是报错：
	// 把"无需撤销"当成错误会让自动回滚在无害的步骤上中断。
	revert Operation
}

// NewStep 构造一个步骤。
func NewStep(id, title string, apply, revert Operation, diff ...DiffLine) Step {
	return Step{ID: id, Title: title, apply: apply, revert: revert, Diff: diff}
}

// Revertable 报告这一步是否需要撤销。
func (s Step) Revertable() bool { return s.revert != nil }

// Plan 是一次完整的变更计划。
type Plan struct {
	// ID 是本计划的标识，回滚时用它找到日志。
	ID string
	// Kind 是变更类型，例如 "firewall.expose_port"。
	Kind string
	// Title 是人类可读的标题（已本地化）。
	Title string
	// Risk 是整体风险等级，取所有步骤中最高的那个。
	Risk Risk
	// Steps 按执行顺序排列。
	Steps []Step
	// Warnings 是需要在界面上显著提示的内容。
	//
	// 与 Notes 分开：Notes 是背景说明，Warnings 是"你可能不想继续"。
	Warnings []string
	// Notes 是背景说明，例如"这些规则只对专用网络生效"。
	Notes []string

	// Payload 是后端私有的数据，本包**不解释**它。
	//
	// # 它为什么必须存在
	//
	// revert 闭包无法持久化。用户上午应用了一次防火墙变更、下午重启了
	// 内核、晚上想撤销 —— 那时进程里早已没有当初的闭包，而撤销一条
	// 防火墙规则又必须知道"当初创建了哪几条"。
	//
	// 于是只能让制造变更的后端把自己需要的东西序列化到这里，
	// 由本包原样存进日志、原样交还给它的 Reverter。
	//
	// 上一版没有这个字段，代价是跨进程撤销做不了，只能报错让用户
	// 手动清理。那个缺口现在被补上了。
	Payload []byte

	CreatedAt time.Time
}

// Diff 汇总计划内全部步骤的差异。
func (p Plan) Diff() []DiffLine {
	var out []DiffLine
	for _, s := range p.Steps {
		out = append(out, s.Diff...)
	}
	return out
}

// Empty 报告计划是否没有任何步骤。
//
// 调用方应当把它当作"已达成目标状态"而不是"操作失败"：
// 用户点"开放 443"而 443 已经开放时，正确的回应是
// "无需改动"，而不是一个报错。
func (p Plan) Empty() bool { return len(p.Steps) == 0 }

// Validate 检查计划是否可执行。
//
// 挡住的是**程序错误**而不是用户错误：没有 ID、没有标题、
// 步骤 ID 重复。这些症状如果放到执行期，表现出来会是一堆
// 难以定位的怪问题（日志里两条同样的步骤、回滚时定位到错误的记录）。
func (p Plan) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("change: 计划缺少 ID")
	}
	if strings.TrimSpace(p.Kind) == "" {
		return errors.New("change: 计划缺少变更类型")
	}
	if strings.TrimSpace(p.Title) == "" {
		return errors.New("change: 计划缺少标题")
	}

	seen := make(map[string]bool, len(p.Steps))
	for i, s := range p.Steps {
		if strings.TrimSpace(s.ID) == "" {
			return fmt.Errorf("change: 第 %d 个步骤缺少 ID", i+1)
		}
		if seen[s.ID] {
			// 重复 ID 会让回滚定位到错误的步骤 —— 而那是在
			// 出问题的时候才暴露，代价最高。
			return fmt.Errorf("change: 步骤 ID 重复: %s", s.ID)
		}
		seen[s.ID] = true

		if s.apply == nil {
			return fmt.Errorf("change: 步骤 %s 没有执行体", s.ID)
		}
	}
	return nil
}

// highestRisk 返回一组步骤中最高的风险等级。
//
// 刻意**不做**"从 Diff 内容推断风险"的猜测：猜错的方向恰好是把
// 危险操作显示成安全，而那个代价不可接受。风险等级由构造计划的
// 那一段代码显式声明 —— 它才知道自己做了什么。
func highestRisk(steps []Step, declared Risk) Risk {
	rank := map[Risk]int{RiskLow: 1, RiskMedium: 2, RiskHigh: 3}

	out := declared
	if out == "" || rank[out] == 0 {
		out = RiskLow
	}
	_ = steps
	return out
}
