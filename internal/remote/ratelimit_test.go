package remote

import (
	"fmt"
	"net"
	"testing"
	"time"
)

// 公网暴露之后的限流。
//
// # 这里的两条断言都是"不这么做就会被玩坏"的地方
//
//  1. **IPv6 必须按 /64 聚合。** 按 /128 限流等于没限流：一个住宅用户
//     拿到的是一个 /64，攻击者可以在自己的机器上随意换源地址
//     （不需要任何特权），每个地址一个桶。
//  2. **锁定必须按来源。** 全局锁定在局域网上是对的，在公网上是
//     拒绝服务：任何路人输错十次就能把真正的用户锁在门外十五分钟，
//     而且可以一直锁下去。

// ---------------------------------------------------------------------------
// 来源归一
// ---------------------------------------------------------------------------

func TestSourceKeyAggregatesIPv6ToSlash64(t *testing.T) {
	t.Parallel()

	// 同一个 /64 下的任意地址必须落到同一个键上 ——
	// 否则攻击者换一个源地址就换一个额度。
	variants := []string{
		"2409:8a50:6a1:7450::1",
		"2409:8a50:6a1:7450::560",
		"2409:8a50:6a1:7450:1847:3ee8:1edc:d3c9",
		"2409:8a50:6a1:7450:ffff:ffff:ffff:ffff",
	}
	first := SourceKey(variants[0])
	for _, v := range variants {
		if got := SourceKey(v); got != first {
			t.Fatalf("%s 归一成了 %q，而 %s 是 %q —— 同一个 /64 必须共用一个额度",
				v, got, variants[0], first)
		}
	}
	if first != "2409:8a50:6a1:7450::/64" {
		t.Fatalf("键的形状不对: %q", first)
	}

	// 不同的 /64 必须是不同的键：把整个 /48 当成一个来源会误伤
	// 同网段的其它用户。
	other := SourceKey("2409:8a50:6a1:7451::1")
	if other == first {
		t.Fatal("相邻的 /64 不该共用一个额度")
	}
}

func TestSourceKeyKeepsIPv4Exact(t *testing.T) {
	t.Parallel()

	// IPv4 上一个用户就是一个地址，不需要聚合。
	if got := SourceKey("192.168.31.92"); got != "192.168.31.92" {
		t.Fatalf("IPv4 键 = %q", got)
	}
	// 两个相邻的 IPv4 必须是不同的键。
	if SourceKey("1.2.3.4") == SourceKey("1.2.3.5") {
		t.Fatal("相邻的 IPv4 不该共用一个额度")
	}
	// IPv4 映射形式要归一成同一个。
	if SourceKey("::ffff:1.2.3.4") != SourceKey("1.2.3.4") {
		t.Fatal("IPv4 映射地址应当与纯 IPv4 归一")
	}
}

func TestSourceKeyPassesThroughUnparseable(t *testing.T) {
	t.Parallel()

	// 解析不出来时保留原样：宁可限流失效，也不要把不同的来源
	// 映射到同一个键上而互相影响。
	for _, raw := range []string{"not-an-ip", "", "10.0.0.1:1234"} {
		if got := SourceKey(raw); got != raw {
			t.Fatalf("SourceKey(%q) = %q，期望原样返回", raw, got)
		}
	}
}

// ---------------------------------------------------------------------------
// 桶表有界
// ---------------------------------------------------------------------------

// 表满之后**绝不放行**。
//
// 这是"表满就跳过限流"这种实现的致命处：攻击者用一堆新来源把表撑满，
// 之后所有请求都不受限。溢出桶让"表满"这个状态本身成为共享限额 ——
// 攻击者把自己关进了同一间屋子。
func TestLimiterNeverBypassesWhenTableIsFull(t *testing.T) {
	t.Parallel()

	l := newLimiter(60, 5)
	start := time.Unix(1_800_000_000, 0)

	// 用不同的来源把表撑满。
	for i := 0; i < maxBuckets; i++ {
		if !l.allow(fmt.Sprintf("src-%d", i), start) {
			t.Fatalf("第 %d 个来源在装满自己的桶时就该放行", i)
		}
	}
	if len(l.buckets) != maxBuckets {
		t.Fatalf("桶表大小 = %d，期望 %d", len(l.buckets), maxBuckets)
	}

	// 现在再来的新来源只能共用溢出桶。
	allowed := 0
	for i := 0; i < 100; i++ {
		if l.allow(fmt.Sprintf("flood-%d", i), start) {
			allowed++
		}
	}
	// 溢出桶的容量就是 burst。
	if allowed > 5 {
		t.Fatalf("溢出桶放行了 %d 次，超过容量 5 —— 表满成了一条绕过限流的路", allowed)
	}
	if allowed == 0 {
		t.Fatal("溢出桶一个都不放行也不对：那等于表满之后所有新用户都用不了")
	}
}

