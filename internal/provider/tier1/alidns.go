package tier1

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/ddnsgo"
	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// alidnsDefaultBase 是阿里云云解析（AliDNS）API 的默认基址。
//
// 必须与 internal/ddnsgo 里移植代码用的端点一致（那边是带尾斜杠的
// "https://alidns.aliyuncs.com/"）：两处指向不同端点会变成"动态解析能用、
// 记录管理不能用"这种极难解释的现象。
const alidnsDefaultBase = "https://alidns.aliyuncs.com"

// alidnsAPIVersion 是云解析 OpenAPI 的版本号。
//
// 它作为公共参数参与签名。版本号写错的后果不是一句明确的报错，而是服务端
// 换一套参数模型解析请求 —— 表现为"参数莫名其妙不生效"。
const alidnsAPIVersion = "2015-01-09"

// alidnsDefaultTTL 是 TTL 缺省时使用的值（秒）。
//
// 600 是阿里云文档写明的默认值，也是移植过来的动态解析实现里的默认值。
// 两侧取同一个值，用户不会遇到"动态解析写 600、界面改一下变成别的"。
const alidnsDefaultTTL = 600

// 分页每页条数。两个上限来自服务端文档：DescribeDomains 最大 100、
// DescribeDomainRecords 最大 500。取上限是为了把往返次数压到最少 ——
// "账号下有几千条记录"是完全可能的。
const (
	alidnsDomainsPerPage = 100
	alidnsRecordsPerPage = 500
)

// Alidns 实现阿里云云解析的完整记录管理。
//
// 它不实现 dns.Verifier：阿里云没有专门的"校验这段凭据"只读端点，用
// DescribeDomains 顶上则需要额外的 RAM 权限，会让"只有解析记录编辑权限"
// 的最小权限账号被误判成凭据无效 —— 那正是 Cloudflare 那边特意避开的坑。
// 接口层真的需要"测试连接"时再补，而不是现在塞一个会误报的实现。
type Alidns struct {
	dynamicDelegate
	meta    dns.Meta
	baseURL string
}

// 编译期断言：接口实现必须完整。
var (
	_ dns.Provider       = (*Alidns)(nil)
	_ dns.DynamicUpdater = (*Alidns)(nil)
	_ dns.ZoneLister     = (*Alidns)(nil)
	_ dns.RecordLister   = (*Alidns)(nil)
	_ dns.RecordCreator  = (*Alidns)(nil)
	_ dns.RecordUpdater  = (*Alidns)(nil)
	_ dns.RecordDeleter  = (*Alidns)(nil)
)

// NewAlidns 构造阿里云云解析实现。
//
// baseURL 为空时使用官方地址；测试传入本地假服务器地址。
// 把地址做成参数而不是包级变量：测试因此可以并行，也不会互相污染。
func NewAlidns(baseURL string) *Alidns {
	if baseURL == "" {
		baseURL = alidnsDefaultBase
	}
	return &Alidns{
		meta: dns.Meta{
			Name:        "alidns",
			DisplayName: i18n.T("tier1.ali.display_name"),
			Tier:        1,
		},
		baseURL: baseURL,
	}
}

// Meta 实现 dns.Provider。
func (a *Alidns) Meta() dns.Meta { return a.meta }

// clientFor 为一次调用构造客户端。
//
// 与 Cloudflare 不同，这里**不需要凭据参数**：阿里云的鉴权信息不在 HTTP
// 头上，而是作为公共参数（AccessKeyId + Signature）拼进查询串，见 call。
// 少一个参数就少一处"以为它在设置鉴权头"的误读。
func (a *Alidns) clientFor(httpInterface string) *client {
	return newClient(a.baseURL, httpInterface)
}

