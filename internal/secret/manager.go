// Package secret 管理 ISC 的主密钥并用它加解密敏感数据。
//
// 设计（见 docs/ARCHITECTURE.md §5 与 docs/DECISIONS.md D06）：
//
//	主密钥（32 字节随机值）
//	  ├─ 由平台密钥存储保护（Windows DPAPI / macOS Keychain /
//	  │  Linux Secret Service / 兜底文件）
//	  └─ 用于对所有凭据做 AES-256-GCM 加密，密文存入 SQLite
//
// 为什么要引入"主密钥"这一层，而不是直接用 DPAPI 加密每个凭据：
//
//   - ROTATION：换主密钥只需重新加密主密钥本身，或者按 key_version
//     逐步重加密，不必一次改写全部凭据；
//   - PORTABILITY：密钥存储的形态在三个平台上差异极大，把它收敛到
//     "保存一小段字节"这一件事上，其余逻辑就完全平台无关了；
//   - SIZE：Keychain / Secret Service 对条目大小有限制，
//     而凭据条目数量会随用户使用增长。
//
// 安全边界：数据库文件泄漏本身不会泄漏任何凭据 —— 没有主密钥，
// 密文不可解。反过来，主密钥泄漏等同于全部凭据泄漏。
package secret

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"

	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// KeyVersion 是当前主密钥的版本号。
//
// 它会随每条密文一起写入数据库（`key_version` 列）。
// 将来支持轮换时，可以按版本号分批重加密，而不必停机。
const KeyVersion = 1

// masterKeyName 是主密钥在平台密钥存储中的条目名。
const masterKeyName = "master"

// masterKeyBytes 是主密钥长度。AES-256 要求 32 字节。
const masterKeyBytes = 32

// envelopeVersion 是密文信封的格式版本。
//
// 它与 KeyVersion 是两件事：前者描述"密文怎么排布"，后者描述
// "用哪把密钥加密"。分开是为了让格式演进与密钥轮换互不牵制。
const envelopeVersion byte = 1

// nonceBytes 是 GCM 的 nonce 长度（标准值 12 字节）。
const nonceBytes = 12

// aad 是附加认证数据。
//
// 它不加密但参与完整性校验，因此把"这是 ISC 的凭据密文、格式版本为 1"
// 绑定进密文里。这样即使有人把另一个系统的密文搬进来，解密也会失败，
// 而不是解出一段看似合法的垃圾。
var aad = []byte("isc-core/secret/v1")

// Manager 持有主密钥并提供加解密。
//
// 并发安全：主密钥在初始化后不再变化，AES-GCM 的 Seal/Open 本身无状态。
type Manager struct {
	store  platform.SecretStore
	master []byte

	// created 记录本次是否新生成的主密钥，供日志区分
	// "首次初始化"与"正常启动"。
	created bool
}

// Open 打开（必要时创建）主密钥。
//
// 主密钥不存在时生成一个新的并交给平台密钥存储保存 ——
// 首次启动走的就是这条路，因此它不是异常路径。
func Open(ctx context.Context, store platform.SecretStore) (*Manager, error) {
	if store == nil {
		return nil, errors.New(i18n.T("secret.err.nil_store"))
	}

	existing, found, err := store.Get(ctx, masterKeyName)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("secret.err.read_master"), err)
	}

	if found {
		if len(existing) != masterKeyBytes {
			// 长度不对说明存储里的东西被截断或被别的程序改写过。
			// 绝不能"凑合用"——用一把长度不对的密钥加密会静默地
			// 降低安全性，而用户永远不会发现。
			return nil, fmt.Errorf(
				i18n.T("secret.err.bad_len"),
				masterKeyBytes, len(existing))
		}
		m := &Manager{store: store, master: existing}
		return m, nil
	}

	master := make([]byte, masterKeyBytes)
	if _, err := rand.Read(master); err != nil {
		return nil, fmt.Errorf(i18n.T("secret.err.gen_master"), err)
	}
	if err := store.Put(ctx, masterKeyName, master); err != nil {
		return nil, fmt.Errorf(i18n.T("secret.err.save_master"), err)
	}
	return &Manager{store: store, master: master, created: true}, nil
}

// Created 报告本次是否新生成了主密钥。
func (m *Manager) Created() bool { return m.created }

// Encrypt 加密明文，返回可直接存入数据库的信封。
//
// 信封格式：
//
//	[0]      格式版本
//	[1:13]   GCM nonce（每次加密都重新随机生成）
//	[13:]    密文 || 认证标签
//
// nonce 必须每次不同：GCM 在同一密钥下重用 nonce 会灾难性地
// 泄漏明文异或值，并让攻击者能够伪造消息。这里用 crypto/rand 生成，
// 32 字节密钥下 96 位随机 nonce 的碰撞概率可忽略。
func (m *Manager) Encrypt(plaintext []byte) ([]byte, error) {
	gcm, err := m.aead()
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf(i18n.T("secret.err.gen_nonce"), err)
	}

	out := make([]byte, 0, 1+nonceBytes+len(plaintext)+gcm.Overhead())
	out = append(out, envelopeVersion)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, aad), nil
}

// Decrypt 解开由 Encrypt 产生的信封。
func (m *Manager) Decrypt(envelope []byte) ([]byte, error) {
	if len(envelope) < 1+nonceBytes {
		return nil, errors.New(i18n.T("secret.err.short"))
	}
	if envelope[0] != envelopeVersion {
		return nil, fmt.Errorf(i18n.T("secret.err.bad_version"), envelope[0])
	}

	gcm, err := m.aead()
	if err != nil {
		return nil, err
	}

	nonce := envelope[1 : 1+nonceBytes]
	plaintext, err := gcm.Open(nil, nonce, envelope[1+nonceBytes:], aad)
	if err != nil {
		// GCM 的失败不区分"被篡改"与"密钥不对"，这是刻意的设计。
		// 对用户来说两者都意味着"这份密文在当前主密钥下不可读"，
		// 而最常见的原因就是换了机器或换了账户。
		return nil, errors.New(
			i18n.T("secret.err.decrypt"))
	}
	return plaintext, nil
}

// aead 构造 AES-256-GCM。
func (m *Manager) aead() (cipher.AEAD, error) {
	if len(m.master) != masterKeyBytes {
		return nil, fmt.Errorf(i18n.T("secret.err.bad_key_len"), len(m.master))
	}
	block, err := aes.NewCipher(m.master)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("secret.err.new_aes"), err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("secret.err.new_gcm"), err)
	}
	return gcm, nil
}
