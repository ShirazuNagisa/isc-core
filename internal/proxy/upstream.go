// Package proxy 是内置的反向代理。
//
// # 它为什么必须存在
//
// 一个家庭网络通常只有**一个**能开放的入口（IPv6 前缀下的一个地址、
// 或者路由器上转发的一个非标端口），而用户想跑的服务往往有好几个。
// 没有反代就只能靠"每个服务用一个端口"硬撑，而那意味着每加一个服务
// 都要再开一个端口、再配一次证书。
//
// 反代把这些收拢到一个入口：按域名分流，证书也只签一次。
//
// # 安全边界（对应 docs/PLAN.md 的 R10）
//
// 反代跑在公网上，因此它天然是攻击面。三条硬约束：
//
//  1. **不能变成开放代理。** 上游地址只允许回环与私有网段 ——
//     否则任何人都能用它转发流量，把用户的机器变成跳板。
//  2. **不信客户端给的身份头。** X-Forwarded-* 由客户端随意伪造，
//     必须清掉再填正确的值。
//  3. **不转发 CONNECT。** 那等于把开放代理的洞开在 TLS 层。
package proxy

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net"
	"net/netip"
	"strings"
	"time"
)

// 上游地址的校验规则。
//
// # 为什么必须校验，而不是"用户填什么就连什么"
//
// 反代监听在公网上。若允许任意上游，任何人都能拿它当跳板 ——
// 请求进来、出去打内网或公网上的第三方，而所有流量都记在用户头上。
// 这不是理论风险：开放代理是被扫描器主动寻找的东西，
// 一台暴露几小时的家用机器就会被找到并滥用。
//
// # 允许的范围
//
//	127.0.0.0/8      回环
//	::1              回环
//	10/8, 172.16/12, 192.168/16   私有 IPv4
//	fc00::/7         唯一本地地址
//	169.254.0.0/16, fe80::/10     链路本地（容器与某些虚拟网络会用到）
//
// 刻意**不允许** "localhost" 之类的名字绕开检查：名字会被 DNS 解析，
// 而解析结果可以变（DNS rebinding）。校验必须在**实际连接的那个地址**
// 上做，因此本包用自定义的 DialContext 在每次连接前校验。

// ErrUnsafeUpstream 表示上游地址不在允许的范围内。
var ErrUnsafeUpstream = errors.New(i18n.T("proxy.err.upstream_private"))

// ValidateUpstream 校验一个上游地址是否可以作为转发目标。
//
// 它接受两种形式：
//
//	http://127.0.0.1:8096      带 scheme 的 URL
//	127.0.0.1:8096             裸的 host:port
//
// 返回规范化之后的 URL 与解析出的地址。
func ValidateUpstream(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New(i18n.T("proxy.err.upstream_empty"))
	}

	host, port, err := splitHostPort(raw)
	if err != nil {
		return "", err
	}

	// 名字必须能解析，且**全部**解析结果都落在允许的范围内。
	//
	// 要求全部而不是任意一个：一个同时解析到 127.0.0.1 与公网地址的
	// 名字（典型的 DNS rebinding 手法）只要有一个通过就会成为跳板。
	addrs, err := resolveHost(host)
	if err != nil {
		return "", err
	}
	for _, addr := range addrs {
		if !IsLocalAddr(addr) {
			return "", fmt.Errorf(i18n.T("proxy.err.denied_detail"), ErrUnsafeUpstream, host, addr)
		}
	}

	return fmt.Sprintf("http://%s", net.JoinHostPort(host, port)), nil
}

