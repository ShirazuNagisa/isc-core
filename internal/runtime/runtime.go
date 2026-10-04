package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/artifacts"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 错误哨兵。
var (
	// ErrNoArtifact 表示该平台没有该运行时的固定发行版。
	ErrNoArtifact = errors.New("no pinned runtime build for this platform")
	// ErrExecutableMissing 表示解压后找不到预期的可执行文件。
	ErrExecutableMissing = errors.New("the runtime archive did not contain the expected executable")
	// ErrNotProvisionable 表示该运行时不由内核供给（例如 Docker）。
	ErrNotProvisionable = errors.New("this runtime is not provisioned by the kernel")
)

// markerName 是运行时目录里的标记文件。
//
// 它同时承担两件事：证明这个目录是内核装出来的（而不是用户手放的），
// 以及记录它的来源与摘要，供 /v1/runtimes 展示与 D29 的许可登记核对。
const markerName = ".isc-runtime.json"

// Marker 是标记文件的内容。
type Marker struct {
	Kind        Kind      `json:"kind"`
	Version     string    `json:"version"`
	Digest      string    `json:"digest"`
	URL         string    `json:"url"`
	Source      string    `json:"source"`
	License     string    `json:"license"`
	InstalledAt time.Time `json:"installed_at"`
}

// Installed 是一个可用的运行时。
type Installed struct {
	Kind    Kind
	Version string
	// Source 是 "system" 或 "managed"。
	Source string
	// Root 是托管运行时的根目录；系统解释器为空。
	Root string
	// Executable 是可执行文件的绝对路径。
	Executable string
}

// Manager 解析与供给运行时。
type Manager struct {
	root     string
	cache    string
	goos     string
	goarch   string
	platform string

	downloader *artifacts.Downloader

	// 以下三个可在测试中替换，避免依赖机器上装了什么、也不碰网络。
	lookPath   func(string) (string, error)
	runVersion func(ctx context.Context, exe string, args []string) (string, error)
	now        func() time.Time
}

// NewManager 构造运行时管理器。
//
// root 是数据根目录（<data>/runtimes 与 <data>/cache 建在它下面）。
func NewManager(dataRoot, goos, goarch string) *Manager {
	return &Manager{
		root:       filepath.Join(dataRoot, "runtimes"),
		cache:      filepath.Join(dataRoot, "cache"),
		goos:       goos,
		goarch:     goarch,
		platform:   platformOf(goos, goarch),
		downloader: &artifacts.Downloader{},
		lookPath:   exec.LookPath,
		runVersion: probeVersion,
		now:        time.Now,
	}
}

// Root 返回托管运行时目录。
func (m *Manager) Root() string { return m.root }

// Resolve 按 D28 的三级顺序找出一个可用的运行时，不下载任何东西。
//
// minVersion 为空表示不限版本。
func (m *Manager) Resolve(ctx context.Context, kind Kind, minVersion string) (Installed, bool, error) {
	if kind == KindNone {
		return Installed{Kind: KindNone, Source: "none"}, true, nil
	}
	if kind == KindDocker {
		// Docker 由用户自己安装，这里只探测。
		if path, err := m.lookPath("docker"); err == nil {
			return Installed{Kind: kind, Source: "system", Executable: path}, true, nil
		}
		return Installed{}, false, nil
	}

	if found, ok := m.resolveSystem(ctx, kind, minVersion); ok {
		return found, true, nil
	}
	if found, ok := m.resolveManaged(kind, minVersion); ok {
		return found, true, nil
	}
	return Installed{}, false, nil
}

// resolveSystem 探测 PATH 上的解释器。
func (m *Manager) resolveSystem(ctx context.Context, kind Kind, minVersion string) (Installed, bool) {
	name := executableName(kind)
	path, err := m.lookPath(name)
	if err != nil {
		return Installed{}, false
	}
	output, err := m.runVersion(ctx, path, systemVersionArgs(kind))
	if err != nil {
		// 装是装了这个名字，但跑不起来（损坏、架构不符、挂在交互上）。
		// 当作"没有"，让调用方回退到托管运行时。
		return Installed{}, false
	}
	version := parseVersion(kind, output)
	if version == "" {
		return Installed{}, false
	}
	if minVersion != "" && compareVersions(version, minVersion) < 0 {
		// 版本过旧不算可用：回退到供给，而不是让用户对着
		// "语法错误 / 特性不支持" 猜原因。
		return Installed{}, false
	}
	return Installed{Kind: kind, Version: version, Source: "system", Executable: path}, true
}

