//go:build !windows

package platform

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖类 Unix 平台的本地管理通道：Unix 域套接字。
//
// # 它为什么必需
//
// 在这之前，`listenLocal` / `dialLocal` **没有任何测试真正跑过它们** ——
// endpoint_test.go 只测了 `unix://` 这种地址串的**解析**，而解析对了不等于
// 套接字能建起来、能连上、能在退出时被清理。
//
// 而 UDS 是这个项目在 Linux / macOS 上唯一的本地管理通道（见
// docs/DECISIONS.md D08）：Windows 用命名管道，其余平台全靠它。
// 它坏了，整个 CLI 与验证控制台在那些平台上都连不上内核。
//
// 注意：本文件带 `!windows` 构建标签，因此**在 Windows 开发机上不会运行**，
// 只在 CI 的 Linux / macOS 上跑。这恰恰是它存在的理由 —— 那些平台上的
// 行为不能只靠"代码看起来对"。

// TestUnixSocketRoundTrip 是这套机制的核心断言：建、连、收发、关。
func TestUnixSocketRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sock := filepath.Join(dir, "isc.sock")

	ln, err := listenLocal(context.Background(), SchemeUnix, sock)
	if err != nil {
		t.Fatalf("建立监听失败: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// 服务端：回显一行。
	done := make(chan error, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			done <- aerr
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 64)
		n, rerr := conn.Read(buf)
		if rerr != nil {
			done <- rerr
			return
		}
		_, werr := conn.Write([]byte("echo:" + string(buf[:n])))
		done <- werr
	}()

	// 客户端。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := dialLocal(ctx, SchemeUnix, sock)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got := string(buf[:n]); got != "echo:hello" {
		t.Errorf("往返结果是 %q，期望 %q", got, "echo:hello")
	}
	if err := <-done; err != nil {
		t.Errorf("服务端出错: %v", err)
	}
}

// TestUnixSocketPermissionsAre0600 钉住权限收紧。
//
// 套接字以文件系统路径寻址，因此**权限就是访问控制**。默认 umask 可能让
// 同组或其他用户可连 —— 那意味着同机器上的任何账号都能调用内核接口。
func TestUnixSocketPermissionsAre0600(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sock := filepath.Join(dir, "isc.sock")

	ln, err := listenLocal(context.Background(), SchemeUnix, sock)
	if err != nil {
		t.Fatalf("建立监听失败: %v", err)
	}
	defer func() { _ = ln.Close() }()

	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat 套接字失败: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("套接字权限是 %o，期望 600 —— "+
			"权限就是这条通道的访问控制", perm)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("路径上的东西不是套接字：mode=%v", fi.Mode())
	}
}

// TestCloseRemovesSocketFile 验证退出时清理。
//
// 不清理的话，"内核是否在运行"在文件系统上就看不出来，而每次重启都要
// 多走一次"检测残留 → 删除 → 重建"。
func TestCloseRemovesSocketFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sock := filepath.Join(dir, "isc.sock")

	ln, err := listenLocal(context.Background(), SchemeUnix, sock)
	if err != nil {
		t.Fatalf("建立监听失败: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("关闭后套接字文件仍在（err=%v）—— "+
			"内核已退出这件事应当在文件系统上可见", err)
	}
}

// TestStaleSocketIsReclaimed 验证异常退出后的残留能被清理。
//
// 内核崩溃或被 kill -9 时不会走 Close，于是 socket 文件留在原地，
// 下一次 bind 会 EADDRINUSE。
func TestStaleSocketIsReclaimed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sock := filepath.Join(dir, "isc.sock")

	// 制造一个"残留"：建一个监听再**不关闭**，直接让进程继续，
	// 然后手工把它变成不可用的状态 —— 更接近真实的是"文件在，
	// 但没有进程监听"，而 net.Listen 对同一个路径会失败。
	first, err := listenLocal(context.Background(), SchemeUnix, sock)
	if err != nil {
		t.Fatalf("第一次监听失败: %v", err)
	}
	// 直接关掉底层监听但**不删除文件**，模拟崩溃留下的残留。
	if ul, ok := first.(*unixListener); ok {
		_ = ul.Listener.Close()
	} else {
		_ = first.Close()
	}

	if _, err := os.Lstat(sock); err != nil {
		t.Skipf("底层监听关闭时已删除文件，无法构造残留场景: %v", err)
	}

	// 第二次监听应当清理掉残留并成功。
	second, err := listenLocal(context.Background(), SchemeUnix, sock)
	if err != nil {
		t.Fatalf("残留套接字没有被清理，第二次监听失败: %v", err)
	}
	defer func() { _ = second.Close() }()
}

