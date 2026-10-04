package remote

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件管理远程监听的自签证书，以及"客户端该往哪连"的候选地址。
//
// # 为什么是自签证书
//
// 要签发一张被系统信任的证书，用户必须拥有域名，并且让 ACME 的校验能到达
// 这台机器 —— 那是"把服务发布到公网"的门槛，而远程监控的门槛应该是"家里
// 同一个局域网"。自签证书把这件事降到零配置。
//
// # 为什么固定的是公钥（SPKI）而不是证书
//
// 固定整张证书是最容易写的做法，但它有一个必然踩到的坑：**地址变了就要
// 重签证书**（新网卡、新网段、路由器换了 DHCP 段），而重签之后指纹变了，
// 所有已配对的手机都会报"服务器身份已变化" —— 用户完全无法理解，因为
// 服务器根本没换。
//
// 因此重签时**沿用同一把私钥**，并且客户端固定的是公钥的 SHA-256：
// SAN 变了、有效期续了、序列号换了，指纹都不变。只有真的换了机器
// （换了密钥）指纹才变，而那时报"身份已变化"恰恰是正确的。

// 证书的有效期。
//
// 十年是一个刻意的选择：这张证书**没有 CA 会吊销它**，过期只会让所有
// 已配对的手机突然连不上，而用户没有任何办法自助修复（他不会想到去
// 换证书，更不会知道怎么做）。既然信任来自固定的公钥而不是证书链，
// 有效期在这里几乎是纯粹的负担。
const (
	certValidity = 10 * 365 * 24 * time.Hour
	// certBackdate 把 NotBefore 往前挪一天，吸收两端时钟的偏差。
	//
	// 手机上时钟偏几分钟是常见的，而"证书还没生效"报出来的错误
	// 与"证书不可信"几乎一样，排查成本极高。
	certBackdate = 24 * time.Hour
)

// 证书与私钥的文件名。
//
// 它们与 runtime.json 分开放在 remote/ 子目录：那个目录整体 0700，
// 而 runtime.json 里的本地令牌**绝不能**与远程凭据混在一起。
const (
	certFileName = "tls.crt"
	keyFileName  = "tls.key"
)

// Certificate 是远程监听用的自签证书。
type Certificate struct {
	TLS  tls.Certificate
	Leaf *x509.Certificate

	// spki 是公钥（SubjectPublicKeyInfo）DER 的 SHA-256。
	spki [sha256.Size]byte
}

// SPKIBase64 返回公钥指纹的 base64url 形式（无填充）。
//
// 这就是二维码里带的那个值，也是客户端固定的对象。
func (c *Certificate) SPKIBase64() string {
	return base64.RawURLEncoding.EncodeToString(c.spki[:])
}

// FingerprintShort 返回供两端人工核对的短码，形如 `A1B2-C3D4`。
//
// 八位十六进制：手输配对码只有六位，核对码再长就会有人开始跳过这一步。
// 它不承担密码学强度（真正的校验是完整的 SPKI 比对），承担的是
// "用户能把两个屏幕上的东西对上"。
func (c *Certificate) FingerprintShort() string {
	full := strings.ToUpper(hex.EncodeToString(c.spki[:4]))
	return full[0:4] + "-" + full[4:8]
}

// NotAfter 返回证书的到期时间。
func (c *Certificate) NotAfter() time.Time { return c.Leaf.NotAfter }

// LoadOrCreateCertificate 读取或生成自签证书。
//
// 已存在且**覆盖当前全部地址**时直接复用；地址集合变了就沿用同一把私钥
// 重签（见文件头的说明）。私钥损坏或无法解析时重新生成一对新的 ——
// 那种情况下"保持指纹稳定"已经不可能，重新开始比启动失败好。
func LoadOrCreateCertificate(dir string) (*Certificate, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf(i18n.T("remote.err.cert_dir"), err)
	}

	keyPath := filepath.Join(dir, keyFileName)
	certPath := filepath.Join(dir, certFileName)

	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}

	want := currentCertificateNames()

	if leafPEM, err := os.ReadFile(certPath); err == nil {
		if crt, parseErr := parseCertificate(leafPEM, key); parseErr == nil {
			if covers(crt.Leaf, want) && time.Now().Before(crt.Leaf.NotAfter) {
				return crt, nil
			}
		}
		// 解析失败或地址不全：落到下面重签。这里**不报错** ——
		// 一张读不出来的证书不该让内核起不来。
	}

	return issueCertificate(certPath, key, want)
}

// loadOrCreateKey 读取私钥；不存在时生成一把 P-256。
func loadOrCreateKey(path string) (*ecdsa.PrivateKey, error) {
	if raw, err := os.ReadFile(path); err == nil {
		if block, _ := pem.Decode(raw); block != nil {
			if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
				if ecdsaKey, ok := key.(*ecdsa.PrivateKey); ok {
					return ecdsaKey, nil
				}
			}
		}
		// 私钥文件存在但读不懂：继续往下走会生成一把新的，
		// 于是指纹变化、所有已配对设备需要重新配对 —— 这是**正确**的
		// 结果，因为我们确实无法再证明自己是原来那台机器。
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("remote.err.key_gen"), err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("remote.err.key_marshal"), err)
	}
	// 0600：私钥只有内核自己能读。
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, fmt.Errorf(i18n.T("remote.err.key_write"), err)
	}
	return key, nil
}

