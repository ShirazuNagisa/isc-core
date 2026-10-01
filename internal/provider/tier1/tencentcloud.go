package tier1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/ddnsgo"
	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件实现腾讯云 DNS（DNSPod API 3.0）的完整记录管理。
//
// 端点、版本号、Action 名与 TC3 签名方式全部照抄 internal/ddnsgo 里那份
// 从 ddns-go 移植过来的实现（provider_tencent_cloud.go）—— 那套调用方式
// 有海量用户在实际跑，没有理由另起一套。
//
// 它与 Cloudflare 实现最大的结构差异：腾讯云把鉴权编进了**每个请求的内容**
// （TC3 签名覆盖 action、时间戳与请求体），所以这里有一个专用的请求函数，
// 见 tcCall。

// tcEndpoint 是腾讯云 DNSPod API 的端点。
//
// 注意主机名的第一段是 dnspod 而不是 tencentcloud：腾讯云按**产品**划分
// 域名，DNSPod 的记录接口只在这个域名上提供。
const tcEndpoint = "https://dnspod.tencentcloudapi.com"

// tcVersion 是 DNSPod API 的版本号。
//
// 必须与移植代码里的常量一致。版本号写错会得到 InvalidAction /
// 参数错误，而这类错误在假服务器的测试里是发现不了的。
const tcVersion = "2021-03-23"

// tcService 是 TC3 签名里的服务名。
//
// 必须是 "dnspod" 而不是 "tencentcloud"：TC3 的签名串与派生密钥都按
// **产品名**计算（签出来的凭证作用域形如 2026-01-02/dnspod/tc3_request），
// 填错会得到 AuthFailure.SignatureFailure，而且报错信息不会告诉你
// 是服务名错了。
//
// ddns-go 里这个值对应未导出的常量 svcDnsPod，跨包引用不到，因此这里
// 只能以字面量重复一次。代价是两边可能漂移 —— 测试里断言了签名作用域中
// 的服务名，防止它被悄悄改成别的值。
const tcService = "dnspod"

// tcDefaultLine 是记录的默认线路。
//
// 腾讯云的 CreateRecord / ModifyRecord 把 RecordLine 定为**必填**，
// 而 dns.Record 里没有线路字段 —— 新建时能表达的只有"默认"。
const tcDefaultLine = "默认"

// 腾讯云的 Action 名。
//
// 单独定义常量而不是散在调用处：同一个字符串既要放进 X-TC-Action 头，
// 又要参与 TC3 签名，两处写字面量一旦不一致，得到的是一个毫无线索的
// InvalidAction。
const (
	tcActionDescribeDomainList = "DescribeDomainList"
	tcActionDescribeRecordList = "DescribeRecordList"
	tcActionDescribeRecord     = "DescribeRecord"
	tcActionCreateRecord       = "CreateRecord"
	tcActionModifyRecord       = "ModifyRecord"
	tcActionDeleteRecord       = "DeleteRecord"
)

// 分页参数。
//
// 腾讯云的 DescribeRecordList 一次最多取 3000 条（默认 100），
// DescribeDomainList 的默认值同样是 3000。这里仍然只要 100 ——
// 页大小要与"最多一百页"的上限配合，才能给内存占用和往返次数一个
// 明确的界。不复用 cloudflare.go 里的同名常量：那是按 Cloudflare 的
// 上限定的，两家调整上限的时机不同，共用一个值只会互相牵连。
const (
	tcZonesPerPage   = 100
	tcRecordsPerPage = 100
)

// TencentCloud 实现腾讯云 DNS（DNSPod）的完整记录管理。
type TencentCloud struct {
	dynamicDelegate
	meta    dns.Meta
	baseURL string
}

// 编译期断言：接口实现必须完整。
var (
	_ dns.Provider       = (*TencentCloud)(nil)
	_ dns.DynamicUpdater = (*TencentCloud)(nil)
	_ dns.ZoneLister     = (*TencentCloud)(nil)
	_ dns.RecordLister   = (*TencentCloud)(nil)
	_ dns.RecordCreator  = (*TencentCloud)(nil)
	_ dns.RecordUpdater  = (*TencentCloud)(nil)
	_ dns.RecordDeleter  = (*TencentCloud)(nil)
)

