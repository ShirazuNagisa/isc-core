//go:build cgo

// Command libisc 把内核编译成**可被其它语言链接的库**（c-shared）。
//
// # 为什么是 c-shared
//
// GUI 在 macOS 上用 Swift、在 Windows 上用 C# —— 两者能链接的都只有
// **C ABI**。因此内核的库化形态是：
//
//	go build -buildmode=c-shared -o libisc.dylib ./cmd/libisc   # macOS
//	go build -buildmode=c-shared -o isc.dll      ./cmd/libisc   # Windows（需 MinGW 或 MSVC）
//
// cgo 会同时产出头文件（libisc.h / isc.h），Swift 用 bridging header、
// C# 用 DllImport 直接调用。构建脚本见 scripts/build-libisc.sh。
//
// # 接口清单（第一版）
//
//	isc_api_version()                    接口版本（GUI 启动时应先比对）
//	isc_version_json()                   内核版本信息
//	isc_start(dataDir)                   在本进程内启动内核，就绪后返回
//	isc_stop()                           停止并等待退出（幂等）
//	isc_restart(dataDir)                 重启
//	isc_status_json()                    便捷：health + meta（合成一份）
//	isc_call(method, path, body)         **通用调用**：契约里的任意路径
//	isc_events_json(sinceSeq, timeoutMs) 事件订阅（游标式长轮询，无 C 回调）
//	isc_free_string(p)                   释放上面每个返回值
//
// 通用调用是核心：接口面由 api/openapi.yaml 的契约定义（有 spec-drift 检查
// 守着），因此**新增功能不需要动库**，GUI 直接按契约调即可。其余函数是
// 常用路径的糖。
//
// 详细说明（JSON 形状、错误码、内存与线程约定）见 docs/LIBRARY-API.md。
//
// # 分层
//
// 这个文件只做薄包装（C 字符串 ↔ Go 字符串），全部行为在 internal/libisc 里
// —— 那一层不依赖 cgo，因此在默认 CI 里就有测试覆盖。
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"unsafe"

	"github.com/ShirazuNagisa/isc-core/internal/libisc"
)

func main() {} // c-shared 需要 main 包，但 main 不会被调用

func cString(s string) *C.char { return C.CString(s) }

func goString(p *C.char) string {
	if p == nil {
		return ""
	}
	return C.GoString(p)
}

// isc_api_version 返回接口版本。GUI 应当在启动时先比对它。
//
//export isc_api_version
func isc_api_version() *C.char { return cString(libisc.APIVersion()) }

// isc_version_json 返回内核版本信息。
//
//export isc_version_json
func isc_version_json() *C.char { return cString(libisc.Marshal(libisc.VersionInfo())) }

// isc_start 在**本进程内**启动内核。dataDir 为空串表示用默认数据目录。
//
//export isc_start
func isc_start(dataDir *C.char) *C.char {
	return cString(libisc.Marshal(libisc.Start(goString(dataDir))))
}

// isc_stop 停止内核并等它真的退出（幂等）。
//
//export isc_stop
func isc_stop() *C.char { return cString(libisc.Marshal(libisc.Stop())) }

// isc_restart 重启内核。
//
//export isc_restart
func isc_restart(dataDir *C.char) *C.char {
	return cString(libisc.Marshal(libisc.Restart(goString(dataDir))))
}

// isc_status_json 返回内核状态（health + meta 合成）。
//
//export isc_status_json
func isc_status_json() *C.char { return cString(libisc.Marshal(libisc.Status())) }

// isc_call 调用契约里的任意路径（进程内派发）。
//
//export isc_call
func isc_call(method, path, body *C.char) *C.char {
	return cString(libisc.Marshal(libisc.Call(goString(method), goString(path), goString(body))))
}

// isc_events_json 拉取事件（游标式长轮询）。
//
//export isc_events_json
func isc_events_json(sinceSeq C.longlong, timeoutMs C.int) *C.char {
	return cString(libisc.Marshal(libisc.Events(int64(sinceSeq), int(timeoutMs))))
}

// isc_free_string 释放本库返回的字符串。**每个**返回值都要用它释放。
//
//export isc_free_string
func isc_free_string(p *C.char) {
	if p != nil {
		C.free(unsafe.Pointer(p))
	}
}
