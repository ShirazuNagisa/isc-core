package reach

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// 本文件是**端口层的诊断**：在用户开一条防火墙规则之前，先看看那个端口上
// 到底有没有东西在听、以及本机有没有权限绑它。
//
// # 为什么需要它
//
// 只做"开端口"而不做这两项检查，会产生两种最难排查的失败：
//
//	端口上没有服务
//	  防火墙规则开好了，用户从外面访问却什么都不通。他会去查路由器、
//	  查运营商、查 DNS —— 而真正的原因是本机上根本没有服务在监听。
//	  （他可能只是忘了启动它，或者它崩溃了。）
//
//	没有权限绑低端口
//	  Linux 上非 root 进程绑不了 <1024 端口。用户在反代里配了 443、
//	  界面上显示"已启用"，而后台每次启动都失败。他会以为是自己
//	  配置错了，而不是权限不够。
//
// 两项都是**预警而不是阻断**：用户可能正要启动那个服务，也可能
// 打算用 setcap 提权。因此它们产出 CheckWarn / CheckFail 而不是错误。

// PortCheck 是一次端口诊断的结果。
type PortCheck struct {
	// Listening 表示探测时该端口上是否有服务在监听。
	Listening bool
	// Detail 是观察到的事实。
	Detail string
}

// listeningTimeout 是单次连接的探测超时。
//
// 很短：本机回环上的连接要么立刻成功、要么立刻被拒，而"超时"通常
// 意味着防火墙把包丢了 —— 那也是一种有用的信息，但它不该让用户等。
const listeningTimeout = 700 * time.Millisecond

// CheckListening 探测本机某端口上是否有服务在监听。
//
// # 为什么用"连一下"而不是读 /proc/net/tcp
//
// 读内核的连接表看起来更"干净"，但：
//
//   - 各平台的文件位置与格式完全不同（/proc、sysctl、GetExtendedTcpTable），
//     要写三份实现；
//   - 它回答的是"内核里有没有这条记录"，而不是"客户端能不能连上"。
//     而后者才是用户关心的问题。
//
// 连一下是**所有平台通用的**，而且它测的正是真实路径。
//
// # 这是一个有副作用的探测
//
// 连接会在目标服务的 accept 队列里留下一条记录，某些服务会为此记一行
// 日志。这是这个方法的代价，而它换来的是跨平台的一致性与准确性。
// 因为如此，探测只在用户**主动点检查或生成计划**时发生，而不是定时轮询。
func CheckListening(ctx context.Context, port int, protocol string) PortCheck {
	if port <= 0 || port > 65535 {
		return PortCheck{Detail: fmt.Sprintf("端口 %d 不在合法范围内", port)}
	}
	if protocol == "" {
		protocol = "tcp"
	}

	// UDP 没有"连接"的概念，因此探测方式完全不同。
	//
	// 这里**不猜**：报告"无法探测"比给一个可能错的结论好 ——
	// 用户会据此去查那个端口，而如果结论是错的，他查的是错的地方。
	if protocol != "tcp" {
		return PortCheck{
			Detail: fmt.Sprintf("%s 端口无法用连接探测（UDP 没有连接的概念）",
				strings.ToUpper(protocol)),
		}
	}

	// 先试 IPv4 回环，再试 IPv6 回环。
	//
	// 两个都要试：服务可能只绑了其中一个。只试 IPv4 会把一个
	// "只监听 ::1" 的服务报成"没有服务"，而那是个假警报。
	for _, addr := range []string{
		fmt.Sprintf("127.0.0.1:%d", port),
		fmt.Sprintf("[::1]:%d", port),
	} {
		dialCtx, cancel := context.WithTimeout(ctx, listeningTimeout)
		var d net.Dialer
		conn, err := d.DialContext(dialCtx, "tcp", addr)
		cancel()

		if err == nil {
			_ = conn.Close()
			return PortCheck{
				Listening: true,
				Detail:    fmt.Sprintf("端口 %d 上有服务在监听", port),
			}
		}

		// 上下文被取消（用户中断）时不该继续试下一个地址。
		if ctx.Err() != nil {
			return PortCheck{Detail: "探测被中断"}
		}
	}

	return PortCheck{
		Detail: fmt.Sprintf("端口 %d 上没有服务在监听", port),
	}
}

// DiagnosePort 产出端口相关的检测项。
//
// binder 用于判断低端口权限；传 nil 时跳过那一项。
func DiagnosePort(ctx context.Context, req Request,
	binder platform.LowPortBinder) []Check {

	if req.Port <= 0 || req.Port > 65535 {
		return []Check{{
			Name:   "端口范围",
			Scope:  ScopeLocal,
			Status: CheckFail,
			Detail: fmt.Sprintf("端口 %d 不在 1-65535 范围内", req.Port),
			Hint:   "请填一个合法端口。",
		}}
	}

	var checks []Check
	checks = append(checks, diagnoseListening(ctx, req))
	checks = append(checks, diagnoseUpstream(req))

	if binder != nil {
		checks = append(checks, diagnoseLowPort(req, binder))
	}
	return checks
}

