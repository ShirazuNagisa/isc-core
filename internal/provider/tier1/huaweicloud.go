package tier1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/ddnsgo"
	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// huaweicloudDefaultBase 是华为云 DNS 的默认基址。
//
// 与移植实现（internal/ddnsgo/provider_huawei.go）用的是同一个端点：
// 华为云 DNS 是全局服务，没有 region 概念，因此不需要按区域换域名。
const huaweicloudDefaultBase = "https://dns.myhuaweicloud.com"

// huaweicloudPerPage 是分页拉取时每页的条数。
//
// 500 是华为云文档给 limit 的上限（域名列表与记录集列表都是 0~500、
// 默认 500）。取上限能把往返次数压到最少。
const huaweicloudPerPage = 500

// huaweicloudDefaultTTL 是"交给服务商默认值"时使用的 TTL。
//
// 华为云没有 Cloudflare 那种表示 auto 的哨兵值：它的 ttl 就是
// 1~2147483647 的秒数，文档里的默认值是 300。既然总要发一个值，
// 就发文档写明的那个 —— 把"默认"留给服务商，意味着我们无法预料
// 它以后变成多少。
const huaweicloudDefaultTTL = 300

// ---------------------------------------------------------------------------
// 记录集（recordset）与"一条记录"的语义差 —— 本文件所有取舍的来源
// ---------------------------------------------------------------------------
//
// 华为云与其它服务商最大的差异在这里：
//
//   - Cloudflare 这类服务商里，一个 (name, type) 可以有多条**独立的**记录，
//     每条有自己的 ID，能单独改、单独删。
//   - 华为云里，(name, type) 唯一确定一个**记录集**，多条值放在同一个
//     recordset 的 records 数组里。整个数组共享一个 ID、一个 TTL、
//     一段 description —— API 层面根本不存在"只改其中一条值"的操作。
//
// 而 dns.Record 描述的是"一条记录"。两边对不上，只能选一种映射：
//
//	读：把一个记录集**展开**成 len(records) 条 dns.Record，它们共享
//	    同一个记录集 ID。于是用户看到的仍然是"三条 A 记录"，与其它
//	    服务商一致，界面不需要为华为云写特例。
//
//	写：提交的 dns.Record 被翻译成"这个记录集的值就是这一个"。
//	    也就是说 —— **改"一条"记录 = 把整个记录集改成这一个值；
//	    删"一条"记录 = 删掉整个记录集。**
//
// 这样做的风险（调用方必须知道，不能只躺在注释里）：
//
//  1. 展开出的多条 dns.Record 的 ID 完全相同。调用方**不能**把 ID 当
//     唯一键 —— 它比"一条记录"更粗。
//  2. 对一个多值记录集里的"一条"执行 UpdateRecord，其余的值会被覆盖掉；
//     执行 DeleteRecord，整个记录集连同其余值一起消失。这是会丢数据的
//     操作，界面上必须给出对应的提示（"这一项对应华为云的一个记录集，
//     会一并影响同名的其它记录值"）。
//  3. 反过来也做不到"给同一个名字再加一个值"：同名同类型的记录集已存在时
//     华为云会拒绝 CreateRecord。要写多值记录集只能绕过这一层，
//     直接用华为云的 API 或控制台。
//
// 为什么仍然这么选：接口的形状（Record 只有一个 Content 字符串、一个 ID）
// 表达不了记录集。硬要"安全"就只能把所有写操作都拒绝掉，那等于没实现这家。
// 摊开成多条记录至少让**读**这一侧与别家一致，风险集中在写上；
// 而写这一侧的风险是"用户以为在删一条、实际删了一组"，
// 这一点在 UpdateRecord / DeleteRecord 的注释里各再强调一次。
//
// ---------------------------------------------------------------------------

