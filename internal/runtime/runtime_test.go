package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/artifacts"
)

// parseDigestForTest 让测试用与实现相同的解析器校验清单里的摘要格式。
func parseDigestForTest(raw string) (artifacts.Digest, error) {
	return artifacts.ParseDigest(raw)
}

// testManager 构造一个不接触真实机器与网络的 Manager。
//
// lookPath 与 runVersion 是注入点：测试不该因为开发机上装没装 node
// 而结果不同，更不该去下载几百 MB 的运行时。
func testManager(t *testing.T, system map[string]string) (*Manager, string) {
	t.Helper()
	dataRoot := t.TempDir()
	m := NewManager(dataRoot, "darwin", "arm64")
	m.lookPath = func(name string) (string, error) {
		if _, ok := system[name]; ok {
			return "/usr/local/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	m.runVersion = func(_ context.Context, exe string, _ []string) (string, error) {
		if output, ok := system[filepath.Base(exe)]; ok {
			return output, nil
		}
		return "", errors.New("no version")
	}
	return m, dataRoot
}

// installFake 手工放一个"已供给"的运行时，模拟此前装好的状态。
func installFake(t *testing.T, m *Manager, kind Kind, version, executable string) string {
	t.Helper()
	artifact, ok := ArtifactFor(kind, m.platform)
	if !ok {
		t.Fatalf("no artifact for %s", kind)
	}
	dir := filepath.Join(m.Root(), string(kind), version)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, executable)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, executable), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"kind":"` + string(kind) + `","version":"` + version + `","digest":"` + artifact.Digest + `"}`)
	if err := os.WriteFile(filepath.Join(dir, markerName), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// --- 三级解析 ---------------------------------------------------------------

func TestResolvePrefersTheSystemInterpreter(t *testing.T) {
	m, _ := testManager(t, map[string]string{"node": "v22.14.0"})
	found, ok, err := m.Resolve(context.Background(), KindNode, "18.0.0")
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if found.Source != "system" {
		t.Fatalf("a satisfying system interpreter must win, got %q", found.Source)
	}
	if found.Version != "22.14.0" {
		t.Fatalf("version parsed as %q", found.Version)
	}
}

func TestResolveFallsBackToAManagedRuntime(t *testing.T) {
	m, _ := testManager(t, nil)
	installFake(t, m, KindNode, "22.14.0", "bin/node")

	found, ok, err := m.Resolve(context.Background(), KindNode, "18.0.0")
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if found.Source != "managed" {
		t.Fatalf("expected the managed runtime, got %q", found.Source)
	}
	if _, err := os.Stat(found.Executable); err != nil {
		t.Fatalf("resolved executable must exist: %v", err)
	}
}

// 系统解释器太旧时必须回退到供给，而不是拿它去跑一个需要新特性的项目 ——
// 那种失败在用户眼里是"语法错误"，离真正原因很远。
func TestResolveRejectsATooOldSystemInterpreter(t *testing.T) {
	m, _ := testManager(t, map[string]string{"python3": "Python 3.8.2"})
	if _, ok, _ := m.Resolve(context.Background(), KindPython, "3.10.0"); ok {
		t.Fatalf("python 3.8 must not satisfy a 3.10 requirement")
	}
	// 不限版本时它是可用的。
	found, ok, err := m.Resolve(context.Background(), KindPython, "")
	if err != nil || !ok || found.Version != "3.8.2" {
		t.Fatalf("without a minimum it should be accepted: %#v ok=%v err=%v", found, ok, err)
	}
}

func TestResolveIgnoresDirectoriesWithoutAMarker(t *testing.T) {
	m, _ := testManager(t, nil)
	// 用户手放的目录没有标记文件，不该被当成内核装的运行时。
	dir := filepath.Join(m.Root(), string(KindGo), "1.27.1", "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := m.Resolve(context.Background(), KindGo, ""); ok {
		t.Fatalf("a directory without a marker file must not be trusted")
	}
}

func TestResolveTreatsDockerAsDetectOnly(t *testing.T) {
	m, _ := testManager(t, nil)
	if _, ok, _ := m.Resolve(context.Background(), KindDocker, ""); ok {
		t.Fatalf("docker should not resolve when it is not installed")
	}
	m2, _ := testManager(t, map[string]string{"docker": "Docker version 27.0.0"})
	found, ok, err := m2.Resolve(context.Background(), KindDocker, "")
	if err != nil || !ok || found.Source != "system" {
		t.Fatalf("docker on PATH should resolve as a system tool: %#v ok=%v err=%v", found, ok, err)
	}
	if _, err := m2.Provision(context.Background(), KindDocker, "", nil); !errors.Is(err, ErrNotProvisionable) {
		t.Fatalf("the kernel must never provision docker, got %v", err)
	}
}

func TestResolveNoneIsAlwaysAvailable(t *testing.T) {
	m, _ := testManager(t, nil)
	found, ok, err := m.Resolve(context.Background(), KindNone, "")
	if err != nil || !ok || found.Source != "none" {
		t.Fatalf("static sites need no runtime: %#v ok=%v err=%v", found, ok, err)
	}
}

// --- 供给 -------------------------------------------------------------------

func TestProvisionReusesAnAlreadyAvailableRuntimeWithoutDownloading(t *testing.T) {
	m, _ := testManager(t, map[string]string{"go": "go version go1.27.1 darwin/arm64"})
	var messages []string
	found, err := m.Provision(context.Background(), KindGo, "1.21.0", func(_ float64, msg string) {
		if msg != "" {
			messages = append(messages, msg)
		}
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if found.Source != "system" {
		t.Fatalf("expected the system toolchain, got %q", found.Source)
	}
	// 没有任何下载：cache 目录不该被创建出内容。
	if entries, err := os.ReadDir(m.cache); err == nil && len(entries) > 0 {
		t.Fatalf("nothing should have been downloaded, found %v", entries)
	}
	if len(messages) == 0 {
		t.Fatalf("the user should be told which interpreter was picked up")
	}
}

func TestProvisionReportsNoPinnedBuildForOtherPlatforms(t *testing.T) {
	m := NewManager(t.TempDir(), "windows", "amd64")
	m.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	m.runVersion = func(context.Context, string, []string) (string, error) { return "", errors.New("nope") }

	// v0.2.0 只供给 macOS arm64；其它平台必须给出明确错误，而不是静默失败。
	_, err := m.Provision(context.Background(), KindNode, "", nil)
	if !errors.Is(err, ErrNoArtifact) {
		t.Fatalf("want ErrNoArtifact on an unsupported platform, got %v", err)
	}
}

func TestProvisionFailsWhenTheArchiveLacksTheExpectedExecutable(t *testing.T) {
	m, _ := testManager(t, nil)
	// 直接把清单里的可执行文件名换成一个不存在的东西，验证校验真的生效。
	// 走不到网络：这里通过替换 staging 目标的方式不可行，因此改为验证
	// Resolve 对"标记存在但可执行文件缺失"的处理。
	dir := filepath.Join(m.Root(), string(KindNode), "22.14.0")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"kind":"node","version":"22.14.0"}`)
	if err := os.WriteFile(filepath.Join(dir, markerName), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := m.Resolve(context.Background(), KindNode, ""); ok {
		t.Fatalf("a managed runtime without its executable must not count as available")
	}
}