// NewTencentCloud 构造腾讯云实现。
//
// baseURL 为空时使用官方地址；测试传入本地假服务器地址。
//
// meta 里的 Name 与 DisplayName 必须与 provider 包登记的一致
// （internal/provider/builtin.go）—— 注册表按 Name 找实现，凭据按 Name
// 关联，对不上就会变成"界面上有这家、调用时找不到实现"。
func NewTencentCloud(baseURL string) *TencentCloud {
	if baseURL == "" {
		baseURL = tcEndpoint
	}
	return &TencentCloud{
		meta: dns.Meta{
			Name:        "tencentcloud",
			DisplayName: "腾讯云 DNS",
			Tier:        1,
		},
		baseURL: baseURL,
	}
}

// Meta 实现 dns.Provider。
func (t *TencentCloud) Meta() dns.Meta { return t.meta }

// clientFor 为一次调用构造客户端。
//
// 与 Cloudflare 不同，这里设置不了鉴权头：TC3 的 Authorization 由每次请求
// 的 action、时间戳与请求体共同决定，只能在构造请求时现签（见 tcCall），
// 因此本方法只负责基址与超时这类公共部分。
//
// cred 参数保留是为了与其它 Tier-1 实现保持同一形状；网卡绑定由 newClient
// 支持，但记录管理接口（dns.ZoneLister 等）没有"指定网卡"的入口，
// 那是 dns.DynamicRequest 才有的字段，所以这里与 Cloudflare 一样用默认网卡。
func (t *TencentCloud) clientFor(_ dns.Credential) *client {
	return newClient(t.baseURL, "")
}

// ---------------------------------------------------------------------------
// 响应结构
// ---------------------------------------------------------------------------

// tcEnvelope 是腾讯云统一的响应信封：无论成败，业务字段都在 Response 下。
type tcEnvelope struct {
	Response tcResponse `json:"Response"`
}

// tcResponse 是 Response 里的内容。
//
// 各 action 的业务字段都在这同一层，字段名互不重叠（DomainList /
// RecordList / RecordInfo / RecordId），因此用一个结构承载全部 ——
// 换成每家 action 一个结构，就要把 Error 判定抄六遍，而"某个 action
// 忘了看 Error"会把失败当成功，那是最危险的一类误报。
type tcResponse struct {
	// RequestID 只用于排障：腾讯云的文档明确要求报障时提供它。
	RequestID string `json:"RequestId"`
	// Error 非空表示业务失败。注意**业务失败同样返回 HTTP 200**。
	Error *tcError `json:"Error"`

	// DescribeDomainList。
	DomainList []tcDomain `json:"DomainList"`

	// DescribeRecordList。
	RecordList []tcRecord `json:"RecordList"`

	// DescribeRecord。
	RecordInfo *tcRecordInfo `json:"RecordInfo"`

	// CreateRecord / ModifyRecord：只返回记录编号，不回显整条记录。
	RecordID *uint64 `json:"RecordId"`
}

// tcError 是腾讯云的错误对象。
type tcError struct {
	Code    string `json:"Code"`
	Message string `json:"Message"`
}

// tcDomain 是 DescribeDomainList 返回的域名条目。
//
// 只取用得上的字段：响应里还有套餐、DNS 状态、标签等十几个字段，
// 全部映射一遍只会让"响应改了"变成一件难以察觉的事。
type tcDomain struct {
	// DomainID 是域名的数字编号。
	//
	// 用 uint64 解码而不是 float64：编号已经是十几亿量级，
	// 浮点解码会在大数上丢精度，得到一个指向别的域名的编号。
	DomainID uint64 `json:"DomainId"`
	// Name 是域名本身（dnspod.cn）。
	Name string `json:"Name"`
	// Status 取值 ENABLE / PAUSE / SPAM。
	Status string `json:"Status"`
}

