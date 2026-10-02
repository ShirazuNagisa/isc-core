package tier1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net/http"
	"net/url"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// godaddyDefaultBase 是 GoDaddy API 的默认基址。
const godaddyDefaultBase = "https://api.godaddy.com"

// godaddyPageSize 是分页拉取时每页的条数。
//
// GoDaddy 的 domains 接口最多接受 limit=1000。这里刻意**不取上限**：
//
//   - GoDaddy 是全行业限流最紧的那一档（多数接口约 60 次/分钟，
//     突发时返回 429 并带上 Retry-After）。一次拉 1000 个域名，
//     响应体可能到几百 KB，却只是为了让界面显示一份列表；
//   - 个人账号实际管理的域名是几十个量级，100 一页顶多两三个往返；
//   - 取小值让"被限流"的概率与"一次请求的代价"都保持在低位 ——
//     限流一旦触发，用户看到的是"所有操作都失败"，代价远大于多一次往返。
const godaddyPageSize = 100

// ---------------------------------------------------------------------------
// 记录 ID 的合成（本文件最关键的一处设计取舍）
// ---------------------------------------------------------------------------
//
// dns.Record 的注释写着"ID 是服务商侧的稳定标识"，但 **GoDaddy 根本不提供
// 记录 ID**：一条记录由 (type, name) 这一对定位，同类型同名可以有多个值
// （最典型的是两条 MX，或两条指向不同地址的 A）。
//
// 这与接口之间是一个根本冲突，无法两全。取舍如下：
//
//  1. 读出记录时**合成**一个 ID：type|apiname|data（各段百分号编码，
//     见 encodeRecordID）。三段都进去是因为只有 (type, name) 不足以在
//     "两条 MX"里区分出用户点的那一条 —— 列表界面必须能定位到具体行。
//  2. 这个 ID **不是稳定的**：用户改了记录值（或服务商侧的值变了），
//     重新读出来就是另一个 ID。也就是说，界面一次操作只能基于它刚读到
//     的那份列表；拿隔夜的 ID 来更新会失败（见 UpdateRecord 的类型/名字
//     校验）。这是"服务商没有 ID"的必然结果，不是可以绕开的 bug。
//  3. 解析不出来时**报错而不是猜**。猜错的后果是改错记录 —— 在一个
//     用户完全无法察觉的地方改坏他的解析，比直接报错危险得多。

// GoDaddy 实现 GoDaddy 的完整记录管理。
//
// 它**不**实现 dns.Verifier：GoDaddy 没有"只校验凭据"的端点，
// 唯一的只读探测是列域名，而凭据不对与"账号下没有域名"在 GoDaddy 的
// 响应里并不总能区分 —— 一个会误报的"测试连接"比没有更糟。
type GoDaddy struct {
	dynamicDelegate
	meta    dns.Meta
	baseURL string
}

// 编译期断言：接口实现必须完整。
var (
	_ dns.Provider       = (*GoDaddy)(nil)
	_ dns.DynamicUpdater = (*GoDaddy)(nil)
	_ dns.ZoneLister     = (*GoDaddy)(nil)
	_ dns.RecordLister   = (*GoDaddy)(nil)
	_ dns.RecordCreator  = (*GoDaddy)(nil)
	_ dns.RecordUpdater  = (*GoDaddy)(nil)
	_ dns.RecordDeleter  = (*GoDaddy)(nil)
)

// NewGoDaddy 构造 GoDaddy 实现。
//
// baseURL 为空时使用官方地址；测试传入本地假服务器地址。
// 与 Cloudflare 保持同样的形态：地址是参数而不是包级变量，
// 测试因此可以并行，也不会互相污染。
func NewGoDaddy(baseURL string) *GoDaddy {
	if baseURL == "" {
		baseURL = godaddyDefaultBase
	}
	return &GoDaddy{
		meta: dns.Meta{
			Name:        "godaddy",
			DisplayName: "GoDaddy",
			Tier:        1,
		},
		baseURL: baseURL,
	}
}

