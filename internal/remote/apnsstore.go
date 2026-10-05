package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件是 APNs 凭据的存放处。
//
// # 为什么不用内嵌的数据库
//
// 凭据是一个**整体**（team id + key id + bundle id + 私钥），
// 而它没有"列表""查询""过滤"这些需求 —— 只有读与写。放进键值表
// 会把一份 JSON 拆成四行，而"四行里少了一行"这种状态没有任何意义。
//
// 因此它是一个文件，而且**加密**：`.p8` 是一把能给你所有用户的手机
// 发推送的钥匙，而数据目录里已经有一个主密钥（`internal/secret`），
// 没有理由让它以明文躺着。
const apnsFileName = "apns.json"

// Cipher 是加解密接口。
//
// 定义在这里而不是直接依赖 `internal/secret`：本包因此可以在测试里
// 用一个恒等实现，而"密钥存在哪、怎么轮换"是另一个包的事。
type Cipher interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(envelope []byte) ([]byte, error)
}

// CredentialStore 读写 APNs 凭据。
type CredentialStore struct {
	path   string
	cipher Cipher

	mu      sync.RWMutex
	cached  *Credentials
	loaded  bool
	loadErr error
}

// NewCredentialStore 构造凭据存储。cipher 为 nil 时**拒绝写入** ——
// 明文落盘比功能不可用糟得多。
func NewCredentialStore(dir string, cipher Cipher) *CredentialStore {
	return &CredentialStore{path: filepath.Join(dir, apnsFileName), cipher: cipher}
}

// Get 返回当前凭据；没有配置时返回 (nil, nil)。
func (s *CredentialStore) Get() (*Credentials, error) {
	s.mu.RLock()
	if s.loaded {
		defer s.mu.RUnlock()
		return s.cached, s.loadErr
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return s.cached, s.loadErr
	}
	s.loaded = true

	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		s.loadErr = fmt.Errorf(i18n.T("remote.err.apns_read"), err)
		return nil, s.loadErr
	}
	if s.cipher == nil {
		s.loadErr = errors.New(i18n.T("remote.err.apns_no_cipher"))
		return nil, s.loadErr
	}

	plain, err := s.cipher.Decrypt(raw)
	if err != nil {
		// 解不开时**保留文件**并报错，而不是当成"没配过"。
		//
		// 两者的区别很实际：当成没配过会让用户重新填一遍，而旧文件
		// 还在那里 —— 下次启动仍然解不开，于是形成一个静默的循环。
		s.loadErr = fmt.Errorf(i18n.T("remote.err.apns_decrypt"), err)
		return nil, s.loadErr
	}

	var credentials Credentials
	if err := json.Unmarshal(plain, &credentials); err != nil {
		s.loadErr = fmt.Errorf(i18n.T("remote.err.apns_decode"), err)
		return nil, s.loadErr
	}
	s.cached = &credentials
	return s.cached, nil
}

// Set 保存凭据（覆盖已有的）。
func (s *CredentialStore) Set(credentials Credentials) error {
	if err := credentials.Validate(); err != nil {
		return err
	}
	if s.cipher == nil {
		return errors.New(i18n.T("remote.err.apns_no_cipher"))
	}

	plain, err := json.Marshal(credentials)
	if err != nil {
		return fmt.Errorf(i18n.T("remote.err.apns_encode"), err)
	}
	envelope, err := s.cipher.Encrypt(plain)
	if err != nil {
		return fmt.Errorf(i18n.T("remote.err.apns_encrypt"), err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf(i18n.T("remote.err.apns_write"), err)
	}
	// 0600：它是密文，但没有任何理由让同机其他用户读到它。
	if err := os.WriteFile(s.path, envelope, 0o600); err != nil {
		return fmt.Errorf(i18n.T("remote.err.apns_write"), err)
	}

	s.mu.Lock()
	s.cached = &credentials
	s.loaded = true
	s.loadErr = nil
	s.mu.Unlock()
	return nil
}

// Delete 删除凭据。没有配置时是空操作。
func (s *CredentialStore) Delete() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf(i18n.T("remote.err.apns_write"), err)
	}
	s.mu.Lock()
	s.cached = nil
	s.loaded = true
	s.loadErr = nil
	s.mu.Unlock()
	return nil
}

// Status 返回不含敏感字段的状态。
//
// key_id 也只保留末四位：把完整的 key id 显示在界面上没有意义，
// 而它与 team id 组合起来足以在 Apple 的接口上定位一个账号。
func (s *CredentialStore) Status() (configured bool, teamID, keyID, bundleID string, err error) {
	credentials, err := s.Get()
	if err != nil || credentials == nil {
		return false, "", "", "", err
	}
	return true, credentials.TeamID, maskTail(credentials.KeyID), credentials.BundleID, nil
}