// tcRecord 是 DescribeRecordList 返回的记录条目。
//
// 注意这里的字段名与 DescribeRecord 的响应**不一样**：列表接口用
// Name / Type / Line，详情接口用 SubDomain / RecordType / RecordLine。
// 同一个产品里两套命名，抄错一个就得到一条字段全空的记录。
type tcRecord struct {
	RecordID uint64 `json:"RecordId"`
	// Name 是主机记录（www），根域名是 "@" —— 不是完整域名。
	Name  string `json:"Name"`
	Type  string `json:"Type"`
	Value string `json:"Value"`
	TTL   int    `json:"TTL"`
	// MX 是优先级；非 MX 记录该字段恒为 0。
	MX int `json:"MX"`
	// Remark 是备注，对应 dns.Record.Comment。
	Remark string `json:"Remark"`
}

// tcRecordInfo 是 DescribeRecord 返回的记录详情。
//
// 这里只留 RecordLine：调用它的唯一目的是查出一条记录所在的**线路**，
// 而 dns.Record 里没有线路字段（见 UpdateRecord 的说明）。
type tcRecordInfo struct {
	RecordLine string `json:"RecordLine"`
}

// ---------------------------------------------------------------------------
// 请求体
// ---------------------------------------------------------------------------

// tcDomainListRequest 是 DescribeDomainList 的请求体。
//
// 刻意不传 Type（域名分组类型）：默认值是 ALL，即"我名下的 + 别人分享给我的"。
// 收窄成 MINE 会让"别人分享过来、但确实要在这里维护"的域名凭空消失，
// 而那是一个用户完全无从排查的问题；没有权限的域名交给服务商在写操作时拒绝，
// 它的报错说得比我们清楚。
type tcDomainListRequest struct {
	Offset int `json:"Offset"`
	Limit  int `json:"Limit"`
}

// tcRecordListRequest 是 DescribeRecordList 的请求体。
type tcRecordListRequest struct {
	// Domain 是域名本身（dnspod.cn），不是域名的数字编号。
	Domain string `json:"Domain"`
	// SubDomain 是主机记录（www；根域名为 "@"）。
	//
	// 字段名用 SubDomain 而不是历史上那个 Subdomain：文档写明两个参数同时
	// 传递时后端优先取 SubDomain，移植过来的 ddns-go 用的也是它。
	SubDomain string `json:"SubDomain,omitempty"`
	// RecordType 为空表示不按类型过滤。
	RecordType string `json:"RecordType,omitempty"`
	Offset     int    `json:"Offset"`
	Limit      int    `json:"Limit"`
	// ErrorOnEmpty 固定为 "no"。
	//
	// 这个参数的默认值是 "yes"：查不到记录时**报错**而不是返回空列表。
	// 对"列出记录"来说那是错的语义 —— 一个还没有任何记录（或过滤条件
	// 暂时没命中）的区域应当得到空列表；顺带也让分页的"多看一页"不会
	// 在末页之后炸掉。
	ErrorOnEmpty string `json:"ErrorOnEmpty"`
}

// tcRecordIDRequest 是 DescribeRecord / DeleteRecord 的请求体：都只需要
// 域名 + 记录编号。
type tcRecordIDRequest struct {
	Domain   string `json:"Domain"`
	RecordID uint64 `json:"RecordId"`
}

// tcRecordBody 是 CreateRecord / ModifyRecord 的请求体。
//
// 两个 action 共用一个结构：字段几乎相同（修改多一个必填的 RecordId），
// 拆成两个只会让"改了一边忘了另一边"成为可能。移植过来的 ddns-go
// 也是这么做的。
type tcRecordBody struct {
	Domain     string `json:"Domain"`
	SubDomain  string `json:"SubDomain"`
	RecordType string `json:"RecordType"`
	RecordLine string `json:"RecordLine"`
	Value      string `json:"Value"`
	// RecordID 只在修改时出现。新建时它必然是 0，omitempty 会把它省掉 ——
	// 发一个 "RecordId": 0 过去只会得到"记录编号错误"。
	RecordID uint64 `json:"RecordId,omitempty"`
	// TTL 为 0（"交给服务商默认值"）时不发送：腾讯云新建记录的默认值是
	// 600 秒。不在这里替用户挑一个值 —— 各套餐允许的最小 TTL 不同
	//（免费版是 600），硬编码一张表迟早会过期，交给服务商拒绝并如实转达
	// 它的说明更准确。
	TTL int `json:"TTL,omitempty"`
	// MX 承载 MX / HTTPS / SVCB 记录的优先级，这三个类型**必填**。
	// 用指针是为了把"优先级为 0"和"这条记录没有优先级"区分开。
	MX *int `json:"MX,omitempty"`
	// Remark 刻意**不加** omitempty：ModifyRecord 的文档写明"传空删除备注"，
	// 省掉这个字段就永远清不掉一条记录上的备注。
	Remark string `json:"Remark"`
}

