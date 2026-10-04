package remote

import (
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件覆盖远程管理面里那些**出错时最难查**的部分。
//
// 挑选标准不是"代码行数多"，而是"错了之后症状与原因隔得有多远"：
// 固定错误的公钥会让所有已配对手机突然报"服务器身份已变化"，
// 而那时用户与服务端都看起来完全正常；配对码的字母表错了只会在
// 某些人手上失败；限流的令牌补充算错了则表现为"偶尔连不上"。

func TestRoleOrdering(t *testing.T) {
	t.Parallel()

	cases := []struct {
		have, want Role
		atLeast    bool
	}{
		{RoleOperator, RoleViewer, true},
		{RoleOperator, RoleOperator, true},
		{RoleViewer, RoleOperator, false},
		{RoleViewer, RoleViewer, true},
	}
	for _, c := range cases {
		if got := c.have.AtLeast(c.want); got != c.atLeast {
			t.Errorf("%s.AtLeast(%s) = %v，期望 %v", c.have, c.want, got, c.atLeast)
		}
	}

	// 未知角色必须落到最低权限，而不是最高。
	//
	// 它在派生路径上是安全关键：如果 rank 对未知值返回一个大数字，
	// 一台角色被写坏的设备就能派生出一个 operator。
	if Role("").AtLeast(RoleViewer) {
		t.Error("未知角色不应达到 viewer 的权限")
	}
	if !RoleOperator.AtLeast(Role("bogus")) {
		t.Error("未知角色的权限序应当是最低的")
	}
}

func TestNewTokenIsHashedNotStored(t *testing.T) {
	t.Parallel()

	token, hash, err := newToken()
	if err != nil {
		t.Fatalf("生成令牌失败: %v", err)
	}
	if len(hash) != 32 {
		t.Fatalf("哈希长度 = %d，期望 32", len(hash))
	}
	if strings.Contains(string(hash), token) {
		t.Fatal("哈希里出现了令牌原文")
	}
	// 同一个令牌必须哈希到同一个值 —— 否则鉴权会永远失败。
	if got := tokenHash(token); string(got) != string(hash) {
		t.Fatal("tokenHash 与 newToken 算的不是同一个东西")
	}
	if !hashEqual(hash, tokenHash(token)) {
		t.Fatal("hashEqual 对同一个令牌返回了 false")
	}
	if hashEqual(hash, tokenHash(token+"x")) {
		t.Fatal("hashEqual 对不同令牌返回了 true")
	}
}

// TestCertificateKeepsSPKIWhenAddressesChange 钉住最要命的那条不变量。
//
// 场景：用户换了网段（或插了一块新网卡），SAN 变了 → 证书必须重签。
// 如果重签时换了私钥，所有已配对的手机都会报"服务器身份已变化" ——
// 而用户完全无法理解，因为**服务器根本没换**。
//
// 客户端固定的是公钥（SPKI）而不是整张证书，正是为了扛住这件事。
func TestCertificateKeepsSPKIWhenAddressesChange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	first, err := LoadOrCreateCertificate(dir)
	if err != nil {
		t.Fatalf("首次生成证书失败: %v", err)
	}

	// 用同一把私钥签一张**地址为空**的证书，模拟"当前地址没有被覆盖"。
	// 下一次加载就会走重签路径。
	key, err := loadOrCreateKey(filepath.Join(dir, keyFileName))
	if err != nil {
		t.Fatalf("读取私钥失败: %v", err)
	}
	if _, err := issueCertificate(filepath.Join(dir, certFileName), key, certificateNames{}); err != nil {
		t.Fatalf("重签失败: %v", err)
	}

	second, err := LoadOrCreateCertificate(dir)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}

	if first.SPKIBase64() != second.SPKIBase64() {
		t.Fatalf("重签之后公钥指纹变了：%s → %s。\n"+
			"这会让每一台已配对的手机都报「服务器身份已变化」，而服务器根本没换。",
			first.SPKIBase64(), second.SPKIBase64())
	}
	if first.FingerprintShort() != second.FingerprintShort() {
		t.Fatal("短码也应当保持不变 —— 它是给人核对用的")
	}
	// 重签之后 SAN 必须重新覆盖当前地址，否则会陷入"每次启动都重签"。
	if !covers(second.Leaf, currentCertificateNames()) {
		t.Fatal("重签后的证书没有覆盖当前地址")
	}
}

