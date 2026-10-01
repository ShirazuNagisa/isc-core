package provider

import (
	"context"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件钉住"哪些服务商有哪些能力"这件事。
//
// # 为什么这条测试重要
//
// 能力位不是配置，而是从实现的接口断言**推导**出来的（见 Capabilities）。
// 推导的链条有三段，任何一段断了都不会有编译错误：
//
//	各家的 NewXxx 实现了哪些接口
//	  → attachImplementations 接上动态解析（移植代码）
//	  → attachTier1 把记录管理与动态解析**合成一个对象**
//
// 断掉的症状是"界面上少了几排按钮"或"点了就报不支持"——
// 而那正是用户最难判断该怪谁的一类问题。所以这里逐家断言。

// tier1Expected 是 Tier-1 六家必须具备的完整能力。
var tier1Expected = []string{
	"cloudflare", "alidns", "tencentcloud", "dnspod", "huaweicloud", "godaddy",
}

func TestTier1ProvidersAreFullyWired(t *testing.T) {
	t.Parallel()

	reg := Default()
	for _, name := range tier1Expected {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, ok := reg.Get(name)
			if !ok {
				t.Fatalf("服务商 %s 未在注册表中登记", name)
			}
			if p.Impl == nil {
				t.Fatalf("%s 没有接入任何实现", name)
			}
			if p.Tier != 1 {
				t.Errorf("%s 的 Tier = %d，期望 1", name, p.Tier)
			}

			caps := p.Capabilities()

			// 完整记录管理。
			//
			// 这五项一起断言而不是分开：少任何一项都意味着"界面上会出现
			// 一排点了就报错的按钮"，而用户无法判断是自己配错了还是
			// 内核没实现。
			if !caps.Available {
				t.Error("Available 应当为 true")
			}
			if !caps.ZoneList {
				t.Error("缺少 ZoneList —— 用户将无法选择域名")
			}
			if !caps.RecordList {
				t.Error("缺少 RecordList")
			}
			if !caps.RecordCreate {
				t.Error("缺少 RecordCreate")
			}
			if !caps.RecordUpdate {
				t.Error("缺少 RecordUpdate")
			}
			if !caps.RecordDelete {
				t.Error("缺少 RecordDelete")
			}

			// 动态解析必须仍然可用。
			//
			// 这是最容易断的一环：attachTier1 会用记录管理实现**替换**
			// p.Impl。若没有把移植过来的动态解析注入进去，这六家就会
			// 静默失去动态解析能力 —— 而它们恰恰是 ddns-go 的主力服务商。
			if !caps.Dynamic {
				t.Error("缺少 Dynamic —— 记录管理实现把动态解析顶掉了")
			}
		})
	}
}

// TestTier1DynamicUpdaterActuallyWorks 验证注入的不只是"能力位"。
//
// 能力位来自接口断言，而断言只看方法存不存在 —— 一个把 dynamicDelegate
// 嵌进去、但从未调用 SetDynamicUpdater 的对象同样会报告 Dynamic=true，
// 只是调用时才失败。这条测试走一遍真实调用路径，把那种情况挡住。
func TestTier1DynamicUpdaterActuallyWorks(t *testing.T) {
	t.Parallel()

	reg := Default()
	for _, name := range tier1Expected {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			updater, ok := reg.DynamicUpdater(name)
			if !ok {
				t.Fatalf("%s 没有动态解析实现", name)
			}

			// 用一个必然失败的地址调用一次，只关心"有没有被正确转发"。
			//
			// 期望的错误是网络层的（连不上），而不是
			// "动态解析实现未接入（装配遗漏）"—— 后者才是接线断了。
			_, err := updater.UpdateDynamic(context.Background(),
				dns.Credential{Provider: name, Fields: map[string]string{}},
				dns.DynamicRequest{
					Domains:    []string{"example.com"},
					RecordType: dns.TypeA,
					IP:         "203.0.113.7",
				})
			if err == nil {
				// 空凭据居然成功了？那说明实现有问题，但与本测试无关，
				// 不判失败。
				return
			}
			if containsAssemblyGap(err.Error()) {
				t.Fatalf("动态解析没有被注入到 %s 的记录管理实现里: %v", name, err)
			}
		})
	}
}

// containsAssemblyGap 判断错误是否是"装配遗漏"。
func containsAssemblyGap(msg string) bool {
	return len(msg) > 0 && (stringContains(msg, "装配遗漏") ||
		stringContains(msg, "动态解析实现未接入"))
}

func stringContains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestTier2ProvidersDoNotClaimRecordManagement 验证 Tier-2 不虚报能力。
//
// 虚报的代价比不报大得多：界面会显示一堆"新增记录""删除记录"按钮，
// 用户点了才发现报错 —— 而那时光看界面已经无法判断是服务商不支持、
// 还是自己的凭据权限不够。
func TestTier2ProvidersDoNotClaimRecordManagement(t *testing.T) {
	t.Parallel()

	// 挑几个纯粹的 Tier-2（不在 Tier-1 名单里）。
	tier2 := []string{"callback", "porkbun", "namesilo", "vercel"}

	reg := Default()
	for _, name := range tier2 {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, ok := reg.Get(name)
			if !ok {
				t.Skipf("%s 不在注册表中", name)
			}
			caps := p.Capabilities()

			if !caps.Available || !caps.Dynamic {
				t.Errorf("%s 应当具备动态解析能力: %+v", name, caps)
			}
			if caps.ZoneList || caps.RecordList || caps.RecordCreate ||
				caps.RecordUpdate || caps.RecordDelete {
				t.Errorf("%s 是 Tier-2，不应报告记录管理能力: %+v", name, caps)
			}
		})
	}
}

// TestTier1ProvidersAreVerifiableOrHonest 记录"测试连接"能力的现状。
//
// 不强制每家都能校验凭据 —— 有些服务商（阿里云、腾讯云、华为云）
// **没有**只读的校验端点，用"列一次域名"来冒充会把最小权限账号
// 误判为无效。与其给一个会误报的实现，不如没有。
//
// 但已经实现了校验的必须如实报告，否则界面会把一个可用的
// "测试连接"按钮藏起来。
func TestTier1ProvidersAreVerifiableOrHonest(t *testing.T) {
	t.Parallel()

	reg := Default()

	// Cloudflare 有专门的只读校验端点，必须报告为可校验。
	cf, ok := reg.Get("cloudflare")
	if !ok {
		t.Fatal("cloudflare 不在注册表中")
	}
	if !cf.Capabilities().Verify {
		t.Error("Cloudflare 有 /user/tokens/verify，应当报告 Verify=true")
	}

	// 其余几家当前的取舍是"不实现" —— 这条断言的作用是：
	// 将来有人给它们加上校验时，必须同时确认能力位跟着变，
	// 而不是加了实现却忘了让界面显示出来。
	for _, name := range []string{"alidns", "tencentcloud", "dnspod", "huaweicloud", "godaddy"} {
		p, ok := reg.Get(name)
		if !ok {
			continue
		}
		hasVerifier := false
		if v, ok := reg.Verifier(name); ok && v != nil {
			hasVerifier = true
		}
		if p.Capabilities().Verify != hasVerifier {
			t.Errorf("%s 的能力位 Verify=%v 与实际是否有校验实现=%v 不一致",
				name, p.Capabilities().Verify, hasVerifier)
		}
	}
}
