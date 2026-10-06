package reachcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 成功路径：真的发出一次 https 请求，并记下状态码与延迟。
//
// 用 httptest 的 TLS 服务器 + 它自带的客户端（信任那张自签证书），
// 这样走的是与生产完全相同的代码路径 —— 包括 https 这一步。
func TestProbeRecordsSuccess(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	domain := srv.Listener.Addr().String()
	p := New(Config{
		Targets:  func(context.Context) []Target { return nil },
		Tunneled: func() bool { return true },
		Timeout:  3 * time.Second,
		Client:   srv.Client(),
	})
	p.check(context.Background(), Target{AppID: "a1", Name: "站点", Domain: domain})

	got := p.Latest()
	if len(got) != 1 {
		t.Fatalf("结果数 = %d", len(got))
	}
	r := got[0]
	if !r.OK || r.StatusCode != http.StatusOK {
		t.Fatalf("应当报可达：%+v", r)
	}
	if r.CheckedAt == "" {
		t.Fatal("结果里必须有检查时刻")
	}
	if r.ConsecutiveFailures != 0 {
		t.Fatalf("成功时不该有连续失败：%d", r.ConsecutiveFailures)
	}
	if !r.Trustworthy {
		t.Fatal("隧道开着时结果应当可信")
	}
}

// 失败必须被记录，且连续失败要累计 —— 界面靠它区分"抖了一下"和"真的挂了"。
func TestConsecutiveFailuresAccumulate(t *testing.T) {
	p := New(Config{
		Targets:  func(context.Context) []Target { return nil },
		Tunneled: func() bool { return true },
		Timeout:  time.Second,
	})
	target := Target{AppID: "a1", Name: "站点", Domain: "definitely-not-a-real-host.invalid"}

	for i := 1; i <= 3; i++ {
		p.check(context.Background(), target)
		got := p.Latest()
		if len(got) != 1 {
			t.Fatalf("结果数 = %d", len(got))
		}
		if got[0].OK {
			t.Fatalf("第 %d 次不该报成功", i)
		}
		if got[0].ConsecutiveFailures != i {
			t.Fatalf("第 %d 次的连续失败数 = %d", i, got[0].ConsecutiveFailures)
		}
		if got[0].Error == "" {
			t.Fatal("失败必须带上原因")
		}
	}
}

// 成功一次之后，连续失败计数必须归零 —— 否则"恢复了"这件事在界面上
// 永远显示不出来。
func TestSuccessResetsConsecutiveFailures(t *testing.T) {
	var fail bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	domain := srv.Listener.Addr().String()
	p := New(Config{
		Targets:  func(context.Context) []Target { return nil },
		Tunneled: func() bool { return true },
		Timeout:  2 * time.Second,
	})
	target := Target{AppID: "a1", Name: "站点", Domain: domain}
	// 这里绕开 https：用一个假 client 不好构造，于是直接验 record 的语义。
	p.record(target, Result{OK: false})
	p.record(target, Result{OK: false})
	if got := p.Latest()[0]; got.ConsecutiveFailures != 2 {
		t.Fatalf("连续失败 = %d", got.ConsecutiveFailures)
	}
	p.record(target, Result{OK: true, StatusCode: 200})
	if got := p.Latest()[0]; got.ConsecutiveFailures != 0 || !got.OK {
		t.Fatalf("成功之后应当归零：%+v", got)
	}
}

// 可信度必须跟着隧道状态走：没有隧道时，"本机访问自己的公网域名"走的是
// 发夹路径，什么也证明不了（见 internal/verify 的说明）。
func TestTrustworthinessFollowsTheTunnel(t *testing.T) {
	tunneled := false
	p := New(Config{
		Targets:  func(context.Context) []Target { return nil },
		Tunneled: func() bool { return tunneled },
		Timeout:  time.Second,
	})
	target := Target{AppID: "a1", Name: "站点", Domain: "example.invalid"}

	p.check(context.Background(), target)
	if got := p.Latest()[0]; got.Trustworthy {
		t.Fatal("没有隧道时不该报可信")
	}
	tunneled = true
	p.check(context.Background(), target)
	if got := p.Latest()[0]; !got.Trustworthy {
		t.Fatal("隧道开着时应当报可信")
	}
}

// Targets 每次都要重新取：站点会被增删，拿启动时的快照会让新站点
// 永远不被检查。
func TestTargetsAreReadEveryRound(t *testing.T) {
	calls := 0
	p := New(Config{
		Targets: func(context.Context) []Target {
			calls++
			return nil
		},
		Timeout: time.Second,
	})
	p.checkAll(context.Background())
	p.checkAll(context.Background())
	if calls != 2 {
		t.Fatalf("Targets 被调用了 %d 次，期望每轮一次", calls)
	}
}
