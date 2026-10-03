//go:build cgo

// Command libisc 把内核编译成**可被其它语言链接的库**（c-shared）。
//
// 这个文件带 `cgo` 构建标签 —— 它是**唯一**要求开 cgo 的目标。少了这个标签，
// `CGO_ENABLED=0 go vet ./...` / `go test ./...` / 发布矩阵都会在这里失败，
// 而内核本体与 CLI 仍然坚持零 cgo（见 docs/DECISIONS.md D24）。
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
// C# 用 DllImport 直接调用。
//
// # 接口为什么是 JSON 进 / JSON 出
//
// 结构体跨语言传递会引入 ABI 对齐与版本漂移；JSON 两端都极稳，而且内核
// 已有的 API 面（internal/api，版本 v1，带 openapi 契约）本身就是 JSON ——
// 库只是把同一条路径搬进进程内，因此 GUI、CLI、控制台看到的行为一致。
//
// # 这一版的定位
//
// **先验证路线，再定形**：只导出跑通"Swift 链接 Go 内核并驱动它"所需的最小
// 面（版本、启动、状态、停止）。最终 API 面要覆盖生命周期、状态、配置、
// 凭据、事件订阅与错误类型，见 docs/DECISIONS.md D24。
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/ShirazuNagisa/isc-core/internal/daemon"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/paths"
	"github.com/ShirazuNagisa/isc-core/internal/runtimeinfo"
	"github.com/ShirazuNagisa/isc-core/internal/version"
)

// 一个进程里只允许一个内核实例：多个实例会抢同一个套接字与数据目录。
var (
	mu      sync.Mutex
	current *kernel
)

type kernel struct {
	daemon *daemon.Daemon
	cancel context.CancelFunc
	done   chan struct{}
	paths  paths.Paths
}

func main() {} // c-shared 需要 main 包，但 main 不会被调用

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func cString(s string) *C.char { return C.CString(s) }

// result 统一返回 {"ok":true,...} 或 {"ok":false,"error":"..."}。
//
// 用 JSON 而不是错误码：调用方拿到的是**可读的原因**，而错误码表要跨语言
// 同步，迟早会漂。
func result(err error, fields map[string]any) *C.char {
	out := map[string]any{}
	if err != nil {
		out["ok"] = false
		out["error"] = err.Error()
	} else {
		out["ok"] = true
		for k, v := range fields {
			out[k] = v
		}
	}
	b, mErr := json.Marshal(out)
	if mErr != nil {
		return cString(fmt.Sprintf(`{"ok":false,"error":%q}`, mErr.Error()))
	}
	return cString(string(b))
}

func goString(p *C.char) string {
	if p == nil {
		return ""
	}
	return C.GoString(p)
}

// ---------------------------------------------------------------------------
// 导出的接口
// ---------------------------------------------------------------------------

// isc_api_version 返回接口版本。GUI 应当在启动时先比对它。
//
//export isc_api_version
func isc_api_version() *C.char { return cString(version.APIVersion) }

// isc_version_json 返回内核版本信息。
//
//export isc_version_json
func isc_version_json() *C.char {
	b, err := json.Marshal(map[string]string{
		"version":    version.Version,
		"commit":     version.Commit,
		"build_time": version.BuildTime,
		"go":         runtime.Version(),
		"platform":   runtime.GOOS + "/" + runtime.GOARCH,
		"api":        version.APIVersion,
	})
	if err != nil {
		return result(err, nil)
	}
	return cString(string(b))
}

// isc_start 在**本进程内**启动内核。dataDir 为空串表示用默认数据目录。
//
// 内核完全就绪后才返回，因此调用方拿到 ok 就能立刻查询状态。
//
//export isc_start
func isc_start(dataDir *C.char) *C.char {
	mu.Lock()
	defer mu.Unlock()

	if current != nil {
		return result(errors.New(i18n.T("libisc.already_running")), nil)
	}

	if dir := goString(dataDir); dir != "" {
		// 数据目录只能通过环境变量交给内核（paths.Resolve 只认它），
		// 因此必须在 Resolve 之前设好。
		if err := os.Setenv(paths.EnvDataDir, dir); err != nil {
			return result(err, nil)
		}
	}

	p, err := paths.Resolve()
	if err != nil {
		return result(err, nil)
	}

	d := daemon.New(daemon.Options{Paths: p, Lang: i18n.Default})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = d.Run(ctx) // Run 阻塞到 ctx 被取消
	}()

	// 等就绪，但要给上限：起不来时必须把原因还给调用方，而不是让它干等。
	select {
	case <-d.Ready():
	case <-done:
		cancel()
		<-done
		return result(errors.New(i18n.T("libisc.exited_during_start")), nil)
	case <-time.After(15 * time.Second):
		cancel()
		<-done
		return result(fmt.Errorf(i18n.T("libisc.start_timeout"), 15), nil)
	}

	info, err := runtimeinfo.Read(d.RuntimeFile())
	if err != nil {
		cancel()
		<-done
		return result(fmt.Errorf(i18n.T("libisc.runtime_info_failed"), err), nil)
	}

	current = &kernel{daemon: d, cancel: cancel, done: done, paths: p}
	return result(nil, map[string]any{
		"endpoint":     info.Endpoint,
		"runtime_file": d.RuntimeFile(),
		"data_dir":     p.Root(),
	})
}

