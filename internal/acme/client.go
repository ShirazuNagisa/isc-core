package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

// 本文件是 ACME 的申请与续期流程。
//
// 协议细节由 golang.org/x/crypto/acme 处理 —— 那部分不该自己写。
// 这里负责的是**与内核的接合**：账户密钥放哪、证书存哪、
// 什么时候该续期、以及失败时怎么让用户看懂。

// 默认的 ACME 目录地址。
const (
	// LetsEncryptProduction 是生产环境。
	LetsEncryptProduction = acme.LetsEncryptURL
	// LetsEncryptStaging 是测试环境。
	//
	// 它签发的证书不被浏览器信任，但**配额宽松得多**。
	// 首次配置时应当用它试一遍 —— 生产环境的失败次数配额
	// 是每小时 5 次，调配置很容易把它用光，而用光之后要等一小时。
	LetsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// 证书文件的扩展名。
const (
	certSuffix = ".crt"
	keySuffix  = ".key"
	// metaSuffix 保存签发信息（签发时间、用途、是否测试环境）。
	metaSuffix = ".json"
)

// Store 管理证书文件。
//
// # 为什么用文件而不是数据库
//
// 证书与私钥最终要交给 TLS 栈使用，而它要的是文件或内存里的
// tls.Certificate。存文件让用户能直接检查（openssl x509 -text）、
// 能在出问题时手工替换、也能被其它工具复用。
//
// 私钥文件权限会被收紧到 0600 —— 那不是万无一失（同机的
// 管理员仍能读到），但它是这一层能做的、也是所有同类工具都在做的。
type Store struct {
	dir string
}

// NewStore 构造证书存储。
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir 返回证书目录。
func (s *Store) Dir() string { return s.dir }

// Meta 是随证书一起保存的签发信息。
type Meta struct {
	// Domains 是这张证书覆盖的域名。
	Domains []string `json:"domains"`
	// DirectoryURL 是签发它的 ACME 目录地址。
	//
	// 必须记下来：用测试环境签的证书不被浏览器信任，
	// 而用户在界面上看到的只是一句"证书无效"。
	DirectoryURL string `json:"directory_url"`
	// IssuedAt / ExpiresAt 是有效期。
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Staging 标记它是否来自测试环境。
	Staging bool `json:"staging"`
}

// Cert 是一张已签发的证书。
type Cert struct {
	Meta
	// CertPEM / KeyPEM 是 PEM 编码的证书链与私钥。
	CertPEM []byte
	KeyPEM  []byte
	// Path 是证书文件路径（便于在界面上显示）。
	Path string
}

// Save 写入证书、私钥与签发信息。
func (s *Store) Save(ctx context.Context, name string, cert Cert) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("acme: 无法创建证书目录 %s: %w", s.dir, err)
	}

	base := filepath.Join(s.dir, sanitizeName(name))

	// 私钥先写，且权限最紧。
	//
	// 顺序有讲究：先写证书再写私钥的话，中间那一刻存在"有证书没私钥"
	// 的状态，而 TLS 栈读到一个只有证书的文件会报一个含混的错。
	if err := writeFileMode(base+keySuffix, cert.KeyPEM, 0o600); err != nil {
		return err
	}
	if err := writeFileMode(base+certSuffix, cert.CertPEM, 0o644); err != nil {
		return err
	}
	return nil
}

// Load 读取一张证书。
func (s *Store) Load(name string) (Cert, error) {
	base := filepath.Join(s.dir, sanitizeName(name))

	certPEM, err := os.ReadFile(base + certSuffix)
	if err != nil {
		return Cert{}, err
	}
	keyPEM, err := os.ReadFile(base + keySuffix)
	if err != nil {
		return Cert{}, err
	}

	// 从证书本身读出有效期与域名 —— 那是**权威来源**。
	//
	// 不依赖 meta 文件：它可能缺失（用户手工拷了证书过来）或过期
	//（我们改了格式）。决定"要不要续期"必须基于证书本身。
	leaf, err := parseLeaf(certPEM)
	if err != nil {
		// 解析失败必须**报错**，不能返回一个有效期为零的 Cert。
		//
		// 静默返回零值会让调用方看到一张"有效期读不出来"的证书，
		// 而真正的问题是文件坏了 —— 用户拿到的提示会指向错误的方向。
		return Cert{}, fmt.Errorf("acme: 证书文件 %s 无法解析: %w",
			base+certSuffix, err)
	}

	return Cert{
		Meta: Meta{
			Domains:   leaf.DNSNames,
			IssuedAt:  leaf.NotBefore,
			ExpiresAt: leaf.NotAfter,
		},
		CertPEM: certPEM,
		KeyPEM:  keyPEM,
		Path:    base + certSuffix,
	}, nil
}

