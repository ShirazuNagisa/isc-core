package tier1

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// cloudflareDefaultBase 是 Cloudflare API 的默认基址。
const cloudflareDefaultBase = "https://api.cloudflare.com/client/v4"

// perPage 是分页拉取时每页的条数。
//
// Cloudflare 的上限是 100（zones 是 50）。取上限能把往返次数压到最少，
// 而"账号下有几千条记录"是完全可能的。
const (
	zonesPerPage   = 50
	recordsPerPage = 100
)

// maxPages 是分页拉取的页数上限。
//
// 存在意义是防止一个畸形响应（result_info 恒返回"还有下一页"）
// 把内核拖进死循环。100 页 × 100 条 = 一万条记录，远超个人账号的实际规模。
const maxPages = 100

// Cloudflare 实现 Cloudflare 的完整记录管理。
//
// 它同时实现了 dns.Verifier —— Cloudflare 提供了专门的只读令牌校验端点，
// 因此"测试连接"不需要产生任何副作用。
//
// 内嵌 dynamicDelegate 让同一个对象也具备动态解析能力：Tier-1 五家在
// 移植的 ddns-go 代码里都有动态解析实现，把两者合成一个对象，能力位
// 才是完整的。见 tier1.go 中 dynamicDelegate 的说明。
type Cloudflare struct {
	dynamicDelegate
	meta    dns.Meta
	baseURL string
}

// 编译期断言：接口实现必须完整。
var (
	_ dns.Provider       = (*Cloudflare)(nil)
	_ dns.Verifier       = (*Cloudflare)(nil)
	_ dns.ZoneLister     = (*Cloudflare)(nil)
	_ dns.RecordLister   = (*Cloudflare)(nil)
	_ dns.RecordCreator  = (*Cloudflare)(nil)
	_ dns.RecordUpdater  = (*Cloudflare)(nil)
	_ dns.RecordDeleter  = (*Cloudflare)(nil)
	_ dns.DynamicUpdater = (*Cloudflare)(nil)
)

// NewCloudflare 构造 Cloudflare 实现。
//
// baseURL 为空时使用官方地址；测试传入本地假服务器地址。
// 把地址做成参数而不是包级变量：测试因此可以并行，也不会互相污染。
func NewCloudflare(baseURL string) *Cloudflare {
	if baseURL == "" {
		baseURL = cloudflareDefaultBase
	}
	return &Cloudflare{
		meta: dns.Meta{
			Name:        "cloudflare",
			DisplayName: "Cloudflare",
			Tier:        1,
		},
		baseURL: baseURL,
	}
}

// Meta 实现 dns.Provider。
func (c *Cloudflare) Meta() dns.Meta { return c.meta }

// clientFor 为一次调用构造带鉴权的客户端。
func (c *Cloudflare) clientFor(cred dns.Credential, httpInterface string) *client {
	cl := newClient(c.baseURL, httpInterface)
	cl.headers["Authorization"] = "Bearer " + cred.Field("token")
	return cl
}

// ---------------------------------------------------------------------------
// 响应结构
// ---------------------------------------------------------------------------

