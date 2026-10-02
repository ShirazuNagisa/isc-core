package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RouteStore 是路由的持久化接口。
//
// 定义在代理包而不是存储包：这样代理包不依赖任何具体的存储实现，
// 测试可以用一个内存实现。
type RouteStore interface {
	List(ctx context.Context) ([]Route, error)
	Replace(ctx context.Context, routes []Route) error
}

// Status 是代理的运行时状态。
type Status struct {
	// Running 表示监听是否已经启动。
	Running bool `json:"running"`
	// Port 是当前监听的端口；未运行时为 0。
	Port int `json:"port"`
	// TLS 表示当前是否以 HTTPS 提供服务。
	//
	// 必须暴露出来：设置里改了 TLS 开关之后，调用方需要据此判断
	// "要不要重启监听"。不判断的话，用户打开 HTTPS 开关会看到
	// "什么都没发生" —— 代理还在用明文跑。
	TLS bool `json:"tls"`
	// Routes 是当前生效的路由数。
	Routes int `json:"routes"`
	// Error 是最近一次启动失败的原因。
	//
	// 它必须被保留下来：端口被占用之类的失败如果只写进日志，
	// 用户在界面上看到的就是"代理没开"，而不知道为什么。
	Error string `json:"error,omitempty"`
}

// Manager 管理代理监听的生命周期。
//
// # 为什么单独一层
//
// Server 只负责"怎么转发"，而"什么时候监听、监听在哪、路由从哪来"
// 是另一件事。分开之后，Server 的全部逻辑都能在没有真实监听的
// 情况下被测试 —— 而那正是最容易出错的部分（路由匹配、头处理）。
type Manager struct {
	store RouteStore
	log   *slog.Logger

	mu      sync.Mutex
	server  *Server
	httpSrv *http.Server
	ln      net.Listener
	port    int
	tls     bool
	lastErr error
}

// NewManager 构造管理器。
func NewManager(store RouteStore, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{store: store, log: log}
}

// RouteStore 返回路由的持久化仓储。
//
// 接口层读路由时用它而不是读 Manager 的内存副本：**存储才是真相**。
// 内存里的那份是"当前生效的"，它在保存失败或未运行时可能与存储不一致，
// 而对外的列表应当反映用户配置的内容。
func (m *Manager) RouteStore() RouteStore { return m.store }

// Server 返回底层的代理服务器，供查找路由等只读用途。
func (m *Manager) Server() *Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.server
}

// Start 在指定端口上启动监听。
//
// 幂等：已经在运行的实例会先被停掉再按新端口重启 —— 用户改端口时
// 期待的就是这个行为，而不是"端口没变所以什么也没发生"。
func (m *Manager) Start(ctx context.Context, port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("proxy: 监听端口 %d 不合法", port)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.startLocked(ctx, port)
}

func (m *Manager) startLocked(ctx context.Context, port int) error {
	srv, ln, err := m.prepareLocked(ctx, port)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Handler: srv,
		// 不设 ReadTimeout / WriteTimeout：代理要转发大文件与长连接
		//（媒体流、WebSocket），设它们会在一段时间后切断正常请求。
		//
		// 只设头部读取超时：它挡住的是"连上之后不发请求"的连接耗尽攻击，
		// 而不影响正常的长传输。
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	m.httpSrv = httpSrv

	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Error("代理监听异常退出", "port", port, "err", err)
			m.mu.Lock()
			m.lastErr = err
			m.mu.Unlock()
		}
	}()

	m.tls = false
	m.log.Info("反向代理已启动", "port", port, "routes", len(srv.Routes()))
	return nil
}

// prepareLocked 准备好代理服务器与监听，但**不开始服务**。
//
// # 为什么要把这一步单独拆出来
//
// 明文与 TLS 两种模式共用同一套准备动作（停旧的、加载路由、绑端口），
// 区别只在"怎么服务"。早先的版本让 ServeTLS 直接复用 startLocked ——
// 而那会先起一个**明文 HTTP 服务**，随后试图在同一个监听上做 TLS，
// 结果是监听已被占用，握手阶段客户端收到的是明文响应。
//
// 那个 bug 的表现是 "first record does not look like a TLS handshake"，
// 完全看不出根因是"起了两个服务"。
func (m *Manager) prepareLocked(ctx context.Context, port int) (*Server, net.Listener, error) {
	// 先停掉旧的：改端口时如果不先释放，新监听会因为端口占用而失败，
	// 而那个错误在用户看来是"改端口之后代理起不来了"。
	if m.httpSrv != nil {
		m.stopLocked()
	}

	srv := NewServer(m.log, port)

	// 从存储加载路由。
	//
	// 加载失败**不阻止监听启动**：一个空路由表的代理会返回 404，
	// 那比"代理根本没起来"更容易诊断，而且用户还能通过接口把路由
	// 修好。若这里直接失败，用户就得先修好数据才能启动服务 ——
	// 而他可能正是想通过界面去修。
	//
	// loadErr 与监听错误分开记录：共用一个 lastErr 的话，监听成功
	// 之后会把它清成 nil，于是"路由加载失败但监听成功"这种情况下
	// 状态里什么都不剩，用户在界面上只看到"代理在运行"却发现
	// 所有请求都是 404。
	var loadErr error

	routes, err := m.store.List(ctx)
	if err != nil {
		m.log.Error("加载代理路由失败，将以空路由表启动", "err", err)
		loadErr = err
		routes = nil
	}
	if err := srv.SetRoutes(routes); err != nil {
		// 存储里的路由不合法：同样是"带着问题启动"更好 ——
		// 用户需要界面可用才能修它。
		m.log.Error("存储中的代理路由不合法，将以空路由表启动", "err", err)
		loadErr = err
		_ = srv.SetRoutes(nil)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		m.lastErr = err
		return nil, nil, fmt.Errorf(
			"proxy: 无法监听端口 %d：%w"+
				"（端口可能已被其它程序占用）", port, err)
	}

	m.server = srv
	m.ln = ln
	m.port = port
	// 保留路由加载的错误：它不会因为监听成功而消失 ——
	// 用户仍然需要知道"为什么所有请求都是 404"。
	m.lastErr = loadErr

	return srv, ln, nil
}

