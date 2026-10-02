package reach

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// 本文件覆盖端口层诊断。
//
// 两项检查各自防的是一种最难排查的失败：
//
//	端口上没有服务
//	  防火墙规则开好了，用户从外面访问却什么都不通。他会去查路由器、
//	  查运营商、查 DNS —— 而真正的原因是本机上根本没有服务在监听。
//
//	没有权限绑低端口
//	  用户在反代里配了 443、界面上显示"已启用"，而后台每次启动都失败。
//	  他会以为是自己配置错了，而不是权限不够。

// ---------------------------------------------------------------------------
// 监听探测
// ---------------------------------------------------------------------------

// TestCheckListeningDetectsListener 验证能发现真的在监听的服务。
func TestCheckListeningDetectsListener(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck // 测试清理

	// 必须真的接受连接，否则探测会超时而不是立刻成功。
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	res := CheckListening(context.Background(), port, "tcp")

	if !res.Listening {
		t.Errorf("应当探测到监听，得到: %s", res.Detail)
	}
	if !strings.Contains(res.Detail, "有服务在监听") {
		t.Errorf("提示不明确: %s", res.Detail)
	}
}

// TestCheckListeningDetectsNoListener 验证能发现端口上什么都没有。
//
// # 怎么拿到一个"确定没人监听"的端口
//
// 先绑一个端口再立刻释放 —— 操作系统随后不会再把它分配给别的进程
// （短时间内），因此它是安全的。直接用一个固定的高位端口是不行的：
// 那可能在别的机器上被占用，测试会随机失败。
func TestCheckListeningDetectsNoListener(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	res := CheckListening(context.Background(), port, "tcp")
	if res.Listening {
		t.Errorf("端口 %d 上不该探测到监听", port)
	}
	if !strings.Contains(res.Detail, "没有服务在监听") {
		t.Errorf("提示不明确: %s", res.Detail)
	}
}

// TestCheckListeningOnIPv6Only 钉住"只监听 IPv6 的服务也要被发现"。
//
// 只试 127.0.0.1 会把一个只绑了 ::1 的服务报成"没有服务" ——
// 而那是个**假警报**，它会引导用户去启动一个已经在跑的服务。
func TestCheckListeningOnIPv6Only(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("本机不支持 IPv6 回环: %v", err)
	}
	defer ln.Close() //nolint:errcheck // 测试清理

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	res := CheckListening(context.Background(), port, "tcp")

	if !res.Listening {
		t.Errorf("只监听 IPv6 回环的服务应当被发现，得到: %s", res.Detail)
	}
}

// TestCheckListeningRejectsBadPort 验证端口范围检查。
func TestCheckListeningRejectsBadPort(t *testing.T) {
	t.Parallel()

	for _, port := range []int{0, -1, 65536, 99999} {
		res := CheckListening(context.Background(), port, "tcp")
		if res.Listening {
			t.Errorf("端口 %d 不该被报告为有监听", port)
		}
		if !strings.Contains(res.Detail, "不在合法范围") {
			t.Errorf("端口 %d 的提示不对: %s", port, res.Detail)
		}
	}
}

// TestCheckListeningUDPIsHonest 钉住 UDP 的处理。
//
// UDP 没有"连接"的概念，因此**不能**用连一下来判断。
// 这里刻意报告"无法探测"而不是猜一个结论 —— 给错的结论会让用户
// 去查错的地方。
func TestCheckListeningUDPIsHonest(t *testing.T) {
	t.Parallel()

	res := CheckListening(context.Background(), 5353, "udp")
	if res.Listening {
		t.Error("UDP 不该被报告为「有监听」—— 那是猜的")
	}
	if !strings.Contains(res.Detail, "无法") {
		t.Errorf("应当说明无法探测，得到: %s", res.Detail)
	}
}

// TestCheckListeningHonorsContext 验证探测会被中断。
//
// 探测有 700ms 的超时，串行检查多项时累积起来会让界面卡顿 ——
// 而用户点了取消却还在等，是很容易被注意到的问题。
func TestCheckListeningHonorsContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立刻取消

	start := time.Now()
	CheckListening(ctx, 9, "tcp") // 9 号端口（discard）几乎肯定没人监听
	elapsed := time.Since(start)

	// 不该等满两个地址各 700ms 的超时。
	if elapsed > 2*livingTimeout() {
		t.Errorf("上下文取消后仍然等了 %v", elapsed)
	}
}

func livingTimeout() time.Duration { return listeningTimeout }

// ---------------------------------------------------------------------------
// 诊断项
// ---------------------------------------------------------------------------

func TestDiagnosePortNoListenerIsWarningNotFailure(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	checks := DiagnosePort(context.Background(), Request{Port: port, Protocol: "tcp"}, nil)

	var found bool
	for _, c := range checks {
		if c.Name != "服务监听" {
			continue
		}
		found = true

		// **警告而不是失败**：用户可能正要启动那个服务。
		//
		// 做成失败会拦住一个完全合理的操作顺序（先开规则、再起服务），
		// 而那个顺序在"先把网络配好再部署服务"的流程里很自然。
		if c.Status != CheckWarn {
			t.Errorf("没有监听应当是警告而不是 %s", c.Status)
		}
		// 提示必须说清后果 —— 那是用户唯一能据此行动的线索。
		if !strings.Contains(c.Hint, "从外面访问") {
			t.Errorf("提示应当说明后果: %s", c.Hint)
		}
	}
	if !found {
		t.Fatal("应当产出「服务监听」这一项")
	}
}

