package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/remote"
	"github.com/ShirazuNagisa/isc-core/internal/settings"
)

// 本文件实现远程管理面（ISC Mizar）的路由与处理器。
//
// # 路由为什么不复制一份
//
// 远程面用的处理器与本地**完全是同一批**（`gen.HandlerFromMux` 注册的那 82
// 条）。远程面额外做的只有两件事：鉴权换成设备令牌、外面套一层白名单。
//
// 之所以不把白名单里的路由单独注册一遍：那样就有了两份路由表，而它们会
// 漂移。漂移的方向恰好是最坏的那个 —— 本地加了新接口，有人顺手也加进
// 远程表，于是新的管理能力在没有经过安全评审的情况下暴露到了局域网上。
// 这里改成白名单：**新增接口默认不在远程面上可见**，要显式加进来。
//
// # 白名单为什么放在 ServeMux 上
//
// `http.ServeMux` 的模式语法与生成代码用的是同一套（`GET /v1/apps/{id}`），
// 因此匹配语义天然一致。自己写一个路径匹配器意味着要重新实现通配段、
// 尾斜杠重定向等细节，而那些细节出错时表现为"某条路径意外放行"。

// remoteRoute 是远程面的一条白名单条目。
type remoteRoute struct {
	// pattern 是 Go 1.22+ 的 ServeMux 模式，必须与生成代码注册的**逐字相同**。
	pattern string
	// role 是访问它所需的最低角色。空串表示免鉴权。
	role remote.Role
}

// remoteRoutes 是远程面的完整可见面。
//
// 维护规则（重要）：这里只放"手机端确实需要"的路径。判断标准不是接口
// 看起来危险不危险，而是**用户在手机上做这件事是否合理**。例如
// `POST /v1/credentials` 会写入一个能重写整个 DNS 区域的凭据 ——
// 在手机上敲那一长串 API Key 本身就是小概率事件，因此它不在表里。
var remoteRoutes = []remoteRoute{
	// --- 免鉴权：两条 ---
	//
	// 它们必须存在（还没配对的客户端没有令牌可用），也必须被限流 ——
	// 限流在下面 remoteAuth 里做，因为那里才有来源地址。
	{"POST /v1/remote/pair", ""},
	// 探针端点：用户最需要"公网到底通不通"这个答案的时刻，
	// 恰好是还没配对成功的时候。要求鉴权就把那个顺序堵死了。
	{"GET /v1/remote/ping", ""},

	// --- 设备自述 ---
	//
	// viewer 就能调：`derive` 是手表拿只读令牌用的，而它只能派生
	// **不高于自己**的角色（服务端强制，见 remote.DeriveDevice）。
	{"GET /v1/remote/self", remote.RoleViewer},
	{"DELETE /v1/remote/self", remote.RoleViewer},
	{"POST /v1/remote/self/derive", remote.RoleViewer},
	{"POST /v1/remote/self/push-token", remote.RoleViewer},
	{"DELETE /v1/remote/self/push-token", remote.RoleViewer},

	// --- 只读 ---
	{"GET /v1/health", remote.RoleViewer},
	{"GET /v1/meta", remote.RoleViewer},
	{"GET /v1/metrics", remote.RoleViewer},
	{"GET /v1/advisories", remote.RoleViewer},
	{"GET /v1/ip/current", remote.RoleViewer},
	{"GET /v1/certs", remote.RoleViewer},
	{"GET /v1/proxy/status", remote.RoleViewer},
	{"GET /v1/apps", remote.RoleViewer},
	{"GET /v1/apps/{id}", remote.RoleViewer},
	{"GET /v1/apps/{id}/logs", remote.RoleViewer},
	// 凭据列表与详情只返回掩码字段，因此只读是安全的；
	// 手机需要它来做"用哪家服务商的哪份凭据"这个选择。
	{"GET /v1/credentials", remote.RoleViewer},
	{"GET /v1/credentials/{id}/zones", remote.RoleViewer},
	{"GET /v1/credentials/{id}/zones/{zoneId}/records", remote.RoleViewer},
	{"GET /v1/credentials/{id}/zones/{zoneId}/records/{recordId}", remote.RoleViewer},
	{"GET /v1/ddns-tasks", remote.RoleViewer},
	{"GET /v1/ddns-tasks/{id}", remote.RoleViewer},
	{"GET /v1/events/poll", remote.RoleViewer},

	// --- 写：DNS 记录 ---
	{"POST /v1/credentials/{id}/zones/{zoneId}/records", remote.RoleOperator},
	{"PUT /v1/credentials/{id}/zones/{zoneId}/records/{recordId}", remote.RoleOperator},
	{"DELETE /v1/credentials/{id}/zones/{zoneId}/records/{recordId}", remote.RoleOperator},

	// --- 写：动态解析 ---
	//
	// 刻意**没有** `PATCH /v1/ddns-tasks/{id}`：那个接口能改域名与取址
	// 来源，权限面远大于"启停"。手机要的只是开关，因此拆成两个窄接口。
	{"POST /v1/ddns-tasks/{id}/run", remote.RoleOperator},
	{"POST /v1/ddns-tasks/{id}/enable", remote.RoleOperator},
	{"POST /v1/ddns-tasks/{id}/disable", remote.RoleOperator},

	// --- 公网访问 ---
	//
	// 三条都在，而**没有**"开启公网访问"那一条。
	//
	// 那个区别是刻意的：公网访问会把内核暴露在互联网上，那个决定
	// 属于坐在机器前面的人。手机能做的是**减少**暴露（DELETE），
	// 以及在配置已经决定好之后让它生效（sync）与回报探测结果（check）。
	//
	// 同理没有 `PUT /v1/remote/apns`：那是一把能给用户**全部**设备
	// 发推送的凭据，装它的动作属于机器一侧。
	{"POST /v1/remote/public/check", remote.RoleOperator},
	{"POST /v1/remote/public/sync", remote.RoleOperator},
	{"DELETE /v1/remote/public", remote.RoleOperator},

	// --- 写：站点与证书 ---
	{"POST /v1/apps/{id}/start", remote.RoleOperator},
	{"POST /v1/apps/{id}/stop", remote.RoleOperator},
	{"POST /v1/apps/{id}/restart", remote.RoleOperator},
	{"POST /v1/certs/renew", remote.RoleOperator},
}

