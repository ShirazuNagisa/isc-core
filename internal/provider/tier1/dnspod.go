package tier1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// dnspodDefaultBase 是 DNSPod 传统 API（dnsapi.cn）的默认基址。
//
// 注意它与腾讯云的 dnspod.tencentcloudapi.com（API 3.0）**不是同一套东西**：
// 传统 API 只认 DNSPod Token（ID + Token），请求体是表单，结果写在响应信封的
// status.code 里。internal/ddnsgo 移植过来的那份实现用的也是这一套，
// 因此这里刻意沿用同一套地址与参数名 —— 用户在两边看到的行为才会一致。
const dnspodDefaultBase = "https://dnsapi.cn"

// dnspodUserAgent 是发给 DNSPod 的 User-Agent。
//
// DNSPod 的接口规范里有一条别家没有的硬性要求：
//
//	请求的时候必须设置 UserAgent，如果不设置或者设置为不合法的
//	（比如设置为浏览器的）也会导致帐号被封禁 API。
//	UserAgent 的格式必须为：程序英文名称/版本(联系邮箱)。
//
// Go 的默认 UA 是 "Go-http-client/1.1"，不是浏览器，但也不符合它要求的格式；
// 被服务商判定为滥用会让**整个账号的 API** 被封（文档原文），代价太大。
// 这里的版本号是协议标识，不是构建版本 —— 跟着发版号漂移没有意义。
const dnspodUserAgent = "ISC-Core/1.0 (+https://github.com/ShirazuNagisa/isc-core)"

// dnspodSuccessCode 是 DNSPod 的成功码。
//
// # 这是整套 API 里最容易写错的一处
//
// status.code 是**字符串**：成功是 "1"，不是数字 1。
// 用数字比较（`code != 1`）在 Go 里根本编译不过，但更阴的写法是把它转成
// 数值再比：DNSPod 的失败码是 "-1"、"-2"、"6"、"8" 这类**字符串**，
// 一旦走了数值比较，失败就永远不等于 1 —— 于是每一次失败都被当成成功，
// 用户的记录没改成功，界面却显示"已保存"。
//
// 因此本文件一律做字符串比较（见 dnspodStatus.ok，那里对"数字 1"也做了
// 兼容，理由写在那段注释里）。
const dnspodSuccessCode = "1"

// DNSPod 的"没有数据"业务码。
//
// 这两个码的语义是"查询成功，只是没有数据"，不是故障：
//
//	9   没有任何域名（Domain.List）
//	10  没有记录（Record.List）
//
// 正常路径上我们不会碰到它们 —— 公共参数里带了 error_on_empty=no
// （见 dnspodParams，官方文档也建议这么传）。这里仍然容忍它们，
// 是为了不把"账号下还没加域名"变成一个红色的报错：那不是故障，是事实。
const (
	dnspodCodeNoDomain = "9"
	dnspodCodeNoRecord = "10"
)

// dnspodZoneEnabled 是域名的正常状态。
//
// Domain.List 的 status 还可能是 pause（暂停解析）/ spam（封禁）/ lock（锁定）。
const dnspodZoneEnabled = "enable"

// dnspodPageSize 是分页拉取时每页的条数。
//
// 旧文档没有给 length 的上限，只说"记录超过 3000 条时会被强制分页，
// 并且只返回前 3000 条"。取 100 是一个保守值：个人账号的域名与记录都在
// 几十条量级，一两页就能拉完；同时避免一次拉回几百 KB 的响应 ——
// DNSPod 对滥用 API 有封禁机制（文档明确列为滥用行为的第一条就是
// "短时间内大量添加、删除、修改、刷新域名或者记录"）。
const dnspodPageSize = 100

// dnspodDefaultLine 是新建记录时使用的线路名。
//
// "默认"是 DNSPod 线路的中文名，不是本地化文案：API 的 record_line 参数
// 收的就是这个中文串（免费套餐也只允许默认线路）。与移植过来的 ddns-go
// 实现保持一致。
const dnspodDefaultLine = "默认"

// dnspodDefaultMXPriority 是 MX 记录缺少优先级时使用的档位。
//
// MX 记录的 mx 参数是必填，缺了它 DNSPod 只会回一句"30 MX 值错误，1-20" ——
// 那不叫信息，叫噪音。10 是 DNSPod 控制台新建 MX 记录的默认值，
// 也是现实中最常见的取值。
const dnspodDefaultMXPriority = 10

// DNSPod 允许的 TTL 范围。
//
// 官方文档给出来的是一段**区间**：`ttl {1-604800}`，并注明"不同等级域名
// 最小值不同"；DNSPod 的套餐说明写得更具体：免费版最低 600 秒、
// 专业版 60 秒、企业版与尊享版 1 秒。
//
// # 为什么不做"档位取整"
//
// 老版控制台的 TTL 是一个下拉框（600 / 1200 / 3600 …），很容易让人以为
// API 也只接受那几个离散值。但两份官方文档（传统 API 与腾讯云 API 3.0）
// 给的都是区间，没有档位表。把用户填的 900 悄悄改成 1800 是**静默改数据**，
// 比交给服务商去拒绝更糟。因此这里只做范围校验，超出范围时在本地就报错，
// 理由是服务商那边只会回一个语义模糊的"32 记录的TTL值超出了限制"。
const (
	dnspodMinTTL = 1
	dnspodMaxTTL = 604800
)

// Dnspod 实现 DNSPod（dnsapi.cn 传统 API）的完整记录管理。
//
// 它**没有**实现 dns.Verifier：本次改动只要求五个记录管理接口。
// 需要说清楚的是，DNSPod 其实有可用的只读探测端点（Info.Version 只带公共参数，
// 凭据不对会返回 "-1 登陆失败"），所以"没有只读端点"不是不做的理由 ——
// 一旦声明 dns.Verifier，dns.Capabilities 与界面上的"测试连接"就会跟着变，
// 那是另一个改动面，留到接入时一起做。
type Dnspod struct {
	dynamicDelegate
	meta    dns.Meta
	baseURL string
}

