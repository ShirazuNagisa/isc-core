package artifacts

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
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

func digestOf(b []byte) Digest {
	sum := sha256.Sum256(b)
	return Digest{Algorithm: "sha256", Hex: hex.EncodeToString(sum[:])}
}

// --- download ---------------------------------------------------------------

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

func TestDigestVerifyDetectsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := digestOf([]byte("abc")).Verify(path); err != nil {
		t.Fatalf("verify should pass: %v", err)
	}
	if err := digestOf([]byte("abd")).Verify(path); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want mismatch, got %v", err)
	}
}

// .NET 官方只发布 SHA-512，而其余运行时发 SHA-256；两者都要能校验。
func TestParseDigestAcceptsBothAlgorithms(t *testing.T) {
	sha256Sum := sha256.Sum256([]byte("payload"))
	value := hex.EncodeToString(sha256Sum[:])

	bare, err := ParseDigest(value)
	if err != nil || bare.Algorithm != "sha256" {
		t.Fatalf("a bare 64-hex value should mean sha256: %#v err=%v", bare, err)
	}
	prefixed, err := ParseDigest("sha256:" + value)
	if err != nil || prefixed != bare {
		t.Fatalf("prefixed form should match the bare form: %#v err=%v", prefixed, err)
	}

	sha512Sum := sha512.Sum512([]byte("payload"))
	long := hex.EncodeToString(sha512Sum[:])
	parsed, err := ParseDigest("sha512:" + long)
	if err != nil || parsed.Algorithm != "sha512" {
		t.Fatalf("sha512 must be accepted: %#v err=%v", parsed, err)
	}

	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := parsed.Verify(path); err != nil {
		t.Fatalf("sha512 verification should pass: %v", err)
	}
	if err := bare.Verify(path); err != nil {
		t.Fatalf("sha256 verification should pass: %v", err)
	}

	for _, bad := range []string{"", "sha256:zz", "md5:abc", "sha256:" + value[:10], "sha512:" + value} {
		if _, err := ParseDigest(bad); !errors.Is(err, ErrBadDigest) {
			t.Fatalf("ParseDigest(%q) should fail with ErrBadDigest, got %v", bad, err)
		}
	}
}

// --- extraction helpers -----------------------------------------------------

func writeTarGz(t *testing.T, path string, entries []tarEntry) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Linkname: e.link, Typeflag: e.kind, Size: int64(len(e.body))}
		if hdr.Mode == 0 {
			hdr.Mode = 0o644
		}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Typeflag != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg && len(e.body) > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

type tarEntry struct {
	name string
	body string
	link string
	mode int64
	kind byte
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// --- extraction: happy paths ------------------------------------------------

func TestExtractTarGzStripsSingleRoot(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "node.tar.gz")
	writeTarGz(t, archive, []tarEntry{
		{name: "node-v22/bin/node", body: "#!/bin/sh\n", mode: 0o755},
		{name: "node-v22/README.md", body: "hi"},
	})
	dest := filepath.Join(dir, "out")
	if err := Extract(archive, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "node")); err != nil {
		t.Fatalf("stripped layout missing: %v", err)
	}
	info, err := os.Stat(filepath.Join(dest, "bin", "node"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("executable bit was not preserved: %v", info.Mode())
	}
}

func TestExtractZip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "php.zip")
	writeZip(t, archive, map[string]string{"php/php": "binary", "php/php.ini": "x"})
	dest := filepath.Join(dir, "out")
	if err := Extract(archive, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "php")); err != nil {
		t.Fatalf("zip layout missing: %v", err)
	}
}

func TestExtractKeepsSymlinksThatStayInside(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "python.tar.gz")
	writeTarGz(t, archive, []tarEntry{
		{name: "python/bin/python3.13", body: "bin", mode: 0o755},
		{name: "python/bin/python3", link: "python3.13", kind: tar.TypeSymlink},
	})
	dest := filepath.Join(dir, "out")
	if err := Extract(archive, dest); err != nil {
		t.Fatalf("an internal symlink must be allowed: %v", err)
	}
	target, err := os.Readlink(filepath.Join(dest, "bin", "python3"))
	if err != nil || target != "python3.13" {
		t.Fatalf("symlink not preserved: %q err=%v", target, err)
	}
}

// --- extraction: hostile archives -------------------------------------------

func TestExtractRejectsPathTraversal(t *testing.T) {
	// 最后一个是 Windows 风格的分隔符：归档是跨平台产物，
	// 在 Unix 上 `..\evil.txt` 只是一个普通文件名，但仍必须拒绝。
	for _, name := range []string{"../evil.txt", "a/../../evil.txt", "/etc/evil.txt", `..\evil.txt`} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "evil.tar.gz")
			writeTarGz(t, archive, []tarEntry{
				{name: "ok.txt", body: "fine"},
				{name: name, body: "pwned"},
			})
			dest := filepath.Join(dir, "out")
			err := Extract(archive, dest)
			if err == nil {
				t.Fatalf("traversal entry %q must be rejected", name)
			}
			if !errors.Is(err, ErrUnsafeArchiveEntry) {
				t.Fatalf("want ErrUnsafeArchiveEntry, got %v", err)
			}
		})
	}
}

func TestExtractRejectsSymlinkLeavingTheDestination(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "evil.tar.gz")
	writeTarGz(t, archive, []tarEntry{
		{name: "runtime/bin/tool", link: "../../../../../../tmp", kind: tar.TypeSymlink},
	})
	dest := filepath.Join(dir, "out")
	if err := Extract(archive, dest); !errors.Is(err, ErrUnsafeArchiveEntry) {
		t.Fatalf("escaping symlink must be rejected, got %v", err)
	}
}

