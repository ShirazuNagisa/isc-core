// Package tier1 是 Tier-1 DNS 服务商的完整记录管理实现。
//
// 与 Tier-2 的分工（见 docs/DECISIONS.md D04）：
//
//	Tier-1  完整记录 CRUD：列区域、列记录、增删改任意记录类型、TTL、代理开关
//	Tier-2  仅 A/AAAA 动态解析（由 internal/ddnsgo 的移植代码提供）
//
// # 为什么不复用移植过来的 ddns-go 实现
//
// 上游的实现**只做一件事**：把 A/AAAA 记录更新到当前 IP。查询、新增、
// 修改的逻辑都写死在 A/AAAA 上，没有删除、没有其它记录类型、没有区域列表。
// 「域名解析管理」这部分等于要重写。
//
// 能复用的部分已经复用了：签名算法（阿里云 RPC 签名、腾讯云 TC3、
// 华为云 SDK-HMAC-SHA256）直接调 internal/ddnsgo 里那份经过验证的代码，
// 不重写第二遍。
package tier1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/ddnsgo"
	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// maxResponseBytes 是响应体读取上限。
//
// 设上限是必要的：服务商返回一个超大响应（或被中间设备劫持）时，
// 不该把内核的内存吃掉 —— 而内核崩掉意味着用户的域名解析静默停摆。
const maxResponseBytes = 4 << 20 // 4 MB

// requestTimeout 是单次 API 调用的超时。
//
// 30 秒的依据：记录管理是交互式操作，用户在界面上等着结果；
// 超过 30 秒还没回，用户已经认为它坏了。
const requestTimeout = 30 * time.Second

// APIError 是服务商返回的错误。
//
// 单独一个类型而不是 fmt.Errorf：接口层需要据此区分"凭据不对"
// 与"记录不存在"这类语义，而靠错误文本判断是脆弱的。
type APIError struct {
	// Status 是 HTTP 状态码。
	Status int
	// Code 是服务商自己的错误码（若有）。
	Code string
	// Message 是服务商的错误说明。
	Message string
	// Op 是出错的操作，便于定位。
	Op string
}

// Error 实现 error。
func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s: HTTP %d, %s: %s", e.Op, e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Op, e.Status, e.Message)
}

// IsNotFound 报告错误是否为"资源不存在"。
//
// 各服务商对 404 的表达不一致，但 HTTP 状态码是可靠的共同点。
func (e *APIError) IsNotFound() bool { return e.Status == http.StatusNotFound }

// IsUnauthorized 报告错误是否为鉴权失败。
func (e *APIError) IsUnauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// client 是各 Tier-1 实现共用的 HTTP 客户端封装。
type client struct {
	http    *http.Client
	baseURL string
	// headers 是每次请求都要带上的头（鉴权信息之外的公共头）。
	headers map[string]string
}

// newClient 构造 HTTP 客户端。
//
// httpInterface 非空时把请求绑定到指定网卡 —— 与移植过来的动态解析
// 实现保持一致的行为，用户在那边选了网卡，在这边也该生效。
func newClient(baseURL, httpInterface string) *client {
	hc := ddnsgo.CreateHTTPClientWithInterface(httpInterface)
	hc.Timeout = requestTimeout
	return &client{
		http:    hc,
		baseURL: strings.TrimSuffix(baseURL, "/"),
		headers: map[string]string{"Accept": "application/json"},
	}
}

// URL 拼接一个 API 路径。
func (c *client) URL(path string) string {
	return c.baseURL + path
}

// doJSON 发起一次请求并把 JSON 响应解析到 out。
//
// body 为 nil 表示无请求体；out 为 nil 表示不解析响应体（例如 DELETE）。
func (c *client) doJSON(ctx context.Context, op, method, url string,
	headers map[string]string, body, out any) error {

	var reader io.Reader
	if body != nil {
		byt, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s: 序列化请求体失败: %w", op, err)
		}
		reader = bytes.NewReader(byt)
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return fmt.Errorf("%s: 构造请求失败: %w", op, err)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: 请求失败: %w", op, err)
	}
	defer resp.Body.Close() //nolint:errcheck // 只读响应，关闭失败无影响

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%s: 读取响应失败: %w", op, err)
	}

	if resp.StatusCode >= 400 {
		// 错误响应也要解析：服务商的错误说明通常比状态码有用得多
		// （例如"记录已存在""域名不在该账号下"）。
		return &APIError{
			Status:  resp.StatusCode,
			Code:    extractErrorCode(raw),
			Message: extractErrorMessage(raw),
			Op:      op,
		}
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: 解析响应失败: %w", op, err)
	}
	return nil
}

// extractErrorMessage 尽力从错误响应里取出可读的说明。
//
// 各家的错误体结构差异极大（Cloudflare 的 {success,errors[]}、
// 阿里云的 {Code,Message}、腾讯云的 {Response:{Error:{Message}}}），
// 因此这里做一次广度优先的字段名搜索，而不是为每家写一套解析 ——
// 目标是"给用户一句能看懂的话"，不是精确还原错误对象。
func extractErrorMessage(raw []byte) string {
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return truncateForMessage(string(raw))
	}

	if msg := searchMessage(probe, 0); msg != "" {
		return msg
	}
	return truncateForMessage(string(raw))
}

