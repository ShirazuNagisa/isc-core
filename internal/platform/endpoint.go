package platform

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件定义本地管理通道的地址格式与拨号逻辑。
//
// 地址格式（scheme://addr）：
//
//	npipe://./pipe/isc-core             Windows 命名管道
//	unix:///var/lib/isc/run/isc.sock    Unix 域套接字
//	tcp://127.0.0.1:0                   回环 TCP（端口 0 表示自动分配）
//
// 客户端发现流程见 docs/ARCHITECTURE.md §8.3：
// 先试 endpoint（管道 / socket），失败再试 fallback_endpoint（回环）。
//
// 浏览器（验证控制台）无法连接命名管道或 Unix 套接字，
// 因此回环 TCP 通道必须始终存在，不能因为"管道更安全"就省掉它。

// 支持的传输 scheme。
const (
	SchemeNamedPipe = "npipe"
	SchemeUnix      = "unix"
	SchemeTCP       = "tcp"
)

// Endpoint 是本地管理通道的地址。
type Endpoint string

// String 实现 fmt.Stringer。
func (e Endpoint) String() string { return string(e) }

// Scheme 返回地址的 scheme（小写）。
func (e Endpoint) Scheme() string {
	s, _, _ := strings.Cut(string(e), "://")
	return strings.ToLower(s)
}

// IsLoopback 报告该地址是否走 TCP 栈。
//
// 走 TCP 意味着任何本机进程都能尝试连接（因此必须强制 token 鉴权），
// 而管道 / Unix 套接字还能额外依赖文件系统或 ACL 权限。
func (e Endpoint) IsLoopback() bool { return e.Scheme() == SchemeTCP }

// TransportName 返回该地址所用传输的可读名称（已本地化）。
//
// 用于日志与控制台展示，例如 "命名管道" / "Unix 域套接字" / "回环 TCP"。
func (e Endpoint) TransportName() string {
	switch e.Scheme() {
	case SchemeNamedPipe:
		return i18n.T("transport.named_pipe")
	case SchemeUnix:
		return i18n.T("transport.unix_sock")
	case SchemeTCP:
		return i18n.T("transport.loopback")
	default:
		return "unknown"
	}
}

// HTTPBaseURL 返回该地址对应的 HTTP base URL。
//
// 命名管道与 Unix 套接字没有真实的 host，统一用占位 host `isc.local`；
// 实际连接由 DialContext 接管，占位值不会出现在网络请求中。
func (e Endpoint) HTTPBaseURL() string {
	if e.IsLoopback() {
		return "http://" + strings.TrimPrefix(string(e), SchemeTCP+"://")
	}
	return "http://isc.local"
}

// Listen 在本地建立监听。
//
// 对 tcp scheme，端口写 0 表示由内核分配空闲端口；
// 调用方需从返回的 listener.Addr() 读回真实端口。
func (e Endpoint) Listen(ctx context.Context) (net.Listener, error) {
	p, err := parseEndpoint(string(e))
	if err != nil {
		return nil, err
	}
	switch p.scheme {
	case SchemeTCP:
		l, err := net.Listen("tcp", p.addr)
		if err != nil {
			return nil, fmt.Errorf("platform: 监听 %s 失败: %w", e, err)
		}
		return l, nil
	case SchemeNamedPipe, SchemeUnix:
		return listenLocal(ctx, p.scheme, p.addr)
	default:
		return nil, fmt.Errorf("platform: 不支持的传输 scheme %q", p.scheme)
	}
}

// DialContext 的签名与 net.Dialer.DialContext 一致，
// 可直接赋给 http.Transport.DialContext。
type DialContext func(ctx context.Context, network, addr string) (net.Conn, error)