// call 发起一次云解析 API 调用。
//
// 全部操作都收敛到这里，原因是签名：阿里云的 Signature 是对**整组参数**算出来的，
// 少签一个参数、或者在签名之后又改动了一个参数，服务端只会回一句
// SignatureDoesNotMatch。让每个调用点自己拼参数，迟早会漏。
func (a *Alidns) call(ctx context.Context, cred dns.Credential,
	op string, params url.Values, out any) error {

	// 取凭据即用即弃：它们只在这一层参与签名，不进日志、不进错误信息
	// （错误里说"缺哪个字段"就够了，说"值是什么"等于把密钥写进日志）。
	accessKeyID := cred.Field("access_key_id")
	accessKeySecret := cred.Field("access_key_secret")
	if accessKeyID == "" || accessKeySecret == "" {
		return errors.New(i18n.T("tier1.ali.need_credentials"))
	}

	// 复用移植代码里那份经过海量用户验证的签名实现，不重写第二遍。
	// 它会往 params 里补公共参数（Format / Version / Timestamp /
	// SignatureNonce / AccessKeyId …）并算好 Signature。
	ddnsgo.AliyunSigner(accessKeyID, accessKeySecret, &params, http.MethodGet, alidnsAPIVersion)

	cl := a.clientFor("")
	// 签名算完之后 params 一个字都不能再改：签名覆盖的就是下面这串查询参数。
	// 阿里云是 RPC 风格 —— 所有操作都打在根路径上，靠 Action 参数区分。
	return cl.doJSON(ctx, op, http.MethodGet, cl.URL("/?"+params.Encode()), nil, nil, out)
}

// ---------------------------------------------------------------------------
// 响应结构
// ---------------------------------------------------------------------------

// alidnsDomainsResp 是 DescribeDomains 的响应。
type alidnsDomainsResp struct {
	TotalCount int `json:"TotalCount"`
	Domains    struct {
		Domain []alidnsDomain `json:"Domain"`
	} `json:"Domains"`
}

// alidnsDomain 是域名列表里的一项。
type alidnsDomain struct {
	DomainID string `json:"DomainId"`
	// DomainName 可能是中文域名的原文，PunyCode 只在中文域名时非空。
	DomainName string `json:"DomainName"`
	PunyCode   string `json:"PunyCode"`
}

// alidnsRecordsResp 是 DescribeDomainRecords 的响应。
type alidnsRecordsResp struct {
	TotalCount    int `json:"TotalCount"`
	DomainRecords struct {
		Record []alidnsRecord `json:"Record"`
	} `json:"DomainRecords"`
}

// alidnsRecord 是一条解析记录。
//
// 字段名沿用服务端的驼峰写法：阿里云的 RPC 接口返回的就是这些名字，
// 再映射一层只会多一处会写错的地方。
type alidnsRecord struct {
	RecordID   string `json:"RecordId"`
	DomainName string `json:"DomainName"`
	// RR 是主机记录，根记录是 "@"（不是空串，也不是域名本身）。
	RR       string `json:"RR"`
	Type     string `json:"Type"`
	Value    string `json:"Value"`
	TTL      int    `json:"TTL"`
	Priority int    `json:"Priority"`
	// Line 是解析线路，默认线路是 "default"。
	Line string `json:"Line"`
	// Status 是 Enable / Disable（暂停解析）。它**不**用于过滤：
	// 暂停的记录依然在账号里，把它藏起来只会让用户以为记录丢了。
	// （Cloudflare 那边按区域状态过滤是另一回事 —— 那是不生效的区域。）
	Status string `json:"Status"`
	Remark string `json:"Remark"`
}

// alidnsRecordIDResp 是新增 / 修改 / 删除的响应。
//
// 三个写接口都只回一个 RecordId 与 RequestId，**不回记录的完整内容** ——
// 所以写成功之后我们只能把调用方给的记录原样回显（见 CreateRecord）。
type alidnsRecordIDResp struct {
	RecordID  string `json:"RecordId"`
	RequestID string `json:"RequestId"`
}