// splitHostPort 从各种写法里取出 host 与 port。
func splitHostPort(raw string) (host, port string, err error) {
	// 去掉 scheme。只支持 http —— 上游是本地服务，
	// 内核与它之间不需要 TLS（那只会让用户多配一份自签证书）。
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "http://"):
		raw = raw[len("http://"):]
	case strings.HasPrefix(lower, "https://"):
		return "", "", errors.New(
			i18n.T("proxy.err.no_https"))
	case strings.Contains(raw, "://"):
		return "", "", fmt.Errorf(i18n.T("proxy.err.bad_scheme"), raw)
	}

	// 去掉路径：上游应当只填到端口。
	if i := strings.IndexAny(raw, "/?#"); i >= 0 {
		raw = raw[:i]
	}

	host, port, err = net.SplitHostPort(raw)
	if err != nil {
		// 没写端口时补一个默认值 —— 但只对明确是本地服务的场景。
		// 这里选择报错而不是猜：猜错的症状是"转发到一个意想不到的
		// 服务上"，而那很难被发现。
		if strings.Contains(err.Error(), "missing port") {
			return "", "", fmt.Errorf(
				i18n.T("proxy.err.need_port"), raw)
		}
		return "", "", fmt.Errorf(i18n.T("proxy.err.bad_upstream_fmt"), err)
	}

	if _, err := net.LookupPort("tcp", port); err != nil {
		return "", "", fmt.Errorf(i18n.T("proxy.err.bad_port_str"), port)
	}
	// 端口 0 不是合法的转发目标。
	if port == "0" {
		return "", "", errors.New(i18n.T("proxy.err.zero_port"))
	}

	host = strings.Trim(host, "[]")
	if host == "" {
		return "", "", errors.New(i18n.T("proxy.err.no_host"))
	}
	return host, port, nil
}

// resolveHost 解析主机名。
//
// 直接是 IP 字面量时不查 DNS —— 那既快又避免了把字面量交给解析器。
func resolveHost(host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr.Unmap()}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("proxy.err.resolve_host"), host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf(i18n.T("proxy.err.no_addr"), host)
	}

	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.Unmap())
	}
	return out, nil
}

// IsLocalAddr 报告一个地址是否落在允许作为上游的范围内。
func IsLocalAddr(addr netip.Addr) bool {
	addr = addr.Unmap()

	switch {
	case addr.IsLoopback():
		return true
	case addr.IsPrivate():
		// 10/8、172.16/12、192.168/16、fc00::/7
		return true
	case addr.IsLinkLocalUnicast():
		// 169.254/16、fe80::/10：容器与某些虚拟网络会用到。
		return true
	case isCarrierNAT(addr):
		// 100.64.0.0/10 是运营商级 NAT 网段。
		//
		// 它**不**被允许作为上游：那是运营商给自己的设备用的地址段，
		// 用户的服务不可能在那里。
		return false
	case addr.IsUnspecified():
		// 0.0.0.0 会被内核解释成"本机任意地址"，看起来像回环，
		// 但它的语义是"监听所有接口" —— 作为**目标**地址是不明确的。
		return false
	default:
		return false
	}
}

var carrierNATPrefix = netip.MustParsePrefix("100.64.0.0/10")

func isCarrierNAT(a netip.Addr) bool {
	return a.Is4() && carrierNATPrefix.Contains(a)
}

// ---------------------------------------------------------------------------
// 安全的拨号
// ---------------------------------------------------------------------------

// safeDialer 在每次连接前校验目标地址。
//
// # 为什么不能只在配置时校验
//
// 配置时校验过不代表连接时还合法：主机名会被 DNS 重新解析，而
// **DNS rebinding** 正是利用这一点 —— 配置时域名解析到 127.0.0.1
// （通过校验），随后改解析到公网地址（变成跳板）。
//
// 因此校验必须发生在**实际连接的那个 IP** 上。这里在校验通过后
// 直接用那个 IP 拨号，而不是把主机名交给拨号器再解析一次 ——
// 后者会让"校验的地址"与"连接的地址"之间存在一个可被利用的窗口。
type safeDialer struct {
	inner *net.Dialer
}

func newSafeDialer() *safeDialer {
	return &safeDialer{inner: &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}}
}

// DialContext 实现 transport 的拨号接口。
func (d *safeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("proxy.err.bad_target_fmt"), err)
	}

	addrs, err := resolveHost(strings.Trim(host, "[]"))
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, addr := range addrs {
		if !IsLocalAddr(addr) {
			// 拒绝而不是跳过：跳过会让"有一个合法地址"成为绕过
			// 检查的手段。
			return nil, fmt.Errorf(i18n.T("proxy.err.denied_detail"), ErrUnsafeUpstream, host, addr)
		}

		// 用**校验过的那个 IP** 拨号，而不是主机名。
		conn, err := d.inner.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New(i18n.T("proxy.err.no_upstream_addr"))
	}
	return nil, lastErr
}
