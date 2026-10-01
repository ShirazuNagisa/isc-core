//go:build windows

package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/windows"
)

// defaults 是 Windows 上的目录布局。
type defaults struct {
	data   string
	config string
}

// defaultRoots 返回 Windows 的默认目录。
//
// 使用 %ProgramData%（即 C:\ProgramData）而不是 %APPDATA%：
// 内核以 Windows 服务身份运行，其账户与交互用户不同，
// 使用机器级目录才能让服务与用户看到同一份数据。
// 见 docs/DECISIONS.md D20。
func defaultRoots() (defaults, error) {
	base := os.Getenv("ProgramData")
	if base == "" {
		// 极少数情况下环境变量缺失，退回系统盘的默认位置。
		drive := os.Getenv("SystemDrive")
		if drive == "" {
			drive = "C:"
		}
		base = filepath.Join(drive+string(filepath.Separator), "ProgramData")
	}
	root := filepath.Join(base, "ISC")
	return defaults{data: root, config: root}, nil
}

// tightenDir 用显式的受保护 DACL 收紧目录访问权限。
//
// 为什么必须显式设置：%ProgramData%\ISC 会从 C:\ProgramData 继承一组
// 宽泛的权限（Users / Authenticated Users），其中包含的账户范围远比
// 本内核需要的宽。显式设置一个**受保护的** DACL（PROTECTED_DACL_…
// 会切断继承）把授权范围收敛到三个明确的 SID。
//
// 授权范围：
//
//	LOCAL SYSTEM        完全控制（内核以服务身份运行时就是它）
//	BUILTIN\Administrators  完全控制（管理员本就拥有一切）
//	INTERACTIVE         完全控制（CLI、验证控制台、下游 GUI 以交互用户身份运行）
//
// ⚠️ 已知局限（记录在 docs/DECISIONS.md D09 与 docs/PLAN.md R13）：
// runtime.json 含访问令牌，因此"可读"等价于"可控制内核"。在多用户共享的
// 机器上，任何交互登录的用户都能读取该令牌，进而控制一个以 SYSTEM 运行的
// 服务 —— 这构成一条本地提权路径。目标场景（家用单用户机器）下不构成问题，
// 但正规的修复是**按会话授权**：识别命名管道客户端的会话，只放行控制台
// 会话的用户与管理员。该方案已设计但排在 M5 之后。
func tightenDir(dir string) string {
	if err := applyDirACL(dir); err != nil {
		// 不返回 error 而是返回警告：ACL 设置失败（例如在 FAT32 或
		// 某些网络文件系统上）不应阻止内核启动，但必须让用户看见。
		return fmt.Sprintf("收紧 %s 的访问权限失败，内核仍会运行但令牌可能被其他用户读取：%v", dir, err)
	}
	return ""
}

// applyDirACL 为目录设置一个显式、受保护的 DACL。
func applyDirACL(dir string) error {
	entries, err := aclEntries()
	if err != nil {
		return err
	}

	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("构造访问控制列表失败: %w", err)
	}
	// ⚠️ 不要对 acl 调用 LocalFree。
	//
	// windows.ACLFromEntries 内部已经用 LocalFree 归还了 setEntriesInAcl
	// 在 Windows 堆上分配的原始 ACL，并返回一个指向 **Go 堆切片** 的指针。
	// 对它再调一次 LocalFree 就是用 LocalFree 释放 Go 内存 ——
	// 表现为随机的 STATUS_HEAP_CORRUPTION (0xc0000374)，极难定位。
	//
	// SetNamedSecurityInfo 会复制 ACL 的内容，不保留该指针；
	// KeepAlive 只是为了保证在这次调用期间切片不被回收。
	err = windows.SetNamedSecurityInfo(
		dir,
		windows.SE_FILE_OBJECT,
		// PROTECTED_DACL_SECURITY_INFORMATION 切断从父目录的继承 ——
		// 这是本函数存在的全部意义：不切断继承的话，
		// C:\ProgramData 上的 Users 权限会继续生效。
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil,
	)
	runtime.KeepAlive(acl)
	if err != nil {
		return fmt.Errorf("设置目录安全信息失败: %w", err)
	}
	return nil
}

// aclEntries 构造授权条目。
//
// 新目录与文件都要继承（SUB_CONTAINERS_AND_OBJECTS_INHERIT），
// 否则 runtime.json 本身不会有正确的权限。
func aclEntries() ([]windows.EXPLICIT_ACCESS, error) {
	sids := make([]*windows.SID, 0, 3)
	for _, t := range []windows.WELL_KNOWN_SID_TYPE{
		windows.WinLocalSystemSid,
		windows.WinBuiltinAdministratorsSid,
		windows.WinInteractiveSid,
	} {
		sid, err := windows.CreateWellKnownSid(t)
		if err != nil {
			return nil, fmt.Errorf("解析内置账户 SID (类型 %d) 失败: %w", t, err)
		}
		sids = append(sids, sid)
	}

	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	for _, sid := range sids {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	return entries, nil
}