// Exists 报告某个名字的证书是否存在。
func (s *Store) Exists(name string) bool {
	base := filepath.Join(s.dir, sanitizeName(name))
	_, err1 := os.Stat(base + certSuffix)
	_, err2 := os.Stat(base + keySuffix)
	return err1 == nil && err2 == nil
}

// List 列出已保存的证书名。
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var out []string
	seen := make(map[string]bool)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, certSuffix) {
			continue
		}
		base := strings.TrimSuffix(name, certSuffix)
		if !seen[base] {
			seen[base] = true
			out = append(out, base)
		}
	}
	return out, nil
}

func writeFileMode(path string, data []byte, mode os.FileMode) error {
	// 先删再写：目标文件可能已存在且权限更宽（例如上一次是手工放的），
	// 而 WriteFile 不会收紧已有文件的权限。
	_ = os.Remove(path)

	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("acme: 写入 %s 失败: %w", path, err)
	}
	// 显式 Chmod：某些平台的 umask 会让实际权限比请求的更宽。
	return os.Chmod(path, mode)
}

// sanitizeName 把证书名转成安全的文件名。
//
// 证书名通常是主域名，但也可能是通配（*.example.com）组合出来的。
// `*` 在 Windows 上是非法文件名字符，而它会让保存失败 ——
// 症状是"申请成功了但找不到证书"。
func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == '*':
			b.WriteString("wildcard")
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "._")
	if out == "" {
		return "cert"
	}
	return out
}

