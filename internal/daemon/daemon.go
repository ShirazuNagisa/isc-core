// Package daemon 组装并运行 ISC 内核守护进程。
//
// 启动顺序见 docs/ARCHITECTURE.md §8.1。M0 阶段只走到"建立本地通道并写出
// runtime.json"，数据库、密钥库、调度器等随里程碑接入。
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api"
	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/job"
	"github.com/ShirazuNagisa/isc-core/internal/logx"
	"github.com/ShirazuNagisa/isc-core/internal/paths"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
	"github.com/ShirazuNagisa/isc-core/internal/runtimeinfo"
	"github.com/ShirazuNagisa/isc-core/internal/version"
)

// tokenBytes 是访问令牌的随机字节数。
//
// 32 字节 = 256 位，远超暴力枚举的可能；base64url 编码后 43 个字符。
const tokenBytes = 32

// defaultLoopbackAddr 是回环监听地址。
//
// 端口 0 让内核自动挑选空闲端口 —— 固定端口会与用户机器上其它程序冲突，
// 而客户端本来就是通过 runtime.json 发现端口的，不需要固定值。
//
// 显式绑定 127.0.0.1 而不是 localhost 或 :0：后者可能同时绑定到
// 0.0.0.0 或 ::，把管理面暴露到局域网。
const defaultLoopbackAddr = "127.0.0.1:0"

// shutdownTimeout 是优雅关闭的总时限。
const shutdownTimeout = 10 * time.Second

// Options 是守护进程的启动参数。
type Options struct {
	// Paths 是数据目录布局。
	Paths paths.Paths

	// LogHandler 是日志处理器。
	//
	// 由调用方（CLI）提前创建，这样进程最早期的日志也能被捕获；
	// 守护进程会在事件总线就绪后把总线接上去，使日志同时进入事件流。
	LogHandler *logx.BusHandler

	// Lang 是用户界面语言。
	Lang i18n.Lang

	// LoopbackAddr 是回环监听地址；为空时用 defaultLoopbackAddr。
	LoopbackAddr string

	// DisableLoopback 关闭回环监听，只保留命名管道 / Unix 套接字。
	//
	// 默认必须开启：浏览器（验证控制台）无法连接命名管道，
	// 关了它等于关掉了控制台。
	DisableLoopback bool

	// AllowedOrigins 是 WebSocket 允许的 Origin 模式，供 Vite 开发调试使用。
	AllowedOrigins []string

	// EventBufferSize 是事件环形缓冲容量；0 表示使用默认值。
	EventBufferSize int
}

// Daemon 是内核守护进程。
type Daemon struct {
	opts   Options
	log    *slog.Logger
	bundle *platform.Bundle
	bus    *event.Bus
	jobs   *job.Engine
	api    *api.Server
	token  string

	servers   []*http.Server
	listeners []net.Listener
	// listenerInfos 记录实际建立的通道，供日志与 runtime.json 使用。
	localEndpoint string
	tcpEndpoint   string

	// ready 在守护进程完全就绪后关闭，供测试等待。
	ready     chan struct{}
	readyOnce sync.Once
}

// New 构造守护进程。此时不会产生任何副作用。
func New(opts Options) *Daemon {
	if opts.LogHandler == nil {
		opts.LogHandler = logx.New(os.Stderr, slog.LevelInfo, nil)
	}
	if opts.LoopbackAddr == "" {
		opts.LoopbackAddr = defaultLoopbackAddr
	}
	if opts.Lang == "" {
		opts.Lang = i18n.Default
	}
	return &Daemon{
		opts:  opts,
		log:   slog.New(opts.LogHandler),
		ready: make(chan struct{}),
	}
}

// Ready 返回一个在守护进程完全就绪后关闭的通道，供测试同步。
func (d *Daemon) Ready() <-chan struct{} { return d.ready }

// RuntimeFile 返回运行时描述文件路径。
func (d *Daemon) RuntimeFile() string { return d.opts.Paths.RuntimeFile() }

