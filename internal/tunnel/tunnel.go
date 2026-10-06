// Package tunnel 让站点在**没有公网地址**的网络上也能被访问。
//
// # 它解决的问题
//
// 内核原有的公网暴露有一个前提：这台机器有一个可被路由到的地址。直连
// 模型往 DNS 写 AAAA，客户端直接连回来。这个前提在两处很常见的网络上
// 不成立 —— 大内网（CGNAT）后的家宽，以及校园网 / 公司网。那里的入站
// 连接在网关上就被丢掉了，本机怎么配都没用。
//
// Cloudflare Tunnel 换个方向：**本机主动向 Cloudflare 建一条长连接并
// 保持住**，外面来的请求顺着这条已经存在的连接送进来。不需要公网地址、
// 不需要入站端口、也不用碰网关。
//
// # 它与反向代理的关系：接在前面，不取代它
//
//	客户端 → Cloudflare 边缘 ══隧道══> 127.0.0.1:<反代端口> → 各站点
//
// 因此隧道配置里只有**一条 catch-all 规则**，不需要每个站点一条。
// "哪个域名去哪个站点"仍然由内核的反向代理按 Host 决定 —— 那份路由表
// 在界面上看得见、改得动、有健康检查；隧道配置不是，也不该是。
//
// 这条不是猜的：catch-all 规则**会保留原样的 Host 头**，已在本机实测
// （两个站点经同一条 catch-all 规则各自路由正确）。
//
// 它也是"新站点自动上隧道"之所以便宜的原因：新增一个站点只需要建一条
// DNS 记录，不用改隧道配置、不用重启隧道。
//
// # 一处实质差异：隧道模式下不再需要 ACME
//
// TLS 在 Cloudflare 边缘终结，用的是 Cloudflare 的通用证书。这不是遗漏，
// 是这条路本身的性质 —— 界面要按暴露方式分别说明，别再显示"证书还有
// 几天到期"。
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// State 是隧道的运行状态。
//
// 比"开 / 关"多几档是刻意的：用户真正需要知道的是**卡在哪一步**，而
// "没开""缺 cloudflared""缺授权""授权了但连不上"要采取的动作完全不同。
type State string

const (
	// StateDisabled 用户没开这个功能。
	StateDisabled State = "disabled"
	// StateNoBinary 找不到 cloudflared 可执行文件。
	StateNoBinary State = "no_binary"
	// StateNoAccount 缺账号授权，需要用户授权一次。
	StateNoAccount State = "no_account"
	// StateStarting 进程起来了，但还没和边缘建立连接。
	StateStarting State = "starting"
	// StateRunning 至少有一条到边缘的连接。
	StateRunning State = "running"
	// StateFailed 启动或运行失败。
	StateFailed State = "failed"
)

// DefaultName 是内核建的隧道名。
//
// 固定而非可配：它唯一的用途是让人一眼看出这条隧道是谁建的，以及让
// "找已存在的那条"有稳定判据。可配只会把"我该填什么"变成一个新问题。
const DefaultName = "isc-phecda"

// Status 是隧道当前的样子。
type Status struct {
	Enabled bool  `json:"enabled"`
	State   State `json:"state"`
	Name    string
	ID      string
	// Hostname 是 DNS 上 CNAME 应该指向的目标。
	Hostname string
	// Connections 是最近一次看到的到边缘的连接数。
	Connections int
	// Binary 是实际会用到的 cloudflared 路径；找不到时为空。
	Binary string
	// ProxyPort 是隧道把流量送到的本机反代端口。
	ProxyPort int
	// LastError 是最近一次失败的原因。
	LastError string
	// LogTail 是隧道进程最近几行输出，供排查。
	LogTail []string
}

// Config 是管理器需要的外部依赖。
type Config struct {
	// DataDir 是内核数据目录；隧道的一切都放在它的 cloudflared 子目录里。
	DataDir string
	// ProxyPort 是反向代理端口，隧道把流量送到这里。
	ProxyPort int
	// Binary 是 cloudflared 的路径。为空时按 FindBinary 的顺序找。
	Binary string
	// OriginCert 是账号授权文件。为空时用 <DataDir>/cloudflared/cert.pem。
	OriginCert string
	// Processes 是进程控制器；为空时无法运行隧道。
	Processes platform.ProcessController
	// Log 是日志。
	Log *slog.Logger
}