// Meta 实现 dns.Provider。
func (g *GoDaddy) Meta() dns.Meta { return g.meta }

// clientFor 为一次调用构造带鉴权的客户端。
//
// # 这里有一个必须说清楚的凭据泄漏面
//
// GoDaddy 的鉴权是**静态明文头**：`Authorization: sso-key <api_key>:<api_secret>`
// 直接把密钥放进请求头（见 internal/ddnsgo/provider_godaddy.go 的同一写法）。
// 它没有签名、没有时效、没有脱敏空间 —— 头部被记下来就等于密钥被记下来。
// 因此本文件的任何错误信息与日志都**绝不**包含该头的值，
// 只报 HTTP 状态码与 GoDaddy 的响应体。
func (g *GoDaddy) clientFor(cred dns.Credential) *client {
	cl := newClient(g.baseURL, "")
	cl.headers["Authorization"] = "sso-key " + cred.Field("api_key") + ":" + cred.Field("api_secret")
	return cl
}

// ---------------------------------------------------------------------------
// 请求 / 响应结构
// ---------------------------------------------------------------------------

// godaddyRecord 是 GoDaddy 的记录表示。
//
// 注意字段名是 data 而不是 Cloudflare 那套 content；priority / port /
// weight / protocol / service 是 SRV 等类型用的。这里只表达 ISC 接口里
// 有对应位置的那几个字段，其余字段**原样不发送** —— 见 godaddyRecordBody。
type godaddyRecord struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Data string `json:"data"`
	TTL  int    `json:"ttl,omitempty"`
	// Priority 用指针：GoDaddy 对非 MX / SRV 记录会把 priority 回成 0，
	// 用零值无法区分"优先级是 0"与"这条记录没有优先级这回事"，
	// 而回读时把 MX 的优先级误判成 0 会让用户看到一条错误的记录。
	// 用指针表达"响应里到底有没有这个字段"，歧义就消失了。
	Priority *int `json:"priority,omitempty"`
}

// godaddyRecords 是 GoDaddy 的批量请求体。
//
// 这是 GoDaddy 记录接口最容易踩的一处：**请求体永远是一个数组**，
// 哪怕只动一条记录。发单个对象会被直接拒绝（400）。
type godaddyRecords []godaddyRecord

// godaddyDomain 是域名列表里的一项。
//
// 注意 GoDaddy 的"区域"就是**域名本身**：没有独立的 zone 概念、
// 没有 zone id、也没有 zone 列表与解析记录列表之分。
// 映射到 ISC 的 dns.Zone 时，ID 与 Name 都填域名。
//
// 只声明 domain 一个字段：其余字段（domainId / status / expires …）
// 在 ISC 的 Zone 里没有对应位置，读进来只会变成一个无人使用的字段，
// 而无人使用的字段迟早会被误当成"已支持的能力"。
type godaddyDomain struct {
	Domain string `json:"domain"`
}

// godaddyDomainPage 是域名列表的一页。
//
// GoDaddy 这个接口**不返回总数**：唯一的分页信号是 next ——
// 一个带 marker 的**完整 URL**（不是裸游标，见 godaddyNextMarker）。
// next 为空即到底。
type godaddyDomainPage struct {
	Domains []godaddyDomain `json:"domains"`
	Next    string          `json:"next"`
}

// godaddyErrorBody 是 GoDaddy 的错误体。
//
// 形如 {"code":"NOT_FOUND","message":"...","fields":[...]}。
// **code 是字符串不是数字** —— 按数字解析会得到空串，
// 而空的 code 会让错误信息退化成一个没有线索的"鉴权失败"，
// 用户就无从判断该改密钥还是该改权限。
type godaddyErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fields  []struct {
		Code        string `json:"code"`
		Message     string `json:"message"`
		Path        string `json:"path"`
		PathRelated string `json:"pathRelated"`
	} `json:"fields"`
}

// ---------------------------------------------------------------------------
// 区域（域名）
// ---------------------------------------------------------------------------

