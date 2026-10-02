//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// 本文件生成 Windows 的 .msi 安装包。
//
// # 为什么用 WiX 而不是手写 MSI
//
// `.deb` 是手写的（ar 归档 + tar，格式简单到几十行能写完，而且**任何平台**
// 都能生成）。MSI 不是：它是 OLE2/CFB 容器里的一张关系型数据库，
// 含十几个必须相互一致的流（StringPool / Tables / Columns / …），
// 还要按 InstallExecuteSequence 编排动作。手写它大约 800–1500 行，
// 而且错了的表现是"装到一半失败"。
//
// WiX 是这件事的通行工具，值得依赖。
//
// # 为什么锁定 v5 而不是最新的 v7
//
// WiX v7 要求接受 **Open Source Maintenance Fee 的 EULA**（可能涉及付费）。
// 那是使用者要做的**法律决定**，不该由构建脚本替他接受 —— 所以这里
// 刻意用最后一个不受该约束的版本。
//
// **版本要求会写进错误信息**：装错版本的表现是一句语焉不详的 WIX7015，
// 而那句话不告诉人该怎么办。

// BuildMSI 生成 .msi。
//
// wix 不在 PATH 上时返回 ErrWixMissing —— 调用方据此**跳过**而不是失败：
// 打不出 MSI 不影响其余七个平台的产物。
func BuildMSI(opts MSIOptions) error {
	wix, err := exec.LookPath("wix")
	if err != nil {
		return ErrWixMissing
	}

	// MSI 的 Version 字段只接受 major.minor.build 三段数字。
	// 内核的版本串可能带 `-dirty` 或 `v` 前缀，这里收敛掉。
	version, err := msiVersion(opts.Version)
	if err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "isc-msi-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir) //nolint:errcheck // 临时目录

	// wix 按**当前目录**解析 .wxs 里的相对路径，因此把三者放进同一个
	// 临时目录，并把工作目录切过去。
	binary := filepath.Join(dir, "isc.exe")
	if err := copyFile(opts.BinaryPath, binary); err != nil {
		return err
	}

	wxs := filepath.Join(dir, "isc.wxs")
	if err := os.WriteFile(wxs, []byte(renderWXS(version, opts.UpgradeCode)), 0o600); err != nil {
		return err
	}

	out := filepath.Join(dir, "isc.msi")
	cmd := exec.Command(wix, "build", "isc.wxs", "-o", "isc.msi",
		"-arch", "x64",
		// 版本锁在产物里：装错 WiX 时给出的是 WIX7015，
		// 而这里要让它**在构建日志里留下用的是哪个版本**。
		"-bindpath", ".")
	cmd.Dir = dir

	combined, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wix build 失败（需要 WiX %s；v7 起要求接受 OSMF EULA）: %w\n%s",
			msiWixVersion, err, combined)
	}

	return copyFile(out, opts.OutPath)
}

// renderWXS 生成 WiX 的授权文件。
//
// 里面有三个刻意的决定，都写在下面的注释里：**不写 PATH**、
// **不写启动项**、**装到 Program Files 而不是用户目录**。
func renderWXS(version, upgradeCode string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">
  <Package Name="ISC" Manufacturer="ISC" Version="` + version + `"
           UpgradeCode="` + upgradeCode + `"
           Scope="perMachine"
           Compressed="yes">

    <MajorUpgrade DowngradeErrorMessage="已经安装了更新的 ISC 版本。" />
    <MediaTemplate EmbedCab="yes" />

    <!-- Program Files 而不是用户目录：这是一个**系统服务**的宿主，
         装到 HKCU 下会让"以管理员身份安装"变成一件自相矛盾的事。 -->
    <StandardDirectory Id="ProgramFiles64Folder">
      <Directory Id="INSTALLFOLDER" Name="ISC" />
    </StandardDirectory>

    <StandardDirectory Id="ProgramMenuFolder">
      <Directory Id="AppShortcutFolder" Name="ISC" />
    </StandardDirectory>

    <ComponentGroup Id="Main" Directory="INSTALLFOLDER">
      <Component Id="IscExe">
        <File Id="IscExe" Source="isc.exe" KeyPath="yes" />
        <!-- 只放一个开始菜单里的"启动控制台"快捷方式。
             不放开机自启 —— 那由 ` + "`isc service install`" + ` 做，
             让用户自己决定。包一装上就自启，对"我只是想试试"的人来说
             是个不愉快的意外（与 .deb 的取舍一致）。 -->
        <Shortcut Id="ConsoleShortcut"
                  Directory="AppShortcutFolder"
                  Name="ISC 控制台"
                  Description="打开本机验证控制台"
                  Target="[INSTALLFOLDER]isc.exe"
                  Arguments="console" />
      </Component>
    </ComponentGroup>

    <Feature Id="MainFeature" Title="ISC" Level="1">
      <ComponentGroupRef Id="Main" />
    </Feature>
  </Package>
</Wix>
`
}