// extractErrorCode 尽力取出服务商的错误码。
func extractErrorCode(raw []byte) string {
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	for _, key := range []string{"code", "Code", "error_code", "status"} {
		if v, ok := probe[key]; ok {
			return fmt.Sprint(v)
		}
	}
	return ""
}

// messageKeys 是各家服务商表示"错误说明"的字段名。
var messageKeys = []string{"message", "Message", "msg", "error_description", "detail"}

// searchMessage 在嵌套结构里广度优先地找一条错误说明。
//
// 深度限制为 4：足够覆盖各家那两三层嵌套，又不会被一个畸形响应
// 拖进无意义的深层遍历。
func searchMessage(m map[string]any, depth int) string {
	if depth > 4 {
		return ""
	}
	for _, key := range messageKeys {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	// errors 数组（Cloudflare 风格）
	if errs, ok := m["errors"].([]any); ok && len(errs) > 0 {
		if first, ok := errs[0].(map[string]any); ok {
			if msg := searchMessage(first, depth+1); msg != "" {
				return msg
			}
		}
	}
	// 递归进每一层对象
	for _, v := range m {
		switch child := v.(type) {
		case map[string]any:
			if msg := searchMessage(child, depth+1); msg != "" {
				return msg
			}
		case []any:
			for _, item := range child {
				if obj, ok := item.(map[string]any); ok {
					if msg := searchMessage(obj, depth+1); msg != "" {
						return msg
					}
				}
			}
		}
	}
	return ""
}

// truncateForMessage 把无法解析的响应截断成一句可读的话。
//
// 必须截断且**不能原样返回**：响应体可能很长，也可能含有账号信息，
// 而它会进入日志与审计。
func truncateForMessage(s string) string {
	s = strings.TrimSpace(s)
	const limit = 200
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// ---------------------------------------------------------------------------
// 记录类型
// ---------------------------------------------------------------------------

// 各家 API 对记录类型的表达基本一致（都是大写字符串），
// 因此不需要转换表。这里只列出**不支持**的类型判据。
//
// supportedRecordType 报告某个类型是否值得发给服务商。
//
// 不做"支持哪些类型"的白名单：各家的支持范围随产品演进，
// 硬编码白名单会让"服务商新支持了 HTTPS 记录"变成一次内核发版。
// 交给服务商去拒绝，并把它的错误如实转达给用户 —— 那比我们自己猜更准确。
func supportedRecordType(t dns.RecordType) bool {
	return strings.TrimSpace(string(t)) != ""
}

// normalizeTTL 把 TTL 归一到服务商能接受的形态。
//
// 0 表示"交给服务商默认值"，由各家实现自行决定怎么表达
// （Cloudflare 用 1 表示 auto，阿里云用默认值，等等）。
func normalizeTTL(ttl int) int {
	if ttl < 0 {
		return 0
	}
	return ttl
}

// remarshal 把一个通用 JSON 对象转成具体结构体。
//
// 为什么需要它：响应信封里的 result 是动态类型（有的接口返回对象、
// 有的返回数组），而 Go 没有泛型方法 —— 给每种结果写一个信封类型
// 会让代码长得多。这里用一次 JSON 往返换取结构体的类型安全，
// 代价是每条记录多一次序列化，而记录数量是几十到几百，可以忽略。
func remarshal(from map[string]any, to any) error {
	byt, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(byt, to)
}

// ---------------------------------------------------------------------------
// 动态解析转发
// ---------------------------------------------------------------------------

// dynamicDelegate 给各家 Tier-1 实现补上"动态解析"能力。
//
// # 为什么需要它
//
// Tier-1 的五家在移植的 ddns-go 代码里**也有**动态解析实现，而记录管理
// 是本包新写的。若两者各占一个对象，注册表里就只能存一个 —— 能力位会
// 只反映其中一个，界面上出现"支持列记录但不支持动态解析"这种与实际
// 不符的组合，用户对着它完全无法判断该做什么。
//
// 做法：让记录管理实现内嵌一个转发器，由装配层把移植过来的动态解析
// 实现注入进来。这样**一个对象同时具备两种能力**，能力位因此完整。
//
// # 为什么不用"嵌入接口"
//
// 曾经试过 combined struct { dns.Provider; dns.DynamicUpdater } ——
// 那是错的：嵌入**接口**只提升该接口自己的方法（这里是 dns.Provider
// 的 Meta），记录增删改查的方法全都传不出来，能力位依旧是残缺的。
// 嵌入一个**具体结构**才会提升它的全部方法。
type dynamicDelegate struct {
	updater dns.DynamicUpdater
}

// SetDynamicUpdater 注入动态解析实现。由装配层在启动时调用一次。
func (d *dynamicDelegate) SetDynamicUpdater(u dns.DynamicUpdater) {
	d.updater = u
}

// UpdateDynamic 实现 dns.DynamicUpdater。
//
// 转发出错时返回一条明确的信息，而不是一个 nil 解引用 ——
// 未注入只可能是装配漏了，而那需要一句能定位的话。
func (d *dynamicDelegate) UpdateDynamic(
	ctx context.Context, cred dns.Credential, req dns.DynamicRequest,
) (dns.DynamicResult, error) {
	if d.updater == nil {
		return dns.DynamicResult{}, errors.New("tier1: 动态解析实现未接入（装配遗漏）")
	}
	return d.updater.UpdateDynamic(ctx, cred, req)
}