func TestCertificateIsReusedWhenAddressesAreCovered(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first, err := LoadOrCreateCertificate(dir)
	if err != nil {
		t.Fatalf("首次生成失败: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, certFileName))
	if err != nil {
		t.Fatalf("读取证书文件失败: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	second, err := LoadOrCreateCertificate(dir)
	if err != nil {
		t.Fatalf("二次加载失败: %v", err)
	}
	after, err := os.Stat(filepath.Join(dir, certFileName))
	if err != nil {
		t.Fatalf("读取证书文件失败: %v", err)
	}

	if first.SPKIBase64() != second.SPKIBase64() {
		t.Fatal("地址没变时不该换证书")
	}
	if !info.ModTime().Equal(after.ModTime()) {
		t.Fatal("地址没变时证书文件不该被重写")
	}
}

func TestCertificateFilesHaveTightPermissions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := LoadOrCreateCertificate(dir); err != nil {
		t.Fatalf("生成证书失败: %v", err)
	}

	keyInfo, err := os.Stat(filepath.Join(dir, keyFileName))
	if err != nil {
		t.Fatalf("读取私钥失败: %v", err)
	}
	// 私钥是远程面的根信任：它被读走就等于能冒充这台服务器。
	if perm := keyInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("私钥权限 = %04o，期望 0600", perm)
	}
}

func TestCertificateIsUsableOverTLS(t *testing.T) {
	t.Parallel()

	// 这里只断言证书对象本身是可用的（有私钥、有叶子）。
	// 真正的握手由 API 层与端到端测试覆盖 —— 起一个监听的测试
	// 会引入端口与并发的不确定性，而它证明的东西并不更多。
	dir := t.TempDir()
	cert, err := LoadOrCreateCertificate(dir)
	if err != nil {
		t.Fatalf("生成证书失败: %v", err)
	}
	if cert.TLS.PrivateKey == nil {
		t.Fatal("证书没有配对私钥，ServeTLS 会失败")
	}
	if len(cert.TLS.Certificate) == 0 {
		t.Fatal("证书链为空")
	}
	if cert.Leaf.NotAfter.Before(time.Now().Add(24 * time.Hour)) {
		t.Fatal("证书有效期短得不合理")
	}
	// 自签：签发者就是自己。
	if err := cert.Leaf.CheckSignatureFrom(cert.Leaf); err != nil {
		t.Fatalf("自签证书的自校验失败: %v", err)
	}
}

// TestNormalizePairingCode 覆盖用户会真的打出来的那些输入。
func TestNormalizePairingCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, in, want string
	}{
		{"已规范", "A2B3C4", "A2B3C4"},
		{"小写", "a2b3c4", "A2B3C4"},
		{"带连字符", "A2B3-C4", "A2B3C4"},
		{"带空格", "A2B3 C4", "A2B3C4"},
		{"字母 I 当成数字 1", "I2B3C4", "12B3C4"},
		{"字母 L 当成数字 1", "L2B3C4", "12B3C4"},
		{"字母 O 当成数字 0", "O2B3C4", "02B3C4"},
		{"长度不足", "A2B3C", ""},
		{"长度超出", "A2B3C4D", ""},
		{"含有字母表外的字符", "A2B3C!", ""},
		{"空串", "", ""},
		{"只有分隔符", "------", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizePairingCode(c.in); got != c.want {
				t.Errorf("NormalizePairingCode(%q) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}

// TestPairingAlphabetAvoidsLookalikes 钉住字母表本身。
//
// 生成端去掉易混字符，是为了让**读**它的人不出错；一旦有人为了
// "看起来更随机"把 I/L/O/U 加回来，这类错误会立刻回来。
func TestPairingAlphabetAvoidsLookalikes(t *testing.T) {
	t.Parallel()

	if len(pairingCodeAlphabet) != 32 {
		t.Fatalf("字母表长度 = %d，必须是 32（否则取模不再均匀）",
			len(pairingCodeAlphabet))
	}
	for _, bad := range []rune{'I', 'L', 'O', 'U'} {
		if strings.ContainsRune(pairingCodeAlphabet, bad) {
			t.Errorf("字母表里不该有 %q", bad)
		}
	}

	// 生成的码必须全部落在字母表内，且长度正确。
	seen := map[rune]bool{}
	for i := 0; i < 200; i++ {
		code := newPairingCode()
		if NormalizePairingCode(code) != code {
			t.Fatalf("生成的码 %q 无法通过规范化", code)
		}
		for _, r := range code {
			seen[r] = true
		}
	}
	// 200 次生成的 1200 个字符应当覆盖到字母表的绝大多数 ——
	// 取模有偏时会出现某些字符**从不出现**。
	if len(seen) < 28 {
		t.Errorf("200 次生成只覆盖了 %d 个字符，分布可疑", len(seen))
	}
}

func TestPairingSessionLifecycle(t *testing.T) {
	t.Parallel()

	m := newPairingManager()
	now := time.Now()

	session, err := m.start(RoleOperator, "iPhone", now)
	if err != nil {
		t.Fatalf("开启会话失败: %v", err)
	}
	if session.Code == "" || session.Secret == "" {
		t.Fatal("会话缺少码或密钥")
	}

	// 同一时刻只能有一个会话：否则界面上显示的那个二维码与实际
	// 生效的会话可能不是同一个。
	if _, err := m.start(RoleViewer, "", now); err != ErrPairingConflict {
		t.Fatalf("第二次开启会话的错误 = %v，期望 ErrPairingConflict", err)
	}

	current, ok := m.current(now)
	if !ok || current.ID != session.ID {
		t.Fatal("current 没有返回刚建立的会话")
	}

	// 认领之后会话必须消失（一次性）。
	claimed, err := m.claim(session.Secret, now)
	if err != nil {
		t.Fatalf("用密钥认领失败: %v", err)
	}
	if claimed.ID != session.ID || claimed.Role != RoleOperator {
		t.Fatal("认领到的会话不对")
	}
	if _, ok := m.current(now); ok {
		t.Fatal("认领之后会话应当已被销毁")
	}
	if _, err := m.claim(session.Secret, now); err != ErrPairingMismatch {
		t.Fatalf("重放同一个密钥的错误 = %v，期望 ErrPairingMismatch", err)
	}
}

func TestPairingAcceptsNormalizedCode(t *testing.T) {
	t.Parallel()

	m := newPairingManager()
	now := time.Now()

	session, err := m.start(RoleViewer, "", now)
	if err != nil {
		t.Fatalf("开启会话失败: %v", err)
	}

	// 用户会把码念出来再敲进去：大小写、分隔符、以及把 0 看成 O。
	messy := strings.ToLower(session.Code[:3]) + "-" + session.Code[3:]
	if _, err := m.claim(messy, now); err != nil {
		t.Fatalf("规范化后的码应当能认领成功，得到 %v", err)
	}
}

func TestPairingExpires(t *testing.T) {
	t.Parallel()

	m := newPairingManager()
	now := time.Now()

	session, err := m.start(RoleViewer, "", now)
	if err != nil {
		t.Fatalf("开启会话失败: %v", err)
	}

	later := now.Add(PairingTTL + time.Second)
	if _, ok := m.current(later); ok {
		t.Fatal("过期的会话不该还在")
	}
	if _, err := m.claim(session.Secret, later); err != ErrPairingMismatch {
		t.Fatalf("用过期会话的密钥认领的错误 = %v，期望 ErrPairingMismatch", err)
	}
}

// TestPairingLocksOutAfterRepeatedFailures 覆盖枚举攻击的那条防线。
//
// 单会话 5 次失败就销毁是不够的：攻击者可以让每个会话只失败 4 次，
// 等它过期再开一个新的。跨会话的连续失败计数才是真正拦住枚举的东西。
func TestPairingLocksOutAfterRepeatedFailures(t *testing.T) {
	t.Parallel()

	m := newPairingManager()
	now := time.Now()

	// 反复"开会话 → 试一次错"：每个会话只失败一次，因此**单会话**
	// 的 5 次上限永远不会触发 —— 这正是这条测试要证明的绕法。
	for i := 0; i < pairingLockThreshold; i++ {
		session, err := m.start(RoleViewer, "", now)
		if err == ErrPairingConflict {
			// 上一轮的会话还在（失败次数没到单会话上限），先撤掉。
			if current, ok := m.current(now); ok {
				m.cancel(current.ID)
			}
			session, err = m.start(RoleViewer, "", now)
		}
		if err != nil {
			t.Fatalf("第 %d 轮开启会话失败: %v", i, err)
		}
		if _, err := m.claim("ZZZZZZ", now); err != ErrPairingMismatch {
			t.Fatalf("第 %d 轮的错误码应当被拒绝，得到 %v", i, err)
		}
		// 会话仍在（只失败了一次），下一轮要显式撤掉。
		if _, ok := m.current(now); !ok {
			t.Fatalf("第 %d 轮之后会话不该消失（单次失败不该销毁它）", i)
		}
		_ = session
	}

	if _, err := m.start(RoleViewer, "", now); err != ErrPairingLocked {
		t.Fatalf("连续失败之后应当被锁定，得到 %v", err)
	}

	// 锁定会自动解除。
	after := now.Add(pairingLockDuration + time.Second)
	if _, err := m.start(RoleViewer, "", after); err != nil {
		t.Fatalf("锁定到期后应当能重新开启会话，得到 %v", err)
	}
}

func TestPairingSessionDestroyedAfterPerSessionFailures(t *testing.T) {
	t.Parallel()

	m := newPairingManager()
	now := time.Now()

	session, err := m.start(RoleViewer, "", now)
	if err != nil {
		t.Fatalf("开启会话失败: %v", err)
	}

	for i := 0; i < pairingMaxFailures; i++ {
		if _, err := m.claim("ZZZZZZ", now); err != ErrPairingMismatch {
			t.Fatalf("第 %d 次失败的错误 = %v", i, err)
		}
	}

	// 会话已经销毁 —— 此时**正确的码也不该再能用**。
	if _, ok := m.current(now); ok {
		t.Fatal("尝试次数用尽之后会话应当已被销毁")
	}
	if _, err := m.claim(session.Code, now); err != ErrPairingMismatch {
		t.Fatalf("销毁之后正确码的错误 = %v，期望 ErrPairingMismatch", err)
	}
}

func TestPairingSuccessResetsFailureCount(t *testing.T) {
	t.Parallel()

	m := newPairingManager()
	now := time.Now()

	if _, err := m.start(RoleViewer, "", now); err != nil {
		t.Fatalf("开启会话失败: %v", err)
	}

	// 先失败几次（不足以触发会话销毁）。
	for i := 0; i < pairingMaxFailures-1; i++ {
		if _, err := m.claim("ZZZZZZ", now); err != ErrPairingMismatch {
			t.Fatalf("失败 %d 的错误 = %v", i, err)
		}
	}

	session, ok := m.current(now)
	if !ok {
		t.Fatal("还没到上限，会话不该消失")
	}
	if _, err := m.claim(session.Code, now); err != nil {
		t.Fatalf("正确码应当仍然可用，得到 %v", err)
	}

	// 成功之后计数归零：下一次会话的失败额度是完整的。
	next, err := m.start(RoleViewer, "", now)
	if err != nil {
		t.Fatalf("开启新会话失败: %v", err)
	}
	if _, err := m.claim(next.Code, now); err != nil {
		t.Fatalf("新会话应当可以一次成功，得到 %v", err)
	}
}

func TestPairingCancel(t *testing.T) {
	t.Parallel()

	m := newPairingManager()
	now := time.Now()
	session, err := m.start(RoleViewer, "", now)
	if err != nil {
		t.Fatalf("开启会话失败: %v", err)
	}

	if !m.cancel(session.ID) {
		t.Fatal("取消一个存在的会话应当返回 true")
	}
	if m.cancel(session.ID) {
		t.Fatal("重复取消应当返回 false")
	}
	if _, ok := m.current(now); ok {
		t.Fatal("取消之后不该还有会话")
	}
}

// TestSecretIsNeverMistakenForCode 覆盖一个真实的边界：
// 密钥是 base64url 的 43 个字符，其中可能**恰好包含**一个合法六位码的子串。
// 匹配必须先按密钥全文比对，否则会出现"用别人二维码的一部分就能配对"。
func TestSecretIsNeverMistakenForCode(t *testing.T) {
	t.Parallel()

	m := newPairingManager()
	now := time.Now()
	session, err := m.start(RoleViewer, "", now)
	if err != nil {
		t.Fatalf("开启会话失败: %v", err)
	}

	// 取密钥里的一段：它长度不对，因此不该被当成有效输入。
	if _, err := m.claim(session.Secret[:6], now); err != ErrPairingMismatch {
		t.Fatalf("密钥前缀的错误 = %v，期望 ErrPairingMismatch", err)
	}
	// 会话应当仍然存活（那一次失败也在计数，但远没到上限）。
	if _, ok := m.current(now); !ok {
		t.Fatal("一次失败的尝试不该销毁会话")
	}
	// 完整的密钥仍然可用。
	if _, err := m.claim(session.Secret, now); err != nil {
		t.Fatalf("完整密钥应当可用，得到 %v", err)
	}

	// 顺带确认密钥确实是高熵的 base64url。
	if len(session.Secret) < 40 {
		t.Fatalf("配对密钥只有 %d 个字符，熵不足", len(session.Secret))
	}
	if _, err := base64.RawURLEncoding.DecodeString(session.Secret); err != nil {
		t.Fatalf("配对密钥不是合法的 base64url: %v", err)
	}
}

// TestLimiterRefillsOverTime 钉住令牌桶的补充速率。
//
// 时间由调用方传入，因此这里不需要 sleep —— 而 sleep 式的限流测试
// 在慢机器上会变成随机失败。
func TestLimiterRefillsOverTime(t *testing.T) {
	t.Parallel()

	l := newLimiter(60, 5) // 每秒一个令牌，桶容量 5
	now := time.Now()

	// 桶一开始是满的：连续 5 次放行，第 6 次拒绝。
	for i := 0; i < 5; i++ {
		if !l.allow("a", now) {
			t.Fatalf("第 %d 次请求就被拒绝了，桶应当是满的", i+1)
		}
	}
	if l.allow("a", now) {
		t.Fatal("桶耗尽之后应当拒绝")
	}

	// 一秒之后补一个令牌。
	if !l.allow("a", now.Add(time.Second)) {
		t.Fatal("一秒之后应当补上一个令牌")
	}
	if l.allow("a", now.Add(time.Second)) {
		t.Fatal("只补了一个令牌，第二次应当仍被拒绝")
	}

	// 长时间空闲之后桶会重新装满，但**不超过容量**。
	later := now.Add(time.Hour)
	for i := 0; i < 5; i++ {
		if !l.allow("a", later) {
			t.Fatalf("空闲之后第 %d 次请求被拒绝", i+1)
		}
	}
	if l.allow("a", later) {
		t.Fatal("桶不该超过容量")
	}
}

func TestLimiterIsPerKey(t *testing.T) {
	t.Parallel()

	l := newLimiter(1, 1)
	now := time.Now()

	if !l.allow("a", now) {
		t.Fatal("第一个来源应当放行")
	}
	if l.allow("a", now) {
		t.Fatal("同一个来源第二次应当拒绝")
	}
	// 另一个来源不受影响 —— 否则一台失灵的设备会把所有人都挡在外面。
	if !l.allow("b", now) {
		t.Fatal("另一个来源应当有自己的桶")
	}
}

func TestNilLimiterAllowsEverything(t *testing.T) {
	t.Parallel()

	var l *limiter
	if !l.allow("a", time.Now()) {
		t.Fatal("nil 限流器应当全部放行（它表示这一层没启用）")
	}
}

func TestLocalAddressesAreUsable(t *testing.T) {
	t.Parallel()

	addresses := Candidates(8788)
	if len(addresses) == 0 {
		t.Fatal("候选地址为空 —— 二维码里将没有任何可连的地址")
	}
	// 每一条都必须是 host:port，而 IPv6 必须带方括号
	// （否则 `240e::1:8788` 会被解析成一个解析不了的地址）。
	for _, addr := range addresses {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			t.Errorf("候选地址 %q 无法解析: %v", addr, err)
			continue
		}
		if host == "" || port != "8788" {
			t.Errorf("候选地址 %q 解析出了 host=%q port=%q", addr, host, port)
		}
	}
	// 主机名那条必须排在最后：它依赖 mDNS，是四个候选里最可能失败的一条。
	last := addresses[len(addresses)-1]
	if !strings.HasSuffix(last, ".local:8788") {
		t.Errorf("最后一条候选应当是 <hostname>.local，得到 %q", last)
	}
}

func TestConcurrentPairingAccessIsRaceFree(t *testing.T) {
	t.Parallel()

	// 配对会话会被两个方向同时访问：界面的短周期轮询读它，
	// 而手机在另一端尝试认领。少了锁就会在这里被 -race 抓到。
	m := newPairingManager()
	now := time.Now()
	if _, err := m.start(RoleViewer, "", now); err != nil {
		t.Fatalf("开启会话失败: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.current(now)
			_, _ = m.claim("ZZZZZZ", now)
		}()
	}
	wg.Wait()
}
