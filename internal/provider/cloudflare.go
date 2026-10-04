package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/provider/tier1"
	"github.com/ShirazuNagisa/isc-core/internal/sysproxy"
)

// cloudflareAPIBase 是 Cloudflare API 的默认基址。
const cloudflareAPIBase = "https://api.cloudflare.com/client/v4"

// verifyTimeout 是凭据校验的超时。
//
// 比常规请求短：用户在界面上点"测试连接"时会一直看着转圈，
// 15 秒还没结果就已经算是交互事故了。
const verifyTimeout = 15 * time.Second

// outboundTransport 是校验器访问服务商 API 用的传输层。
//
// 这里必须显式指定 Proxy：走 http.DefaultTransport 只会读环境变量，
// 而 macOS 用户的代理配置在系统设置里。少了这一行的后果不是"没走代理"
// 这么轻描淡写 —— 用户开着代理时，直连出去会被本地网络拦截，
// 界面上显示的是"凭据验证失败"，用户会一路去怀疑自己的 API Token。
var outboundTransport = &http.Transport{
	Proxy:                 sysproxy.Func(),
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          10,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

// newOutboundClient 构造使用统一传输层的客户端。
func newOutboundClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: outboundTransport}
}

// CloudflareVerifier 校验 Cloudflare API Token。
//
// 之所以在 M1 就实现它：凭据的整条链路（界面输入 → 加密落库 →
// 重启后解密 → 拿去调用真实 API）需要一个端到端的证据，
// 否则"能存能读"只证明了数据库可用，没证明凭据真的能用。
type CloudflareVerifier struct {
	// BaseURL 允许测试指向本地假服务器；为空时用官方地址。
	BaseURL string
	// Client 允许测试注入；为空时用带超时的默认客户端。
	Client *http.Client
}

// NewCloudflareVerifier 构造使用默认配置的校验器。
func NewCloudflareVerifier() CloudflareVerifier {
	return CloudflareVerifier{
		BaseURL: cloudflareAPIBase,
		Client:  newOutboundClient(verifyTimeout),
	}
}

// cloudflareVerifyResponse 是 /user/tokens/verify 的响应。
type cloudflareVerifyResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result struct {
		Status string `json:"status"`
	} `json:"result"`
}

// Verify 实现 Verifier。
//
// 调用 `GET /user/tokens/verify` —— 这是 Cloudflare 专门为
// "这段 token 还有效吗"提供的只读端点，不会创建或修改任何资源。
// 用"列一下 zones"来校验是常见的错误做法：那需要额外的权限，
// 会让只有 DNS 编辑权限的最小权限 token 被误判为无效。
func (v CloudflareVerifier) Verify(ctx context.Context, fields map[string]string) error {
	token := strings.TrimSpace(fields["token"])
	if token == "" {
		return errors.New(i18n.T("provider.cf.need_token"))
	}

	base := v.BaseURL
	if base == "" {
		base = cloudflareAPIBase
	}
	client := v.Client
	if client == nil {
		client = newOutboundClient(verifyTimeout)
	}

	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(base, "/")+"/user/tokens/verify", nil)
	if err != nil {
		return fmt.Errorf(i18n.T("provider.cf.build_failed"), err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf(i18n.T("provider.cf.connect_failed"), err)
	}
	defer resp.Body.Close() //nolint:errcheck // 只读响应，关闭失败无影响

	// 限制读取量：对方返回一个超大响应时不该把内核的内存吃掉。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Errorf(i18n.T("provider.cf.read_failed"), err)
	}

	var parsed cloudflareVerifyResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		// 不把响应体塞进错误信息：它可能含有账号信息，
		// 而错误信息会进入日志与审计。
		return fmt.Errorf(i18n.T("provider.cf.bad_body"), resp.StatusCode)
	}

	if !parsed.Success {
		codes := make([]int, 0, len(parsed.Errors))
		for _, e := range parsed.Errors {
			codes = append(codes, e.Code)
		}
		if tier1.CloudflareGlobalAPIKeyHint(token, codes...) {
			return errors.New(i18n.T("provider.cf.global_key"))
		}
		return fmt.Errorf(i18n.T("provider.cf.rejected"), cloudflareErrorMessage(parsed))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf(i18n.T("provider.cf.bad_status"), resp.StatusCode)
	}
	// status 为 active 才算真正可用；其它值（如 disabled）意味着
	// token 存在但已被停用，此时报"成功"会误导用户。
	if parsed.Result.Status != "" && parsed.Result.Status != "active" {
		return fmt.Errorf(i18n.T("provider.cf.not_active"), parsed.Result.Status)
	}
	return nil
}

// cloudflareErrorMessage 提取可读的错误信息。
func cloudflareErrorMessage(r cloudflareVerifyResponse) string {
	if len(r.Errors) == 0 {
		return i18n.T("provider.cf.no_detail")
	}
	parts := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
		parts = append(parts, fmt.Sprintf("%d %s", e.Code, e.Message))
	}
	return strings.Join(parts, "; ")
}
