//go:build windows

package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件验证 .msi 真的能生成，而且生成的东西**是一份合法的 MSI**。
//
// 与 deb_test.go 同样的思路：产物不是"写出来了"就算数，要能按格式解析回来。
// MSI 是 OLE2/CFB 容器，最省事也最有力的检查是**魔数**与**若干必需的流名**。
//
// wix 不在 PATH 上时跳过：这条是加固，不是构建前置条件。

func requireWix(t *testing.T) string {
	t.Helper()

	wix, err := exec.LookPath("wix")
	if err != nil {
		t.Skip("没有找到 wix；跳过 MSI 测试")
	}
	return wix
}

// buildTestMSI 生成一个用于检查的 .msi。
func buildTestMSI(t *testing.T) []byte {
	t.Helper()
	requireWix(t)

	dir := t.TempDir()
	exe := filepath.Join(dir, "isc.exe")
	// 内容不重要，形状重要：wix 会把它按 PE 处理前先读大小与时间。
	if err := os.WriteFile(exe, bytes.Repeat([]byte{0x4D, 0x5A}, 4096), 0o600); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out.msi")
	err := BuildMSI(MSIOptions{
		BinaryPath:  exe,
		Version:     "0.1.0",
		UpgradeCode: "9A1B2C3D-4E5F-6071-8293-A4B5C6D7E8F9",
		OutPath:     out,
	})
	if err != nil {
		t.Fatalf("生成 MSI 失败: %v", err)
	}

	byt, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return byt
}

// TestMSIIsOle2Container 验证它是 CFB 容器。
//
// 这七个字节是 OLE2 的签名。它们不对，说明 wix 写出来的不是 MSI 而是别的
// 东西 —— 而 Windows 安装时的表现会是一句"不是有效的安装包"。
func TestMSIIsOle2Container(t *testing.T) {
	byt := buildTestMSI(t)

	want := []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
	if len(byt) < len(want) {
		t.Fatalf("产物只有 %d 字节，连文件头都不完整", len(byt))
	}
	if !bytes.Equal(byt[:8], want) {
		t.Errorf("OLE2 魔数不对：得到 % X，期望 % X", byt[:8], want)
	}
}

// TestMSIInstallsAdministratively 用 **Windows Installer 自己**验证产物。
//
// # 为什么换成这个办法
//
// 上一版试的是"在文件里搜 `!_StringPool` 等流名"。它搜不到，于是我一度
// 怀疑产物有问题 —— 直到换成 `msiexec /a`：
//
//	msiexec /a probe.msi /qn TARGETDIR=…   → 退出 0，解出 PFiles64\Probe\hello.txt
//
// **产物一直是好的，错的是我的检查方式。** 原因是 MSI 的流名在 CFB 里
// 用一套**特殊的 MSI 编码**存放（基址 U+4840），按普通 UTF-16 搜自然找不到。
//
// 这件事的教训比测试本身值钱：**当"检查失败"与"事实"矛盾时，先怀疑检查。**
// 我当时在字节层面反推了很久，而正确的动作是**去问那个真正会读它的程序**。
//
// `/a` 是**管理安装**：它校验数据库并把文件解到指定目录，**不真正安装**，
// 因此对一个测试来说是安全的。
func TestMSIInstallsAdministratively(t *testing.T) {
	requireWix(t)

	dir := t.TempDir()
	exe := filepath.Join(dir, "isc.exe")
	if err := os.WriteFile(exe, bytes.Repeat([]byte{0x4D, 0x5A}, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	msi := filepath.Join(dir, "isc.msi")
	if err := BuildMSI(MSIOptions{
		BinaryPath:  exe,
		Version:     "0.1.0",
		UpgradeCode: "9A1B2C3D-4E5F-6071-8293-A4B5C6D7E8F9",
		OutPath:     msi,
	}); err != nil {
		t.Fatalf("生成 MSI 失败: %v", err)
	}

	target := filepath.Join(dir, "extract")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	// /qn 静默，/a 管理安装（不注册、不写系统目录）。
	cmd := exec.Command("msiexec", "/a", msi, "/qn", "TARGETDIR="+target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("msiexec /a 失败（产物不是 Windows Installer 认得的包）: %v\n%s",
			err, out)
	}

	// 解出来的目录结构也要对：装到 Program Files 下的 ISC，
	// 而不是某个用户目录。
	var found string
	err := filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), "isc.exe") {
			found = p
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == "" {
		t.Fatalf("管理安装没有解出 isc.exe —— 包是合法的，但内容不对。\n"+
			"解出来的东西在 %s 下", target)
	}
	if !strings.Contains(strings.ToLower(found), "program files") &&
		!strings.Contains(strings.ToLower(found), "pfiles") {
		t.Errorf("isc.exe 被装到了 %q —— 期望在 Program Files 之下（perMachine）", found)
	}
}

// TestMSIVersionIsSanitised 验证版本号被收敛成 x.y.z。
//
// MSI 的 Version 字段只接受三段数字。内核的版本串可能带 `v` 前缀或
// `-dirty` 后缀，直接塞进去会被 wix 拒绝 —— 而那句报错不会告诉人
// "你的版本串格式不对"。
func TestMSIVersionIsSanitised(t *testing.T) {
	t.Parallel()

	ok := []struct{ in, want string }{
		{"0.1.0", "0.1.0"},
		{"v0.1.0", "0.1.0"},
		{"0.1.0-dirty", "0.1.0"},
		{"0.1.0+meta", "0.1.0"},
		{"1.2", "1.2.0"},
		{"1", "1.0.0"},
	}
	for _, tc := range ok {
		got, err := msiVersion(tc.in)
		if err != nil {
			t.Errorf("msiVersion(%q) 报错: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("msiVersion(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}

	// 这些必须**报错**而不是猜一个数字出来 —— 猜的后果是装上一个
	// 版本号错误的包，而升级逻辑靠版本号认亲。
	for _, bad := range []string{"", "abc", "1.x.0", "..", "v"} {
		if got, err := msiVersion(bad); err == nil {
			t.Errorf("msiVersion(%q) 返回了 %q 而没有报错 —— "+
				"版本号是升级逻辑的依据，不能猜", bad, got)
		}
	}
}

// TestMSIMissingWixIsNotFatal 验证没有 wix 时返回的是哨兵错误。
//
// 调用方靠 errors.Is(err, ErrWixMissing) 决定**跳过**而不是失败：
// 打不出 MSI 不该让其余七个平台的产物也拿不到。
func TestMSIMissingWixIsNotFatal(t *testing.T) {
	// 把 PATH 清空到一个不可能有 wix 的目录。
	t.Setenv("PATH", t.TempDir())

	err := BuildMSI(MSIOptions{
		BinaryPath:  "does-not-matter.exe",
		Version:     "0.1.0",
		UpgradeCode: "9A1B2C3D-4E5F-6071-8293-A4B5C6D7E8F9",
		OutPath:     filepath.Join(t.TempDir(), "x.msi"),
	})
	if err == nil {
		t.Fatal("PATH 上没有 wix 时应当报错")
	}
	if !strings.Contains(err.Error(), "wix") {
		t.Errorf("错误信息里应当点明缺的是 wix，得到: %v", err)
	}
}
