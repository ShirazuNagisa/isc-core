//go:build !appstore

// 下载器的测试。
//
// 整个文件跟着下载器一起被 appstore 标签排除：上架版本里没有下载代码，
// 这些测试自然也无从跑起。解压与摘要的测试**不在这里** —— 那两样在上架
// 版本里照样要用，所以留在 artifacts_test.go 里，两种构建都跑。
package artifacts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDownloadVerifiesDigestAndLeavesNoPartialFile(t *testing.T) {
	payload := []byte("runtime-archive-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.tar.gz")
	d := &Downloader{}
	n, err := d.Download(context.Background(), srv.URL, digestOf(payload), dest)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("wrote %d bytes, want %d", n, len(payload))
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("content mismatch: err=%v", err)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatalf("partial file was left behind")
	}
}

func TestDownloadRejectsWrongDigestAndRemovesTheFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("tampered"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.tar.gz")
	_, err := (&Downloader{}).Download(context.Background(), srv.URL, digestOf([]byte("expected")), dest)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
	// 校验失败绝不能留下一个"看起来完整"的文件。
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("destination must not exist after a checksum failure")
	}
	if _, statErr := os.Stat(dest + ".part"); !os.IsNotExist(statErr) {
		t.Fatalf("partial file must be removed after a checksum failure")
	}
}

func TestDownloadRejectsPlainHTTPAndBadStatus(t *testing.T) {
	if _, err := (&Downloader{}).Download(context.Background(), "http://example.com/x.tar.gz", Digest{Algorithm: "sha256", Hex: strings.Repeat("a", 64)}, filepath.Join(t.TempDir(), "x")); !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("plain http must be refused, got %v", err)
	}
	if _, err := (&Downloader{}).Download(context.Background(), "ftp://example.com/x", Digest{Algorithm: "sha256", Hex: strings.Repeat("a", 64)}, filepath.Join(t.TempDir(), "x")); !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("non-http scheme must be refused, got %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := (&Downloader{}).Download(context.Background(), srv.URL, Digest{Algorithm: "sha256", Hex: strings.Repeat("a", 64)}, filepath.Join(t.TempDir(), "x")); !errors.Is(err, ErrBadStatus) {
		t.Fatalf("404 must surface as ErrBadStatus, got %v", err)
	}
}

func TestDownloadEnforcesSizeLimit(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "big.bin")
	_, err := (&Downloader{MaxBytes: 1024}).Download(context.Background(), srv.URL, digestOf(payload), dest)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("oversized download must not be kept")
	}
}

func TestDownloadResumesAfterConnectionDrop(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 64<<10) // 1 MiB
	var attempts int
	var ranged []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		ranged = append(ranged, r.Header.Get("Range"))
		if attempts == 1 {
			// 先正常回一段，再粗暴断开 —— 模拟链路中断。
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			w.Write(payload[:len(payload)/3])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler)
		}
		// 第二次：按 Range 续传。
		start := 0
		if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
			fmt.Sscanf(rng, "bytes=%d-", &start)
		}
		w.Header().Set("Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)-start))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(payload[start:])
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	d := &Downloader{}
	if _, err := d.Download(context.Background(), srv.URL, digestOf(payload), dest); err != nil {
		t.Fatalf("续传后仍然失败：%v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("内容不对：拿到 %d 字节，期望 %d", len(got), len(payload))
	}
	if attempts < 2 {
		t.Fatalf("只尝试了 %d 次，没有续传", attempts)
	}
	if ranged[len(ranged)-1] == "" {
		t.Fatal("重试时没有带 Range 头")
	}
}

// 服务端不支持 Range（回 200 而不是 206）时，必须从头写，
// 否则会把整份内容接在半截文件后面，得到一份长度翻倍的东西。
func TestDownloadRestartsWhenServerIgnoresRange(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefgh"), 32<<10)
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		if attempts == 1 {
			w.Write(payload[:len(payload)/2])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler)
		}
		// 刻意忽略 Range，永远回 200 全量。
		w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	d := &Downloader{}
	if _, err := d.Download(context.Background(), srv.URL, digestOf(payload), dest); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, payload) {
		t.Fatalf("内容不对：拿到 %d 字节，期望 %d（多半是续传时没有重置）", len(got), len(payload))
	}
}

// 摘要不符要整份丢弃，并且不留残骸。
func TestDownloadChecksumMismatchLeavesNothing(t *testing.T) {
	payload := []byte("真实内容")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	wrong := digestOf([]byte("别的内容"))
	d := &Downloader{}
	if _, err := d.Download(context.Background(), srv.URL, wrong, dest); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("期望 ErrChecksumMismatch，得到 %v", err)
	}
	for _, name := range []string{"out.bin", "out.bin.part"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s 不该留下", name)
		}
	}
}

// 服务端明确说"没有"时不要反复重试。
func TestDownloadDoesNotRetryOn404(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	d := &Downloader{}
	_, err := d.Download(context.Background(), srv.URL, digestOf([]byte("x")),
		filepath.Join(t.TempDir(), "out.bin"))
	if !errors.Is(err, ErrBadStatus) {
		t.Fatalf("期望 ErrBadStatus，得到 %v", err)
	}
	if attempts != 1 {
		t.Fatalf("404 重试了 %d 次，不该重试", attempts)
	}
}
