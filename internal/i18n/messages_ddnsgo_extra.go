package i18n

// 本文件是移植代码（internal/ddnsgo）里**上游没有提供译文**的那部分消息。
//
// # 为什么不加进 messages_ddnsgo.go
//
// 那个文件由 `scripts/extract-ddnsgo-messages.ps1` 从 ddns-go 的
// `util/messages.go` **生成**。手工往里面加条目会在下一次重新生成时被抹掉，
// 而且会让"生成物"与"手写物"混在一起，两边都不可信。
//
// 这里的 key 是 ddns-go 那套"中文原文即消息 key"的约定，因此它们是中文句子
// 而不是点分标识符。
//
// # 这些条目是从哪来的
//
// 上游 `util/messages.go` 里没有它们，原因是两类：
//
//   - **ISC 自己加的代码**：网卡绑定（SO_BINDTODEVICE / LocalAddr 回退）
//     是移植时新增的，因为内核需要指定出口网卡而 ddns-go 的原始实现
//     只在上游的 GUI 里暴露这个能力；
//   - **上游较新版本才有的 provider**：EdgeOne 源站组、Cloudns、NowCN、
//     天翼云（Tnethk）等，它们的文案在提取脚本跑完之后才出现在上游。
//
// 两类都不该等上游 —— 没有译文的表现是**英文界面上显示中文**，
// 而机制不会报错（`T()` 找不到 key 时返回 key 本身）。
//
// `TestDdnsGoKeysAreTranslated` 守着这条线：任何新的、没有译文的 key
// 都会让测试失败，而不是悄悄漏过去。
var messagesEnDdnsGoExtra = map[string]string{
	// --- 通用 ---
	"异常信息: %v":               "error: %v",
	"Callback调用失败, 异常信息: %v": "the callback failed: %v",
	"获取%s失败: 未识别的获取方式 %q":    "failed to obtain the %s address: unrecognised retrieval method %q",

	// --- 域名解析结果（provider 通用）---
	// 注意下面两条只差一个结尾换行。
	//
	// 移植过来的 provider 用的不是同一个写法（有的带 \n，有的不带），而
	// key 是**精确字符串** —— 因此它们必须各有一条译文，否则总有一条
	// 在英文界面上显示中文。这一条是补译文时漏掉的，因为测试当时用 %s
	// 打印 key，行尾的 \n 看不见。
	"域名解析 %s 成功! IP: %s":                          "DNS record %s updated successfully! IP: %s",
	"域名解析 %s 成功! IP: %s\n":                        "DNS record %s updated successfully! IP: %s\n",
	"域名解析 %s 失败! 异常信息: %s":                        "DNS record %s failed to update: %s",
	"新增域名解析 %s 失败! 异常信息: %s, 请求URL: %s, 响应内容: %s": "creating DNS record %s failed: %s, request URL: %s, response body: %s",
	"新增域名解析 %s 失败! 异常信息: %v":                      "creating DNS record %s failed: %v",
	"更新域名解析 %s 失败! 异常信息: %s, 请求URL: %s, 响应内容: %s": "updating DNS record %s failed: %s, request URL: %s, response body: %s",
	"更新域名解析 %s 失败! 异常信息: %v":                      "updating DNS record %s failed: %v",

	// 下面两条是**拼接出来的**运行时 key：namesilo 用 requestType 前缀拼出
	// "新增…"/"更新…"。它们此前完全不在目录里，因为静态扫描只拼字面量、
	// 看不见变量 —— 于是这条成功消息在英文界面上一直是中文。
	//
	// 现在由 ddnsGoDynamicKeys + TestDynamicKeySitesAreRegistered 守着：
	// 任何新的拼接点都必须显式登记，否则测试失败。
	"新增域名解析 %s 成功! IP: %s\n": "created DNS record %s successfully! IP: %s\n",
	"更新域名解析 %s 成功! IP: %s\n": "updated DNS record %s successfully! IP: %s\n",

	"查询域名信息发生异常！ %s": "querying the domain information raised an error: %s",

	// --- dynadot ---
	"dynadot仅支持单域名配置，多个域名请添加更多配置": "dynadot supports only a single domain per configuration; add more configurations for multiple domains",

	// --- EdgeOne 源站组 ---
	"你的IP %s 没有变化, EdgeOne 源站组 %s":   "your IP %s has not changed; EdgeOne origin group %s",
	"查询 EdgeOne 站点信息发生异常! %s":        "querying the EdgeOne site information raised an error: %s",
	"查询 EdgeOne 源站组信息发生异常! %s":       "querying the EdgeOne origin group information raised an error: %s",
	"整理 EdgeOne 源站组记录失败! %s":         "failed to reconcile the EdgeOne origin group records: %s",
	"更新 EdgeOne 源站组 %s 成功! IP: %s":   "EdgeOne origin group %s updated successfully! IP: %s",
	"更新 EdgeOne 源站组 %s 失败! 异常信息: %s": "updating EdgeOne origin group %s failed: %s",

	// --- 返回给上层的错误（原本是裸 fmt.Errorf 的中文）---
	//
	// 这一组此前连"消息 key"都不是：它们是**硬编码**在
	// `fmt.Errorf("创建 dnsla 请求失败: %w", err)` 里的。移植代码的约定
	// （中文原文即 key）只覆盖了 Log/LogStr，这些错误漏在外面 ——
	// 于是它们在英文界面上永远是中文。
	//
	// 现在走 ddnsgo.Errorf，它先取译文格式串再交给 fmt.Errorf，
	// 因此 %w 的包裹语义完好（LogStr 会提前 Sprintf，把错误链弄断）。

	// 出口网卡
	"找不到网卡 %s: %v":     "interface %s not found: %v",
	"获取网卡 %s 地址失败: %v": "failed to get the address of interface %s: %v",
	"网卡 %s 没有可用的单播地址":  "interface %s has no usable unicast address",

	// dnsla
	"创建 dnsla 请求失败: %w":              "failed to build the dnsla request: %w",
	"请求 dnsla 失败: %w":                "the dnsla request failed: %w",
	"读取 dnsla 响应失败: %w":              "failed to read the dnsla response: %w",
	"dnsla 请求失败，状态码: %d, 响应: %s":     "the dnsla request failed with status %d, response: %s",
	"创建 dnsla 记录列表请求失败: %w":          "failed to build the dnsla record-list request: %w",
	"请求 dnsla 记录列表失败: %w":            "the dnsla record-list request failed: %w",
	"读取 dnsla 记录列表响应失败: %w":          "failed to read the dnsla record-list response: %w",
	"dnsla 记录列表请求失败，状态码: %d, 响应: %s": "the dnsla record-list request failed with status %d, response: %s",

	// EdgeOne 源站组（返回给上层的部分）
	"在 EdgeOne 中未找到站点: %s":                        "no site %s was found in EdgeOne",
	"未能获取域名 %s 对应的 IPv4 地址":                       "could not obtain the IPv4 address for domain %s",
	"未能获取域名 %s 对应的 IPv6 地址":                       "could not obtain the IPv6 address for domain %s",
	"域名 %s 未配置可更新的源站记录":                           "domain %s has no updatable origin record configured",
	"请在域名后追加 ?GroupId=xxx 或 ?OriginGroupName=xxx": "append ?GroupId=xxx or ?OriginGroupName=xxx to the domain",
	"在 EdgeOne 中未找到源站组: %s":                       "no origin group %s was found in EdgeOne",
	"在 EdgeOne 中未找到源站组 GroupId=%s":                "no origin group with GroupId=%s was found in EdgeOne",
	"找到多个名称匹配的源站组，请改用 GroupId 指定唯一源站组":            "several origin groups share that name; use GroupId to pick exactly one",

	// 各家共用的请求阶段
	"生成签名失败: %v":                         "failed to generate the signature: %v",
	"创建请求失败: %v":                         "failed to build the request: %v",
	"请求失败: %v":                           "the request failed: %v",
	"读取响应失败: %v":                         "failed to read the response: %v",
	"序列化请求体失败: %w":                       "failed to serialise the request body: %w",
	"API请求失败，状态码: %d, 响应: %s":            "the API request failed with status %d, response: %s",
	"解析响应失败: %s, 请求URL: %s, 响应内容: %s":    "failed to parse the response: %s, request URL: %s, response body: %s",
	"API请求失败，请求URL: %s, 状态码: %d, 响应: %s": "the API request failed; request URL: %s, status %d, response: %s",

	// --- 出口网卡绑定（ISC 新增）---
	"绑定网卡失败, 将使用默认网卡. 网卡: %s, 错误: %v":                          "binding to the interface failed; the default interface will be used. interface: %s, error: %v",
	"绑定网卡失败, 将使用默认网卡. 网卡: %s, 网络: %s, 错误: 本地IP无效: %s":          "binding to the interface failed; the default interface will be used. interface: %s, network: %s, error: the local IP is invalid: %s",
	"设置 SO_BINDTODEVICE 失败, 回退为仅 LocalAddr 绑定. 网卡: %s, 错误: %v": "setting SO_BINDTODEVICE failed; falling back to binding LocalAddr only. interface: %s, error: %v",
}
