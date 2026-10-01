package verify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖外部验证的核心判断。
//
// 全部价值集中在一条：**能不能从来源地址分辨出"真的从外面连上了"
// 与"其实走的是本机/内网路径"**。分错的方向恰好是"把无效凭据当成
// 成功"，那会让用户以为已经配好了，然后在真正的外网访问失败时
// 彻底摸不着头脑。

// ---------------------------------------------------------------------------
// 来源分类
// ---------------------------------------------------------------------------

// TestClassifySource 是本文件最重要的一条。
func TestClassifySource(t *testing.T) {
	t.Parallel()

	cases := []struct {
		addr string
		want SourceKind
		why  string
	}{
		// 能证明链路的只有公网地址。
		{"2409:8a50:6a1:7450::50b", SourcePublic, "手机走移动数据的 IPv6 地址"},
		{"240e:3b0:1111:2200::1", SourcePublic, "中国电信公网 IPv6"},
		{"203.0.113.7", SourcePublic, "公网 IPv4"},
		{"8.8.8.8", SourcePublic, "Google DNS"},

		// 走回环 —— 用户在自己电脑上打开了那个地址。
		{"127.0.0.1", SourceLoopback, "本机"},
		{"::1", SourceLoopback, "本机 IPv6"},

		// 内网 —— 手机还连着 Wi-Fi，或者路由器做了 NAT 回流。
		{"192.168.31.241", SourcePrivate, "家庭内网"},
		{"10.0.0.5", SourcePrivate, "私有网段"},
		{"172.16.0.1", SourcePrivate, "私有网段"},
		{"fd00::1", SourcePrivate, "IPv6 唯一本地地址"},

		// **最容易判错的一类**：运营商级 NAT。
		//
		// netip 的 IsPrivate() 不覆盖 100.64.0.0/10，因此它会被
		// 误判为公网地址。而家宽场景里它极常见 —— 把它当成公网访问，
		// 用户会在自己家里得到"验证成功"，然后在外网彻底连不上。
		{"100.64.0.1", SourcePrivate, "运营商级 NAT（RFC 6598）"},
		{"100.127.255.254", SourcePrivate, "运营商级 NAT 上边界"},

		// 链路本地。
		{"169.254.1.1", SourceLinkLocal, "APIPA"},
		{"fe80::1", SourceLinkLocal, "IPv6 链路本地"},
	}

	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			t.Parallel()

			addr := netip.MustParseAddr(tc.addr)
			got := ClassifySource(addr, nil)
			if got != tc.want {
				t.Errorf("ClassifySource(%s) = %s，期望 %s（%s）",
					tc.addr, got, tc.want, tc.why)
			}

			// 只有公网来源能证明链路可用。
			if got.ProvesReachability() != (tc.want == SourcePublic) {
				t.Errorf("%s 的 ProvesReachability() 与分类不一致", tc.addr)
			}
		})
	}
}

// TestCarrierNATBoundary 验证运营商级 NAT 网段的边界。
//
// 100.64.0.0/10 的范围是 100.64.0.0 ~ 100.127.255.255。
// 边界写错一位会把大量正常公网地址误判为内网（用户看到"无效凭据"
// 却找不出原因），或者反过来把 NAT 地址当成公网（更糟）。
func TestCarrierNATBoundary(t *testing.T) {
	t.Parallel()

	inside := []string{"100.64.0.0", "100.64.0.1", "100.100.1.1", "100.127.255.255"}
	for _, s := range inside {
		if got := ClassifySource(netip.MustParseAddr(s), nil); got != SourcePrivate {
			t.Errorf("%s 在 100.64.0.0/10 内，应当判为内网，得到 %s", s, got)
		}
	}

	// 紧邻的地址不在网段内。
	outside := []string{"100.63.255.255", "100.128.0.0", "100.0.0.1"}
	for _, s := range outside {
		if got := ClassifySource(netip.MustParseAddr(s), nil); got != SourcePublic {
			t.Errorf("%s 不在 100.64.0.0/10 内，应当判为公网，得到 %s", s, got)
		}
	}
}

func TestClassifySourceHandlesIPv4Mapped(t *testing.T) {
	t.Parallel()

	// ::ffff:127.0.0.1 是 IPv4 映射形式的回环地址。
	// 不先 Unmap 的话会被当成 IPv6 全局地址 —— 那是一个"本机访问
	// 被记为公网访问"的漏洞。
	addr := netip.MustParseAddr("::ffff:127.0.0.1")
	if got := ClassifySource(addr, nil); got != SourceLoopback {
		t.Errorf("IPv4 映射的回环地址 = %s，期望 loopback", got)
	}
}