// RemoteRoutes 构造远程面的处理器。
//
// 中间件链与本地面对齐（RequestID → Language → Recover → LogRequests），
// 但**刻意替换掉最后两环**：
//
//   - `LoopbackGuard` 不装。它检查的是 `Host` 头是不是本机地址，
//     而远程请求的 Host 按定义就不是 —— 装了它会让每一个远程请求 403。
//     安全性在这里由设备令牌承担，而不是"请求来自本机"这个假设。
//
//   - `AuthMiddleware`（本地令牌）不装。本地令牌永远不出本机。
//
// 也不挂载 `/`、`/console/*`、`/v1/console/bootstrap`、`/console/i18n.json`：
// 那条引导路径**免鉴权就会交出令牌**，它在回环上安全是因为只有本机能连，
// 而在局域网上它等于把内核管理权交给任何人。
func (s *Server) RemoteRoutes() http.Handler {
	if s.Remote == nil {
		return http.NotFoundHandler()
	}

	// 真实路由：与本地同一批处理器。
	real := http.NewServeMux()
	gen.HandlerFromMux(s, real)

	policy := http.NewServeMux()
	for _, rt := range remoteRoutes {
		policy.Handle(rt.pattern, s.remoteGuard(rt, real))
	}
	// 兜底：没列进白名单的路径一律 403，而不是 404。
	//
	// 403 而不是 404：404 会让"这个接口存在但你没权限"与"这个接口根本
	// 不存在"变成同一个响应，而这两种情况对排查来说完全不同。
	policy.Handle("/", s.remoteForbidden())

	return Chain(policy,
		RequestID(),
		Language,
		Recover(s.Log),
		LogRequests(s.Log),
		func(next http.Handler) http.Handler {
			return s.remoteAuth(next)
		},
	)
}

// remoteForbidden 处理不在白名单里的路径。
func (s *Server) remoteForbidden() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, s.Log, http.StatusForbidden,
			CodeForbidden, "error.forbidden", i18n.FromContext(r.Context()).T("remote.api.path_forbidden"))
	})
}

// remoteGuard 校验角色后转交给真实路由。
func (s *Server) remoteGuard(rt remoteRoute, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rt.role == "" {
			next.ServeHTTP(w, r)
			return
		}
		device, ok := DeviceFromContext(r.Context())
		if !ok {
			// 走到这里说明中间件与白名单不一致（例如新增了一条
			// 免鉴权路径却忘了在 remoteAuth 里放行）。宁可 401 也不要
			// 放过去。
			writeProblem(w, r, s.Log, http.StatusUnauthorized,
				CodeUnauthorized, "error.unauthorized", i18n.FromContext(r.Context()).T("remote.api.token_invalid"))
			return
		}
		if !device.Role.AtLeast(rt.role) {
			writeProblem(w, r, s.Log, http.StatusForbidden,
				CodeForbidden, "error.forbidden", i18n.FromContext(r.Context()).T("remote.api.path_forbidden"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// deviceContextKey 是设备在请求 context 里的键。
type deviceContextKey struct{}

// remoteFaceKey 标记"这个请求来自远程监听"。
//
// 它与设备分开：配对请求**没有**设备（还没签出来），但它同样来自远程，
// 而审计里"谁在什么时候试图配对"恰恰是最需要留下的一条。
type remoteFaceKey struct{}

// DeviceFromContext 取出当前请求所属的远程设备。
func DeviceFromContext(ctx context.Context) (remote.Device, bool) {
	d, ok := ctx.Value(deviceContextKey{}).(remote.Device)
	return d, ok
}

// onRemoteFace 报告这个请求是否来自远程监听。
func onRemoteFace(ctx context.Context) bool {
	on, _ := ctx.Value(remoteFaceKey{}).(bool)
	return on
}

// remoteAuth 是远程面的鉴权中间件。
func (s *Server) remoteAuth(next http.Handler) http.Handler {
	svc := s.Remote
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 标记来源：审计要靠它区分"本机点的"与"局域网上某台机器点的"。
		r = r.WithContext(context.WithValue(r.Context(), remoteFaceKey{}, true))

		// 免鉴权的两条路径仍然要限流 —— 它们是任何人都能打的。
		//
		// 两条用**各自**的额度：共用一个桶时，攻击者狂打探针就能把
		// 正常用户的配对额度耗光，而那是一条不用配对就能发动的
		// 拒绝服务。
		if isPublicRemotePath(r) {
			allowed := svc.AllowPair(clientIP(r), time.Now())
			if r.URL.Path == remote.PublicProbePath {
				allowed = svc.AllowPing(clientIP(r), time.Now())
			}
			if !allowed {
				writeProblem(w, r, s.Log, http.StatusTooManyRequests,
					CodeForbidden, "error.forbidden", i18n.FromContext(r.Context()).T("remote.api.rate_limited"))
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		token := bearerToken(r)
		device, err := svc.Authenticate(r.Context(), token)
		if err != nil {
			s.remoteAuthFailure(w, r, err)
			return
		}

		if !svc.AllowRequest(device.ID, time.Now()) {
			writeProblem(w, r, s.Log, http.StatusTooManyRequests,
				CodeForbidden, "error.forbidden", i18n.FromContext(r.Context()).T("remote.api.rate_limited"))
			return
		}

		// 记录最后访问时间。节流到一分钟一次：那是一个展示字段，
		// 而每一次请求都写一次库会让 5 秒一次的指标轮询变成写放大。
		if time.Since(device.LastSeenAt) > time.Minute {
			svc.TouchDevice(r.Context(), device.ID, time.Now(), clientIP(r))
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), deviceContextKey{}, device)))
	})
}

// remoteAuthFailure 把鉴权失败分成"不认识这个令牌"与"设备已被吊销"。
//
// 对客户端来说两者的下一步动作不同：前者是"这串东西不是我们的"
// （可能扫错了服务器），后者是"你被管理员断开了，去重新配对"。
// 客户端要能把它们显示成不同的话，因此 detail 必须分开。
func (s *Server) remoteAuthFailure(w http.ResponseWriter, r *http.Request, err error) {
	detail := i18n.FromContext(r.Context()).T("remote.api.token_invalid")
	if errors.Is(err, remote.ErrDeviceRevoked) {
		detail = i18n.FromContext(r.Context()).T("remote.api.token_revoked")
	}
	writeProblem(w, r, s.Log, http.StatusUnauthorized, CodeUnauthorized, "error.unauthorized", detail)
}

// isPublicRemotePath 报告一条路径在远程面上是否免鉴权。
// isPublicRemotePath 报告这条路径是否**不需要设备令牌**。
//
// 两条：
//
//   - `POST /v1/remote/pair`：配对本身，它用的是一次性的六位码/密钥。
//   - `GET /v1/remote/ping`：探针端点。用户最需要"公网到底通不通"
//     这个答案的时刻，恰好是**还没配对成功**的时候（想确认能不能
//     连上，然后扫码）。要求鉴权就把这个顺序堵死了。
//
// 两条都**只挂限流、不挂鉴权**，而 ping 返回的东西刻意只有
// "到了"这一个事实 —— 身份由 TLS 证明。
func isPublicRemotePath(r *http.Request) bool {
	if r.Method == http.MethodPost && r.URL.Path == "/v1/remote/pair" {
		return true
	}
	return r.Method == http.MethodGet && r.URL.Path == remote.PublicProbePath
}

// bearerToken 从 Authorization 头取出 Bearer 令牌。
//
// 刻意**不支持**查询串或子协议（本地面为浏览器 WebSocket 留的那条路）：
// 浏览器永远不会是本服务的合法客户端，而多一种令牌载体就多一处可能被
// 写进日志、Referer 与历史记录的地方。
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// clientIP 返回请求的来源地址（不含端口）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------------------------------------------------------------------------
// 本地面：状态与设置
// ---------------------------------------------------------------------------

// GetRemoteStatus 实现 GET /v1/remote/status。
func (s *Server) GetRemoteStatus(w http.ResponseWriter, r *http.Request) {
	if s.Remote == nil {
		writeJSON(w, s.Log, http.StatusOK, "application/json", gen.RemoteStatus{
			State:   gen.RemoteState(remote.StateDisabled),
			Enabled: false,
			Port:    remote.DefaultPort,
		})
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenRemoteStatus(s.Remote.Status(r.Context())))
}

// UpdateRemoteSettings 实现 PATCH /v1/remote/settings。
func (s *Server) UpdateRemoteSettings(w http.ResponseWriter, r *http.Request) {
	if s.Remote == nil || s.Settings == nil {
		s.internalError(w, r, i18n.T("api.provider_missing"), errors.New(i18n.T("api.remote_unwired")))
		return
	}

	var patch gen.RemoteSettingsPatch
	if !decodeBody(w, r, s, &patch) {
		return
	}

	// 先落设置再起监听。顺序不能反：Apply 会去绑端口，而绑定可能失败；
	// 如果先绑成功再落库时校验失败（例如与反代端口冲突），
	// 我们就会留下一个"正在监听但设置里没记录"的状态。
	//
	// 反过来说，设置已经落库而监听没起来（端口被占）是**可接受的**：
	// 那正是用户想看到的状态 —— 他的选择被记住了，失败原因显示在界面上。
	st, err := s.Settings.Update(r.Context(), settings.Patch{
		RemoteEnabled:       patch.Enabled,
		RemotePort:          patch.Port,
		RemoteNotifications: patch.NotificationsEnabled,
	})
	if err != nil {
		s.auditFailure(r, audit.ActionRemoteSettings, "", err)
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return
	}

	s.Remote.SetNotificationsEnabled(st.RemoteNotifications)
	if err := s.Remote.Apply(r.Context(), st.RemoteEnabled, st.RemotePort); err != nil {
		// 监听起不来不是"请求有问题"：设置已经生效，失败原因会由
		// status 里的 last_error 暴露给界面。这里只记一笔日志。
		s.Log.Warn(i18n.T("remote.msg.failed"), "err", err)
	}

	s.auditSuccess(r, audit.ActionRemoteSettings, "",
		i18n.T("api.remote_settings_changed", enabledWord(st.RemoteEnabled), st.RemotePort))

	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenRemoteStatus(s.Remote.Status(r.Context())))
}