// Huaweicloud 实现华为云 DNS 的完整记录管理。
//
// 没有实现 dns.Verifier：华为云没有"只校验 AK/SK"的只读端点，
// 能做的只有真去列一次域名 —— 那是本文件 ListZones 的事，
// 不需要在这里再造一个能力入口。
type Huaweicloud struct {
	dynamicDelegate
	meta    dns.Meta
	baseURL string
}

// 编译期断言：接口实现必须完整。
var (
	_ dns.Provider       = (*Huaweicloud)(nil)
	_ dns.DynamicUpdater = (*Huaweicloud)(nil)
	_ dns.ZoneLister     = (*Huaweicloud)(nil)
	_ dns.RecordLister   = (*Huaweicloud)(nil)
	_ dns.RecordCreator  = (*Huaweicloud)(nil)
	_ dns.RecordUpdater  = (*Huaweicloud)(nil)
	_ dns.RecordDeleter  = (*Huaweicloud)(nil)
)

// NewHuaweicloud 构造华为云实现。
//
// baseURL 为空时使用官方地址；测试传入本地假服务器地址。
// 与 NewCloudflare 保持同一形状：地址是参数而不是包级变量，
// 测试因此可以并行，也不会互相污染。
func NewHuaweicloud(baseURL string) *Huaweicloud {
	if baseURL == "" {
		baseURL = huaweicloudDefaultBase
	}
	return &Huaweicloud{
		meta: dns.Meta{
			Name: "huaweicloud",
			// 与 internal/provider/builtin.go 里登记的显示名一致：
			// 凭据字段是按 Name 找的，名字对不上就会取不到凭据。
			DisplayName: "华为云 DNS",
			Tier:        1,
		},
		baseURL: baseURL,
	}
}

// Meta 实现 dns.Provider。
func (h *Huaweicloud) Meta() dns.Meta { return h.meta }

// clientFor 为一次调用构造 HTTP 客户端与签名器。
//
// 与 Cloudflare 的实现有一处关键差别：华为云的凭据**不能**塞进
// client.headers。SDK-HMAC-SHA256 要求用 SK 对"方法 + 规范化 URI +
// 规范化查询串 + 参与签名的请求头 + 请求体哈希"做 HMAC，
// 签名值每个请求都不一样，而 headers 里的值对每个请求都一样。
// 所以凭据以签名器的形式随请求传递，用完即随栈帧消失，
// 不会被存到任何长生命周期对象上，也绝不写进日志或错误信息。
//
// httpInterface 目前所有调用点都传空串 —— dns 的记录管理接口没有
// 携带网卡的字段。保留这个参数是为了与 Cloudflare 同一形状，
// 将来接口层要加网卡绑定时不必改所有调用点。
func (h *Huaweicloud) clientFor(cred dns.Credential, httpInterface string) (*client, ddnsgo.Signer, error) {
	// Field 会去掉首尾空白：用户从控制台复制 AK/SK 经常带上尾随空格，
	// 而服务商把它当成密钥的一部分，只会回一句语义不明的鉴权失败。
	ak := cred.Field("access_key_id")
	sk := cred.Field("access_key_secret")
	if ak == "" || sk == "" {
		// 错误信息里只提字段名，绝不回显凭据内容。
		return nil, ddnsgo.Signer{}, errors.New(
			"tier1: 华为云需要 access_key_id 与 access_key_secret 两个凭据字段")
	}
	return newClient(h.baseURL, httpInterface), ddnsgo.Signer{Key: ak, Secret: sk}, nil
}

// ---------------------------------------------------------------------------
// 请求与签名
// ---------------------------------------------------------------------------