// ListZones 实现 dns.ZoneLister。
//
// 分页拉全量：用户可能管理着几十个域名，而"列表里少了一半"是一个
// 很难被察觉、又很让人恼火的问题（与 Cloudflare 实现保持一致的行为）。
func (g *GoDaddy) ListZones(ctx context.Context, cred dns.Credential) ([]dns.Zone, error) {
	if err := godaddyCheckCredential(cred); err != nil {
		return nil, err
	}

	cl := g.clientFor(cred)

	var out []dns.Zone
	marker := ""
	for page := 0; page < maxPages; page++ {
		params := url.Values{}
		params.Set("limit", fmt.Sprint(godaddyPageSize))
		if marker != "" {
			// marker 是服务商给的不透明游标，必须原样回传（URL 编码由
			// url.Values 负责）。自己拼或者截断它都会让分页结果错乱。
			params.Set("marker", marker)
		}

		var resp godaddyDomainPage
		err := cl.doJSON(ctx, i18n.T("tier1.op.list_domains"), http.MethodGet,
			cl.URL("/v1/domains?"+params.Encode()), nil, nil, &resp)
		if err != nil {
			return nil, err
		}

		for _, d := range resp.Domains {
			name := strings.TrimSpace(d.Domain)
			if name == "" {
				continue
			}
			out = append(out, dns.Zone{ID: name, Name: name})
		}

		marker = godaddyNextMarker(resp.Next)
		if marker == "" {
			break
		}
	}
	return out, nil
}

// godaddyNextMarker 从 GoDaddy 的 next 链接里取出 marker。
//
// 上游返回的 next 是一个**完整 URL**（形如
// https://api.godaddy.com/v1/domains?limit=100&marker=abc），
// 不是裸游标。直接把它当 marker 发回去只会得到 400。
// 解析失败时返回空串 —— 即"没有下一页"，宁可少拉一页也不要用一个
// 坏游标去撞 GoDaddy 的限流。
func godaddyNextMarker(next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return ""
	}
	u, err := url.Parse(next)
	if err != nil {
		return ""
	}
	return u.Query().Get("marker")
}

// ---------------------------------------------------------------------------
// 记录
// ---------------------------------------------------------------------------

// ListRecords 实现 dns.RecordLister。
//
// # 为什么拉全量再在本地过滤
//
// GoDaddy 唯独不支持按类型过滤（没有 type 查询参数），要按类型过滤只能走
// /records/{type}/{name} 那个路径 —— 而它要求 type 与 name **同时**给出，
// 且 name 必须是相对名。于是"只看 AAAA 记录"要么拿不到，要么得把域名下
// 每个名字各请求一次 —— 在一个 60 次/分钟的限流下，后者是灾难。
//
// 因此这里一次拉全量（带 limit 分页），过滤在本地做。代价是多取了一些
// 数据，换来的是**请求次数与过滤条件的组合数无关**。
func (g *GoDaddy) ListRecords(ctx context.Context, cred dns.Credential,
	zone dns.Zone, filter dns.RecordFilter) ([]dns.Record, error) {

	if err := godaddyCheckCredential(cred); err != nil {
		return nil, err
	}
	domain, err := godaddyZoneName(zone)
	if err != nil {
		return nil, err
	}
	// 名字过滤先在这一步校验并翻译。
	//
	// 顺序很重要：一个不属于该域名的记录名是**用户的输入错误**，
	// 必须在发请求之前就报出来 —— 否则我们不但白耗一次 GoDaddy
	// 那本就很紧的限流配额，还会把一个其实没发生的服务商错误
	// （"没找到"）报给用户，把他引向完全错误的方向。
	filter, err = godaddyNormalizeFilter(domain, filter)
	if err != nil {
		return nil, err
	}

	cl := g.clientFor(cred)

	var out []dns.Record
	marker := ""
	for page := 0; page < maxPages; page++ {
		params := url.Values{}
		params.Set("limit", fmt.Sprint(godaddyPageSize))
		if marker != "" {
			params.Set("marker", marker)
		}

		var resp struct {
			Records godaddyRecords `json:"records"`
			Next    string         `json:"next"`
		}
		err := cl.doJSON(ctx, i18n.T("tier1.op.list_records"), http.MethodGet,
			cl.URL(fmt.Sprintf("/v1/domains/%s/records?%s",
				url.PathEscape(domain), params.Encode())),
			nil, nil, &resp)
		if err != nil {
			return nil, err
		}

		for _, r := range resp.Records {
			rec, err := godaddyToRecord(domain, r)
			if err != nil {
				// 单条无法翻译不该让整次列出失败：用户可以照常看到
				// 其余的记录，而不是面对一个空列表和一句报错。
				continue
			}
			if !godaddyMatchFilter(rec, domain, filter) {
				continue
			}
			out = append(out, rec)
		}

		marker = godaddyNextMarker(resp.Next)
		if marker == "" {
			break
		}
	}
	return out, nil
}

