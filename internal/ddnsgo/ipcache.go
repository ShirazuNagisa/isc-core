package ddnsgo

import "sync/atomic"

// IpCache 是地址缓存与防抖计数器，移植自 ddns-go 的 util/ip_cache.go。
//
// # 为什么需要它
//
// 定时任务的周期可能很短（例如 10 秒），而每次都去问服务商"记录对不对"
// 既慢又会触发限流。IpCache 的语义是：
//
//   - 地址变了 → 立刻去比对（这是必须响应的）；
//   - 地址没变 → 攒够 N 次才比对一次（避免无谓的 API 调用）。
//
// 上游用环境变量 DDNS_IP_CACHE_TIMES 控制 N。这里换成显式的 SetCacheTimes ——
// 环境变量是隐式配置：写在服务定义里的值与写在别处的值哪个生效，
// 排查时很难判断；而且它没有校验，填个负数会让行为变得莫名其妙。
//
// 另外上游有一个包级开关 util.ForceCompareGlobal，用来在"用户刚保存配置"
// 时强制比对一次。它被 dns/index.go 在运行期反复改写 —— 那正是本项目
// 明令禁止的"参与逻辑判断的全局可变状态"（见 docs/PLAN.md R11）。
// ISC 用更直接的方式达到同样效果：保存配置后由调度器显式跑一次。
// 因此本包**没有**这个开关。

// defaultCacheTimes 是防抖次数的默认值。
//
// 5 次与上游默认一致：按默认 5 分钟周期算，就是每 25 分钟才与服务商
// 比对一次没变化的地址。这是个经过验证的平衡点。
const defaultCacheTimes = 5

// cacheTimes 由 SetCacheTimes 在启动时写入，之后只读。
//
// 用 atomic 而不是普通变量：定时任务在多个 goroutine 上跑，
// 而配置更新可能随时发生。
var cacheTimes atomic.Int64

func init() { cacheTimes.Store(defaultCacheTimes) }

// SetCacheTimes 设置"地址未变化时，间隔多少次才与服务商比对"。
//
// n < 1 时忽略（而不是夹到 1）：静默改掉用户填的值会让"我明明设了 0
// 为什么还在请求"变成一个查不明白的问题。
func SetCacheTimes(n int) {
	if n >= 1 {
		cacheTimes.Store(int64(n))
	}
}

// CurrentCacheTimes 返回当前的防抖次数。
func CurrentCacheTimes() int { return int(cacheTimes.Load()) }

// IpCache 上次 IP 缓存。
type IpCache struct {
	// Addr 是上次见到的地址。
	Addr string
	// Times 是距离下次比对还剩几次。
	Times int
	// TimesFailedIP 是连续获取 IP 失败的次数。
	//
	// 上游用它做"失败 3 次才报一次"的抑制，避免网络抖动导致告警风暴。
	TimesFailedIP int
}

// Check 判断是否需要去服务商那边比对一次。
//
// 返回 true 表示"该去比对了"（地址变了，或者攒够了次数）。
// 语义与上游逐行一致 —— 这段逻辑的正确性完全体现在边界上：
// Times <= 1 而不是 == 0，保证第 N 次一定触发。
func (d *IpCache) Check(newAddr string) bool {
	if newAddr == "" {
		return true
	}

	// 地址改变 或 达到剩余次数
	if d.Addr != newAddr || d.Times <= 1 {
		d.Addr = newAddr
		d.Times = CurrentCacheTimes() + 1
		return true
	}
	d.Addr = newAddr
	d.Times--
	return false
}

// Reset 清空缓存，使下一次 Check 必定返回 true。
//
// 用于"用户刚改了配置"或"上次更新失败需要立刻重试"的场景，
// 替代上游那个被到处改写的全局开关。
func (d *IpCache) Reset() {
	d.Addr = ""
	d.Times = 0
}
