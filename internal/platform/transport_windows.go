//go:build windows

package platform

import (
	"context"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
)

// 本文件实现 Windows 的本地管理通道：命名管道。
//
// 为什么不直接用回环 TCP：命名管道不出现在 netstat 里、不与任何端口冲突，
// 且可以用安全描述符在**内核层面**限制谁能连接。回环 TCP 仍然保留，
// 因为浏览器（验证控制台）无法连接命名管道。

// pipeName 是内核管理通道的管道名。
//
// 使用固定名称而非随机名：客户端（CLI / GUI）无需先读 runtime.json
// 就能定位内核；真正的访问控制由安全描述符 + token 双重保证。
const pipeName = "isc-core"

// socketName 仅为满足跨平台接口而存在（Windows 无 Unix 套接字）。
const socketName = "isc.sock"

// pipeSecurityDescriptor 是命名管道的安全描述符（SDDL）。
//
// 逐个说明：
//
//	D:P                                  受保护的 DACL，不继承父对象权限
//	(A;;GA;;;SY)                         允许 LOCAL SYSTEM 完全访问
//	(A;;GA;;;BA)                         允许 Administrators 完全访问
//	(A;;GA;;;IU)                         允许交互登录用户完全访问
//
// 为什么这样写：
//
//   - 内核以 Windows 服务（LocalSystem）身份运行时，管道由 SYSTEM 创建；
//     CLI 与控制台以交互用户身份运行，必须显式授予 IU 才能连接。
//   - **不授予 `WD`（Everyone）与 `AN`（Anonymous）**：没有 DACL 项就没有访问权。
//   - **不授予 `NU`（Network logon）**：远程访问命名管道会以网络登录身份到达，
//     其令牌不含 INTERACTIVE（IU）SID，因此会被拒绝。这阻断了
//     `\\host\pipe\isc-core` 形式的远程连接。
//
// 注意：管道 ACL 只是纵深防御的一层。真正决定"能否操作内核"的是
// runtime.json 里的随机 token（见 docs/DECISIONS.md D09）——
// 本机任何用户都能连接管道，但没有 token 就什么也做不了。
const pipeSecurityDescriptor = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;IU)"

// namedPipeTransport 是 Windows 平台的传输后端。
type namedPipeTransport struct{}

// newLocalTransport 返回 Windows 的本地传输后端。
func newLocalTransport() Transport { return namedPipeTransport{} }

// Describe 实现 describer。
func (namedPipeTransport) Describe() ImplState {
	return ImplState{Available: true, Backend: "winio-named-pipe"}
}

// Listen 在 endpoint 指定的命名管道上建立监听。
func (namedPipeTransport) Listen(ctx context.Context, endpoint string) (net.Listener, error) {
	return Endpoint(endpoint).Listen(ctx)
}

// localEndpoint 返回 Windows 的默认管道地址。
//
// runDir 在 Windows 上被忽略：命名管道位于 \\.\pipe\ 命名空间，
// 不依赖文件系统路径。
func localEndpoint(string) Endpoint {
	return Endpoint(SchemeNamedPipe + "://./pipe/" + pipeName)
}

// listenLocal 实现命名管道的监听。
func listenLocal(_ context.Context, scheme, addr string) (net.Listener, error) {
	if scheme != SchemeNamedPipe {
		return nil, fmt.Errorf("platform: Windows 仅支持 %s 传输，收到 %s", SchemeNamedPipe, scheme)
	}
	l, err := winio.ListenPipe(addr, &winio.PipeConfig{
		SecurityDescriptor: pipeSecurityDescriptor,
		// 管理接口是请求-响应式的小消息，默认缓冲区足够；
		// 显式写出以便日后调整时有据可依。
		InputBufferSize:  4096,
		OutputBufferSize: 4096,
	})
	if err != nil {
		return nil, fmt.Errorf("platform: 创建命名管道 %s 失败: %w", addr, err)
	}
	return l, nil
}

// dialLocal 连接命名管道。
func dialLocal(ctx context.Context, scheme, addr string) (net.Conn, error) {
	if scheme != SchemeNamedPipe {
		return nil, fmt.Errorf("platform: Windows 仅支持 %s 传输，收到 %s", SchemeNamedPipe, scheme)
	}
	return winio.DialPipeContext(ctx, addr)
}