// ---------------------------------------------------------------------------
// 本地面：配对
// ---------------------------------------------------------------------------

// StartRemotePairing 实现 POST /v1/remote/pairing。
func (s *Server) StartRemotePairing(w http.ResponseWriter, r *http.Request) {
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}

	var req gen.PairingRequest
	if r.ContentLength > 0 && !decodeBody(w, r, s, &req) {
		return
	}

	role := remote.RoleViewer
	if req.Role != nil && remote.Role(*req.Role).Valid() {
		role = remote.Role(*req.Role)
	}
	label := ""
	if req.Label != nil {
		label = strings.TrimSpace(*req.Label)
	}

	session, err := s.Remote.BeginPairing(role, label, clientIP(r), time.Now())
	switch {
	case errors.Is(err, remote.ErrPairingConflict):
		writeProblem(w, r, s.Log, http.StatusConflict, CodeConflict, "error.conflict",
			i18n.FromContext(r.Context()).T("remote.api.pairing_conflict"))
		return
	case errors.Is(err, remote.ErrPairingLocked):
		writeProblem(w, r, s.Log, http.StatusTooManyRequests, CodeForbidden, "error.forbidden",
			i18n.FromContext(r.Context()).T("remote.api.pairing_locked"))
		return
	case err != nil:
		s.internalError(w, r, i18n.T("api.remote_pairing_failed"), err)
		return
	}

	out, err := s.pairingSessionToGen(session)
	if err != nil {
		s.internalError(w, r, i18n.T("api.remote_pairing_failed"), err)
		return
	}

	s.auditSuccess(r, audit.ActionRemotePair, session.ID,
		i18n.T("api.remote_pairing_started", string(role)))
	writeJSON(w, s.Log, http.StatusCreated, "application/json", out)
}

// CancelRemotePairing 实现 DELETE /v1/remote/pairing/{id}。
func (s *Server) CancelRemotePairing(w http.ResponseWriter, r *http.Request, id gen.PairingId) {
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}
	if !s.Remote.CancelPairing(string(id)) {
		writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found",
			i18n.FromContext(r.Context()).T("remote.api.pairing_none"))
		return
	}
	s.auditSuccess(r, audit.ActionRemotePair, string(id), i18n.T("api.remote_pairing_canceled"))
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 本地面：设备管理
// ---------------------------------------------------------------------------

// ListRemoteDevices 实现 GET /v1/remote/devices。
func (s *Server) ListRemoteDevices(w http.ResponseWriter, r *http.Request) {
	if s.Remote == nil {
		writeJSON(w, s.Log, http.StatusOK, "application/json", gen.RemoteDeviceList{Items: []gen.RemoteDevice{}})
		return
	}
	devices, err := s.Remote.ListDevices(r.Context())
	if err != nil {
		s.internalError(w, r, i18n.T("api.remote_devices_failed"), err)
		return
	}
	items := make([]gen.RemoteDevice, 0, len(devices))
	for _, d := range devices {
		items = append(items, toGenRemoteDevice(d))
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.RemoteDeviceList{Items: items})
}

