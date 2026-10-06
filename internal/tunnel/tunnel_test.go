package tunnel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTunnelIDByNameMatchesWithinOneRecord(t *testing.T) {
	// cloudflared 的 JSON 里字段顺序不保证，两种都要认。
	same := `[{"id":"ae47ca01-7774-4fe9-b75a-abd27e303634","name":"isc-phecda","createdAt":"2026-10-06T13:05:12Z"}]`
	if got := tunnelIDByName(same, DefaultName); got != "ae47ca01-7774-4fe9-b75a-abd27e303634" {
		t.Fatalf("id 在 name 之前时没认出来：%q", got)
	}
	reversed := `[{"name":"isc-phecda","id":"ae47ca01-7774-4fe9-b75a-abd27e303634"}]`
	const want = "ae47ca01-7774-4fe9-b75a-abd27e303634"
	if got := tunnelIDByName(reversed, DefaultName); got != want {
		t.Fatalf("name 在 id 之前时没认出来：%q", got)
	}
}

// 跨记录匹配比匹配不到更糟：那会把 A 的名字配上 B 的 id，于是内核去操作
// 一条**别的**隧道。宁可不认，让它去建一条新的。
func TestTunnelIDByNameNeverCrossesRecords(t *testing.T) {
	text := `[{"name":"someone-elses","id":"11111111-1111-1111-1111-111111111111"},` +
		`{"name":"isc-phecda","createdAt":"x"},` +
		`{"name":"another","id":"22222222-2222-2222-2222-222222222222"}]`
	if got := tunnelIDByName(text, DefaultName); got != "" {
		t.Fatalf("跨记录匹配了，得到 %q", got)
	}
}

func TestTunnelIDFromTextReadsCreateOutput(t *testing.T) {
	text := "Tunnel credentials written to /x/ae47ca01-7774-4fe9-b75a-abd27e303634.json.\n" +
		"Created tunnel isc-phecda with id ae47ca01-7774-4fe9-b75a-abd27e303634"
	if got := tunnelIDFromText(text); got != "ae47ca01-7774-4fe9-b75a-abd27e303634" {
		t.Fatalf("got %q", got)
	}
	if got := tunnelIDFromText("no uuid here"); got != "" {
		t.Fatalf("没有 UUID 时不该返回东西，得到 %q", got)
	}
}

// 生成的配置必须**只有一条** catch-all 规则。
//
// 这一条是"新站点自动上隧道"的全部依据：多写一条按 hostname 的规则，
// 就意味着新增站点要改配置、要重启隧道，而那份配置在界面上看不见。
func TestWriteConfigIsASingleCatchAllRule(t *testing.T) {
	dir := t.TempDir()
	m := New(Config{DataDir: dir, ProxyPort: 8443})
	m.SetTunnelID("ae47ca01-7774-4fe9-b75a-abd27e303634")
	if err := m.writeConfig("ae47ca01-7774-4fe9-b75a-abd27e303634"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(m.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, "service:") != 1 {
		t.Fatalf("应当只有一条 service 规则：\n%s", text)
	}
	if !strings.Contains(text, "http://127.0.0.1:8443") {
		t.Fatalf("上游端口不对：\n%s", text)
	}
	if strings.Contains(text, "hostname:") {
		t.Fatalf("不该有按 hostname 的规则：\n%s", text)
	}
	// 凭据路径必须落在内核自己的目录里，不能是用户的 ~/.cloudflared。
	if !strings.Contains(text, m.CredentialsPath("ae47ca01-7774-4fe9-b75a-abd27e303634")) {
		t.Fatalf("凭据路径不在内核目录里：\n%s", text)
	}
}

func TestFindBinaryHonoursConfiguredPathAndReportsMissing(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "cloudflared")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(Config{DataDir: t.TempDir(), Binary: script})
	if got := m.FindBinary(); got != script {
		t.Fatalf("FindBinary = %q，期望 %q", got, script)
	}
	// 配置了一个不存在的路径时，必须继续往下找，而不是返回它。
	m2 := New(Config{DataDir: t.TempDir(), Binary: filepath.Join(dir, "nope")})
	if got := m2.FindBinary(); got == filepath.Join(dir, "nope") {
		t.Fatal("不该返回一个不存在的路径")
	}
}

// 进程起来了但还没连上边缘时，Ready 必须是 false。
//
// 域名绑定靠它决定"这个域名走隧道还是走直连"。一个乐观的 true 会把
// CNAME 指向一条还没接通的隧道，用户得到的是一个打不开的域名。
func TestReadyIsFalseUntilAnEdgeConnectionRegisters(t *testing.T) {
	m := New(Config{DataDir: t.TempDir()})
	m.SetTunnelID("ae47ca01-7774-4fe9-b75a-abd27e303634")
	m.mu.Lock()
	m.state = StateStarting
	m.mu.Unlock()

	if m.Ready() {
		t.Fatal("还没有连接就报就绪")
	}
	if m.Hostname() == "" {
		t.Fatal("有隧道 id 时应当能给出 CNAME 目标")
	}

	m.onOutput("2026-10-06T13:06:52Z INF Registered tunnel connection connIndex=0 location=lax10 protocol=quic\n")
	if !m.Ready() {
		t.Fatal("看到 Registered tunnel connection 之后应当就绪")
	}
	if got := m.Status().Connections; got != 1 {
		t.Fatalf("连接数 = %d，期望 1", got)
	}
}

// 状态切换必须能反映"卡在哪一步"：缺二进制 / 缺授权 / 失败。
func TestStartReportsTheMissingPiece(t *testing.T) {
	// 没有任何 cloudflared 时：no_binary。
	empty := New(Config{DataDir: t.TempDir(), Binary: filepath.Join(t.TempDir(), "nope")})
	if err := empty.Start(context.Background()); err == nil {
		t.Fatal("没有 cloudflared 时应当报错")
	}
	// 这里只断言"不是 disabled"：具体是 no_binary 还是 no_account 取决于
	// 这台机器上装没装 cloudflared，而测试不该依赖开发机的环境。
	if empty.Status().State == StateDisabled {
		t.Fatal("应当落到一个缺东西的状态，而不是 disabled")
	}
	if empty.Status().LastError == "" {
		t.Fatal("必须给出原因，界面要显示它")
	}
}

// Stop 之后必须回到 disabled，且再来一次 Start 不会复用旧状态。
func TestStopReturnsToDisabled(t *testing.T) {
	m := New(Config{DataDir: t.TempDir()})
	m.SetTunnelID("ae47ca01-7774-4fe9-b75a-abd27e303634")
	m.mu.Lock()
	m.state = StateRunning
	m.mu.Unlock()

	m.Stop()
	if st := m.Status(); st.State != StateDisabled || st.Enabled {
		t.Fatalf("Stop 之后状态不对：%+v", st)
	}
	if m.Ready() {
		t.Fatal("停掉之后不该还报就绪")
	}
}
