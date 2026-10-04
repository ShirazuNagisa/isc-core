package api

import (
	"net/http"

	"github.com/ShirazuNagisa/isc-core/internal/acme"
	"github.com/ShirazuNagisa/isc-core/internal/advisories"
	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/apps"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/metrics"
	"github.com/ShirazuNagisa/isc-core/internal/runtime"
)

// 本文件实现首页需要的两件事：资源占用与"接下来该做什么"。
//
// 它们都**不改状态**，因此都是普通 GET：界面可以随便刷新。

// GetMetrics 实现 GET /v1/metrics。
func (s *Server) GetMetrics(w http.ResponseWriter, r *http.Request) {
	// 采样器缺席时返回一份"不支持"的空快照，而不是 404/500：
	// 界面据此显示"此平台不支持指标"，而不是报错。
	if s.Metrics == nil {
		writeJSON(w, s.Log, http.StatusOK, "application/json", gen.MetricsSnapshot{
			Host: gen.HostMetrics{Backend: ptr("unsupported")},
			Apps: []gen.AppMetrics{},
		})
		return
	}

	snapshot := s.Metrics.Latest()
	history := s.Metrics.HostHistory()
	backend := s.Metrics.Describe()

	out := gen.MetricsSnapshot{
		Host: toGenHostMetrics(snapshot.Host, backend, true),
		Apps: make([]gen.AppMetrics, 0, len(snapshot.Apps)),
	}
	for _, item := range snapshot.Apps {
		out.Apps = append(out.Apps, toGenAppMetrics(item))
	}
	items := make([]gen.HostMetrics, 0, len(history))
	for _, item := range history {
		// 历史点**不带** disks / gpu。
		//
		// 60 个历史点各带一遍全部挂载点会让一次轮询的响应体膨胀十几倍，
		// 而曲线只用到标量字段 —— 挂载点的容量不是曲线，是一个"现在"
		// 的数字。这条取舍写在 openapi.yaml 的 HostMetrics.disks 上。
		items = append(items, toGenHostMetrics(item, backend, false))
	}
	out.History = &items

	writeJSON(w, s.Log, http.StatusOK, "application/json", out)
}

// toGenHostMetrics 转换一次主机采样。
//
// withDetail 为 false 时只填标量字段（历史点用），为 true 时再带上
// 挂载点与 GPU（当前快照用）。
func toGenHostMetrics(sample metrics.HostSample, backend string, withDetail bool) gen.HostMetrics {
	out := gen.HostMetrics{
		CpuPercent:       sample.CPUPercent,
		MemoryUsedBytes:  int(sample.MemoryUsedBytes),
		MemoryTotalBytes: int(sample.MemoryTotalBytes),
		NetRxBytesPerSec: sample.NetRxBytesPerSec,
		NetTxBytesPerSec: sample.NetTxBytesPerSec,
		Backend:          &backend,
	}
	if !sample.At.IsZero() {
		at := sample.At
		out.At = &at
	}
	if !withDetail {
		return out
	}

	// 挂载点始终发出去（哪怕是空数组）：客户端据此区分"这台机器没有
	// 磁盘可报"与"这个内核版本还不报磁盘"。前者显示"没有挂载点"，
	// 后者显示"该功能需要更新内核"，而两者的下一步动作完全不同。
	disks := make([]gen.DiskMetrics, 0, len(sample.Disks))
	for _, disk := range sample.Disks {
		item := gen.DiskMetrics{
			MountPoint: disk.MountPoint,
			TotalBytes: int(disk.TotalBytes),
			UsedBytes:  int(disk.UsedBytes),
			FreeBytes:  int(disk.FreeBytes),
		}
		item.FsType = strPtr(disk.FSType)
		disks = append(disks, item)
	}
	out.Disks = &disks

	if sample.GPU != nil {
		gpu := gen.GpuMetrics{Backend: sample.GPU.Backend}
		gpu.Name = strPtr(sample.GPU.Name)
		gpu.UtilizationPercent = sample.GPU.Utilization
		out.Gpu = &gpu
	}
	// GPU 为 nil 时**不发**这个字段：缺省表示"这台机器没有可采样的 GPU"，
	// 而 `backend: unsupported` 是它的一种显式写法。客户端两种都要认，
	// 但内核这边不该把"没有"伪装成一个空对象。
	return out
}

func toGenAppMetrics(sample metrics.AppSample) gen.AppMetrics {
	out := gen.AppMetrics{
		AppId:         sample.AppID,
		Pid:           sample.PID,
		CpuPercent:    sample.CPUPercent,
		MemoryBytes:   int(sample.MemoryBytes),
		UptimeSeconds: int(sample.UptimeSeconds),
	}
	return out
}

