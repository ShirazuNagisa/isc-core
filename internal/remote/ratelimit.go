package remote

import (
	"net"
	"strings"
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

// SourceKey 把一个来源地址压成**限流用的键**。
//
// # 为什么不能直接用地址本身
//
// IPv4 上一个攻击者只有一个地址，因此按地址限流是有效的。IPv6 上不是：
// 一个住宅用户拿到的是一个 **/64**，也就是 2^64 个可用地址。攻击者
// 在自己的机器上随意换源地址（那不需要任何特权，Linux 上 `ip -6 addr add`
// 就行），每一个都得到一个独立的桶 ——
// **限流形同虚设，而内存先炸。**
//
// 因此 IPv6 按 **/64 前缀**聚合：那是运营商分给一个用户的粒度，
// 也是"一个人"在 IPv6 世界里最接近的对应物。
//
// /128 以下的前缀（/48、/56 之类）不聚合：那些是分给一个**网络**的，
// 把整个网络当成一个来源会误伤同网段的其它用户。
func SourceKey(source string) string {
	ip := net.ParseIP(strings.TrimSpace(source))
	if ip == nil {
		// 解析不出来时保留原样：宁可限流失效，也不要把不同的来源
		// 映射到同一个键上而互相影响。
		return source
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	// IPv6：取 /64 前缀，零化后 64 位。
	masked := ip.Mask(net.CIDRMask(64, 128))
	if masked == nil {
		return ip.String()
	}
	return masked.String() + "/64"
}

// limiter 是一个按 key 分桶的令牌桶限流器。
//
// key 是来源 IP（经 SourceKey 归一）或设备 ID。桶是懒创建的。
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

	// overflow 是表满之后新来源共用的桶。
	//
	// 为 nil 表示还没建。它存在、而不是"表满就放行"，是因为后者
	// 恰好是攻击者想要的：用一堆新来源把表撑满，之后所有请求都不受限。
	overflow *bucket
}

// maxBuckets 是桶表的上限。
//
// 4096 个键：正常部署里来源数远小于它（局域网几十个，公网上一个
// 攻击者经 SourceKey 聚合之后也只占几个）。上限的作用是在被刷时
// 把内存**钉死**，而不是让它随攻击规模增长。
const maxBuckets = 4096

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

	l.sweepLocked(now, false)

	b := l.bucketLocked(key, now)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// bucketLocked 取出该 key 的桶，必要时新建。调用方必须已持有 l.mu。
//
// 表满时**不新建**，而是把请求记到共享的溢出桶上。
//
// 这是必须的：IPv6 下一个攻击者能轻易拿出几百万个 /64 以外的源
// （更别说伪造的 IPv4 源由反向路径过滤挡住之前，任何来源都能自称），
// 给每个都开一个桶，内存先炸，而限流形同虚设。溢出桶让"表满"
// 这个状态本身成为**共享的**限额 —— 攻击者把自己关进了同一间屋子。
//
// 代价是：在被刷的时候，新来的合法用户也会被溢出桶限住。那是对的
// 失败方向 —— 宁可让新用户重试，也不要让限流失效。
func (l *limiter) bucketLocked(key string, now time.Time) *bucket {
	if b, ok := l.buckets[key]; ok {
		l.refillLocked(b, now)
		return b
	}

	if len(l.buckets) >= maxBuckets {
		// 先认真清一次（可能有大量空闲桶），清不出来才用溢出桶。
		l.sweepLocked(now, true)
		if len(l.buckets) >= maxBuckets {
			if l.overflow == nil {
				l.overflow = &bucket{tokens: l.burst, last: now}
			}
			l.refillLocked(l.overflow, now)
			return l.overflow
		}
	}

	// 新来源：装满一桶。桶的容量就是它一次能连发多少个请求。
	b := &bucket{tokens: l.burst, last: now}
	l.buckets[key] = b
	return b
}

// refillLocked 按经过的时间补充令牌。调用方必须已持有 l.mu。
func (l *limiter) refillLocked(b *bucket, now time.Time) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed <= 0 {
		return
	}
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
}

// sweepLocked 丢掉长时间没有活动的桶。
//
// 调用方必须已持有 l.mu。force 为真时忽略清理间隔 —— 表满时
// 值得立刻认真清一次，而那正是它最需要发生的时候。
func (l *limiter) sweepLocked(now time.Time, force bool) {
	if !force && now.Sub(l.lastSweep) < bucketSweepGap {
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

	// pingPerMinute / pingBurst 是**探针端点**的额度。
	//
	// 它必须与配对分开。共用一个桶时两边会互相挤占，而两个方向都是坏的：
	//
	//   - 攻击者狂打 ping，就能把正常用户的配对额度耗光 ——
	//     那是一条不用配对就能发动的拒绝服务。
	//   - 手机做一次可达性自检要试好几个地址，可能连着打几次 ping，
	//     而那不该让它离"配对被限流"更近。
	//
	// 60 次/分：探针是幂等的、返回体只有 `{"ok":true}`，
	// 因此可以比配对宽松得多。
	pingPerMinute = 60
	pingBurst     = 20

	// devicePerMinute / deviceBurst 是已鉴权设备的额度。
	//
	// 60 次/分：手机前台是 5 秒一次指标 + 一条挂着 25 秒的长轮询，
	// 也就是每分钟十几到二十几个请求。留三倍余量，同时保证一台
	// 失控的客户端不会把内核拖垮。
	devicePerMinute = 60
	deviceBurst     = 30
)