// UpdateRemoteDevice 实现 PATCH /v1/remote/devices/{id}。
func (s *Server) UpdateRemoteDevice(w http.ResponseWriter, r *http.Request, id gen.RemoteDeviceId) {
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}

	var patch gen.RemoteDevicePatch
	if !decodeBody(w, r, s, &patch) {
		return
	}

	in := remote.DevicePatch{Label: patch.Label, NotificationsEnabled: patch.NotificationsEnabled}
	if patch.Role != nil {
		role := remote.Role(*patch.Role)
		in.Role = &role
	}

	device, err := s.Remote.UpdateDevice(r.Context(), string(id), in)
	if err != nil {
		s.remoteDeviceError(w, r, err)
		return
	}

	s.auditSuccess(r, audit.ActionRemoteDeviceUpdate, device.ID, device.Label)
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenRemoteDevice(device))
}

// RevokeRemoteDevice 实现 DELETE /v1/remote/devices/{id}。
func (s *Server) RevokeRemoteDevice(w http.ResponseWriter, r *http.Request, id gen.RemoteDeviceId) {
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}

	n, err := s.Remote.RevokeDevice(r.Context(), string(id), time.Now())
	if err != nil {
		s.remoteDeviceError(w, r, err)
		return
	}
	if n == 0 {
		writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found",
			i18n.FromContext(r.Context()).T("remote.api.device_not_found"))
		return
	}

	// 记下受影响的台数：级联吊销之后"到底断开了几台"是排查丢手机那件事
	// 时第一个会问的问题，而它无法从别的地方推出来。
	s.auditSuccess(r, audit.ActionRemoteRevoke, string(id),
		i18n.T("api.remote_revoked", n))
	w.WriteHeader(http.StatusNoContent)
}

// remoteDeviceError 统一设备操作错误。
func (s *Server) remoteDeviceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, remote.ErrDeviceNotFound):
		writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found",
			i18n.FromContext(r.Context()).T("remote.api.device_not_found"))
	case errors.Is(err, remote.ErrDeviceRevoked):
		writeProblem(w, r, s.Log, http.StatusConflict, CodeConflict, "error.conflict",
			i18n.FromContext(r.Context()).T("remote.api.token_revoked"))
	default:
		s.internalError(w, r, i18n.T("api.remote_devices_failed"), err)
	}
}

// ---------------------------------------------------------------------------
// 远程面：配对与自述
// ---------------------------------------------------------------------------

// CompleteRemotePairing 实现 POST /v1/remote/pair（远程面，免鉴权）。
func (s *Server) CompleteRemotePairing(w http.ResponseWriter, r *http.Request) {
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}

	var req gen.RemotePairRequest
	if !decodeBody(w, r, s, &req) {
		return
	}

	// **只认密钥。**
	//
	// 以前这里还接受一个六位码（`req.Code`），那条路径已删除：
	// 六位码只有约 10 亿种可能，靠按来源锁定兜底，而且携带不了任何
	// 身份信息。二维码与配对链接两条路都带高熵密钥，也自带公钥指纹。
	credential := ""
	if req.Secret != nil {
		credential = *req.Secret
	}
	if credential == "" {
		writeProblem(w, r, s.Log, http.StatusBadRequest, CodeInvalidRequest, "error.invalid_request",
			i18n.FromContext(r.Context()).T("remote.api.credential_missing"))
		return
	}

	now := time.Now()
	session, err := s.Remote.ClaimPairing(credential, clientIP(r), now)
	if err != nil {
		detail := i18n.FromContext(r.Context()).T("remote.api.pairing_mismatch")
		status := http.StatusUnauthorized
		if errors.Is(err, remote.ErrPairingLocked) {
			detail = i18n.FromContext(r.Context()).T("remote.api.pairing_locked")
			status = http.StatusTooManyRequests
		}
		// 失败也要留痕：配对是"把一台新设备放进内核"的动作，
		// 一次成功之前的所有失败尝试恰恰是排查时最想看到的东西。
		s.auditFailure(r, audit.ActionRemotePairFailed, clientIP(r), errors.New(detail))
		writeProblem(w, r, s.Log, status, CodeUnauthorized, "error.unauthorized", detail)
		return
	}

	device, token, err := s.Remote.IssueDevice(r.Context(), session, deviceInfoFromGen(req.Device), now)
	if err != nil {
		s.internalError(w, r, i18n.T("api.remote_pairing_failed"), err)
		return
	}

	s.auditSuccess(r, audit.ActionRemotePair, device.ID,
		i18n.T("api.remote_paired", device.Label, string(device.Role)))

	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.RemotePairResult{
		DeviceId: device.ID,
		Token:    token,
		Role:     gen.RemoteRole(device.Role),
		Server:   s.remoteServerInfo(),
	})
}

// GetRemoteSelf 实现 GET /v1/remote/self。
func (s *Server) GetRemoteSelf(w http.ResponseWriter, r *http.Request) {
	device, ok := DeviceFromContext(r.Context())
	if !ok {
		writeProblem(w, r, s.Log, http.StatusUnauthorized, CodeUnauthorized, "error.unauthorized",
			i18n.FromContext(r.Context()).T("remote.api.token_invalid"))
		return
	}
	// 探测计划随自述一起下发：手机本来就每次连接都取一次
	// `/v1/remote/self`，把它挂在这里意味着不需要多一次往返，
	// 也意味着手机随时都拿着最新的公网地址（地址会变）。
	var probe *gen.RemotePublicProbe
	if s.Remote != nil {
		plan := s.Remote.PublicProbe()
		converted := toGenPublicProbe(plan)
		probe = &converted
	}

	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.RemoteSelf{
		Device: toGenRemoteDevice(device),
		Role:   gen.RemoteRole(device.Role),
		Server: s.remoteServerInfo(),
		Public: probe,
	})
}

// UnpairRemoteSelf 实现 DELETE /v1/remote/self。
func (s *Server) UnpairRemoteSelf(w http.ResponseWriter, r *http.Request) {
	device, ok := DeviceFromContext(r.Context())
	if !ok {
		writeProblem(w, r, s.Log, http.StatusUnauthorized, CodeUnauthorized, "error.unauthorized",
			i18n.FromContext(r.Context()).T("remote.api.token_invalid"))
		return
	}
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}
	if _, err := s.Remote.RevokeDevice(r.Context(), device.ID, time.Now()); err != nil {
		s.remoteDeviceError(w, r, err)
		return
	}
	s.auditSuccess(r, audit.ActionRemoteUnpair, device.ID, device.Label)
	w.WriteHeader(http.StatusNoContent)
}

