// Package runtimeinfo 读写内核的运行时描述文件 runtime.json。
//
// 该文件是**客户端发现内核的唯一入口**：CLI、验证控制台与下游 GUI
// 都通过它拿到传输地址与访问令牌。见 docs/ARCHITECTURE.md §8.2。
//
// 安全约束（docs/DECISIONS.md D09）：
//
//   - 文件含访问令牌，权限必须为 0600；
//   - 所在目录（<数据根>/run）权限收紧至仅本机用户可读；
//   - 内核退出时必须删除该文件，避免残留误导客户端。
package runtimeinfo

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"os"
	"path/filepath"
	"time"
)

// FileName 是运行时描述文件的固定文件名。
const FileName = "runtime.json"

// Info 是 runtime.json 的内容。
type Info struct {
	// PID 是内核进程 ID，用于判断文件是否为残留。
	PID int `json:"pid"`

	// Version 是内核版本。
	Version string `json:"version"`

	// Endpoint 是首选传输地址，形如：
	//
	//	npipe:////./pipe/isc-core      Windows 命名管道
	//	unix:///var/lib/isc/run/isc.sock  Unix 域套接字
	//
	// 客户端应优先尝试它 —— 它不经由 TCP 栈，权限由文件系统 / ACL 控制。
	Endpoint string `json:"endpoint"`

	// FallbackEndpoint 是回环 HTTP 地址，形如 http://127.0.0.1:52341。
	//
	// 它是浏览器（验证控制台）唯一能使用的入口，因为浏览器无法连接
	// 命名管道或 Unix 套接字。同时作为首选传输不可用时的回退。
	FallbackEndpoint string `json:"fallback_endpoint,omitempty"`

	// Token 是访问令牌（base64url，32 字节随机值）。
	//
	// 所有请求都必须携带 `Authorization: Bearer <token>`，
	// 即使是走回环 TCP 也必须 —— 防止本机其他用户或浏览器发起的
	// 跨站请求伪造。
	Token string `json:"token"`

	// StartedAt 是内核启动时间。
	StartedAt time.Time `json:"started_at"`
}

// Write 原子地写入 runtime.json。
//
// 先写临时文件再重命名，避免客户端读到写了一半的内容。
func Write(path string, info Info) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf(i18n.T("runtime.err.mkdir"), err)
	}

	byt, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf(i18n.T("runtime.err.marshal"), err)
	}
	byt = append(byt, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-*.json")
	if err != nil {
		return fmt.Errorf(i18n.T("runtime.err.tempfile"), err)
	}
	tmpName := tmp.Name()
	// 任何失败路径都要清掉临时文件。
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf(i18n.T("runtime.err.chmod"), err)
	}
	if _, err := tmp.Write(byt); err != nil {
		_ = tmp.Close()
		return fmt.Errorf(i18n.T("runtime.err.write"), err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf(i18n.T("runtime.err.sync"), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf(i18n.T("runtime.err.close"), err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf(i18n.T("runtime.err.rename"), path, err)
	}
	tmpName = "" // 已重命名，无需清理
	return nil
}

// Read 读取 runtime.json。
func Read(path string) (Info, error) {
	var info Info
	byt, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(byt, &info); err != nil {
		return info, fmt.Errorf(i18n.T("runtime.err.parse"), path, err)
	}
	return info, nil
}

// Remove 删除 runtime.json，忽略"文件不存在"。
func Remove(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// IsStale 报告 runtime.json 是否属于一个已经不存在的进程。
//
// 内核以非正常方式退出（被 kill -9、断电）时文件会残留，
// 客户端据此区分"内核没在跑"与"内核崩过"。
//
// 注意：PID 可能被复用，因此这只是启发式判断，不能作为安全依据。
func (i Info) IsStale() bool {
	if i.PID <= 0 {
		return true
	}
	return !processAlive(i.PID)
}

// processAlive 报告指定 PID 的进程是否存在，由各平台文件实现：
//
//	process_windows.go  OpenProcess + GetExitCodeProcess
//	                    （os.FindProcess 在 Windows 上恒成功，无法用于存活判断）
//	process_unix.go     signal 0 探测
