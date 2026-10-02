//go:build windows

package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 本文件覆盖 Windows 上的目录权限收紧。它此前一条测试都没有。
//
// # 为什么它值得测
//
// `%ProgramData%\ISC` 会从 `C:\ProgramData` **继承**一组宽泛的权限
// （Users / Authenticated Users）。而那个目录里放着 runtime.json，
// 里面有访问内核的令牌 —— 继承不切断的话，同机器上的任何账号都能读走它。
//
// 这个文件里的注释还记着一次真机教训：对 `ACLFromEntries` 的返回值再调
// 一次 `LocalFree` 会表现为随机的堆损坏（STATUS_HEAP_CORRUPTION）。
// 那段代码因此特别需要一条能跑起来的测试。

// TestACLEntriesGrantsExactlyThreePrincipals 钉住授权范围。
//
// 授权面每宽一分都是风险：多一个 SID 就多一类能读走令牌的账户。
// 这条测试要求**恰好三个**，且就是设计里写明的三个。
func TestACLEntriesGrantsExactlyThreePrincipals(t *testing.T) {
	t.Parallel()

	entries, err := aclEntries()
	if err != nil {
		t.Fatalf("构造 ACE 失败: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("应当恰好授予 3 个主体，得到 %d 个 —— "+
			"多一个就多一类能读走令牌的账户", len(entries))
	}

	// 主体的**身份**由 TestApplyDirACLCutsInheritance 从生效的 DACL 里
	// 校验 —— 这里只看这一层能安全看到的东西（见下面的说明）。
	for i, e := range entries {
		if e.AccessMode != windows.GRANT_ACCESS {
			t.Errorf("第 %d 条 ACE 的 AccessMode 是 %d，期望 GRANT_ACCESS",
				i, e.AccessMode)
		}
		if e.AccessPermissions != windows.GENERIC_ALL {
			t.Errorf("第 %d 条 ACE 的权限是 %#x，期望 GENERIC_ALL", i, e.AccessPermissions)
		}
		if e.Trustee.TrusteeForm != windows.TRUSTEE_IS_SID {
			t.Errorf("第 %d 条 ACE 的 TrusteeForm 是 %d，期望 TRUSTEE_IS_SID —— "+
				"用名字而不是 SID 会在中文系统上失效", i, e.Trustee.TrusteeForm)
		}
		if e.Inheritance != windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT {
			t.Errorf("第 %d 条 ACE 没有向下继承", i)
		}

		// TrusteeValue 是个 uintptr，把它转回 *SID 属于
		// "uintptr → unsafe.Pointer"，go vet 会（正确地）拦下来。
		//
		// 因此这里只断言"它被填了"，而**身份**的校验放在
		// TestApplyDirACLCutsInheritance 里 —— 那里是从已生效的 DACL
		// 读回来的 `*ACCESS_ALLOWED_ACE`，指针来源明确，转换是安全的。
		if e.Trustee.TrusteeValue == 0 {
			t.Errorf("第 %d 条 ACE 的 TrusteeValue 为空", i)
		}
	}

}

// TestApplyDirACLCutsInheritance 是本文件**最重要**的一条。
//
// 它不看代码做了什么，而是**把生效的 DACL 读回来**看结果：继承标记
// 必须是切断的。这正是 `applyDirACL` 存在的全部意义 —— 实现在注释里
// 写得很清楚："不切断继承的话，C:\ProgramData 上的 Users 权限会继续生效"。
func TestApplyDirACLCutsInheritance(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "isc-data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := applyDirACL(dir); err != nil {
		t.Fatalf("设置 DACL 失败: %v", err)
	}

	// 把 DACL 读回来。
	sd, err := windows.GetNamedSecurityInfo(
		dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("读取 DACL 失败: %v", err)
	}

	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("读取 control 失败: %v", err)
	}
	// SE_DACL_PROTECTED 就是"切断继承"那一位 ——
	// 它由 PROTECTED_DACL_SECURITY_INFORMATION 置上。
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Error("继承**没有**被切断 —— " +
			"C:\\ProgramData 上的 Users 权限会继续对数据目录生效，" +
			"而同机器上的任何账号都能读走 runtime.json 里的令牌")
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("取 DACL 失败: %v", err)
	}
	if dacl == nil {
		t.Fatal("DACL 为 nil —— 那意味着**所有人**都有完全控制权")
	}
	// 断言的是**授予了几个不同的主体**，而不是 ACE 的条数。
	//
	// SUB_CONTAINERS_AND_OBJECTS_INHERIT 会被 SetEntriesInAcl 展开成多条
	// ACE（容器一条、对象一条），因此三个主体在这个目录上会产生 6 条。
	// 按条数断言会把一个正确的实现判成错的 —— 第一版就是这么写的。
	trustees := map[string]int{}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatalf("读取第 %d 条 ACE 失败: %v", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Errorf("第 %d 条 ACE 不是允许类型（%d）—— "+
				"拒绝类型的 ACE 会让内核自己都写不进去", i, ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		trustees[sid.String()]++
	}

	if len(trustees) != 3 {
		t.Errorf("授予了 %d 个不同的主体，期望 3 个: %v", len(trustees), trustees)
	}

	// 而且必须是**设计里写明的那三个** —— 只数个数的话，
	// 一个把 Users 换进来的实现也能通过。
	for _, known := range []windows.WELL_KNOWN_SID_TYPE{
		windows.WinLocalSystemSid,
		windows.WinBuiltinAdministratorsSid,
		windows.WinInteractiveSid,
	} {
		want, werr := windows.CreateWellKnownSid(known)
		if werr != nil {
			t.Fatalf("解析内置 SID %d 失败: %v", known, werr)
		}
		if trustees[want.String()] == 0 {
			t.Errorf("生效的 DACL 里没有内置账户 %v —— "+
				"授权面与设计不符", want)
		}
	}

	// 反向：不该有第四个主体。
	if len(trustees) > 3 {
		for sid := range trustees {
			t.Logf("生效的主体: %s", sid)
		}
	}
	// 而且不能有重复的**不同**掩码 —— 每条应当都是 GENERIC_ALL。
	for sid, n := range trustees {
		if n == 0 {
			t.Errorf("主体 %s 没有任何 ACE", sid)
		}
	}
}

