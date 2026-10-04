package ddns

import "testing"

// netInterface 允许网卡名留空（表示自动挑一个），url/cmd 不允许。
//
// 这条区分是补出来的：要求用户填网卡名，等于要求他知道自己的网卡叫什么
// （en0 / eth0 / 一堆 utun），而首次配置必然因此失败一次。但 url 与 cmd
// 没有可推断的默认值，留空只会得到一个看不懂的错误。
func TestSourceValidationAllowsAnAutomaticInterface(t *testing.T) {
	base := Task{CredentialID: "c1", Label: "测试"}

	cases := []struct {
		name    string
		source  Source
		wantErr bool
	}{
		{
			name:   "netInterface 留空表示自动",
			source: Source{Enable: true, GetType: GetTypeNetInterface, Value: "", Domains: []string{"a.example.com"}},
		},
		{
			name:   "netInterface 也可以指定网卡",
			source: Source{Enable: true, GetType: GetTypeNetInterface, Value: "en0", Domains: []string{"a.example.com"}},
		},
		{
			name:    "url 留空没有可推断的默认值",
			source:  Source{Enable: true, GetType: GetTypeURL, Value: "", Domains: []string{"a.example.com"}},
			wantErr: true,
		},
		{
			name:    "cmd 留空同理",
			source:  Source{Enable: true, GetType: GetTypeCmd, Value: "", Domains: []string{"a.example.com"}},
			wantErr: true,
		},
		{
			name:    "没有域名时不算一个有效的来源",
			source:  Source{Enable: true, GetType: GetTypeNetInterface, Value: "", Domains: nil},
			wantErr: true,
		},
		{
			name:    "获取方式不认识",
			source:  Source{Enable: true, GetType: "magic", Value: "x", Domains: []string{"a.example.com"}},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := base
			task.IPv4 = tc.source
			err := task.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected a validation error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// 关掉的来源不参与校验：一个只维护 IPv6 的任务不该因为 IPv4 那半没填而失败。
func TestDisabledSourcesAreNotValidated(t *testing.T) {
	task := Task{
		CredentialID: "c1",
		Label:        "测试",
		IPv4:         Source{Enable: false, GetType: GetTypeURL, Value: ""},
		IPv6:         Source{Enable: true, GetType: GetTypeNetInterface, Value: "", Domains: []string{"a.example.com"}},
	}
	if err := task.Validate(); err != nil {
		t.Fatalf("a disabled source should be ignored: %v", err)
	}
}

// 两种都关掉时任务什么也不做，应当被拒绝。
func TestTaskWithoutAnyEnabledSourceIsRejected(t *testing.T) {
	task := Task{CredentialID: "c1", Label: "测试"}
	if err := task.Validate(); err == nil {
		t.Fatalf("a task that updates nothing must be rejected")
	}
}

// 域名归一化会去重：同一个域名被写两次会让同一个地址被写两遍。
func TestNormalizeDomainsDeduplicates(t *testing.T) {
	got := NormalizeDomains([]string{"A.example.com", "a.example.com", "", "  ", "b.example.com"})
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}