func TestDiagnosePortWithListenerPasses(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck // 测试清理

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	checks := DiagnosePort(context.Background(), Request{Port: port, Protocol: "tcp"}, nil)

	for _, c := range checks {
		if c.Name == "服务监听" && c.Status != CheckPass {
			t.Errorf("有服务在监听时应当是 pass，得到 %s: %s", c.Status, c.Detail)
		}
	}
}

func TestDiagnosePortRangeFails(t *testing.T) {
	t.Parallel()

	checks := DiagnosePort(context.Background(), Request{Port: 70000}, nil)
	if len(checks) != 1 {
		t.Fatalf("非法端口应当只产出一项，得到 %d 项", len(checks))
	}
	if checks[0].Status != CheckFail {
		t.Errorf("非法端口应当是 fail，得到 %s", checks[0].Status)
	}
}

func TestDiagnosePortMapping(t *testing.T) {
	t.Parallel()

	// 直通。
	checks := DiagnosePort(context.Background(),
		Request{Port: 8443, Protocol: "tcp", UpstreamPort: 8443}, nil)
	var detail string
	for _, c := range checks {
		if c.Name == "端口映射" {
			detail = c.Detail
		}
	}
	if !strings.Contains(detail, "直通") {
		t.Errorf("相同端口应当标为直通，得到 %q", detail)
	}

	// 非标端口入口。
	checks = DiagnosePort(context.Background(),
		Request{Port: 8443, Protocol: "tcp", UpstreamPort: 443}, nil)
	detail = ""
	var hint string
	for _, c := range checks {
		if c.Name == "端口映射" {
			detail, hint = c.Detail, c.Hint
		}
	}
	if !strings.Contains(detail, "8443 → 443") && !strings.Contains(detail, "→") {
		t.Errorf("映射关系不明确: %q", detail)
	}
	// 用户需要知道访问时要带端口号 —— 否则他会以为配错了。
	if !strings.Contains(hint, "端口号") {
		t.Errorf("应当提示访问时要带端口号: %q", hint)
	}
}

// ---------------------------------------------------------------------------
// 低端口权限
// ---------------------------------------------------------------------------

// fakeBinder 是可控的低端口权限检测器。
type fakeBinder struct {
	canBind bool
	note    string
}

func (f fakeBinder) CanBindLowPorts() bool { return f.canBind }
func (f fakeBinder) Describe() platform.ImplState {
	return platform.ImplState{
		Available: true, Backend: "cap_net_bind_service", Note: f.note,
	}
}

// TestLowPortWithoutPermissionIsFailure 钉住这一项是**失败**而不是警告。
//
// 没有权限就一定绑不上，而重试、等一会儿、改配置都不会让它变好 ——
// 让用户先应用再看它失败只是浪费一次系统变更。
func TestLowPortWithoutPermissionIsFailure(t *testing.T) {
	t.Parallel()

	binder := fakeBinder{canBind: false, note: "未检测到 CAP_NET_BIND_SERVICE"}

	checks := DiagnosePort(context.Background(),
		Request{Port: 443, Protocol: "tcp"}, binder)

	var found bool
	for _, c := range checks {
		if c.Name != "低端口权限" {
			continue
		}
		found = true

		if c.Status != CheckFail {
			t.Errorf("没有权限时应当是 fail，得到 %s", c.Status)
		}
		// 提示必须给出**可执行的**做法，而且不止一种。
		for _, want := range []string{"setcap", "root", "1024"} {
			if !strings.Contains(c.Hint, want) {
				t.Errorf("提示里缺少 %q: %s", want, c.Hint)
			}
		}
	}
	if !found {
		t.Fatal("应当产出「低端口权限」这一项")
	}
}

func TestLowPortWithPermissionPasses(t *testing.T) {
	t.Parallel()

	binder := fakeBinder{canBind: true, note: "以 root 运行"}

	checks := DiagnosePort(context.Background(),
		Request{Port: 443, Protocol: "tcp"}, binder)

	for _, c := range checks {
		if c.Name == "低端口权限" && c.Status != CheckPass {
			t.Errorf("有权限时应当是 pass，得到 %s: %s", c.Status, c.Detail)
		}
	}
}

// TestHighPortSkipsPermissionCheck 验证高位端口不触发权限检测。
//
// 对 8443 报告"低端口权限不足"是无意义的噪音，而噪音会让用户
// 忽略真正重要的那几条。
func TestHighPortSkipsPermissionCheck(t *testing.T) {
	t.Parallel()

	// 故意给一个"没有权限"的检测器。
	binder := fakeBinder{canBind: false, note: "没有权限"}

	checks := DiagnosePort(context.Background(),
		Request{Port: 8443, Protocol: "tcp"}, binder)

	for _, c := range checks {
		if c.Name == "低端口权限" {
			if c.Status != CheckPass {
				t.Errorf("高位端口应当是 pass，得到 %s: %s", c.Status, c.Detail)
			}
			if !strings.Contains(c.Detail, "不是特权端口") {
				t.Errorf("说明不对: %s", c.Detail)
			}
		}
	}
}