// issueCertificate 用给定的私钥签一张新证书并写入磁盘。
func issueCertificate(path string, key *ecdsa.PrivateKey, names certificateNames) (*Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf(i18n.T("remote.err.serial"), err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: names.subject()},
		NotBefore:             now.Add(-certBackdate),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              names.dns,
		IPAddresses:           names.ips,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("remote.err.cert_sign"), err)
	}

	// 0644：证书是公开的，客户端本来就会拿到它。
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, fmt.Errorf(i18n.T("remote.err.cert_write"), err)
	}
	return buildCertificate(der, key)
}

// parseCertificate 把磁盘上的证书与私钥配成一对。
func parseCertificate(certPEM []byte, key *ecdsa.PrivateKey) (*Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New(i18n.T("remote.err.cert_parse"))
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New(i18n.T("remote.err.cert_parse"))
	}
	return &Certificate{
		TLS:  tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: key, Leaf: leaf},
		Leaf: leaf,
		spki: spkiHash(leaf),
	}, nil
}

// buildCertificate 由 DER 构造 tls.Certificate 并算出公钥指纹。
func buildCertificate(der []byte, key *ecdsa.PrivateKey) (*Certificate, error) {
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, errors.New(i18n.T("remote.err.cert_parse"))
	}
	return &Certificate{
		TLS:  tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf},
		Leaf: leaf,
		spki: spkiHash(leaf),
	}, nil
}

// spkiHash 计算证书公钥（SubjectPublicKeyInfo）DER 的 SHA-256。
func spkiHash(leaf *x509.Certificate) [sha256.Size]byte {
	return sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
}

// certificateNames 是一张证书要覆盖的名字集合。
type certificateNames struct {
	ips []net.IP
	dns []string
}

// subject 返回证书的 CN。它只用于展示，不参与任何校验。
func (n certificateNames) subject() string {
	if len(n.dns) > 0 {
		return n.dns[0]
	}
	return "isc"
}

// covers 报告证书是否覆盖给定的名字集合。
//
// 判据是**当前的地址全都已经在证书里**，而不是两者相等：多出来的旧地址
// 无害（用户换过网段之后，旧地址的 SAN 留在证书里不会让任何人失败），
// 而缺一个地址就意味着某个客户端会看到名字不匹配。
func covers(leaf *x509.Certificate, want certificateNames) bool {
	have := map[string]bool{}
	for _, ip := range leaf.IPAddresses {
		have[ip.String()] = true
	}
	for _, name := range leaf.DNSNames {
		have[name] = true
	}
	for _, ip := range want.ips {
		if !have[ip.String()] {
			return false
		}
	}
	for _, name := range want.dns {
		if !have[name] {
			return false
		}
	}
	return true
}

// currentCertificateNames 收集本机当前应当写进证书的地址。
func currentCertificateNames() certificateNames {
	return certificateNames{
		ips: localIPs(),
		dns: []string{hostname(), hostname() + ".local"},
	}
}

// hostname 返回本机的**短**主机名；拿不到时返回一个稳定的占位值。
//
// # 为什么必须取短名
//
// macOS 上 `os.Hostname()` 返回的是 FQDN（`Mac-mini.local`），而
// 候选地址里还要再拼一个 `.local` 上去 —— 直接用它就会得到
// `Mac-mini.local.local`，一个永远解析不了的名字。
//
// 这个缺陷是端到端冒烟测试抓到的，单元测试抓不到：它需要**真实的**
// 主机名才能显形，而测试机上那个名字恰好不带点。
func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "isc"
	}
	if short, _, found := strings.Cut(name, "."); found && short != "" {
		return short
	}
	return name
}

// localIPs 返回本机的非回环单播地址。
func localIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	out := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
			continue
		}
		out = append(out, ip)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// LocalAddresses 返回客户端可以尝试的候选地址（不含端口）。
//
// 顺序就是建议的尝试顺序，而且这个顺序有实际意义：
//
//  1. IPv4 私有地址 —— 局域网里的手机最可能走通这一条；
//  2. IPv4 其他 —— 少数企业网/公网直连的情形；
//  3. IPv6 全局地址 —— 双方都有 IPv6 时可用（国内家宽很常见）；
//  4. `<hostname>.local` —— 地址变了也能靠 mDNS 找回来。
//
// 一次带上全部候选几乎没有成本（二维码大几十字节而已），而换 WiFi、
// 改网段、只走 IPv6 这几种情况都靠它自愈。
func LocalAddresses() []string {
	var private, other, v6 []string
	for _, ip := range localIPs() {
		switch {
		case ip.To4() != nil && ip.IsPrivate():
			private = append(private, ip.String())
		case ip.To4() != nil:
			other = append(other, ip.String())
		default:
			v6 = append(v6, ip.String())
		}
	}

	out := make([]string, 0, len(private)+len(other)+len(v6)+1)
	out = append(out, private...)
	out = append(out, other...)
	out = append(out, v6...)
	out = append(out, hostname()+".local")
	return out
}

// Candidates 返回带端口的候选地址。
//
// IPv6 必须加方括号，否则 `240e::1:8788` 会被解析成一个解析不了的地址 ——
// 这是一个只在纯 IPv6 用户那里出现、而其他所有人都测不到的缺陷。
func Candidates(port int) []string {
	hosts := LocalAddresses()
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, net.JoinHostPort(host, fmt.Sprint(port)))
	}
	return out
}