// DialContext 返回一个连接到该地址的拨号函数。
//
// 返回值可直接用于 http.Transport，从而让标准库的 http.Client
// 通过命名管道或 Unix 套接字发送请求。
func (e Endpoint) DialContext() (DialContext, error) {
	p, err := parseEndpoint(string(e))
	if err != nil {
		return nil, err
	}
	switch p.scheme {
	case SchemeTCP:
		d := &net.Dialer{Timeout: 10 * time.Second}
		return func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp", p.addr)
		}, nil
	case SchemeNamedPipe, SchemeUnix:
		return func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialLocal(ctx, p.scheme, p.addr)
		}, nil
	default:
		return nil, fmt.Errorf("platform: 不支持的传输 scheme %q", p.scheme)
	}
}

// HTTPClient 构造一个通过该地址访问内核的 HTTP 客户端。
//
// timeout 为 0 表示不设总超时（适用于事件流这类长连接）。
func (e Endpoint) HTTPClient(timeout time.Duration) (*http.Client, error) {
	dial, err := e.DialContext()
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dial,
			MaxIdleConns:          8,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: time.Second,
			// 明确禁用代理：本机通道绝不应经过任何代理。
			Proxy: nil,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// 解析
// ---------------------------------------------------------------------------

type parsedEndpoint struct {
	scheme string
	// addr 是 scheme 相关的地址形式：
	//	npipe → \\.\pipe\isc-core
	//	unix  → /var/lib/isc/run/isc.sock
	//	tcp   → 127.0.0.1:52341
	addr string
}

func parseEndpoint(s string) (parsedEndpoint, error) {
	if s == "" {
		return parsedEndpoint{}, fmt.Errorf("platform: endpoint 为空")
	}
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok {
		return parsedEndpoint{}, fmt.Errorf("platform: endpoint %q 缺少 scheme:// 前缀", s)
	}
	if rest == "" {
		return parsedEndpoint{}, fmt.Errorf("platform: endpoint %q 的地址部分为空", s)
	}

	switch strings.ToLower(scheme) {
	case SchemeNamedPipe:
		// npipe://./pipe/isc-core  →  \\.\pipe\isc-core
		//
		// 同时容忍 npipe:////./pipe/isc-core 与 npipe://\\.\pipe\isc-core
		// 这类等价写法，避免调用方拼错时得到难以理解的报错。
		p := strings.ReplaceAll(rest, "/", `\`)
		p = strings.TrimLeft(p, `.\`)
		if p == "" {
			return parsedEndpoint{}, fmt.Errorf("platform: 命名管道地址 %q 无效", s)
		}
		return parsedEndpoint{scheme: SchemeNamedPipe, addr: `\\.\` + p}, nil

	case SchemeUnix:
		if !strings.HasPrefix(rest, "/") {
			return parsedEndpoint{}, fmt.Errorf(
				"platform: Unix 套接字路径必须是绝对路径，得到 %q", rest)
		}
		return parsedEndpoint{scheme: SchemeUnix, addr: rest}, nil

	case SchemeTCP:
		host, _, err := net.SplitHostPort(rest)
		if err != nil {
			return parsedEndpoint{}, fmt.Errorf("platform: TCP 地址 %q 无效: %w", rest, err)
		}
		// 安全约束：只允许回环地址，防止误把管理面暴露到局域网。
		switch host {
		case "127.0.0.1", "::1", "localhost":
		default:
			return parsedEndpoint{}, fmt.Errorf(
				"platform: 拒绝非回环的管理地址 %q —— 管理面绝不能对外暴露", rest)
		}
		return parsedEndpoint{scheme: SchemeTCP, addr: rest}, nil

	default:
		return parsedEndpoint{}, fmt.Errorf("platform: 不支持的传输 scheme %q", scheme)
	}
}

// LocalEndpoint 返回本平台在指定运行时目录下的默认本地通道地址。
//
// runDir 只对 Unix 系平台有意义（套接字路径），Windows 命名管道
// 使用固定的内核对象命名空间，不依赖文件系统路径。
func LocalEndpoint(runDir string) Endpoint {
	return localEndpoint(runDir)
}