// ListAdvisories 实现 GET /v1/advisories。
func (s *Server) ListAdvisories(w http.ResponseWriter, r *http.Request) {
	cat := i18n.FromContext(r.Context())
	found := advisories.Evaluate(s.advisoryInput(r))

	items := make([]gen.Advisory, 0, len(found))
	for _, item := range found {
		entry := gen.Advisory{
			Id:       item.ID,
			Severity: gen.AdvisorySeverity(item.Severity),
			Title:    cat.T(item.TitleKey, item.TitleArgs...),
		}
		if item.DetailKey != "" {
			detail := cat.T(item.DetailKey, item.DetailArgs...)
			entry.Detail = &detail
		}
		if item.Action != nil {
			action := &gen.AdvisoryAction{Label: cat.T(item.Action.LabelKey)}
			if item.Action.Navigation != "" {
				// 跳转类动作没有 method/path —— 界面据此渲染成"跳到某处"
				// 而不是"发一个请求"。
				navigation := gen.AdvisoryActionNavigation(item.Action.Navigation)
				action.Navigation = &navigation
			} else {
				action.Method = &item.Action.Method
				action.Path = &item.Action.Path
				action.Body = &item.Action.Body
			}
			entry.Action = action
		}
		items = append(items, entry)
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.AdvisoryList{Items: items})
}

// advisoryInput 把内核各处的状态收集成评估输入。
//
// 这里是**唯一**需要知道"状态分别存在哪"的地方；评估逻辑本身是纯函数。
func (s *Server) advisoryInput(r *http.Request) advisories.Input {
	ctx := r.Context()
	in := advisories.Input{}

	if s.Settings != nil {
		current := s.Settings.Get()
		in.ACMEEmail = current.ACMEEmail
		in.ACMECredentialID = current.ACMEDNSCredentialID
		in.ProxyEnabled = current.ProxyEnabled
	}
	if s.Credentials != nil {
		if list, _, err := s.Credentials.List(ctx, "", 1); err == nil {
			in.HasDNSCredential = len(list) > 0
		}
	}

	// 运行时可用性 = 本机已探测到 **或** 该平台有固定发行版可以装。
	//
	// 只看"现在有没有"会误报：一个还没下载过的运行时并不是问题，
	// 部署时内核会去装。真正会卡住用户的是"既没装、也没得装"。
	in.AvailableRuntimes = map[string]bool{}
	platform := runtime.CurrentPlatform()
	for _, kind := range runtime.Kinds() {
		if _, pinned := runtime.ArtifactFor(kind, platform); pinned {
			in.AvailableRuntimes[string(kind)] = true
		}
	}
	if s.Runtimes != nil {
		if inventory, err := s.Runtimes.Inventory(ctx); err == nil {
			for _, item := range inventory {
				in.AvailableRuntimes[string(item.Kind)] = true
				if item.Kind == runtime.KindDocker {
					in.DockerAvailable = true
				}
			}
		}
	}

	// 域名与证书状态：与应用详情用的是同一套查法。
	hosts := map[string]bool{}
	if s.ProxyRoutes != nil {
		if routes, err := s.ProxyRoutes.List(ctx); err == nil {
			for _, route := range routes {
				for _, host := range route.Hosts {
					hosts[host] = true
				}
			}
		}
	}
	certs := map[string]acme.CertStatus{}
	if s.Certs != nil {
		if statuses, err := s.Certs.Status(s.certWants(r)); err == nil {
			for _, status := range statuses {
				for _, domain := range status.Domains {
					certs[domain] = status
				}
			}
		}
	}

	if s.Apps != nil {
		if list, err := s.Apps.List(ctx); err == nil {
			in.Apps = make([]advisories.AppState, 0, len(list))
			for _, app := range list {
				in.Apps = append(in.Apps, toAdvisoryApp(app, hosts, certs))
			}
		}
	}

	if s.Tasks != nil {
		if tasks, err := s.Tasks.List(ctx); err == nil {
			in.DDNSTasks = make([]advisories.DDNSTaskState, 0, len(tasks))
			for _, task := range tasks {
				in.DDNSTasks = append(in.DDNSTasks, advisories.DDNSTaskState{
					ID:         task.ID,
					Label:      task.Label,
					Enabled:    task.Enabled,
					LastStatus: string(task.LastStatus),
				})
			}
		}
	}

	return in
}

func toAdvisoryApp(app apps.App, hosts map[string]bool, certs map[string]acme.CertStatus) advisories.AppState {
	state := advisories.AppState{
		ID:           app.ID,
		Name:         app.Name,
		State:        string(app.State),
		Health:       string(app.Health),
		Kind:         string(app.Kind),
		LastError:    app.LastError,
		RestartCount: app.RestartCount,
		MaxRestarts:  app.MaxRestarts,
	}
	for _, domain := range app.Domains {
		entry := advisories.DomainState{
			Name:       domain,
			RouteReady: hosts[domain],
		}
		if status, ok := certs[domain]; ok {
			entry.CertPresent = !status.ExpiresAt.IsZero()
			entry.CertNeedsRenew = status.NeedsRenew
			entry.CertStaging = status.Staging
			entry.CertError = status.Error
		}
		state.Domains = append(state.Domains, entry)
	}
	return state
}

// --- 小工具 -----------------------------------------------------------------

func ptr[T any](value T) *T { return &value }
