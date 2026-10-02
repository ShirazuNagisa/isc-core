package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 本文件把签好的证书接进反向代理的 TLS 配置。
//
// # 为什么用 GetCertificate 而不是静态的 Certificates 列表
//
// 静态列表要求启动时就知道全部证书，而证书是**运行期陆续签发**的
//（用户加一条 HTTPS 路由 → 内核去签 → 签好了要立刻能用）。
// 用回调之后，新证书签好的那一刻就生效，不需要重启监听 ——
// 而重启会切断所有在途连接。
//
// # SNI 缺失时的行为
//
// 没有 SNI 的 TLS 握手（老客户端、直接用 IP 访问）拿不到域名，
// 因此无法选择证书。这时**拒绝握手**而不是随便给一张：
// 给错了证书会让客户端看到"证书域名不匹配"，而那个提示会把人
// 引向"证书配错了"，而不是"你该用域名访问"。

// CertProvider 按域名提供证书。
//
// 返回的 error 会终止握手并写进日志，因此它应当说明**为什么**——
// 用户在排查时能看到的第一手信息就是它。
type CertProvider interface {
	// Certificate 返回覆盖该域名的证书。
	Certificate(serverName string) (*tls.Certificate, error)
}

// TLSOptions 是启用 HTTPS 所需的配置。
type TLSOptions struct {
	// Provider 提供证书。
	Provider CertProvider
	// NextProtos 是 ALPN 协议列表。
	//
	// 必须包含 "h2" 与 "http/1.1"：不写的话浏览器会退回 HTTP/1.1，
	// 而 h2 的性能优势（多路复用）正是反代场景最需要的。
	NextProtos []string
	// MinVersion 是最低 TLS 版本。
	//
	// 默认 TLS 1.2：1.0/1.1 已被所有主流浏览器弃用，
	// 而且它们有已知的弱点。允许它们只会让扫描报告多几条告警。
	MinVersion uint16
}

// TLSConfig 构造 TLS 配置。
func (s *Server) TLSConfig(opts TLSOptions) *tls.Config {
	nextProtos := opts.NextProtos
	if len(nextProtos) == 0 {
		nextProtos = []string{"h2", "http/1.1"}
	}
	minVersion := opts.MinVersion
	if minVersion == 0 {
		minVersion = tls.VersionTLS12
	}

	cfg := &tls.Config{
		MinVersion: minVersion,
		NextProtos: nextProtos,
		// 会话票据的密钥由 Go 自动管理；这里只需要打开它，
		// 让回访的客户端省掉一次完整握手。
		SessionTicketsDisabled: false,
	}

	if opts.Provider != nil {
		var (
			mu    sync.Mutex
			cache = make(map[string]*tls.Certificate)
		)

		cfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := normalizeSNI(hello.ServerName)
			if name == "" {
				return nil, fmt.Errorf(
					"proxy: TLS 握手没有提供 SNI 主机名，无法选择证书。" +
						"请用域名访问（而不是直接用 IP）")
			}

			// 缓存：GetCertificate 在**每次**握手时都会被调用，
			// 而每次去磁盘读 PEM 并解析在高频访问下是明显的开销。
			//
			// 缓存的是已经解析好的 *tls.Certificate，因此省掉的是
			// 读文件 + 解析 PEM + 解析 X.509 这一整串动作。
			mu.Lock()
			if cert, ok := cache[name]; ok {
				mu.Unlock()
				return cert, nil
			}
			mu.Unlock()

			cert, err := opts.Provider.Certificate(name)
			if err != nil {
				return nil, err
			}

			mu.Lock()
			cache[name] = cert
			mu.Unlock()
			return cert, nil
		}
	}

	return cfg
}

// normalizeSNI 规范化 SNI 主机名。
//
// 去掉端口与结尾的点：SNI 里按规范只有主机名，但某些客户端会带上
// 多余的东西，而它们会让查表失败 —— 症状是"浏览器说证书不对"，
// 而实际是查不到证书。
func normalizeSNI(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, ".")
	if h, _, err := splitHostPortLoose(name); err == nil {
		name = h
	}
	return name
}

// splitHostPortLoose 尝试拆分 host:port。
//
// 与 net.SplitHostPort 的区别：没有端口时返回原值而不是错误。
// SNI 里通常没有端口，而用 SplitHostPort 会把正常的主机名当成错误。
func splitHostPortLoose(s string) (host, port string, err error) {
	idx := strings.LastIndex(s, ":")
	if idx < 0 {
		return s, "", nil
	}
	// IPv6 字面量（含多个冒号、可能带方括号）不做拆分。
	if strings.Contains(s[:idx], ":") && !strings.HasPrefix(s, "[") {
		return s, "", nil
	}
	return strings.Trim(s[:idx], "[]"), s[idx+1:], nil
}

// ServeTLS 在已建立的监听上提供 HTTPS。
//
// 与 http.Server.ServeTLS 的区别：那个要求证书文件路径，
// 而我们的证书是运行期签发的，只能走 GetCertificate。
func (m *Manager) ServeTLS(ctx context.Context, port int, opts TLSOptions) error {
	if opts.Provider == nil {
		return fmt.Errorf("proxy: 启用 HTTPS 需要证书来源")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 只做准备，**不**调用 startLocked。
	//
	// startLocked 会立刻起一个明文 HTTP 服务并占住监听，
	// 之后在同一个监听上做 TLS 是做不到的 —— 客户端在握手阶段
	// 收到的会是一段明文响应，报错是
	// "first record does not look like a TLS handshake"，
	// 而那个提示完全看不出根因是"起了两个服务"。
	srv, ln, err := m.prepareLocked(ctx, port)
	if err != nil {
		return err
	}

	tlsCfg := srv.TLSConfig(opts)
	httpSrv := &http.Server{
		Handler:           srv,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	m.httpSrv = httpSrv

	go func() {
		// 传空的证书路径：证书由 TLSConfig.GetCertificate 在握手时
		// 按 SNI 提供，而它是运行期陆续签发的，没有固定文件路径。
		if err := httpSrv.ServeTLS(ln, "", ""); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			m.log.Error("HTTPS 监听异常退出", "port", port, "err", err)
			m.mu.Lock()
			m.lastErr = err
			m.mu.Unlock()
		}
	}()

	m.log.Info("反向代理已启动（HTTPS）", "port", port,
		"min_version", tlsVersionName(tlsCfg.MinVersion),
		"routes", len(srv.Routes()))
	return nil
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}