func parseLeaf(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("acme: 证书不是合法的 PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

// ---------------------------------------------------------------------------
// ACME 客户端
// ---------------------------------------------------------------------------

// Config 是申请证书的配置。
type Config struct {
	// Domains 是要签发在一张证书上的域名。
	Domains []string
	// Email 是 ACME 账户的联系邮箱。
	//
	// 它很重要：证书快要过期而自动续期失败时，Let's Encrypt 会用
	// 它来提醒。不填会让"续期静默失败"变成"站点某天突然打不开"。
	Email string
	// DirectoryURL 是 ACME 目录地址。留空用生产环境。
	DirectoryURL string
	// AccountKeyPath 是账户私钥的保存位置。
	AccountKeyPath string
}

// Client 负责申请与续期证书。
type Client struct {
	solver *DNS01Provider
	store  *Store

	// mu 串行化签发。
	//
	// 并发的签发请求会创建两个 ACME 订单，而**失败配额是按订单计的**
	//（Let's Encrypt 生产环境每小时 5 次失败）。并发的重试很容易
	// 把配额用光，而用光之后要等一小时。
	mu sync.Mutex
}

// NewClient 构造客户端。
func NewClient(solver *DNS01Provider, store *Store) *Client {
	return &Client{solver: solver, store: store}
}

// Result 是一次签发的结果。
type Result struct {
	// Cert 是签发出来的证书。
	Cert Cert
	// Staging 标记它是否来自测试环境。
	Staging bool
}

// Obtain 申请（或续期）一张证书。
//
// 流程：注册账户 → 创建订单 → 逐个域名完成 DNS-01 校验 → 提交 CSR →
// 下载证书。
//
// # 失败时的清理
//
// 无论成功与否，为挑战创建的 TXT 记录都会被尽力删除。残留的
// 挑战记录本身无害（它是随机值），但会在用户的 DNS 里留下垃圾 ——
// 而用户看到自己没建过的记录时会怀疑是不是被入侵了。
func (c *Client) Obtain(ctx context.Context, cfg Config) (Result, error) {
	if len(cfg.Domains) == 0 {
		return Result{}, errors.New("acme: 至少要指定一个域名")
	}
	if cfg.DirectoryURL == "" {
		cfg.DirectoryURL = LetsEncryptProduction
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	accountKey, err := loadOrCreateAccountKey(cfg.AccountKeyPath)
	if err != nil {
		return Result{}, err
	}

	client := &acme.Client{
		Key:          accountKey,
		DirectoryURL: cfg.DirectoryURL,
	}

	// 注册账户。已注册时 ACME 会返回已有的账户，因此这个调用是幂等的。
	if _, err := client.Register(ctx, &acme.Account{Contact: contactFor(cfg.Email)},
		acme.AcceptTOS); err != nil {
		return Result{}, fmt.Errorf("acme: 注册账户失败: %w", err)
	}

	ids := make([]acme.AuthzID, 0, len(cfg.Domains))
	for _, d := range cfg.Domains {
		ids = append(ids, acme.AuthzID{Type: "dns", Value: d})
	}

	order, err := client.AuthorizeOrder(ctx, ids)
	if err != nil {
		return Result{}, fmt.Errorf("acme: 创建订单失败: %w", err)
	}

	// 逐个域名完成 DNS-01 校验。
	//
	// 一个域名失败就整体失败：部分成功的订单没有意义 ——
	// 证书要么覆盖全部域名，要么就得重新申请。
	if err := c.solveChallenges(ctx, client, order); err != nil {
		return Result{}, err
	}

	// 生成密钥与 CSR。
	certKey, csrDER, err := newCSR(cfg.Domains)
	if err != nil {
		return Result{}, err
	}

	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csrDER, true)
	if err != nil {
		return Result{}, fmt.Errorf("acme: 提交 CSR 失败: %w", err)
	}

	certPEM := encodeCertChain(der)
	keyPEM, err := marshalKey(certKey)
	if err != nil {
		return Result{}, err
	}

	leaf, err := parseLeaf(certPEM)
	if err != nil {
		return Result{}, err
	}

	cert := Cert{
		Meta: Meta{
			Domains:      cfg.Domains,
			DirectoryURL: cfg.DirectoryURL,
			IssuedAt:     leaf.NotBefore,
			ExpiresAt:    leaf.NotAfter,
			Staging:      isStaging(cfg.DirectoryURL),
		},
		CertPEM: certPEM,
		KeyPEM:  keyPEM,
	}

	name := certName(cfg.Domains)
	if err := c.store.Save(ctx, name, cert); err != nil {
		return Result{}, err
	}

	return Result{Cert: cert, Staging: cert.Staging}, nil
}

// solveChallenges 完成订单里全部授权域的 DNS-01 校验。
func (c *Client) solveChallenges(ctx context.Context, client *acme.Client, order *acme.Order) error {
	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return fmt.Errorf("acme: 读取授权失败: %w", err)
		}
		// 已经通过的授权不需要重做（订单可能被复用）。
		if authz.Status == acme.StatusValid {
			continue
		}

		chal := findDNS01Challenge(authz)
		if chal == nil {
			return fmt.Errorf(
				"acme: 域名 %s 的授权里没有 DNS-01 校验方式（可用的有 %v）",
				authz.Identifier.Value, challengeTypes(authz))
		}

		keyAuth, err := client.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return fmt.Errorf("acme: 计算挑战值失败: %w", err)
		}

		domain := authz.Identifier.Value

		// 先登记清理目标，再写记录。
		//
		// 顺序不能反：写在前面的话，如果写入过程本身失败（网络中断、
		// 服务商报错），我们仍然需要知道"可能已经写进去了一条" ——
		// 那时 CleanUp 才有东西可删。
		defer func(d string) {
			// 用一个独立的上下文：签发失败的原因常常是 ctx 被取消，
			// 而用一个已取消的 ctx 去清理会让残留记录留在用户的 DNS 里。
			cleanupCtx := context.WithoutCancel(ctx)
			if err := c.solver.CleanUp(cleanupCtx, d, chal.Token, keyAuth); err != nil {
				// 清理失败不改变签发结果，但必须留下痕迹 ——
				// 它是用户 DNS 里的一条残留记录。
				fmt.Fprintf(os.Stderr,
					"acme: 清理域名 %s 的挑战记录失败（可手动删除 _acme-challenge 记录）: %v\n",
					d, err)
			}
		}(domain)

		if err := c.solver.Present(ctx, domain, chal.Token, keyAuth); err != nil {
			return err
		}

		if _, err := client.Accept(ctx, chal); err != nil {
			return fmt.Errorf("acme: 通知挑战就绪失败: %w", err)
		}

		// 等 ACME 服务器完成校验。
		//
		// WaitAuthorization 内部会轮询，并在授权进入终态时返回。
		// 失败时它给出的是 StatusInvalid，而那需要翻译成人话 ——
		// 用户看到 "invalid" 完全不知道下一步该做什么。
		if _, err := client.WaitAuthorization(ctx, authzURL); err != nil {
			return explainAuthzError(domain, err)
		}
	}
	return nil
}

