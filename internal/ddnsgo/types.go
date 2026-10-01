// Package ddnsgo 是从 ddns-go 移植而来的 DNS 服务商实现集合。
//
// # 来源与许可
//
// 本包绝大多数文件是 ddns-go（https://github.com/jeessy2/ddns-go）源码的
// **机械移植**，由 scripts/port-ddnsgo.ps1 生成。变换只有三条：
//
//  1. 包名统一为 ddnsgo；
//  2. 去掉 config. 与 util. 包名前缀（移植后同属一个包）；
//  3. 删除指向 ddns-go 自身包的 import。
//
// **逻辑一行未改**：签名算法、URL、请求体、错误处理、比较条件全部原样保留。
// 这不是偷懒 —— 这些实现经过海量用户验证，任何"顺手改进"都可能引入只有
// 在特定服务商、特定账号配置下才暴露的缺陷。
//
// ddns-go 以 MIT 许可证发布（Copyright (c) 2020 jeessy）。完整的许可证
// 原文与本项目的修改说明见仓库根目录的 THIRD_PARTY_NOTICES.md。
//
// # 为什么全部放在一个包里
//
// ddns-go 把类型、签名工具与全部服务商都放在 package dns / util / config
// 里。移植时保持"一个包"能最大限度保留原样 —— 拆包需要给几十个内部符号
// 加上导出名与包前缀，那正是最容易出错的地方。
//
// 本包**不是** ISC 对外的 DNS 抽象。对外接口在 internal/dns，
// 由 internal/dns/tier2.go 适配。这层隔离让"照搬的上游代码"与
// "我们自己的设计"边界清晰，将来升级上游时也不会互相牵扯。
//
// # 与上游的一处实质性差异
//
// 上游把"与服务商比对的间隔次数"放在环境变量 DDNS_IP_CACHE_TIMES 里读，
// 还有一个包级开关 util.ForceCompareGlobal。两者都是全局可变状态，
// 在 ISC 里被替换为显式设置（见 SetCacheTimes）。详见 ipcache.go。
package ddnsgo

import "regexp"

// Ipv4Reg 用于从 URL / 命令输出中抓取 IPv4 字符串。
var Ipv4Reg = regexp.MustCompile(`((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3,3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])`)

// Ipv6Reg 用于从 URL / 命令输出中抓取 IPv6 字符串。
//
// 这里放宽提取范围（允许 IPv4-mapped IPv6，例如 ::ffff:192.168.1.102），
// 最终是否合法由 net.ParseIP 进一步保证。放宽是为了避免因正则过严
// 导致截断或漏匹配。
var Ipv6Reg = regexp.MustCompile(`([0-9A-Fa-f:.]{2,})`)

// DNS 是服务商凭据。
//
// 字段名与语义与 ddns-go 完全一致：ID / Secret / ExtParam 三个位置化的
// 值对应各服务商的凭据字段。ISC 侧的映射规则见 provider.FieldSpec.DdnsGoSlot。
type DNS struct {
	// Name 是服务商标识。如：alidns
	Name string
	// ID 是凭据的第一部分（多数服务商是 AccessKey ID）。
	ID string
	// Secret 是凭据的第二部分（多数服务商是 AccessKey Secret）。
	Secret string
	// ExtParam 是部分服务商需要的额外参数（如 Vercel 的 teamId）。
	ExtParam string
}

// DnsConfig 是一条动态解析配置。
//
// 结构必须与 ddns-go 的 config.DnsConfig 保持逐字段一致 ——
// 30 个 provider 直接读写这些匿名字段（conf.Ipv4.Enable 等），
// 任何重命名都会让移植代码无法编译。
type DnsConfig struct {
	Name string
	Ipv4 struct {
		Enable bool
		// GetType 是获取 IP 的方式：url / netInterface / cmd
		GetType      string
		URL          string
		NetInterface string
		Cmd          string
		Domains      []string
		// ForceAddr 非空时直接使用它，跳过 GetType 指定的获取方式。
		//
		// 这是 ISC 对上游结构做的**唯一一处新增字段**。理由：
		// 上游让每个 provider 自己去找 IP，而 ISC 有统一的 IPMonitor
		// 在跟踪地址与前缀变化 —— 让 30 个 provider 各查一次既浪费，
		// 也会在地址刚好变化的边界时刻取到互不相同的值。
		// 由调用方注入地址，provider 只负责"怎么把它写进 DNS"。
		ForceAddr string
	}
	Ipv6 struct {
		Enable bool
		// GetType 是获取 IP 的方式：url / netInterface / cmd
		GetType      string
		URL          string
		NetInterface string
		Cmd          string
		// Ipv6Reg 选择用第几个地址，或作为正则筛选。
		Ipv6Reg string
		Domains []string
		// ForceAddr 见 Ipv4.ForceAddr。
		ForceAddr string
	}
	DNS DNS
	TTL string
	// HttpInterface 是发送 HTTP 请求时使用的网卡名；为空表示默认网卡。
	HttpInterface string
}

// updateStatusType 是一条域名的更新结果。
type updateStatusType string

// 更新结果常量。值与 ddns-go 一致，便于日志与事件的可读性。
const (
	// UpdatedNothing 未改变
	UpdatedNothing updateStatusType = "未改变"
	// UpdatedFailed 更新失败
	UpdatedFailed updateStatusType = "失败"
	// UpdatedSuccess 更新成功
	UpdatedSuccess updateStatusType = "成功"
)
