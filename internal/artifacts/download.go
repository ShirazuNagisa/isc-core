//go:build !appstore

// 下载器。
//
// # 为什么整个文件被 appstore 标签排除
//
// App Review 2.5.2 禁止应用下载并执行代码。打上 appstore 标签构建时，
// 这个文件**根本不参与编译** —— 不是"关掉一个开关"，而是那份二进制里
// 没有这段代码。取回逻辑（HTTP 客户端、断点续传、重试退避）一行都不在。
//
// 校验与解压保留：包内预置的运行时走的是同一条校验 → 解压 → 落位链路。
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
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/sysproxy"
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
	// ErrBadStatus 表示服务端没有返回 2xx。
	ErrBadStatus = errors.New("artifact request failed")
)

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
		Transport: sysproxy.Transport(&http.Transport{
			ResponseHeaderTimeout: 30 * time.Second,
		}),
	}
}

func (d *Downloader) maxBytes() int64 {
	if d.MaxBytes > 0 {
		return d.MaxBytes
	}
	return DefaultMaxBytes
}

// maxDownloadAttempts 是单个产物的下载尝试次数。
//
// 运行时都是几十到几百 MB，而家用网络的连接会中途断掉 —— 实测一个
// 200 MB 的 JDK 下到 69% 被掐断。没有续传的话，前面十几分钟全部作废，
// 用户看到的是"装不上运行时"，而网络其实一直是通的。
const maxDownloadAttempts = 5

// Download 把 url 下载到 dest，并在写入过程中校验 want。
//
// 语义要点：
//
//   - 只接受 **https**（本机回环上的测试服务除外，见 allowInsecure）；
//   - 内容先写 dest+".part"，校验通过才改名到 dest —— 中断或校验失败
//     都不会在目标位置留下一个看似完整的文件；
//   - 传输中断**保留**已下载的部分，下次带 Range 续传：文件大、链路差时
//     这是"能不能装上"和"装不上"的区别；
//   - 摘要不符时删除全部内容并返回 ErrChecksumMismatch，绝不把半个文件
//     当成完整的用 —— 续传的前提是最终整份校验，而不是相信分片。
//
// 返回值为写入的字节数。
func (d *Downloader) Download(ctx context.Context, rawURL string, want Digest, dest string) (int64, error) {
	if err := validateURL(rawURL); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return 0, err
	}
	partial := dest + ".part"

	var hasher hash.Hash
	if want.Algorithm == "sha512" {
		hasher = sha512.New()
	} else {
		hasher = sha256.New()
	}

	var written int64
	var lastErr error
	for attempt := 1; attempt <= maxDownloadAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			_ = os.Remove(partial)
			return written, err
		}
		transient, err := d.fetch(ctx, rawURL, partial, hasher, &written)
		if err == nil {
			return written, d.finish(partial, dest, want, hasher, written)
		}
		lastErr = err
		if !transient {
			_ = os.Remove(partial)
			return written, err
		}
		if attempt < maxDownloadAttempts {
			select {
			case <-ctx.Done():
				_ = os.Remove(partial)
				return written, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
	}
	_ = os.Remove(partial)
	return written, fmt.Errorf("download failed after %d attempts: %w", maxDownloadAttempts, lastErr)
}

// finish 校验摘要并把临时文件改名到位。
func (d *Downloader) finish(partial, dest string, want Digest, hasher hash.Hash, written int64) error {
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != want.Hex {
		_ = os.Remove(partial)
		return fmt.Errorf("%w: expected %s, got %s:%s", ErrChecksumMismatch, want, want.Algorithm, got)
	}
	// 改名是原子的：目标路径要么不存在，要么就是校验通过的完整内容。
	if err := os.Rename(partial, dest); err != nil {
		_ = os.Remove(partial)
		return err
	}
	return nil
}

// fetch 发起一次下载尝试，从 written 处续传。
//
// 返回的 transient 表示"这个失败值得再试一次"（链路问题），而不是
// 服务端明确拒绝或内容确实不对。
func (d *Downloader) fetch(ctx context.Context, rawURL, partial string, hasher hash.Hash, written *int64) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false, err
	}
	if *written > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", *written))
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return true, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 408/429/5xx 是"稍后再来"，其余是明确拒绝。
		retry := resp.StatusCode == http.StatusRequestTimeout ||
			resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode >= 500
		return retry, fmt.Errorf("%w: %s returned %s", ErrBadStatus, rawURL, resp.Status)
	}

	// 服务端不支持 Range（回了 200 而不是 206）就只能从头来：
	// 否则会把整份内容接在半截文件后面，得到一份长度翻倍的东西。
	if *written > 0 && resp.StatusCode != http.StatusPartialContent {
		*written = 0
		hasher.Reset()
	}

	total := *written + resp.ContentLength
	if resp.ContentLength >= 0 && total > d.maxBytes() {
		return false, fmt.Errorf("%w: %d bytes announced, limit is %d", ErrTooLarge, total, d.maxBytes())
	}

	flags := os.O_CREATE | os.O_WRONLY
	if *written > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	out, err := os.OpenFile(partial, flags, 0o600)
	if err != nil {
		return false, err
	}

	copyErr := func() error {
		defer func() { _ = out.Close() }()
		buf := make([]byte, 128<<10)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				*written += int64(n)
				if *written > d.maxBytes() {
					return fmt.Errorf("%w: limit is %d", ErrTooLarge, d.maxBytes())
				}
				if _, err := out.Write(buf[:n]); err != nil {
					return err
				}
				_, _ = hasher.Write(buf[:n])
				if d.Progress != nil {
					d.Progress(*written, *written+resp.ContentLength)
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
		// 刻意**不删** .part：下一次尝试从这里续传。
		if errors.Is(copyErr, ErrTooLarge) || ctx.Err() != nil {
			return false, copyErr
		}
		return true, copyErr
	}
	// 声明了长度却没给够：这是被截断的响应，续传前不能当作完成。
	if resp.ContentLength >= 0 && resp.ContentLength > 0 {
		if got := *written; got < total {
			return true, fmt.Errorf("%w: got %d of %d bytes", io.ErrUnexpectedEOF, got, total)
		}
	}
	return false, nil
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
