package platform

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
)

// mustAddr 解析一个地址，失败即终止测试。
func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("测试数据中的地址 %q 无法解析: %v", s, err)
	}
	return a
}

// TestParseEndpoint 覆盖地址解析。
//
// 这段逻辑很容易写错且错了以后症状离奇（连接挂起、连到错误的管道），
// 因此逐种写法都要钉住。
func TestParseEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		wantErr bool
		scheme  string
		addr    string
	}{
		// --- 命名管道 ---
		{
			name: "命名管道标准写法", in: "npipe://./pipe/isc-core",
			scheme: SchemeNamedPipe, addr: `\\.\pipe\isc-core`,
		},
		{
			name: "命名管道带前导斜杠", in: "npipe:////./pipe/isc-core",
			scheme: SchemeNamedPipe, addr: `\\.\pipe\isc-core`,
		},
		{
			name: "命名管道反斜杠写法", in: `npipe://\\.\pipe\isc-core`,
			scheme: SchemeNamedPipe, addr: `\\.\pipe\isc-core`,
		},
		{
			name: "命名管道缺少 pipe 前缀也接受", in: "npipe://isc-core",
			scheme: SchemeNamedPipe, addr: `\\.\isc-core`,
		},

		// --- Unix 套接字 ---
		{
			name: "Unix 套接字绝对路径", in: "unix:///var/lib/isc/run/isc.sock",
			scheme: SchemeUnix, addr: "/var/lib/isc/run/isc.sock",
		},
		{
			name: "Unix 套接字必须是绝对路径", in: "unix://relative/isc.sock",
			wantErr: true,
		},

		// --- 回环 TCP ---
		{
			name: "回环 IPv4", in: "tcp://127.0.0.1:52341",
			scheme: SchemeTCP, addr: "127.0.0.1:52341",
		},
		{
			name: "回环端口 0 表示自动分配", in: "tcp://127.0.0.1:0",
			scheme: SchemeTCP, addr: "127.0.0.1:0",
		},
		{
			name: "回环 IPv6", in: "tcp://[::1]:52341",
			scheme: SchemeTCP, addr: "[::1]:52341",
		},
		{
			name: "localhost 允许", in: "tcp://localhost:1234",
			scheme: SchemeTCP, addr: "localhost:1234",
		},

		// --- 拒绝非回环地址（安全约束）---
		{
			name: "拒绝 0.0.0.0", in: "tcp://0.0.0.0:1234", wantErr: true,
		},
		{
			name: "拒绝局域网地址", in: "tcp://192.168.1.10:1234", wantErr: true,
		},
		{
			name: "拒绝公网地址", in: "tcp://8.8.8.8:1234", wantErr: true,
		},

		// --- 畸形输入 ---
		{name: "空串", in: "", wantErr: true},
		{name: "缺少 scheme", in: "127.0.0.1:1234", wantErr: true},
		{name: "scheme 后为空", in: "npipe://", wantErr: true},
		{name: "未知 scheme", in: "http://127.0.0.1:1234", wantErr: true},
		{name: "TCP 缺少端口", in: "tcp://127.0.0.1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseEndpoint(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseEndpoint(%q) 期望报错，实际成功: %+v", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseEndpoint(%q) 意外报错: %v", tt.in, err)
			}
			if got.scheme != tt.scheme {
				t.Errorf("scheme = %q, 期望 %q", got.scheme, tt.scheme)
			}
			if got.addr != tt.addr {
				t.Errorf("addr = %q, 期望 %q", got.addr, tt.addr)
			}
		})
	}
}

func TestEndpointSchemeAndLoopback(t *testing.T) {
	t.Parallel()

	if !Endpoint("tcp://127.0.0.1:1").IsLoopback() {
		t.Error("tcp endpoint 应被判定为回环")
	}
	if Endpoint("npipe://./pipe/x").IsLoopback() {
		t.Error("命名管道不应被判定为回环")
	}
	if got := Endpoint("TCP://127.0.0.1:1").Scheme(); got != SchemeTCP {
		t.Errorf("scheme 应大小写不敏感，得到 %q", got)
	}
}

func TestHTTPBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   Endpoint
		want string
	}{
		{Endpoint("tcp://127.0.0.1:52341"), "http://127.0.0.1:52341"},
		{Endpoint("npipe://./pipe/isc-core"), "http://isc.local"},
		{Endpoint("unix:///run/isc.sock"), "http://isc.local"},
	}
	for _, tt := range tests {
		if got := tt.in.HTTPBaseURL(); got != tt.want {
			t.Errorf("%s.HTTPBaseURL() = %q, 期望 %q", tt.in, got, tt.want)
		}
	}
}

// TestLocalEndpoint 钉住各平台的默认地址形状。
func TestLocalEndpoint(t *testing.T) {
	t.Parallel()

	ep := LocalEndpoint("/var/lib/isc/run")
	if ep == "" {
		t.Fatal("LocalEndpoint 不应返回空串")
	}
	if _, err := parseEndpoint(ep.String()); err != nil {
		t.Fatalf("LocalEndpoint 返回了无法解析的地址 %q: %v", ep, err)
	}
}

