package provider

import (
	"github.com/ShirazuNagisa/isc-core/internal/credential"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件登记内核内置的服务商元信息。
//
// 分两层（见 docs/DECISIONS.md D04）：
//
//	Tier-1  完整记录 CRUD —— Cloudflare、阿里云、腾讯云、DNSPod、华为云、GoDaddy
//	Tier-2  仅 A/AAAA 动态解析 —— 其余厂家，实现将在 M2 从 ddns-go 移植
//
// 字段声明顺序有意义：ddns-go 的配置只提供位置化的 id / secret / extParam，
// 导入时按声明顺序映射。因此**顺序一旦发布不可随意调整**。

// ddns-go 凭据模型的槽位编号。
//
// 直接复用 credential 包里的常量，而不是在本地再定义一套 ——
// 导入与运行期调用必须用同一份声明，两套常量迟早会漂移。
const (
	slotID       = credential.DdnsGoID
	slotSecret   = credential.DdnsGoSecret
	slotExtParam = credential.DdnsGoExtParam
)

// 通用字段定义。抽出来是为了让各家声明保持一致，
// 也避免同一段说明文案在十几处重复。
var (
	fieldAccessKeyID = credential.FieldSpec{
		Key: "access_key_id", LabelKey: "provider.field.access_key_id",
		Required: true, Secret: false,
		HelpKey:    "provider.help.access_key_id",
		DdnsGoSlot: slotID,
	}
	fieldAccessKeySecret = credential.FieldSpec{
		Key: "access_key_secret", LabelKey: "provider.field.access_key_secret",
		Required: true, Secret: true,
		DdnsGoSlot: slotSecret,
	}
	fieldSecretID = credential.FieldSpec{
		Key: "secret_id", LabelKey: "provider.field.secret_id",
		Required: true, Secret: false,
		DdnsGoSlot: slotID,
	}
	fieldSecretKey = credential.FieldSpec{
		Key: "secret_key", LabelKey: "provider.field.secret_key",
		Required: true, Secret: true,
		DdnsGoSlot: slotSecret,
	}
	// Cloudflare 只用一个 API Token —— ddns-go 把它放在 secret 槽位，
	// id 槽位是空的。因此这里的 DdnsGoSlot 必须是 slotSecret。
	fieldAPIToken = credential.FieldSpec{
		Key: "token", LabelKey: "provider.field.api_token",
		Required: true, Secret: true,
		HelpKey:    "provider.help.api_token",
		DdnsGoSlot: slotSecret,
	}
	fieldAPIKey = credential.FieldSpec{
		Key: "api_key", LabelKey: "provider.field.api_key",
		Required: true, Secret: false,
		DdnsGoSlot: slotID,
	}
	fieldAPISecret = credential.FieldSpec{
		Key: "api_secret", LabelKey: "provider.field.api_secret",
		Required: true, Secret: true,
		DdnsGoSlot: slotSecret,
	}
	fieldDNSPodID = credential.FieldSpec{
		Key: "id", LabelKey: "provider.field.dnspod_id",
		Required: true, Secret: false,
		HelpKey:    "provider.help.dnspod_id",
		DdnsGoSlot: slotID,
	}
	fieldDNSPodToken = credential.FieldSpec{
		Key: "token", LabelKey: "provider.field.dnspod_token",
		Required: true, Secret: true,
		HelpKey:    "provider.help.dnspod_token",
		DdnsGoSlot: slotSecret,
	}
)

// tier2Fields 是 Tier-2 服务商的通用字段声明。
//
// 全部**非必填**：这些服务商的凭据形态差异很大（有的只要一个 token、
// 有的要 URL、有的要 用户名+密码），而具体用法要等 M2 移植对应实现时
// 才能确定。现在把它们标成必填只会在导入 ddns-go 配置时误伤合法数据。
var tier2Fields = []credential.FieldSpec{
	{Key: "id", LabelKey: "provider.field.id", Secret: false,
		HelpKey: "provider.help.tier2_id", DdnsGoSlot: slotID},
	{Key: "secret", LabelKey: "provider.field.secret", Secret: true,
		HelpKey: "provider.help.tier2_secret", DdnsGoSlot: slotSecret},
	{Key: "ext_param", LabelKey: "provider.field.ext_param", Secret: false,
		HelpKey: "provider.help.tier2_ext_param", DdnsGoSlot: slotExtParam},
}

// builtin 返回全部内置服务商。
func builtin() []Provider {
	out := []Provider{
		// ------------------------------------------------------------------
		// Tier-1：完整记录 CRUD
		// ------------------------------------------------------------------
		{
			Name:             "cloudflare",
			DisplayName:      "Cloudflare",
			ConsoleURL:       "https://dash.cloudflare.com/profile/api-tokens",
			Tier:             1,
			CredentialFields: []credential.FieldSpec{fieldAPIToken},
			// 实现由 attachImplementations 接上：动态更新来自移植的上游代码，
			// 凭据校验来自我们自己的只读端点调用。
		},
		{
			Name:        "alidns",
			DisplayName: i18n.T("provider.name.alidns"),
			ConsoleURL:  "https://ram.console.aliyun.com/manage/ak",
			Tier:        1,
			CredentialFields: []credential.FieldSpec{
				fieldAccessKeyID, fieldAccessKeySecret,
			},
		},
		{
			Name:        "tencentcloud",
			DisplayName: i18n.T("provider.name.tencentcloud"),
			ConsoleURL:  "https://console.cloud.tencent.com/cam/capi",
			Tier:        1,
			CredentialFields: []credential.FieldSpec{
				fieldSecretID, fieldSecretKey,
			},
		},
		{
			// DNSPod 有自己独立的 API 与凭据体系（ID + Token），
			// 与腾讯云 API 的 SecretId/SecretKey 不是一回事，
			// 因此作为独立服务商登记 —— ddns-go 也是分成两个实现的。
			Name:        "dnspod",
			DisplayName: "DNSPod",
			ConsoleURL:  "https://console.dnspod.cn/account/token/token",
			Tier:        1,
			CredentialFields: []credential.FieldSpec{
				fieldDNSPodID, fieldDNSPodToken,
			},
		},
		{
			Name:        "huaweicloud",
			DisplayName: i18n.T("provider.name.huaweicloud"),
			ConsoleURL:  "https://console.huaweicloud.com/iam/#/mine/accessKey",
			Tier:        1,
			CredentialFields: []credential.FieldSpec{
				fieldAccessKeyID, fieldAccessKeySecret,
			},
		},
		{
			Name:        "godaddy",
			DisplayName: "GoDaddy",
			ConsoleURL:  "https://developer.godaddy.com/keys",
			Tier:        1,
			CredentialFields: []credential.FieldSpec{
				fieldAPIKey, fieldAPISecret,
			},
		},
	}

	// ------------------------------------------------------------------
	// Tier-2：仅动态解析（M2 从 ddns-go 移植）
	//
	// 名称必须与 ddns-go 的 `dns.Name` 完全一致，否则导入配置时无法对应。
	// ------------------------------------------------------------------
	tier2 := []struct{ name, display string }{
		{"aliesa", i18n.T("provider.name.ali_esa")},
		{"baiducloud", i18n.T("provider.name.baiducloud")},
		{"callback", i18n.T("provider.name.callback")},
		{"cloudns", "ClouDNS"},
		{"desec", "deSEC"},
		{"dnsla", "DNSLA"},
		{"dynadot", "Dynadot"},
		{"dynv6", "dynv6"},
		{"edgeone", i18n.T("provider.name.edgeone")},
		{"eranet", "Eranet"},
		{"gcore", "Gcore"},
		{"hipmdnsmgr", "HiPM DNS Manager"},
		{"name_com", "Name.com"},
		{"namecheap", "Namecheap"},
		{"namesilo", "NameSilo"},
		{"nowcn", "Now.cn"},
		{"nsone", "IBM NS1 Connect"},
		{"porkbun", "Porkbun"},
		{"rainyun", i18n.T("provider.name.rainyun")},
		{"spaceship", "Spaceship"},
		{"tnethk", "TnetHK"},
		{"trafficroute", i18n.T("provider.name.volcengine")},
		{"vercel", "Vercel"},
	}
	for _, t := range tier2 {
		out = append(out, anonymousProvider(t.name, t.display, 2, tier2Fields))
	}

	// 接上实现。
	//
	// 顺序有讲究：
	//  1. attachImplementations 接上移植代码提供的动态解析（Tier-2 全部 +
	//     Tier-1 五家 —— 它们也在上游的支持列表里）；
	//  2. attachTier1 把新写的记录管理能力**合并**进去，而不是覆盖。
	//
	// 顺序反过来的话，Tier-1 五家的动态解析会被记录管理实现顶掉，
	// 界面上会看到"支持列记录但不支持动态解析"这种与实际不符的组合。
	return attachTier1(attachImplementations(out))
}