// Run 启动守护进程并阻塞直到 ctx 被取消或发生致命错误。
//
// 返回 nil 表示正常关闭。
func (d *Daemon) Run(ctx context.Context) error {
	// 1. 数据目录
	warnings, err := d.opts.Paths.EnsureDirs()
	if err != nil {
		return err
	}
	for _, w := range warnings {
		d.log.Warn(w)
	}

	// 2. 语言与平台
	i18n.SetDefault(d.opts.Lang)
	d.bundle = platform.Current()
	d.log.Info(i18n.T("daemon.starting"),
		"version", version.Version,
		"os", d.bundle.OS, "arch", d.bundle.Arch,
		"paths", d.opts.Paths.String())

	// 3. 清理上一次的残留运行时文件。
	if err := d.cleanupStaleRuntime(); err != nil {
		return err
	}

	// 4. 事件总线与任务引擎
	d.bus = event.NewBus(d.opts.EventBufferSize)
	d.opts.LogHandler.SetPublisher(d.bus)

	// 任务引擎的父上下文是本次运行的生命周期，而不是调用方的 ctx：
	// 调用方的 ctx 可能在 HTTP 请求结束时就被取消。
	runCtx, cancelRun := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRun()

	d.jobs = job.NewEngine(runCtx, d.bus, job.NewMemoryStore(), d.log)

	// 5. 访问令牌
	if d.token, err = newToken(); err != nil {
		return err
	}

	// 6. API 服务
	d.api = api.New(api.Deps{
		Bus:            d.bus,
		Jobs:           d.jobs,
		Platform:       d.bundle,
		Log:            d.log,
		StartedAt:      time.Now().UTC(),
		AllowedOrigins: d.opts.AllowedOrigins,
		Token:          d.token,
	})

	// 7. 建立传输通道
	if err := d.listen(ctx); err != nil {
		return err
	}

	// 8. 写出 runtime.json —— 此刻起客户端才能发现内核。
	if err := d.writeRuntimeInfo(); err != nil {
		d.closeListeners()
		return err
	}

	// 9. 起服务
	d.serve(cancelRun)

	d.readyOnce.Do(func() { close(d.ready) })
	d.log.Info(i18n.T("daemon.started"))

	// 10. 等待退出信号
	<-ctx.Done()
	d.log.Info(i18n.T("daemon.stopping"))

	return d.shutdown()
}

// cleanupStaleRuntime 清理上一次非正常退出留下的 runtime.json。
//
// 不做这件事的话，客户端会读到一个指向已死进程的地址，
// 得到"连接被拒绝"这种难以归因的报错，而不是清晰的"内核未运行"。
func (d *Daemon) cleanupStaleRuntime() error {
	path := d.opts.Paths.RuntimeFile()
	info, err := runtimeinfo.Read(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		// 文件损坏：直接删掉比让所有客户端持续解析失败要好。
		d.log.Warn("运行时文件无法解析，已删除", "path", path, "err", err)
		return runtimeinfo.Remove(path)
	}
	if info.IsStale() {
		d.log.Warn(i18n.T("daemon.stale_runtime_file", info.PID), "path", path)
		return runtimeinfo.Remove(path)
	}
	return fmt.Errorf("%s", i18n.T("daemon.already_running", info.PID))
}

// listen 建立本地管理通道。
//
// 策略：
//
//   - 命名管道 / Unix 套接字是首选（ACL 层防护，不占 TCP 端口）；
//   - 回环 TCP 始终一并开启，因为浏览器无法连接前两者。
//
// 前者失败不是致命错误：降级为"仅回环"仍可用，只是少了一层防护，
// 因此记 Warn 而不是直接退出。
func (d *Daemon) listen(_ context.Context) error {
	localEP := platform.LocalEndpoint(d.opts.Paths.RunDir())
	ln, err := localEP.Listen(context.Background())
	if err != nil {
		d.log.Warn("本地管理通道建立失败，降级为仅回环",
			"endpoint", localEP.String(), "err", err)
	} else {
		d.localEndpoint = localEP.String()
		d.addServer(ln)
		d.log.Info(i18n.T("transport.desc", localEP.String(), localEP.TransportName()))
	}

	if !d.opts.DisableLoopback {
		tcpEP := platform.Endpoint(platform.SchemeTCP + "://" + d.opts.LoopbackAddr)
		tcpLn, err := tcpEP.Listen(context.Background())
		if err != nil {
			d.closeListeners()
			return fmt.Errorf("%s: %w", i18n.T("daemon.transport_failed", tcpEP.String()), err)
		}
		// 端口 0 表示自动分配，必须回读真实端口才能告诉客户端。
		d.tcpEndpoint = platform.SchemeTCP + "://" + tcpLn.Addr().String()
		d.addServer(tcpLn)
		d.log.Info(i18n.T("transport.desc", d.tcpEndpoint,
			platform.Endpoint(d.tcpEndpoint).TransportName()))
	}

	if len(d.servers) == 0 {
		return fmt.Errorf("%s", i18n.T("daemon.transport_failed", "无可用通道"))
	}
	return nil
}