// DeriveRemoteDevice 实现 POST /v1/remote/self/derive。
func (s *Server) DeriveRemoteDevice(w http.ResponseWriter, r *http.Request) {
	parent, ok := DeviceFromContext(r.Context())
	if !ok {
		writeProblem(w, r, s.Log, http.StatusUnauthorized, CodeUnauthorized, "error.unauthorized",
			i18n.FromContext(r.Context()).T("remote.api.token_invalid"))
		return
	}
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}

	var req gen.RemoteDeriveRequest
	if !decodeBody(w, r, s, &req) {
		return
	}

	label := ""
	if req.Label != nil {
		label = strings.TrimSpace(*req.Label)
	}
	info := remote.DeviceInfo{Name: label}
	if req.Device != nil {
		info = deviceInfoFromGen(*req.Device)
		if label == "" {
			label = info.Name
		}
	}

	device, token, err := s.Remote.DeriveDevice(
		r.Context(), parent, remote.Role(req.Role), label, info, time.Now())
	if err != nil {
		// 角色越权在这里是 403 而不是 400：请求本身是合法的，
		// 是这台设备没有那个权限。
		if strings.Contains(err.Error(), i18n.T("remote.err.role_escalation")) {
			writeProblem(w, r, s.Log, http.StatusForbidden, CodeForbidden, "error.forbidden",
				i18n.FromContext(r.Context()).T("remote.api.role_escalation"))
			return
		}
		writeProblem(w, r, s.Log, http.StatusBadRequest, CodeInvalidRequest, "error.invalid_request",
			i18n.FromContext(r.Context()).T("remote.api.role_invalid"))
		return
	}

	s.auditSuccess(r, audit.ActionRemoteDerive, device.ID,
		i18n.T("api.remote_derived", device.Label, string(device.Role), parent.Label))

	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.RemotePairResult{
		DeviceId: device.ID,
		Token:    token,
		Role:     gen.RemoteRole(device.Role),
		Server:   s.remoteServerInfo(),
	})
}

// ---------------------------------------------------------------------------
// 本地面：动态解析启停
// ---------------------------------------------------------------------------

// EnableDdnsTask 实现 POST /v1/ddns-tasks/{id}/enable。
func (s *Server) EnableDdnsTask(w http.ResponseWriter, r *http.Request, id gen.DdnsTaskId) {
	s.setDdnsEnabled(w, r, id, true)
}

// DisableDdnsTask 实现 POST /v1/ddns-tasks/{id}/disable。
func (s *Server) DisableDdnsTask(w http.ResponseWriter, r *http.Request, id gen.DdnsTaskId) {
	s.setDdnsEnabled(w, r, id, false)
}

// setDdnsEnabled 实现启停。
//
// 走"取出来改一个字段再整体写回"而不是新增一条存储路径：任务的其他字段
// 由 Update 统一校验与归一化（域名去重、防抖缓存重置），绕过它就会
// 得到一条"改了开关但域名没归一化"的任务。
func (s *Server) setDdnsEnabled(w http.ResponseWriter, r *http.Request, id gen.DdnsTaskId, enabled bool) {
	if s.Tasks == nil {
		s.internalError(w, r, i18n.T("api.verify.no_service"), errors.New(i18n.T("api.remote_unwired")))
		return
	}

	task, err := s.Tasks.Get(r.Context(), string(id))
	if err != nil {
		s.taskError(w, r, err, string(id))
		return
	}

	if task.Enabled == enabled {
		// 幂等：已经是目标状态就直接返回。不这么做会让一次多余的
		// 点击触发一次到服务商的真实请求（Update 会 ResetCache 并 trigger）。
		writeJSON(w, s.Log, http.StatusOK, "application/json", toGenDdnsTask(task))
		return
	}

	task.Enabled = enabled
	updated, err := s.Tasks.Update(r.Context(), string(id), task)
	if err != nil {
		s.auditFailure(r, audit.ActionTaskUpdate, string(id), err)
		s.taskError(w, r, err, string(id))
		return
	}

	s.auditSuccess(r, audit.ActionTaskUpdate, updated.ID, updated.Label)
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenDdnsTask(updated))
}

// ---------------------------------------------------------------------------
// 远程面：推送令牌
// ---------------------------------------------------------------------------

// RegisterRemotePushToken 实现 POST /v1/remote/self/push-token。
//
// 推送的**发送**侧（凭据、JWT、HTTP/2 到 APNs）是独立的一块，见
// internal/remote 的推送文件；这里只负责把设备令牌记下来。
func (s *Server) RegisterRemotePushToken(w http.ResponseWriter, r *http.Request) {
	device, ok := DeviceFromContext(r.Context())
	if !ok {
		writeProblem(w, r, s.Log, http.StatusUnauthorized, CodeUnauthorized, "error.unauthorized",
			i18n.FromContext(r.Context()).T("remote.api.token_invalid"))
		return
	}
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}

	var req gen.RemotePushTokenRequest
	if !decodeBody(w, r, s, &req) {
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeProblem(w, r, s.Log, http.StatusBadRequest, CodeInvalidRequest, "error.invalid_request",
			i18n.FromContext(r.Context()).T("remote.api.device_missing"))
		return
	}

	env := remote.PushEnvProduction
	if req.Environment != nil && *req.Environment == gen.Sandbox {
		env = remote.PushEnvSandbox
	}
	topic := s.Remote.APNSTopic()
	if req.Topic != nil && *req.Topic != "" {
		topic = *req.Topic
	}

	on := true
	if _, err := s.Remote.UpdateDevice(r.Context(), device.ID, remote.DevicePatch{
		NotificationsEnabled: &on,
	}); err != nil {
		s.remoteDeviceError(w, r, err)
		return
	}
	if err := s.Remote.SetPushToken(r.Context(), device.ID, token, env, topic); err != nil {
		s.internalError(w, r, i18n.T("api.remote_devices_failed"), err)
		return
	}

	// 推送令牌本身是敏感值（拿到它就能给这台设备发通知），因此审计里
	// **不写令牌**，只写"登记了哪个环境的令牌"。
	s.auditSuccess(r, audit.ActionRemoteDeviceUpdate, device.ID,
		i18n.T("api.remote_push_registered", string(env)))
	w.WriteHeader(http.StatusNoContent)
}

