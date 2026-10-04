package remote

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件实现配对会话。
//
// # 两条路径，同一张门
//
// 扫码路径带的是 256 位随机的 `secret`，手输路径带的是六位 `code`。
// 两者指向**同一个会话**，因为"用户选了哪条路"不该影响服务端的任何状态 ——
// 否则就会出现"生成了二维码于是手输码失效"这种没人能预料的耦合。
//
// # 六位码为什么够用
//
// 32^6 ≈ 1.07e9，约 30 位。单看熵它当然远不如 256 位，但配对码不是一段
// 长期秘密：它只活五分钟、只能成功一次、每次失败都计数。把这三条叠起来，
// 暴力枚举需要的机会数远超一个会话的生命周期，而全局锁定会在那之前
// 先把它掐掉（见 ratelimit.go）。
//
// # 为什么用 Crockford base32
//
// 字母表 `0123456789ABCDEFGHJKMNPQRSTVWXYZ` 去掉了 `I`/`L`/`O`/`U`，
// 并且**给了明确的纠错规则**：解码时 `I`/`L` 一律当 `1`、`O` 一律当 `0`。
// 这条规则是这类码能被读对的关键 —— "去掉易混字符"只解决了生成端，
// 而真实失败发生在用户把屏幕上的字符念错、再敲进另一个设备的时候。

// pairingCodeAlphabet 是手输配对码的字母表（Crockford base32）。
//
// 长度必须是 32：这样 `随机字节 % 32` 是均匀分布（256 能被 32 整除），
// 不需要拒绝采样。换字母表时如果长度不是 2 的幂，生成函数必须同步改。
const pairingCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// pairingCodeLength 是手输配对码的长度。
const pairingCodeLength = 6

// PairingTTL 是配对会话的有效期。
//
// 五分钟：足够"在电脑上点一下、拿起手机、找到它"，又不至于让一个走开去
// 接杯水的人留下一扇开着的门。过期是自动的，用户不需要记得关。
const PairingTTL = 5 * time.Minute

// pairingMaxFailures 是单个会话允许的失败次数，超过即销毁。
const pairingMaxFailures = 5

// 配对的失败原因。
var (
	// ErrPairingNone 表示当前没有进行中的会话。
	ErrPairingNone = errors.New("remote: no active pairing session")
	// ErrPairingConflict 表示已经有一个未过期的会话。
	ErrPairingConflict = errors.New("remote: pairing session already active")
	// ErrPairingMismatch 表示配对码不对。
	ErrPairingMismatch = errors.New("remote: pairing code mismatch")
	// ErrPairingLocked 表示配对已被临时锁定。
	ErrPairingLocked = errors.New("remote: pairing locked")
)

// PairingSession 是一次配对会话。
type PairingSession struct {
	ID    string
	Role  Role
	Label string

	// Secret 是二维码/配对链接里携带的高熵配对密钥（base64url）。
	//
	// 它是**唯一**的配对凭证。以前还有一条六位码的手输路径，已删除：
	// 六位码只有约 10 亿种可能，靠按来源锁定兜底，而它携带不了任何
	// 身份信息（所以那时必须让用户人工比对公钥指纹）。二维码与链接
	// 两条路都自带指纹，人工比对那一步因此也一并消失了。
	Secret string

	CreatedAt time.Time
	ExpiresAt time.Time
}

// Expired 报告会话是否已过期。
func (p PairingSession) Expired(now time.Time) bool { return !now.Before(p.ExpiresAt) }

// pairingEntry 是会话在表里的形态。
type pairingEntry struct {
	session  PairingSession
	failures int
}

// pairingManager 管理配对会话与锁定状态。
//
// 会话**不落库**：它最多活五分钟，进程重启后继续持有它没有任何意义
// （用户手上的二维码已经作废），而落库就多了一张需要清理的表。
type pairingManager struct {
	mu sync.Mutex
	// sessions 以 ID 为键。用 map 而不是单值，是为了让"取消旧会话、
	// 立刻建一个新的"这条路径不会因为时序而在两个请求之间互相踩。
	sessions map[string]*pairingEntry

	// failures 是**按来源**统计的跨会话连续失败次数。
	//
	// 单个会话限制 5 次是不够的：攻击者可以让每个会话只失败 4 次，
	// 然后等它过期再开一个新的 —— 那样单会话计数永远不触发。
	// 跨会话的计数才是真正拦住枚举的那一道。
	//
	// # 为什么必须是按来源，而不是一个全局计数
	//
	// 全局计数在局域网上是对的（来源就那么几个，锁了就是锁了）。
	// 一旦内核被暴露在公网上，它立刻变成**拒绝服务**：任何一个路人
	// 反复输错十次，就能把真正的用户锁在门外十五分钟，而且可以
	// 一直锁下去。
	//
	// 按来源之后，攻击者只能把自己锁住。
	//
	// 那"分布式枚举"怎么办？靠的是**别处**已经有的那道闸：
	// 任何一个来源的失败都会销毁当前会话（见 recordFailureLocked），
	// 因此无论多少个来源同时猜，一个会话总共只接受 5 次猜测 ——
	// 而 5 / 32^6 约等于 1e-8。会话销毁与会话计数都是全局的，
	// 而它们才是真正决定枚举成本的东西。
	failures map[string]*sourceFailures
}