// maskTail 只保留末四位。
func maskTail(value string) string {
	if len(value) <= 4 {
		return value
	}
	return "…" + value[len(value)-4:]
}

// pusherCache 按 (接入点, 凭据) 缓存 Pusher。
//
// 每次发通知都新建一个的话，JWT 的缓存就没了 —— 而 APNs 会因为我们
// 频繁换 token 而拒绝连接（TooManyProviderTokenUpdates）。那是一个
// "偶尔漏几条通知"的故障，极难查。
//
// # 为什么缓存要按接入点分，而不是一个进程一把
//
// 接入点从"一个进程一个"变成了"一台设备一个"：内核必须按**设备登记时
// 自报的环境**把通知发到沙箱或生产。发错的代价不是"这条丢了" ——
// Apple 回的是 400 BadDeviceToken，而本包把这个错误理解成"令牌已失效"，
// 于是会把这台设备**完全有效**的令牌清掉。用户看到的是"通知一直收不到，
// 而且每次都要重新登记一遍"。
//
// 一个进程里最多两个接入点，因此这张表最多两行。用 map 而不是两个字段：
// 将来若多出一个环境，"忘了加一个分支"会变成一次查表未命中（走默认），
// 而不是一个只在那个环境上才出现的静默失败。
type pusherCache struct {
	mu     sync.Mutex
	store  *CredentialStore
	host   string
	byHost map[string]cachedPusher
	// newPusher 构造一把 pusher。生产路径上永远是 `NewAPNSPusher`。
	//
	// 它是一处**只为测试存在**的缝，理由与 `Pusher` 接口本身一样：本包
	// 有一条硬约束 —— 测试不许对真实 APNs 端点发请求。而接入点现在是
	// **按设备算出来**的，于是"调用方有没有把设备传对"这件事，在没有
	// 这道缝的情况下只能靠"不要写错"来保证：真去发一次就打到 Apple 了。
	//
	// 默认为 nil（走真实现），因此它对生产行为没有任何影响。
	newPusher func(Credentials, string) (Pusher, error)
}

// cachedPusher 是某个接入点上的一把 pusher，以及它由哪份凭据构造而来。
//
// 指纹与 pusher 放在一起而不是各存一份：两者必须同时更新，分开存就会
// 出现"凭据换了、指纹还是旧的，于是继续用旧凭据签的 JWT"这种状态。
type cachedPusher struct {
	pusher      Pusher
	fingerprint string
}

func newPusherCache(store *CredentialStore, host string) *pusherCache {
	return &pusherCache{store: store, host: host, byHost: map[string]cachedPusher{}}
}

// get 返回把通知送到**这台设备**该用的 Pusher；没配凭据时返回 nil。
//
// 设备是参数而不是由调用方先算好接入点：算错了没有任何补救 ——
// 一个走错环境的令牌在 Apple 那边与一个伪造的令牌没有区别。
func (c *pusherCache) get(device Device) (Pusher, error) {
	credentials, err := c.store.Get()
	if err != nil || credentials == nil {
		return nil, err
	}

	host := c.hostFor(device)
	fingerprint := credentials.TeamID + "/" + credentials.KeyID + "/" + credentials.BundleID

	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.byHost[host]; ok && cached.fingerprint == fingerprint {
		return cached.pusher, nil
	}
	pusher, err := c.buildPusher(*credentials, host)
	if err != nil {
		return nil, err
	}
	if c.byHost == nil {
		c.byHost = map[string]cachedPusher{}
	}
	c.byHost[host] = cachedPusher{pusher: pusher, fingerprint: fingerprint}
	return pusher, nil
}

// buildPusher 构造一把 pusher；测试会替换 `newPusher`。
func (c *pusherCache) buildPusher(credentials Credentials, host string) (Pusher, error) {
	if c.newPusher != nil {
		return c.newPusher(credentials, host)
	}
	return NewAPNSPusher(credentials, host)
}

// hostFor 决定一台设备该走哪个接入点。
func (c *pusherCache) hostFor(device Device) string {
	// 显式覆盖优先，而且**对所有设备**生效。
	//
	// 验收脚本用一个本地的假 APNs 替换真实端点（见
	// scripts/acceptance-remote.sh 里的 ISC_APNS_HOST）。按环境分流会把
	// 一半设备发到 Apple 的真实接入点 —— 那既让那条脚本失去意义，
	// 也破坏了"测试不许碰真实端点"这条约束。
	if c.host != "" {
		return c.host
	}
	if device.APNSEnvironment == PushEnvSandbox {
		return APNSHostSandbox
	}
	// 空串与认不出的值都当生产：绝大多数设备是商店版（生产令牌），
	// 而两种猜错方式的代价是对称的 —— 没有更好的选择。空串还会出现在
	// "这个字段存在之前登记的设备"上。
	return APNSHostProduction
}
