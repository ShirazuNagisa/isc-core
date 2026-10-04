package artifacts

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DefaultMaxExtractedBytes 是单次解压的总体积上限。
//
// 防的是"压缩炸弹"：一个几 MB 的归档可以解出几百 GB。上限取 8 GiB ——
// 远大于任何真实运行时（最大的是 .NET SDK，解压后约 1.5 GB），
// 但足以让炸弹在写满磁盘之前失败。
const DefaultMaxExtractedBytes = int64(8) << 30

// ErrUnsafeArchiveEntry 表示归档里有一项会把内容写到目标目录之外。
var ErrUnsafeArchiveEntry = errors.New("archive contains an unsafe entry")

// ErrUnsupportedArchive 表示归档格式无法识别。
var ErrUnsupportedArchive = errors.New("unsupported archive format")

// ExtractOptions 控制解压行为。
type ExtractOptions struct {
	// StripSingleRoot 在归档只有一个顶层目录时去掉它。
	//
	// 语言运行时的发行包几乎都是这个形状（node-v22…/、go/、jdk-21…/），
	// 而调用方想要的是"解压出来就是运行时根目录"。
	StripSingleRoot bool
	// MaxTotalBytes 为 0 时使用 DefaultMaxExtractedBytes。
	MaxTotalBytes int64
}

// Extract 解压归档到 destDir，去掉单一顶层目录。
func Extract(archivePath, destDir string) error {
	return ExtractWith(archivePath, destDir, ExtractOptions{StripSingleRoot: true})
}

// ExtractWith 解压归档到 destDir。
//
// # 安全模型
//
// 归档内容是**不可信输入**（来自网络）。这里逐项防御：
//
//   - 名字先经 filepath.Clean("/"+name) 归一化，再要求结果仍在 destDir 内 ——
//     这一步同时挡掉绝对路径与 ".." 穿越；
//   - 逐级创建父目录时**拒绝已存在的符号链接**。否则攻击者可以先用一项
//     `x -> /tmp`，再用 `x/evil` 把文件写到树外；只在最后检查目标路径
//     是不够的，必须保证路径上没有任何一环是符号链接；
//   - 归档内的符号链接与硬链接只允许指向目标目录**之内**；
//   - 权限位取 header 的 Perm()，它天然不含 setuid/setgid/sticky；
//   - 累计写入量受 MaxTotalBytes 限制。
func ExtractWith(archivePath, destDir string, opt ExtractOptions) error {
	if opt.MaxTotalBytes <= 0 {
		opt.MaxTotalBytes = DefaultMaxExtractedBytes
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return err
	}
	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}
	// 解压前先解析一次：destDir 自身若是符号链接，后面的包含性判断
	// 会拿真实路径与符号路径比较，必然误判。
	if resolved, err := filepath.EvalSymlinks(absDest); err == nil {
		absDest = resolved
	}

	switch {
	case isZip(archivePath):
		return extractZip(archivePath, absDest, opt)
	case isTarGz(archivePath):
		return extractTarGz(archivePath, absDest, opt)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedArchive, filepath.Base(archivePath))
	}
}

func isZip(path string) bool { return strings.HasSuffix(strings.ToLower(path), ".zip") }

func isTarGz(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz")
}

// stripPrefix 计算要去掉的顶层目录名；没有则返回空串。
func stripPrefix(names []string) string {
	prefix := ""
	for _, name := range names {
		trimmed := strings.TrimPrefix(filepath.Clean("/"+name), "/")
		if trimmed == "" || trimmed == "." {
			continue
		}
		head, _, _ := strings.Cut(trimmed, "/")
		if prefix == "" {
			prefix = head
			continue
		}
		if head != prefix {
			return ""
		}
	}
	// 只有一项且就是目录本身时不去掉：那会导致整个归档被"剥没"。
	if prefix == "" || len(names) == 1 {
		return ""
	}
	return prefix
}

// applyStrip 把归档内的名字转成相对 destDir 的相对路径。
//
// 这里**不做 filepath.Clean**：归一化会把 `..` 抹掉，而 safeTarget 必须
// 看到归档原本写了什么才能拒绝它。剥掉顶层目录也只按完整的一段来匹配 ——
// 否则 strip="node-v22" 会把 "node-v22-other/x" 误伤成 "-other/x"。
func applyStrip(name, strip string) string {
	rel := strings.TrimPrefix(name, "./")
	if strip == "" {
		return rel
	}
	trimmed := strings.TrimPrefix(rel, strip)
	if trimmed == rel {
		return rel
	}
	if trimmed != "" && !strings.HasPrefix(trimmed, "/") {
		return rel
	}
	return strings.TrimPrefix(trimmed, "/")
}

// safeTarget 把归档内的相对路径解析为目标目录下的绝对路径，
// 并确认它没有跑到目标目录之外。
//
// # 为什么先拒绝、而不是先归一化
//
// `filepath.Clean("/" + name)` 会把 `../evil.txt` 归一成 `evil.txt` —— 落点
// 确实还在目标目录内，因此"包含性"检查会通过。但那等于**默默改写**了归档
// 的意图：`../important.txt` 会覆盖目标目录里的同名文件，而调用方无从察觉。
//
// 归档里出现 `..` 只有两种可能：打包时用了相对路径（应当修打包），或者
// 它是恶意的。两种都不该被"猜一个合理落点"糊过去，因此这里对原始名字
// 直接拒绝，只在确认名字本身干净之后才做归一化。
func safeTarget(absDest, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("%w: empty entry name", ErrUnsafeArchiveEntry)
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: %s is an absolute path", ErrUnsafeArchiveEntry, rel)
	}
	// 同时按两种分隔符切分：Windows 上 `..\evil` 与 `../evil` 等价，
	// 而归档是跨平台产物，不能只按当前平台的分隔符判断。
	for _, part := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return "", fmt.Errorf("%w: %s contains a parent reference", ErrUnsafeArchiveEntry, rel)
		}
	}

	clean := filepath.Clean(rel)
	if clean == "" || clean == "." || clean == string(os.PathSeparator) {
		return "", fmt.Errorf("%w: empty entry name", ErrUnsafeArchiveEntry)
	}
	target := filepath.Join(absDest, clean)
	if target != absDest && !strings.HasPrefix(target, absDest+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %s escapes the destination", ErrUnsafeArchiveEntry, rel)
	}
	return target, nil
}