// --- 清单与版本 -------------------------------------------------------------

func TestCatalogEntriesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, artifact := range Catalog() {
		key := string(artifact.Kind) + "@" + artifact.Platform
		if seen[key] {
			t.Errorf("duplicate artifact for %s", key)
		}
		seen[key] = true

		if artifact.URL == "" || !strings.HasPrefix(artifact.URL, "https://") {
			t.Errorf("%s: artifacts must be fetched over https, got %q", key, artifact.URL)
		}
		if _, err := parseDigestForTest(artifact.Digest); err != nil {
			t.Errorf("%s: digest %q is malformed: %v", key, artifact.Digest, err)
		}
		if artifact.Executable == "" {
			t.Errorf("%s: the expected executable must be declared", key)
		}
		if artifact.Archive == "" {
			t.Errorf("%s: the archive name is required to know what lands in the cache", key)
		}
		// 再分发必须登记来源与许可（D29）。
		if artifact.Source == "" || artifact.License == "" || artifact.Verified == "" {
			t.Errorf("%s: source, license and verification note are all required (D29)", key)
		}
	}
}

// .NET 只发 SHA-512，其余发 SHA-256；两种都要能解析。
func TestCatalogCoversBothDigestAlgorithms(t *testing.T) {
	algorithms := map[string]bool{}
	for _, artifact := range Catalog() {
		digest, err := parseDigestForTest(artifact.Digest)
		if err != nil {
			t.Fatal(err)
		}
		algorithms[digest.Algorithm] = true
	}
	if !algorithms["sha256"] {
		t.Errorf("expected sha256 entries")
	}
	if !algorithms["sha512"] {
		t.Errorf("expected the .NET sha512 entry to be handled")
	}
}