// ---------------------------------------------------------------------------
// 会话
// ---------------------------------------------------------------------------

func newTestManager(t *testing.T, target string) *Manager {
	t.Helper()
	m := NewManager(func(context.Context) string { return target }, nil)
	m.SetTTL(3 * time.Second)
	return m
}

// localURL 返回验证端点的**本机可达**地址。
//
// 不能用 sess.URL()：那个地址是给手机用的公网地址，在测试里通常是
// 不可路由的（例如 203.0.113.7 属于 TEST-NET-3），请求会一直挂到超时。
func localURL(s Session) string {
	return fmt.Sprintf("http://127.0.0.1:%d/%s", s.Port, s.Token)
}

func TestStartBindsAndReportsURL(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, "2409:8a50:6a1:7450::50b")
	sess, err := m.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatalf("开始验证失败: %v", err)
	}
	defer func() { _ = m.Stop(sess.ID) }() //nolint:errcheck // 测试清理

	if sess.Port == 0 {
		t.Error("应当分配到一个真实端口")
	}
	if sess.Token == "" {
		t.Error("缺少验证令牌")
	}
	if sess.Status != StatusWaiting {
		t.Errorf("初始状态 = %s", sess.Status)
	}

	// URL 里的 IPv6 字面量必须带方括号，否则解析会把地址里的冒号
	// 当成端口分隔符 —— 这是 IPv6 场景里最常见的低级错误。
	url := sess.URL()
	if !strings.HasPrefix(url, "http://[2409:8a50:6a1:7450::50b]:") {
		t.Errorf("IPv6 地址没有被方括号包裹: %s", url)
	}
	if !strings.HasSuffix(url, "/"+sess.Token) {
		t.Errorf("URL 里缺少令牌路径: %s", url)
	}
}

func TestURLWrapsOnlyIPv6(t *testing.T) {
	t.Parallel()

	v4 := Session{TargetIP: "203.0.113.7", Port: 8080, Token: "abc"}
	if got := v4.URL(); got != "http://203.0.113.7:8080/abc" {
		t.Errorf("IPv4 不该加方括号: %s", got)
	}

	none := Session{Port: 8080, Token: "abc"}
	if got := none.URL(); got != "" {
		t.Errorf("没有目标地址时应当返回空串，得到 %s", got)
	}
}

// TestLocalHitIsNotProof 验证本机访问被判为无效凭据。
//
// 这是整个功能里最关键的一条：本机访问走的是回环，不经过网络，
// 无论防火墙与运营商是否放行都会"成功"。
func TestLocalHitIsNotProof(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, "2409:8a50:6a1:7450::50b")
	sess, err := m.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(sess.ID) }() //nolint:errcheck // 测试清理

	// 从本机访问 —— 这正是用户"在自己电脑上试一下"时的行为。
	//
	// 这里**刻意**用公网地址而不是 127.0.0.1：IPv6 没有 NAT，
	// 用户在自己机器上打开那个公网地址时，连接是直连的，
	// 来源地址就是机器自己的全局单播地址 —— 那正是最难识别、
	// 也最容易造成假阳性的一种情形。
	resp, err := http.Get(sess.URL())
	if err != nil {
		t.Fatalf("请求验证地址失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck // 测试清理

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("验证页面返回 %d", resp.StatusCode)
	}
	// 页面必须明确说这次访问不算数。
	if !strings.Contains(string(body), "不能作为凭据") {
		t.Errorf("页面没有说明这次访问无效:\n%s", body)
	}
	if !strings.Contains(string(body), "这台机器自己") {
		t.Errorf("页面应当解释原因是本机自己发起的访问:\n%s", body)
	}

	// 会话状态必须是 hairpin_only，而不是 reachable。
	got, ok := m.Get(sess.ID)
	if !ok {
		t.Fatal("会话不见了")
	}
	if got.Status != StatusHairpinOnly {
		t.Errorf("状态 = %s，期望 hairpin_only —— "+
			"把本机访问当成成功会让用户以为已经配好了", got.Status)
	}
	if got.Reachable() {
		t.Error("Reachable() 不该为 true")
	}
	if len(got.Hits) != 1 {
		t.Fatalf("应当记录 1 次访问，得到 %d", len(got.Hits))
	}
	// **关键**：分类必须是 self 而不是 public。
	//
	// IPv6 没有 NAT，从本机访问自己的公网地址时来源就是那个全局单播
	// 地址 —— 光看地址类型会判成公网，于是用户在"自己电脑上试一下"
	// 时看到"链路是通的"，等真的用手机时才发现根本连不上。
	if got.Hits[0].Kind != SourceSelf {
		t.Errorf("来源分类 = %s，期望 self（本机自己的地址）", got.Hits[0].Kind)
	}
	// 来源地址必须记录下来 —— 用户据此判断"这到底是谁访问的"。
	if got.Hits[0].RemoteAddr == "" {
		t.Error("缺少来源地址")
	}
}