// doSignedJSON 发起一次华为云 API 调用。
//
// 为什么不能复用共用的 doJSON：华为云要求**逐请求签名**，而签名必须发生在
// 请求构造完成之后、发送之前 —— 它覆盖了 method、路径、查询串、
// X-Sdk-Date 等请求头以及请求体的 SHA256。doJSON 在内部构造完请求就立刻
// 发送，没有给调用方留"在中间插手"的位置；把签名塞进 client.headers
// 也不行，Authorization 每个请求都不同。
//
// 因此这里保留 doJSON 的骨架（超时、响应体上限、错误归类、错误说明抽取），
// 只在"构造完请求"与"发送"之间插入签名这一步。共用逻辑一条没有重写：
// 超时用 requestTimeout、响应体上限用 maxResponseBytes、错误类型用 APIError、
// 兜底的错误码/说明抽取用 extractErrorCode / extractErrorMessage。
func (h *Huaweicloud) doSignedJSON(ctx context.Context, cl *client, op, method, path string,
	query url.Values, signer ddnsgo.Signer, body, out any) error {

	urlStr := h.baseURL + path
	if len(query) > 0 {
		urlStr += "?" + query.Encode()
	}

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

	req, err := http.NewRequestWithContext(ctx, method, urlStr, reader)
	if err != nil {
		return fmt.Errorf("%s: 构造请求失败: %w", op, err)
	}
	// 这些头在签名**之前**设置，因此会被算进 SignedHeaders
	//（Accept、host、X-Sdk-Date）。再晚设置的头不会进签名，
	// 服务商那边也不会去校验它们。
	for k, v := range cl.headers {
		req.Header.Set(k, v)
	}

	// Sign 内部会：补上 X-Sdk-Date、把查询串按规范化形式重写、
	// 计算 CanonicalRequest 与签名，最后设置 Authorization。
	if err := signer.Sign(req); err != nil {
		// 不要把 req.Header 带进错误信息：签名头里含有由 SK 派生出来的
		// 信息，凭据不该出现在任何日志或错误里。
		return fmt.Errorf("%s: 计算请求签名失败: %w", op, err)
	}
	// Content-Type 放在签名**之后**设置，因此它不在 SignedHeaders 里。
	//
	// 这是照搬移植实现（internal/ddnsgo/provider_huawei.go）里的顺序：
	// 那套请求形态已经被海量用户验证过，没有必要为了一点整洁去改它。
	// 官方 SDK 允许它参与签名（签名头集合由 SignedHeaders 显式声明，
	// 服务商按声明的那一组去校验），所以两种顺序都对，这里选择不动。
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

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
		// 错误响应也要解析：服务商的错误说明比状态码有用得多
		//（"记录集已存在""系统默认记录集不能删除"）。
		code, msg := huaweicloudError(raw)
		return &APIError{
			Status:  resp.StatusCode,
			Code:    code,
			Message: msg,
			Op:      op,
		}
	}

	// 2xx 配空响应体是合法的（DELETE、以及某些只回状态码的写操作），
	// 不能当成"解析失败" —— 那会让用户以为没写进去，然后去重试。
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: 解析响应失败: %w", op, err)
	}
	return nil
}

// hwErrorResp 是华为云的错误响应体。
//
// 形状是 {"error_code":"DNS.0302","error_msg":"..."}。这两个字段名都不在
// 共用 extractErrorMessage 的候选名单里（它找的是 message/msg/detail…），
// 所以这里先显式解一次；解不出来再退回共用的广度优先搜索兜底。
type hwErrorResp struct {
	ErrorCode string `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`
}

// huaweicloudError 从错误响应里取出错误码与说明。
//
// 目标只有一个：让用户看到服务商的原话。用户拿那句原话去搜，
// 比看我们转述的"请求失败"有用得多。
func huaweicloudError(raw []byte) (code, msg string) {
	var e hwErrorResp
	if err := json.Unmarshal(raw, &e); err == nil {
		code, msg = e.ErrorCode, e.ErrorMsg
	}
	if msg == "" {
		// 兜底：华为云个别接口回的是 {error:{code,message}} 这种嵌套结构，
		// 共用的搜索能把它挖出来；连 JSON 都不是时会截断原文。
		msg = extractErrorMessage(raw)
	}
	if code == "" {
		code = extractErrorCode(raw)
	}
	return code, msg
}