// 这是最关键的一条：先用一项把某目录变成指向树外的符号链接，再用后续项
// 往那个目录里写文件。只检查最终目标路径是不够的 —— 必须确认路径上
// 没有任何一环是符号链接。
func TestExtractRefusesToWriteThroughAPreExistingSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(filepath.Join(dest, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	// 模拟"目标目录里已经有一个指向树外的符号链接"。
	if err := os.Symlink(outside, filepath.Join(dest, "runtime", "escape")); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(dir, "evil.tar.gz")
	writeTarGz(t, archive, []tarEntry{
		{name: "top/keep.txt", body: "x"},
		{name: "top/runtime/escape/pwned.txt", body: "pwned"},
	})
	if err := Extract(archive, dest); !errors.Is(err, ErrUnsafeArchiveEntry) {
		t.Fatalf("writing through a symlink must be refused, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(err) {
		t.Fatalf("file escaped the destination directory")
	}
}

func TestExtractZipRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.zip")
	writeZip(t, archive, map[string]string{"ok.txt": "fine", "../evil.txt": "pwned"})
	if err := Extract(archive, filepath.Join(dir, "out")); !errors.Is(err, ErrUnsafeArchiveEntry) {
		t.Fatalf("zip traversal must be rejected, got %v", err)
	}
}

func TestExtractEnforcesTotalSizeBudget(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "bomb.tar.gz")
	body := strings.Repeat("A", 64<<10)
	writeTarGz(t, archive, []tarEntry{
		{name: "top/a.bin", body: body},
		{name: "top/b.bin", body: body},
		{name: "top/c.bin", body: body},
	})
	err := ExtractWith(archive, filepath.Join(dir, "out"), ExtractOptions{StripSingleRoot: true, MaxTotalBytes: 128 << 10})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestExtractRejectsUnknownFormat(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "thing.rar")
	if err := os.WriteFile(archive, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Extract(archive, filepath.Join(dir, "out")); !errors.Is(err, ErrUnsupportedArchive) {
		t.Fatalf("want ErrUnsupportedArchive, got %v", err)
	}
}

// 归档里带 setuid 的文件不能把 setuid 带出来。
func TestExtractStripsSetuidBits(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "suid.tar.gz")
	writeTarGz(t, archive, []tarEntry{
		{name: "top/tool", body: "x", mode: 0o4755},
		{name: "top/other", body: "y"},
	})
	dest := filepath.Join(dir, "out")
	if err := Extract(archive, dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dest, "tool"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSetuid != 0 {
		t.Fatalf("setuid bit survived extraction: %v", info.Mode())
	}
}

// 剥不剥顶层目录，决定了解压后可执行文件在哪 —— 而每种运行时的归档布局
// 都不一样（Node/Go 有包装目录，PHP 只有一个裸文件）。这里的每条规则都
// 对应一个真实存在的归档形状。
func TestStripPrefixDecidesByShape(t *testing.T) {
	cases := []struct {
		name  string
		names []string
		dirs  map[string]bool
		want  string
	}{
		{
			name:  "单个裸文件不剥（PHP 的归档就是一个 php）",
			names: []string{"php"},
			want:  "",
		},
		{
			name:  "单个被包了一层的文件要剥",
			names: []string{"php-8.5/bin/php"},
			want:  "php-8.5",
		},
		{
			name:  "单个显式目录项要剥",
			names: []string{"pkg/"},
			dirs:  map[string]bool{"pkg/": true},
			want:  "pkg",
		},
		{
			name:  "同一顶层下的多项要剥",
			names: []string{"go/bin/go", "go/pkg/tool", "go/README"},
			want:  "go",
		},
		{
			name:  "顶层不唯一时不剥",
			names: []string{"a/x", "b/y"},
			want:  "",
		},
		{
			name:  "带 ./ 前缀也能认出来",
			names: []string{"./pkg/a", "./pkg/b"},
			want:  "pkg",
		},
		{
			name:  "空归档不剥",
			names: nil,
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripPrefix(tc.names, tc.dirs); got != tc.want {
				t.Fatalf("stripPrefix = %q, want %q", got, tc.want)
			}
		})
	}
}

// 单文件归档里被包了一层时，解压后必须能找到那个文件。
func TestExtractStripsASingleWrappedFile(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "wrapped.tar.gz")
	writeTarGz(t, archive, []tarEntry{
		{name: "php-8.5/bin/php", body: "binary", mode: 0o755},
	})
	dest := filepath.Join(dir, "out")

	if err := ExtractWith(archive, dest, ExtractOptions{StripSingleRoot: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "php")); err != nil {
		t.Fatalf("the wrapped file should have been stripped: %v", err)
	}
}

// 而一个裸文件归档**不能**被剥掉，否则解压出来是空的。
func TestExtractKeepsABareSingleFile(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "bare.tar.gz")
	writeTarGz(t, archive, []tarEntry{
		{name: "php", body: "binary", mode: 0o755},
	})
	dest := filepath.Join(dir, "out")

	if err := ExtractWith(archive, dest, ExtractOptions{StripSingleRoot: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "php")); err != nil {
		t.Fatalf("a bare single-file archive must not be stripped away: %v", err)
	}
}

// 传输中途断掉时必须续传，而不是从头再来。
//
// 运行时是几十到几百 MB，实测一个 200 MB 的 JDK 下到 69% 被链路掐断。
// 没有续传的话，前面十几分钟全部作废，而用户看到的是"运行时装不上"。
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