// addServer 为一个监听器创建 http.Server。
func (d *Daemon) addServer(ln net.Listener) {
	srv := &http.Server{
		Handler: d.api.Routes(),
		// ReadHeaderTimeout 必须设置：否则一个只连不发的客户端就能
		// 占住连接不放（Slowloris）。服务端一旦被拖住，
		// 用户界面上的所有操作都会一起卡死。
		ReadHeaderTimeout: 10 * time.Second,
		// 刻意不设 WriteTimeout / IdleTimeout：事件流是长连接，
		// 写超时会在客户端长时间无事件时把连接掐断。
		ErrorLog: slog.NewLogLogger(d.log.Handler(), slog.LevelWarn),
	}
	d.servers = append(d.servers, srv)
	d.listeners = append(d.listeners, ln)
}

// serve 为每个监听器启动一个服务 goroutine。
func (d *Daemon) serve(cancelRun context.CancelFunc) {
	for i := range d.servers {
		srv, ln := d.servers[i], d.listeners[i]
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				d.log.Error("HTTP 服务异常退出", "err", err, "addr", ln.Addr().String())
				// 服务异常是致命的：继续运行只会让客户端连不上却以为内核还活着。
				cancelRun()
			}
		}()
	}
}

// writeRuntimeInfo 写出 runtime.json。
func (d *Daemon) writeRuntimeInfo() error {
	info := runtimeinfo.Info{
		PID:              os.Getpid(),
		Version:          version.Version,
		Endpoint:         d.localEndpoint,
		FallbackEndpoint: d.tcpEndpoint,
		Token:            d.token,
		StartedAt:        time.Now().UTC(),
	}
	if err := runtimeinfo.Write(d.opts.Paths.RuntimeFile(), info); err != nil {
		d.log.Error(i18n.T("daemon.runtime_write_failed", err.Error()))
		return err
	}
	return nil
}

// shutdown 优雅关闭。
//
// 顺序有讲究：
//
//  1. 先删 runtime.json —— 让新客户端立刻停止发现内核，
//     避免它们在关闭过程中连进来又被拒；
//  2. 关闭事件总线 —— 断开全部 WebSocket 订阅，让事件流的 handler 返回；
//  3. 取消并等待在途任务 —— 任务可能正在写 DNS 记录，中途打断会留下
//     半完成的状态；
//  4. 关闭 HTTP 服务并等待在途请求。
func (d *Daemon) shutdown() error {
	var firstErr error

	if err := runtimeinfo.Remove(d.opts.Paths.RuntimeFile()); err != nil {
		d.log.Warn("删除运行时文件失败", "err", err)
		firstErr = err
	}

	if d.bus != nil {
		d.bus.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if d.jobs != nil {
		if err := d.jobs.Shutdown(ctx); err != nil {
			d.log.Warn("等待任务收尾超时", "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	for _, srv := range d.servers {
		if err := srv.Shutdown(ctx); err != nil {
			d.log.Warn("关闭 HTTP 服务超时", "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	d.closeListeners()

	d.log.Info(i18n.T("daemon.stopped"))
	return firstErr
}

func (d *Daemon) closeListeners() {
	for _, ln := range d.listeners {
		_ = ln.Close()
	}
}

// newToken 生成访问令牌。
func newToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("daemon: 生成访问令牌失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