// TestRefusesToOverwriteRegularFile 钉住一条安全边界。
//
// 清理残留时**必须确认那确实是个套接字**。不加这条判断的话，一个同名
// 的普通文件（用户自己的数据）会在内核启动时被无声地删掉。
func TestRefusesToOverwriteRegularFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sock := filepath.Join(dir, "isc.sock")

	const content = "这是用户自己的文件，不该被删掉"
	if err := os.WriteFile(sock, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := listenLocal(context.Background(), SchemeUnix, sock)
	if err == nil {
		t.Fatal("同名普通文件存在时应当拒绝监听，而不是覆盖它")
	}
	if !strings.Contains(err.Error(), "拒绝覆盖") {
		t.Errorf("错误信息应当说明为什么拒绝，得到: %v", err)
	}

	// 而且**文件必须原封不动**。
	got, rerr := os.ReadFile(sock)
	if rerr != nil {
		t.Fatalf("用户的文件不见了: %v", rerr)
	}
	if string(got) != content {
		t.Errorf("文件内容被改动了: %q", got)
	}
}

// TestRejectsWrongScheme 验证类 Unix 平台拒绝非 unix 的地址串。
//
// 命名管道在类 Unix 上不存在，静默失败会让排查变得很困难 ——
// 用户会看到"连不上内核"而不知道是地址串写错了。
func TestRejectsWrongScheme(t *testing.T) {
	t.Parallel()

	if _, err := listenLocal(context.Background(), SchemeNamedPipe, "x"); err == nil {
		t.Error("类 Unix 平台不应当接受命名管道地址")
	} else if !strings.Contains(err.Error(), SchemeUnix) {
		t.Errorf("错误信息应当指出只支持 %s，得到: %v", SchemeUnix, err)
	}

	if _, err := dialLocal(context.Background(), SchemeTCP, "127.0.0.1:1"); err == nil {
		t.Error("dialLocal 不应当接受 TCP 地址")
	}
}

// TestLocalEndpointIsUnderRunDir 验证默认套接字落在运行目录下。
func TestLocalEndpointIsUnderRunDir(t *testing.T) {
	t.Parallel()

	runDir := "/var/lib/isc/run"
	ep := localEndpoint(runDir)

	if ep.Scheme() != SchemeUnix {
		t.Errorf("方案是 %q，期望 %q", ep.Scheme(), SchemeUnix)
	}
	// Endpoint 没有导出的 Addr()，因此按它自己的分隔规则取路径部分。
	_, addr, _ := strings.Cut(string(ep), "://")
	if !filepath.IsAbs(addr) {
		t.Errorf("套接字地址必须是绝对路径，得到 %q", addr)
	}
	if !strings.HasPrefix(addr, runDir) {
		t.Errorf("套接字应当落在运行目录 %q 下，得到 %q", runDir, addr)
	}
	if filepath.Base(addr) != socketName {
		t.Errorf("文件名是 %q，期望 %q", filepath.Base(addr), socketName)
	}
}

// TestConcurrentDial 验证多个客户端能同时连上。
//
// 内核同时服务 CLI、控制台与下游 GUI，因此多个连接是常态而不是特例。
func TestConcurrentDial(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sock := filepath.Join(dir, "isc.sock")

	ln, err := listenLocal(context.Background(), SchemeUnix, sock)
	if err != nil {
		t.Fatalf("建立监听失败: %v", err)
	}
	defer func() { _ = ln.Close() }()

	const clients = 8
	go func() {
		for i := 0; i < clients; i++ {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c) // 回显
			}(conn)
		}
	}()

	errs := make(chan error, clients)
	for i := 0; i < clients; i++ {
		go func(i int) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			conn, derr := dialLocal(ctx, SchemeUnix, sock)
			if derr != nil {
				errs <- derr
				return
			}
			defer func() { _ = conn.Close() }()

			msg := []byte("client")
			if _, werr := conn.Write(msg); werr != nil {
				errs <- werr
				return
			}
			buf := make([]byte, len(msg))
			if _, rerr := io.ReadFull(conn, buf); rerr != nil {
				errs <- rerr
				return
			}
			errs <- nil
		}(i)
	}

	for i := 0; i < clients; i++ {
		if err := <-errs; err != nil {
			t.Errorf("第 %d 个客户端失败: %v", i+1, err)
		}
	}
}