// Manager 管理一条隧道的完整生命周期。
type Manager struct {
	cfg   Config
	log   *slog.Logger
	procs platform.ProcessController

	mu          sync.Mutex
	proc        platform.Process
	state       State
	id          string
	lastErr     string
	connections int
	tail        []string
	stopping    bool
	restarts    int
}

// New 构造管理器。不做任何 IO —— 真正的动作都在 Start 里。
func New(cfg Config) *Manager {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &Manager{cfg: cfg, log: log, procs: cfg.Processes, state: StateDisabled}
}

// Dir 是隧道的工作目录。
func (m *Manager) Dir() string { return filepath.Join(m.cfg.DataDir, "cloudflared") }

// OriginCertPath 是账号授权文件的位置。
func (m *Manager) OriginCertPath() string {
	if m.cfg.OriginCert != "" {
		return m.cfg.OriginCert
	}
	return filepath.Join(m.Dir(), "cert.pem")
}

// ConfigPath 是生成出来的 cloudflared 配置。
func (m *Manager) ConfigPath() string { return filepath.Join(m.Dir(), "config.yml") }

// CredentialsPath 是某条隧道的凭据文件。
func (m *Manager) CredentialsPath(id string) string {
	return filepath.Join(m.Dir(), id+".json")
}

// HasAccount 报告是否已经完成过账号授权。
//
// 判断"文件在且非空"，**不是**判断它可执行 —— 授权文件是一张 PEM 证书，
// 权限是 0600。这里踩过一次：用可执行性去判断它，会让一个明明已经授权
// 好的机器一直报"缺授权"，而错误信息指的路径上文件就在那里。
func (m *Manager) HasAccount() bool {
	info, err := os.Stat(m.OriginCertPath())
	return err == nil && !info.IsDir() && info.Size() > 0
}

// FindBinary 按固定顺序找一个可用的 cloudflared。
//
// 顺序是刻意的：先看配置（用户明确指定的最优先），再看内核自己的运行时
// 目录（将来由内核供给），最后是两个常见安装位置。找不到不是错误 ——
// 状态如实报出来，界面据此告诉用户"缺 cloudflared"。
func (m *Manager) FindBinary() string {
	if m.cfg.Binary != "" && fileExecutable(m.cfg.Binary) {
		return m.cfg.Binary
	}
	candidates := []string{
		filepath.Join(m.cfg.DataDir, "runtimes", "cloudflared", "cloudflared"),
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "bin", "cloudflared"))
	}
	candidates = append(candidates, "/usr/local/bin/cloudflared", "/opt/homebrew/bin/cloudflared")
	for _, path := range candidates {
		if fileExecutable(path) {
			return path
		}
	}
	if path, err := exec.LookPath("cloudflared"); err == nil {
		return path
	}
	return ""
}

// Status 返回当前状态。
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := Status{
		State:       m.state,
		Name:        DefaultName,
		ID:          m.id,
		Connections: m.connections,
		ProxyPort:   m.cfg.ProxyPort,
		Binary:      m.FindBinary(),
		LastError:   m.lastErr,
		LogTail:     append([]string(nil), m.tail...),
	}
	out.Enabled = m.state != StateDisabled
	out.Hostname = m.hostnameLocked()
	return out
}

// Ready 报告隧道是否已经可以承载流量。
//
// 调用方（域名绑定）据此决定"这个域名是走隧道还是走直连"，因此它必须
// 保守：只有真的连上了边缘才算就绪。进程活着但连不上，绑一个 CNAME
// 过去只会得到一个打不开的域名。
func (m *Manager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state == StateRunning && m.id != ""
}

// Hostname 返回 DNS 上 CNAME 应指向的目标；没有隧道时为空。
func (m *Manager) Hostname() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hostnameLocked()
}

func (m *Manager) hostnameLocked() string {
	if m.id == "" {
		return ""
	}
	return m.id + ".cfargotunnel.com"
}

// TunnelID 返回当前隧道 id（可能为空）。
func (m *Manager) TunnelID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.id
}

// SetTunnelID 在重启后恢复上次用的隧道，免得每次启动都去问一遍 Cloudflare。
func (m *Manager) SetTunnelID(id string) {
	m.mu.Lock()
	m.id = id
	m.mu.Unlock()
}

