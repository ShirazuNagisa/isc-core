//go:build !windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net"
	"os"
	"path/filepath"
	"time"
)

// 本文件实现类 Unix 平台（Linux / macOS / BSD）的本地管理通道：Unix 域套接字。
//
// Unix 套接字以文件系统路径寻址，因此可以用 0600 权限在**文件系统层面**
// 限制谁能连接；配合 runtime.json 里的 token 构成双重防护。

// socketName 是套接字文件名。
const socketName = "isc.sock"

// unixSocketPathWarn 是套接字路径的"该报清楚"阈值。
//
// 真正的上限来自 sockaddr_un.sun_path：macOS 104 字节、Linux 108 字节
// （都含结尾的 NUL）。超过它 net.Listen 只会给出 EINVAL
// （"bind: invalid argument"）—— 那句话指不到"路径太长"。
//
// 阈值取 100 而不是精确上限：报错时把"接近上限"也算进来，
// 比让用户自己去猜"到底多少字节"要好。
const unixSocketPathWarn = 100

// pipeName 仅为满足跨平台接口而存在（类 Unix 无命名管道）。
const pipeName = "isc-core"

// unixSocketTransport 是类 Unix 平台的传输后端。
type unixSocketTransport struct{}

// newLocalTransport 返回类 Unix 平台的本地传输后端。
func newLocalTransport() Transport { return unixSocketTransport{} }

// Describe 实现 describer。
func (unixSocketTransport) Describe() ImplState {
	return ImplState{Available: true, Backend: "unix-socket"}
}

// Listen 在 endpoint 指定的 Unix 套接字上建立监听。
func (unixSocketTransport) Listen(ctx context.Context, endpoint string) (net.Listener, error) {
	return Endpoint(endpoint).Listen(ctx)
}

// localEndpoint 返回类 Unix 平台的默认套接字地址。
func localEndpoint(runDir string) Endpoint {
	return Endpoint(SchemeUnix + "://" + filepath.Join(runDir, socketName))
}

// listenLocal 实现 Unix 套接字的监听。
func listenLocal(_ context.Context, scheme, addr string) (net.Listener, error) {
	if scheme != SchemeUnix {
		return nil, fmt.Errorf(i18n.T("platform.unix_only"), SchemeUnix, scheme)
	}

	// 上次异常退出可能留下 socket 文件，导致 bind 失败（EADDRINUSE）。
	// 只在确认它确实是 socket 时才清理，避免误删同名普通文件。
	if fi, err := os.Lstat(addr); err == nil {
		if fi.Mode()&os.ModeSocket != 0 {
			if err := os.Remove(addr); err != nil {
				return nil, fmt.Errorf(i18n.T("platform.stale_cleanup"), addr, err)
			}
		} else {
			return nil, fmt.Errorf(
				i18n.T("platform.not_socket"), addr)
		}
	}

	l, err := net.Listen("unix", addr)
	if err != nil {
		// 路径长度是 Unix 套接字最容易踩的坑，而内核给出的错误指不到它。
		//
		// 它在本项目里真实发生过：macOS 上 `t.TempDir()` 落在
		// /var/folders/xx/…/T/ 下，一个稍长的测试名再加 `/run/isc.sock`
		// 就顶到 104 字节 —— 症状是**所有**用到数据目录的测试集体失败，
		// 而每条错误看上去都像内核坏了。
		if len(addr) >= unixSocketPathWarn {
			return nil, fmt.Errorf(i18n.T("platform.sock_too_long"),
				len(addr), addr, err)
		}
		return nil, fmt.Errorf(i18n.T("platform.sock_failed"), addr, err)
	}

	// 监听建立后立刻收紧权限：默认 umask 可能让同组或其他用户可连。
	if err := os.Chmod(addr, 0o600); err != nil {
		_ = l.Close()
		_ = os.Remove(addr)
		return nil, fmt.Errorf(i18n.T("platform.chmod_sock"), addr, err)
	}

	return &unixListener{Listener: l, path: addr}, nil
}

// dialLocal 连接 Unix 套接字。
func dialLocal(ctx context.Context, scheme, addr string) (net.Conn, error) {
	if scheme != SchemeUnix {
		return nil, fmt.Errorf(i18n.T("platform.unix_only"), SchemeUnix, scheme)
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, "unix", addr)
}

// unixListener 在关闭监听时清理套接字文件。
//
// 不做这件事的话，每次内核重启都会因为 socket 文件残留而多走一次
// "检测 → 删除 → 重建"的路径；虽然 listenLocal 能处理，但显式清理
// 更干净，也让"内核已退出"在文件系统上可见。
type unixListener struct {
	net.Listener
	path string
}

// Close 关闭监听并删除套接字文件。
func (l *unixListener) Close() error {
	err := l.Listener.Close()
	if rmErr := os.Remove(l.path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		if err == nil {
			err = rmErr
		}
	}
	return err
}