// 编译期断言：接口实现必须完整。
var (
	_ dns.Provider       = (*Dnspod)(nil)
	_ dns.DynamicUpdater = (*Dnspod)(nil)
	_ dns.ZoneLister     = (*Dnspod)(nil)
	_ dns.RecordLister   = (*Dnspod)(nil)
	_ dns.RecordCreator  = (*Dnspod)(nil)
	_ dns.RecordUpdater  = (*Dnspod)(nil)
	_ dns.RecordDeleter  = (*Dnspod)(nil)
)

// NewDnspod 构造 DNSPod 实现。
//
// baseURL 为空时使用官方地址；测试传入本地假服务器地址。
// 与 Cloudflare 保持同样的形态：地址是参数而不是包级变量，
// 测试因此可以并行，也不会互相污染。
func NewDnspod(baseURL string) *Dnspod {
	if baseURL == "" {
		baseURL = dnspodDefaultBase
	}
	return &Dnspod{
		meta: dns.Meta{
			Name:        "dnspod",
			DisplayName: "DNSPod",
			Tier:        1,
		},
		baseURL: baseURL,
	}
}

// Meta 实现 dns.Provider。
func (d *Dnspod) Meta() dns.Meta { return d.meta }

// clientFor 为一次调用构造客户端。
//
// # 凭据在请求体里，而不是请求头里
//
// 与 Cloudflare / GoDaddy 把密钥放进 HTTP 头不同，DNSPod 的 login_token 是
// **表单的一个字段**（见 dnspodParams）。两者都是明文，但表单这一侧多出一个
// 泄漏面：请求体一旦被写进日志或错误信息，就是凭据泄漏。因此本文件的任何
// 错误信息都只带服务商返回的说明，绝不回显请求体（见 dnspodPostForm）。
func (d *Dnspod) clientFor(cred dns.Credential, httpInterface string) *client {
	cl := newClient(d.baseURL, httpInterface)
	cl.headers["User-Agent"] = dnspodUserAgent
	return cl
}

// ---------------------------------------------------------------------------
// 请求 / 响应结构
// ---------------------------------------------------------------------------

// dnspodEnvelope 是所有 DNSPod 响应的外层信封。
//
// 同一个信封承载了列区域、列记录、增删改的全部结果，只是字段各不相同：
// domains / records / record / domain / info。因此这里用一个联合结构，
// 而不是给每个 Action 各写一个 —— 旧 API 的响应字段名高度重复，
// 分开写只会把同一段 tag 抄五遍。
type dnspodEnvelope struct {
	Status  dnspodStatus   `json:"status"`
	Info    *dnspodInfo    `json:"info"`
	Domain  *dnspodDomain  `json:"domain"`
	Domains []dnspodDomain `json:"domains"`
	Records []dnspodRecord `json:"records"`
	// Record 出现在 Record.Create / Modify / Info 的响应里，
	// 但这三个接口给的**字段集各不相同**（见 dnspodRecord 的说明）。
	Record *dnspodRecord `json:"record"`
}

// dnspodStatus 是信封里的状态块。
type dnspodStatus struct {
	// Code 是成功/失败的判据。官方文档写的是字符串 "1"。
	//
	// 用 dnspodFlexString 而不是 string：同一套 API 在不同接口之间换过字段
	// 类型（Domain.List 的 id 是数字、Record.List 的 id 是字符串；
	// Record.List 的 ttl 是字符串、domain 块里的 ttl 是数字），
	// 所以这里对 "1" 与 1 都接受 —— 两种写法都只当成功，其余一律失败。
	// 反过来做的代价是：要么永远判不出成功（数值比较），
	// 要么整家服务商直接解析失败（只认字符串）。
	Code    dnspodFlexString `json:"code"`
	Message string           `json:"message"`
}

// ok 报告这次调用是否成功。
//
// 唯一的判据是"code 等于字符串 1"：缺失、空串、其它任何值都算失败。
// 失败时**宁可报错也不当成功** —— 把失败当成功会让界面显示"已保存"
// 而记录其实没变，那是最难被用户发现的一类错误。
func (s dnspodStatus) ok() bool { return string(s.Code) == dnspodSuccessCode }

// dnspodInfo 是列表类响应里的条数统计。
//
// record_total 在官方示例里是字符串（"2"），domain_total 是数字（2）——
// 同一个信封里两种类型混着来，这正是需要 dnspodFlexInt 的原因。
type dnspodInfo struct {
	DomainTotal dnspodFlexInt `json:"domain_total"`
	RecordTotal dnspodFlexInt `json:"record_total"`
}

// dnspodDomain 是 Domain.List 里的一条区域。
//
// id 在 Domain.List 的示例里是数字（2238269），在 Domain.Info 里是字符串
// （"2238269"）—— 同一个 API 的不同接口给了不同类型，因此用 dnspodFlexString
// 兜住两种形态。ID 一旦变形（例如走了 float64 变成 2.238269e+06）
// 就再也定位不到那个域名了。
type dnspodDomain struct {
	ID       dnspodFlexString `json:"id"`
	Name     string           `json:"name"`
	Punycode string           `json:"punycode"`
	Status   string           `json:"status"`
}

// dnspodRecord 是一条记录。
//
// # 为什么会有两套字段名
//
// Record.List 返回的是 name / type / line，而 Record.Info 返回的是
// sub_domain / record_type / record_line —— **同一份数据、两套字段名**。
// 这里把两套都放进一个结构，用 subDomain() / recordType() / recordLine()
// 取值，而不是为两个接口各写一个结构、再各写一份翻译代码。
//
// # 为什么数值字段是 dnspodFlexInt 而不是 int
//
// 依据是这套 API 的真实行为：同一类值在不同接口里给了不同类型
// （Domain.List 的域名 id 是数字 2238269，Record.List 的记录 id 是字符串
// "44146112"，而 Record.Create 的响应给字符串、Record.Modify 的给数字；
// 同一个 info 块里 record_total 是字符串而 domain_total 是数字）。
// ttl / mx / enabled 在官方示例里都是字符串，这里同样用宽松类型兜住 ——
// 一旦某个接口换回数字，用 int 的代价不是报错，而是**静默读成 0**：
// TTL 的 0 在我们的模型里表示"交给服务商默认"，等于把 600 秒读成"随便"。
type dnspodRecord struct {
	ID dnspodFlexString `json:"id"`

	// Record.List 的名字。
	Name string `json:"name"`
	Type string `json:"type"`
	Line string `json:"line"`
	// LineID 形如 "10=1"。线路在 ISC 的模型里没有位置，
	// 但修改记录时必须原样带回去（见 UpdateRecord）。
	LineID string `json:"line_id"`

	// Record.Info 的名字。
	SubDomain    string `json:"sub_domain"`
	RecordType   string `json:"record_type"`
	RecordLine   string `json:"record_line"`
	RecordLineID string `json:"record_line_id"`

	Value string        `json:"value"`
	TTL   dnspodFlexInt `json:"ttl"`
	// MX 是优先级；非 MX 记录 DNSPod 也返回它，值为 "0"。
	MX dnspodFlexInt `json:"mx"`
	// Enabled 是 "1"（启用）/ "0"（暂停）。
	Enabled dnspodFlexString `json:"enabled"`
	// Remark 是记录备注，对应 dns.Record.Comment（只读方向，见 CreateRecord）。
	Remark string `json:"remark"`
}

