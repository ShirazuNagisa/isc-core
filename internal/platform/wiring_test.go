package platform

import (
	"runtime"
	"strings"
	"testing"
)

// 本文件守的是**装配点接对了没有**，而不是某个后端写得对不对。
//
// # 为什么需要它
//
// `internal/platform` 里每个后端都有自己的测试（plist 渲染、nft 规则、
// SCM 参数……），而它们全是**与平台无关的纯逻辑**：把后端函数单独拿出来测。
// 于是有一类缺陷谁也看不见 —— **实现写好了，但装配点还挂着 stub**。
//
// 它真实发生过，而且是两个平台同时：
//
//	platform_darwin.go  ServiceManager: newUnsupportedServiceManager(launchd_todo)
//	platform_linux.go   ServiceManager: newUnsupportedServiceManager(systemd_todo)
//
// 而 service_darwin.go / service_linux.go 里的 launchd 与 systemd 后端
// 早就写完了。后果是 `isc service install` 在这两个平台上直接报
// "将在 M5 实现"，而 docs/PLAN.md 的 M5-a 写着三平台服务安装已完成 ——
// 在 Windows 上开发、只在 Windows 上真机验证，就正好漏掉这两处（R8）。
//
// 判据刻意选得"粗"：**只要装配点指向 stub 就失败**。它不检查后端名字的
// 拼写（那是各平台自己的事），检查的是"这个能力到底接上了没有"。
func TestServiceManagerIsWired(t *testing.T) {
	t.Parallel()

	bundle := Current(t.TempDir())
	state := bundle.ServiceManager.Describe()

	if !state.Available {
		t.Fatalf("ServiceManager 在 %s 上不可用（Backend=%q）—— "+
			"装配点很可能还挂着 stub，而实现文件里已经有真正的后端了。"+
			"见本文件的注释。", runtime.GOOS, state.Backend)
	}
	if state.Backend == "" || strings.Contains(state.Backend, "unsupported") {
		t.Errorf("ServiceManager 的 Backend = %q，看起来仍是占位实现", state.Backend)
	}

	// 每个平台该接哪个后端是确定的，写下来免得"接上了但接错了"。
	want := map[string]string{
		"darwin":  "launchd",
		"linux":   "systemd",
		"windows": "windows-scm",
	}
	if name, ok := want[runtime.GOOS]; ok && state.Backend != name {
		t.Errorf("ServiceManager 在 %s 上接到了 %q，期望 %q",
			runtime.GOOS, state.Backend, name)
	}
}

// TestBundleHasNoStubsWhereImplementationsExist 是上一条的**普遍形式**。
//
// 只盯 ServiceManager 是不够的：同一类漏接可能发生在任何一个后端上
//（防火墙、IP 监控、密钥存储、传输、低端口绑定）。这里逐个检查
// "这个平台上应当可用的后端"是不是真的可用。
//
// 名单是**按平台声明**的，因此它同时是一份"这个平台做到哪一步"的清单：
// 想让它继续通过，就得把新后端的名字加进来 —— 而不是让缺陷静悄悄地留着。
func TestBundleHasNoStubsWhereImplementationsExist(t *testing.T) {
	t.Parallel()

	bundle := Current(t.TempDir())

	type check struct {
		name  string
		state ImplState
	}
	checks := []check{
		{"firewall", bundle.Firewall.Describe()},
		{"service_manager", bundle.ServiceManager.Describe()},
		{"ip_monitor", bundle.IPMonitor.Describe()},
		{"secret_store", bundle.SecretStore.Describe()},
		{"transport", bundle.Transport.Describe()},
	}

	// 这几个平台上，这些后端都应当是真实现。
	// freebsd 等其它平台走 platform_other.go，那里**刻意**是 stub（没有实现），
	// 因此不在名单里。
	implemented := map[string][]string{
		"darwin":  {"firewall", "service_manager", "ip_monitor", "secret_store", "transport"},
		"linux":   {"firewall", "service_manager", "ip_monitor", "secret_store", "transport"},
		"windows": {"firewall", "service_manager", "ip_monitor", "secret_store", "transport"},
	}
	required := implemented[runtime.GOOS]
	if len(required) == 0 {
		t.Skipf("%s 上这些后端刻意是占位实现，跳过", runtime.GOOS)
	}

	byName := map[string]ImplState{}
	for _, c := range checks {
		byName[c.name] = c.state
	}
	for _, name := range required {
		state, ok := byName[name]
		if !ok {
			t.Errorf("名单里的后端 %q 没有出现在检查列表里 —— 名单该更新了", name)
			continue
		}
		if !state.Available {
			t.Errorf("%s 上的 %s 不可用（Backend=%q）—— "+
				"实现存在但装配点没接上，或者名单写错了", runtime.GOOS, name, state.Backend)
		}
	}
}