// TestTCPListenAndDial 验证回环 TCP 通道能真正收发。
func TestTCPListenAndDial(t *testing.T) {
	t.Parallel()

	ln, err := Endpoint("tcp://127.0.0.1:0").Listen(context.Background())
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer ln.Close() //nolint:errcheck // 测试清理

	// 端口 0 由内核分配，必须回读真实地址。
	actual := Endpoint("tcp://" + ln.Addr().String())
	dial, err := actual.DialContext()
	if err != nil {
		t.Fatalf("构造拨号函数失败: %v", err)
	}

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
		close(accepted)
	}()

	conn, err := dial(context.Background(), "tcp", actual.String())
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer conn.Close() //nolint:errcheck // 测试清理

	peer, ok := <-accepted
	if !ok {
		t.Fatal("未收到连接")
	}
	defer peer.Close() //nolint:errcheck // 测试清理

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := peer.Read(buf); err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(buf) != "ping" {
		t.Errorf("读到 %q, 期望 \"ping\"", buf)
	}
}

// TestTransportNameLocalized 确认传输名称走了 i18n 而不是硬编码。
func TestTransportNameLocalized(t *testing.T) {
	t.Parallel()

	for _, ep := range []Endpoint{
		"npipe://./pipe/x", "unix:///run/x.sock", "tcp://127.0.0.1:1",
	} {
		name := ep.TransportName()
		if name == "" || name == "unknown" {
			t.Errorf("%s 的传输名称不应为 %q", ep, name)
		}
		// i18n 缺失时 T 会返回 key 本身，这是设计上的可见失败。
		if strings.Contains(name, "transport.") {
			t.Errorf("%s 的传输名称落回了 i18n key: %q", ep, name)
		}
	}
}

// TestIsGlobalIPv6 覆盖"哪些地址算可用于公网访问的 IPv6"。
//
// 判错会导致把链路本地或 ULA 地址写进 AAAA 记录，
// 从外部永远访问不到，且症状很难归因。
func TestIsGlobalIPv6(t *testing.T) {
	t.Parallel()

	tests := []struct {
		addr string
		want bool
	}{
		{"240e:3b0:1234:5600::1", true}, // 中国电信典型全局地址
		{"2001:4860:4860::8888", true},  // Google DNS
		{"fe80::1", false},              // 链路本地
		{"::1", false},                  // 回环
		{"fc00::1", false},              // ULA
		{"fd12:3456:789a::1", false},    // ULA
		{"ff02::1", false},              // 组播
		{"::ffff:192.168.1.1", false},   // IPv4-mapped
		{"192.168.1.1", false},          // 根本不是 IPv6
		{"::", false},                   // 未指定地址
	}
	for _, tt := range tests {
		addr := mustAddr(t, tt.addr)
		if got := IsGlobalIPv6(addr); got != tt.want {
			t.Errorf("IsGlobalIPv6(%s) = %v, 期望 %v", tt.addr, got, tt.want)
		}
	}
}

func TestInterfaceAddrsGlobalIPv6(t *testing.T) {
	t.Parallel()

	i := InterfaceAddrs{
		IPv6: []netip.Addr{
			mustAddr(t, "fe80::1"),
			mustAddr(t, "240e:3b0:1234:5600::1"),
			mustAddr(t, "fd00::1"),
			mustAddr(t, "240e:3b0:1234:5600::2"),
		},
	}
	got := i.GlobalIPv6()
	if len(got) != 2 {
		t.Fatalf("GlobalIPv6 应筛出 2 个地址，得到 %d 个: %v", len(got), got)
	}
	if got[0].String() != "240e:3b0:1234:5600::1" {
		t.Errorf("顺序应保持，得到 %v", got)
	}
}

func TestAddrEventIsPrefixEvent(t *testing.T) {
	t.Parallel()

	prefix := netip.MustParsePrefix("240e:3b0:1234:5600::/64")
	ev := AddrEvent{Kind: AddrChanged, Iface: "eth0", Prefix: prefix}
	if !ev.IsPrefixEvent() {
		t.Error("IPv6 前缀事件应被识别")
	}

	v4 := AddrEvent{Prefix: netip.MustParsePrefix("192.168.1.0/24")}
	if v4.IsPrefixEvent() {
		t.Error("IPv4 前缀不应被识别为前缀事件")
	}

	if !strings.Contains(ev.String(), "240e:3b0:1234:5600::/64") {
		t.Errorf("事件字符串应含前缀: %s", ev.String())
	}
}

func TestPortRange(t *testing.T) {
	t.Parallel()

	if got := NewPort(8080).String(); got != "8080" {
		t.Errorf("单端口应输出 \"8080\"，得到 %q", got)
	}
	if got := (PortRange{From: 8000, To: 8100}).String(); got != "8000-8100" {
		t.Errorf("端口范围输出错误: %q", got)
	}
	if (PortRange{From: 0, To: 0}).Valid() {
		t.Error("端口 0 不合法")
	}
	if (PortRange{From: 100, To: 99}).Valid() {
		t.Error("倒序范围不合法")
	}
	if !NewPort(80).Valid() {
		t.Error("端口 80 应合法")
	}
}

func TestUnsupportedBackendReportsUnavailable(t *testing.T) {
	t.Parallel()

	b := Current()
	if b == nil {
		t.Fatal("Current 不应返回 nil")
	}
	// 无论平台，传输后端都必须可用 —— 没有它内核根本无法被管理。
	tx := b.Capabilities().Transport
	if !tx.Available {
		t.Errorf("传输后端必须可用，得到 %+v", tx)
	}
	if b.OS == "" || b.Arch == "" {
		t.Error("Bundle 应填充 OS 与 Arch")
	}
}