// subDomain 取主机记录（子域名），兼容两个接口的字段名。
func (r dnspodRecord) subDomain() string {
	if s := strings.TrimSpace(r.SubDomain); s != "" {
		return s
	}
	return strings.TrimSpace(r.Name)
}

// recordType 取记录类型，兼容两个接口的字段名。
func (r dnspodRecord) recordType() string {
	if s := strings.TrimSpace(r.RecordType); s != "" {
		return s
	}
	return strings.TrimSpace(r.Type)
}

// recordLine 取线路的中文名，兼容两个接口的字段名。
func (r dnspodRecord) recordLine() string {
	if s := strings.TrimSpace(r.RecordLine); s != "" {
		return s
	}
	return strings.TrimSpace(r.Line)
}

// recordLineID 取线路 ID，兼容两个接口的字段名。
func (r dnspodRecord) recordLineID() string {
	if s := strings.TrimSpace(r.RecordLineID); s != "" {
		return s
	}
	return strings.TrimSpace(r.LineID)
}

// disabled 报告记录是否被显式暂停。
//
// 只有拿到明确的 "0" 才算暂停：字段缺失、解析不出来（Enabled 为空）时
// 按"启用"处理。反过来做的风险是 —— 把一个其实正常工作的记录当成暂停的，
// 然后在修改时把 disable 写回去，用户的解析就这么断了。
func (r dnspodRecord) disabled() bool { return string(r.Enabled) == "0" }

// stateParam 返回记录当前状态对应的 status 参数值。
//
// Record.Create / Modify 的 status 取 "enable" / "disable"，而读接口给的是
// enabled 的 "1" / "0"，中间的翻译就在这里。
func (r dnspodRecord) stateParam() string {
	if r.disabled() {
		return "disable"
	}
	return "enable"
}

// zoneName 取响应里带的区域名。
//
// 优先 punycode：dns.Record.Name 要的是可以直接比较的完整记录名，
// 而 punycode 是它的 ASCII 形式 —— 用中文原名拼出来的名字，
// 跟调用方手上那份（多半来自别的接口或用户输入）不一定相等。
func (e dnspodEnvelope) zoneName() string {
	if e.Domain == nil {
		return ""
	}
	if p := strings.TrimSpace(e.Domain.Punycode); p != "" {
		return p
	}
	return strings.TrimSpace(e.Domain.Name)
}

// ---------------------------------------------------------------------------
// 表单编解码：为什么这家的请求不能走 doJSON
// ---------------------------------------------------------------------------

// dnspodFlexString 接受 JSON 字符串或 JSON 数字（数字按字面量保留）。
//
// 存在的理由见 dnspodDomain / dnspodStatus 的说明：DNSPod 的同一类字段
// 在不同接口之间换了类型。用 float64 中转会把 16894439 变成 1.6894439e+07，
// 而这种 ID 变形之后再拿去调 Record.Modify 只会得到一个"记录ID错误"。
type dnspodFlexString string

// UnmarshalJSON 实现 json.Unmarshaler。
func (s *dnspodFlexString) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		*s = ""
		return nil
	}
	if trimmed[0] == '"' {
		// 走一次标准解析，让转义与多字节字符按 JSON 规则处理。
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		*s = dnspodFlexString(v)
		return nil
	}
	*s = dnspodFlexString(trimmed)
	return nil
}

// dnspodFlexInt 接受 JSON 数字或 JSON 字符串形式的整数。
type dnspodFlexInt int

// UnmarshalJSON 实现 json.Unmarshaler。
func (n *dnspodFlexInt) UnmarshalJSON(raw []byte) error {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		// 解析不出来就按 0 处理，而不是让整次请求失败：
		// 一条记录里某个字段的形态变化，不该让用户看到整个列表加载失败。
		// 0 在 TTL 上的含义是"服务商默认"，是这里最保守的取值。
		*n = 0
		return nil
	}
	*n = dnspodFlexInt(v)
	return nil
}

// Int 返回整数值。
func (n dnspodFlexInt) Int() int { return int(n) }

// dnspodParams 构造每次调用都要带上的公共参数。
//
//	login_token     鉴权参数（见 dnspodLoginToken），由 ID 与 Token 逗号拼接
//	format=json     返回 JSON。**必须显式传**：官方文档写明默认为 xml，
//	                       而本文件的解析全部假定 JSON
//	lang=cn         错误信息用中文。用户可见的错误里会带上服务商的原文，
//	                       默认的英文说明对国内用户没有意义
//	error_on_empty=no  没有数据时返回空列表而不是错误码（文档建议这么传）
//
// 这个 map 里带着凭据，因此它只能出现在请求体里 —— 不允许被打印。
func dnspodParams(loginToken string) url.Values {
	return url.Values{
		"login_token":    {loginToken},
		"format":         {"json"},
		"lang":           {"cn"},
		"error_on_empty": {"no"},
	}
}