type cfEnvelope struct {
	Success    bool          `json:"success"`
	Errors     []cfError     `json:"errors"`
	Messages   []string      `json:"messages"`
	Result     any           `json:"result"`
	ResultInfo *cfResultInfo `json:"result_info"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cfResultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalPages int `json:"total_pages"`
	Count      int `json:"count"`
	TotalCount int `json:"total_count"`
}

type cfZone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type cfRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
	Comment  string `json:"comment"`
	Priority *int   `json:"priority"`
}

// ---------------------------------------------------------------------------
// 凭据校验
// ---------------------------------------------------------------------------

// Verify 实现 dns.Verifier。
//
// 调用 GET /user/tokens/verify —— Cloudflare 专门为"这段 token 还有效吗"
// 提供的只读端点。用"列一下 zones"来校验是常见的错误做法：那需要额外的
// 权限，会让只有 DNS 编辑权限的最小权限 token 被误判为无效。
func (c *Cloudflare) Verify(ctx context.Context, cred dns.Credential) error {
	token := strings.TrimSpace(cred.Field("token"))
	if token == "" {
		return errors.New(i18n.T("tier1.cf.need_token"))
	}

	cl := c.clientFor(cred, "")
	var env cfEnvelope
	if err := cl.doJSON(ctx, i18n.T("tier1.op.verify"), http.MethodGet,
		cl.URL("/user/tokens/verify"), nil, nil, &env); err != nil {
		// 鉴权被拒是 4xx，会先走这条路径 —— Global API Key 就在其中。
		if hint := cloudflareGlobalKeyError(token, err); hint != nil {
			return hint
		}
		return err
	}
	if !env.Success {
		if CloudflareGlobalAPIKeyHint(token, cfErrorCodes(env)...) {
			return errors.New(i18n.T("tier1.cf.global_key"))
		}
		return fmt.Errorf(i18n.T("tier1.cf.rejected"), cfErrorText(env))
	}

	// 还要看令牌状态。
	//
	// success=true 只表示"请求成功了"，而已停用的令牌同样会返回 success。
	// 只检查 success 会让界面显示"凭据有效"，而实际调用全部失败 ——
	// 那是最难排查的一类误报。
	var result struct {
		Status string `json:"status"`
	}
	if env.Result != nil {
		if obj, ok := env.Result.(map[string]any); ok {
			_ = remarshal(obj, &result)
		}
	}
	if result.Status != "" && result.Status != "active" {
		return fmt.Errorf(i18n.T("tier1.cf.token_not_active"), result.Status)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 区域
// ---------------------------------------------------------------------------

// ListZones 实现 dns.ZoneLister。
//
// 分页拉全量而不是只取第一页：用户可能管理着几十个域名，
// 而"列表里少了一半"是一个很难被察觉、又很让人恼火的问题。
func (c *Cloudflare) ListZones(ctx context.Context, cred dns.Credential) ([]dns.Zone, error) {
	cl := c.clientFor(cred, "")

	var out []dns.Zone
	for page := 1; page <= maxPages; page++ {
		params := url.Values{}
		params.Set("page", fmt.Sprint(page))
		params.Set("per_page", fmt.Sprint(zonesPerPage))

		var env cfEnvelope
		err := cl.doJSON(ctx, i18n.T("tier1.op.list_zones"), http.MethodGet,
			cl.URL("/zones?"+params.Encode()), nil, nil, &env)
		if err != nil {
			// 用户未必先点过"校验"再进 DNS 分区。直接撞上
			// "HTTP 400: Invalid request headers" 同样无解，
			// 所以这条路径也要给出同样的指引。
			if hint := cloudflareGlobalKeyError(cred.Field("token"), err); hint != nil {
				return nil, hint
			}
			return nil, err
		}
		if !env.Success {
			return nil, fmt.Errorf(i18n.T("tier1.cf.list_zones_failed"), cfErrorText(env))
		}

		zones, err := decodeZones(env.Result)
		if err != nil {
			return nil, err
		}
		out = append(out, zones...)

		if env.ResultInfo == nil || page >= env.ResultInfo.TotalPages || len(zones) == 0 {
			break
		}
	}
	return out, nil
}

// decodeZones 把通用的 any 结果解码成区域列表。
//
// 响应里的 result 是动态类型，用一次中转解码而不是让整个信封泛型化：
// Go 没有泛型方法，而给每种结果都写一个信封类型只会让代码更长。
func decodeZones(result any) ([]dns.Zone, error) {
	if result == nil {
		return nil, nil
	}
	items, ok := result.([]any)
	if !ok {
		return nil, errors.New(i18n.T("tier1.cf.zones_bad_shape"))
	}

	out := make([]dns.Zone, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		var z cfZone
		if err := remarshal(obj, &z); err != nil {
			continue
		}
		// 只保留活跃区域：pending / moved 状态下的区域改记录不会生效，
		// 列出来只会让用户以为"我改了但没用"。
		if z.Status != "" && z.Status != "active" {
			continue
		}
		out = append(out, dns.Zone{ID: z.ID, Name: z.Name})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 记录
// ---------------------------------------------------------------------------

// ListRecords 实现 dns.RecordLister。
func (c *Cloudflare) ListRecords(ctx context.Context, cred dns.Credential,
	zone dns.Zone, filter dns.RecordFilter) ([]dns.Record, error) {

	cl := c.clientFor(cred, "")

	var out []dns.Record
	for page := 1; page <= maxPages; page++ {
		params := url.Values{}
		params.Set("page", fmt.Sprint(page))
		params.Set("per_page", fmt.Sprint(recordsPerPage))
		if filter.Type != "" {
			params.Set("type", string(filter.Type))
		}
		if filter.Name != "" {
			// Cloudflare 的记录名要求 punycode。
			params.Set("name", filter.Name)
		}

		var env cfEnvelope
		err := cl.doJSON(ctx, i18n.T("tier1.op.list_records"), http.MethodGet,
			cl.URL(fmt.Sprintf("/zones/%s/dns_records?%s", zone.ID, params.Encode())),
			nil, nil, &env)
		if err != nil {
			return nil, err
		}
		if !env.Success {
			return nil, fmt.Errorf(i18n.T("tier1.cf.list_records_failed"), cfErrorText(env))
		}

		records, err := decodeRecords(env.Result)
		if err != nil {
			return nil, err
		}
		out = append(out, records...)

		if env.ResultInfo == nil || page >= env.ResultInfo.TotalPages || len(records) == 0 {
			break
		}
	}
	return out, nil
}

func decodeRecords(result any) ([]dns.Record, error) {
	if result == nil {
		return nil, nil
	}
	items, ok := result.([]any)
	if !ok {
		return nil, errors.New(i18n.T("tier1.cf.records_bad_shape"))
	}

	out := make([]dns.Record, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		var r cfRecord
		if err := remarshal(obj, &r); err != nil {
			continue
		}
		rec := dns.Record{
			ID:      r.ID,
			Name:    r.Name,
			Type:    dns.RecordType(r.Type),
			Content: r.Content,
			// Cloudflare 用 ttl=1 表示 auto；对外统一表达为 0。
			TTL:     normalizeCloudflareTTL(r.TTL),
			Proxied: r.Proxied,
			Comment: r.Comment,
		}
		if r.Priority != nil {
			rec.Priority = *r.Priority
		}
		out = append(out, rec)
	}
	return out, nil
}

// CreateRecord 实现 dns.RecordCreator。
func (c *Cloudflare) CreateRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, rec dns.Record) (dns.Record, error) {

	body, err := cloudflareRecordBody(rec)
	if err != nil {
		return dns.Record{}, err
	}

	cl := c.clientFor(cred, "")
	var env cfEnvelope
	err = cl.doJSON(ctx, i18n.T("tier1.op.create_record"), http.MethodPost,
		cl.URL(fmt.Sprintf("/zones/%s/dns_records", zone.ID)), nil, body, &env)
	if err != nil {
		return dns.Record{}, err
	}
	if !env.Success {
		return dns.Record{}, fmt.Errorf(i18n.T("tier1.cf.create_failed"), cfErrorText(env))
	}
	return decodeOneRecord(env.Result)
}

// UpdateRecord 实现 dns.RecordUpdater。
//
// 用 PUT（整体替换）而不是 PATCH（部分更新）：接口约定调用方提交的是
// **完整记录**，而 PUT 语义与之精确对应。用 PATCH 的话，"清空备注"
// 这类操作需要显式传 null，各家表达不一致，容易产生歧义。
func (c *Cloudflare) UpdateRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, rec dns.Record) (dns.Record, error) {

	if strings.TrimSpace(rec.ID) == "" {
		return dns.Record{}, errors.New(i18n.T("tier1.need_record_id"))
	}
	body, err := cloudflareRecordBody(rec)
	if err != nil {
		return dns.Record{}, err
	}

	cl := c.clientFor(cred, "")
	var env cfEnvelope
	err = cl.doJSON(ctx, i18n.T("tier1.op.update_record"), http.MethodPut,
		cl.URL(fmt.Sprintf("/zones/%s/dns_records/%s", zone.ID, rec.ID)),
		nil, body, &env)
	if err != nil {
		return dns.Record{}, err
	}
	if !env.Success {
		return dns.Record{}, fmt.Errorf(i18n.T("tier1.cf.update_failed"), cfErrorText(env))
	}
	return decodeOneRecord(env.Result)
}

// DeleteRecord 实现 dns.RecordDeleter。
func (c *Cloudflare) DeleteRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, recordID string) error {

	if strings.TrimSpace(recordID) == "" {
		return errors.New(i18n.T("tier1.need_id_delete"))
	}

	cl := c.clientFor(cred, "")
	var env cfEnvelope
	err := cl.doJSON(ctx, i18n.T("tier1.op.delete_record"), http.MethodDelete,
		cl.URL(fmt.Sprintf("/zones/%s/dns_records/%s", zone.ID, recordID)),
		nil, nil, &env)
	if err != nil {
		return err
	}
	if !env.Success {
		return fmt.Errorf(i18n.T("tier1.cf.delete_failed"), cfErrorText(env))
	}
	return nil
}

// ---------------------------------------------------------------------------
// 请求体
// ---------------------------------------------------------------------------

// cfRecordBody 是新增 / 修改记录的请求体。
//
// 用指针表达"只在有意义时才发送"：Proxied 与 Priority 对某些记录类型
// 是非法的，发过去会直接被拒（例如给 TXT 记录发 proxied）。
type cfRecordBody struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Comment  string `json:"comment,omitempty"`
	Proxied  *bool  `json:"proxied,omitempty"`
	Priority *int   `json:"priority,omitempty"`
}

func cloudflareRecordBody(rec dns.Record) (*cfRecordBody, error) {
	if !supportedRecordType(rec.Type) {
		return nil, errors.New(i18n.T("tier1.need_type"))
	}
	if strings.TrimSpace(rec.Name) == "" {
		return nil, errors.New(i18n.T("tier1.need_name"))
	}

	body := &cfRecordBody{
		Type:    string(rec.Type),
		Name:    rec.Name,
		Content: rec.Content,
		TTL:     cloudflareTTL(normalizeTTL(rec.TTL)),
		Comment: rec.Comment,
	}

	// 代理开关只对可代理的记录类型有意义。
	//
	// 对 TXT / MX 这类记录发送 proxied 会被 Cloudflare 直接拒绝，
	// 而那是一个"用户什么都没做错却收到报错"的场景。
	if cloudflareProxiable(rec.Type) {
		proxied := rec.Proxied
		body.Proxied = &proxied
	}

	// 优先级只对 MX / SRV 有意义。
	if rec.Type == dns.TypeMX || rec.Type == dns.TypeSRV {
		priority := rec.Priority
		body.Priority = &priority
	}

	return body, nil
}

// cloudflareProxiable 报告记录类型是否支持 CDN 代理。
func cloudflareProxiable(t dns.RecordType) bool {
	switch t {
	case dns.TypeA, dns.TypeAAAA, dns.TypeCNAME:
		return true
	default:
		return false
	}
}

// cloudflareTTL 把对外的 TTL 表达转成 Cloudflare 的。
//
// 0（"交给服务商默认"）在 Cloudflare 里是 1（auto）。
func cloudflareTTL(ttl int) int {
	if ttl == 0 {
		return 1
	}
	return ttl
}

// normalizeCloudflareTTL 把 Cloudflare 的 TTL 转成对外的表达。
func normalizeCloudflareTTL(ttl int) int {
	if ttl == 1 {
		return 0
	}
	return ttl
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// cfErrorText 汇总信封里的错误信息。
// cloudflareInvalidHeaders 是 Cloudflare 对"Authorization 头不合法"的错误码。
//
// 触发条件是把 Global API Key 当成 Bearer 令牌发出去：Cloudflare 认不出
// 这个形状，于是回 6003 Invalid request headers —— 一个完全指不到重点的
// 错误，用户会以为是自己抄错了 token。
const cloudflareInvalidHeaders = 6003

// globalAPIKeyPattern 匹配 Cloudflare Global API Key 的形状。
//
// Global API Key 是 37 位十六进制；API Token 是 40 位、通常含 - 和 _。
// 长度与字符集都不同，所以形状判断可靠，不会把合法令牌误判。
var globalAPIKeyPattern = regexp.MustCompile(`^[0-9a-fA-F]{37}$`)

// CloudflareGlobalAPIKeyHint 判断这次鉴权失败是不是"填了 Global API Key"。
//
// 单列成一个函数是为了让两条路径（本包的完整实现、旧版 Tier-2 校验器）
// 得到同一份判断 —— 这类"用户最常踩的坑"只该有一处定义。
func CloudflareGlobalAPIKeyHint(token string, codes ...int) bool {
	for _, c := range codes {
		if c == cloudflareInvalidHeaders {
			return true
		}
	}
	return globalAPIKeyPattern.MatchString(strings.TrimSpace(token))
}

// cloudflareRejectedHeaders 判断一个 HTTP 错误响应是不是"Authorization
// 头不合法"—— 把 Global API Key 当 Bearer 令牌发出去就是这种结果。
//
// 除了错误码，还看令牌形状：即使服务商换了错误码，37 位十六进制
// 这个特征本身也足以说明问题，而用户最需要的就是这句话。
func cloudflareRejectedHeaders(token string, apiErr *APIError) bool {
	if apiErr == nil {
		return false
	}
	if apiErr.Code == strconv.Itoa(cloudflareInvalidHeaders) ||
		strings.Contains(apiErr.Message, "Invalid request headers") {
		return true
	}
	return globalAPIKeyPattern.MatchString(strings.TrimSpace(token))
}

// cloudflareGlobalKeyError 在"填了 Global API Key"时返回带指引的错误；
// 其它失败返回 nil，由调用方按原样上报。
func cloudflareGlobalKeyError(token string, err error) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) && cloudflareRejectedHeaders(token, apiErr) {
		return errors.New(i18n.T("tier1.cf.global_key"))
	}
	return nil
}

// cfErrorCodes 取出响应里的错误码，供上面的判定使用。
func cfErrorCodes(env cfEnvelope) []int {
	codes := make([]int, 0, len(env.Errors))
	for _, e := range env.Errors {
		codes = append(codes, e.Code)
	}
	return codes
}

func cfErrorText(env cfEnvelope) string {
	if len(env.Errors) == 0 {
		if len(env.Messages) > 0 {
			return strings.Join(env.Messages, "; ")
		}
		return i18n.T("tier1.no_error_detail")
	}
	parts := make([]string, 0, len(env.Errors))
	for _, e := range env.Errors {
		parts = append(parts, fmt.Sprintf("%d %s", e.Code, e.Message))
	}
	return strings.Join(parts, "; ")
}

func decodeOneRecord(result any) (dns.Record, error) {
	obj, ok := result.(map[string]any)
	if !ok {
		return dns.Record{}, errors.New(i18n.T("tier1.cf.record_bad_shape"))
	}
	var r cfRecord
	if err := remarshal(obj, &r); err != nil {
		return dns.Record{}, fmt.Errorf(i18n.T("tier1.cf.record_parse"), err)
	}
	rec := dns.Record{
		ID: r.ID, Name: r.Name, Type: dns.RecordType(r.Type),
		Content: r.Content, TTL: normalizeCloudflareTTL(r.TTL),
		Proxied: r.Proxied, Comment: r.Comment,
	}
	if r.Priority != nil {
		rec.Priority = *r.Priority
	}
	return rec, nil
}
