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
	"github.com/ShirazuNagisa/isc-core/internal/dns"
	"github.com/ShirazuNagisa/isc-core/internal/proxy"
	"github.com/ShirazuNagisa/isc-core/internal/tunnel"
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
	// tunnel 是 Cloudflare 隧道的管理器；为空表示这份内核没有隧道能力，
	// 此时解析维护完全走原来的动态解析那条路。
	tunnel *tunnel.Manager
	// zoneFinder 按域名反查"该用哪把凭据、哪个区域"。
	//
	// 隧道记录必须建在域名**所属**的区域里，而这件事完全由数据决定；
	// 让调用方猜一个区域，就是让它在没有依据的情况下做选择。
	zoneFinder *dns.ZoneFinder
	// dns 用来读写记录本身。
	dns *dns.Service

	// dnsCredential 返回"做 DNS 操作该用哪个凭据"。
	//
	// 用的是设置里的 ACME DNS 凭据：用户已经在那里指定过一次"这是我的
	// DNS 凭据"，再让他指定第二次没有道理。
	dnsCredential func(context.Context) string
}

func newAppBinder(
	manager *proxy.Manager,
	tasks *ddns.Service,
	dnsCredential func(context.Context) string,
	tunnel *tunnel.Manager,
	zoneFinder *dns.ZoneFinder,
	dnsSvc *dns.Service,
	log *slog.Logger,
) *appBinder {
	return &appBinder{
		manager: manager, routes: manager.RouteStore(),
		tasks: tasks, dnsCredential: dnsCredential, log: log,
		tunnel: tunnel, zoneFinder: zoneFinder, dns: dnsSvc,
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
	if len(app.Domains) == 0 {
		return "", nil
	}

	// 隧道就绪时**只**走隧道。
	//
	// 不并存：那条路维护的是 A/AAAA（"这台机器的地址"），而隧道模式下
	// 客户端连的是 Cloudflare 边缘，本机地址根本不被使用。两条一起跑会
	// 打架 —— 动态解析会把 CNAME 覆盖回 AAAA，于是域名指向一个外面连
	// 不上的地址，而界面上一切正常。
	//
	// 就绪而不是"开启了"：进程活着但连不上边缘时绑 CNAME 过去，用户
	// 得到的同样是一个打不开的域名，只是慢一步才发现。
	if b.tunnelReady() {
		var firstErr error
		for _, domain := range app.Domains {
			if err := b.ensureTunnelHost(ctx, domain); err != nil {
				b.log.Warn("could not point a domain at the tunnel",
					"app_id", app.ID, "domain", domain, "err", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		// 返回空任务 id：隧道模式下没有动态解析任务要维护。
		return "", firstErr
	}

	if b.tasks == nil {
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
	// 隧道模式下没有动态解析任务，但要撤掉指向隧道的那条记录 ——
	// 否则删掉站点之后域名还指过来，用户得到的是一片没有后端的 502，
	// 而"站点已经删了"这件事在 DNS 上看不出来。
	if b.tunnelReady() {
		if err := b.removeTunnelHost(ctx, domain); err != nil {
			b.log.Warn("could not remove the tunnel record for a domain",
				"app_id", app.ID, "domain", domain, "err", err)
			return err
		}
		return nil
	}
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

// tunnelReady 报告"域名应该指向隧道"。
//
// 抽成一个方法而不是在 EnsureDNS 里直接问：这个判断的**时机**本身是
// 设计决定（就绪而非开启），集中在一处才不会被下一处调用点写成别的。
func (b *appBinder) tunnelReady() bool {
	return b.tunnel != nil && b.tunnel.Ready()
}
