// Package main 是发布构建脚本。
//
// # 为什么用 Go 而不是 shell 脚本
//
// 项目要出三个平台的产物，而构建本身也得在那三个平台上都能跑。
// 用 PowerShell 写就得再维护一份 .sh，两者迟早会漂移 —— 而漂移的
// 表现是"在某台机器上打出来的包少了一个平台"。
//
// Go 程序在任何装了 Go 的机器上行为一致，而且它能被 go vet 覆盖。
//
// 用法：
//
//	go run ./scripts/release
//	go run ./scripts/release -out dist -version 0.2.0
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// target 是一个构建目标。
type target struct {
	GOOS   string
	GOARCH string
	// Label 是产物名里的平台标识。
	Label string
	// Format 是打包格式：zip 给 Windows，tar.gz 给其余。
	//
	// 这不是随意的选择：Windows 用户习惯双击解压，而 zip 是资源管理器
	// 原生支持的；tar.gz 在 Unix 上是标准，且能保留可执行位。
	Format string
}

// 默认构建目标。
//
// 与 .github/workflows/ci.yml 的矩阵保持一致 —— 两处不一致会让
// "CI 通过但发布包缺平台"这种事发生。
var defaultTargets = []target{
	{"windows", "amd64", "windows-amd64", "zip"},
	{"windows", "arm64", "windows-arm64", "zip"},
	{"linux", "amd64", "linux-amd64", "tar.gz"},
	{"linux", "arm64", "linux-arm64", "tar.gz"},
	{"darwin", "amd64", "darwin-amd64", "tar.gz"},
	{"darwin", "arm64", "darwin-arm64", "tar.gz"},
	{"freebsd", "amd64", "freebsd-amd64", "tar.gz"},
}

const (
	modulePath = "github.com/ShirazuNagisa/isc-core"
	binaryName = "isc"

	// 打包元数据。
	maintainer = "Sh1razu <ShirazuNagisa@users.noreply.github.com>"
	homepage   = "https://github.com/ShirazuNagisa/isc-core"
)

// zipEpoch 是写进 zip 头的固定时间戳。
//
// 1980-01-01 是 ZIP 格式能表示的最早时间。用它而不是零值：零值会被
// 编码成一个未定义的值，而某些解压工具会因此报"时间戳无效"。
var zipEpoch = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

func main() {
	var (
		outDir     = flag.String("out", "dist", "产物输出目录")
		versionArg = flag.String("version", "", "版本号（默认由 git describe 推导）")
		onlyList   = flag.Bool("targets", false, "只打印构建目标后退出")
		skipTests  = flag.Bool("skip-tests", false, "跳过构建前的测试")
	)
	flag.Parse()

	if *onlyList {
		for _, t := range defaultTargets {
			fmt.Printf("%s/%s\t%s\t%s\n", t.GOOS, t.GOARCH, t.Label, t.Format)
		}
		return
	}

	if err := run(*outDir, *versionArg, !*skipTests); err != nil {
		fmt.Fprintln(os.Stderr, "构建失败:", err)
		os.Exit(1)
	}
}