// ---------------------------------------------------------------------------
// 区域
// ---------------------------------------------------------------------------

// ListZones 实现 dns.ZoneLister。
//
// 分页拉全量而不是只取第一页：用户可能管理着几十个域名，而"列表里少了一半"
// 是一个很难被察觉、又很让人恼火的问题。
func (a *Alidns) ListZones(ctx context.Context, cred dns.Credential) ([]dns.Zone, error) {
	var out []dns.Zone
	seen := 0

	for page := 1; page <= maxPages; page++ {
		params := url.Values{}
		params.Set("Action", "DescribeDomains")
		params.Set("PageNumber", strconv.Itoa(page))
		params.Set("PageSize", strconv.Itoa(alidnsDomainsPerPage))

		var resp alidnsDomainsResp
		if err := a.call(ctx, cred, i18n.T("tier1.op.list_zones"), params, &resp); err != nil {
			return nil, err
		}

		items := resp.Domains.Domain
		seen += len(items)

		for _, d := range items {
			// 中文域名时 DomainName 是原文、PunyCode 是 punycode 形式，
			// 优先用原文：它才是用户在控制台里认得出的那个名字。
			// PunyCode 只在原文缺失时兜底。
			name := alidnsTrimName(d.DomainName)
			if name == "" {
				name = alidnsTrimName(d.PunyCode)
			}
			if name == "" {
				// 没有名字的域名既不能展示也不能用于任何后续调用，
				// 与其在界面上留一个空气泡，不如跳过。
				continue
			}
			id := strings.TrimSpace(d.DomainID)
			if id == "" {
				// 域名不在阿里云注册时 DomainId 可能是空的。dns.Zone.ID 的
				// 约定是"服务商侧的稳定标识"，而阿里云的一切记录操作都按
				// **域名**定位（DescribeDomainRecords 要的是 DomainName），
				// 域名本身就是那个稳定标识，所以退回用域名。
				id = name
			}
			out = append(out, dns.Zone{ID: id, Name: name})
		}

		if !alidnsMorePages(len(items), seen, resp.TotalCount, alidnsDomainsPerPage) {
			break
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 记录
// ---------------------------------------------------------------------------

// ListRecords 实现 dns.RecordLister。
func (a *Alidns) ListRecords(ctx context.Context, cred dns.Credential,
	zone dns.Zone, filter dns.RecordFilter) ([]dns.Record, error) {

	zoneName := alidnsTrimName(zone.Name)
	if zoneName == "" {
		// 阿里云按域名而不是 ID 定位记录，没有域名就无从查起。
		return nil, errors.New(i18n.T("tier1.ali.need_zone_name"))
	}

	var nameFilter, rrKeyword string
	if strings.TrimSpace(filter.Name) != "" {
		nameFilter = alidnsTrimName(filter.Name)
		rr, err := alidnsRR(zoneName, filter.Name)
		if err != nil {
			// 过滤器给了一个不属于本区域的记录名，说明调用方把参数搞错了。
			// 返回空列表会把这个错误伪装成"这里没有记录"，报错更容易定位。
			return nil, err
		}
		// 根记录的 RR 是 "@" 这个占位符，而 RRKeyWord 是"前后模糊匹配"。
		// 我们无法确认服务端把 "@" 当普通字面量检索；万一不是，用户查根记录
		// 会拿到一个**假的空列表**，然后很可能去新建一条重复记录。
		// 所以只在 RR 是真正的子域名时才用它收窄查询，根记录退化成
		// 全量拉取 + 客户端精确过滤：多翻几页，换一个不骗人的结果。
		if rr != "@" {
			rrKeyword = rr
		} else {
			// 反过来，过滤器里写 "@" 的人想要的是根记录本身，
			// 而比较用的是完整域名 —— 不归一化的话什么都匹配不上。
			// （alidnsRR 认 "@" 这种写法，这里就跟它保持一致。）
			nameFilter = zoneName
		}
	}

	var out []dns.Record
	seen := 0

	for page := 1; page <= maxPages; page++ {
		params := url.Values{}
		params.Set("Action", "DescribeDomainRecords")
		params.Set("DomainName", zoneName)
		params.Set("PageNumber", strconv.Itoa(page))
		params.Set("PageSize", strconv.Itoa(alidnsRecordsPerPage))
		if rrKeyword != "" {
			// 只是在服务端先粗筛一遍，精确匹配仍然由下面做：
			// 这个参数是模糊匹配，会把 "wwww" 也带回来。
			params.Set("RRKeyWord", rrKeyword)
		}
		if filter.Type != "" {
			// TypeKeyWord 文档写明是"全匹配、不区分大小写"，但客户端仍然
			// 复核一遍 —— 少一次"服务端语义变了而我们没跟上"的风险。
			params.Set("TypeKeyWord", string(filter.Type))
		}

		var resp alidnsRecordsResp
		if err := a.call(ctx, cred, i18n.T("tier1.op.list_records"), params, &resp); err != nil {
			return nil, err
		}

		items := resp.DomainRecords.Record
		seen += len(items)

		for _, r := range items {
			rec := alidnsToRecord(r, zoneName)
			if !alidnsMatches(rec, nameFilter, filter.Type) {
				continue
			}
			out = append(out, rec)
		}

		if !alidnsMorePages(len(items), seen, resp.TotalCount, alidnsRecordsPerPage) {
			break
		}
	}
	return out, nil
}

// CreateRecord 实现 dns.RecordCreator。
func (a *Alidns) CreateRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, rec dns.Record) (dns.Record, error) {

	zoneName := alidnsTrimName(zone.Name)
	rr, err := alidnsRR(zoneName, rec.Name)
	if err != nil {
		return dns.Record{}, err
	}
	if !supportedRecordType(rec.Type) {
		return dns.Record{}, errors.New(i18n.T("tier1.need_type"))
	}

	params := url.Values{}
	params.Set("Action", "AddDomainRecord")
	params.Set("DomainName", zoneName)
	params.Set("RR", rr)
	params.Set("Type", string(rec.Type))
	params.Set("Value", rec.Content)
	params.Set("TTL", strconv.Itoa(alidnsTTL(normalizeTTL(rec.TTL))))
	// 解析线路刻意不传：阿里云对缺省的 Line 用默认线路（"default"），
	// 而 dns.Record 里没有 Line 字段，传什么都是在替用户瞎猜。
	//
	// 优先级只在调用方明确给了正值时才传。阿里云文档把它写作"MX 记录的
	// 优先级，取值范围 [1,50]，MX 记录必填"，但其它类型是否接受这个参数
	// 文档没说 —— 与其替服务端做判断，不如原样透传：调用方没设就不发，
	// 设了就发，被拒绝的话错误信息里有服务端的原话。
	if rec.Priority > 0 {
		params.Set("Priority", strconv.Itoa(rec.Priority))
	}

	var resp alidnsRecordIDResp
	if err := a.call(ctx, cred, i18n.T("tier1.op.create_record"), params, &resp); err != nil {
		return dns.Record{}, err
	}
	// AddDomainRecord 只返回 RecordId，不返回记录本身；拿不到它就等于
	// "我们不知道刚写进去的是哪一条"，后续的修改与删除都失去依据，
	// 所以这里必须当成失败，而不是假装成功。
	recordID := strings.TrimSpace(resp.RecordID)
	if recordID == "" {
		return dns.Record{}, errors.New(i18n.T("tier1.ali.no_record_id"))
	}

	out := rec
	out.ID = recordID
	// 回显的 TTL 是**实际写进去的值**（缺省时是 600），而不是调用方给的 0：
	// 界面上显示"服务商默认"是没问题的，但记录本身有确切值，回显确切值
	// 才不会让下一次比较凭空多出一处差异。
	out.TTL = alidnsTTL(normalizeTTL(rec.TTL))
	// 记录名按"服务端会返回的形式"归一化（去尾点、拼回完整域名），
	// 这样刚创建完的这条记录与随后列表拉回来的那条是同一个字符串。
	out.Name = alidnsFullName(zoneName, rr)
	// 备注与代理开关在阿里云没有对应参数：Comment 会被丢弃（接口文档里
	// Comment 本来就标注为"仅部分服务商支持"），Proxied 恒为 false。
	out.Proxied = false
	out.Comment = ""
	return out, nil
}

// UpdateRecord 实现 dns.RecordUpdater。
func (a *Alidns) UpdateRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, rec dns.Record) (dns.Record, error) {

	recordID := strings.TrimSpace(rec.ID)
	if recordID == "" {
		return dns.Record{}, errors.New(i18n.T("tier1.ali.need_id_update"))
	}
	zoneName := alidnsTrimName(zone.Name)
	rr, err := alidnsRR(zoneName, rec.Name)
	if err != nil {
		return dns.Record{}, err
	}
	if !supportedRecordType(rec.Type) {
		return dns.Record{}, errors.New(i18n.T("tier1.need_type"))
	}

	// 先读回这条记录，只为了拿到它的解析线路。
	//
	// 阿里云的 UpdateDomainRecord 是一次"整条记录覆盖写"，而 dns.Record 里
	// 没有 Line 字段：省略 Line 时文档只说"默认为 default"，并没有承诺保留
	// 原值。两种读法都说得通，但猜错的代价是不对称的 —— 多读一次只是一个
	// 请求，猜错则会在"只改了个 IP"这种操作里把用户配好的解析线路（例如
	// "境外"）悄悄改回默认线路，而且**没有任何人会立刻发现**。
	// 所以这里按"省略即改回默认"来防：读回原值、原样送回。读不到就让这次
	// 修改失败并把服务端的原话交给用户 —— 失败可以重试，静默的数据损坏不能。
	line, err := a.recordLine(ctx, cred, recordID)
	if err != nil {
		return dns.Record{}, err
	}

	params := url.Values{}
	params.Set("Action", "UpdateDomainRecord")
	params.Set("RecordId", recordID)
	params.Set("RR", rr)
	params.Set("Type", string(rec.Type))
	params.Set("Value", rec.Content)
	params.Set("TTL", strconv.Itoa(alidnsTTL(normalizeTTL(rec.TTL))))
	if rec.Priority > 0 {
		params.Set("Priority", strconv.Itoa(rec.Priority))
	}
	if line != "" {
		params.Set("Line", line)
	}

	var resp alidnsRecordIDResp
	if err := a.call(ctx, cred, i18n.T("tier1.op.update_record"), params, &resp); err != nil {
		return dns.Record{}, err
	}

	out := rec
	out.ID = recordID
	out.TTL = alidnsTTL(normalizeTTL(rec.TTL))
	out.Name = alidnsFullName(zoneName, rr)
	out.Proxied = false
	out.Comment = ""
	return out, nil
}

// DeleteRecord 实现 dns.RecordDeleter。
//
// 这里刻意不要求 zone.Name：阿里云按 RecordId 删除，不需要域名，
// 而"只知道 ID 却删不掉"没有任何好处。（与 ListRecords 相反 ——
// 那个接口真的是按域名查的。）
func (a *Alidns) DeleteRecord(ctx context.Context, cred dns.Credential,
	_ dns.Zone, recordID string) error {

	recordID = strings.TrimSpace(recordID)
	if recordID == "" {
		return errors.New(i18n.T("tier1.need_id_delete"))
	}

	params := url.Values{}
	params.Set("Action", "DeleteDomainRecord")
	params.Set("RecordId", recordID)

	var resp alidnsRecordIDResp
	return a.call(ctx, cred, i18n.T("tier1.op.delete_record"), params, &resp)
}

// recordLine 读回一条记录当前的解析线路。
//
// 用 DescribeDomainRecordInfo（按 RecordId 查）而不是 DescribeDomainRecords
// （按域名 + 关键字查）：后者要翻页、还要在结果里挑出匹配的那一条，
// 而这里只想知道一个字段。
func (a *Alidns) recordLine(ctx context.Context, cred dns.Credential, recordID string) (string, error) {
	params := url.Values{}
	params.Set("Action", "DescribeDomainRecordInfo")
	params.Set("RecordId", recordID)

	var resp alidnsRecord
	if err := a.call(ctx, cred, i18n.T("tier1.op.get_record"), params, &resp); err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Line), nil
}