// TestUnknownPathIs404 验证令牌路径之外的请求不会被记录。
//
// 一个固定路径的"验证端点"会被扫描器发现，而它会把任意扫描流量
// 变成一次"验证成功"的误报 —— 或者反过来暴露这台机器上跑着 ISC。
func TestUnknownPathIs404(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, "203.0.113.7")
	sess, err := m.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(sess.ID) }() //nolint:errcheck // 测试清理

	base := fmt.Sprintf("http://127.0.0.1:%d", sess.Port)
	for _, path := range []string{"/", "/index.html", "/verify", "/" + sess.Token + "x"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("请求 %s 失败: %v", path, err)
		}
		resp.Body.Close() //nolint:errcheck // 测试清理
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("路径 %s 应当返回 404，得到 %d", path, resp.StatusCode)
		}
	}

	// 这些请求都不该被计入。
	got, _ := m.Get(sess.ID)
	if len(got.Hits) != 0 {
		t.Errorf("令牌之外的路径不该被记录，实际记录了 %d 次", len(got.Hits))
	}
	if got.Status != StatusWaiting {
		t.Errorf("状态 = %s，期望 waiting", got.Status)
	}
}

func TestTokenIsUnguessable(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool)
	for i := 0; i < 32; i++ {
		tok, err := newToken()
		if err != nil {
			t.Fatal(err)
		}
		// 16 字节 base64url 后是 22 个字符。
		if len(tok) < 20 {
			t.Fatalf("令牌太短（%d 字符），可能被猜到: %s", len(tok), tok)
		}
		if seen[tok] {
			t.Fatal("生成了重复的令牌")
		}
		seen[tok] = true
	}
}

// TestExpiryMarksUnreachable 验证超时后的结论。
func TestExpiryMarksUnreachable(t *testing.T) {
	t.Parallel()

	m := NewManager(func(context.Context) string { return "203.0.113.7" }, nil)
	m.SetTTL(150 * time.Millisecond)

	sess, err := m.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}

	// 等过期。
	deadline := time.Now().Add(3 * time.Second)
	var got Session
	for time.Now().Before(deadline) {
		got, _ = m.Get(sess.ID)
		if got.Status == StatusUnreachable {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	if got.Status != StatusUnreachable {
		t.Fatalf("状态 = %s，期望 unreachable", got.Status)
	}
	// 结论必须指向上游 —— 那才是用户接下来该处理的地方。
	if !strings.Contains(got.Message, "上游") {
		t.Errorf("超时结论应当指向上游封禁: %s", got.Message)
	}
}

func TestStopKeepsRecord(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, "203.0.113.7")
	sess, err := m.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Stop(sess.ID); err != nil {
		t.Fatalf("停止失败: %v", err)
	}

	// 记录要保留：用户可能想回看"上次验证的结果是什么"。
	got, ok := m.Get(sess.ID)
	if !ok {
		t.Fatal("停止后不该丢掉记录")
	}
	if got.Status != StatusStopped {
		t.Errorf("状态 = %s，期望 stopped", got.Status)
	}
}

func TestStopUnknownSession(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, "203.0.113.7")
	if err := m.Stop("nope"); err == nil {
		t.Error("停止不存在的会话应当报错")
	}
}

func TestStartRejectsOccupiedPort(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, "203.0.113.7")
	first, err := m.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(first.ID) }() //nolint:errcheck // 测试清理

	// 同一个端口不能再监听一次。
	_, err = m.Start(context.Background(), StartRequest{Port: first.Port})
	if err == nil {
		t.Fatal("占用中的端口应当报错")
	}
	// 错误信息要指出端口冲突，而不是一句笼统的失败。
	if !strings.Contains(err.Error(), "占用") {
		t.Errorf("错误信息应当提示端口冲突: %v", err)
	}
}

