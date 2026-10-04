// Package advisories 把当前状态归纳成"用户接下来该做什么"。
//
// 它是**纯函数**：输入是一份普通数据结构，输出是一个有序列表。它不碰
// 存储、不发请求、不依赖任何管理器 —— 因此可以完整地单元测试，而
// API 层只负责把各处的状态收集起来。
//
// # 为什么要有这一层
//
// v0.2.0 面向的是"只想一步到位"的用户。对他们来说，一个跑不起来但界面
// 一切正常的应用，比一个明确报错的应用更难处理。建议引擎把这些沉默的
// 失败翻译成一句可行动的话：缺什么、去哪配、点一下能不能修好。
//
// # 严重程度
//
//   - blocking：不处理就达不到用户的目标（站点上不了公网、应用起不来）。
//   - warning：能用，但有隐患或已临期（证书快到期、上次解析失败）。
//   - info：一次性引导（还没配过 DNS 凭据）。
package advisories

import "sort"

// Severity 是建议的严重程度。
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityBlocking Severity = "blocking"
)

func severityRank(s Severity) int {
	switch s {
	case SeverityBlocking:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

// Action 是"一键修好"所需的最小信息。
//
// 它是**方法 + 路径 + 载荷**而不是一个回调：建议引擎不该知道怎么改内核
// 状态，界面也不该猜。内核补上了这个动作，界面把它渲染成一个按钮。
type Action struct {
	LabelKey string
	Method   string
	Path     string
	Body     map[string]any
}

// Advisory 是一条建议。
//
// 文案存的是 i18n key 与参数，不是成品句子：语言由看的人决定，
// 而本包是纯逻辑，不该依赖某个全局语言设置。
type Advisory struct {
	ID         string
	Severity   Severity
	TitleKey   string
	TitleArgs  []any
	DetailKey  string
	DetailArgs []any
	Action     *Action
}

// DomainState 是一个域名的公网侧状态。
type DomainState struct {
	Name           string
	RouteReady     bool
	CertPresent    bool
	CertNeedsRenew bool
	CertStaging    bool
	CertError      string
}

// AppState 是一个站点的状态。
type AppState struct {
	ID           string
	Name         string
	State        string
	Health       string
	Kind         string
	LastError    string
	RestartCount int
	MaxRestarts  int
	Domains      []DomainState
}

// DDNSTaskState 是一个动态解析任务的状态。
type DDNSTaskState struct {
	ID         string
	Label      string
	Enabled    bool
	LastStatus string
}

// Input 是评估所需的全部输入。
type Input struct {
	// ACMEEmail 为空时签不出证书（CA 需要一个联系邮箱）。
	ACMEEmail string
	// ACMECredentialID 是 DNS-01 用的凭据；为空时无法完成校验。
	ACMECredentialID string
	// ProxyEnabled 为 false 时反代根本不监听，公网访问无从谈起。
	ProxyEnabled bool
	// HasDNSCredential 表示至少有一个可用的 DNS 凭据。
	HasDNSCredential bool

	Apps []AppState

	// AvailableRuntimes 是 kind → 是否可用。
	AvailableRuntimes map[string]bool

	// DockerAvailable 表示本机是否装了 Docker（内核不代装）。
	DockerAvailable bool

	DDNSTasks []DDNSTaskState
}

// Evaluate 生成建议列表，按严重程度排序（重的在前），同级按 ID 稳定排序。
func Evaluate(in Input) []Advisory {
	out := make([]Advisory, 0)

	// 1. 一次性引导：还没有任何 DNS 凭据时，什么都做不了。
	if !in.HasDNSCredential && len(in.Apps) == 0 {
		out = append(out, Advisory{
			ID:        "first_run_setup",
			Severity:  SeverityInfo,
			TitleKey:  "advisory.first_run_setup.title",
			DetailKey: "advisory.first_run_setup.detail",
			Action: &Action{
				LabelKey: "advisory.action.open_dns",
				Method:   "POST",
				Path:     "/v1/credentials",
			},
		})
	}

	wantsPublic := false
	for _, app := range in.Apps {
		if len(app.Domains) > 0 {
			wantsPublic = true
			break
		}
	}

	// 2. 公网链路的三个前置条件。只在确实有站点想上公网时才提，
	//    否则会给"只想内网跑一个静态站"的用户制造噪音。
	if wantsPublic {
		if !in.ProxyEnabled {
			out = append(out, Advisory{
				ID:        "proxy_disabled",
				Severity:  SeverityBlocking,
				TitleKey:  "advisory.proxy_disabled.title",
				DetailKey: "advisory.proxy_disabled.detail",
				Action: &Action{
					LabelKey: "advisory.action.enable_proxy",
					Method:   "PATCH",
					Path:     "/v1/settings",
					Body:     map[string]any{"proxy_enabled": true},
				},
			})
		}
		if in.ACMEEmail == "" {
			out = append(out, Advisory{
				ID:        "acme_email_missing",
				Severity:  SeverityBlocking,
				TitleKey:  "advisory.acme_email_missing.title",
				DetailKey: "advisory.acme_email_missing.detail",
			})
		}
		if in.ACMECredentialID == "" {
			out = append(out, Advisory{
				ID:        "acme_credential_missing",
				Severity:  SeverityBlocking,
				TitleKey:  "advisory.acme_credential_missing.title",
				DetailKey: "advisory.acme_credential_missing.detail",
			})
		}
	}

	// 3. 站点自身的问题。
	for _, app := range in.Apps {
		if app.State == "failed" {
			out = append(out, Advisory{
				ID:         "app_failed:" + app.ID,
				Severity:   SeverityBlocking,
				TitleKey:   "advisory.app_failed.title",
				TitleArgs:  []any{app.Name},
				DetailKey:  "advisory.app_failed.detail",
				DetailArgs: []any{app.LastError},
			})
		} else if app.Health == "unhealthy" {
			out = append(out, Advisory{
				ID:        "app_unhealthy:" + app.ID,
				Severity:  SeverityWarning,
				TitleKey:  "advisory.app_unhealthy.title",
				TitleArgs: []any{app.Name},
				DetailKey: "advisory.app_unhealthy.detail",
			})
		}

		// 重启过说明它崩过。上限用尽时上面那条 blocking 已经说了，
		// 这里只在"崩过但已经恢复"时提醒。
		if app.RestartCount > 0 && app.State != "failed" {
			out = append(out, Advisory{
				ID:         "app_restarted:" + app.ID,
				Severity:   SeverityWarning,
				TitleKey:   "advisory.app_restarted.title",
				TitleArgs:  []any{app.Name, app.RestartCount},
				DetailKey:  "advisory.app_restarted.detail",
				DetailArgs: []any{app.LastError},
			})
		}

		// 运行时缺失时部署必然失败：提前说，而不是让用户点下去再看报错。
		if app.Kind != "" && app.Kind != "docker" {
			if available, known := in.AvailableRuntimes[app.Kind]; known && !available {
				out = append(out, Advisory{
					ID:        "runtime_missing:" + app.ID + ":" + app.Kind,
					Severity:  SeverityBlocking,
					TitleKey:  "advisory.runtime_missing.title",
					TitleArgs: []any{app.Name, app.Kind},
					DetailKey: "advisory.runtime_missing.detail",
					Action: &Action{
						LabelKey: "advisory.action.provision_runtime",
						Method:   "POST",
						Path:     "/v1/runtimes/provision",
						Body:     map[string]any{"kinds": []string{app.Kind}},
					},
				})
			}
		}
		if app.Kind == "docker" && !in.DockerAvailable {
			out = append(out, Advisory{
				ID:        "docker_missing:" + app.ID,
				Severity:  SeverityBlocking,
				TitleKey:  "advisory.docker_missing.title",
				TitleArgs: []any{app.Name},
				DetailKey: "advisory.docker_missing.detail",
			})
		}

		// 4. 域名与证书。
		for _, domain := range app.Domains {
			if !domain.RouteReady {
				out = append(out, Advisory{
					ID:         "route_missing:" + app.ID + ":" + domain.Name,
					Severity:   SeverityWarning,
					TitleKey:   "advisory.route_missing.title",
					TitleArgs:  []any{domain.Name},
					DetailKey:  "advisory.route_missing.detail",
					DetailArgs: []any{app.Name},
				})
			}
			switch {
			case domain.CertError != "":
				out = append(out, Advisory{
					ID:         "cert_failed:" + domain.Name,
					Severity:   SeverityWarning,
					TitleKey:   "advisory.cert_failed.title",
					TitleArgs:  []any{domain.Name},
					DetailKey:  "advisory.cert_failed.detail",
					DetailArgs: []any{domain.CertError},
				})
			case domain.CertPresent && domain.CertNeedsRenew:
				key := "advisory.cert_expiring.detail"
				if domain.CertStaging {
					key = "advisory.cert_staging.detail"
				}
				out = append(out, Advisory{
					ID:        "cert_expiring:" + domain.Name,
					Severity:  SeverityWarning,
					TitleKey:  "advisory.cert_expiring.title",
					TitleArgs: []any{domain.Name},
					DetailKey: key,
				})
			}
		}
	}

	// 5. 动态解析的失败。
	for _, task := range in.DDNSTasks {
		if task.Enabled && task.LastStatus == "failed" {
			out = append(out, Advisory{
				ID:        "ddns_failing:" + task.ID,
				Severity:  SeverityWarning,
				TitleKey:  "advisory.ddns_failing.title",
				TitleArgs: []any{task.Label},
				DetailKey: "advisory.ddns_failing.detail",
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		left, right := severityRank(out[i].Severity), severityRank(out[j].Severity)
		if left != right {
			return left < right
		}
		return out[i].ID < out[j].ID
	})
	return out
}
