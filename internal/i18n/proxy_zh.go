package i18n

// proxyMessagesZh 是**内置反向代理**的消息。
//
// 这里的文案有一个共同的服务对象：**用户配置错了上游**。而"配错上游"的
// 症状是"从外面打开是 502"，与原因隔着好几层 —— 因此每条错误都尽量把
// **下一步该看什么**写进去（"它启动了吗？端口填对了吗？"）。
//
// 另有三条是**安全检查**，不是普通的校验：上游必须在本机或内网（否则
// 内核就成了一个开放代理）、上游不能指向代理自己（否则无限循环）、
// 域名不能重复绑定（否则请求打到哪一条取决于不可见的顺序）。
// 它们的措辞刻意解释了**为什么**，因为用户的本能是"我就想这么配"。
var proxyMessagesZh = map[string]string{
	// --- 生命周期 ---
	"proxy.err.bad_port": "proxy: 监听端口 %d 不合法",
	"proxy.err.listen": "proxy: 无法监听端口 %d：%w" +
		"（端口可能已被其它程序占用）",
	"proxy.err.not_running": "proxy: 代理未在运行",
	"proxy.err.load_routes": "proxy: 加载路由失败: %w",

	// --- 路由校验 ---
	"proxy.err.dup_domain": "proxy: 域名 %s 被两条路由同时使用（%s 与 %s）—— " +
		"同一个域名只能指向一个上游，否则请求打到哪一条取决于不可见的顺序",
	"proxy.err.no_route_id": "proxy: 路由缺少 ID",
	"proxy.err.no_domain":   "proxy: 路由至少要指定一个域名",

	// --- 域名格式 ---
	"proxy.err.empty_domain": "proxy: 域名不能为空",
	"proxy.err.bad_chars":    "proxy: 域名含有非法字符: %q",
	"proxy.err.wildcard_pos": "proxy: 通配只能写成 *.example.com 的形式" +
		"（通配符只能在最前面）: %q",
	"proxy.err.bad_pattern": "proxy: 非法的域名模式: %q",

	// --- 上游地址（校验与解析）---
	"proxy.err.upstream_empty":   "proxy: 上游地址不能为空",
	"proxy.err.upstream_private": "proxy: 上游地址必须是本机或内网地址",
	"proxy.err.self_loop": "proxy: 上游指向了代理自己（端口 %d）—— " +
		"那会造成无限循环，请填写实际提供服务的那个端口",
	"proxy.err.need_port":        "proxy: 上游地址必须带端口（例如 127.0.0.1:8096）：%s",
	"proxy.err.bad_upstream_fmt": "proxy: 上游地址格式不对: %w",
	"proxy.err.bad_scheme":       "proxy: 不支持的 scheme: %s",
	"proxy.err.bad_port_str":     "proxy: 端口不合法: %s",
	"proxy.err.zero_port":        "proxy: 端口不能为 0",
	"proxy.err.no_host":          "proxy: 上游地址缺少主机部分",
	"proxy.err.resolve_host":     "proxy: 无法解析上游主机 %q: %w",
	"proxy.err.no_addr":          "proxy: 上游主机 %q 没有解析到任何地址",
	"proxy.err.bad_target_fmt":   "proxy: 目标地址格式不对: %w",
	"proxy.err.no_upstream_addr": "没有可用的上游地址",
	"proxy.err.resolve_failed":   "proxy: 解析上游地址失败: %w",

	// 这条把**两个地址都列出来**：用户需要看到"你填的那个"与"它解析到哪"。
	"proxy.err.denied_detail":   "%w：%s 解析到 %s",
	"proxy.err.upstream_denied": "上游地址不被允许",

	// https 上游。本地服务之间的流量不出机器，加 TLS 只增加配置负担。
	"proxy.err.no_https": "proxy: 上游不支持 https —— 本地服务之间的流量不出机器，" +
		"加 TLS 只会让你多配一份自签证书而没有实际收益",

	// --- 转发时 ---
	"proxy.err.no_connect":    "不支持 CONNECT",
	"proxy.err.no_rule":       "没有为此域名配置转发规则",
	"proxy.err.bad_forward":   "转发配置有误",
	"proxy.err.connect_local": "无法连接到本地服务（它启动了吗？端口填对了吗？）",

	// --- TLS ---
	"proxy.err.no_sni": "proxy: TLS 握手没有提供 SNI 主机名，无法选择证书。" +
		"请用域名访问（而不是直接用 IP）",
	"proxy.err.need_cert_source": "proxy: 启用 HTTPS 需要证书来源",
}