// 空闲的桶会被清掉，因此正常运行下不会有内存增长。
func TestLimiterSweepsIdleBuckets(t *testing.T) {
	t.Parallel()

	l := newLimiter(60, 5)
	start := time.Unix(1_800_000_000, 0)
	for i := 0; i < 100; i++ {
		l.allow(fmt.Sprintf("src-%d", i), start)
	}
	if len(l.buckets) != 100 {
		t.Fatalf("桶数 = %d", len(l.buckets))
	}

	// 跳过清理间隔之后再来一次。
	later := start.Add(bucketIdleTTL + bucketSweepGap + time.Minute)
	l.allow("fresh", later)

	if len(l.buckets) > 2 {
		t.Fatalf("空闲桶没有被清掉，剩 %d 个", len(l.buckets))
	}
}

// ---------------------------------------------------------------------------
// 按来源锁定
// ---------------------------------------------------------------------------

// **这条是公网暴露之后最关键的一条。**
//
// 全局锁定在局域网上是对的（来源就那么几个，锁了就是锁了），
// 在公网上是拒绝服务：任何一个路人反复输错十次，就能把真正的用户
// 锁在门外十五分钟，而且可以一直锁下去。
func TestLockoutIsScopedToTheSource(t *testing.T) {
	t.Parallel()

	m := newPairingManager()
	now := time.Unix(1_800_000_000, 0)
	const attacker, victim = "203.0.113.9/64", "198.51.100.7/64"

	// 攻击者连错到被锁。
	//
	// 注意每一轮都要**先确保有会话**：单会话 5 次失败就销毁，
	// 因此要凑够 10 次跨会话失败，必须不断重开 —— 那正是攻击者
	// 会做的事，也正是跨会话计数存在的理由。
	for i := 0; i < pairingLockThreshold; i++ {
		if _, err := m.start(RoleViewer, "", attacker, now); err != nil && err != ErrPairingConflict {
			t.Fatalf("第 %d 轮开会话失败: %v", i, err)
		}
		if _, err := m.claim("ZZZZZZ", attacker, now); err != ErrPairingMismatch {
			t.Fatalf("第 %d 轮期望 mismatch，得到 %v", i, err)
		}
	}
	if !m.lockedFrom(attacker, now) {
		t.Fatal("反复猜错的来源应当被锁")
	}

	// 受害者不受影响 —— 这正是"按来源"要买到的东西。
	if m.lockedFrom(victim, now) {
		t.Fatal("一个来源被锁不该牵连另一个来源（那就是拒绝服务）")
	}
	if _, err := m.start(RoleViewer, "", victim, now); err != nil {
		t.Fatalf("受害者应当还能开配对会话: %v", err)
	}
}

// 锁到期之后必须能重新开始，而不是"锁到世界末日"。
//
// 这一条容易写错的地方是 `time.Time{}` 的零值：`now.After(零值)`
// 永远为真，于是"锁到期就清零"会在每一次失败上触发，
// 计数器永远到不了阈值 —— 锁定静默失效。
func TestLockoutExpiresAndCounterResets(t *testing.T) {
	t.Parallel()

	m := newPairingManager()
	now := time.Unix(1_800_000_000, 0)
	const source = "203.0.113.9/64"

	for i := 0; i < pairingLockThreshold; i++ {
		if _, err := m.start(RoleViewer, "", source, now); err != nil && err != ErrPairingConflict {
			t.Fatalf("第 %d 轮开会话失败: %v", i, err)
		}
		_, _ = m.claim("ZZZZZZ", source, now)
	}
	if !m.lockedFrom(source, now) {
		t.Fatal("应当被锁")
	}

	later := now.Add(pairingLockDuration + time.Minute)
	if m.lockedFrom(source, later) {
		t.Fatal("锁到期之后不该还是锁着的")
	}
	// 到期后计数必须清零，否则下一次失败立刻重新锁上。
	entry := m.failures[source]
	if entry.count != 0 {
		t.Fatalf("锁到期后计数 = %d，期望 0", entry.count)
	}
	if _, err := m.start(RoleViewer, "", source, later); err != nil {
		t.Fatalf("锁到期之后应当能开新会话: %v", err)
	}
}