// ---------------------------------------------------------------------------
// 区域
// ---------------------------------------------------------------------------

// ListZones 实现 dns.ZoneLister。
//
// 分页拉全量而不是只取第一页：用户可能管理着几十个域名，而"列表里少了一半"
// 是一个很难被察觉、又很让人恼火的问题。
func (t *TencentCloud) ListZones(ctx context.Context, cred dns.Credential) ([]dns.Zone, error) {
	cl := t.clientFor(cred)

	var out []dns.Zone
	offset := 0
	// 页数上限沿用包里的 maxPages（见 cloudflare.go）：畸形响应导致的
	// 死循环与是哪家服务商无关。
	for page := 0; page < maxPages; page++ {
		var env tcEnvelope
		err := tcCall(ctx, cl, cred, tcActionDescribeDomainList, "列出区域",
			tcDomainListRequest{Offset: offset, Limit: tcZonesPerPage}, &env)
		if err != nil {
			return nil, err
		}

		items := env.Response.DomainList
		out = append(out, tcToZones(items)...)

		// 用**服务商返回的条数**判断是否到末页，而不是过滤后的条数：
		// 被滤掉的暂停域名同样是这一页的数据，拿过滤后的长度去比较
		// 会让分页提前结束，静默漏掉后面几页的域名。
		if len(items) < tcZonesPerPage {
			break
		}
		offset += len(items)
	}
	return out, nil
}