// godaddyNormalizeFilter 校验并翻译过滤条件。
//
// 名字在接口层是完整记录名（www.example.com），而 GoDaddy 只认相对名（www）。
// 这里提前翻译好，好处有两个：非法输入在发请求之前就被拒；
// 后面的逐条比较不必每次都做一次字符串切分。
func godaddyNormalizeFilter(domain string, filter dns.RecordFilter) (dns.RecordFilter, error) {
	if strings.TrimSpace(filter.Name) == "" {
		filter.Name = ""
		return filter, nil
	}
	rel, err := godaddyRelativeName(domain, filter.Name)
	if err != nil {
		return filter, err
	}
	filter.Name = rel
	return filter, nil
}

// godaddyMatchFilter 在本地套用过滤条件。
//
// filter.Name 此时已经是**相对名**（见 godaddyNormalizeFilter），
// 类型比较忽略大小写：DNS 类型本来就是大小写不敏感的，
// 而用户从别处复制来的写法不一定和记录一致。
func godaddyMatchFilter(rec dns.Record, domain string, filter dns.RecordFilter) bool {
	if filter.Type != "" && !strings.EqualFold(string(rec.Type), string(filter.Type)) {
		return false
	}
	if filter.Name != "" &&
		!strings.EqualFold(strings.TrimSuffix(rec.Name, "."+domain), filter.Name) {
		return false
	}
	return true
}

// CreateRecord 实现 dns.RecordCreator。
//
// 用 PATCH 而不是 PUT：PATCH 是"新增或更新这一条"，PUT 是"用我给你的
// 这个数组**替换掉**该 (type, name) 下的全部记录"。对一个叫 Create 的
// 操作来说，PUT 会静默删掉同名同类型的其它值 —— 用户想加第二条 MX，
// 结果第一条没了。
//
// ddns-go 的移植实现（internal/ddnsgo/provider_godaddy.go）用的是 PUT，
// 那不是笔误：动态解析要的正是"这个名字下只保留我这一条"。
// 两者语义不同，因此不能照搬。
func (g *GoDaddy) CreateRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, rec dns.Record) (dns.Record, error) {

	if err := godaddyCheckCredential(cred); err != nil {
		return dns.Record{}, err
	}
	domain, err := godaddyZoneName(zone)
	if err != nil {
		return dns.Record{}, err
	}

	body, err := godaddyRecordBody(domain, rec)
	if err != nil {
		return dns.Record{}, err
	}

	cl := g.clientFor(cred)
	err = cl.doJSON(ctx, i18n.T("tier1.op.create_record"), http.MethodPatch,
		cl.URL(fmt.Sprintf("/v1/domains/%s/records/%s/%s",
			url.PathEscape(domain),
			url.PathEscape(string(rec.Type)), url.PathEscape(body.Name))),
		nil, godaddyRecords{*body}, nil)
	if err != nil {
		return dns.Record{}, err
	}

	// GoDaddy 对成功的写入只回 200 与一个空体，没有"服务端返回的那条记录"
	// 可读。回显请求内容并补上合成 ID，是这里唯一能做到的事 ——
	// 因此返回值里的 TTL 是**我们请求写入的值**（已归一到 GoDaddy 允许的
	// 上下限），而不是服务商侧最终的值。
	return dns.Record{
		ID:       encodeRecordID(string(rec.Type), body.Name, body.Data),
		Name:     rec.Name,
		Type:     rec.Type,
		Content:  body.Data,
		TTL:      body.TTL,
		Priority: godaddyPriorityValue(body.Priority),
	}, nil
}