// ---------------------------------------------------------------------------
// 名字与取值转换
// ---------------------------------------------------------------------------

// alidnsRR 把完整记录名转成阿里云要的 RR（主机记录）。
//
// 这是整个阿里云实现里最容易出错的一步，规则比看上去多：
//
//   - 根记录（example.com）的 RR 是 **"@"**，不是空串、也不是域名本身。
//     官方文档为此专门写了一句话："如果要解析 example.com，主机记录要填写 @，
//     而不是空"。
//   - RR 是**去掉区域名之后的前缀**（www、a.b），不是完整域名。
//     把完整域名填进去，阿里云会在 example.com 下面建一条名为
//     www.example.com.example.com 的记录，而且**不会报错** —— 这是最坏的一类
//     错误：用户看到一次成功，得到的却是一条永远解析不到的记录。
//   - 记录名不在该区域下时必须直接报错，绝不能"猜一个前缀"写出去。
//   - 尾点是合法的 FQDN 写法（www.example.com.），大小写也不敏感，
//     两者都要在这里消化掉，否则会拼出一个带尾点或大小写不一致的 RR。
func alidnsRR(zoneName, recordName string) (string, error) {
	zone := alidnsTrimName(zoneName)
	name := alidnsTrimName(recordName)

	if zone == "" {
		return "", errors.New(i18n.T("tier1.ali.need_zone_for_host"))
	}
	if name == "" {
		return "", errors.New(i18n.T("tier1.need_name"))
	}
	// 调用方直接给了 RR 形式的根记录。dns.Record.Name 的约定是完整域名，
	// 但 "@" 是阿里云/腾讯云/ddns-go 都在用的写法，认它比报错有用。
	if name == "@" {
		return "@", nil
	}
	if strings.EqualFold(name, zone) {
		return "@", nil
	}

	// 用原始（未折叠大小写）的字符串做切分：strings.ToLower 在非 ASCII 上
	// 可能改变字节长度，先折小写再按长度切片会切出半个字符。
	if len(name) <= len(zone) {
		return "", alidnsNameError(recordName, zone)
	}
	cut := len(name) - len(zone)
	if name[cut-1] != '.' || !strings.EqualFold(name[cut:], zone) {
		// 注意这里必须同时检查"前面那个字符是点"：否则 notexample.com
		// 会被当成 example.com 下的记录，前缀切出个 "not" 来。
		return "", alidnsNameError(recordName, zone)
	}

	rr := name[:cut-1]
	if rr == "" {
		return "", alidnsNameError(recordName, zone)
	}
	if rr == "@" {
		// "@.example.com" —— 移植过来的动态解析代码就是这么拼根域名的。
		return "@", nil
	}
	return rr, nil
}

