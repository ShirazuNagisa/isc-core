package provider

import (
	"context"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/sysproxy"
)

// networkFailurePattern 匹配"根本没连上"这一类错误。
var networkFailurePattern = regexp.MustCompile(
	`(?i)connect|EOF|certificate|x509|timeout|no such host|connection refused|network is unreachable`)

// TestLiveCloudflareIsReachable 是一次真实的联网检查，默认跳过。
//
// 用 `ISC_LIVE_CHECK=1 go test ./internal/provider/ -run Live -v` 运行。
//
// 它存在的理由是一个已经发生过的故障：内核用 Go 写，而 Go 不读 macOS
// 的系统代理设置。用户开着代理软件时，所有出站请求都在直连，被本地
// 网络拦截后表现为"凭据验证失败"。那次排查花掉的力气，全都因为缺少
// 一个"内核到底连不连得上服务商"的检查。
//
// 断言方式刻意选得松：不要求凭据有效（这里用的就是假令牌），只要求
// **失败发生在 TLS 之后** —— 即拿到的是 Cloudflare 的拒绝，而不是
// 连接层错误。这样它不需要任何真实凭据就能长期有效。
func TestLiveCloudflareIsReachable(t *testing.T) {
	if os.Getenv("ISC_LIVE_CHECK") == "" {
		t.Skip("需要联网：设置 ISC_LIVE_CHECK=1 后运行")
	}
	t.Logf("内核识别的代理：%s", sysproxy.Describe())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := NewCloudflareVerifier().Verify(ctx, map[string]string{"token": "not-a-real-token"})
	if err == nil {
		t.Fatal("一个不存在的令牌不该通过校验")
	}
	msg := err.Error()
	t.Logf("Cloudflare 应答：%s", msg)
	if networkFailurePattern.MatchString(msg) {
		t.Fatalf("没能连上 Cloudflare（TLS/连接层失败），"+
			"通常是出站请求没有走系统代理：%s", msg)
	}
}