// writeConfig 生成隧道配置。
//
// 只有一条 catch-all 规则，理由见包注释：站点到域名的映射归反向代理管，
// 这里多写一份就是多一份会漂移的真相。
func (m *Manager) writeConfig(id string) error {
	if err := os.MkdirAll(m.Dir(), 0o700); err != nil {
		return err
	}
	// 文件内容用英文：它是内核生成、可能被用户打开查看的产物，
	// 而内核里面向用户的字符串一律走消息目录（见 i18n 的棘轮规则）。
	// 配置文件的注释不经过消息目录，因此直接写英文，不用中文。
	body := fmt.Sprintf(`# Generated by the ISC kernel. Manual edits are overwritten on the next start.
#
# A single catch-all rule on purpose: hostname-to-site mapping belongs to the
# kernel's reverse proxy, which routes by Host header (verified: a catch-all
# rule preserves the original Host). Adding a site therefore needs only a DNS
# record -- no config change, no tunnel restart.
tunnel: %s
credentials-file: %s
ingress:
  - service: http://127.0.0.1:%d
`, id, m.CredentialsPath(id), m.cfg.ProxyPort)
	return os.WriteFile(m.ConfigPath(), []byte(body), 0o600)
}

// Start 确保隧道在跑。
//
// 任何一步缺东西都如实落到状态里，而不是只返回一个用户看不懂的错误：
// 界面要能说"缺 cloudflared"或者"需要授权一次"。
func (m *Manager) Start(ctx context.Context) error {
	if m.procs == nil {
		return m.fail(StateFailed, errors.New("tunnel: process control is unavailable on this platform"))
	}
	binary := m.FindBinary()
	if binary == "" {
		return m.fail(StateNoBinary, errors.New("cloudflared was not found"))
	}
	if !m.HasAccount() {
		return m.fail(StateNoAccount, fmt.Errorf("no Cloudflare account certificate at %s", m.OriginCertPath()))
	}

	id, err := m.ensureTunnel(ctx, binary)
	if err != nil {
		return m.fail(StateFailed, err)
	}
	if err := m.writeConfig(id); err != nil {
		return m.fail(StateFailed, err)
	}

	m.mu.Lock()
	m.id = id
	m.state = StateStarting
	m.lastErr = ""
	m.stopping = false
	m.connections = 0
	m.mu.Unlock()

	if err := m.spawn(binary, id); err != nil {
		return m.fail(StateFailed, err)
	}
	m.log.Info("tunnel started", "id", id, "proxy_port", m.cfg.ProxyPort)
	return nil
}

// ensureTunnel 找到已有的同名隧道，没有就建一条。
//
// 复用优先：隧道是账号级资源，建多了会在 Cloudflare 后台堆成一片；而且
// DNS 记录指向的是**具体某一条**隧道，换一条就得把所有记录改一遍。
func (m *Manager) ensureTunnel(ctx context.Context, binary string) (string, error) {
	if id := m.TunnelID(); id != "" && fileExists(m.CredentialsPath(id)) {
		return id, nil
	}
	if err := os.MkdirAll(m.Dir(), 0o700); err != nil {
		return "", err
	}
	if text, err := m.runCLI(ctx, binary, "tunnel", "list", "--output", "json"); err == nil {
		if id := tunnelIDByName(text, DefaultName); id != "" && fileExists(m.CredentialsPath(id)) {
			return id, nil
		}
	}
	text, err := m.runCLI(ctx, binary, "tunnel", "create", DefaultName)
	if err != nil {
		return "", fmt.Errorf("could not create a tunnel: %w", err)
	}
	id := tunnelIDFromText(text)
	if id == "" {
		// 输出格式变了也不能猜：直接说清楚。
		return "", errors.New("cloudflared created a tunnel but its id was not in the output")
	}
	return id, nil
}

// runCLI 跑一条 cloudflared 管理命令。
//
// 不经 shell（D30），argv 逐项传递；授权文件通过环境变量指到内核自己的
// 目录，因此**不碰**用户的 ~/.cloudflared。
func (m *Manager) runCLI(ctx context.Context, binary string, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binary, args...)
	cmd.Dir = m.Dir()
	cmd.Env = append(platform.MinimalEnv(), "TUNNEL_ORIGIN_CERT="+m.OriginCertPath())
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		return text, fmt.Errorf("%s: %w", strings.TrimSpace(text), err)
	}
	return text, nil
}

const cliTimeout = 90 * time.Second

// spawn 起隧道进程。
func (m *Manager) spawn(binary, id string) error {
	proc, err := m.procs.Start(platform.ProcessSpec{
		Executable: binary,
		Args:       []string{"tunnel", "--config", m.ConfigPath(), "run", id},
		Dir:        m.Dir(),
		Env:        platform.MinimalEnv(),
		OnOutput:   m.onOutput,
	})
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.proc = proc
	m.mu.Unlock()
	go m.waitProc(proc)
	return nil
}