func run(outDir, versionArg string, runTests bool) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	if err := os.Chdir(root); err != nil {
		return err
	}

	version := versionArg
	if version == "" {
		version = deriveVersion()
	}
	commit := gitOutput("rev-parse", "--short", "HEAD")
	if commit == "" {
		commit = "unknown"
	}
	// 构建时间用 UTC：本地产物的时间戳不该随开发者的时区变化，
	// 否则同样的源码在不同机器上产出的包内容不一致。
	//
	// 支持 SOURCE_DATE_EPOCH（可复现构建的通行约定）。
	//
	// 设了它之后，同一份源码 + 同一个 epoch 会产出**逐字节相同**的
	// 二进制 —— 而这是"校验和一致"能证明"两个产物来自同一份源码"
	// 的前提。不设时用当前时间，那是开发构建的合理默认。
	buildTime := buildTimestamp()

	fmt.Printf("ISC-Core 发布构建\n")
	fmt.Printf("  版本    %s\n", version)
	fmt.Printf("  提交    %s\n", commit)
	fmt.Printf("  时间    %s\n", buildTime)
	fmt.Printf("  目标    %d 个平台\n", len(defaultTargets))
	fmt.Printf("  Go      %s (%s/%s)\n\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)

	if runTests {
		// 构建前跑一遍测试。
		//
		// 发布包里带着一个测试不过的内核，比"多等两分钟"糟得多。
		fmt.Println("==> 运行测试")
		if err := runCmd(root, "go", "test", "./...", "-count=1"); err != nil {
			return fmt.Errorf("测试未通过，已中止发布构建：%w", err)
		}
	}

	// 输出目录必须**重建**：残留的旧产物会被打进校验和，
	// 而那份校验和看起来一切正常。
	if err := os.RemoveAll(outDir); err != nil {
		return fmt.Errorf("清理输出目录失败: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	ldflags := strings.Join([]string{
		"-s", "-w", // 去掉符号表与调试信息：产物小一半左右
		"-X", modulePath + "/internal/version.Version=" + version,
		"-X", modulePath + "/internal/version.Commit=" + commit,
		"-X", modulePath + "/internal/version.BuildTime=" + buildTime,
	}, " ")

	var artifacts []string
	for _, t := range defaultTargets {
		fmt.Printf("==> 构建 %s\n", t.Label)

		// buildTarget 返回的是**暂存目录**（里面有可执行文件与文档），
		// 而不是可执行文件本身。早先这里把它叫做 binPath，
		// 而那个名字在说谎 —— 打包与 .deb 生成都需要这个目录。
		stage, err := buildTarget(root, t, outDir, ldflags)
		if err != nil {
			return err
		}

		archive, err := pack(outDir, t, stage, version, exeNameFor(t))
		if err != nil {
			return err
		}
		artifacts = append(artifacts, archive)
		fmt.Printf("    %s\n", filepath.Base(archive))

		// Linux 额外产出 .deb 与 .rpm。
		//
		// 只有 Linux 需要：Windows 与 macOS 的用户不会用这两个。
		if t.GOOS == "linux" {
			debPath := filepath.Join(outDir,
				fmt.Sprintf("%s_%s_%s.deb", binaryName, version, t.GOARCH))
			if err := BuildDeb(debPath, DebOptions{
				Package:     binaryName,
				Version:     version,
				Arch:        t.GOARCH,
				Maintainer:  maintainer,
				Description: "ISC 接入编排器内核：动态域名解析、IPv6 前缀跟踪与反向代理",
				Homepage:    homepage,
				BinaryPath:  filepath.Join(stage, exeNameFor(t)),
				DocDir:      stage,
			}); err != nil {
				return fmt.Errorf("生成 %s 失败: %w", filepath.Base(debPath), err)
			}
			artifacts = append(artifacts, debPath)
			fmt.Printf("    %s\n", filepath.Base(debPath))

			// RPM 系发行版（RHEL / Fedora / openSUSE）用这个。
			rpmPath := filepath.Join(outDir,
				fmt.Sprintf("%s-%s-1.%s.rpm", binaryName, version, t.GOARCH))
			if err := BuildRPM(rpmPath, RpmOptions{
				Package: binaryName,
				// RPM 的版本号不能含连字符 —— 那是它分隔版本与
				// 发布号的字符。BuildRPM 会挡住并给出解决方式。
				Version:    rpmVersion(version),
				Release:    "1",
				Arch:       t.GOARCH,
				Summary:    "ISC 接入编排器内核",
				License:    "GPL-3.0-or-later",
				URL:        homepage,
				BinaryPath: filepath.Join(stage, exeNameFor(t)),
				DocDir:     stage,
			}); err != nil {
				// RPM 生成失败**不中止整个发布**：它的格式比 deb 复杂，
				// 而一个打不出来的 rpm 不该让另外九个产物也发不出去。
				//
				// 但必须显式报出来 —— 静默跳过会让"某个平台少了包"
				// 这件事一直不被发现。
				fmt.Fprintf(os.Stderr,
					"    警告：生成 rpm 失败（其余产物不受影响）：%v\n", err)
			} else {
				artifacts = append(artifacts, rpmPath)
				fmt.Printf("    %s\n", filepath.Base(rpmPath))
			}
		}

		// Windows 额外产出 .msi。
		//
		// 与 .deb / .rpm 不同，它需要**外部工具**（WiX），因此只在本机构建
		// Windows 目标且 wix 在 PATH 上时才有。这也是它的失败
		// **不中止整个发布**的原因：另外九个产物与它无关。
		if t.GOOS == "windows" {
			msiPath := filepath.Join(outDir,
				fmt.Sprintf("%s_%s_%s.msi", binaryName, version, t.GOARCH))
			err := BuildMSI(MSIOptions{
				BinaryPath:  filepath.Join(stage, exeNameFor(t)),
				Version:     version,
				UpgradeCode: msiUpgradeCode,
				OutPath:     msiPath,
			})
			switch {
			case err == nil:
				artifacts = append(artifacts, msiPath)
				fmt.Printf("    %s\n", filepath.Base(msiPath))
			case errors.Is(err, ErrWixMissing):
				// 没装 WiX 不是错误，是**这台机器打不了 MSI**。
				// 把该怎么装写出来 —— 否则用户只看到"少了一个包"，
				// 而不知道少的那个是可以装的。
				fmt.Fprintf(os.Stderr,
					"    跳过 msi：PATH 上没有 wix。"+
						"装法：dotnet tool install --global wix --version %s\n",
					msiWixVersion)
			default:
				fmt.Fprintf(os.Stderr,
					"    警告：生成 msi 失败（其余产物不受影响）：%v\n", err)
			}
		}

		// macOS 额外产出 .pkg。
		//
		// 与 .msi 一样需要外部工具，但性质不同：pkgbuild 是 **macOS 自带**的，
		// 因此"打不出 pkg"只可能发生在**不是在 macOS 上构建 darwin 目标**时
		// —— 而那种组合下本来也不该尝试。桩会返回 ErrPkgToolsMissing，
		// 这里据此安静地跳过。
		if t.GOOS == "darwin" {
			pkgPath := filepath.Join(outDir,
				fmt.Sprintf("%s_%s_%s.pkg", binaryName, version, t.GOARCH))
			err := BuildPkg(PkgOptions{
				BinaryPath: filepath.Join(stage, exeNameFor(t)),
				Version:    version,
				OutPath:    pkgPath,
			})
			switch {
			case err == nil:
				artifacts = append(artifacts, pkgPath)
				fmt.Printf("    %s\n", filepath.Base(pkgPath))
			case errors.Is(err, ErrPkgToolsMissing):
				fmt.Fprintf(os.Stderr,
					"    跳过 pkg：这台机器上没有 macOS 打包工具"+
						"（pkgbuild 是系统自带的，因此这意味着当前不是 macOS）\n")
			default:
				fmt.Fprintf(os.Stderr,
					"    警告：生成 pkg 失败（其余产物不受影响）：%v\n", err)
			}
		}
	}

	// 清掉暂存目录。
	//
	// 不做的话 dist/ 里会留着一个 stage/ 树，而用户看到它会以为
	// 那也是一份该下载的产物 —— 实际上里面的可执行文件没有版本号，
	// 也没有打包。
	if err := os.RemoveAll(filepath.Join(outDir, "stage")); err != nil {
		return fmt.Errorf("清理暂存目录失败: %w", err)
	}

	// 校验和。
	//
	// 用户下载之后应当能验证他拿到的东西没被动过 —— 而这个内核会拿到
	// 用户的 DNS 凭据，因此"确认产物来源"不是可有可无的一步。
	fmt.Println("\n==> 生成校验和")
	sumFile := filepath.Join(outDir, "SHA256SUMS")
	if err := writeChecksums(sumFile, artifacts); err != nil {
		return err
	}
	fmt.Printf("    %s\n", sumFile)

	fmt.Printf("\n完成：%d 个产物在 %s/\n", len(artifacts), outDir)
	return nil
}

