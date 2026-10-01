// Package reach 是"让本机服务可从公网访问"的插件抽象。
//
// # 为什么需要一个抽象，而不是直接写 IPv6
//
// 本产品的默认路径是 IPv6 原生（国内家宽普遍有 IPv6 委派前缀，
// 不需要公网 IPv4、不需要中转服务器）。但这条路有若干**不可控**的
// 失败点，而且它们看起来一模一样：
//
//	运营商没给 IPv6
//	给了 IPv6 但路由器没下发前缀
//	前缀有了但路由器防火墙没放行
//	本机防火墙没放行
//	服务没在监听
//	运营商封了入站端口（少数省份确实如此）
//
// 用户在每一层看到的都是"手机打不开"。如果他试了所有本地设置都没用，
// 需要的是一个**换路**的选择 —— frp、cloudflared 之类的中转方案。
// 那些方案与本机 IPv6 的区别很大（需要外部服务器、流量走中转、
// 配置完全不同），因此必须由插件来承担，而不是在主流程里塞 if。
//
// # 与 change 包的关系
//
// 插件不直接修改系统，它**生成变更计划**（change.Plan）。
// 这样"开放端口"这件事无论来自哪个插件，都自动获得
// 预览、失败回滚、事后撤销这三重保证。
package reach

import (
	"context"

	"github.com/ShirazuNagisa/isc-core/internal/change"
)

// Meta 描述一种可达方式。
type Meta struct {
	// Name 是稳定标识，例如 "ipv6-native"。
	Name string `json:"name"`
	// DisplayName 是给用户看的名字（已本地化）。
	DisplayName string `json:"display_name"`
	// Description 用一两句话说明这种方式的工作原理。
	//
	// 用户需要在"IPv6 直连"与"经中转服务器"之间做选择，
	// 而这个选择取决于他是否理解二者的差别 —— 尤其是
	// "流量是否经过第三方"这一点。
	Description string `json:"description"`
	// NeedsExternalServer 表示这种方式依赖一台外部服务器。
	//
	// 必须显式声明：它是用户决策时最关键的一条信息
	//（要不要另外买一台机器、流量会不会经过别人的设备）。
	NeedsExternalServer bool `json:"needs_external_server"`
	// Tier 标记实现成熟度。1 = 完整实现，2 = 骨架。
	Tier int `json:"tier"`
}

// CheckStatus 是单项检测的结果。
type CheckStatus string

const (
	// CheckPass 通过。
	CheckPass CheckStatus = "pass"
	// CheckFail 未通过，且这是**本机**能够修复的问题。
	CheckFail CheckStatus = "fail"
	// CheckWarn 有隐患但不阻断。
	CheckWarn CheckStatus = "warn"
	// CheckUnknown 无法判定（例如需要外部视角）。
	CheckUnknown CheckStatus = "unknown"
	// CheckBlocked 本机一切正常，但**上游**挡住了。
	//
	// 单独一档而不是并入 CheckFail：这两者的处置方式完全不同 ——
	// 一个是"去改本机设置"，另一个是"本机已经没得改了"。
	// 把它们混在一起正是让用户白白折腾几小时的原因。
	CheckBlocked CheckStatus = "blocked"
)

// Scope 说明一项检测是在哪里做的。
//
// 它决定了结论的可信度：本地检测通过只说明"本机没问题"，
// 不能说明"外面能连上"。区分这两件事是本包最重要的职责。
type Scope string

const (
	// ScopeLocal 在本机做的检测。
	ScopeLocal Scope = "local"
	// ScopeUpstream 需要外部视角才能做的检测。
	//
	// 这类检测**无法**在本机完成 —— 从本机访问自己的公网地址
	// 通常会走回环（NAT 发夹），因此无论防火墙是否放行都会"成功"。
	// 一个在本机自测通过的端口完全可能被运营商封着。
	ScopeUpstream Scope = "upstream"
)

// Check 是一项检测结果。
type Check struct {
	// Name 是检测项名称（已本地化）。
	Name string `json:"name"`
	// Scope 说明这项检测是在哪里做的。
	Scope Scope `json:"scope"`
	// Status 是结果。
	Status CheckStatus `json:"status"`
	// Detail 是观察到的具体事实，例如"未找到全局 IPv6 地址"。
	Detail string `json:"detail,omitempty"`
	// Hint 是**该怎么办**。
	//
	// 这一栏是整套检测里最有价值的部分：用户能自己搜索"没有全局 IPv6
	// 地址"该怎么做，但他真正需要的是"去路由器的 IPv6 设置里确认
	// 前缀委派（DHCPv6-PD）已开启"。
	Hint string `json:"hint,omitempty"`
}

// Readiness 是一次就绪度探测的结果。
type Readiness struct {
	// Viable 表示这种方式当前是否可用。
	//
	// 判定规则：所有 ScopeLocal 的检测都不是 CheckFail。
	// 注意 CheckBlocked **不影响** Viable —— 它说明本机已经准备好了，
	// 只是上游不放行，而那不代表本机配置有问题。
	Viable bool `json:"viable"`
	// Checks 是逐项结果，按"从下到上"的顺序排列
	//（本机地址 → 本机防火墙 → 服务监听 → 上游可达性）。
	Checks []Check `json:"checks"`
	// Summary 是一句话结论（已本地化）。
	Summary string `json:"summary"`
}

// BlockingCheck 返回第一个导致不可用的检测项。
//
// 用于生成"先解决这个"的提示 —— 一次列出十个问题只会让人无从下手。
func (r Readiness) BlockingCheck() (Check, bool) {
	for _, c := range r.Checks {
		if c.Scope == ScopeLocal && c.Status == CheckFail {
			return c, true
		}
	}
	return Check{}, false
}

// UpstreamBlocked 报告是否存在"本机正常但上游挡住"的检测项。
func (r Readiness) UpstreamBlocked() (Check, bool) {
	for _, c := range r.Checks {
		if c.Status == CheckBlocked {
			return c, true
		}
	}
	return Check{}, false
}

// Request 是"让某个服务可达"的请求。
type Request struct {
	// Port 是要对外开放的端口。
	Port int
	// Protocol 是 "tcp" 或 "udp"。
	Protocol string
	// Label 是规则的可读名称，便于用户在防火墙界面里认出它。
	Label string
	// UpstreamPort 是上游端口；为 0 表示与 Port 相同。
	//
	// 非标端口入口（例如 8443 → 443）会用到它。
	UpstreamPort int
}

// Provider 是一种可达方式。
type Provider interface {
	// Meta 返回元信息。
	Meta() Meta
	// Probe 检测本机当前是否具备使用这种方式的条件。
	//
	// 它必须是**只读**的：用户点"检查"时不该产生任何系统变更。
	Probe(ctx context.Context) (Readiness, error)
	// Plan 生成让服务可达所需的变更计划。
	//
	// 返回一个空计划是合法的，表示"无需改动"（例如规则已存在）。
	// 返回错误表示这种方式当前不可用 —— 错误信息必须说明**为什么**，
	// 因为它是用户看到的唯一线索。
	Plan(ctx context.Context, req Request) (change.Plan, error)
}
