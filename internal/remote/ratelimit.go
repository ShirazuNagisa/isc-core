package remote

import (
	"sync"
	"time"
)

// 本文件是远程面的限流器。
//
// # 为什么限流是**必须**的，而不是"加固"
//
// 远程面上有一条免鉴权的路径（`POST /v1/remote/pair`），它上面挂着六位
// 配对码。没有限流时，六位码的 30 位熵在局域网上是可以被跑完的：一个
// 脚本每秒发几千个请求，五分钟的会话窗口里足够枚举一小半空间。
//
// 会话内的失败计数（pairing.go）解决的是"同一个码试几次"，跨会话计数
// 解决的是"开新码再试"，而这两条都需要**按来源**限流才有意义 ——
// 否则攻击者只要并发开多条连接，每次失败都会分散到不同的计数上。
//
// 令牌桶而不是固定窗口：固定窗口在边界上有两倍突发（窗口末尾一次、
// 下一窗口开头一次），而配对正是那种"每个窗口都用满"就能推进的攻击。

// limiter 是一个按 key 分桶的令牌桶限流器。
//
// key 是来源 IP 或设备 ID。桶是懒创建的：只有真的发起过请求的来源
// 才会占内存，而来源的数量在有界网络里本来就不大。
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	// rate 是每秒补充的令牌数；burst 是桶的容量。
	rate  float64
	burst float64

	// lastSweep 用于限制清理频率。
	//
	// 每次 allow 都遍历全表会让限流本身变成 O(来源数) 的热点，
	// 而清理并不需要那么及时。
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// 空闲桶的保留时长与清理间隔。
const (
	bucketIdleTTL  = 10 * time.Minute
	bucketSweepGap = time.Minute
)

// newLimiter 构造一个限流器。perMinute 是每分钟允许的请求数，
// burst 是允许的瞬时突发。
func newLimiter(perMinute, burst float64) *limiter {
	return &limiter{
		buckets: map[string]*bucket{},
		rate:    perMinute / 60,
		burst:   burst,
	}
}

// allow 报告该 key 现在是否可以放行。
//
// 时间由调用方传入，而不是内部取 time.Now()：这样"令牌怎么随时间补充"
// 这件事可以被完整地单元测试，而不是只能靠 sleep 去试探。
func (l *limiter) allow(key string, now time.Time) bool {
	if l == nil {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweepLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		// 新来源：装满一桶。桶的容量就是它一次能连发多少个请求。
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * l.rate
			if b.tokens > l.burst {
				b.tokens = l.burst
			}
			b.last = now
		}
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweepLocked 丢掉长时间没有活动的桶。
//
// 调用方必须已持有 l.mu。
func (l *limiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < bucketSweepGap {
		return
	}
	l.lastSweep = now
	for key, b := range l.buckets {
		if now.Sub(b.last) > bucketIdleTTL {
			delete(l.buckets, key)
		}
	}
}

// 限流参数。
const (
	// pairPerMinute / pairBurst 是配对路径的额度。
	//
	// 10 次/分：一个人重新配对时最坏也就试三五次；而这个值让
	// 枚举 30 位空间在时间上完全不可行。
	pairPerMinute = 10
	pairBurst     = 5

	// devicePerMinute / deviceBurst 是已鉴权设备的额度。
	//
	// 60 次/分：手机前台是 5 秒一次指标 + 一条挂着 25 秒的长轮询，
	// 也就是每分钟十几到二十几个请求。留三倍余量，同时保证一台
	// 失控的客户端不会把内核拖垮。
	devicePerMinute = 60
	deviceBurst     = 30
)