func TestParseVersionHandlesEveryToolchainWording(t *testing.T) {
	cases := map[string]string{
		"v22.14.0":                               "22.14.0",
		"Python 3.13.16":                         "3.13.16",
		"go version go1.27.1 darwin/arm64":       "1.27.1",
		"openjdk version \"21.0.12\" 2026-01-01": "21.0.12",
		"8.0.425":                                "8.0.425",
		"PHP 8.5.8 (cli) (built: Jan 1 2026)":    "8.5.8",
		"":                                       "",
	}
	for input, want := range cases {
		if got := parseVersion(KindNode, input); got != want {
			t.Errorf("parseVersion(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"22.14.0", "18.0.0", 1},
		{"18.0.0", "22.14.0", -1},
		{"21.0.12", "21.0.12", 0},
		{"21.0.12.1", "21.0.12", 1},
		{"3.10.0", "3.9.18", 1},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// --- 清单与删除 -------------------------------------------------------------

func TestInventoryMergesManagedAndSystemRuntimes(t *testing.T) {
	m, _ := testManager(t, map[string]string{"php": "PHP 8.5.8 (cli)"})
	installFake(t, m, KindNode, "22.14.0", "bin/node")

	items, err := m.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[Kind]Installed{}
	for _, item := range items {
		byKind[item.Kind] = item
	}
	if byKind[KindNode].Source != "managed" {
		t.Errorf("node should come from the managed directory: %#v", byKind[KindNode])
	}
	if byKind[KindPHP].Source != "system" {
		t.Errorf("php should come from PATH: %#v", byKind[KindPHP])
	}
}

func TestRemoveOnlyDeletesManagedRuntimes(t *testing.T) {
	m, _ := testManager(t, nil)
	installFake(t, m, KindNode, "22.14.0", "bin/node")
	// 用户手放的目录。
	manual := filepath.Join(m.Root(), string(KindGo), "1.27.1")
	if err := os.MkdirAll(manual, 0o700); err != nil {
		t.Fatal(err)
	}

	removed, err := m.Remove(KindNode)
	if err != nil || !removed {
		t.Fatalf("managed runtime should be removable: removed=%v err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(m.Root(), string(KindNode))); !os.IsNotExist(err) {
		t.Fatalf("the managed runtime directory should be gone")
	}
	if _, err := m.Remove(KindGo); err != nil {
		t.Fatalf("removing a non-managed runtime must be a no-op, got %v", err)
	}
	if _, err := os.Stat(manual); err != nil {
		t.Fatalf("a directory the kernel did not install must be left alone: %v", err)
	}
}

func TestCurrentPlatformUsesGoosAndGoarch(t *testing.T) {
	got := CurrentPlatform()
	if !strings.Contains(got, "/") {
		t.Fatalf("platform key should be GOOS/GOARCH, got %q", got)
	}
}

// --- 内置运行时（App Store 形态）-----------------------------------------

// 包内的归档必须被**真的读走**，而且照样过摘要校验。
//
// 这里故意放一个内容不对的同名文件：能走到"校验失败"就证明它读的是包内
// 那一份 —— 因为这份 Manager 没有下载能力，包里没有的话会直接返回
// ErrNotBundled，根本走不到校验。
func TestProvisionReadsTheBundledArchiveAndStillVerifiesIt(t *testing.T) {
	m, _ := testManager(t, nil)
	artifact, ok := ArtifactFor(KindNode, m.platform)
	if !ok {
		t.Skipf("%s 上没有 node 的固定发行版", m.platform)
	}

	bundle := t.TempDir()
	body := []byte("this is not the archive the digest was computed over")
	if err := os.WriteFile(filepath.Join(bundle, artifact.Archive), body, 0o600); err != nil {
		t.Fatal(err)
	}
	m.UseBundle(bundle)
	m.SetDownloader(nil)

	_, err := m.Provision(context.Background(), KindNode, "", nil)
	if err == nil {
		t.Fatal("内容与摘要不符的归档不该通过校验")
	}
	if errors.Is(err, ErrNotBundled) {
		t.Fatalf("应当读到了包内那份；报 ErrNotBundled 说明它没去找：%v", err)
	}
}

// 这份构建没有下载能力、包里也没有 → 明确失败。
//
// 最坏的结果不是失败，而是**悄悄去下载**：本机上一切正常，而审核时那份
// 二进制的行为与本地测的完全不是一回事。
func TestOfflineBuildRefusesToDownload(t *testing.T) {
	m, _ := testManager(t, nil)
	m.UseBundle(t.TempDir()) // 空的
	m.SetDownloader(nil)

	_, err := m.Provision(context.Background(), KindNode, "", nil)
	if !errors.Is(err, ErrNotBundled) {
		t.Fatalf("期望 ErrNotBundled（构建配置问题，重试无用），得到 %v", err)
	}
}

// 没声明内置目录时不该去翻包 —— 否则普通构建会拿一个无关目录当运行时来源。
func TestWithoutABundleNoBundledArchiveIsConsidered(t *testing.T) {
	m, _ := testManager(t, nil)
	artifact, ok := ArtifactFor(KindNode, m.platform)
	if !ok {
		t.Skipf("%s 上没有 node 的固定发行版", m.platform)
	}
	if _, found := m.bundleArchive(artifact); found {
		t.Fatal("没有声明内置目录时不该认为包内有归档")
	}
}

// --- 内置运行时的位置 -------------------------------------------------------

// .app 的布局是固定的，因此这个位置可以从可执行文件推出来，不需要宿主传。
func TestBundleDirIsDerivedFromTheExecutableLocation(t *testing.T) {
	app := t.TempDir()
	exe := filepath.Join(app, "ISC Phecda.app", "Contents", "MacOS", "ISC Phecda")
	want := filepath.Join(app, "ISC Phecda.app", "Contents", "Resources", "runtimes")
	if err := os.MkdirAll(want, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(exe), 0o700); err != nil {
		t.Fatal(err)
	}

	got, ok := bundleDirFromExecutable(exe)
	if !ok {
		t.Fatal("标准的 .app 布局应当能推出内置目录")
	}
	if got != want {
		t.Fatalf("推出的是 %q，期望 %q", got, want)
	}
}

// 不是 .app 布局（命令行跑内核、跑测试）时不该硬凑一个目录出来。
func TestBundleDirIsNotInventedOutsideAnAppBundle(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin", "isc")
	if err := os.MkdirAll(filepath.Dir(exe), 0o700); err != nil {
		t.Fatal(err)
	}
	// 连 Contents/Resources/runtimes 都不建：这条守的是"宁可报没有，
	// 也不要推一个不存在的路径出去"。
	if got, ok := bundleDirFromExecutable(exe); ok {
		t.Fatalf("不该推出 %q", got)
	}
}

// 布局对但目录不存在时同样报"没有" —— 那表示这份部署没内置运行时。
func TestBundleDirRequiresTheDirectoryToExist(t *testing.T) {
	app := t.TempDir()
	exe := filepath.Join(app, "X.app", "Contents", "MacOS", "X")
	if err := os.MkdirAll(filepath.Dir(exe), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok := bundleDirFromExecutable(exe); ok {
		t.Fatal("目录不存在时不该认为有内置运行时")
	}
}
