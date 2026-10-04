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

// pusherCache 按凭据缓存一个 Pusher。
//
// 每次发通知都新建一个的话，JWT 的缓存就没了 —— 而 APNs 会因为我们
// 频繁换 token 而拒绝连接（TooManyProviderTokenUpdates）。那是一个
// "偶尔漏几条通知"的故障，极难查。
type pusherCache struct {
	mu          sync.Mutex
	store       *CredentialStore
	host        string
	pusher      Pusher
	fingerprint string
}

func newPusherCache(store *CredentialStore, host string) *pusherCache {
	return &pusherCache{store: store, host: host}
}

// get 返回当前的 Pusher；没配置凭据时返回 nil。
func (c *pusherCache) get() (Pusher, error) {
	credentials, err := c.store.Get()
	if err != nil || credentials == nil {
		return nil, err
	}

	fingerprint := credentials.TeamID + "/" + credentials.KeyID + "/" + credentials.BundleID

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pusher != nil && c.fingerprint == fingerprint {
		return c.pusher, nil
	}
	pusher, err := NewAPNSPusher(*credentials, c.host)
	if err != nil {
		return nil, err
	}
	c.pusher = pusher
	c.fingerprint = fingerprint
	return pusher, nil
}
