package artifacts

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
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
	if _, err := (&Downloader{}).Download(context.Background(), "http://example.com/x.tar.gz", strings.Repeat("a", 64), filepath.Join(t.TempDir(), "x")); !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("plain http must be refused, got %v", err)
	}
	if _, err := (&Downloader{}).Download(context.Background(), "ftp://example.com/x", strings.Repeat("a", 64), filepath.Join(t.TempDir(), "x")); !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("non-http scheme must be refused, got %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := (&Downloader{}).Download(context.Background(), srv.URL, strings.Repeat("a", 64), filepath.Join(t.TempDir(), "x")); !errors.Is(err, ErrBadStatus) {
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

func TestVerifyDetectsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(path, digestOf([]byte("abc"))); err != nil {
		t.Fatalf("verify should pass: %v", err)
	}
	if err := Verify(path, digestOf([]byte("abd"))); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want mismatch, got %v", err)
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