// UpdateRecord 实现 dns.RecordUpdater。
//
// 用 PUT 而不是 PATCH：接口约定调用方提交的是**完整记录**，PUT 的
// "整体替换"语义与之对应，也与 Cloudflare 实现的取舍一致。
//
// # 真实语义（必须如实说明）
//
// PUT /records/{type}/{name} 替换的是该 (type, name) 下的**全部**记录。
// 因此当同名同类型有多条值（两条 MX）时，更新其中一条会把其余几条
// **删掉**，只留下提交的这条。这是 GoDaddy 的接口形态决定的，
// 不是本实现能规避的；调用方在存在多值记录时应当明确告知用户这一后果。
func (g *GoDaddy) UpdateRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, rec dns.Record) (dns.Record, error) {

	if err := godaddyCheckCredential(cred); err != nil {
		return dns.Record{}, err
	}
	domain, err := godaddyZoneName(zone)
	if err != nil {
		return dns.Record{}, err
	}
	if strings.TrimSpace(rec.ID) == "" {
		return dns.Record{}, errors.New(i18n.T("tier1.gd.need_id_update"))
	}

	idType, idName, _, err := decodeRecordID(rec.ID)
	if err != nil {
		return dns.Record{}, err
	}

	body, err := godaddyRecordBody(domain, rec)
	if err != nil {
		return dns.Record{}, err
	}

	// ID 与提交的记录必须指向同一条 (type, name)。
	//
	// 这是"记录没有稳定 ID"这个冲突的直接后果：ID 里含 data，用户改了值
	// 之后 ID 就与记录对不上了。不校验的话，我们会拿着一个旧 ID 去改一条
	// 它并不指向的记录 —— 那是最糟的一类错误：用户以为改的是 A，实际被改的
	// 是 B，而且两边都不报错。
	if !strings.EqualFold(idType, string(rec.Type)) || !strings.EqualFold(idName, body.Name) {
		return dns.Record{}, fmt.Errorf(
			i18n.T("tier1.gd.id_mismatch"),
			idType, idName, rec.Type, body.Name)
	}

	cl := g.clientFor(cred)
	err = cl.doJSON(ctx, i18n.T("tier1.op.update_record"), http.MethodPut,
		cl.URL(fmt.Sprintf("/v1/domains/%s/records/%s/%s",
			url.PathEscape(domain),
			url.PathEscape(idType), url.PathEscape(idName))),
		nil, godaddyRecords{*body}, nil)
	if err != nil {
		return dns.Record{}, err
	}

	// 与 CreateRecord 同理：没有响应体可读，回显请求内容 + 合成的新 ID。
	// 新 ID 用新的 data —— 旧的已经不对应任何记录了。
	return dns.Record{
		ID:       encodeRecordID(idType, idName, body.Data),
		Name:     rec.Name,
		Type:     dns.RecordType(idType),
		Content:  body.Data,
		TTL:      body.TTL,
		Priority: godaddyPriorityValue(body.Priority),
	}, nil
}

