package i18n

// 本文件由 scripts/extract-ddnsgo-messages.ps1 生成，请勿手工编辑。
//
// 内容是从 ddns-go（MIT，Copyright (c) 2020 jeessy）的 util/messages.go
// 提取的英文译文。移植过来的 provider 沿用"中文原文即消息 key"的约定，
// 因此这里的 key 是中文句子而不是点分标识符 —— 这是刻意的：
// 改写 233 处调用点的风险远高于让两类 key 并存。
//
// 中文侧不需要对应条目：T() 在找不到 key 时返回 key 本身，
// 而 key 已经是中文。

// messagesEnDdnsGo 是移植代码使用的英文译文。
var messagesEnDdnsGo = map[string]string{
	"可使用 .\\ddns-go.exe -s install 安装服务运行": "You can use '.\\ddns-go.exe -s install' to install service",
	"可使用 sudo ./ddns-go -s install 安装服务运行": "You can use 'sudo ./ddns-go -s install' to install service",
	"监听 %s":                                "Listening on %s",
	"配置文件已保存在: %s":                         "Config file has been saved to: %s",
	"你的IP %s 没有变化, 域名 %s":                  "Your IP %s has not changed! Domain: %s",
	"DNS 记录没有变化, 域名 %s":                    "DNS record has not changed! Domain: %s",
	"Cloudflare 代理状态发生变化: %v -> %v! 域名 %s": "Cloudflare proxy status changed: %v -> %v! Domain: %s",
	"新增域名解析 %s 成功! IP: %s":                 "Added domain %s successfully! IP: %s",
	"新增域名解析 %s 失败! 异常信息: %s":               "Failed to add domain %s! Result: %s",
	"更新域名解析 %s 成功! IP: %s":                 "Updated domain %s successfully! IP: %s",
	"更新域名解析 %s 失败! 异常信息: %s":               "Failed to update domain %s! Result: %s",
	"你的IPv4未变化, 未触发 %s 请求":                 "Your IPv4 has not changed, %s request has not been triggered",
	"你的IPv6未变化, 未触发 %s 请求":                 "Your IPv6 has not changed, %s request has not been triggered",
	"Namecheap 不支持更新 IPv6":                 "Namecheap does not support IPv6",
	"dynadot仅支持单域名配置, 多个域名请添加更多配置":         "dynadot only supports single domain configuration, please add more configurations",
	"异常信息: %s":                             "Exception: %s",
	"查询域名信息发生异常! %s":                       "Failed to query domain info! %s",
	"返回内容: %s ,返回状态码: %d":                  "Response body: %s, Response status code: %d",
	"通过接口获取IPv4失败! 接口地址: %s":               "Failed to get IPv4 from %s",
	"通过接口获取IPv6失败! 接口地址: %s":               "Failed to get IPv6 from %s",
	"将不会触发Webhook, 仅在第 3 次失败时触发一次Webhook, 当前失败次数：%d": "Webhook will not be triggered, only trigger once when the third failure, current failure times: %d",
	"在DNS服务商中未找到根域名: %s":                             "Root domain not found in DNS provider: %s",
	"Webhook配置中的URL不正确":                              "Webhook URL is incorrect",
	"Webhook中的 RequestBody JSON 无效":                  "Webhook RequestBody JSON is invalid",
	"Webhook调用成功! 返回数据：%s":                           "Successfully called Webhook! Response body: %s",
	"Webhook调用失败! 异常信息：%s":                           "Failed to call Webhook! Exception: %s",
	"Webhook Header不正确: %s":                          "Webhook header is invalid: %s",
	"请输入Webhook的URL":                                 "Please enter the Webhook URL",
	"Callback的URL不正确":                                "Callback URL is incorrect",
	"Callback调用成功, 域名: %s, IP: %s, 返回数据: %s":         "Successfully called Callback! Domain: %s, IP: %s, Response body: %s",
	"Callback调用失败, 异常信息: %s":                         "Failed to call Callback! Exception: %s",
	"必须输入用户名/密码":                                     "Username/Password is required",
	"密码不安全！尝试使用更复杂的密码":                               "Password is not secure! Try using a more complex password",
	"数据解析失败, 请刷新页面重试":                                "Data parsing failed, please refresh the page and try again",
	"第 %s 个配置未填写域名":                                  "The %s config does not fill in the domain",
	"从网卡获得IPv4失败":                                    "Failed to get IPv4 from network card",
	"从网卡中获得IPv4失败! 网卡名: %s":                          "Failed to get IPv4 from network card! Network card name: %s",
	"获取IPv4结果失败! 接口: %s ,返回值: %s":                    "Failed to get IPv4 result! Interface: %s ,Result: %s",
	"获取%s结果失败! 未能成功执行命令：%s, 错误：%q, 退出状态码：%s":         "Failed to get %s result! Command: %s, Error: %q, Exit status code: %s",
	"获取%s结果失败! 命令: %s, 标准输出: %q":                     "Failed to get %s result! Command: %s, Stdout: %q",
	"从网卡获得IPv6失败":                                    "Failed to get IPv6 from network card",
	"从网卡中获得IPv6失败! 网卡名: %s":                          "Failed to get IPv6 from network card! Network card name: %s",
	"获取IPv6结果失败! 接口: %s ,返回值: %s":                    "Failed to get IPv6 result! Interface: %s ,Result: %s",
	"未找到第 %d 个IPv6地址! 将使用第一个IPv6地址":                  "%dth IPv6 address not found! Will use the first IPv6 address",
	"IPv6匹配表达式 %s 不正确! 最小从1开始":                       "IPv6 match expression %s is incorrect! Minimum start from 1",
	"IPv6将使用正则表达式 %s 进行匹配":                           "IPv6 will use regular expression %s for matching",
	"匹配成功! 匹配到地址: %s":                                "Matched successfully! Matched address: %s",
	"没有匹配到任何一个IPv6地址, 将使用第一个地址":                      "No IPv6 address matched, will use the first address",
	"未能获取IPv4地址, 将不会更新":                              "Failed to get IPv4 address, will not update",
	"未能获取IPv6地址, 将不会更新":                              "Failed to get IPv6 address, will not update",
	"域名: %s 不正确":                                     "The domain %s is incorrect",
	"域名: %s 解析失败":                                    "The domain %s resolution failed",
	"域名 %s 解析未找到，且因添加了参数 %s=%s 导致无法创建。本次更新已被忽略":      "DNS resolution for domain %s was not found, and the creation failed due to the added parameter %s=%s. This update has been ignored.",
	"IPv6未改变, 将等待 %d 次后与DNS服务商进行比对":                  "IPv6 has not changed, will wait %d times to compare with DNS provider",
	"IPv4未改变, 将等待 %d 次后与DNS服务商进行比对":                  "IPv4 has not changed, will wait %d times to compare with DNS provider",
	"本机DNS异常! 将默认使用 %s, 可参考文档通过 -dns 自定义 DNS 服务器":    "Local DNS exception! Will use %s by default, you can use -dns to customize DNS server",
	"等待网络连接: %s":                                     "Waiting for network connection: %s",
	"%s 后重试...":                                      "Retry after %s",
	"网络已连接":                                          "The network is connected",
	"监听端口发生异常, 请检查端口是否被占用! %s":                       "Port listening failed, please check if the port is occupied! %s",
	"ddns-go 服务卸载成功":                                 "ddns-go service uninstalled successfully",
	"ddns-go 服务卸载失败, 异常信息: %s":                       "ddns-go service uninstallation failed, Exception: %s",
	"安装 ddns-go 服务成功! 请打开浏览器并进行配置":                   "Installed ddns-go service successfully! Please open the browser and configure it",
	"安装 ddns-go 服务失败, 异常信息: %s":                      "Failed to install ddns-go service, Exception: %s",
	"ddns-go 服务已安装, 无需再次安装":                          "ddns-go service has been installed, no need to install again",
	"重启 ddns-go 服务成功":                                "Restarted ddns-go service successfully",
	"启动 ddns-go 服务成功":                                "Started ddns-go service successfully",
	"ddns-go 服务未安装, 请先安装服务":                          "ddns-go service is not installed, please install the service first",
	"未改变": "unchanged",
	"失败":  "failed",
	"成功":  "success",
	"%q 配置文件为空, 超过3小时禁止从公网访问":      "%q configuration file is empty, public network access is prohibited for more than 3 hours",
	"%q 被禁止从公网访问":                  "%q is prohibited from accessing the public network",
	"%q 帐号密码不正确":                   "%q username or password is incorrect",
	"%q 登录成功":                      "%q logged in successfully",
	"用户名或密码错误":                     "Username or password is incorrect",
	"登录失败次数过多，请稍后再试":               "Too many login failures, please try again later",
	"用户名 %s 的密码已重置成功! 请重启ddns-go":  "The password of username %s has been reset successfully! Please restart ddns-go",
	"需在 %s 之前完成用户名密码设置,请重启ddns-go": "Need to complete the username and password setting before %s, please restart ddns-go",
	"配置文件 %s 不存在, 可通过-c指定配置文件":     "Config file %s does not exist, you can specify the configuration file through -c",
	"查询域名 %s 信息发生异常! %v":           "Failed to query domain %s info! %v",
	"在DNS服务商中未找到域名: %s":            "Domain not found in DNS provider: %s",
	"IP %s 没有变化，域名 %s":             "IP %s has not changed, domain %s",
}

// ddnsGoKeys 返回移植代码使用的全部消息 key，供 i18n 完整性测试使用。
func ddnsGoKeys() []string {
	out := make([]string, 0, len(messagesEnDdnsGo))
	for k := range messagesEnDdnsGo {
		out = append(out, k)
	}
	return out
}