// resolveManaged 在数据目录里找已供给的运行时。
func (m *Manager) resolveManaged(kind Kind, minVersion string) (Installed, bool) {
	kindDir := filepath.Join(m.root, string(kind))
	entries, err := os.ReadDir(kindDir)
	if err != nil {
		return Installed{}, false
	}
	best := Installed{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(kindDir, entry.Name())
		marker, err := readMarker(dir)
		if err != nil || marker.Kind != kind {
			// 没有标记文件就不是内核装的，不认。
			continue
		}
		if minVersion != "" && compareVersions(marker.Version, minVersion) < 0 {
			continue
		}
		artifact, _ := ArtifactFor(kind, m.platform)
		exe := filepath.Join(dir, artifact.Executable)
		if artifact.Executable == "" {
			continue
		}
		if info, err := os.Stat(exe); err != nil || info.IsDir() {
			continue
		}
		if best.Version == "" || compareVersions(marker.Version, best.Version) > 0 {
			best = Installed{Kind: kind, Version: marker.Version, Source: "managed", Root: dir, Executable: exe}
		}
	}
	if best.Version == "" {
		return Installed{}, false
	}
	return best, true
}

// Provision 让某个运行时变得可用：优先复用系统解释器，否则下载并安装。
//
// progress 可为 nil；取值 0..1，消息已本地化。
func (m *Manager) Provision(ctx context.Context, kind Kind, minVersion string, progress func(float64, string)) (Installed, error) {
	if kind == KindDocker {
		return Installed{}, fmt.Errorf("%w: %s", ErrNotProvisionable, kind)
	}
	if found, ok, err := m.Resolve(ctx, kind, minVersion); err != nil {
		return Installed{}, err
	} else if ok {
		if found.Source == "system" {
			report(progress, 1, i18n.T("runtime.msg.using_system", string(kind), found.Version))
		}
		return found, nil
	}

	artifact, ok := ArtifactFor(kind, m.platform)
	if !ok {
		return Installed{}, fmt.Errorf("%w: %s on %s", ErrNoArtifact, kind, m.platform)
	}
	digest, err := artifacts.ParseDigest(artifact.Digest)
	if err != nil {
		// 清单是代码常量，走到这里说明清单本身写错了。
		return Installed{}, fmt.Errorf("runtime %s: %w", kind, err)
	}

	if err := os.MkdirAll(m.cache, 0o700); err != nil {
		return Installed{}, err
	}
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return Installed{}, err
	}

	archivePath := filepath.Join(m.cache, artifact.Archive)
	report(progress, 0, i18n.T("runtime.msg.downloading", string(kind), artifact.Version, humanBytes(artifact.SizeBytes)))
	downloader := *m.downloader
	downloader.Progress = func(received, total int64) {
		if total <= 0 {
			total = artifact.SizeBytes
		}
		if total <= 0 {
			return
		}
		// 下载占前 80% 的进度；剩下的是解压与落位。
		report(progress, float64(received)/float64(total)*0.8, "")
	}
	if _, err := downloader.Download(ctx, artifact.URL, digest, archivePath); err != nil {
		return Installed{}, err
	}
	// 下载阶段已校验过；这里再验一次，挡住"下载后被替换"。
	if err := digest.Verify(archivePath); err != nil {
		return Installed{}, err
	}

	report(progress, 0.85, i18n.T("runtime.msg.extracting", string(kind), artifact.Version))
	staging, err := os.MkdirTemp(m.root, ".staging-"+string(kind)+"-")
	if err != nil {
		return Installed{}, err
	}
	// 失败时清掉半成品目录：留下它会让下一次解压撞上"目标已存在"。
	keepStaging := false
	defer func() {
		if !keepStaging {
			_ = os.RemoveAll(staging)
		}
	}()

	if err := artifacts.ExtractWith(archivePath, staging, artifacts.ExtractOptions{StripSingleRoot: artifact.StripRoot}); err != nil {
		return Installed{}, err
	}

	executable := filepath.Join(staging, artifact.Executable)
	if info, err := os.Stat(executable); err != nil || info.IsDir() {
		return Installed{}, fmt.Errorf("%w: %s (looked for %s)", ErrExecutableMissing, artifact.Archive, artifact.Executable)
	}
	// 归档里的权限位未必带可执行位，这里补一次 —— 否则运行时会以
	// "permission denied" 失败，而那个信息离"解压时没恢复权限"很远。
	if err := os.Chmod(executable, 0o755); err != nil {
		return Installed{}, err
	}

	marker := Marker{
		Kind: kind, Version: artifact.Version, Digest: artifact.Digest,
		URL: artifact.URL, Source: artifact.Source, License: artifact.License,
		InstalledAt: m.now().UTC(),
	}
	body, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return Installed{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, markerName), body, 0o600); err != nil {
		return Installed{}, err
	}

	target := filepath.Join(m.root, string(kind), artifact.Version)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return Installed{}, err
	}
	// 目标已存在（并发供给或上次残留）时先移除，再原子改名。
	if _, err := os.Stat(target); err == nil {
		if err := os.RemoveAll(target); err != nil {
			return Installed{}, err
		}
	}
	if err := os.Rename(staging, target); err != nil {
		return Installed{}, err
	}
	keepStaging = true

	// 归档留在 cache 里没有意义：它已经解压并校验过，留着只占磁盘。
	_ = os.Remove(archivePath)

	report(progress, 1, i18n.T("runtime.msg.ready", string(kind), artifact.Version))
	return Installed{
		Kind: kind, Version: artifact.Version, Source: "managed",
		Root: target, Executable: filepath.Join(target, artifact.Executable),
	}, nil
}