// sourceFailures 是一个来源的失败计数与锁定时间。
type sourceFailures struct {
	count       int
	lockedUntil time.Time
	last        time.Time
}

func newPairingManager() *pairingManager {
	return &pairingManager{
		sessions: map[string]*pairingEntry{},
		failures: map[string]*sourceFailures{},
	}
}

// start 开一个新会话。已有未过期的会话时返回 ErrPairingConflict。
func (m *pairingManager) start(role Role, label, source string, now time.Time) (PairingSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.lockedFrom(source, now) {
		return PairingSession{}, ErrPairingLocked
	}
	m.expireLocked(now)
	if len(m.sessions) > 0 {
		return PairingSession{}, ErrPairingConflict
	}

	id, err := randomID(12)
	if err != nil {
		return PairingSession{}, err
	}
	secret, err := randomSecret()
	if err != nil {
		return PairingSession{}, err
	}
	session := PairingSession{
		ID:        id,
		Role:      role,
		Label:     label,
		Secret:    secret,
		CreatedAt: now,
		ExpiresAt: now.Add(PairingTTL),
	}
	m.sessions[id] = &pairingEntry{session: session}
	return session, nil
}

// cancel 取消一个会话。返回是否真的存在并处于有效期内。
func (m *pairingManager) cancel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.sessions[id]; !ok {
		return false
	}
	delete(m.sessions, id)
	return true
}

// current 返回当前有效的会话（没有则返回零值与 false）。
func (m *pairingManager) current(now time.Time) (PairingSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.expireLocked(now)
	for _, entry := range m.sessions {
		return entry.session, true
	}
	return PairingSession{}, false
}

// locked 报告配对是否处于锁定状态。
//
// 调用方必须已持有 m.mu。
// lockedFrom 报告某个来源现在是否被锁。
//
// 注意 `lockedUntil` 的**零值**要单独判：`time.Time{}` 是公元 1 年，
// 而 `now.After(零值)` 永远为真 —— 少了这个判断，"锁到期就清零"
// 会在每一次失败上触发，计数器永远到不了阈值，
// **整个锁定功能静默失效**。
func (m *pairingManager) lockedFrom(source string, now time.Time) bool {
	entry, ok := m.failures[source]
	if !ok || entry.lockedUntil.IsZero() {
		return false
	}
	if !now.Before(entry.lockedUntil) {
		// 锁到期：计数一并清零。不清的话下一次失败会立刻重新锁上，
		// 于是"锁 15 分钟"变成"锁到世界末日"。
		entry.count = 0
		entry.lockedUntil = time.Time{}
		return false
	}
	return true
}

// expireLocked 清掉已过期的会话。
//
// 调用方必须已持有 m.mu。
func (m *pairingManager) expireLocked(now time.Time) {
	for id, entry := range m.sessions {
		if entry.session.Expired(now) {
			delete(m.sessions, id)
		}
	}
}

// claim 用一个密钥或六位码认领会话。
//
// 成功即销毁会话（一次性）；失败会累计到会话与全局两个计数器上。
// 返回的会话只用于读取角色与名字。
func (m *pairingManager) claim(credential, source string, now time.Time) (PairingSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.lockedFrom(source, now) {
		return PairingSession{}, ErrPairingLocked
	}
	m.expireLocked(now)

	entry := m.matchLocked(credential)
	if entry == nil {
		m.recordFailureLocked(source, now)
		return PairingSession{}, ErrPairingMismatch
	}

	// 命中即销毁：配对码是一次性的。留着一个已经用过的会话会让
	// "同一个码再扫一次"变成一次静默的重放。
	delete(m.sessions, entry.session.ID)
	// 配对成功即清掉这个来源的失败计数：一个人输错两次之后终于输对，
	// 不该因为那两次而离锁定更近。
	if entry, ok := m.failures[source]; ok {
		entry.count = 0
		entry.lockedUntil = time.Time{}
	}
	return entry.session, nil
}