// UnregisterRemotePushToken 实现 DELETE /v1/remote/self/push-token。
func (s *Server) UnregisterRemotePushToken(w http.ResponseWriter, r *http.Request) {
	device, ok := DeviceFromContext(r.Context())
	if !ok {
		writeProblem(w, r, s.Log, http.StatusUnauthorized, CodeUnauthorized, "error.unauthorized",
			i18n.FromContext(r.Context()).T("remote.api.token_invalid"))
		return
	}
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}

	off := false
	if _, err := s.Remote.UpdateDevice(r.Context(), device.ID, remote.DevicePatch{
		NotificationsEnabled: &off,
	}); err != nil {
		s.remoteDeviceError(w, r, err)
		return
	}
	if err := s.Remote.SetPushToken(r.Context(), device.ID, "", "", ""); err != nil {
		s.internalError(w, r, i18n.T("api.remote_devices_failed"), err)
		return
	}

	s.auditSuccess(r, audit.ActionRemoteDeviceUpdate, device.ID, i18n.T("api.remote_push_unregistered"))
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 本地面：APNs 凭据
// ---------------------------------------------------------------------------

// SetRemoteApns 实现 PUT /v1/remote/apns。
//
// 凭据被加密之后落在数据目录里（见 internal/remote 的说明），
// 而**私钥永不回显** —— 它出现在任何响应或日志里都是缺陷。
func (s *Server) SetRemoteApns(w http.ResponseWriter, r *http.Request) {
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}

	var body gen.ApnsCredentials
	if !decodeBody(w, r, s, &body) {
		return
	}
	if strings.TrimSpace(body.TeamId) == "" || strings.TrimSpace(body.KeyId) == "" ||
		strings.TrimSpace(body.BundleId) == "" || strings.TrimSpace(body.PrivateKey) == "" {
		writeProblem(w, r, s.Log, http.StatusBadRequest, CodeInvalidRequest, "error.invalid_request",
			i18n.FromContext(r.Context()).T("remote.api.apns_incomplete"))
		return
	}

	err := s.Remote.SetAPNSCredentials(remote.Credentials{
		TeamID:     strings.TrimSpace(body.TeamId),
		KeyID:      strings.TrimSpace(body.KeyId),
		BundleID:   strings.TrimSpace(body.BundleId),
		PrivateKey: body.PrivateKey,
	})
	if err != nil {
		// 私钥解析失败是**用户输入的问题**，不是内核内部错误 ——
		// 报 500 会让用户以为是自己点错了地方。
		writeProblem(w, r, s.Log, http.StatusBadRequest, CodeInvalidRequest, "error.invalid_request",
			err.Error())
		return
	}

	// 审计里**不写任何凭据字段**，只写"改过 APNs 配置"。
	s.auditSuccess(r, audit.ActionRemoteApns, "", i18n.T("api.remote_apns_set"))
	writeJSON(w, s.Log, http.StatusOK, "application/json", s.apnsStatus())
}

// DeleteRemoteApns 实现 DELETE /v1/remote/apns。
func (s *Server) DeleteRemoteApns(w http.ResponseWriter, r *http.Request) {
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}
	if err := s.Remote.DeleteAPNSCredentials(); err != nil {
		s.internalError(w, r, i18n.T("api.remote_devices_failed"), err)
		return
	}
	s.auditSuccess(r, audit.ActionRemoteApns, "", i18n.T("api.remote_apns_cleared"))
	w.WriteHeader(http.StatusNoContent)
}

// TestRemotePush 实现 POST /v1/remote/devices/{id}/test-push。
func (s *Server) TestRemotePush(w http.ResponseWriter, r *http.Request, id gen.RemoteDeviceId) {
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}

	delivery, err := s.Remote.TestPush(r.Context(), string(id))
	if err != nil {
		if errors.Is(err, remote.ErrDeviceNotFound) {
			s.remoteDeviceError(w, r, err)
			return
		}
		writeProblem(w, r, s.Log, http.StatusBadRequest, CodeInvalidRequest, "error.invalid_request",
			err.Error())
		return
	}

	// 200 而不是 4xx/5xx：**发送失败是一个结果，不是一次接口错误**。
	// 界面要显示的是 Apple 给的那句话（"BadDeviceToken"、"TopicDisallowed"…），
	// 而那正是用户排查时唯一有用的东西。
	message := i18n.T("api.remote_push_sent")
	ok := delivery.Status == remote.PushStatusSent
	if !ok {
		message = delivery.Reason
		if message == "" {
			message = i18n.T("api.remote_push_failed", delivery.HTTPStatus)
		}
	}
	s.auditSuccess(r, audit.ActionRemoteDeviceUpdate, string(id),
		i18n.T("api.remote_push_test", delivery.Status))
	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.ServiceActionResult{
		Ok:      ok,
		Message: &message,
	})
}

// apnsStatus 组装 APNs 的对外状态（不含私钥）。
func (s *Server) apnsStatus() gen.ApnsStatus {
	out := gen.ApnsStatus{}
	if s.Remote == nil {
		return out
	}
	configured, teamID, keyID, bundleID, err := s.Remote.APNSStatus()
	if err != nil {
		return out
	}
	out.Configured = configured
	out.TeamId = strPtr(teamID)
	out.KeyId = strPtr(keyID)
	out.BundleId = strPtr(bundleID)
	return out
}

// PingRemoteFace 实现 GET /v1/remote/ping。
//
// # 它为什么必须免鉴权
//
// 用户最需要"公网到底通不通"这个答案的时刻，恰好是**还没配对成功**
// 的时候 —— 想确认能不能连上，然后扫码。要求鉴权就把这个顺序堵死了。
//
// # 它为什么只回一个 ok
//
// 身份由 TLS 证明：公网路径是受信任证书（域名对得上），局域网路径是
// 固定公钥。这里多说一个字都是多余的暴露面 —— 这是一个任何人都能
// 打的端点。
func (s *Server) PingRemoteFace(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Log, http.StatusOK, "application/json", map[string]bool{"ok": true})
}