// DeleteRecord 实现 dns.RecordDeleter。
//
// # 真实语义：删掉整个 (type, name) 组合，而不是"一条记录"
//
// 这与接口注释（"删除一条记录"）**不一致**，而且是 GoDaddy 的接口形态
// 决定的：DELETE /records/{type}/{name} 没有"只删某一条值"的表达方式，
// 该路径下没有记录 ID 可指。所以：
//
//   - 域名下若有两 A 记录，删除其中一条会把**两条都删掉**；
//   - 这个行为无法通过"先读后写"绕开（PUT 只能替换成另一组记录，
//     表达不了"少一条"）。
//
// 不做任何假装：不返回"已删除 1 条"这类会误导调用方的结果，
// 也在下面的错误信息与该方法的文档里把后果讲明白。
// 调用方**必须**在删除前把这一后果告知用户（界面上的确认文案）。
func (g *GoDaddy) DeleteRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, recordID string) error {

	if err := godaddyCheckCredential(cred); err != nil {
		return err
	}
	domain, err := godaddyZoneName(zone)
	if err != nil {
		return err
	}
	if strings.TrimSpace(recordID) == "" {
		return errors.New(i18n.T("tier1.gd.need_id_delete"))
	}

	idType, idName, _, err := decodeRecordID(recordID)
	if err != nil {
		return err
	}

	cl := g.clientFor(cred)
	err = cl.doJSON(ctx, i18n.T("tier1.op.delete_record"), http.MethodDelete,
		cl.URL(fmt.Sprintf("/v1/domains/%s/records/%s/%s",
			url.PathEscape(domain),
			url.PathEscape(idType), url.PathEscape(idName))),
		nil, nil, nil)
	if err == nil {
		return nil
	}

	// 404 在 GoDaddy 这里有两种截然不同的成因，而用户能采取的行动也不同，
	// 所以补一句本地化的说明并保留原始错误（%w）。
	// 其余错误（429 限流、401 鉴权）原样上抛 —— 那些不需要额外解释。
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.IsNotFound() {
		return fmt.Errorf(i18n.T("tier1.gd.not_found"), err)
	}
	return err
}

// ---------------------------------------------------------------------------
// 请求体
// ---------------------------------------------------------------------------

// godaddyRecordBody 把 ISC 的记录翻译成 GoDaddy 的请求体。
//
// 只发送接口能表达的那几个字段。port / weight / protocol / service
// (SRV) 在 dns.Record 里没有对应位置，因此不发送 —— 发一个零值过去
// 等于把用户已有的 SRV 参数清掉。
func godaddyRecordBody(domain string, rec dns.Record) (*godaddyRecord, error) {
	if !supportedRecordType(rec.Type) {
		return nil, errors.New(i18n.T("tier1.need_type"))
	}
	if strings.TrimSpace(rec.Name) == "" {
		return nil, errors.New(i18n.T("tier1.need_name"))
	}
	name, err := godaddyRelativeName(domain, rec.Name)
	if err != nil {
		return nil, err
	}

	body := &godaddyRecord{
		Type: string(rec.Type),
		Name: name,
		Data: rec.Content,
		TTL:  godaddyTTL(rec.TTL),
	}

	// 优先级只在有意义时发送。
	//
	// GoDaddy 与 Cloudflare 在这点上的规则一致：给 A / TXT 这类记录发
	// priority 会被直接拒绝，而那是"用户什么都没做错却收到报错"的场景。
	// MX / SRV 则相反 —— 优先级是它们的一部分，缺了会被拒。
	if usesPriority(rec.Type) {
		p := rec.Priority
		body.Priority = &p
	}
	return body, nil
}

// usesPriority 报告记录类型是否使用优先级字段。
func usesPriority(t dns.RecordType) bool {
	switch t {
	case dns.TypeMX, dns.TypeSRV:
		return true
	default:
		return false
	}
}