// matchLocked 找出凭证命中的会话（可能为空）。
//
// 调用方必须已持有 m.mu。
func (m *pairingManager) matchLocked(credential string) *pairingEntry {
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return nil
	}

	// 先按密钥找：它是自动带过来的，不必做任何归一化，
	// 因此这条路径上不能出现"因为归一化而匹配到了别的会话"。
	for _, entry := range m.sessions {
		if secretEqual(entry.session.Secret, credential) {
			return entry
		}
	}

	return nil
}

// recordFailureLocked 记一次失败，必要时把会话销毁或锁定来源。
//
// 调用方必须已持有 m.mu。
func (m *pairingManager) recordFailureLocked(source string, now time.Time) {
	// 会话销毁是**全局**的：任何一个来源猜错都会推进它。
	//
	// 这一条才是真正决定枚举成本的：无论攻击者用多少个来源并发猜，
	// 一个会话总共只接受 5 次猜测，然后就必须让用户重新生成。
	for id, entry := range m.sessions {
		entry.failures++
		if entry.failures >= pairingMaxFailures {
			delete(m.sessions, id)
		}
	}

	if m.failures == nil {
		m.failures = map[string]*sourceFailures{}
	}
	entry, ok := m.failures[source]
	if !ok {
		// 来源表也要有上限，否则被刷时它会无界增长。
		// 满了就不再记新来源：那些来源失去的是"被锁定"的能力，
		// 而它们仍然受令牌桶限流 —— 不会因此变得不受限。
		if len(m.failures) >= maxTrackedSources {
			m.sweepFailuresLocked(now)
			if len(m.failures) >= maxTrackedSources {
				return
			}
		}
		entry = &sourceFailures{}
		m.failures[source] = entry
	}
	// 锁已经过期：从零开始数，否则会"一次失败立刻重新锁上"。
	// 同样要判零值 —— 见 lockedFrom 的说明。
	if !entry.lockedUntil.IsZero() && !now.Before(entry.lockedUntil) {
		entry.count = 0
		entry.lockedUntil = time.Time{}
	}
	entry.count++
	entry.last = now

	if entry.count >= pairingLockThreshold {
		entry.lockedUntil = now.Add(pairingLockDuration)
		entry.count = 0
	}
}

// sweepFailuresLocked 丢掉很久没有活动的来源记录。
//
// 调用方必须已持有 m.mu。
func (m *pairingManager) sweepFailuresLocked(now time.Time) {
	for source, entry := range m.failures {
		if now.Sub(entry.last) > sourceIdleTTL && now.After(entry.lockedUntil) {
			delete(m.failures, source)
		}
	}
}

// 跨会话连续失败达到这个次数就锁定**该来源**。
//
// 10 次：正常用户输错两次就会去重新生成二维码，而这个阈值给真正的
// 手误留足了余量（六位码有 32^6 种，10 次猜测的成功概率约 1e-8）。
const pairingLockThreshold = 10

// maxTrackedSources 是按来源记账表的容量上限。
const maxTrackedSources = 4096

// sourceIdleTTL 是来源记录的保留时长。
const sourceIdleTTL = 30 * time.Minute

// pairingLockDuration 是锁定时长。
//
// 十五分钟：它比一个人愿意对着手机反复输码的时间长，又比"明天再试"
// 短得多 —— 锁定的目的是让枚举在时间上不可行，不是惩罚用户。
const pairingLockDuration = 15 * time.Minute

// randomID 生成一个 n 字节随机数的 base64url 文本。
func randomID(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New(i18n.T("remote.err.random"))
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// randomSecret 生成二维码里携带的配对密钥。
func randomSecret() (string, error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New(i18n.T("remote.err.random"))
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// secretEqual 常数时间比较两个配对密钥。
//
// 密钥是 256 位随机值，逐字节计时攻击在它面前本来就没有意义（要猜中
// 前缀才能利用时间差，而猜中前缀的概率本身就是 2^-8 起步）。这里仍然
// 用常数时间比较，是为了让"凡是比对机密就走同一条函数"成为一条不需要
// 每次重新判断的规则 —— 判断本身才是容易出错的地方。
func secretEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