// ---------------------------------------------------------------------------
// 响应结构
// ---------------------------------------------------------------------------

// hwMetadata 是分页元数据。
type hwMetadata struct {
	// TotalCount 是满足查询条件的**资源总数**，不受 limit/offset 影响。
	TotalCount int `json:"total_count"`
}

// hwLinks 是指向当前页 / 下一页的链接。
//
// 只定义、不使用：链接里带的是 marker（下一页的起始资源 ID），而 limit/offset
// 这套翻页方式更直观、也更容易在测试里断言。留着字段是为了让响应结构
// 与文档一致，将来要改用 marker 翻页时不必重新查文档。
type hwLinks struct {
	Self string `json:"self"`
	Next string `json:"next"`
}

// hwZone 是 /v2/zones 返回的一个域名。
type hwZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Status 只用于诊断，不参与过滤 —— 理由见 ListZones。
	Status string `json:"status"`
}

type hwZonesResp struct {
	Zones    []hwZone   `json:"zones"`
	Metadata hwMetadata `json:"metadata"`
	Links    hwLinks    `json:"links"`
}

// hwRecordset 是记录集 —— 华为云记录管理的最小单位。
type hwRecordset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	TTL         int    `json:"ttl"`
	// Records 是这个记录集的全部值：**一个元素才是一条 DNS 记录**。
	Records []string `json:"records"`
	Status  string   `json:"status"`
	ZoneID  string   `json:"zone_id"`
	// ZoneName 同样带结尾的点（"example.com."）。
	ZoneName string `json:"zone_name"`
	// Default 为 true 表示系统默认生成的记录集（SOA / NS），不允许删除。
	Default bool `json:"default"`
	Weight  int  `json:"weight"`
}

type hwRecordsetsResp struct {
	Recordsets []hwRecordset `json:"recordsets"`
	Metadata   hwMetadata    `json:"metadata"`
	Links      hwLinks       `json:"links"`
}

// ---------------------------------------------------------------------------
// 区域
// ---------------------------------------------------------------------------

// ListZones 实现 dns.ZoneLister。
//
// 走 /v2/zones（查询公网域名列表）—— 与移植实现用的是同一个端点，
// 记录集那一族接口则在 v2.1 下（两个版本并存，不是笔误）。
func (h *Huaweicloud) ListZones(ctx context.Context, cred dns.Credential) ([]dns.Zone, error) {
	cl, signer, err := h.clientFor(cred, "")
	if err != nil {
		return nil, err
	}

	var out []dns.Zone
	offset := 0
	for page := 0; page < maxPages; page++ {
		query := url.Values{}
		query.Set("limit", strconv.Itoa(huaweicloudPerPage))
		query.Set("offset", strconv.Itoa(offset))

		var resp hwZonesResp
		err := h.doSignedJSON(ctx, cl, "列出区域", http.MethodGet,
			"/v2/zones", query, signer, nil, &resp)
		if err != nil {
			return nil, err
		}

		for _, z := range resp.Zones {
			// 华为云的域名带结尾的点（"example.com."），对外必须去掉：
			// 这个值会被拿去和用户输入、以及记录名做比较或拼接，
			// 多一个点会让所有比较都失败，而且失败得很安静。
			name := huaweicloudRecordName(z.Name)
			if z.ID == "" || name == "" {
				// 没有 ID 的域名后续任何调用都用不了，
				// 列出来只会让用户选到一个必然失败的选项。
				continue
			}
			out = append(out, dns.Zone{ID: z.ID, Name: name})
		}

		// 不过滤域名状态（ACTIVE / PENDING_*/FREEZE / DISABLE / ERROR…）。
		//
		// 与 Cloudflare 那里的取舍相反，理由是风险不对称：华为云的状态取值
		// 有十来个，猜错一个词就会把用户自己的域名从列表里**藏起来**，
		// 那是用户完全无法自救的故障；而多列出一个冻结中的域名，
		// 最多是改的时候被服务商拒绝，错误信息里会带着原因。
		// 宁可多列，不可漏列。

		next, more := hwNextOffset(offset, len(resp.Zones), resp.Metadata.TotalCount)
		if !more {
			break
		}
		offset = next
	}
	return out, nil
}

