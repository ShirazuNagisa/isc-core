package advisories

import (
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// TestEveryKeyExistsInTheCatalog 挡住拼错的 key。
//
// 建议的文案是**存 key**的，而这类 key 不走 i18n.T 调用点，因此 i18n 包
// 自己的"用到的 key 是否存在"检查覆盖不到它们 —— 拼错的结果是界面上
// 直接显示 `advisory.xxx.title`。
func TestEveryKeyExistsInTheCatalog(t *testing.T) {
	known := map[string]bool{}
	for _, key := range i18n.Keys() {
		known[key] = true
	}

	// 用一份"什么都出问题"的输入把所有分支都走一遍。
	full := Input{
		ProxyEnabled:     false,
		ACMEEmail:        "",
		ACMECredentialID: "",
		HasDNSCredential: false,
		Apps: []AppState{
			{ID: "a", Name: "A", State: "failed", Health: "unhealthy", Kind: "node",
				LastError: "boom", RestartCount: 3, MaxRestarts: 3},
			{ID: "b", Name: "B", State: "running", Health: "unhealthy", Kind: "python", RestartCount: 1},
			{ID: "c", Name: "C", State: "running", Health: "healthy", Kind: "docker"},
			{ID: "d", Name: "D", State: "running", Health: "healthy", Kind: "",
				Domains: []DomainState{
					{Name: "x.example.com", RouteReady: false},
					{Name: "y.example.com", RouteReady: true, CertError: "dns timeout"},
					{Name: "z.example.com", RouteReady: true, CertPresent: true, CertNeedsRenew: true},
					{Name: "w.example.com", RouteReady: true, CertPresent: true, CertNeedsRenew: true, CertStaging: true},
				}},
		},
		AvailableRuntimes: map[string]bool{"node": false, "python": true, "docker": true},
		DockerAvailable:   false,
		DDNSTasks:         []DDNSTaskState{{ID: "t", Label: "T", Enabled: true, LastStatus: "failed"}},
	}

	seen := map[string]bool{}
	check := func(a Advisory) {
		if a.TitleKey == "" {
			t.Errorf("%s: empty title key", a.ID)
			return
		}
		if !known[a.TitleKey] {
			t.Errorf("%s: title key %q is not in the message catalog", a.ID, a.TitleKey)
		}
		if a.DetailKey != "" && !known[a.DetailKey] {
			t.Errorf("%s: detail key %q is not in the message catalog", a.ID, a.DetailKey)
		}
		if a.Action != nil && !known[a.Action.LabelKey] {
			t.Errorf("%s: action key %q is not in the message catalog", a.ID, a.Action.LabelKey)
		}
	}

	// 两份输入：一份"哪儿都出问题"，一份"全新安装"。
	// 后者才会走到一次性引导那条规则（它要求还没有任何站点）。
	for _, a := range Evaluate(full) {
		check(a)
		seen[a.ID[:prefixLen(a.ID)]] = true
	}
	for _, a := range Evaluate(Input{}) {
		check(a)
		seen[a.ID[:prefixLen(a.ID)]] = true
	}

	// 确认上面的输入确实触发了每一条规则 —— 否则这个测试会因为"没走到"
	// 而变成一条永远通过的空测试。
	for _, want := range []string{
		"first_run_setup", "proxy_disabled", "acme_email_missing", "acme_credential_missing",
		"app_failed", "app_unhealthy", "app_restarted", "runtime_missing", "docker_missing",
		"route_missing", "cert_failed", "cert_expiring", "ddns_failing",
	} {
		if !seen[want] {
			t.Errorf("rule %q was never exercised by the fixture", want)
		}
	}
}

// prefixLen 取 "rule:id" 里的规则名长度。
func prefixLen(id string) int {
	for i, r := range id {
		if r == ':' {
			return i
		}
	}
	return len(id)
}

func TestEvaluateOnACleanSetupIsQuiet(t *testing.T) {
	in := Input{
		ProxyEnabled:     true,
		ACMEEmail:        "me@example.com",
		ACMECredentialID: "cred-1",
		HasDNSCredential: true,
		Apps: []AppState{{
			ID: "a", Name: "A", State: "running", Health: "healthy", Kind: "node",
			Domains: []DomainState{{Name: "a.example.com", RouteReady: true, CertPresent: true}},
		}},
		AvailableRuntimes: map[string]bool{"node": true},
	}
	if got := Evaluate(in); len(got) != 0 {
		t.Fatalf("a clean setup should produce no advice, got %#v", got)
	}
}

func TestEvaluateSortsBySeverity(t *testing.T) {
	in := Input{
		HasDNSCredential: true,
		ProxyEnabled:     false,
		ACMEEmail:        "me@example.com",
		ACMECredentialID: "cred",
		Apps: []AppState{{
			ID: "a", Name: "A", State: "running", Health: "healthy",
			Domains: []DomainState{{Name: "a.example.com", RouteReady: true, CertError: "nope"}},
		}},
	}
	got := Evaluate(in)
	if len(got) < 2 {
		t.Fatalf("expected at least two advisories, got %d", len(got))
	}
	// 不处理就达不到目标的排在前面。
	if got[0].Severity != SeverityBlocking {
		t.Fatalf("blocking advice must come first, got %s", got[0].Severity)
	}
	for i := 1; i < len(got); i++ {
		if severityRank(got[i-1].Severity) > severityRank(got[i].Severity) {
			t.Fatalf("advisories are not sorted by severity: %v", got)
		}
	}
}

// 只想内网跑一个静态站的用户不该看到一堆关于公网的提醒。
func TestPublicAdviceIsOnlyRaisedWhenASiteWantsADomain(t *testing.T) {
	in := Input{
		HasDNSCredential: true,
		ProxyEnabled:     false,
		ACMEEmail:        "",
		ACMECredentialID: "",
		Apps:             []AppState{{ID: "a", Name: "A", State: "running", Health: "healthy", Kind: ""}},
	}
	for _, a := range Evaluate(in) {
		switch a.ID {
		case "proxy_disabled", "acme_email_missing", "acme_credential_missing":
			t.Fatalf("no site asked for a domain, so %q should not be raised", a.ID)
		}
	}
}

func TestRuntimeAdviceCarriesAProvisionAction(t *testing.T) {
	in := Input{
		HasDNSCredential:  true,
		ProxyEnabled:      true,
		ACMEEmail:         "me@example.com",
		ACMECredentialID:  "cred",
		AvailableRuntimes: map[string]bool{"node": false},
		Apps:              []AppState{{ID: "a", Name: "A", State: "stopped", Kind: "node"}},
	}
	got := Evaluate(in)
	if len(got) != 1 {
		t.Fatalf("expected exactly one advisory, got %#v", got)
	}
	action := got[0].Action
	if action == nil || action.Path != "/v1/runtimes/provision" {
		t.Fatalf("a missing runtime should come with a way to fix it: %#v", got[0])
	}
	kinds, ok := action.Body["kinds"].([]string)
	if !ok || len(kinds) != 1 || kinds[0] != "node" {
		t.Fatalf("the action must name the runtime to prepare: %#v", action.Body)
	}
}

// 一个"未知"的运行时类型不该报成缺失：那会对着一个用户从没选过的
// 类型弹提示。只有明确知道不可用时才提。
func TestUnknownRuntimeKindDoesNotRaiseAdvice(t *testing.T) {
	in := Input{
		HasDNSCredential:  true,
		ProxyEnabled:      true,
		ACMEEmail:         "me@example.com",
		ACMECredentialID:  "cred",
		AvailableRuntimes: map[string]bool{"node": true},
		Apps:              []AppState{{ID: "a", Name: "A", State: "running", Kind: "cobol"}},
	}
	if got := Evaluate(in); len(got) != 0 {
		t.Fatalf("an unknown kind must not be reported as missing: %#v", got)
	}
}
