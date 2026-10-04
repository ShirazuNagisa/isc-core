package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/apps"
	"github.com/ShirazuNagisa/isc-core/internal/ddns"
	"github.com/ShirazuNagisa/isc-core/internal/proxy"
)

// appBinder 把托管站点接到反向代理上。
//
// 它是 apps.Binder 的实现，放在 daemon 里：只有装配点同时握着代理管理器
// 与存储，而应用领域层不该知道反向代理的存在。
//
// # 为什么不直接调存储
//
// 走 proxy.Manager.SaveRoutes 而不是自己写库：那条路径会校验上游地址
// （必须是回环或私有网段）、检查域名冲突，并在成功后重载代理。绕过它
// 就等于绕过这些检查，而"反代被配成开放代理"是最严重的后果之一。
type appBinder struct {
	manager *proxy.Manager
	routes  proxy.RouteStore
	log     *slog.Logger
	// tasks 是动态解析服务；为空表示这台内核没有它（库的使用者可以只要
	// 反代那部分能力），此时解析维护整段跳过。
	tasks *ddns.Service
	// dnsCredential 返回"做 DNS 操作该用哪个凭据"。
	//
	// 用的是设置里的 ACME DNS 凭据：用户已经在那里指定过一次"这是我的
	// DNS 凭据"，再让他指定第二次没有道理。
	dnsCredential func(context.Context) string
}

func newAppBinder(manager *proxy.Manager, tasks *ddns.Service, dnsCredential func(context.Context) string, log *slog.Logger) *appBinder {
	return &appBinder{
		manager: manager, routes: manager.RouteStore(),
		tasks: tasks, dnsCredential: dnsCredential, log: log,
	}
}