// ensureParents 逐级创建 target 的父目录，并拒绝路径上已存在的符号链接。
func ensureParents(absDest, target string) error {
	rel, err := filepath.Rel(absDest, filepath.Dir(target))
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	current := absDest
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			// 这是符号链接替换攻击的入口，必须在此止住。
			return fmt.Errorf("%w: %s is a symbolic link", ErrUnsafeArchiveEntry, current)
		case err == nil && !info.IsDir():
			return fmt.Errorf("%w: %s is not a directory", ErrUnsafeArchiveEntry, current)
		case err == nil:
			continue
		case os.IsNotExist(err):
			if err := os.Mkdir(current, 0o700); err != nil && !os.IsExist(err) {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// withinDest 判断解析后的链接目标是否仍在目标目录内。
func withinDest(absDest, base, linkname string) bool {
	resolved := linkname
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(base), linkname)
	}
	resolved = filepath.Clean(resolved)
	return resolved == absDest || strings.HasPrefix(resolved, absDest+string(os.PathSeparator))
}

func writeFile(target string, mode os.FileMode, r io.Reader, budget *int64) error {
	// Perm() 只保留 rwx 九位，天然剔除 setuid/setgid/sticky。
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	// 目录之外一律不给其他用户的写权限。
	if perm&0o022 != 0 {
		perm &^= 0o022
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	limited := io.LimitReader(r, *budget+1)
	n, err := io.Copy(f, limited)
	*budget -= n
	if *budget < 0 {
		return fmt.Errorf("%w: extracted size exceeds the configured limit", ErrTooLarge)
	}
	return err
}

func extractTarGz(archivePath, absDest string, opt ExtractOptions) error {
	strip := ""
	if opt.StripSingleRoot {
		names, err := tarNames(archivePath)
		if err != nil {
			return err
		}
		strip = stripPrefix(names)
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()

	budget := opt.MaxTotalBytes
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := extractTarEntry(tr, header, absDest, strip, &budget); err != nil {
			return err
		}
	}
}

func extractTarEntry(tr *tar.Reader, header *tar.Header, absDest, strip string, budget *int64) error {
	rel := applyStrip(header.Name, strip)
	if rel == "" || rel == "." {
		return nil
	}
	target, err := safeTarget(absDest, rel)
	if err != nil {
		return err
	}
	if err := ensureParents(absDest, target); err != nil {
		return err
	}

	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o700)
	case tar.TypeReg:
		return writeFile(target, header.FileInfo().Mode(), tr, budget)
	case tar.TypeSymlink:
		if !withinDest(absDest, target, header.Linkname) {
			return fmt.Errorf("%w: symlink %s -> %s leaves the destination", ErrUnsafeArchiveEntry, rel, header.Linkname)
		}
		_ = os.Remove(target)
		return os.Symlink(header.Linkname, target)
	case tar.TypeLink:
		// 硬链接的 Linkname 相对归档根，同样先过剥离再判包含。
		linkRel := applyStrip(header.Linkname, strip)
		source, err := safeTarget(absDest, linkRel)
		if err != nil {
			return err
		}
		_ = os.Remove(target)
		return os.Link(source, target)
	default:
		// 设备节点、FIFO 等一律跳过：运行时发行包里没有，
		// 而创建它们需要权限、也没有正当用途。
		return nil
	}
}

func tarNames(archivePath string) ([]string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()
	var names []string
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		names = append(names, header.Name)
	}
}

func extractZip(archivePath, absDest string, opt ExtractOptions) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	strip := ""
	if opt.StripSingleRoot {
		names := make([]string, 0, len(reader.File))
		for _, f := range reader.File {
			names = append(names, f.Name)
		}
		strip = stripPrefix(names)
	}

	budget := opt.MaxTotalBytes
	for _, entry := range reader.File {
		rel := applyStrip(entry.Name, strip)
		if rel == "" || rel == "." {
			continue
		}
		target, err := safeTarget(absDest, rel)
		if err != nil {
			return err
		}
		if err := ensureParents(absDest, target); err != nil {
			return err
		}

		mode := entry.Mode()
		switch {
		case entry.FileInfo().IsDir() || strings.HasSuffix(entry.Name, "/"):
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			rc, err := entry.Open()
			if err != nil {
				return err
			}
			link, readErr := io.ReadAll(io.LimitReader(rc, 4096))
			_ = rc.Close()
			if readErr != nil {
				return readErr
			}
			linkname := string(link)
			if !withinDest(absDest, target, linkname) {
				return fmt.Errorf("%w: symlink %s -> %s leaves the destination", ErrUnsafeArchiveEntry, rel, linkname)
			}
			_ = os.Remove(target)
			if err := os.Symlink(linkname, target); err != nil {
				return err
			}
		default:
			rc, err := entry.Open()
			if err != nil {
				return err
			}
			writeErr := writeFile(target, mode, rc, &budget)
			_ = rc.Close()
			if writeErr != nil {
				return writeErr
			}
		}
	}
	return nil
}