// alidnsNameError 拼一条能告诉用户"该写成什么样"的错误。
func alidnsNameError(recordName, zoneName string) error {
	return fmt.Errorf(i18n.T("tier1.ali.name_outside_zone"),
		strings.TrimSpace(recordName), zoneName, zoneName)
}

// alidnsFullName 把服务端的 RR 还原成完整记录名。
func alidnsFullName(zoneName, rr string) string {
	zone := alidnsTrimName(zoneName)
	rr = strings.TrimSpace(rr)
	if rr == "" || rr == "@" || strings.EqualFold(rr, zone) {
		// 根记录。第三个条件是防御：服务端理论上只返回 "@"，
		// 但把域名本身填进 RR 的账号确实存在，别拼出 example.com.example.com。
		return zone
	}
	return rr + "." + zone
}

// alidnsTrimName 归一化一个域名：去掉首尾空白与末尾的根点。
//
// 去空白不是洁癖：阿里云文档的响应示例里 DomainName 就带着一个换行
// （"example.com\n"）。不清理的话，记录名会变成 "www.example.com\n" ——
// 界面上看不出差别，但相等比较、跨服务商复制、写回服务商全都会出问题。
func alidnsTrimName(s string) string {
	return strings.TrimSuffix(strings.TrimSpace(s), ".")
}

