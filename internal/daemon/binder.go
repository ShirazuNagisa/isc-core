package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/apps"
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
}

func newAppBinder(manager *proxy.Manager, log *slog.Logger) *appBinder {
	return &appBinder{manager: manager, routes: manager.RouteStore(), log: log}
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