// explainAuthzError 把授权失败翻译成用户能据此行动的话。
func explainAuthzError(domain string, err error) error {
	return fmt.Errorf(
		"acme: 域名 %s 的 DNS-01 校验未通过: %w\n"+
			"常见原因：\n"+
			"  · 该域名的权威 DNS 不是所选服务商（检查 NS 记录）\n"+
			"  · 服务商那边的记录传播还没完成（稍后重试）\n"+
			"  · 凭据没有该域名的编辑权限\n"+
			"  · 域名本身不存在或已过期",
		domain, err)
}

// findDNS01Challenge 从授权里挑出 DNS-01 挑战。
//
// 注意 Challenges 的元素类型本身已经是指针，因此取地址会得到
// **指针的指针 —— 那既不是想要的类型，写进去的修改也落不回原处。
func findDNS01Challenge(authz *acme.Authorization) *acme.Challenge {
	for _, ch := range authz.Challenges {
		if ch != nil && ch.Type == "dns-01" {
			return ch
		}
	}
	return nil
}

func challengeTypes(authz *acme.Authorization) []string {
	out := make([]string, 0, len(authz.Challenges))
	for _, ch := range authz.Challenges {
		out = append(out, ch.Type)
	}
	return out
}

func contactFor(email string) []string {
	if strings.TrimSpace(email) == "" {
		return nil
	}
	return []string{"mailto:" + email}
}

// certName 由域名列表生成证书的文件名。
func certName(domains []string) string {
	if len(domains) == 0 {
		return "cert"
	}
	// 用第一个域名作为主名 —— 它是证书的 CN，也是用户认得出的那个。
	base := strings.TrimPrefix(domains[0], "*.")
	if len(domains) == 1 {
		return base
	}
	// 多个域名时加一个计数，避免两张不同的证书落到同一个文件上。
	return fmt.Sprintf("%s+%d", base, len(domains)-1)
}

func isStaging(directoryURL string) bool {
	return strings.Contains(directoryURL, "staging")
}

// ---------------------------------------------------------------------------
// 密钥与 CSR
// ---------------------------------------------------------------------------

// newAccountKey 生成一个新的账户密钥。
//
// 用 P-256 而不是 RSA-2048：账户密钥只用于签署 ACME 请求，
// 每次签发要签几十次，而 ECDSA 的签名速度与体积都远优于 RSA。
// 各家 ACME 实现都支持 ES256。
func newAccountKey() (crypto.Signer, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

func loadOrCreateAccountKey(path string) (crypto.Signer, error) {
	if path == "" {
		return newAccountKey()
	}

	if data, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(data)
		if block != nil {
			key, err := x509.ParseECPrivateKey(block.Bytes)
			if err == nil {
				return key, nil
			}
		}
		// 文件存在但读不出密钥：**报错而不是覆盖**。
		//
		// 覆盖会让这个账户永久失效 —— 已经签发的证书仍然有效，
		// 但它们再也无法续期，而症状要到 90 天后才出现。
		return nil, fmt.Errorf(
			"acme: 账户密钥文件 %s 无法解析。"+
				"删除它会让这个 ACME 账户永久失效（已签发的证书将无法续期），"+
				"请先备份并确认", path)
	}

	key, err := newAccountKey()
	if err != nil {
		return nil, fmt.Errorf("acme: 生成账户密钥失败: %w", err)
	}

	der, err := x509.MarshalECPrivateKey(key.(*ecdsa.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("acme: 序列化账户密钥失败: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("acme: 创建账户密钥目录失败: %w", err)
	}
	if err := writeFileMode(path,
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}),
		0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// newCSR 生成证书密钥与 CSR。
func newCSR(domains []string) (crypto.Signer, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("acme: 生成证书密钥失败: %w", err)
	}

	tmpl := &x509.CertificateRequest{
		// DNSNames 里要保留通配形式（*.example.com）——
		// 去掉 `*` 会签出一张不覆盖子域名的证书，
		// 而症状是"申请成功了但浏览器仍报证书错误"。
		DNSNames: domains,
		Subject: pkix.Name{
			CommonName: strings.TrimPrefix(domains[0], "*."),
		},
	}

	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, nil, fmt.Errorf("acme: 生成 CSR 失败: %w", err)
	}
	return key, der, nil
}

func marshalKey(key crypto.Signer) ([]byte, error) {
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("acme: 不支持的密钥类型")
	}
	der, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		return nil, fmt.Errorf("acme: 序列化证书密钥失败: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func encodeCertChain(der [][]byte) []byte {
	var out []byte
	for _, b := range der {
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b})...)
	}
	return out
}