// alidnsToRecord 把服务端的记录翻译成对外的表达。
func alidnsToRecord(r alidnsRecord, zoneName string) dns.Record {
	zone := alidnsTrimName(r.DomainName)
	if zone == "" {
		zone = zoneName
	}
	return dns.Record{
		ID:   strings.TrimSpace(r.RecordID),
		Name: alidnsFullName(zone, r.RR),
		Type: dns.RecordType(strings.TrimSpace(r.Type)),
		// 值不做 Trim：TXT 记录的值里首尾的空格可能是用户内容的一部分。
		Content:  r.Value,
		TTL:      r.TTL,
		Priority: r.Priority,
		// Comment 映射到 Remark。备注在写入接口里没有对应参数，
		// 因此它在这里是**只读**的：读得回来，写不回去。
		Comment: strings.TrimSpace(r.Remark),
		// Proxied 恒为 false：阿里云的解析记录没有 CDN 代理开关，
		// 云解析与 CDN 是两套产品。
	}
}

// alidnsMatches 复核服务端返回的记录是否真的满足过滤条件。
//
// 服务端的 RRKeyWord 是模糊匹配（会把 wwww 也带回来），而 TypeKeyWord 的
// 语义将来也可能变；在客户端再比一次的成本可以忽略，换来的是"过滤条件是
// 精确匹配"这个承诺真的成立。
func alidnsMatches(rec dns.Record, name string, recordType dns.RecordType) bool {
	if recordType != "" && !strings.EqualFold(string(rec.Type), string(recordType)) {
		return false
	}
	if name != "" && !strings.EqualFold(alidnsTrimName(rec.Name), name) {
		return false
	}
	return true
}

