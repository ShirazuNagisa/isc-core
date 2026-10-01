package provider

import (
	"github.com/ShirazuNagisa/isc-core/internal/dns"
	"github.com/ShirazuNagisa/isc-core/internal/provider/tier1"
)

// 本文件把 Tier-1 的完整记录管理实现接到注册表上。
//
// 与 Tier-2 的区别（见 tier2.go）：Tier-2 的动态解析来自移植的 ddns-go
// 代码，一张工厂表就能覆盖；Tier-1 的记录 CRUD 是**为 ISC 新写**的 ——
// 上游只实现了"A/AAAA 更新到当前 IP"，没有记录管理可言。

// tier1Impl 返回某家服务商的 Tier-1 实现；没有则返回 nil。
//
// 地址参数留空表示使用官方 API 地址；测试通过直接构造
// tier1.NewXxx(localURL) 来指向假服务器。
//
// 名字必须与 builtin.go 里登记的完全一致 —— 写错一个字母的症状是
// 该服务商静默地失去全部记录管理能力，而界面上只是少了几排按钮。
func tier1Impl(name string) dns.Provider {
	switch name {
	case "cloudflare":
		return tier1.NewCloudflare("")
	case "alidns":
		return tier1.NewAlidns("")
	case "tencentcloud":
		return tier1.NewTencentCloud("")
	case "dnspod":
		return tier1.NewDnspod("")
	case "huaweicloud":
		return tier1.NewHuaweicloud("")
	case "godaddy":
		return tier1.NewGoDaddy("")
	default:
		return nil
	}
}

// dynamicSetter 由 Tier-1 实现提供，用于注入动态解析能力。
//
// 见 internal/provider/tier1 里 dynamicDelegate 的说明：
// Tier-1 五家在移植代码里也有动态解析实现，必须与记录管理合成一个对象，
// 能力位才是完整的。
type dynamicSetter interface {
	SetDynamicUpdater(dns.DynamicUpdater)
}

// attachTier1 为已登记的服务商接上 Tier-1 的记录管理能力。
//
// 它同时完成"注入动态解析"这件事：移植代码提供的动态解析实现被交给
// Tier-1 对象，于是**一个对象同时具备两种能力**，`Capabilities()` 推导出的
// 能力位才是准确的。
//
// # 能力位的来源
//
// `Provider.Capabilities()` 从实现的接口断言推导 —— 因此
// "某家支持删除记录"是**代码事实**，而不是一份需要人工维护、
// 迟早会过期的能力表。
func attachTier1(list []Provider) []Provider {
	for i := range list {
		p := &list[i]
		impl := tier1Impl(p.Name)
		if impl == nil {
			// 没有记录管理实现：保留原有的（Tier-2 动态解析）实现。
			continue
		}

		// 把移植代码的动态解析能力注入进去。
		if setter, ok := impl.(dynamicSetter); ok {
			if dyn, ok := p.Impl.(dns.DynamicUpdater); ok {
				setter.SetDynamicUpdater(dyn)
			}
		}
		p.Impl = impl
	}
	return list
}