// godaddyPriorityValue 把可能为空的优先级还原成零值。
func godaddyPriorityValue(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// godaddyTTL 把对外的 TTL 表达转成 GoDaddy 能接受的值。
//
// GoDaddy 的 TTL 下限是 600 秒、上限 86400 秒，且**不接受 0**。
// 对外的 0（"交给服务商默认"）因此映射到 3600 —— GoDaddy 控制台上
// 新记录的默认值。选 3600 而不是下限 600：600 秒的 TTL 会显著抬高
// 权威服务器的查询量，把它当作"用户没意见"的默认值是不合适的。
//
// 低于下限或高于上限的值一律夹到边界：直接发过去只会换来一个
// "TTL 不合法"的报错，而用户真正想表达的（"给个合理的值"）是明确的。
func godaddyTTL(ttl int) int {
	ttl = normalizeTTL(ttl)
	switch {
	case ttl == 0:
		return 3600
	case ttl < 600:
		return 600
	case ttl > 86400:
		return 86400
	default:
		return ttl
	}
}

// ---------------------------------------------------------------------------
// 记录 ID 的编解码
// ---------------------------------------------------------------------------

// encodeRecordID 合成一个记录 ID。
//
// 格式：type|apiname|data，三段各自做百分号编码。
//
// 为什么用百分号编码而不是直接拼：data 里出现分隔符是完全可能的
// （TXT 记录里放 '|'、放引号、放换行都是合法的），一旦分隔符出现在
// 值里，解析就会得到错误的段数或错误的边界。编码后分隔符不可能
// 出现在段内，格式因此是可逆的。
//
// 注意这个 ID 会被放进 URL 路径（DELETE / PUT），所以它必须只含
// 路径安全的字符 —— 百分号编码顺带保证了这一点。
func encodeRecordID(typ, name, data string) string {
	return url.PathEscape(typ) + "|" + url.PathEscape(name) + "|" + url.PathEscape(data)
}

// decodeRecordID 解析合成的记录 ID。
//
// 段数不对时**报错而不是猜**：一个解析错的 ID 会导致改错或删错记录，
// 而用户完全无法察觉。返回的 data 字段当前没有调用方使用（更新时以
// 请求体里的值为准），保留它是为了让 ID 的格式在这一个地方被完整表达。
func decodeRecordID(id string) (typ, name, data string, err error) {
	parts := strings.Split(id, "|")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf(
			i18n.T("tier1.gd.bad_id"), id)
	}
	decoded := make([]string, 3)
	for i, p := range parts {
		v, err := url.PathUnescape(p)
		if err != nil {
			return "", "", "", fmt.Errorf(i18n.T("tier1.gd.id_parse"), id, err)
		}
		decoded[i] = v
	}
	if strings.TrimSpace(decoded[0]) == "" || strings.TrimSpace(decoded[1]) == "" {
		return "", "", "", fmt.Errorf(i18n.T("tier1.gd.id_missing"), id)
	}
	return decoded[0], decoded[1], decoded[2], nil
}

// ---------------------------------------------------------------------------
// 名字翻译
// ---------------------------------------------------------------------------

// godaddyZoneName 取出可用的域名。
//
// 优先用 Name 而不是 ID：GoDaddy 没有 zone id，两边通常都是域名，
// 但用户或导入流程完全可能只填了 Name 而 ID 留空。
func godaddyZoneName(zone dns.Zone) (string, error) {
	name := strings.TrimSpace(zone.Name)
	if name == "" {
		name = strings.TrimSpace(zone.ID)
	}
	// 容忍尾部点（FQDN 写法）：GoDaddy 不接受 example.com. 这种形式。
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return "", errors.New(i18n.T("tier1.gd.need_domain"))
	}
	return name, nil
}

// godaddyRelativeName 把完整记录名翻译成 GoDaddy 的相对名。
//
// GoDaddy 的记录名是**相对域名**的：www.example.com → www，
// 根记录 → "@"。把完整名直接发过去会被拒绝，或者更糟 ——
// 在域名下创建一条名字真的叫 www.example.com 的记录。
func godaddyRelativeName(domain, fullName string) (string, error) {
	name := strings.TrimSpace(fullName)
	// 容忍尾部点，否则 "www.example.com." 会匹配不上而落到 default 分支，
	// 最终以 "@" 的形式写到根记录上 —— 一条静默写错位置的危险路径。
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return "", errors.New(i18n.T("tier1.need_name"))
	}
	if name == "@" {
		return "@", nil
	}

	lowerName := strings.ToLower(name)
	lowerDomain := strings.ToLower(domain)
	switch {
	case lowerName == lowerDomain:
		return "@", nil
	case strings.HasSuffix(lowerName, "."+lowerDomain):
		rel := name[:len(name)-len(domain)-1]
		if rel == "" {
			return "@", nil
		}
		return rel, nil
	default:
		return "", fmt.Errorf(
			i18n.T("tier1.gd.name_outside_domain"), fullName, domain)
	}
}