// rpmVersion 把版本号转成 RPM 认的形式。
//
// RPM 用连字符分隔 Version 与 Release，因此 Version 里**不能有连字符**。
// 而我们的版本可能来自 `git describe`（例如 "1.0.0-rc1"），
// 于是这里把连字符换成下划线。
//
// 换成下划线而不是直接删掉：`1.0.0rc1` 与 `1.0.0-rc1` 在读的人看来
// 是两回事，而下划线保留了那个分隔。
func rpmVersion(v string) string {
	return strings.ReplaceAll(v, "-", "_")
}

// exeNameFor 返回某个目标的可执行文件名。
//
// 单独一个函数而不是在两处各写一遍：打包时需要知道"哪个文件是可执行的"
// 才能给它设置权限位，而两处判断不一致会让那个文件既没有执行位、
// 也不被当成二进制。
func exeNameFor(t target) string {
	if t.GOOS == "windows" {
		return binaryName + ".exe"
	}
	return binaryName
}

// buildTarget 编译单个目标，返回**暂存目录**。
//
// 目录里除了可执行文件还有随包分发的文档（许可证、说明），
// 因此打包与 .deb 生成都以目录为单位，而不是单个文件。
func buildTarget(root string, t target, outDir, ldflags string) (string, error) {
	exeName := exeNameFor(t)

	// 先放到一个临时目录，再连同文档一起打包 ——
	// 直接放进 outDir 会让中间产物与最终产物混在一起。
	stage := filepath.Join(outDir, "stage", t.Label)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", err
	}
	binPath := filepath.Join(stage, exeName)

	cmd := exec.Command("go", "build",
		// -trimpath 去掉产物里的本机路径。
		//
		// 不做的话，构建机上用户的目录名会被写进二进制 ——
		// 那既是隐私问题，也让构建不可复现。
		"-trimpath",
		"-ldflags", ldflags,
		"-o", binPath,
		"./cmd/isc",
	)
	cmd.Dir = root
	// CGO_ENABLED=0 是本项目的硬约束（见 docs/DECISIONS.md）。
	//
	// 开了 cgo 就需要目标平台的 C 工具链，而交叉编译时那通常没有 ——
	// 表现是"某个平台的构建莫名失败"。
	cmd.Env = append(os.Environ(),
		"GOOS="+t.GOOS,
		"GOARCH="+t.GOARCH,
		"CGO_ENABLED=0",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("构建 %s 失败: %w", t.Label, err)
	}

	// 打包用的附属文件：许可证与说明。
	//
	// 它们必须跟着产物走 —— GPLv3 要求分发时附带许可证，
	// 而用户拿到一个二进制却找不到许可证与来源，是无法接受的分发方式。
	for _, name := range []string{
		"LICENSE", "THIRD_PARTY_NOTICES.md", "README.md",
	} {
		if err := copyFile(filepath.Join(root, name), filepath.Join(stage, name)); err != nil {
			// README 之类的缺失不该让发布失败，但要报出来。
			fmt.Fprintf(os.Stderr, "    警告：未能附带 %s：%v\n", name, err)
		}
	}

	return stage, nil
}