// hwNextOffset 判断还要不要继续翻页，并给出下一页的 offset。
//
// offset 是**本次请求**的偏移，got 是这一页返回的**原始**条数
// （不是本地过滤后的条数 —— total_count 数的是服务商那边的资源），
// total 是响应里的 metadata.total_count，可能为 0（有的接口不返回它）。
//
// 三个终止条件：
//   - 空页：没有更多数据了；
//   - 已取够 total_count：正常结束。华为云两个列表接口都会返回它，
//     因此这是实践中真正生效的那一条；
//   - 返回条数超过 limit：服务商没按 limit 分页、一次把数据全给了，
//     再往后翻只会拿到重复内容。
//
// 特意**没有**加"不满一页就算最后一页"这条常见的优化：它省下的只是
// total_count 缺失时的一次请求，却会在"服务商把 limit 截成更小的值"时
// **静默漏掉后面的数据** —— 界面上少了记录，用户根本无从察觉。
// 宁可多打一次请求。
func hwNextOffset(offset, got, total int) (int, bool) {
	if got == 0 {
		return 0, false
	}
	if total > 0 && offset+got >= total {
		return 0, false
	}
	if got > huaweicloudPerPage {
		return 0, false
	}
	return offset + got, true
}

// ---------------------------------------------------------------------------
// 记录
// ---------------------------------------------------------------------------

// ListRecords 实现 dns.RecordLister。
func (h *Huaweicloud) ListRecords(ctx context.Context, cred dns.Credential,
	zone dns.Zone, filter dns.RecordFilter) ([]dns.Record, error) {

	if strings.TrimSpace(zone.ID) == "" {
		return nil, errors.New("tier1: 列出记录需要区域 ID")
	}
	cl, signer, err := h.clientFor(cred, "")
	if err != nil {
		return nil, err
	}

	// 过滤用的名字去掉结尾的点：调用方给的是 www.example.com 这种写法，
	// 而服务商那边存的是 www.example.com.。
	want := huaweicloudRecordName(filter.Name)

	var out []dns.Record
	offset := 0
	for page := 0; page < maxPages; page++ {
		query := url.Values{}
		query.Set("limit", strconv.Itoa(huaweicloudPerPage))
		query.Set("offset", strconv.Itoa(offset))
		if filter.Type != "" {
			query.Set("type", string(filter.Type))
		}
		if want != "" {
			// 华为云的 name 参数是**模糊**匹配，文档原话是
			// "待查询的记录集的域名中包含此 name"。所以这里只是让服务商
			// 少回一些数据，精确匹配必须由下面的本地过滤完成。
			//
			// 没有用 search_mode=equal（文档里的精确搜索开关）：
			// 精确模式是否要求带上结尾的点、是否区分大小写都没有实证，
			// 猜错的结果是**静默地返回空列表** —— 用户会以为记录没了。
			// 而模糊模式返回的是超集，本地再过滤一次是确定安全的。
			query.Set("name", want)
		}

		var resp hwRecordsetsResp
		err := h.doSignedJSON(ctx, cl, "列出记录", http.MethodGet,
			fmt.Sprintf("/v2.1/zones/%s/recordsets", url.PathEscape(zone.ID)),
			query, signer, nil, &resp)
		if err != nil {
			return nil, err
		}

		for _, rs := range resp.Recordsets {
			// 精确匹配在这里做，理由见上面：服务商给的是"包含"匹配，
			// 查 www.example.com 会连 www.example.com.cn 一起返回。
			// 比较忽略大小写 —— DNS 名字本来就不区分大小写，
			// 而用户输入里出现大写是很常见的。
			if want != "" && !strings.EqualFold(huaweicloudRecordName(rs.Name), want) {
				continue
			}
			out = append(out, huaweicloudExpand(rs)...)
		}

		next, more := hwNextOffset(offset, len(resp.Recordsets), resp.Metadata.TotalCount)
		if !more {
			break
		}
		offset = next
	}
	return out, nil
}