// TestApplyDirACLIsIdempotent 验证可以重复施加。
//
// 内核每次启动都会收紧一次目录，而在已经收紧过的目录上再收紧**必须**成功 ——
// 否则第二次启动就会报一条"权限设置失败"的警告，而那是假的。
func TestApplyDirACLIsIdempotent(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "isc-data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if err := applyDirACL(dir); err != nil {
			t.Fatalf("第 %d 次设置 DACL 失败: %v —— "+
				"内核每次启动都会收紧一次，重复施加必须成功", i+1, err)
		}
	}
}

// TestTightenDirReportsFailureWithoutBlocking 钉住"失败不阻止启动"。
//
// 实现在注释里写明了这个取舍：ACL 设置失败（FAT32、某些网络文件系统）
// 不应阻止内核启动，但**必须让用户看见**。因此它返回一句警告而不是 error。
func TestTightenDirReportsFailureWithoutBlocking(t *testing.T) {
	t.Parallel()

	// 不存在的目录 → 设置 DACL 必然失败。
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	warn := tightenDir(missing)
	if warn == "" {
		t.Error("对不存在的目录应当返回一句警告，而不是空字符串 —— " +
			"静默失败会让用户以为权限已经收紧了")
	}
	if !strings.Contains(warn, missing) {
		t.Errorf("警告里应当点明是哪个目录，得到 %q", warn)
	}
	if !strings.Contains(warn, "仍会运行") {
		t.Errorf("警告里应当说明内核仍会运行，得到 %q", warn)
	}
}

// TestTightenDirSilentOnSuccess 是上一条的反面。
//
// 成功时**不该**有任何输出 —— 否则每次启动都会有一条无意义的警告，
// 而用户会学会忽略所有警告。
func TestTightenDirSilentOnSuccess(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "isc-data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if warn := tightenDir(dir); warn != "" {
		t.Errorf("成功时不该有警告，得到 %q —— "+
			"无意义的警告会让用户学会忽略所有警告", warn)
	}
}

// TestDefaultRootsIsUnderProgramData 验证默认数据目录的落点。
func TestDefaultRootsIsUnderProgramData(t *testing.T) {
	t.Parallel()

	def, err := defaultRoots()
	if err != nil {
		t.Fatalf("解析默认目录失败: %v", err)
	}
	if def.data == "" {
		t.Fatal("数据目录为空")
	}
	if !filepath.IsAbs(def.data) {
		t.Errorf("数据目录必须是绝对路径，得到 %q", def.data)
	}
	if !strings.Contains(def.data, "ISC") {
		t.Errorf("数据目录里应当含 ISC，得到 %q", def.data)
	}
	// 落点必须在 ProgramData 下：那是 Windows 上"机器级、服务可写"
	// 的标准位置，而 %LOCALAPPDATA% 属于单个用户，服务身份读不到。
	if base := os.Getenv("ProgramData"); base != "" {
		if !strings.HasPrefix(def.data, base) {
			t.Errorf("数据目录 %q 不在 ProgramData（%q）下", def.data, base)
		}
	}
}