// tcToZones 把域名条目翻译成 dns.Zone。
func tcToZones(items []tcDomain) []dns.Zone {
	out := make([]dns.Zone, 0, len(items))
	for _, d := range items {
		name := strings.TrimSpace(d.Name)
		if name == "" {
			continue
		}
		// 只保留 ENABLE 的域名：暂停（PAUSE）与封禁（SPAM）的域名下改记录
		// 不会生效，列出来只会让用户以为"我改了但没用" —— 与 Cloudflare
		// 那边滤掉 pending 是同一个理由。
		//
		// 代价是用户看不到自己暂停过的域名。dns.Zone 只有 ID 与 Name 两个
		// 字段，没有地方如实说明"这个域名被暂停了"，因此宁可少列，
		// 也不给一个能点、点了却无效的条目。
		if d.Status != "" && d.Status != "ENABLE" {
			continue
		}
		out = append(out, dns.Zone{
			ID:   strconv.FormatUint(d.DomainID, 10),
			Name: name,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// 记录
// ---------------------------------------------------------------------------

// ListRecords 实现 dns.RecordLister。
func (t *TencentCloud) ListRecords(ctx context.Context, cred dns.Credential,
	zone dns.Zone, filter dns.RecordFilter) ([]dns.Record, error) {

	domain, err := tcDomainOf(zone)
	if err != nil {
		return nil, err
	}

	cl := t.clientFor(cred)

	var out []dns.Record
	offset := 0
	for page := 0; page < maxPages; page++ {
		req := tcRecordListRequest{
			Domain:       domain,
			Offset:       offset,
			Limit:        tcRecordsPerPage,
			ErrorOnEmpty: "no",
		}
		if filter.Type != "" {
			req.RecordType = string(filter.Type)
		}
		if filter.Name != "" {
			// 过滤用的名字也是完整域名，要转成主机记录。
			req.SubDomain = tcSubDomain(domain, filter.Name)
		}

		var env tcEnvelope
		if err := tcCall(ctx, cl, cred, tcActionDescribeRecordList, "列出记录", req, &env); err != nil {
			return nil, err
		}

		items := env.Response.RecordList
		for _, item := range items {
			out = append(out, tcToRecord(domain, item))
		}
		if len(items) < tcRecordsPerPage {
			break
		}
		offset += len(items)
	}
	return out, nil
}

// tcToRecord 把腾讯云的记录翻译成 dns.Record。
//
// 这里刻意**不按 Status 过滤**：被停用（DISABLE）的记录依然存在，而且
// 用户可能正想在 ISC 里把它改回去；藏起来只会让他在列表里找不到这条记录。
// （域名列表那边滤掉暂停域名是另一回事：那里滤掉的是一个整体失效的容器。）
func tcToRecord(zoneName string, r tcRecord) dns.Record {
	return dns.Record{
		// RecordId 是数字，dns.Record.ID 是字符串：在这里转一次，
		// 写回时再转回来（见 tcParseRecordID）。用 FormatUint 而不是
		// 走浮点，避免大编号丢精度。
		ID:      strconv.FormatUint(r.RecordID, 10),
		Name:    tcFullName(zoneName, r.Name),
		Type:    dns.RecordType(r.Type),
		Content: r.Value,
		TTL:     normalizeTTL(r.TTL),
		// 非 MX / HTTPS / SVCB 记录该字段恒为 0，与 dns.Record 的零值一致，
		// 因此可以直接赋值。
		Priority: r.MX,
		Comment:  r.Remark,
	}
}

// CreateRecord 实现 dns.RecordCreator。
func (t *TencentCloud) CreateRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, rec dns.Record) (dns.Record, error) {

	domain, err := tcDomainOf(zone)
	if err != nil {
		return dns.Record{}, err
	}

	// 新建只能用默认线路：dns.Record 没有线路字段，RecordLine 又是必填。
	//（修改时则会把原记录所在线路带回去，见 UpdateRecord。）
	body, err := tcRecordBodyFor(domain, tcDefaultLine, rec, 0)
	if err != nil {
		return dns.Record{}, err
	}

	cl := t.clientFor(cred)
	var env tcEnvelope
	if err := tcCall(ctx, cl, cred, tcActionCreateRecord, "新增记录", body, &env); err != nil {
		return dns.Record{}, err
	}
	if env.Response.RecordID == nil {
		// 没有编号就没法再改或删这条记录。返回一条 ID 为空的记录会让
		// 失败延后到下一次操作，且那时已经完全看不出问题出在哪。
		return dns.Record{}, errors.New("tier1: 新增记录成功但服务商未返回记录编号")
	}

	return tcApplyServerTruth(rec, domain, *env.Response.RecordID), nil
}

// UpdateRecord 实现 dns.RecordUpdater。
//
// # 为什么要先读一次原记录
//
// 腾讯云的 ModifyRecord 把 RecordLine（线路）与 Value、RecordType 等一起
// 定为必填，而 dns.Record 里**没有**线路字段 —— 调用方根本无从表达
// "这条记录挂在电信线路上"。如果固定填"默认"，一条多线路（负载均衡）的
// 记录会被连线路一起改掉，而用户在界面上看不到任何异常。
//
// 所以先读一次原记录的线路，再原样带回。这是**尽力而为**：读失败就退回
// 默认线路，而不是让整次修改失败 —— 有些 CAM 策略只授予写权限，
// 不该因为一次辅助查询把本来能成功的修改挡掉。
func (t *TencentCloud) UpdateRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, rec dns.Record) (dns.Record, error) {

	domain, err := tcDomainOf(zone)
	if err != nil {
		return dns.Record{}, err
	}
	id, err := tcParseRecordID(rec.ID)
	if err != nil {
		return dns.Record{}, err
	}

	cl := t.clientFor(cred)
	line := t.existingRecordLine(ctx, cl, cred, domain, id)

	body, err := tcRecordBodyFor(domain, line, rec, id)
	if err != nil {
		return dns.Record{}, err
	}

	var env tcEnvelope
	if err := tcCall(ctx, cl, cred, tcActionModifyRecord, "修改记录", body, &env); err != nil {
		return dns.Record{}, err
	}
	// 响应里的编号必须与请求的一致。不一致意味着改错了记录，
	// 那是绝不能当成成功的。
	if env.Response.RecordID != nil && *env.Response.RecordID != id {
		return dns.Record{}, fmt.Errorf(
			"tier1: 修改记录返回的编号 %d 与请求的 %d 不一致，请到控制台确认记录状态",
			*env.Response.RecordID, id)
	}

	return tcApplyServerTruth(rec, domain, id), nil
}

// existingRecordLine 查出一条记录当前所在的线路；查不到时返回默认线路。
func (t *TencentCloud) existingRecordLine(ctx context.Context, cl *client,
	cred dns.Credential, domain string, id uint64) string {

	var env tcEnvelope
	err := tcCall(ctx, cl, cred, tcActionDescribeRecord, "读取记录",
		tcRecordIDRequest{Domain: domain, RecordID: id}, &env)
	if err != nil || env.Response.RecordInfo == nil {
		return tcDefaultLine
	}
	if line := strings.TrimSpace(env.Response.RecordInfo.RecordLine); line != "" {
		return line
	}
	return tcDefaultLine
}

// DeleteRecord 实现 dns.RecordDeleter。
func (t *TencentCloud) DeleteRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, recordID string) error {

	domain, err := tcDomainOf(zone)
	if err != nil {
		return err
	}
	id, err := tcParseRecordID(recordID)
	if err != nil {
		return err
	}

	cl := t.clientFor(cred)
	var env tcEnvelope
	return tcCall(ctx, cl, cred, tcActionDeleteRecord, "删除记录",
		tcRecordIDRequest{Domain: domain, RecordID: id}, &env)
}

// ---------------------------------------------------------------------------
// 请求
// ---------------------------------------------------------------------------

// tcCall 发起一次腾讯云 API 调用并把响应解进 env。
//
// op 是中文的操作名，只用于用户可见的错误信息；action 是腾讯云的 Action。
//
// # 为什么不能复用 client.doJSON
//
// TC3 签名的输入里有三样东西：请求体的**精确字节**、content-type 与 host，
// 而且签名结果要以 Authorization 头写回**同一个请求**。doJSON 把"序列化
// 请求体"和"设置请求头"都藏在内部：调用方既拿不到待签的那串字节，
// 也拿不到那个 *http.Request。签名与请求构造是耦合的，因此这里单独走一条
// 路径 —— 但超时、响应上限、错误分类全部复用 client 里已有的东西，
// 不另起一套 HTTP 逻辑。
func tcCall(ctx context.Context, cl *client, cred dns.Credential,
	action, op string, body any, env *tcEnvelope) error {

	secretID, secretKey := cred.Field("secret_id"), cred.Field("secret_key")
	if secretID == "" || secretKey == "" {
		// 只说明缺了哪个字段，绝不回显它们的值。
		return errors.New("tier1: 腾讯云需要 SecretId 与 SecretKey（凭据字段 secret_id / secret_key）")
	}

	payload := []byte("{}")
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s: 序列化请求体失败: %w", op, err)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	// 腾讯云的所有 action 都打在这个端点的根路径上，靠 X-TC-Action 区分；
	// 签名串里的路径也被写死成 "/"（见 ddnsgo.TencentCloudSigner），
	// 因此 baseURL 里不能带路径前缀。
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.URL("/"), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%s: 构造请求失败: %w", op, err)
	}
	for k, v := range cl.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	// 该接口不需要 X-TC-Region，签名也不覆盖它 —— 与移植代码保持一致。
	req.Header.Set("X-TC-Version", tcVersion)

	// 签名必须在请求头设置好之后做：签名串覆盖了 content-type 与 host，
	// 而且传进去的 payload 必须与上面写进请求体的字节逐字相同。
	//
	// 另注：签名串里的主机名由服务名拼出（dnspod.tencentcloudapi.com），
	// 与 baseURL 无关。测试把 baseURL 指向本地假服务器时，签名对真实
	// 服务端是无效的 —— 假服务器不校验签名，这一点在测试文件里有说明。
	ddnsgo.TencentCloudSigner(secretID, secretKey, req, action, string(payload), tcService)

	resp, err := cl.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: 请求失败: %w", op, err)
	}
	defer resp.Body.Close() //nolint:errcheck // 只读响应，关闭失败无影响

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%s: 读取响应失败: %w", op, err)
	}

	if resp.StatusCode >= 400 {
		// 网关层的鉴权失败（AuthFailure）会走这里，它的错误体同样是
		// {"Response":{"Error":{...}}}，由 extractErrorMessage 兜住。
		return &APIError{
			Status:  resp.StatusCode,
			Code:    extractErrorCode(raw),
			Message: extractErrorMessage(raw),
			Op:      op,
		}
	}

	if err := json.Unmarshal(raw, env); err != nil {
		return fmt.Errorf("%s: 解析响应失败: %w", op, err)
	}
	// 业务错误同样走 HTTP 200，错误在 Response.Error 里。只看状态码会把
	// "记录编号错误"当成成功 —— 那是最危险的一类误报。
	if e := env.Response.Error; e != nil && (e.Code != "" || e.Message != "") {
		return fmt.Errorf("tier1: %s失败：%s", op, tcErrorText(env.Response))
	}
	return nil
}