// EnsureDNS 保证这些域名有动态解析在维护。
//
// # 为什么部署时要顺手做这件事
//
// 只配反向代理的话，域名指向的是**配置那一刻**的本机地址。家宽的地址会变
// （IPv6 前缀重拨就换），于是"站点发布成功了，第二天打不开"。用户没有理由
// 知道这两件事要分别配置。
//
// 复用已有任务而不是每次新建：同一个域名被两个任务同时更新会互相打架，
// 而任务列表也会变得没法看。
func (b *appBinder) EnsureDNS(ctx context.Context, app apps.App) (string, error) {
	if b.tasks == nil || len(app.Domains) == 0 {
		return "", nil
	}
	credentialID := ""
	if b.dnsCredential != nil {
		credentialID = b.dnsCredential(ctx)
	}
	if credentialID == "" {
		// 没有可用的 DNS 凭据：这不是失败，只是这件事现在做不了。
		return "", nil
	}

	tasks, err := b.tasks.List(ctx)
	if err != nil {
		return "", err
	}

	// 已经有任务在管这些域名时，把缺的域名补进去就好。
	for _, task := range tasks {
		if !coversAny(task, app.Domains) {
			continue
		}
		missing := missingDomains(task, app.Domains)
		if len(missing) == 0 {
			return task.ID, nil
		}
		task.IPv4.Domains = append(task.IPv4.Domains, missing...)
		if task.IPv6.Enable {
			task.IPv6.Domains = append(task.IPv6.Domains, missing...)
		}
		if _, err := b.tasks.Update(ctx, task.ID, task); err != nil {
			return "", err
		}
		return task.ID, nil
	}

	// 两种记录类型都开：内核的地址快照会过滤掉不能用于公网的地址，
	// 因此没有可用 IPv4 时它不会硬写一个私网地址进去。
	created, err := b.tasks.Create(ctx, ddns.Task{
		CredentialID: credentialID,
		Label:        app.Name,
		Enabled:      true,
		IPv4:         ddns.Source{Enable: true, GetType: ddns.GetTypeNetInterface, Domains: app.Domains},
		IPv6:         ddns.Source{Enable: true, GetType: ddns.GetTypeNetInterface, Domains: app.Domains},
	})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// RemoveDNS 撤销某个域名的动态解析。
func (b *appBinder) RemoveDNS(ctx context.Context, app apps.App, domain string) error {
	if b.tasks == nil || app.DDNSTaskID == "" {
		return nil
	}
	tasks, err := b.tasks.List(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.ID != app.DDNSTaskID {
			continue
		}
		task.IPv4.Domains = removeDomain(task.IPv4.Domains, domain)
		task.IPv6.Domains = removeDomain(task.IPv6.Domains, domain)
		// 一个域名都不剩的任务什么也不做，留着只会让列表变脏。
		if len(task.IPv4.Domains) == 0 && len(task.IPv6.Domains) == 0 {
			return b.tasks.Delete(ctx, task.ID)
		}
		_, err := b.tasks.Update(ctx, task.ID, task)
		return err
	}
	return nil
}

func coversAny(task ddns.Task, domains []string) bool {
	for _, want := range domains {
		if containsHost(task.IPv4.Domains, want) || containsHost(task.IPv6.Domains, want) {
			return true
		}
	}
	return false
}

func missingDomains(task ddns.Task, domains []string) []string {
	out := make([]string, 0, len(domains))
	for _, want := range domains {
		if containsHost(task.IPv4.Domains, want) || containsHost(task.IPv6.Domains, want) {
			continue
		}
		out = append(out, want)
	}
	return out
}

func removeDomain(domains []string, drop string) []string {
	out := make([]string, 0, len(domains))
	for _, domain := range domains {
		if strings.EqualFold(strings.TrimSpace(domain), drop) {
			continue
		}
		out = append(out, domain)
	}
	return out
}

// EnsureRoute 保证 domain 指向该应用的本地端口。幂等。
func (b *appBinder) EnsureRoute(ctx context.Context, app apps.App, domain string) (string, error) {
	if b.manager == nil || b.routes == nil {
		return "", nil
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return "", nil
	}
	upstream := fmt.Sprintf("http://127.0.0.1:%d", app.LocalPort)

	routes, err := b.routes.List(ctx)
	if err != nil {
		return "", err
	}

	for index := range routes {
		if !containsHost(routes[index].Hosts, domain) {
			continue
		}
		// 已经有一条覆盖该域名的规则：把它对准本应用。
		// 端口可能因为重新分配而变过，所以上游也要跟着更新。
		changed := false
		if routes[index].Upstream != upstream {
			routes[index].Upstream = upstream
			changed = true
		}
		if !routes[index].TLS {
			// 站点要公网可访问，HTTPS 是默认预期。
			routes[index].TLS = true
			changed = true
		}
		if !changed {
			return routes[index].ID, nil
		}
		if err := b.manager.SaveRoutes(ctx, routes); err != nil {
			return "", err
		}
		return routes[index].ID, nil
	}

	id, err := newRouteID()
	if err != nil {
		return "", err
	}
	routes = append(routes, proxy.Route{
		ID:       id,
		Label:    app.Name,
		Hosts:    []string{domain},
		Upstream: upstream,
		TLS:      true,
	})
	if err := b.manager.SaveRoutes(ctx, routes); err != nil {
		return "", err
	}
	return id, nil
}

// RemoveRoute 撤销属于该应用的绑定。
func (b *appBinder) RemoveRoute(ctx context.Context, app apps.App, domain string) error {
	if b.manager == nil || b.routes == nil {
		return nil
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	routes, err := b.routes.List(ctx)
	if err != nil {
		return err
	}
	kept := make([]proxy.Route, 0, len(routes))
	removed := false
	for _, route := range routes {
		// 只删"同时覆盖该域名、且上游指向本应用端口"的规则：
		// 用户手工配的、指向别处的同名规则不归我们处置。
		owns := containsHost(route.Hosts, domain) &&
			route.Upstream == fmt.Sprintf("http://127.0.0.1:%d", app.LocalPort)
		if owns {
			removed = true
			continue
		}
		kept = append(kept, route)
	}
	if !removed {
		return nil
	}
	return b.manager.SaveRoutes(ctx, kept)
}

func containsHost(hosts []string, want string) bool {
	for _, host := range hosts {
		if strings.EqualFold(strings.TrimSpace(host), want) {
			return true
		}
	}
	return false
}

func newRouteID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
