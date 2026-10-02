package verify

import (
	"time"
)

// 本文件把外部验证的结论**回喂给可达性检查**。
//
// # 它闭合的那条链路
//
// M3 的验收标准里写着「故意封掉端口后，isc doctor 能明确指出是运营商侧
// 封禁而非本机问题」。而在此之前那条链路是断的：
//
//	CheckBlocked 这个状态在 reach 包里定义了、注释齐全、
//	BlockingCheck 会处理它、doctor 里有完整的「上游挡住了」结论分支 ——
//	**但没有任何代码会设置它。**
//
// 于是那个分支永远不会被走到，而用户拿到的永远是那句
//「本机自测通过不等于外网能连上，请用手机确认」。那句话是对的，
// 但它把判断的责任完全推给了用户 —— 而系统其实**有能力**判断：
//
//	isc verify 建立了一个临时的外部可达性探测点
//	如果没有收到任何来自公网的访问，那就是上游挡住了
//
// # 为什么需要"回喂"而不是各测各的
//
// verify 是**主动探测**（用户点一下，等手机来访问），而 reach 的
// 上游检查是**被动报告**（本机无法自测）。两者是同一件事的两个视角，
// 而把它们连起来的唯一障碍是：verify 的结论此前只存在于那个临时会话里，
// 会话过期就没了。

// Verdict 是一次外部验证留下**持久结论**。
//
// 与 Session 分开：会话是短命的（默认十分钟就过期），而结论
// 应当比会话活得久 —— 用户验过一次之后再跑 doctor，应当还能看到
// 那个结论，而不是回到"未知"。
type Verdict struct {
	// Reachable 为真表示确实收到了来自公网的访问。
	Reachable bool
	// Blocked 为真表示探测做完了、但**一次公网访问都没收到**。
	//
	// 这是"上游挡住了"的证据 —— 而它与"没验过"是两回事。
	Blocked bool
	// Port 是这次验证用的端口。
	Port int
	// At 是结论形成的时间。
	At time.Time
	// Detail 是一句话说明。
	Detail string
}

// SetVerdictSink 设置结论的落点。
//
// 做成回调而不是让 verify 直接依赖 reach：两者的依赖方向应当是
// "上层把它们接起来"，而不是"验证模块去认识可达性模块"。
// 那样任何一个模块都无法单独测试。
func (m *Manager) SetVerdictSink(fn func(Verdict)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verdictSink = fn
}

// VerdictTTL 是结论的有效期。
//
// 它比会话的 TTL 长得多，但**不能无限**：用户今天验出来"被运营商封了"，
// 下周换了宽带或运营商改了策略之后，那个结论就不再成立了。
//
// 24 小时的依据：它足以让"验一次、然后反复跑 doctor"这个用法成立，
// 又短到不会让一个过期的结论长期误导人。
const VerdictTTL = 24 * time.Hour

// verdictFromSession 由会话算出结论。
//
// 只有**终态**才产生结论：
//
//	reachable      收到了公网访问 → 链路通
//	unreachable    超时且一次访问都没有 → 上游挡住了
//	hairpin_only   只收到本机/内网的访问 → **不产生结论**
//
// 最后一条是刻意的：只收到回环访问说明不了上游通不通（用户可能
// 只是用 WiFi 而不是 4G 打开的），把它当成"被封了"是一个**错误的
// 诊断**，而错误的诊断比没有诊断更糟。
func verdictFromSession(s Session) (Verdict, bool) {
	switch s.Status {
	case StatusReachable:
		return Verdict{
			Reachable: true,
			Port:      s.Port,
			At:        time.Now().UTC(),
			Detail:    "已收到来自公网的访问，链路确实可用",
		}, true

	case StatusUnreachable:
		return Verdict{
			Blocked: true,
			Port:    s.Port,
			At:      time.Now().UTC(),
			Detail: "外部验证超时，且一次公网访问都没有收到 —— " +
				"本机监听正常但流量没有到达，说明上游挡住了这个端口",
		}, true

	default:
		// pending / hairpin_only：都不足以得出结论。
		return Verdict{}, false
	}
}

// publishVerdict 在会话进入终态时把结论送出去。
//
// 调用方必须**已经持有 m.mu**（会话状态的变更都在锁内）。
func (m *Manager) publishVerdict(sess *Session) {
	if m.verdictSink == nil {
		return
	}
	v, ok := verdictFromSession(*sess)
	if !ok {
		return
	}
	// 同步调用：结论的落点只是一次内存写入，而异步化会让
	// "刚验完立刻跑 doctor"看到旧值。
	m.verdictSink(v)
}

// SetLastVerdict 记录最近一次外部验证的结论。
func (m *Manager) SetLastVerdict(v Verdict) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastVerdict = &v
}

// LastVerdict 返回最近一次外部验证的结论。
//
// 第二个返回值表示"有没有一个还新鲜的结论"。过期的结论会被丢弃 ——
// 见 VerdictTTL 的说明。
func (m *Manager) LastVerdict() (Verdict, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.lastVerdict == nil {
		return Verdict{}, false
	}
	if time.Since(m.lastVerdict.At) > VerdictTTL {
		// 过期就丢掉。留着它会让 doctor 给出一个基于上周情况的结论。
		m.lastVerdict = nil
		return Verdict{}, false
	}
	return *m.lastVerdict, true
}