// Inventory 列出当前可用的运行时：系统探测到的 + 已供给的。
func (m *Manager) Inventory(ctx context.Context) ([]Installed, error) {
	out := make([]Installed, 0)

	// 已供给的（按类型取最新的一个）。
	for _, kind := range Kinds() {
		if found, ok := m.resolveManaged(kind, ""); ok {
			out = append(out, found)
		}
	}
	// 系统解释器：只报"没有托管版本"的那些，避免同一个运行时出现两条
	// 看起来重复的条目。
	managed := map[Kind]bool{}
	for _, item := range out {
		managed[item.Kind] = true
	}
	for _, kind := range []Kind{KindNode, KindPython, KindPHP, KindGo, KindJava, KindDotNet, KindDocker} {
		if managed[kind] {
			continue
		}
		if found, ok := m.resolveSystem(ctx, kind, ""); ok {
			out = append(out, found)
		}
	}
	return out, nil
}

// Remove 删除一个托管运行时。
//
// 系统解释器不可删：内核没有装它，也就没有资格卸它。
func (m *Manager) Remove(kind Kind) (bool, error) {
	kindDir := filepath.Join(m.root, string(kind))
	entries, err := os.ReadDir(kindDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	removed := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(kindDir, entry.Name())
		if _, err := readMarker(dir); err != nil {
			// 不是内核装的目录，不动它。
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return removed, err
		}
		removed = true
	}
	if removed {
		// 版本目录都删掉之后，空的 <kind>/ 也一并清掉：留着它会让
		// /v1/runtimes 与磁盘看起来不一致（目录在、内容没有）。
		_ = os.Remove(kindDir)
	}
	return removed, nil
}

func readMarker(dir string) (Marker, error) {
	body, err := os.ReadFile(filepath.Join(dir, markerName))
	if err != nil {
		return Marker{}, err
	}
	var marker Marker
	if err := json.Unmarshal(body, &marker); err != nil {
		return Marker{}, err
	}
	return marker, nil
}

// probeVersion 执行解释器的版本参数并合并两路输出。
//
// java 把版本写到 stderr，因此不能只看 stdout。
func probeVersion(ctx context.Context, exe string, args []string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, exe, args...)
	// 不给 stdin：某些解释器在没有输入时会挂住等待。
	cmd.Stdin = nil
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil && text == "" {
		return "", err
	}
	// 有输出就够解析了；退出码非零（例如 java -version 的旧版行为）
	// 不该让"能不能用"的判断失败。
	return text, nil
}

var versionPattern = regexp.MustCompile(`\d+(?:\.\d+)+`)

// parseVersion 从版本输出里取出第一个形如 x.y.z 的版本号。
//
// 每种工具的措辞各不相同（`v22.14.0`、`Python 3.13.16`、
// `go version go1.27.1 darwin/arm64`、`openjdk version "21.0.12"`），
// 因此统一按"第一个点分数字"提取，而不是为每种写一个正则。
func parseVersion(kind Kind, output string) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	match := versionPattern.FindString(output)
	return match
}

// compareVersions 比较两个点分版本号。
//
// 只按数字段比较（非数字段忽略），缺少的段按 0 处理：
// "21.0.12" 与 "21.0.12.1" 比较时前者视为 21.0.12.0。
func compareVersions(a, b string) int {
	left := numericSegments(a)
	right := numericSegments(b)
	length := len(left)
	if len(right) > length {
		length = len(right)
	}
	for i := 0; i < length; i++ {
		var x, y int
		if i < len(left) {
			x = left[i]
		}
		if i < len(right) {
			y = right[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func numericSegments(version string) []int {
	parts := strings.FieldsFunc(version, func(r rune) bool { return r == '.' || r == '-' || r == '+' || r == ' ' })
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		digits := strings.TrimFunc(part, func(r rune) bool { return r < '0' || r > '9' })
		if digits == "" {
			continue
		}
		value, err := strconv.Atoi(digits)
		if err != nil {
			continue
		}
		out = append(out, value)
	}
	return out
}

func report(progress func(float64, string), fraction float64, message string) {
	if progress == nil {
		return
	}
	progress(fraction, message)
}

// humanBytes 把字节数写成人看的大小。
func humanBytes(size int64) string {
	if size <= 0 {
		return ""
	}
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	units := []string{"KB", "MB", "GB", "TB"}
	for _, suffix := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.0f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.0f PB", value/unit)
}

// Platform 返回清单使用的平台键。
func (m *Manager) Platform() string { return m.platform }

// CurrentPlatform 返回本机平台键。
func CurrentPlatform() string { return platformOf(runtime.GOOS, runtime.GOARCH) }
