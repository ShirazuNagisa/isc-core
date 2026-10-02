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
	"查询域名信息发生异常！ %s":                              "querying the domain information raised an error: %s",

	// --- dynadot ---
	"dynadot仅支持单域名配置，多个域名请添加更多配置": "dynadot supports only a single domain per configuration; add more configurations for multiple domains",

	// --- EdgeOne 源站组 ---
	"你的IP %s 没有变化, EdgeOne 源站组 %s":   "your IP %s has not changed; EdgeOne origin group %s",
	"查询 EdgeOne 站点信息发生异常! %s":        "querying the EdgeOne site information raised an error: %s",
	"查询 EdgeOne 源站组信息发生异常! %s":       "querying the EdgeOne origin group information raised an error: %s",
	"整理 EdgeOne 源站组记录失败! %s":         "failed to reconcile the EdgeOne origin group records: %s",
	"更新 EdgeOne 源站组 %s 成功! IP: %s":   "EdgeOne origin group %s updated successfully! IP: %s",
	"更新 EdgeOne 源站组 %s 失败! 异常信息: %s": "updating EdgeOne origin group %s failed: %s",

	// --- 出口网卡绑定（ISC 新增）---
	"绑定网卡失败, 将使用默认网卡. 网卡: %s, 错误: %v":                          "binding to the interface failed; the default interface will be used. interface: %s, error: %v",
	"绑定网卡失败, 将使用默认网卡. 网卡: %s, 网络: %s, 错误: 本地IP无效: %s":          "binding to the interface failed; the default interface will be used. interface: %s, network: %s, error: the local IP is invalid: %s",
	"设置 SO_BINDTODEVICE 失败, 回退为仅 LocalAddr 绑定. 网卡: %s, 错误: %v": "setting SO_BINDTODEVICE failed; falling back to binding LocalAddr only. interface: %s, error: %v",
}
