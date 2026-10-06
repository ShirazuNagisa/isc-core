//go:build !appstore

// 供应链路的测试：下载 → 校验 → 解压 → 标记 → 落位。
//
// 整个文件跟着下载器一起被 appstore 标签排除 —— 这个文件里的每一条都要
// 起一个 httptest 服务把归档送出去，而在上架构建里没有取回这一步。
//
// 上架构建该有的行为由 offline_appstore_test.go 覆盖（缺运行时 → 报"没被
// 打进包里"，以及这份构建不认为自己能下载）。
package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/artifacts"
)

// 这一组测试把"下载 → 校验摘要 → 解压 → 写标记 → 落位"整条链路跑通。
//
// 此前这条链路只被验证到"没有发行版就报错"为止 —— 真正会下载的那一段
// 从没跑过，因为它需要联网取几百 MB。用一个注入的发行版 + 本地 httptest
// 服务，就能在毫秒级覆盖它，并且能断言那些**只在失败时**才看得出来的性质
// （摘要不匹配不能留下半个运行时、解压缺文件不能留下残留目录）。

// buildTarGz 造一个内存里的 tar.gz。
//
// entries 的键是归档内的路径，值是文件内容。值为 nil 表示目录。
func buildTarGz(t *testing.T, entries map[string][]byte, modes map[string]int64) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range entries {
		mode := int64(0o644)
		if modes != nil {
			if custom, ok := modes[name]; ok {
				mode = custom
			}
		}
		if content == nil {
			if err := tarWriter.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := tarWriter.WriteHeader(&tar.Header{
			Name: name, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(content)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func sha256Of(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fakeRelease 起一个本地服务提供合成归档，并返回配套的 Manager。
func fakeRelease(t *testing.T, archive []byte, digest string, executable string, stripRoot bool) (*Manager, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release.tar.gz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)

	dataRoot := t.TempDir()
	manager := NewManager(dataRoot, "darwin", "arm64")
	// 关键：不给它任何系统解释器，否则它会走"系统已装"那条路，下载路径
	// 根本不会被执行。
	manager.lookPath = func(string) (string, error) { return "", errors.New("not installed") }
	manager.runVersion = func(context.Context, string, []string) (string, error) {
		return "", errors.New("not installed")
	}
	// httptest 起在 127.0.0.1 上，而下载器只允许回环地址走 http。
	manager.artifacts = []Artifact{{
		Kind: KindNode, Version: "22.14.0", Platform: "darwin/arm64",
		URL: server.URL + "/release.tar.gz", Digest: digest,
		Archive: "release.tar.gz", StripRoot: stripRoot, Executable: executable,
		SizeBytes: int64(len(archive)), License: "MIT", Source: "test",
	}}
	return manager, server
}

// 一条完整的成功路径。
func TestProvisionDownloadsVerifiesExtractsAndInstalls(t *testing.T) {
	executable := []byte("#!/bin/sh\necho v22.14.0\n")
	archive := buildTarGz(t, map[string][]byte{
		"node-v22.14.0-darwin-arm64/bin/node": executable,
		"node-v22.14.0-darwin-arm64/README":   []byte("hi"),
	}, map[string]int64{"node-v22.14.0-darwin-arm64/bin/node": 0o755})

	manager, _ := fakeRelease(t, archive, sha256Of(archive), "bin/node", true)

	var fractions []float64
	installed, err := manager.Provision(context.Background(), KindNode, "18.0.0", func(fraction float64, _ string) {
		fractions = append(fractions, fraction)
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	// 1. 落位正确：可执行文件在，并且**真的能跑**。
	if installed.Source != "managed" {
		t.Fatalf("source = %q", installed.Source)
	}
	if installed.Version != "22.14.0" {
		t.Fatalf("version = %q", installed.Version)
	}
	output, err := exec.Command(installed.Executable).CombinedOutput()
	if err != nil {
		t.Fatalf("the installed executable must be runnable: %v (%s)", err, output)
	}
	if !strings.Contains(string(output), "22.14.0") {
		t.Fatalf("unexpected output: %q", output)
	}

	// 2. 标记文件写了，且内容能说明它是哪来的（D29 的再分发登记靠它）。
	marker, err := readMarker(installed.Root)
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	if marker.Digest != sha256Of(archive) || marker.Kind != KindNode {
		t.Fatalf("unexpected marker: %#v", marker)
	}
	if marker.InstalledAt.IsZero() {
		t.Fatalf("the marker should record when it was installed")
	}

	// 3. 归档不留在缓存里：已经解压并校验过，留着只占磁盘。
	if entries, err := os.ReadDir(manager.cache); err == nil && len(entries) != 0 {
		t.Fatalf("the archive should not be kept, found %v", entries)
	}
	// 4. 解压用的中间目录也不该留下。
	if entries, err := os.ReadDir(manager.root); err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".staging-") {
				t.Fatalf("a staging directory was left behind: %s", entry.Name())
			}
		}
	}

	// 5. 进度是单调不减的，并且最后到 1。
	if len(fractions) == 0 {
		t.Fatalf("progress was never reported")
	}
	for i := 1; i < len(fractions); i++ {
		if fractions[i] < fractions[i-1] {
			t.Fatalf("progress went backwards: %v", fractions)
		}
	}
	if last := fractions[len(fractions)-1]; last != 1 {
		t.Fatalf("progress should end at 1, got %v", last)
	}

	// 6. 装完之后 Resolve 认它 —— 也就是第二次部署不会再下载一遍。
	found, ok, err := manager.Resolve(context.Background(), KindNode, "18.0.0")
	if err != nil || !ok || found.Source != "managed" {
		t.Fatalf("the installed runtime should resolve afterwards: %#v ok=%v err=%v", found, ok, err)
	}
}

// 摘要不匹配必须**什么都不留下**。
//
// 这是整条供应链上最重要的一条性质：一个校验失败的发行版绝不能以任何
// 形式留在磁盘上，否则下一次部署可能就把半个坏掉的解释器当成可用的。
func TestProvisionRejectsTamperedReleaseAndLeavesNothingBehind(t *testing.T) {
	archive := buildTarGz(t, map[string][]byte{
		"node/bin/node": []byte("#!/bin/sh\necho v22.14.0\n"),
	}, nil)
	// 故意写一个不同的摘要（模拟上游换了内容或传输被篡改）。
	manager, _ := fakeRelease(t, archive, sha256Of([]byte("something else")), "bin/node", true)

	_, err := manager.Provision(context.Background(), KindNode, "", nil)
	if !errors.Is(err, artifacts.ErrChecksumMismatch) {
		t.Fatalf("want a checksum mismatch, got %v", err)
	}
	assertNoInstalledRuntime(t, manager, KindNode)
}

func TestProvisionRejectsAnArchiveWithoutTheExpectedExecutable(t *testing.T) {
	// 归档里只有别的东西：装上去也会在"启动"时才发现，因此必须在落位前挡住。
	archive := buildTarGz(t, map[string][]byte{
		"node/README": []byte("no binary here"),
	}, nil)
	manager, _ := fakeRelease(t, archive, sha256Of(archive), "bin/node", true)

	_, err := manager.Provision(context.Background(), KindNode, "", nil)
	if !errors.Is(err, ErrExecutableMissing) {
		t.Fatalf("want ErrExecutableMissing, got %v", err)
	}
	assertNoInstalledRuntime(t, manager, KindNode)
}

func TestProvisionReportsAnUnreachableRelease(t *testing.T) {
	archive := buildTarGz(t, map[string][]byte{"node/bin/node": []byte("x")}, nil)
	manager, server := fakeRelease(t, archive, sha256Of(archive), "bin/node", true)
	server.Close() // 服务没了

	_, err := manager.Provision(context.Background(), KindNode, "", nil)
	if err == nil {
		t.Fatalf("an unreachable release must fail")
	}
	assertNoInstalledRuntime(t, manager, KindNode)
}

// 解压时剥掉单一顶层目录，且**只有**单一顶层目录时才剥。
func TestProvisionHandlesBothArchiveLayouts(t *testing.T) {
	payload := []byte("#!/bin/sh\necho ok\n")

	// 有顶层目录：剥掉之后是 bin/node。
	withRoot := buildTarGz(t, map[string][]byte{
		"package/bin/node": payload,
	}, map[string]int64{"package/bin/node": 0o755})
	manager, _ := fakeRelease(t, withRoot, sha256Of(withRoot), "bin/node", true)
	if _, err := manager.Provision(context.Background(), KindNode, "", nil); err != nil {
		t.Fatalf("strip-root layout failed: %v", err)
	}

	// 没有顶层目录（PHP 的归档就是这样，只有一个文件）：不剥。
	flat := buildTarGz(t, map[string][]byte{"php": payload}, map[string]int64{"php": 0o755})
	manager, _ = fakeRelease(t, flat, sha256Of(flat), "php", false)
	if _, err := manager.Provision(context.Background(), KindNode, "", nil); err != nil {
		t.Fatalf("flat layout failed: %v", err)
	}
}

// 归档里的权限位常常不带可执行位；落位前必须补上。
//
// 不补的话，运行时会以 "permission denied" 失败 —— 而那个信息离
// "解压时没恢复权限"很远。
func TestProvisionRestoresTheExecutableBit(t *testing.T) {
	archive := buildTarGz(t, map[string][]byte{
		"node/bin/node": []byte("#!/bin/sh\necho ok\n"),
	}, map[string]int64{"node/bin/node": 0o644}) // 归档里没有可执行位
	manager, _ := fakeRelease(t, archive, sha256Of(archive), "bin/node", true)

	installed, err := manager.Provision(context.Background(), KindNode, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(installed.Executable)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("the executable bit was not restored: %v", info.Mode())
	}
	if output, err := exec.Command(installed.Executable).CombinedOutput(); err != nil {
		t.Fatalf("not runnable: %v (%s)", err, output)
	}
}

// 供给过的运行时可以被删除，并且删干净。
func TestProvisionThenRemove(t *testing.T) {
	archive := buildTarGz(t, map[string][]byte{
		"node/bin/node": []byte("#!/bin/sh\necho ok\n"),
	}, map[string]int64{"node/bin/node": 0o755})
	manager, _ := fakeRelease(t, archive, sha256Of(archive), "bin/node", true)
	if _, err := manager.Provision(context.Background(), KindNode, "", nil); err != nil {
		t.Fatal(err)
	}

	removed, err := manager.Remove(KindNode)
	if err != nil || !removed {
		t.Fatalf("remove: removed=%v err=%v", removed, err)
	}
	if _, ok, _ := manager.Resolve(context.Background(), KindNode, ""); ok {
		t.Fatalf("a removed runtime must not resolve")
	}
	if _, err := os.Stat(filepath.Join(manager.Root(), string(KindNode))); !os.IsNotExist(err) {
		t.Fatalf("the runtime directory should be gone")
	}
}

func assertNoInstalledRuntime(t *testing.T, manager *Manager, kind Kind) {
	t.Helper()
	if _, ok, _ := manager.Resolve(context.Background(), kind, ""); ok {
		t.Fatalf("a failed provision must not leave a usable runtime")
	}
	if entries, err := os.ReadDir(filepath.Join(manager.Root(), string(kind))); err == nil && len(entries) > 0 {
		t.Fatalf("a failed provision left files behind: %v", entries)
	}
	if entries, err := os.ReadDir(manager.Root()); err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".staging-") {
				t.Fatalf("a failed provision left a staging directory: %s", entry.Name())
			}
		}
	}
	// 失败时缓存里的半成品也要清掉，否则下次解压会撞上"目标已存在"。
	if entries, err := os.ReadDir(manager.cache); err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".part") {
				t.Fatalf("a partial download was left behind: %s", entry.Name())
			}
		}
	}
}