// godaddyFullName 把 GoDaddy 的相对名还原成完整记录名。
//
// 接口约定 Record.Name 是完整记录名（www.example.com），而 GoDaddy 说 "@"。
// 不翻译的话，界面会显示一条名字叫 "@" 的记录，用户无法判断它属于哪个域名。
func godaddyFullName(domain, rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "@" {
		return domain
	}
	return rel + "." + domain
}

// ---------------------------------------------------------------------------
// 翻译与辅助
// ---------------------------------------------------------------------------

// godaddyToRecord 把一条 GoDaddy 记录翻译成 ISC 的记录。
func godaddyToRecord(domain string, r godaddyRecord) (dns.Record, error) {
	typ := strings.TrimSpace(r.Type)
	if typ == "" {
		return dns.Record{}, errors.New(i18n.T("tier1.gd.record_no_type"))
	}
	rel := strings.TrimSpace(r.Name)
	if rel == "" {
		// GoDaddy 理论上总有 name，但一条没有 name 的记录如果被当成根记录
		// 展示出来，用户会看到一个不存在的记录 —— 宁可丢掉它。
		return dns.Record{}, errors.New(i18n.T("tier1.gd.record_no_name"))
	}

	rec := dns.Record{
		ID:      encodeRecordID(typ, rel, r.Data),
		Name:    godaddyFullName(domain, rel),
		Type:    dns.RecordType(typ),
		Content: r.Data,
		TTL:     r.TTL,
	}
	if r.Priority != nil && usesPriority(dns.RecordType(typ)) {
		rec.Priority = *r.Priority
	}
	return rec, nil
}

// godaddyCheckCredential 在发请求前检查凭据齐不齐。
//
// 早失败的价值在这里格外大：GoDaddy 的鉴权头是明文的 key:secret，
// 少了一半根本没救；而且 GoDaddy 的限流很紧，一次注定失败的请求
// 也在消耗配额。
func godaddyCheckCredential(cred dns.Credential) error {
	if cred.Field("api_key") == "" || cred.Field("api_secret") == "" {
		return errors.New(i18n.T("tier1.gd.need_credentials"))
	}
	return nil
}

// godaddyErrorText 从 GoDaddy 的错误体里取出一句能展示给用户的话。
//
// 存在的必要：公共的 extractErrorMessage（tier1.go）做的是**广度优先**的
// 字段名搜索，它会先撞上 fields[0].message —— 那是"某个字段为什么不合格"，
// 而不是"这次请求为什么失败"。对用户来说正确的答案是顶层 message，
// 这里按 GoDaddy 自己的结构精确取值，并把 code 一起带上。
//
// 它解析的正是 godaddyErrorBody 的形状 —— code 是字符串、说明在 fields
// 里。注意当前没有生产调用方：所有网络调用都经 doJSON，错误一律是
// *APIError，好处是各家在那里被一视同仁地分类（IsNotFound /
// IsUnauthorized），那比一句更好看的文本更重要。保留并测试这个函数，
// 是为了让"GoDaddy 的错误体长什么样"成为一条被验证过的事实 ——
// 下一次有人要按 code 分支处理错误时，不必再从零试。
func godaddyErrorText(body []byte) string {
	var parsed godaddyErrorBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return truncateForMessage(string(body))
	}
	if msg := strings.TrimSpace(parsed.Message); msg != "" {
		if code := strings.TrimSpace(parsed.Code); code != "" {
			return fmt.Sprintf("[%s] %s", code, msg)
		}
		return msg
	}
	return truncateForMessage(string(body))
}