// ReportRemotePublicCheck 实现 POST /v1/remote/public/check。
func (s *Server) ReportRemotePublicCheck(w http.ResponseWriter, r *http.Request) {
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}

	var body gen.RemotePublicCheckReport
	if !decodeBody(w, r, s, &body) {
		return
	}
	family := remote.NormalizeReportedFamily(string(body.Family))
	if family == "" {
		writeProblem(w, r, s.Log, http.StatusBadRequest, CodeInvalidRequest,
			"error.invalid_request", i18n.FromContext(r.Context()).T("api.remote_public_bad_family"))
		return
	}

	// 手机在自家 Wi-Fi 上时，连公网地址**也会成功** —— 那个连接根本
	// 没出局域网。把它当成"公网可达"是最坏的一种结论：用户会以为
	// 已经验证过，而其实什么都没验证。
	sameLAN := body.SameLan != nil && *body.SameLan

	verdict := remote.PublicVerdictUnreachable
	detail := ""
	if body.Detail != nil {
		detail = *body.Detail
	}
	switch {
	case sameLAN:
		verdict = remote.PublicVerdictUnknown
		if detail == "" {
			detail = i18n.FromContext(r.Context()).T("api.remote_public_same_lan")
		}
	case body.Reachable:
		verdict = remote.PublicVerdictReachable
		if detail == "" {
			detail = i18n.FromContext(r.Context()).T("api.remote_public_reachable")
		}
	case detail == "":
		detail = i18n.FromContext(r.Context()).T("api.remote_public_unreachable")
	}

	if err := s.Remote.RecordPublicCheck(remote.PublicCheck{
		Verdict: verdict, Family: family, Detail: detail,
	}); err != nil {
		s.internalError(w, r, i18n.T("api.remote_public_check_failed"), err)
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", s.Remote.Status(r.Context()).Public)
}

// ListRemotePublicDomains 实现 GET /v1/remote/public/domains。
//
// 供界面把"挂在哪个域名下"做成一个**列表**而不是一个输入框：
// 用户不需要记住自己的区域名，更不该把它打错 —— 打错的后果是
// 找不到区域，或者更糟：在一个同名但不同账号的区域下建记录。
func (s *Server) ListRemotePublicDomains(w http.ResponseWriter, r *http.Request) {
	type item struct {
		Domain   string `json:"domain"`
		Provider string `json:"provider"`
	}
	out := make([]item, 0)
	if s.PublicDomains != nil {
		domains, err := s.PublicDomains(r.Context())
		if err != nil {
			// 列不出来不是"没有域名"——把原因带出去，否则界面会显示
			// 一个空列表，而用户以为是自己没配 DNS。
			writeProblem(w, r, s.Log, http.StatusBadRequest, CodeInvalidRequest,
				"error.invalid_request", err.Error())
			return
		}
		for _, d := range domains {
			out = append(out, item{Domain: d.Domain, Provider: d.Provider})
		}
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", out)
}

// SyncRemotePublic 实现 POST /v1/remote/public/sync。
func (s *Server) SyncRemotePublic(w http.ResponseWriter, r *http.Request) {
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}
	if _, err := s.Remote.SyncPublic(r.Context()); err != nil {
		// 同步失败的原因几乎都是**用户能自己修好的**：凭据不对、
		// 区域选错、本机没有公网 IPv6。因此回 400 并把原话带出去，
		// 而不是压成一句"内部错误"。
		writeProblem(w, r, s.Log, http.StatusBadRequest, CodeInvalidRequest,
			"error.invalid_request", err.Error())
		return
	}
	s.auditSuccess(r, audit.ActionRemotePublic, "", i18n.T("api.remote_public_synced"))
	writeJSON(w, s.Log, http.StatusOK, "application/json", s.Remote.Status(r.Context()))
}

// DeleteRemotePublic 实现 DELETE /v1/remote/public。
//
// 只删内核自己建的那几条记录（按记录 ID）。
func (s *Server) DeleteRemotePublic(w http.ResponseWriter, r *http.Request) {
	if s.Remote == nil {
		s.remoteUnavailable(w, r)
		return
	}
	if err := s.Remote.TeardownPublic(r.Context()); err != nil {
		// 删除失败**不是**"什么都没做"：台账已经清空，但区域里可能
		// 留下一条记录。让界面能据此提示用户去 DNS 后台确认。
		s.internalError(w, r, i18n.T("api.remote_public_teardown_failed"), err)
		return
	}
	s.auditSuccess(r, audit.ActionRemotePublic, "", i18n.T("api.remote_public_removed"))
	w.WriteHeader(http.StatusNoContent)
}

// remoteUnavailable 表示远程面的某一部分尚未接入。
func (s *Server) remoteUnavailable(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, s.Log, http.StatusNotImplemented,
		CodeUnsupported, "error.not_implemented", i18n.FromContext(r.Context()).T("remote.api.push_not_wired"))
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// remoteServerInfo 组装给客户端看的服务端信息。
func (s *Server) remoteServerInfo() gen.RemoteServerInfo {
	info := gen.RemoteServerInfo{
		Name:       s.Remote.Name(),
		Version:    s.Remote.Version(),
		ApiVersion: s.Remote.APIVersion(),
	}
	cert := s.Remote.Certificate()
	spki, short := cert.SPKIBase64(), cert.FingerprintShort()
	info.SpkiSha256 = &spki
	info.FingerprintShort = &short
	if topic := s.Remote.APNSTopic(); topic != "" {
		info.ApnsTopic = &topic
	}
	// 推送开关而不是"远程访问开关"：这个字段回答的是
	// "服务端会不会主动给我推通知"，而客户端据此决定要不要去注册
	// APNs 令牌。取错源头会让它在没配 APNs 时也显示 true。
	notifications := s.Remote.NotificationsEnabled()
	info.NotificationsEnabled = &notifications
	return info
}

// pairingSessionToGen 把配对会话转成契约类型，并生成二维码内容。
func (s *Server) pairingSessionToGen(session remote.PairingSession) (gen.PairingSession, error) {
	payload, err := s.Remote.QRPayload(session)
	if err != nil {
		return gen.PairingSession{}, err
	}
	// 链接与二维码用的是**同一份**载荷，只是换了个载体。
	link, err := s.Remote.QRLink(session)
	if err != nil {
		return gen.PairingSession{}, err
	}
	out := gen.PairingSession{
		Id:               session.ID,
		Role:             gen.RemoteRole(session.Role),
		FingerprintShort: s.Remote.Certificate().FingerprintShort(),
		SpkiSha256:       s.Remote.Certificate().SPKIBase64(),
		Addresses:        remote.Candidates(s.Remote.Port()),
		ExpiresAt:        session.ExpiresAt,
		QrPayload:        payload,
		QrLink:           link,
	}
	if session.Label != "" {
		label := session.Label
		out.Label = &label
	}
	return out, nil
}