// 失败计数真的会累积到阈值 —— 钉住那个"零值把它清零"的 bug。
func TestFailureCounterReachesThreshold(t *testing.T) {
	t.Parallel()

	m := newPairingManager()
	now := time.Unix(1_800_000_000, 0)
	const source = "203.0.113.9/64"

	for i := 0; i < pairingLockThreshold; i++ {
		m.mu.Lock()
		m.recordFailureLocked(source, now)
		m.mu.Unlock()
	}
	if !m.lockedFrom(source, now) {
		t.Fatal("连续失败到阈值之后必须锁定。若这里失败，多半是 time.Time{} 的零值把计数清零了")
	}
}

// 配对成功要清掉该来源的失败计数。
//
// 一个人输错两次之后终于输对，不该因为那两次而离锁定更近。
func TestSuccessfulClaimClearsFailures(t *testing.T) {
	t.Parallel()

	m := newPairingManager()
	now := time.Unix(1_800_000_000, 0)
	const source = "192.168.31.92"

	session, err := m.start(RoleViewer, "", source, now)
	if err != nil {
		t.Fatalf("开会话失败: %v", err)
	}
	// 先错几次（但没到销毁会话的程度）。
	for i := 0; i < 2; i++ {
		if _, err := m.claim("ZZZZZZ", source, now); err == nil {
			t.Fatal("错的码不该认领成功")
		}
	}
	if _, err := m.claim(session.Secret, source, now); err != nil {
		t.Fatalf("正确的码应当认领成功: %v", err)
	}
	// 成功后重开一个会话继续（原来的已经被消费掉了）。
	if _, err := m.start(RoleViewer, "", source, now); err != nil {
		t.Fatalf("失败: %v", err)
	}
	if entry := m.failures[source]; entry != nil && entry.count != 0 {
		t.Fatalf("成功之后失败计数 = %d，期望 0", entry.count)
	}
}

// 来源表也有上限，被刷时不会无界增长。
func TestFailureTableIsBounded(t *testing.T) {
	t.Parallel()

	m := newPairingManager()
	now := time.Unix(1_800_000_000, 0)
	for i := 0; i < maxTrackedSources+500; i++ {
		m.mu.Lock()
		m.recordFailureLocked(fmt.Sprintf("src-%d", i), now)
		m.mu.Unlock()
	}
	if len(m.failures) > maxTrackedSources {
		t.Fatalf("来源表大小 = %d，超过上限 %d", len(m.failures), maxTrackedSources)
	}
}

// 来源归一之后，同一台机器换 IPv6 地址**不会**绕过限流。
//
// 这是 IPv6 下最容易被忽略的一条：`ip -6 addr add` 不需要任何特权。
func TestIPv6AddressRotationSharesOneBucket(t *testing.T) {
	t.Parallel()

	l := newLimiter(60, 5)
	start := time.Unix(1_800_000_000, 0)

	allowed := 0
	for i := 0; i < 100; i++ {
		// 每次换一个源地址，但都在同一个 /64 里。
		addr := net.ParseIP(fmt.Sprintf("2409:8a50:6a1:7450::%x", i)).String()
		if l.allow(SourceKey(addr), start) {
			allowed++
		}
	}
	if allowed > 5 {
		t.Fatalf("换源地址绕过了限流：放行 %d 次，而突发额度是 5", allowed)
	}
}

// 探针端点必须有**自己**的额度，不能与配对共用。
//
// 共用一个桶时两个方向都是坏的：
//
//   - 攻击者狂打 ping 就能把正常用户的配对额度耗光 ——
//     那是一条**不用配对就能发动**的拒绝服务。
//   - 手机做一次可达性自检要试好几个地址，而那不该让它离
//     "配对被限流"更近。
func TestPingBudgetIsSeparateFromPairing(t *testing.T) {
	t.Parallel()

	svc, err := New(Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	now := time.Unix(1_800_000_000, 0)
	const source = "203.0.113.9"

	// 把探针额度打光。
	for i := 0; i < pingBurst; i++ {
		if !svc.AllowPing(source, now) {
			t.Fatalf("第 %d 次探针就该放行", i+1)
		}
	}
	if svc.AllowPing(source, now) {
		t.Fatal("探针额度用光之后应当拒绝")
	}

	// **配对额度不受影响。** 这一条就是这条测试的全部意义。
	if !svc.AllowPair(source, now) {
		t.Fatal("探针把配对额度挤掉了 —— 那是一條不用配对就能发动的拒绝服务")
	}

	// 反过来也要成立：配对打光不该影响探针。
	other := "198.51.100.7"
	for i := 0; i < pairBurst; i++ {
		svc.AllowPair(other, now)
	}
	if svc.AllowPair(other, now) {
		t.Fatal("配对额度应当用光了")
	}
	if !svc.AllowPing(other, now) {
		t.Fatal("配对额度用光不该影响探针")
	}
}