// diagnoseListening 检查入口端口上是否有服务。
func diagnoseListening(ctx context.Context, req Request) Check {
	res := CheckListening(ctx, req.Port, req.Protocol)

	if res.Listening {
		return Check{
			Name:   "服务监听",
			Scope:  ScopeLocal,
			Status: CheckPass,
			Detail: res.Detail,
		}
	}

	// UDP 的情况特殊：探测不出来不代表没有服务。
	//
	// 提示里**不再解释一遍为什么探测不了** —— Detail 已经说过了，
	// 重复只会让警告变长而信息量不变。
	if req.Protocol != "" && req.Protocol != "tcp" {
		return Check{
			Name:   "服务监听",
			Scope:  ScopeLocal,
			Status: CheckWarn,
			Detail: res.Detail,
			Hint:   "请自行确认该端口上的服务已启动。",
		}
	}

	// **警告而不是失败**：用户可能正要启动那个服务。
	//
	// 做成失败会拦住一个完全合理的操作顺序（先开规则、再起服务），
	// 而那个顺序在"先把网络配好再部署服务"的流程里很自然。
	return Check{
		Name:   "服务监听",
		Scope:  ScopeLocal,
		Status: CheckWarn,
		Detail: res.Detail,
		// 提示只说**后果与下一步**，不重复 Detail 已经说过的事实。
		//
		// 早先的版本在这里又写了一遍"先确认本机有服务在监听 18099"，
		// 而 Detail 刚刚说过同一件事 —— 拼接之后读起来是
		// "…没有服务在监听 开放这个端口之前，先确认本机有服务在监听 18099…"，
		// 既重复又拗口。这是真机上跑出来才看出来的。
		Hint: fmt.Sprintf(
			"规则开好了但本机没有服务，从外面访问仍然什么都不通，" +
				"而那时很难想到问题出在本机上。" +
				"如果服务还没启动，请先启动它；如果只是还没部署，可以先放行。"),
	}
}

// diagnoseUpstream 检查端口映射是否合理。
func diagnoseUpstream(req Request) Check {
	upstream := req.UpstreamPort
	if upstream == 0 {
		upstream = req.Port
	}

	if upstream == req.Port {
		return Check{
			Name:   "端口映射",
			Scope:  ScopeLocal,
			Status: CheckPass,
			Detail: fmt.Sprintf("外部 %d → 内部 %d（直通）", req.Port, upstream),
		}
	}
	return Check{
		Name:   "端口映射",
		Scope:  ScopeLocal,
		Status: CheckPass,
		Detail: fmt.Sprintf("外部 %d → 内部 %d", req.Port, upstream),
		Hint: "非标端口入口：访问时要显式带端口号，" +
			"例如 https://example.com:8443",
	}
}

// diagnoseLowPort 检查本机能否绑定低端口。
//
// 只在 Linux 上可能失败：Windows 与 macOS 不限制普通进程绑低端口。
func diagnoseLowPort(req Request, binder platform.LowPortBinder) Check {
	const lowPortLimit = 1024

	// 只管入口端口：内部端口是本机服务自己的事。
	if req.Port >= lowPortLimit {
		return Check{
			Name:   "低端口权限",
			Scope:  ScopeLocal,
			Status: CheckPass,
			Detail: fmt.Sprintf("%d 不是特权端口，无需额外权限", req.Port),
		}
	}

	st := binder.Describe()
	if binder.CanBindLowPorts() {
		return Check{
			Name:   "低端口权限",
			Scope:  ScopeLocal,
			Status: CheckPass,
			Detail: fmt.Sprintf("可以绑定 %d（%s）", req.Port, st.Backend),
		}
	}

	// 这是**失败**而不是警告：没有权限就一定绑不上，
	// 而重试、等一会儿、改配置都不会让它变好。
	return Check{
		Name:   "低端口权限",
		Scope:  ScopeLocal,
		Status: CheckFail,
		Detail: st.Note,
		Hint: fmt.Sprintf(
			"绑定 %d 需要额外权限。三种做法：\n"+
				"  · 换一个 ≥1024 的端口（最简单，代价是访问时要带端口号）\n"+
				"  · 给可执行文件授权：sudo setcap 'cap_net_bind_service=+ep' /path/to/isc\n"+
				"  · 以 root 运行内核",
			req.Port),
	}
}

// ErrNoListener 表示端口上没有服务在监听。
//
// 它存在是为了让调用方能区分"探测失败"与"确实没有服务"。
var ErrNoListener = errors.New("reach: 该端口上没有服务在监听")