// toGenRemoteStatus 转换状态。
func toGenRemoteStatus(st remote.Status) gen.RemoteStatus {
	out := gen.RemoteStatus{
		State:                gen.RemoteState(st.State),
		Enabled:              st.Enabled,
		Port:                 st.Port,
		DeviceCount:          st.DeviceCount,
		NotificationsEnabled: st.NotificationsEnabled,
	}
	listening := st.Listening
	out.Listening = &listening
	addresses := st.Addresses
	out.Addresses = &addresses
	out.Hostname = strPtr(st.Hostname)
	out.SpkiSha256 = strPtr(st.SPKI)
	out.FingerprintShort = strPtr(st.FingerprintShort)
	out.LastError = strPtr(st.LastError)
	if !st.TLSNotAfter.IsZero() {
		notAfter := st.TLSNotAfter
		out.TlsNotAfter = &notAfter
	}
	if st.Pairing != nil {
		session := gen.PairingSession{
			Id:               st.Pairing.ID,
			Role:             gen.RemoteRole(st.Pairing.Role),
			FingerprintShort: st.FingerprintShort,
			SpkiSha256:       st.SPKI,
			Addresses:        st.Addresses,
			ExpiresAt:        st.Pairing.ExpiresAt,
		}
		if st.Pairing.Label != "" {
			label := st.Pairing.Label
			session.Label = &label
		}
		out.Pairing = &session
	}
	public := toGenPublicStatus(st.Public)
	out.Public = &public

	configured := st.APNSConfigured
	out.ApnsConfigured = &configured
	out.ApnsStatus = &gen.ApnsStatus{
		Configured: st.APNSConfigured,
		KeyId:      strPtr(st.APNSKeyID),
		BundleId:   strPtr(st.APNSBundleID),
		TeamId:     strPtr(st.APNSTeamID),
	}
	return out
}

// toGenRemoteDevice 转换设备。
func toGenRemoteDevice(d remote.Device) gen.RemoteDevice {
	out := gen.RemoteDevice{
		Id:                   d.ID,
		Label:                d.Label,
		Role:                 gen.RemoteRole(d.Role),
		NotificationsEnabled: d.NotificationsEnabled,
		Platform:             strPtr(d.Platform),
		Model:                strPtr(d.Model),
		OsVersion:            strPtr(d.OSVersion),
		AppVersion:           strPtr(d.AppVersion),
		LastSeenIp:           strPtr(d.LastSeenIP),
	}
	// 父设备用空串表示"直接配对而来"。用空串而不是缺省，是因为界面要
	// 据此决定缩不缩进 —— 一个 nil 与空串在 Swift 侧是同一个判断。
	parent := d.ParentDeviceID
	out.ParentDeviceId = &parent
	if !d.CreatedAt.IsZero() {
		created := d.CreatedAt
		out.CreatedAt = &created
	}
	if !d.UpdatedAt.IsZero() {
		updated := d.UpdatedAt
		out.UpdatedAt = &updated
	}
	if !d.LastSeenAt.IsZero() {
		seen := d.LastSeenAt
		out.LastSeenAt = &seen
	}
	if !d.RevokedAt.IsZero() {
		revoked := d.RevokedAt
		out.RevokedAt = &revoked
	}
	return out
}

// deviceInfoFromGen 转换客户端自报的设备信息。
func deviceInfoFromGen(in gen.RemoteDeviceInfo) remote.DeviceInfo {
	out := remote.DeviceInfo{Name: in.Name}
	if in.Platform != nil {
		out.Platform = *in.Platform
	}
	if in.Model != nil {
		out.Model = *in.Model
	}
	if in.OsVersion != nil {
		out.OSVersion = *in.OsVersion
	}
	if in.AppVersion != nil {
		out.AppVersion = *in.AppVersion
	}
	return out
}

// decodeBody 解析 JSON 请求体。
//
// 与既有 handler 的差别：这里限制 1 MiB。远程面上任何一条路径都可能被
// 互联网上的设备（而不是本机进程）调用，而一个没有上限的 Decoder 允许
// 对端用一次请求把内存吃光。
func decodeBody(w http.ResponseWriter, r *http.Request, s *Server, out any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(out); err != nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", i18n.T("api.empty_body"))
		return false
	}
	return true
}

// enabledWord 把开关状态翻成审计文案里用的词。
//
// 不在文案里塞 true/false：审计记录是给人看的，
// "远程访问已 true" 这种句子会让排查的人先愣一下。
func enabledWord(on bool) string {
	if on {
		return i18n.T("remote.word.enabled")
	}
	return i18n.T("remote.word.disabled")
}

// toGenPublicStatus 把公网访问的状态转成契约类型。
func toGenPublicStatus(in remote.PublicStatus) gen.RemotePublicStatus {
	out := gen.RemotePublicStatus{Enabled: in.Enabled, Ready: in.Ready}
	if in.Host != "" {
		out.Host = strPtr(in.Host)
	}
	if in.Domain != "" {
		out.Domain = strPtr(in.Domain)
	}
	if len(in.Records) > 0 {
		records := make(map[string]string, len(in.Records))
		for k, v := range in.Records {
			records[k] = v
		}
		out.Records = &records
	}
	if in.LastCheck != nil {
		check := gen.RemotePublicCheck{
			At:      in.LastCheck.At,
			Verdict: gen.RemotePublicCheckVerdict(in.LastCheck.Verdict),
		}
		if in.LastCheck.Detail != "" {
			check.Detail = strPtr(in.LastCheck.Detail)
		}
		if in.LastCheck.Family != "" {
			check.Family = strPtr(in.LastCheck.Family)
		}
		out.LastCheck = &check
	}
	return out
}

// toGenPublicProbe 把探测计划转成契约类型。
func toGenPublicProbe(in remote.PublicProbe) gen.RemotePublicProbe {
	out := gen.RemotePublicProbe{Port: in.Port}
	if in.Host != "" {
		out.Host = strPtr(in.Host)
	}
	if in.Note != "" {
		out.Note = strPtr(in.Note)
	}
	if len(in.LANAddresses) > 0 {
		lan := append([]string(nil), in.LANAddresses...)
		out.LanAddresses = &lan
	}
	targets := make([]struct {
		Address *string `json:"address,omitempty"`
		Family  string  `json:"family"`
		Url     string  `json:"url"`
	}, 0, len(in.Targets))
	for _, t := range in.Targets {
		item := struct {
			Address *string `json:"address,omitempty"`
			Family  string  `json:"family"`
			Url     string  `json:"url"`
		}{Family: t.Family, Url: t.URL}
		if t.Address != "" {
			item.Address = strPtr(t.Address)
		}
		targets = append(targets, item)
	}
	out.Targets = targets
	return out
}