// huaweicloudExpand 把一个记录集展开成若干条 dns.Record。
//
// 这是本文件的核心取舍，理由见文件头的长注释：华为云能给的最小单位是
// 记录集，而 dns.Record 描述的是"一条记录"，只能把 records 数组摊开。
// 摊开出来的每条记录**共享同一个记录集 ID**。
func huaweicloudExpand(rs hwRecordset) []dns.Record {
	// 记录集的值数组理论上不会为空；真为空时也要让它在列表里看得见，
	// 否则用户既看不到它、也没有任何办法删掉它。
	if len(rs.Records) == 0 {
		rs.Records = []string{""}
	}
	out := make([]dns.Record, 0, len(rs.Records))
	for _, v := range rs.Records {
		out = append(out, huaweicloudRecord(rs, v))
	}
	return out
}

// huaweicloudRecord 把"记录集 + 其中一个值"翻译成一条 dns.Record。
func huaweicloudRecord(rs hwRecordset, value string) dns.Record {
	return dns.Record{
		// ID 是**记录集**的 ID：多条记录可能共用它，见文件头。
		ID:   rs.ID,
		Name: huaweicloudRecordName(rs.Name),
		Type: dns.RecordType(rs.Type),
		// 值原样保留，不做解析。MX 的优先级、SRV 的权重与端口、
		// CAA 的 flag 都编在值字符串里（例如 "10 mail.example.com."），
		// 按类型拆开再拼回去需要一张语法表，写错一个字就会把用户的值
		// 改坏；原样往返则绝对不会出错。
		//
		// 代价是 Priority 字段恒为 0：界面上编辑 MX 记录时，
		// 优先级要连着值一起写（"10 mail.example.com."）。
		Content: value,
		TTL:     rs.TTL,
		// Comment 来自记录集的 description —— 它是**记录集级**的，
		// 所以同一个记录集展开出的每条记录看到的备注都一样。
		//
		// Proxied 恒为 false：华为云没有 CDN 代理开关这个能力。
		Comment: rs.Description,
	}
}

// huaweicloudRecordName 把服务商返回的名字（或用户输入的名字）归一成对外表达。
//
// 华为云的记录名与域名都以点结尾（FQDN），对外统一不带点。
func huaweicloudRecordName(name string) string {
	return strings.TrimSuffix(strings.TrimSpace(name), ".")
}

// huaweicloudFQDN 把对外的记录名翻译成华为云要求的 FQDN。
//
// 华为云要求记录名以点结尾，少一个点会被直接拒绝；
// 顺带把 "@" 补成区域名 —— dns.DomainResult 里根域名就是这个写法，
// 而华为云不认 "@"，直接发过去只会得到一句"域名格式不正确"。
//
// 返回空串表示连区域名都没有，交给调用方报"记录名不能为空"。
func huaweicloudFQDN(name string, zone dns.Zone) string {
	n := huaweicloudRecordName(name)
	if n == "" || n == "@" {
		n = huaweicloudRecordName(zone.Name)
	}
	if n == "" {
		return ""
	}
	return n + "."
}