// tcErrorText 汇总 Response.Error 里的错误说明。
//
// RequestId 一并带上：腾讯云的文档明确要求报障时提供它，
// 让用户能直接复制这一句话，比让他去翻日志有用得多。
func tcErrorText(r tcResponse) string {
	text := "未提供错误详情"
	switch e := r.Error; {
	case e == nil:
	case e.Code != "" && e.Message != "":
		text = e.Code + ": " + e.Message
	case e.Message != "":
		text = e.Message
	case e.Code != "":
		text = e.Code
	}
	if r.RequestID != "" {
		text += "（RequestId: " + r.RequestID + "）"
	}
	return text
}

// tcRecordBodyFor 组装新增 / 修改记录的请求体。
func tcRecordBodyFor(domain, line string, rec dns.Record, recordID uint64) (*tcRecordBody, error) {
	if !supportedRecordType(rec.Type) {
		return nil, errors.New("tier1: 记录类型不能为空")
	}
	if strings.TrimSpace(rec.Name) == "" {
		return nil, errors.New("tier1: 记录名不能为空")
	}
	if strings.TrimSpace(line) == "" {
		line = tcDefaultLine
	}

	body := &tcRecordBody{
		Domain:     domain,
		SubDomain:  tcSubDomain(domain, rec.Name),
		RecordType: string(rec.Type),
		RecordLine: line,
		Value:      rec.Content,
		RecordID:   recordID,
		TTL:        normalizeTTL(rec.TTL),
		Remark:     rec.Comment,
	}

	// 优先级只对 MX / HTTPS / SVCB 有意义 —— 按 CreateRecord 的文档，
	// 这三个类型的 MX 是必填项；发给别的类型会被拒或忽略。
	switch rec.Type {
	case dns.TypeMX, dns.RecordType("HTTPS"), dns.RecordType("SVCB"):
		priority := rec.Priority
		body.MX = &priority
	}
	return body, nil
}