// pack 把暂存目录打成一个压缩包。
//
// version 显式传入而不是从包级变量读：产物名里的版本是**这次构建**
// 的属性，而不是程序的全局状态。做成全局变量会让"同一进程里构建两个
// 版本"这种事静默地出错。
//
// 版本必须出现在文件名里：用户下载一堆包时，文件名是他唯一能看到的
// 版本信息。
func pack(outDir string, t target, stage, version, exeName string) (string, error) {
	base := fmt.Sprintf("%s-%s-%s", binaryName, version, t.Label)
	ext := ".tar.gz"
	if t.Format == "zip" {
		ext = ".zip"
	}
	outPath := filepath.Join(outDir, base+ext)

	if t.Format == "zip" {
		return outPath, packZip(outPath, stage)
	}
	return outPath, packTarGz(outPath, stage, exeName)
}

func packZip(outPath, dir string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // 只写文件

	zw := zip.NewWriter(f)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := addToZip(zw, filepath.Join(dir, e.Name()), e.Name()); err != nil {
			return err
		}
	}

	if err := zw.Close(); err != nil {
		return err
	}
	return f.Sync()
}

func addToZip(zw *zip.Writer, path, name string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close() //nolint:errcheck // 只读文件

	info, err := src.Stat()
	if err != nil {
		return err
	}

	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = name
	hdr.Method = zip.Deflate

	// 时间戳固定，理由与 tar 侧相同。
	//
	// FileInfoHeader 会把文件在**磁盘上的修改时间**写进 zip 头，而编译
	// 产物每次构建的 mtime 都不同 —— 于是同样的源码会产出不同的 zip。
	// 这一点是实测发现的：加了 SOURCE_DATE_EPOCH 之后 tar.gz 已经逐字节
	// 相同，而 zip 仍然不同。
	//
	// 用 1980-01-01 UTC 而不是零值：那是 ZIP 格式能表示的最早时间，
	// 而零值会被编码成一个未定义的值。
	hdr.Modified = zipEpoch
	hdr.SetModTime(zipEpoch)

	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, src)
	return err
}

