// Package artifacts 负责把外部产物安全地搬到本机：下载、校验、解压。
//
// # 为什么单独成包
//
// 这三件事在内核里此前**完全不存在**（SHA-256 只用于迁移校验和、ACME
// DNS-01 值与云厂商签名）。而 v0.2.0 要用它们供给语言运行时 —— 也就是
// 把网络上的整份解释器装到用户机器上并执行。因此：
//
//   - 校验不是可选项：每个产物都带固定摘要，边写边算，不符即整份丢弃；
//   - 解压视为处理**不可信输入**：拒绝路径穿越、拒绝逃逸的符号链接，
//     并在创建每一项之前重新确认落点仍在目标目录内；
//   - 失败不留残骸：全部先写临时路径，成功才改名到位。
//
// 错误信息用英文：它们属于内部诊断，用户可见的文案在 API 层经 i18n 目录
// 生成（见 docs/DECISIONS.md D21 的棘轮规则）。
package artifacts

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultMaxBytes 是单个产物的默认体积上限。
//
// 取 2 GiB：比任何真实运行时都大得多（最大的是 .NET SDK，数百 MB），
// 但足以挡住"URL 指向了一个无限流"这类故障 —— 没有上限的话，一个错误的
// 地址会把磁盘写满，而症状是本机莫名其妙没有空间了。
const DefaultMaxBytes = int64(2) << 30

// 错误哨兵。调用方按它们分支，不要去匹配文案。
var (
	// ErrInsecureURL 表示产物地址不是 https。
	ErrInsecureURL = errors.New("artifact URL must use https")
	// ErrChecksumMismatch 表示下载内容的 SHA-256 与固定摘要不符。
	ErrChecksumMismatch = errors.New("artifact checksum mismatch")
	// ErrTooLarge 表示产物超过体积上限。
	ErrTooLarge = errors.New("artifact exceeds the size limit")
	// ErrBadStatus 表示服务端没有返回 2xx。
	ErrBadStatus = errors.New("artifact request failed")
	// ErrBadDigest 表示摘要本身格式不合法（而不是内容不符）。
	ErrBadDigest = errors.New("artifact digest is malformed")
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

// Downloader 下载产物。
type Downloader struct {
	// Client 为空时使用默认客户端。
	Client *http.Client
	// MaxBytes 为 0 时使用 DefaultMaxBytes。
	MaxBytes int64
	// Progress 可选；received 是累计字节数，total 为 -1 表示服务端未给出长度。
	Progress func(received, total int64)
}

func (d *Downloader) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	// 不设整体超时：下载可能持续数分钟，超时应当由 ctx 控制。
	// 但响应头必须限时 —— 否则一个只连不答的服务端会永久挂住任务。
	return &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second,
			Proxy:                 http.ProxyFromEnvironment,
		},
	}
}

func (d *Downloader) maxBytes() int64 {
	if d.MaxBytes > 0 {
		return d.MaxBytes
	}
	return DefaultMaxBytes
}

// Download 把 url 下载到 dest，并在写入过程中校验 wantSHA256。
//
// 语义要点：
//
//   - 只接受 **https**（本机回环上的测试服务除外，见 allowInsecure）；
//   - 内容先写 dest+".part"，校验通过才改名到 dest —— 中断或校验失败
//     都不会在目标位置留下一个看似完整的文件；
//   - 摘要不符时**删除**已下载内容并返回 ErrChecksumMismatch，
//     不做"重试时复用半个文件"这种优化（那会引入新的失败模式）。
//
// 返回值为写入的字节数。
func (d *Downloader) Download(ctx context.Context, rawURL string, want Digest, dest string) (int64, error) {
	if err := validateURL(rawURL); err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("%w: %s returned %s", ErrBadStatus, rawURL, resp.Status)
	}
	total := resp.ContentLength
	if total > d.maxBytes() {
		return 0, fmt.Errorf("%w: %d bytes announced, limit is %d", ErrTooLarge, total, d.maxBytes())
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return 0, err
	}
	partial := dest + ".part"
	out, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}

	var hasher hash.Hash
	if want.Algorithm == "sha512" {
		hasher = sha512.New()
	} else {
		hasher = sha256.New()
	}
	var written int64
	// copyErr 单独保存：即使拷贝失败也要先把文件关掉再返回。
	copyErr := func() error {
		defer func() { _ = out.Close() }()
		buf := make([]byte, 128<<10)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				written += int64(n)
				if written > d.maxBytes() {
					return fmt.Errorf("%w: limit is %d", ErrTooLarge, d.maxBytes())
				}
				if _, err := out.Write(buf[:n]); err != nil {
					return err
				}
				_, _ = hasher.Write(buf[:n])
				if d.Progress != nil {
					d.Progress(written, total)
				}
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}()

	if copyErr != nil {
		_ = os.Remove(partial)
		return written, copyErr
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != want.Hex {
		_ = os.Remove(partial)
		return written, fmt.Errorf("%w: expected %s, got %s:%s", ErrChecksumMismatch, want, want.Algorithm, got)
	}

	// 改名是原子的：目标路径要么不存在，要么就是校验通过的完整内容。
	if err := os.Rename(partial, dest); err != nil {
		_ = os.Remove(partial)
		return written, err
	}
	return written, nil
}

// validateURL 只放行 https。
//
// 例外是回环地址上的 http：本机测试服务（httptest）用它，
// 而回环流量不出机器，不构成降级。
func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "127.0.0.1" || host == "::1" || host == "localhost" {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrInsecureURL, raw)
	default:
		return fmt.Errorf("%w: %s", ErrInsecureURL, raw)
	}
}