// ---------------------------------------------------------------------------
// 名称与编号的翻译
// ---------------------------------------------------------------------------

// tcDomainOf 取出区域名（腾讯云的 Domain 参数）。
//
// 用的是**区域名**而不是 zone.ID：腾讯云所有记录接口都按域名（dnspod.cn）
// 定位，DomainId 只是个可选的加速参数。dns.Zone.ID 里存的是数字编号，
// 两者不能混用 —— 把编号填进 Domain 会得到 InvalidParameter.DomainInvalid。
func tcDomainOf(zone dns.Zone) (string, error) {
	name := strings.TrimSpace(zone.Name)
	if name == "" {
		return "", errors.New("tier1: 腾讯云需要区域名（域名），只给区域 ID 无法定位记录")
	}
	return name, nil
}

// tcFullName 把腾讯云的主机记录拼成完整记录名。
//
// 腾讯云（与 DNSPod 一脉相承）用"主机记录 + 域名"两个字段表达一条记录，
// 根域名的主机记录是 "@"；而 dns.Record.Name 约定为完整域名
// （www.example.com）。这里做一次拼接，返回给调用方的永远是完整域名。
func tcFullName(zoneName, sub string) string {
	sub = strings.TrimSpace(sub)
	if sub == "" || sub == "@" {
		return zoneName
	}
	return sub + "." + zoneName
}