// dnspodLoginToken 组装并校验 DNSPod 的 login_token。
//
// DNSPod 的凭据是"API ID + API Token"两个字段（内部键名就是 id 与 token，
// 见 internal/provider/builtin.go 的 fieldDNSPodID / fieldDNSPodToken），
// 鉴权方式是把两者用英文逗号拼成**一个**参数：login_token=<ID>,<Token>。
//
// 这也解释了 internal/ddnsgo 那份移植实现里为什么没有任何签名函数：
// 这套 API 的鉴权就是一个明文参数，没有签名算法可以复用。
//
// 返回值只能被塞进表单，**绝不允许**写进日志、错误信息或审计记录。
func dnspodLoginToken(cred dns.Credential) (string, error) {
	id, token := cred.Field("id"), cred.Field("token")
	if id == "" || token == "" {
		// 错误信息里只说缺了什么，不带任何凭据内容。
		return "", errors.New("tier1: DNSPod 需要 API ID 与 API Token 两个凭据字段（登录令牌由二者拼接而成）")
	}
	return id + "," + token, nil
}

// dnspodPostForm 以表单方式发起一次 DNSPod 调用，并把 JSON 响应解析到 out。
//
// # 为什么不能复用 tier1.go 的 doJSON
//
// doJSON 只会发 JSON：它把 body 序列化成 JSON，并在有 body 时固定设置
// Content-Type: application/json。而 DNSPod 的所有 Action 都只吃
// application/x-www-form-urlencoded —— 拿 JSON 过去，服务端一个参数都取不到，
// 只会回一句"参数不合法"，那是一条极难定位的错误（参数明明都在请求里）。
//
// 因此这里另写一份表单版。除"请求体怎么编码"这一点外，其余约定全部沿用
// tier1.go 那一套，不重复造 HTTP 逻辑：
//
//	requestTimeout       单次调用超时
//	maxResponseBytes     响应体上限（防超大响应吃掉内核内存）
//	APIError             >=400 时的错误类型，带上服务商的说明
//	extractErrorMessage  错误说明的尽力提取（不会原样吐回整个响应体）
//
// # 凭据泄漏面
//
// form 里带 login_token，所以这个函数的**任何**错误信息都只包含 op、
// HTTP 状态码与服务商返回的说明，绝不回显 form 的内容 —— 一旦把请求体
// 写进日志，凭据就跟着进了日志。
func (c *client) dnspodPostForm(ctx context.Context, op, url string, form url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	// url.Values.Encode 负责百分号编码。DNSPod 的 record_line_id 形如 "10=1"，
	// 里面的 '=' 必须被编码成 %3D（官方文档专门提醒过这一点），
	// 自己拼表单很容易漏掉。
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%s: 构造请求失败: %w", op, err)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

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
		// 与 doJSON 一致：错误响应体也要解析，服务商的说明通常比状态码有用
		// 得多。extractErrorMessage 只取 message 字段（取不到时才截断 raw），
		// 所以这里也不会把带凭据的**请求**内容泄漏出去。
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

// dnspodStatusError 把一次业务层面的失败翻译成错误。
//
// 这里刻意用 *APIError 而不是 fmt.Errorf：接口层靠它的 Code 判断语义，
// 而靠错误文本判断是脆弱的。
//
// 但有一点必须说清楚：DNSPod 的业务错误走的是 **HTTP 200**
// （4xx/5xx 已经被 dnspodPostForm 拦掉了），所以 Status 填 200 是如实的，
// IsNotFound / IsUnauthorized 对这套 API 也就没有鉴别力 ——
// 真正的信息在 Code 与 Message 里。宁可在注释里写清楚，
// 也不要为了"看起来和别家统一"把业务码伪装成 401/404。
func dnspodStatusError(op string, st dnspodStatus) error {
	msg := strings.TrimSpace(st.Message)
	if msg == "" {
		// 只带 HTTP 状态码的错误对用户毫无帮助，这一句至少说明
		// "服务商没说原因"，而不是让界面显示一片空白。
		msg = "服务商未提供错误说明"
	}
	return &APIError{
		Status:  http.StatusOK,
		Code:    string(st.Code),
		Message: msg,
		Op:      op,
	}
}

// ---------------------------------------------------------------------------
// 区域
// ---------------------------------------------------------------------------

// ListZones 实现 dns.ZoneLister。
//
// 两点取舍：
//
//  1. 分页拉全量（与 Cloudflare / GoDaddy 实现一致）：用户可能管理着几十个
//     域名，而"列表里少了一半"是一个很难被察觉、又很让人恼火的问题。
//  2. 只保留 status 为 enable 的域名。pause（暂停解析）/ spam（封禁）/
//     lock（锁定）状态下改记录不会生效，列出来只会让用户以为"我改了但没用"
//     —— 与 Cloudflare 过滤掉非 active 区域是同一个理由。
func (d *Dnspod) ListZones(ctx context.Context, cred dns.Credential) ([]dns.Zone, error) {
	loginToken, err := dnspodLoginToken(cred)
	if err != nil {
		return nil, err
	}

	cl := d.clientFor(cred, "")

	var out []dns.Zone
	for page := 0; page < maxPages; page++ {
		form := dnspodParams(loginToken)
		form.Set("offset", strconv.Itoa(page*dnspodPageSize))
		form.Set("length", strconv.Itoa(dnspodPageSize))

		var env dnspodEnvelope
		if err := cl.dnspodPostForm(ctx, "列出区域", cl.URL("/Domain.List"), form, &env); err != nil {
			return nil, err
		}
		// "没有任何域名"不是错误，见 dnspodCodeNoDomain。
		if !env.Status.ok() && string(env.Status.Code) != dnspodCodeNoDomain {
			return nil, dnspodStatusError("列出区域", env.Status)
		}

		for _, z := range env.Domains {
			name := strings.TrimSpace(z.Name)
			if name == "" {
				continue
			}
			if s := strings.TrimSpace(z.Status); s != "" && s != dnspodZoneEnabled {
				continue
			}
			// ID 优先：名字可能是中文、可能是别名，而 domain_id 是稳定的，
			// 后续的记录增删改都拿它定位域名。
			out = append(out, dns.Zone{ID: string(z.ID), Name: name})
		}

		// 一页没取满就说明到底了。不能靠 info.domain_total 判断：
		// 它在官方示例里是数字、在别的接口里是字符串，形态并不统一。
		if len(env.Domains) < dnspodPageSize {
			break
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 记录
// ---------------------------------------------------------------------------

// ListRecords 实现 dns.RecordLister。
//
// # 过滤
//
// Record.List 只有 sub_domain 与 keyword 两个过滤参数，**没有按类型过滤**，
// 因此类型过滤只能在本地做（与 GoDaddy 实现同样的取舍）。
// 子域名过滤仍然交给服务商：它能把"这个域名下有几千条记录"的分页代价降下来，
// 而完整记录名到主机记录的翻译由 dnspodSubDomain 负责。
//
// 本地仍然做一次精确匹配：服务商的 sub_domain 过滤是它自己的规则，
// 而 dns.RecordFilter 的契约是"精确匹配完整记录名"，这一层不能少。
func (d *Dnspod) ListRecords(ctx context.Context, cred dns.Credential,
	zone dns.Zone, filter dns.RecordFilter) ([]dns.Record, error) {

	loginToken, err := dnspodLoginToken(cred)
	if err != nil {
		return nil, err
	}

	cl := d.clientFor(cred, "")

	// 名字过滤：能翻译成主机记录就交给服务商，翻不出来（名字不在这个区域下、
	// 或者调用方没给区域名）就只做本地匹配 —— 把一个对不上的名字当
	// sub_domain 发过去，DNSPod 会用"22 子域名不合法"整单拒绝，
	// 而一个过滤条件不该让整次查询失败。
	subDomain := ""
	if n := strings.TrimSpace(filter.Name); n != "" {
		subDomain = dnspodSubDomain(n, zone.Name)
	}

	var out []dns.Record
	for page := 0; page < maxPages; page++ {
		form := dnspodParams(loginToken)
		if err := dnspodSetZone(form, zone, "列出记录"); err != nil {
			return nil, err
		}
		form.Set("offset", strconv.Itoa(page*dnspodPageSize))
		form.Set("length", strconv.Itoa(dnspodPageSize))
		if subDomain != "" {
			form.Set("sub_domain", subDomain)
		}

		var env dnspodEnvelope
		if err := cl.dnspodPostForm(ctx, "列出记录", cl.URL("/Record.List"), form, &env); err != nil {
			return nil, err
		}
		// "没有记录"不是错误，见 dnspodCodeNoRecord。
		if !env.Status.ok() && string(env.Status.Code) != dnspodCodeNoRecord {
			return nil, dnspodStatusError("列出记录", env.Status)
		}

		// 区域名以调用方给的为准，缺了才用响应里的：ListZones 出来的区域
		// 一定带名字，而只拿到 ID 的调用方（例如从本地库里读出来的）也能
		// 在这里把完整记录名拼出来。
		zoneName := strings.TrimSpace(zone.Name)
		if zoneName == "" {
			zoneName = env.zoneName()
		}

		for _, r := range env.Records {
			rec := dnspodToRecord(zoneName, r)
			if !dnspodMatchFilter(rec, filter) {
				continue
			}
			out = append(out, rec)
		}

		if len(env.Records) < dnspodPageSize {
			break
		}
	}
	return out, nil
}

// CreateRecord 实现 dns.RecordCreator。
//
// # 一处必须写明的接口妥协
//
// Record.Create 的响应**只返回记录 ID / 名字 / 状态**（官方示例只有
// record.id / record.name / record.status），拿不到完整记录。因此这里如实
// 回显请求里提交的字段并补上服务商给的 ID，而不是假装从响应里解析出了一条
// 记录 —— 那样只会得到一条满是零值的记录，比不回显更糟。
//
// # 备注
//
// DNSPod 的记录备注（remark）**不在** Record.Create / Record.Modify 的参数里，
// 它有一个专门的 Record.Remark 接口。这里选择不额外发第二次请求：
// 一次"新增记录"变成两个可能各成功一半的调用，失败时的语义很难向用户解释。
// 因此备注是只读的（列表里能看到），写入方向留到有明确需求时再说。
func (d *Dnspod) CreateRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, rec dns.Record) (dns.Record, error) {

	if !supportedRecordType(rec.Type) {
		return dns.Record{}, errors.New("tier1: 记录类型不能为空")
	}
	if strings.TrimSpace(rec.Name) == "" {
		return dns.Record{}, errors.New("tier1: 记录名不能为空")
	}

	loginToken, err := dnspodLoginToken(cred)
	if err != nil {
		return dns.Record{}, err
	}
	host, err := dnspodHostRecord(rec.Name, zone.Name)
	if err != nil {
		return dns.Record{}, err
	}
	ttl, err := dnspodTTL(rec.TTL)
	if err != nil {
		return dns.Record{}, err
	}

	cl := d.clientFor(cred, "")

	form := dnspodParams(loginToken)
	if err := dnspodSetZone(form, zone, "新增记录"); err != nil {
		return dns.Record{}, err
	}
	// sub_domain 必须显式发送：DNSPod 把"没有这个参数"解释成 "@"，
	// 于是"主机记录没算出来"会静默变成"记录加到根域名上"。
	form.Set("sub_domain", host)
	form.Set("record_type", string(rec.Type))
	form.Set("value", rec.Content)
	// 线路用默认线路。dns.Record 里没有线路这个概念，而"不传 record_line"
	// 在不同套餐下的行为并不一致；显式写"默认"，与移植过来的 ddns-go
	// 实现（以及它的海量线上验证）保持一致。
	form.Set("record_line", dnspodDefaultLine)
	// 新记录默认启用。dns.Record 里没有"启用/暂停"的位置，
	// 所以这里只能选一个明确的值并写出来，而不是留给服务商的默认值去决定。
	form.Set("status", "enable")
	if ttl > 0 {
		// TTL 为 0 时不传：官方文档里 ttl 是可选的，
		// 不传即用服务商/套餐的默认值，这正是"交给服务商默认"的语义。
		form.Set("ttl", strconv.Itoa(ttl))
	}
	if dnspodUsesPriority(rec.Type) {
		form.Set("mx", strconv.Itoa(dnspodPriority(rec.Type, rec.Priority)))
	}

	var env dnspodEnvelope
	if err := cl.dnspodPostForm(ctx, "新增记录", cl.URL("/Record.Create"), form, &env); err != nil {
		return dns.Record{}, err
	}
	if !env.Status.ok() {
		return dns.Record{}, dnspodStatusError("新增记录", env.Status)
	}
	if env.Record == nil {
		return dns.Record{}, errors.New("tier1: DNSPod 的记录新增响应结构不符合预期（缺少 record 字段）")
	}

	out := rec
	out.ID = string(env.Record.ID)
	if out.ID == "" {
		return dns.Record{}, errors.New("tier1: DNSPod 未返回新记录的 ID，无法定位刚创建的记录")
	}
	// TTL 按"实际提交的值"回显：调用方传 0 时我们没提交 ttl，
	// 服务商实际用了多少我们并不知道，如实返回 0（= 服务商默认）。
	out.TTL = ttl
	return out, nil
}

// UpdateRecord 实现 dns.RecordUpdater。
//
// # DNSPod 的"修改"与别家不同，因此这里必须先读后写
//
// Record.Modify 有三处与别家不一样的语义，全部来自官方文档：
//
//  1. sub_domain **不传就默认为 "@"** —— 也就是说，不带这个参数去改一条
//     www 的记录，会把它搬到根域名上。所以它必须每次都显式发送。
//  2. record_type 与 value 是必选；ttl / status 可选，而文档**没有**说明
//     可选参数不传时是保留原值还是被重置。
//  3. record_line 是**必选**（"记录线路，通过API记录线路获得，中文，比如：默认"），
//     record_line_id 与它二选一。但 dns.Record 里根本没有"线路"这个概念 ——
//     线路是 DNSPod 特有的（默认 / 电信 / 联通 / 境外…），同一条主机记录
//     在每条线路上是一条**独立**的记录。
//
// 第 3 条决定了这里必须先调一次 Record.Info 把当前记录读回来：
//
//   - 只有读回来才知道这条记录在哪条线路上，才能把 record_line_id 原样带回。
//     不读就只能写死"默认"，那会把用户的联通/电信线路记录悄悄搬到默认线路 ——
//     一个用户极难察觉、后果却很严重的破坏；
//   - 顺带拿到 ttl / mx / enabled 的当前值。既然文档没说清"不传"的语义，
//     我们就把当前值**显式回填**，不去赌未文档化的行为 ——
//     宁可多发两个字节，也不要静默改数据。
//
// # 还有一条硬约束
//
// 官方文档同时写着两件事：一小时内提交超过 5 次"没有任何变动"的修改请求，
// 该记录会被系统锁定一小时；接口规范里更把"记录内容没有任何改变的刷新"
// 直接列为滥用行为。既然已经读了当前记录，就顺手比对一次：没有任何字段
// 需要改时**直接返回，不发这个请求**。"用户点了保存但什么都没改"
// 是最常见的操作，不拦下来就可能把记录锁死。
func (d *Dnspod) UpdateRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, rec dns.Record) (dns.Record, error) {

	if strings.TrimSpace(rec.ID) == "" {
		return dns.Record{}, errors.New("tier1: 更新记录需要记录 ID")
	}
	if !supportedRecordType(rec.Type) {
		return dns.Record{}, errors.New("tier1: 记录类型不能为空")
	}

	loginToken, err := dnspodLoginToken(cred)
	if err != nil {
		return dns.Record{}, err
	}
	host, err := dnspodHostRecord(rec.Name, zone.Name)
	if err != nil {
		return dns.Record{}, err
	}
	zoneName := strings.TrimSpace(zone.Name)

	cl := d.clientFor(cred, "")

	cur, err := dnspodRecordInfo(ctx, cl, loginToken, zone, rec.ID)
	if err != nil {
		// 把"为什么还要多读一次"写进错误里：用户看到的是
		// "读取记录失败"，而不是一个看起来多余的动作。
		return dns.Record{}, fmt.Errorf(
			"tier1: 修改记录前需要先读取记录 %s 的当前内容（DNSPod 要求带上记录线路，"+
				"且未提供字段的语义没有文档化）：%w", rec.ID, err)
	}

	form := dnspodParams(loginToken)
	if err := dnspodSetZone(form, zone, "修改记录"); err != nil {
		return dns.Record{}, err
	}
	form.Set("record_id", rec.ID)
	form.Set("sub_domain", host)
	form.Set("record_type", string(rec.Type))
	form.Set("value", rec.Content)

	// 线路原样带回。record_line_id 形如 "10=1"，其中的 '=' 由
	// url.Values.Encode 负责转义成 %3D（官方文档专门提醒过这一点）。
	switch {
	case cur.recordLineID() != "":
		form.Set("record_line_id", cur.recordLineID())
	case cur.recordLine() != "":
		form.Set("record_line", cur.recordLine())
	default:
		// 读回来的记录里也没有线路信息（理论上不会发生）：退回默认线路，
		// 至少不要在请求里留一个空值让服务商去猜。
		form.Set("record_line", dnspodDefaultLine)
	}

	// TTL：调用方说"用服务商默认"（0）时保留当前值，而不是让记录被重置成
	// 某个我们并不知道的默认值。
	ttl := normalizeTTL(rec.TTL)
	if ttl == 0 {
		ttl = cur.TTL.Int()
	}
	if ttl, err = dnspodTTL(ttl); err != nil {
		return dns.Record{}, err
	}
	if ttl > 0 {
		form.Set("ttl", strconv.Itoa(ttl))
	}

	// 优先级同理：MX / SRV 靠 mx 表达优先级，调用方没给就保留当前值。
	if dnspodUsesPriority(rec.Type) {
		p := rec.Priority
		if p <= 0 && rec.Type == dns.TypeMX {
			// MX 的 mx 是必填，缺了会被拒；当前值也没有才用默认档。
			p = cur.MX.Int()
			if p <= 0 {
				p = dnspodDefaultMXPriority
			}
		}
		form.Set("mx", strconv.Itoa(p))
	}

	// 记录状态：一律发送**当前状态**。dns.Record 里没有"启用/暂停"的位置，
	// 所以既不能改它，也绝不能写死 enable —— 那会把用户暂停掉的记录悄悄启用。
	form.Set("status", cur.stateParam())

	// 没有任何变动就不发请求（见函数头上那条"锁定一小时"的约束）。
	if dnspodSameModify(form, *cur) {
		return dnspodToRecord(zoneName, *cur), nil
	}

	var env dnspodEnvelope
	if err := cl.dnspodPostForm(ctx, "修改记录", cl.URL("/Record.Modify"), form, &env); err != nil {
		return dns.Record{}, err
	}
	if !env.Status.ok() {
		return dns.Record{}, dnspodStatusError("修改记录", env.Status)
	}

	// Record.Modify 的响应同样只有 id / name / value / status，
	// 因此按**实际提交的值**回显。
	out := dns.Record{
		ID:      rec.ID,
		Name:    dnspodFullName(zoneName, host),
		Type:    rec.Type,
		Content: rec.Content,
		Comment: rec.Comment,
	}
	out.TTL = dnspodAtoi(form.Get("ttl"))
	out.Priority = dnspodAtoi(form.Get("mx"))
	return out, nil
}

// DeleteRecord 实现 dns.RecordDeleter。
//
// 记录已经不存在时 DNSPod 返回业务码 8（"记录ID错误"），这里如实报错而
// 不是当成成功：接口层需要能区分"删掉了"与"本来就没有"，
// 而把后者吞掉会让用户在别的地方看到对不上的数据。
func (d *Dnspod) DeleteRecord(ctx context.Context, cred dns.Credential,
	zone dns.Zone, recordID string) error {

	if strings.TrimSpace(recordID) == "" {
		return errors.New("tier1: 删除记录需要记录 ID")
	}

	loginToken, err := dnspodLoginToken(cred)
	if err != nil {
		return err
	}

	cl := d.clientFor(cred, "")

	form := dnspodParams(loginToken)
	if err := dnspodSetZone(form, zone, "删除记录"); err != nil {
		return err
	}
	form.Set("record_id", recordID)

	var env dnspodEnvelope
	if err := cl.dnspodPostForm(ctx, "删除记录", cl.URL("/Record.Remove"), form, &env); err != nil {
		return err
	}
	if !env.Status.ok() {
		return dnspodStatusError("删除记录", env.Status)
	}
	return nil
}

// dnspodRecordInfo 读回一条记录的当前内容（Record.Info）。
//
// 为什么需要它，见 UpdateRecord 的说明：DNSPod 的修改接口要求带上"线路"，
// 而线路在 ISC 的模型里没有位置，只能从服务商那里读回来。
func dnspodRecordInfo(ctx context.Context, cl *client,
	loginToken string, zone dns.Zone, recordID string) (*dnspodRecord, error) {

	form := dnspodParams(loginToken)
	if err := dnspodSetZone(form, zone, "读取记录"); err != nil {
		return nil, err
	}
	form.Set("record_id", recordID)

	var env dnspodEnvelope
	if err := cl.dnspodPostForm(ctx, "读取记录", cl.URL("/Record.Info"), form, &env); err != nil {
		return nil, err
	}
	if !env.Status.ok() {
		return nil, dnspodStatusError("读取记录", env.Status)
	}
	if env.Record == nil {
		return nil, errors.New("tier1: DNSPod 的记录信息响应结构不符合预期（缺少 record 字段）")
	}
	return env.Record, nil
}

// ---------------------------------------------------------------------------
// 参数与翻译
// ---------------------------------------------------------------------------

// dnspodSetZone 把区域写进表单。
//
// 优先用 domain_id：域名可能对不上（punycode、中文域名、别名绑定），
// 而 ID 是稳定的。两者都没有时直接报错 —— 让服务商去猜一个空域名，
// 只会得到一个语义模糊的"6 域名ID错误"。
func dnspodSetZone(form url.Values, zone dns.Zone, op string) error {
	if id := strings.TrimSpace(zone.ID); id != "" {
		form.Set("domain_id", id)
		return nil
	}
	if name := strings.TrimSpace(zone.Name); name != "" {
		form.Set("domain", name)
		return nil
	}
	return fmt.Errorf("tier1: %s需要区域 ID 或区域名（DNSPod 的接口以 domain_id 或 domain 定位域名）", op)
}

// dnspodSubDomain 把完整记录名翻译成 DNSPod 的主机记录（子域名）。
//
//	www.example.com + example.com -> www
//	example.com     + example.com -> @      （根域名在 DNSPod 里是 "@"）
//
// 翻译不出来（名字不在这个区域之下，或者区域名未知）时返回空串，
// 含义是"不要把这个条件交给服务商"：那种情况下让本地精确匹配得出
// "没有匹配项"是对的，而把 "www.other.com" 当 sub_domain 发过去，
// DNSPod 会用"22 子域名不合法"把整次查询拒掉。
func dnspodSubDomain(fullName, zoneName string) string {
	fullName = strings.TrimSuffix(strings.TrimSpace(fullName), ".")
	zoneName = strings.TrimSuffix(strings.TrimSpace(zoneName), ".")
	if fullName == "" || zoneName == "" {
		return ""
	}
	if strings.EqualFold(fullName, zoneName) {
		return "@"
	}
	suffix := "." + zoneName
	if len(fullName) > len(suffix) && strings.EqualFold(fullName[len(fullName)-len(suffix):], suffix) {
		return fullName[:len(fullName)-len(suffix)]
	}
	return ""
}

// dnspodHostRecord 是写入场景（新增 / 修改）用的严格版本。
//
// 与 dnspodSubDomain 只差一点，但很关键：翻译不出来时报错，而不是返回空串。
// DNSPod 把"没有 sub_domain 参数"解释成 "@"，于是"区域名未知"会静默变成
// "写到根域名上" —— 那是在用户完全看不到的地方改错记录，
// 比直接报错危险得多。所以这里宁可让调用失败，也不退化成空值。
func dnspodHostRecord(fullName, zoneName string) (string, error) {
	if strings.TrimSpace(zoneName) == "" {
		return "", fmt.Errorf("tier1: 需要区域名才能把记录名 %q 翻译成 DNSPod 的主机记录"+
			"（缺少区域名会被 DNSPod 当成根域名 @，因此不能猜）", fullName)
	}
	host := dnspodSubDomain(fullName, zoneName)
	if host == "" {
		return "", fmt.Errorf("tier1: 记录名 %q 不在区域 %q 之下，无法算出 DNSPod 的主机记录",
			fullName, zoneName)
	}
	return host, nil
}

// dnspodUsesPriority 报告记录类型是否需要 mx 参数。
//
// MX 记录是官方文档明确要求必填 mx 的（范围 1-20，超出会回"30 MX 值错误"）；
// SRV 的优先级在 DNSPod 里同样存在 mx 字段里（腾讯云 API 3.0 的文档把
// MX / HTTPS / SVCB 都列为"必填 MX"）。
//
// 只对这两种类型发送 mx：老文档只说"当记录类型是 MX 时有效"，
// 对其它类型的行为没有说明 —— 与其赌它被忽略，不如不发送。
// 这也是 Cloudflare 实现里"非 MX/SRV 不发 priority"的同一个取舍。
func dnspodUsesPriority(t dns.RecordType) bool {
	switch t {
	case dns.TypeMX, dns.TypeSRV:
		return true
	default:
		return false
	}
}

// dnspodPriority 归一要写进 mx 的优先级。
//
// MX：官方文档给的范围是 1-20，且必填。调用方没给（<=0）时用 10 兜底 ——
// 缺了它服务商只会回一句"30 MX 值错误，1-20"，那不叫信息，叫噪音。
// 超过 20 的值**原样发出去**：本地截断是静默改数据，
// 让 DNSPod 用自己的错误码把范围讲清楚更好。
//
// SRV：优先级 0 是合法且有意义的（最小优先级），因此不兜底、不改写。
func dnspodPriority(t dns.RecordType, p int) int {
	if t == dns.TypeMX && p < 1 {
		return dnspodDefaultMXPriority
	}
	return p
}

// dnspodTTL 校验并归一 TTL，0 表示"交给服务商默认"。
//
// 范围与"为什么不做档位取整"见 dnspodMinTTL / dnspodMaxTTL 的说明。
func dnspodTTL(ttl int) (int, error) {
	t := normalizeTTL(ttl)
	if t == 0 {
		return 0, nil
	}
	if t < dnspodMinTTL || t > dnspodMaxTTL {
		return 0, fmt.Errorf(
			"tier1: TTL %d 超出 DNSPod 允许的范围（%d-%d 秒）；"+
				"另外不同套餐的最低值不同：免费版 600 秒、专业版 60 秒、企业版 1 秒",
			t, dnspodMinTTL, dnspodMaxTTL)
	}
	return t, nil
}

// dnspodSameModify 判断这次修改是否真的会改变任何东西。
//
// 只比对表单里**已经设了的**字段：没设的字段不会被发送，也就改不了什么。
// 当前值来自 Record.Info。任何一项拿不准（解析不出来）都返回 false，
// 也就是"当作有变动、照常提交" —— 这里的方向是宁可多一次请求，
// 也不要因为比对不准而把用户真正想做的修改吞掉。
func dnspodSameModify(form url.Values, cur dnspodRecord) bool {
	for _, key := range []string{"sub_domain", "record_type", "value", "ttl", "mx", "status"} {
		vals, ok := form[key]
		if !ok || len(vals) == 0 {
			continue
		}
		want := vals[0]

		var got string
		switch key {
		case "sub_domain":
			got = cur.subDomain()
		case "record_type":
			got = cur.recordType()
		case "value":
			got = cur.Value
		case "ttl":
			got = strconv.Itoa(cur.TTL.Int())
		case "mx":
			got = strconv.Itoa(cur.MX.Int())
		case "status":
			got = cur.stateParam()
		}
		if want != got {
			return false
		}
	}
	return true
}

// dnspodToRecord 把 DNSPod 的记录翻译成统一模型。
//
// 三处翻译，都是这套 API 与 ISC 模型之间的真实差异：
//
//  1. name 是**主机记录**（根域名是 "@"），而 dns.Record.Name 要的是完整
//     记录名 —— 不翻译，上层拿到的名字就没法和别家对齐；
//  2. ttl / mx 是字符串（"600" / "0"），靠 dnspodFlexInt 拿回整数；
//  3. remark 对应 dns.Record.Comment（只读方向：Record.Create / Modify
//     都不接受备注，见 CreateRecord 的说明）。
//
// 线路（line）在 dns.Record 里没有位置，只能丢弃 —— 后果是"同一主机记录
// 在不同线路上的多条记录"在列表里长得一模一样。调用方更新它们时必须带上
// 各自的 ID，UpdateRecord 也正是靠 ID 去读回线路，所以不会改错那一条。
func dnspodToRecord(zoneName string, r dnspodRecord) dns.Record {
	return dns.Record{
		ID:       string(r.ID),
		Name:     dnspodFullName(zoneName, r.subDomain()),
		Type:     dns.RecordType(r.recordType()),
		Content:  r.Value,
		TTL:      r.TTL.Int(),
		Comment:  r.Remark,
		Priority: r.MX.Int(),
	}
}

// dnspodFullName 把主机记录拼成完整记录名。
func dnspodFullName(zoneName, host string) string {
	host = strings.TrimSpace(host)
	zoneName = strings.TrimSpace(zoneName)
	switch {
	case host == "" || host == "@":
		return zoneName
	case zoneName == "":
		// 拿不到区域名时只能原样返回。DNSPod 的列表响应里通常带
		// domain.name / domain.punycode，所以这条分支实际很少走到。
		return host
	default:
		return host + "." + zoneName
	}
}

// dnspodMatchFilter 在本地套用过滤条件。
//
// 名字比较忽略大小写：DNS 名字本来就大小写不敏感，而用户从界面上复制来的
// 名字，大小写不一定和记录一致（与 GoDaddy 实现保持一致的行为）。
func dnspodMatchFilter(rec dns.Record, filter dns.RecordFilter) bool {
	if filter.Type != "" && !strings.EqualFold(string(rec.Type), string(filter.Type)) {
		return false
	}
	if filter.Name != "" && !strings.EqualFold(rec.Name, filter.Name) {
		return false
	}
	return true
}

// dnspodAtoi 解析我们自己刚写进表单的整数值。
//
// 调用方只会把 strconv.Itoa 的结果传进来，因此解析失败按 0 处理
// （0 在 TTL / 优先级上都表示"没有值"），不需要把错误带回给用户。
func dnspodAtoi(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}