// packTarGz 打包成 tar.gz。
//
// exeName 是其中**应当被标记为可执行**的那个文件。这个参数是必需的，
// 而且它修的是一个只能在 Windows 上复现的缺陷：
//
//	tar 头里的模式来自文件在磁盘上的 mode，而在 Windows 上 go build
//	产出的文件是 0666 —— 没有执行位这个概念。于是**在 Windows 上交叉
//	编译出的 Linux 产物**，用户解压后会得到 "Permission denied"。
//
// 交叉编译是完全正当的用法（本项目就是这么发布的），因此不能靠
// "构建机上恰好有正确的权限位"。
func packTarGz(outPath, dir, exeName string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // 只写文件

	gz := gzip.NewWriter(f)
	// 时间戳清零：让同样的源码产出**逐字节相同**的包。
	//
	// 这不只是洁癖 —— 不可复现的构建意味着"校验和一致"无法证明
	// 两个产物来自同一份源码。
	gz.ModTime = time.Time{}

	tw := tar.NewWriter(gz)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	// 排序：目录读取顺序在不同文件系统上不同，不排序会让包内容
	// 的排列随机，从而破坏可复现性。
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		mode := os.FileMode(0o644)
		if e.Name() == exeName {
			mode = 0o755
		}
		if err := addToTar(tw, filepath.Join(dir, e.Name()), e.Name(), mode); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Sync()
}

func addToTar(tw *tar.Writer, path, name string, mode os.FileMode) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close() //nolint:errcheck // 只读文件

	info, err := src.Stat()
	if err != nil {
		return err
	}

	hdr := &tar.Header{
		Name: name,
		Mode: int64(mode.Perm()),
		Size: info.Size(),
		// 时间戳清零，理由同上。
		ModTime: time.Time{},
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, src)
	return err
}

// writeChecksums 生成 SHA256SUMS。
func writeChecksums(path string, files []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // 只写文件

	// 按文件名排序：校验和文件本身也该是可复现的。
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)

	for _, file := range sorted {
		sum, err := sha256File(file)
		if err != nil {
			return err
		}
		// 用 `<hash>  <name>` 两空格格式：那是 sha256sum -c 认的格式，
		// 用户不必读文档就能验证。
		if _, err := fmt.Fprintf(f, "%s  %s\n", sum, filepath.Base(file)); err != nil {
			return err
		}
	}
	return f.Sync()
}

// md5File 算一个文件的 MD5。
//
// 用 MD5 而不是 SHA-256：deb 的 md5sums 文件是 dpkg 的既定格式，
// 而它只用 MD5。这里**不是**在用 MD5 做安全保证 —— 包的真实性由
// SHA256SUMS 与用户的核对来保证。
func md5File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck // 只读文件

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck // 只读文件

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// buildTimestamp 返回构建时间戳。
//
// 优先用 SOURCE_DATE_EPOCH（可复现构建的通行约定），否则用当前时间。
func buildTimestamp() string {
	if raw := strings.TrimSpace(os.Getenv("SOURCE_DATE_EPOCH")); raw != "" {
		secs, err := strconv.ParseInt(raw, 10, 64)
		if err == nil && secs > 0 {
			return time.Unix(secs, 0).UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(os.Stderr,
			"警告：SOURCE_DATE_EPOCH=%q 无法解析，改用当前时间\n", raw)
	}
	return time.Now().UTC().Format(time.RFC3339)
}

// deriveVersion 由 git 推导版本号。
func deriveVersion() string {
	// 恰好在一个 tag 上时用 tag。
	if tag := gitOutput("describe", "--tags", "--exact-match"); tag != "" {
		return strings.TrimPrefix(tag, "v")
	}
	// 否则用 "<最近 tag>-<距 tag 的提交数>-g<哈希>"。
	if desc := gitOutput("describe", "--tags", "--always", "--dirty"); desc != "" {
		return strings.TrimPrefix(desc, "v")
	}
	// 连 git 都没有（源码包）时退回一个明确的值，而不是留空。
	// 空版本号在界面上显示为空白，用户完全看不出自己在跑什么。
	return "unknown"
}

func gitOutput(args ...string) string {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// repoRoot 找到仓库根目录。
//
// 用 go env GOMOD 而不是相对路径：脚本可能从任意目录被调用，
// 而相对路径会让"在 scripts/ 下跑"与"在根目录跑"得到不同结果。
func repoRoot() (string, error) {
	cmd := exec.Command("go", "env", "GOMOD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("无法定位 go.mod: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", fmt.Errorf("当前目录不在 Go 模块内")
	}
	return filepath.Dir(gomod), nil
}

func runCmd(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // 只读文件

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close() //nolint:errcheck // 只写文件

	_, err = io.Copy(out, in)
	return err
}