func TestListIsNewestFirst(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, "203.0.113.7")

	var ids []string
	for i := 0; i < 3; i++ {
		s, err := m.Start(context.Background(), StartRequest{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
		time.Sleep(5 * time.Millisecond)
	}
	defer func() {
		for _, id := range ids {
			_ = m.Stop(id)
		}
	}()

	list := m.List()
	if len(list) != 3 {
		t.Fatalf("应当有 3 个会话，得到 %d", len(list))
	}
	if list[0].ID != ids[2] {
		t.Errorf("列表应当最近的在前，得到 %s", list[0].ID)
	}
}

// TestListReturnsCopies 验证返回的是副本。
//
// 共享底层切片会让"读取列表"与"记录新访问"并发时产生数据竞争 ——
// 而那个症状是偶发的、极难复现的。
func TestListReturnsCopies(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, "203.0.113.7")
	sess, err := m.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(sess.ID) }() //nolint:errcheck // 测试清理

	if _, err := http.Get(localURL(sess)); err != nil {
		t.Fatal(err)
	}

	list := m.List()
	if len(list[0].Hits) == 0 {
		t.Fatal("应当记录到访问")
	}
	// 篡改副本不该影响内部状态。
	list[0].Hits[0].RemoteAddr = "tampered"

	again, _ := m.Get(sess.ID)
	if again.Hits[0].RemoteAddr == "tampered" {
		t.Error("List 返回的应当是副本，篡改它不该影响内部状态")
	}
}

// ---------------------------------------------------------------------------
// 结果页
// ---------------------------------------------------------------------------

// TestResultPageNeverLeaksInternals 验证页面不泄漏本机信息。
//
// 这是整个项目里唯一一个会被**外部设备**看到的页面。
func TestResultPageNeverLeaksInternals(t *testing.T) {
	t.Parallel()

	sess := &Session{
		ID: "abc123", Port: 8080, Token: "sekrit",
		TargetIP: "2409:8a50:6a1:7450::50b",
		Hits:     []Hit{{RemoteAddr: "8.8.8.8", Kind: SourcePublic}},
	}

	for _, kind := range []SourceKind{SourcePublic, SourceSelf, SourceLoopback, SourcePrivate, SourceLinkLocal} {
		page := renderPage(kind, sess)

		// 不得出现令牌 —— 页面被截图分享时不该泄漏它。
		if strings.Contains(page, sess.Token) {
			t.Errorf("[%s] 页面里出现了验证令牌", kind)
		}
		// 不得出现会话 ID。
		if strings.Contains(page, sess.ID) {
			t.Errorf("[%s] 页面里出现了会话 ID", kind)
		}
		// 不得出现端口与目标地址之外的内部信息。
		if strings.Contains(page, "ISC-Core") || strings.Contains(page, "isc-core") {
			t.Errorf("[%s] 页面里出现了项目内部标识", kind)
		}
		// 必须是一个完整的 HTML 文档。
		if !strings.HasPrefix(page, "<!DOCTYPE html>") {
			t.Errorf("[%s] 页面不是完整的 HTML", kind)
		}
		if !strings.Contains(page, "</html>") {
			t.Errorf("[%s] 页面没有正确闭合", kind)
		}
		// 不得引用任何外部资源：手机上多一次请求就多一次失败机会。
		for _, bad := range []string{"http://", "https://", "<script", "src="} {
			if strings.Contains(page, bad) {
				t.Errorf("[%s] 页面引用了外部资源或脚本: %q", kind, bad)
			}
		}
	}
}

func TestResultPageShowsCorrectConclusion(t *testing.T) {
	t.Parallel()

	sess := &Session{Hits: []Hit{{RemoteAddr: "8.8.8.8", Kind: SourcePublic}}}

	ok := renderPage(SourcePublic, sess)
	if !strings.Contains(ok, "链路是通的") {
		t.Error("公网来源应当显示成功结论")
	}

	bad := renderPage(SourceLoopback, sess)
	if !strings.Contains(bad, "不能作为凭据") {
		t.Error("回环来源应当明确说明无效")
	}
	if strings.Contains(bad, "链路是通的") {
		t.Error("回环来源不该显示成功结论")
	}
}

func TestSourceAddressIsEscaped(t *testing.T) {
	t.Parallel()

	// 来源地址理论上来自 TCP 层、不可伪造，但仍然要转义 ——
	// "这个值不会含有特殊字符"是一个不该依赖的假设。
	sess := &Session{
		Hits: []Hit{{RemoteAddr: `<script>alert(1)</script>`, Kind: SourcePublic}},
	}
	page := renderPage(SourcePublic, sess)
	if strings.Contains(page, "<script>") {
		t.Error("来源地址没有被转义")
	}
}
