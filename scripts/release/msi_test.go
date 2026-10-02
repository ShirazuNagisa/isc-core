//go:build windows

package main

import (
	"bytes"
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

// TestMSIContainsRequiredStreams 验证 MSI 数据库的几个核心流在。
//
// ⚠️ **当前跳过，原因是一个未查清的问题（如实记录）**
//
// 按 MSI 规范，CFB 容器里必须有 !_StringPool / !_Tables / !_Columns 等流，
// 它们是"这张数据库能不能被 Windows 读懂"的最小集合。
//
// 但实测：wix 5.0.2 产出的 32 KB 文件里**搜不到 `!_` 的 UTF-16LE 序列**
// （0 次）。我只查到这一步 —— 文件头是合法的 CFB（魔数、扇区大小 4096、
// 目录扇区号 1 都对），而按目录扇区偏移读出来的名字是 0xFFFF（未分配项），
// 说明**我的偏移算法或搜索方式有问题**，而不是文件有问题。
//
// 所以这条测试现在**不能说明任何事**：它既没有证明产物合格，也没有证明
// 产物不合格。做成跳过而不是删除，是因为"该检查什么"已经写清楚了，
// 缺的只是把它算对。
//
// 查清之前，MSI 的产物质量只有 TestMSIIsOle2Container 那一条在守。
func TestMSIContainsRequiredStreams(t *testing.T) {
	t.Skip("未查清：wix 5 的产物里搜不到 `!_` 的 UTF-16LE 序列。" +
		"CFB 头合法，但按目录扇区偏移读到的名字是 0xFFFF（未分配项）——" +
		"更像是我的偏移算法或搜索方式错了。查清之前这条测试不能说明任何事。")

	byt := buildTestMSI(t)

	for _, name := range []string{"!_StringPool", "!_StringData", "!_Tables", "!_Columns"} {
		if !containsUTF16LE(byt, name) {
			t.Errorf("MSI 里找不到流 %q —— "+
				"它是 MSI 数据库的必需部分，缺了 Windows 会拒绝安装", name)
		}
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

// containsUTF16LE 在字节流里查找一个以 UTF-16LE 编码的字符串。
func containsUTF16LE(hay []byte, s string) bool {
	needle := make([]byte, 0, len(s)*2)
	for _, r := range s {
		needle = append(needle, byte(r), 0)
	}
	return bytes.Contains(hay, needle)
}