// Stop 停止监听。
func (m *Manager) Stop(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	return nil
}

func (m *Manager) stopLocked() {
	if m.httpSrv == nil {
		return
	}

	// 用一个独立的上限：关停会等待在途请求结束（可能有正在传输的
	// 大文件），而无限等待会让"关掉代理"这个动作看起来卡住了。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := m.httpSrv.Shutdown(ctx); err != nil {
		m.log.Warn("代理关停超时，强制关闭", "err", err)
		_ = m.httpSrv.Close()
	}
	if m.server != nil {
		_ = m.server.Close()
	}

	port := m.port
	m.httpSrv = nil
	m.server = nil
	m.ln = nil
	m.port = 0
	m.tls = false

	m.log.Info("反向代理已停止", "port", port)
}

// Reload 从存储重新加载路由并整体替换。
//
// 它**不需要重启监听**：路由表是热更新的。用户改一条路由时，
// 在途的请求不该被切断 —— 那会让正在看视频的人莫名其妙地断流。
func (m *Manager) Reload(ctx context.Context) error {
	m.mu.Lock()
	srv := m.server
	m.mu.Unlock()

	if srv == nil {
		return errors.New("proxy: 代理未在运行")
	}

	routes, err := m.store.List(ctx)
	if err != nil {
		return fmt.Errorf("proxy: 加载路由失败: %w", err)
	}
	if err := srv.SetRoutes(routes); err != nil {
		return err
	}
	return nil
}

// SaveRoutes 校验并保存路由；代理正在运行时立即生效。
//
// # 为什么代理没在运行时也要能保存
//
// 用户完全可能先规划好路由、再开启代理。早先的版本在未运行时返回
// "代理未在运行"，而那让"先配置后启用"这条最自然的顺序走不通 ——
// 用户会以为是自己填错了什么。
//
// # 为什么先校验再落库
//
// 反过来的话，一份不合法的路由会被写进数据库，而接口返回失败 ——
// 用户会以为"没保存成功"，但内核下次启动时会因为这份数据而带着
// 空路由表运行。那是一个"明明没保存却生效了坏数据"的怪状态。
func (m *Manager) SaveRoutes(ctx context.Context, routes []Route) error {
	m.mu.Lock()
	selfPort := m.port
	m.mu.Unlock()

	// 用当前监听的端口做自环校验；未运行时用 0（跳过该项）——
	// 那种情况下还没有"自己"可以指向。
	if err := validateAll(routes, selfPort); err != nil {
		return err
	}

	if err := m.store.Replace(ctx, routes); err != nil {
		return err
	}

	// 落库成功后再生效。
	//
	// 未运行时**不算失败**：那是"先配置后启用"的正常路径，
	// 路由会在下次 Start 时被加载。
	if m.Server() == nil {
		m.log.Info("转发规则已保存（反向代理未运行，将在启动时生效）",
			"routes", len(routes))
		return nil
	}

	// 生效失败时数据已经是对的，只是暂时没生效 ——
	// 那比"生效了但数据是坏的"容易恢复。
	return m.Reload(ctx)
}

// validateAll 校验整组路由，包含**跨路由**的冲突检查。
//
// 冲突检查只能在整组上做：同一个域名映射到两个上游时，请求打到
// 哪一条取决于路由表的顺序，而那个顺序对用户是不可见的。
func validateAll(routes []Route, selfPort int) error {
	seen := make(map[string]string, len(routes))

	for _, r := range routes {
		if err := r.Validate(selfPort); err != nil {
			return err
		}
		for _, h := range r.Hosts {
			key := normalizeHost(h)
			if existing, dup := seen[key]; dup {
				return fmt.Errorf(
					"proxy: 域名 %s 被两条路由同时使用（%s 与 %s）—— "+
						"同一个域名只能指向一个上游，"+
						"否则请求打到哪一条取决于不可见的顺序",
					h, existing, r.ID)
			}
			seen[key] = r.ID
		}
	}
	return nil
}

func normalizeHost(h string) string {
	return strings.ToLower(strings.TrimSpace(h))
}

// Routes 返回当前生效的路由。
//
// 读的是**内存里那份**（当前生效的），而不是存储 —— 证书管理需要
// 知道"现在真的在服务哪些域名"，而存储里的可能与生效的不一致
// （保存成功但热更新失败时）。
func (m *Manager) Routes() []Route {
	m.mu.Lock()
	srv := m.server
	m.mu.Unlock()

	if srv == nil {
		return nil
	}
	return srv.Routes()
}

// Status 返回当前状态。
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	st := Status{Port: m.port, Running: m.httpSrv != nil, TLS: m.tls}
	if m.server != nil {
		st.Routes = len(m.server.Routes())
	}
	if m.lastErr != nil {
		st.Error = m.lastErr.Error()
	}
	return st
}