// CreateRecord 实现 dns.RecordCreator。
//
// 写入前请先读文件头关于记录集的说明。这里要点是：
// 华为云的 POST 建的是**记录集**，本次写入的记录集只包含 rec.Content 一个值。
// 如果同名同类型的记录集已经存在，华为云会拒绝这次创建（一个 (name, type)
// 只允许一个记录集），而"往已有记录集里追加一个值"在本接口下做不到 ——
// 那需要提交整个 values 数组，而 dns.Record 只有一个 Content。
func (h *Huaweicloud) CreateRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, rec dns.Record) (dns.Record, error) {

	if strings.TrimSpace(zone.ID) == "" {
		return dns.Record{}, errors.New("tier1: 新增记录需要区域 ID")
	}
	body, err := huaweicloudBody(rec, zone, true)
	if err != nil {
		return dns.Record{}, err
	}
	cl, signer, err := h.clientFor(cred, "")
	if err != nil {
		return dns.Record{}, err
	}

	var resp hwRecordset
	err = h.doSignedJSON(ctx, cl, "新增记录", http.MethodPost,
		fmt.Sprintf("/v2.1/zones/%s/recordsets", url.PathEscape(zone.ID)),
		nil, signer, body, &resp)
	if err != nil {
		return dns.Record{}, err
	}
	return huaweicloudWritten(resp, rec), nil
}

// UpdateRecord 实现 dns.RecordUpdater。
//
// **改"一条"记录 = 把这个记录集的值改成这一个。** 华为云的 PUT 提交的是
// 整个记录集：records 数组里放几条，结果就是几条。因此当这个记录集里原本有
// 多个值时，其余的值会被这次修改覆盖掉 —— 这是会丢数据的操作，
// 界面必须提醒用户，理由见文件头。
//
// rec.TTL 为 0 时不发 ttl 字段：华为云的文档写明"默认为空，表示维持原值"，
// 正好对上接口里"0 表示交给服务商"的约定。反过来说，不能在这里把 TTL
// 重置成默认的 300 —— 那会悄悄改掉用户设过的值。
func (h *Huaweicloud) UpdateRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, rec dns.Record) (dns.Record, error) {

	if strings.TrimSpace(zone.ID) == "" {
		return dns.Record{}, errors.New("tier1: 修改记录需要区域 ID")
	}
	if strings.TrimSpace(rec.ID) == "" {
		// 这个 ID 是记录集 ID。展开出来的多条记录共用它，
		// 所以拿到哪一条的 ID 都一样。
		return dns.Record{}, errors.New("tier1: 修改记录需要记录 ID（华为云为记录集 ID）")
	}
	body, err := huaweicloudBody(rec, zone, false)
	if err != nil {
		return dns.Record{}, err
	}
	cl, signer, err := h.clientFor(cred, "")
	if err != nil {
		return dns.Record{}, err
	}

	var resp hwRecordset
	err = h.doSignedJSON(ctx, cl, "修改记录", http.MethodPut,
		fmt.Sprintf("/v2.1/zones/%s/recordsets/%s",
			url.PathEscape(zone.ID), url.PathEscape(rec.ID)),
		nil, signer, body, &resp)
	if err != nil {
		return dns.Record{}, err
	}
	return huaweicloudWritten(resp, rec), nil
}

// DeleteRecord 实现 dns.RecordDeleter。
//
// **删"一条"记录 = 删掉整个记录集**：recordID 是记录集 ID，记录集里的
// **所有**值会一起消失。这是本实现里破坏性最强的一处，理由与替代方案的
// 缺失见文件头 —— 接口只给了 ID，没有给"要删的是哪一个值"，
// 因此不可能只删掉数组里的某一个元素。
//
// 另外：系统默认生成的记录集（SOA / NS，default=true）不允许删除。
// 这里不做预检查，直接让服务商拒绝 —— 它给的原话比我们猜的准确，
// 而且省掉一次多余的往返。
func (h *Huaweicloud) DeleteRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, recordID string) error {

	if strings.TrimSpace(zone.ID) == "" {
		return errors.New("tier1: 删除记录需要区域 ID")
	}
	if strings.TrimSpace(recordID) == "" {
		return errors.New("tier1: 删除记录需要记录 ID（华为云为记录集 ID）")
	}
	cl, signer, err := h.clientFor(cred, "")
	if err != nil {
		return err
	}

	return h.doSignedJSON(ctx, cl, "删除记录", http.MethodDelete,
		fmt.Sprintf("/v2.1/zones/%s/recordsets/%s",
			url.PathEscape(zone.ID), url.PathEscape(recordID)),
		nil, signer, nil, nil)
}