// tcSubDomain 把完整记录名翻译成腾讯云的主机记录。
//
// 三种输入都要照顾到：
//
//	www.example.com  → www     完整域名，去掉区域后缀
//	example.com      → @       区域本身，腾讯云用 "@" 表示根
//	@                → @       调用方直接给了主机记录
//
// 以上都不是的输入**原样透传**：腾讯云的 SubDomain 永远是相对 Domain 的，
// 即便传进来一个别的域名，落到服务商那边也只会变成该域名下的一条记录，
// 不存在"写到别人区域里"的风险。把它判成错误反而会误伤
// "a.b"、"*"、"_acme-challenge" 这类合法的主机记录写法。
func tcSubDomain(zoneName, recordName string) string {
	name := strings.TrimSpace(recordName)
	if name == "" || name == "@" {
		return "@"
	}
	// 末尾的点是 FQDN 的合法写法（www.example.com.），腾讯云不接受它。
	name = strings.TrimSuffix(name, ".")
	zone := strings.Trim(strings.TrimSpace(zoneName), ".")
	if zone == "" {
		return name
	}
	if strings.EqualFold(name, zone) {
		return "@"
	}
	// 先按字节定位分隔点，再用 EqualFold 校验后半段：这样大小写不敏感，
	// 又不会在多字节域名上因为长度换算切错位置（切错时 EqualFold 会失败，
	// 结果是原样透传，而不是切出一个错误的子域名）。
	if idx := len(name) - len(zone) - 1; idx > 0 && name[idx] == '.' && strings.EqualFold(name[idx+1:], zone) {
		return name[:idx]
	}
	return name
}

// tcParseRecordID 把 dns.Record.ID 翻译成腾讯云的 RecordId。
//
// dns.Record.ID 是字符串（各家的编号形态不同，有的是 UUID），而腾讯云的
// RecordId 是 64 位整数。转换失败必须报错，绝不能当成 0：那要么去打一条
// "记录编号错误"，要么更糟 —— 命中编号为 0 的某条记录。
func tcParseRecordID(id string) (uint64, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return 0, errors.New("tier1: 腾讯云需要记录 ID")
	}
	v, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("tier1: 记录 ID %q 不是腾讯云的记录编号（应为十进制数字）", trimmed)
	}
	return v, nil
}

// tcApplyServerTruth 把服务商实际落库的形态补回记录里。
//
// 腾讯云的 CreateRecord / ModifyRecord 只返回记录编号，不回显整条记录，
// 所以返回的是"入参 + 服务端的编号"，而不是像 Cloudflare 那样返回服务端
// 的最终状态 —— 要拿到后者得再发一次 DescribeRecord，多一次往返换一个
// 刚写进去的值不划算。
//
// 两处仍然按服务端的事实修正：
//
//   - Name 归一成完整域名。调用方可能传 "www"（主机记录），而落库后它的
//     完整域名是 www.example.com；不归一的话，同一个字段在 Create 与
//     List 之间会出现两种形态。
//   - Proxied 恒为 false：腾讯云 DNS 没有 CDN 代理开关，把这个字段原样
//     返回会让调用方以为它生效了。
func tcApplyServerTruth(rec dns.Record, domain string, id uint64) dns.Record {
	rec.ID = strconv.FormatUint(id, 10)
	rec.Name = tcFullName(domain, tcSubDomain(domain, rec.Name))
	rec.Proxied = false
	return rec
}