func TestNilBinderSkipsCheck(t *testing.T) {
	t.Parallel()

	checks := DiagnosePort(context.Background(),
		Request{Port: 443, Protocol: "tcp"}, nil)

	for _, c := range checks {
		if c.Name == "低端口权限" {
			t.Error("未提供检测器时不该产出低端口权限项")
		}
	}
}

// ---------------------------------------------------------------------------
// 边界
// ---------------------------------------------------------------------------

func TestLowPortBoundary(t *testing.T) {
	t.Parallel()

	// 1023 是特权端口，1024 不是。
	// 这个边界很容易写成 <=1024 或 <1000，而错了之后
	// 用户会在 1024 上收到一条无意义的警告。
	binder := fakeBinder{canBind: false, note: "没有权限"}

	for port, wantFail := range map[int]bool{
		443:  true,
		1023: true,
		1024: false,
		8443: false,
	} {
		checks := DiagnosePort(context.Background(),
			Request{Port: port, Protocol: "tcp"}, binder)

		for _, c := range checks {
			if c.Name != "低端口权限" {
				continue
			}
			gotFail := c.Status == CheckFail
			if gotFail != wantFail {
				t.Errorf("端口 %d：期望 fail=%v，得到 %s（%s）",
					port, wantFail, c.Status, c.Detail)
			}
		}
	}
}

// TestDiagnosePortOrderIsBottomUp 验证检测顺序符合"从下到上"。
//
// 用户按这个顺序逐项修复即可，而颠倒顺序会让他去修一个当前
// 根本用不上的环节。
func TestDiagnosePortOrderIsBottomUp(t *testing.T) {
	t.Parallel()

	checks := DiagnosePort(context.Background(),
		Request{Port: 443, Protocol: "tcp"}, fakeBinder{canBind: true})

	if len(checks) < 2 {
		t.Fatalf("应当至少有两项检测，得到 %d", len(checks))
	}

	// 服务监听在前，低端口权限在后。
	var names []string
	for _, c := range checks {
		names = append(names, c.Name)
	}

	idxListening, idxLowPort := -1, -1
	for i, n := range names {
		if n == "服务监听" {
			idxListening = i
		}
		if n == "低端口权限" {
			idxLowPort = i
		}
	}
	if idxListening < 0 || idxLowPort < 0 {
		t.Fatalf("缺少检测项: %v", names)
	}
	if idxListening > idxLowPort {
		t.Errorf("「服务监听」应当排在「低端口权限」之前: %v", names)
	}
}

// TestDiagnosePortIsConcurrencySafe 验证并发调用不会互相干扰。
//
// 界面可能同时检查多个端口，而共享状态会让结果串台 ——
// 那会表现为"有时报有服务、有时报没有"，极难复现。
func TestDiagnosePortIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck // 测试清理

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	open := ln.Addr().(*net.TCPAddr).Port

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			DiagnosePort(context.Background(),
				Request{Port: open, Protocol: "tcp"}, fakeBinder{canBind: true})
			DiagnosePort(context.Background(),
				Request{Port: 9, Protocol: "tcp"}, fakeBinder{canBind: true})
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// 文案拼接
// ---------------------------------------------------------------------------

// TestJoinCheckText 守住的是一类**只有拼起来才看得出来**的缺陷。
//
// 两条文案各自都通顺，而直接用一个空格拼接会读成连体句：
// 事实那句通常不以标点结尾，而提示那句是一个完整句子。
// 这是真机上跑出来才发现的。
func TestJoinCheckText(t *testing.T) {
	t.Parallel()

	cases := []struct {
		detail, hint, want string
	}{
		// 事实句没有标点：补一个句号，否则两句会粘在一起。
		{"端口 80 上没有服务在监听", "先启动服务", "端口 80 上没有服务在监听。 先启动服务"},
		// 已经有标点：不重复添加。
		{"端口 80 上没有服务在监听。", "先启动服务", "端口 80 上没有服务在监听。 先启动服务"},
		{"事实！", "提示", "事实！ 提示"},
		{"fact.", "hint", "fact. hint"},
		// 只有一边有内容时不该多出标点。
		{"", "只有提示", "只有提示"},
		{"只有事实", "", "只有事实"},
		{"", "", ""},
	}

	for _, tc := range cases {
		got := joinCheckText(tc.detail, tc.hint)
		if got != tc.want {
			t.Errorf("joinCheckText(%q, %q) = %q\n期望 %q",
				tc.detail, tc.hint, got, tc.want)
		}
	}
}

func TestJoinCheckTextTrimsSpace(t *testing.T) {
	t.Parallel()

	got := joinCheckText("  事实  ", "  提示  ")
	if got != "事实。 提示" {
		t.Errorf("应当去掉两端空白，得到 %q", got)
	}
}