// ---------------------------------------------------------------------------
// 请求体
// ---------------------------------------------------------------------------

// hwRecordsetBody 是新增 / 修改记录集的请求体。
//
// TTL 用指针表达"这个字段发不发"：华为云的 PUT 是部分更新，字段缺失表示
// "维持原值"（文档原话）。用 int + omitempty 是错的 —— TTL=0 会被当成
// "不发"，看起来行为一样，但一旦有人给 TTL 填了负数就会静默丢掉；
// 指针把"没打算改"和"要改成某个值"表达得毫不含糊。
type hwRecordsetBody struct {
	Name string `json:"name"`
	// Description 为空时不发：同样是"维持原值"，因此华为云无法把备注清空，
	// 这是它的语义，omitempty 正好对上。
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type"`
	TTL         *int     `json:"ttl,omitempty"`
	Records     []string `json:"records"`
	// Weight 只在新增时发送：0 在华为云表示"备用"记录，不显式指定的话，
	// 万一服务商默认成 0，用户会得到一个不参与解析的记录集 ——
	// 这种错误极难被发现。移植过来的 ddns-go 实现也是显式写 1 的。
	Weight *int `json:"weight,omitempty"`
}

// huaweicloudBody 构造写请求体。
//
// create 决定两处差异：新增必须给出一个具体 TTL（没有"原值"可维持），
// 且要带上 weight。
func huaweicloudBody(rec dns.Record, zone dns.Zone, create bool) (*hwRecordsetBody, error) {
	if !supportedRecordType(rec.Type) {
		return nil, errors.New("tier1: 记录类型不能为空")
	}
	fqdn := huaweicloudFQDN(rec.Name, zone)
	if fqdn == "" {
		return nil, errors.New("tier1: 记录名不能为空")
	}
	if strings.TrimSpace(rec.Content) == "" {
		return nil, errors.New("tier1: 记录值不能为空")
	}

	body := &hwRecordsetBody{
		Name:        fqdn,
		Description: strings.TrimSpace(rec.Comment),
		Type:        string(rec.Type),
		// 只放一个值。要把这个记录集写成多值，接口层得先能表达多值才行。
		Records: []string{rec.Content},
	}

	switch ttl := normalizeTTL(rec.TTL); {
	case ttl > 0:
		body.TTL = &ttl
	case create:
		ttl := huaweicloudDefaultTTL
		body.TTL = &ttl
	default:
		// 修改且调用方没给 TTL：不发这个字段，让服务商维持原值。
	}

	if create {
		weight := 1 // 1 = 主用记录
		body.Weight = &weight
	}
	return body, nil
}

// huaweicloudWritten 把写操作的响应翻译成一条 dns.Record。
func huaweicloudWritten(resp hwRecordset, sent dns.Record) dns.Record {
	if resp.ID == "" && len(resp.Records) == 0 {
		// 服务商理论上会回一个完整的记录集对象（修改是 202 + 记录集），
		// 万一某次只回了状态码与空体：写入其实已经生效，把它报成失败
		// 会让用户重试，反而制造麻烦。此时把入参带回去 ——
		// 更新时它带着原来的 ID，新增时 ID 为空（调用方据此知道要重新列一次）。
		return sent
	}
	records := huaweicloudExpand(resp)
	for _, r := range records {
		// 优先按请求里的值匹配：一个记录集可能有多个值，
		// 返回"我们刚写进去的那一条"比返回第一条更有用。
		if r.Content == sent.Content {
			return r
		}
	}
	// 值对不上（服务商做了归一化，例如给 CNAME 补了结尾的点）：
	// 返回服务商实际存下来的样子，而不是我们自己的猜测。
	return records[0]
}
