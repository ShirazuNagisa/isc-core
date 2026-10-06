package artifacts

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
)

// 共享的原语：内容摘要与体积上限。
//
// # 为什么与下载器分开成文件
//
// 它**两种构建都要**。上架版本没有下载器（App Review 2.5.2 禁止应用下载并
// 执行代码），但包内预置的运行时照样要过校验 —— "它在包里"不是跳过校验的
// 理由，包里的东西同样可能被换掉。

// 错误哨兵。调用方按它们分支，不要去匹配文案。
var (
	// ErrChecksumMismatch 表示内容与固定摘要不符。
	ErrChecksumMismatch = errors.New("artifact checksum mismatch")
	// ErrBadDigest 表示摘要本身格式不合法（而不是内容不符）。
	ErrBadDigest = errors.New("artifact digest is malformed")
	// ErrTooLarge 表示产物超过体积上限。
	//
	// 与摘要放在一起而不是跟着下载器：解压也要用它（一个压缩炸弹解出来的
	// 东西可以远超归档本身），所以两种构建都要有。
	ErrTooLarge = errors.New("artifact exceeds the size limit")
)

// Digest 是一个内容摘要：算法 + 十六进制值。
//
// 为什么要带算法：.NET 官方只发布 SHA-512，而 Node/Go/Python/PHP/Temurin
// 发 SHA-256。早期版本把摘要硬编码成 SHA-256，遇到 .NET 就只能放弃校验
// 或换源 —— 而"放弃校验"等于把整条供应链的信任建立在 HTTPS 上。
type Digest struct {
	Algorithm string
	Hex       string
}

// ParseDigest 解析 "sha256:<hex>" / "sha512:<hex>"，也接受裸的 64 位
// 十六进制（按 SHA-256 处理）。
func ParseDigest(raw string) (Digest, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	algorithm := "sha256"
	if prefix, rest, found := strings.Cut(value, ":"); found {
		algorithm = prefix
		value = rest
	}
	switch algorithm {
	case "sha256":
		if len(value) != 64 {
			return Digest{}, fmt.Errorf("%w: sha256 needs 64 hex characters", ErrBadDigest)
		}
	case "sha512":
		if len(value) != 128 {
			return Digest{}, fmt.Errorf("%w: sha512 needs 128 hex characters", ErrBadDigest)
		}
	default:
		return Digest{}, fmt.Errorf("%w: unsupported algorithm %q", ErrBadDigest, algorithm)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return Digest{}, fmt.Errorf("%w: not hexadecimal", ErrBadDigest)
	}
	return Digest{Algorithm: algorithm, Hex: value}, nil
}

func (d Digest) String() string { return d.Algorithm + ":" + d.Hex }

// Verify 校验一个文件的内容摘要。
func (d Digest) Verify(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var hasher hash.Hash
	switch d.Algorithm {
	case "sha512":
		hasher = sha512.New()
	default:
		hasher = sha256.New()
	}
	if _, err := io.Copy(hasher, f); err != nil {
		return err
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != d.Hex {
		return fmt.Errorf("%w: expected %s, got sha256:%s", ErrChecksumMismatch, d, got)
	}
	return nil
}