// alidnsTTL 把对外的 TTL 表达转成阿里云的。
//
// 0（"交给服务商默认"）在阿里云里就是 600，文档写明了这个默认值。
// 其余值**原样透传**，不做区间夹取：不同版本的最小 TTL 不一样（免费版与
// 个人版 600 秒，企业旗舰版 1 秒），在客户端猜一个下限，只会把"服务端能
// 说清楚的一次拒绝"变成"用户设了 60，实际写进去 600"。
func alidnsTTL(ttl int) int {
	if ttl <= 0 {
		return alidnsDefaultTTL
	}
	return ttl
}

// alidnsMorePages 报告"还有下一页"。
//
// 阿里云返回的是总数（TotalCount）而不是总页数，因此结束条件要同时看
// 本页是否取满、以及服务端报的总数：
//
//   - 只看总数：TotalCount 缺失（0）时只取一页，用户会看到半个列表；
//   - 只看页大小：服务端若把 PageSize 截成更小的值，会多跑一次空请求。
//
// 页数上限用共享的 maxPages 兜底，防止畸形响应把内核拖进死循环。
func alidnsMorePages(pageItems, seen, total, perPage int) bool {
	if pageItems == 0 || pageItems < perPage {
		return false
	}
	return total <= 0 || seen < total
}