// connectionLine 匹配 cloudflared 注册连接的那一行。
//
// 靠日志判断"连上了"而不是只看进程活着：进程起来但连不上边缘是很常见的
// 中间状态（DNS 不通、网关拦了 7844），那时候报"运行中"会让用户以为
// 站点已经可以从公网访问了。
var connectionLine = regexp.MustCompile(`Registered tunnel connection`)

func (m *Manager) onOutput(chunk string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(chunk, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// 只留尾部若干行：隧道日志很啰嗦，全留会把状态响应撑大。
		m.tail = append(m.tail, line)
		if len(m.tail) > maxLogTail {
			m.tail = m.tail[len(m.tail)-maxLogTail:]
		}
		if connectionLine.MatchString(line) {
			m.connections++
			if m.state == StateStarting || m.state == StateFailed {
				m.state = StateRunning
			}
		}
	}
}

const maxLogTail = 8

// waitProc 等进程退出，并在非主动停止时重启。
//
// 隧道是一条**长连接**：网络切换、边缘侧维护、校园网的会话老化都会把它
// 断开。没有这一层重启，"站点今天能开、明天不能开"就没有任何线索。
func (m *Manager) waitProc(proc platform.Process) {
	status, _ := proc.Wait()

	m.mu.Lock()
	stopping := m.stopping
	m.proc = nil
	if !stopping {
		m.state = StateFailed
		m.connections = 0
		m.lastErr = fmt.Sprintf("tunnel exited (code %d)", status.Code)
	}
	restarts := m.restarts
	if !stopping {
		m.restarts++
	}
	m.mu.Unlock()

	if stopping || restarts >= maxRestarts {
		return
	}
	// 退避：立刻重连会在边缘侧被限流，也会把日志刷满。
	delay := time.Duration(restarts+1) * restartBackoff
	m.log.Warn("tunnel exited, restarting", "code", status.Code, "in", delay)
	time.Sleep(delay)

	m.mu.Lock()
	stopping = m.stopping
	m.mu.Unlock()
	if stopping {
		return
	}
	if err := m.Start(context.Background()); err != nil {
		m.log.Error("tunnel restart failed", "err", err)
	}
}

const (
	// maxRestarts 是连续重启的上限。没有上限的话，一个配置错误（比如凭据
	// 被删）会让内核每隔几秒 fork 一次进程，日志很快淹没真正的原因。
	maxRestarts = 5
	// restartBackoff 是退避的基数。
	restartBackoff = 5 * time.Second
	// stopTimeout 是停止隧道进程的等待上限。
	stopTimeout = 5 * time.Second
)

// Stop 停掉隧道进程。
func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopping = true
	proc := m.proc
	m.proc = nil
	m.state = StateDisabled
	m.connections = 0
	m.restarts = 0
	m.mu.Unlock()
	if proc == nil {
		return
	}
	// 先请它自己退（cloudflared 收到 TERM 会断开与边缘的连接），
	// 超时再强杀 —— 直接 KILL 会让边缘侧把这次断开记成异常。
	_ = proc.Signal(platform.SignalTerminate)
	select {
	case <-proc.Done():
	case <-time.After(stopTimeout):
		_ = proc.Signal(platform.SignalKill)
		<-proc.Done()
	}
}

func (m *Manager) fail(state State, err error) error {
	m.mu.Lock()
	m.state = state
	m.lastErr = err.Error()
	m.mu.Unlock()
	return err
}

// --- 小工具 -----------------------------------------------------------------

func fileExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// tunnelIDPattern 匹配隧道 UUID。
const tunnelIDPattern = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`

var idOnly = regexp.MustCompile(tunnelIDPattern)

func tunnelIDFromText(text string) string { return idOnly.FindString(text) }

// tunnelIDByName 从 `tunnel list --output json` 里找出某个名字的隧道 id。
//
// 不引入 JSON 解析：输入是我们自己命令的输出、形状固定，而多一个结构体
// 就多一处会随 cloudflared 版本漂移的东西。做法是按 `{` 切开每一条记录，
// 在**同一条记录内**同时看到名字和 id 才算数 —— 跨记录匹配会把 A 的名字
// 配上 B 的 id，那比找不到更糟。
func tunnelIDByName(jsonText, name string) string {
	quoted := `"` + name + `"`
	for _, record := range strings.Split(jsonText, "{") {
		if !strings.Contains(record, quoted) {
			continue
		}
		if id := idOnly.FindString(record); id != "" {
			return id
		}
	}
	return ""
}