// isc_stop 停止内核并等它真的退出。
//
//export isc_stop
func isc_stop() *C.char {
	mu.Lock()
	k := current
	current = nil
	mu.Unlock()

	if k == nil {
		return result(nil, map[string]any{"stopped": false, "note": i18n.T("libisc.not_running_note")})
	}

	k.cancel()
	select {
	case <-k.done:
	case <-time.After(20 * time.Second):
		return result(fmt.Errorf(i18n.T("libisc.stop_timeout"), 20), nil)
	}
	return result(nil, map[string]any{"stopped": true})
}

// isc_status_json 返回内核状态（与 CLI / 控制台是同一份数据）。
//
// 走内核自己的传输层：**内核既然在本进程里跑，就已经有一个可用端点**；
// 复用这条路径意味着 GUI 看到的状态与 CLI 逐字一致，不会出现"库一套实现、
// CLI 另一套实现"。
//
//export isc_status_json
func isc_status_json() *C.char {
	mu.Lock()
	k := current
	mu.Unlock()

	if k == nil {
		return result(errors.New(i18n.T("libisc.not_running")), nil)
	}

	info, err := runtimeinfo.Read(k.daemon.RuntimeFile())
	if err != nil {
		return result(err, nil)
	}

	// 路径要**对着契约写**：内核里没有 /v1/status 这个接口（第一版猜错，
	// 拿到的是 404）。真正存在的是 /v1/health（内核自身健康）与 /v1/meta
	//（版本、平台、后端能力）。
	//
	// 库这一层把它们合成一个 JSON 返回，调用方一次就能拿全 —— 否则每个
	// GUI 都要自己拼两次请求。
	health, err := getUnix(info.Endpoint, info.Token, "/v1/health")
	if err != nil {
		return result(err, nil)
	}
	meta, err := getUnix(info.Endpoint, info.Token, "/v1/meta")
	if err != nil {
		return result(err, nil)
	}

	var healthObj, metaObj any
	if err := json.Unmarshal([]byte(health), &healthObj); err != nil {
		return result(fmt.Errorf(i18n.T("libisc.health_decode_failed"), err), nil)
	}
	if err := json.Unmarshal([]byte(meta), &metaObj); err != nil {
		return result(fmt.Errorf(i18n.T("libisc.meta_decode_failed"), err), nil)
	}
	b, err := json.Marshal(map[string]any{"health": healthObj, "meta": metaObj})
	if err != nil {
		return result(err, nil)
	}
	return cString(string(b))
}

// isc_free_string 释放本库返回的字符串。**每个**返回值都要用它释放。
//
//export isc_free_string
func isc_free_string(p *C.char) {
	if p != nil {
		C.free(unsafe.Pointer(p))
	}
}

// getUnix 通过 Unix 套接字调用内核接口（带应用令牌）。
func getUnix(endpoint, token, path string) (string, error) {
	const prefix = "unix://"
	if len(endpoint) <= len(prefix) || endpoint[:len(prefix)] != prefix {
		return "", fmt.Errorf(i18n.T("libisc.unsupported_endpoint"), endpoint)
	}
	socket := endpoint[len(prefix):]

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
	}

	// Host 必须是回环地址之一。
	//
	// 内核有一层 DNS rebinding 防护（D22）会拒绝 Host 不是本机地址的请求 ——
	// 第一版这里写的是 `http://isc`，于是请求被挡下：
	//
	//	WARN 拒绝非本机 Host 的请求（可能是 DNS rebinding 尝试） host=isc
	//
	// 那层防护是对的（浏览器发起的跨站请求 Host 会是攻击者的域名），
	// 因此这里改成 127.0.0.1，而不是去放宽内核的校验。
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck // 只读

	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf(i18n.T("libisc.bad_status"), resp.StatusCode, string(buf))
	}
	return string(buf), nil
}
