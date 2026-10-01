package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
	"github.com/ShirazuNagisa/isc-core/internal/runtimeinfo"
)

// 客户端错误。
var (
	// ErrNotRunning 表示没有找到运行中的内核。
	ErrNotRunning = errors.New("cli: 内核未运行")
	// ErrUnreachable 表示找到了运行时文件但所有通道都连不上。
	ErrUnreachable = errors.New("cli: 内核不可达")
)

// clientTimeout 是普通请求的超时。
//
// 事件流与任务轮询不走这个客户端，因此给一个较短的超时是安全的：
// 真正的慢操作都返回 202，不会让请求挂住。
const clientTimeout = 15 * time.Second

// Client 是通过本地 API 访问内核的客户端。
//
// CLI、验证控制台与下游 GUI 共享同一套发现与鉴权语义
// （见 docs/ARCHITECTURE.md §8.3）。
type Client struct {
	// endpoint 是实际连上的通道。
	endpoint platform.Endpoint

	// baseURL 是该通道对应的 HTTP base URL。
	baseURL string

	// token 是访问令牌。
	token string

	http *http.Client
}

// Connect 发现并连接内核。
//
// 顺序：先试 runtime.json 里的首选通道（命名管道 / Unix 套接字），
// 失败再试回环。首选通道不仅有 ACL 层防护，也不会与其他程序争抢端口。
func Connect(ctx context.Context, runtimeFile string) (*Client, error) {
	info, err := runtimeinfo.Read(runtimeFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotRunning
		}
		return nil, fmt.Errorf("cli: 读取运行时文件失败: %w", err)
	}
	if info.IsStale() {
		// 文件在但进程没了：内核是崩溃退出的。
		// 对用户而言这与"没在运行"是同一件事，但排查方向不同，
		// 因此这里返回 ErrNotRunning 并让调用方提示"上次可能异常退出"。
		return nil, ErrNotRunning
	}

	var lastErr error
	for _, raw := range candidates(info) {
		c, err := newClient(raw, info.Token)
		if err != nil {
			lastErr = err
			continue
		}
		if err := c.ping(ctx); err != nil {
			lastErr = err
			continue
		}
		return c, nil
	}
	if lastErr == nil {
		lastErr = errors.New("运行时文件中没有可用的通道")
	}
	return nil, fmt.Errorf("%w: %v", ErrUnreachable, lastErr)
}

// candidates 返回按优先级排列的候选通道。
func candidates(info runtimeinfo.Info) []string {
	out := make([]string, 0, 2)
	if info.Endpoint != "" {
		out = append(out, info.Endpoint)
	}
	if info.FallbackEndpoint != "" && info.FallbackEndpoint != info.Endpoint {
		out = append(out, info.FallbackEndpoint)
	}
	return out
}

func newClient(raw, token string) (*Client, error) {
	ep := platform.Endpoint(raw)
	hc, err := ep.HTTPClient(clientTimeout)
	if err != nil {
		return nil, err
	}
	return &Client{
		endpoint: ep,
		baseURL:  ep.HTTPBaseURL(),
		token:    token,
		http:     hc,
	}, nil
}

// Endpoint 返回已连接的通道地址。
func (c *Client) Endpoint() platform.Endpoint { return c.endpoint }

// BaseURL 返回 HTTP base URL。
func (c *Client) BaseURL() string { return c.baseURL }

// ping 用 /v1/health 验证通道确实可用。
//
// 只验证"能连通"而不校验版本：版本不匹配时应当给出可操作的提示，
// 而不是让 status 命令直接失败。
func (c *Client) ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var h gen.Health
	return c.do(ctx, http.MethodGet, "/v1/health", &h)
}

// Health 查询健康状态。
func (c *Client) Health(ctx context.Context) (gen.Health, error) {
	var h gen.Health
	err := c.do(ctx, http.MethodGet, "/v1/health", &h)
	return h, err
}

// getInto 发起一次带鉴权的 GET 并解码响应。
func (c *Client) getInto(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, out)
}

// post 发起一次带鉴权的 POST。
func (c *Client) post(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodPost, path, out)
}

// Meta 查询元信息与平台能力。
func (c *Client) Meta(ctx context.Context) (gen.Meta, error) {
	var m gen.Meta
	err := c.do(ctx, http.MethodGet, "/v1/meta", &m)
	return m, err
}

// Job 查询单个任务。
func (c *Client) Job(ctx context.Context, id string) (gen.Job, error) {
	var j gen.Job
	err := c.do(ctx, http.MethodGet, "/v1/jobs/"+id, &j)
	return j, err
}

// do 发起一次请求并解析响应。
//
// 错误响应会被解析成 problem+json 并原样返回其 title/detail ——
// 内核已经把错误本地化过了，客户端不需要（也不应该）再翻译一次。
func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // 只读响应，关闭失败无影响

	if resp.StatusCode >= 400 {
		var p gen.Problem
		if err := json.NewDecoder(resp.Body).Decode(&p); err == nil && p.Title != "" {
			return newAPIError(resp.StatusCode, p)
		}
		return fmt.Errorf("cli: 内核返回 HTTP %d", resp.StatusCode)
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// apiError 把内核返回的 problem 包装成 error，保留错误码供调用方分支判断。
type apiError struct {
	status int
	code   string
	title  string
	detail string
}

func newAPIError(status int, p gen.Problem) *apiError {
	e := &apiError{status: status, title: p.Title}
	if p.Code != nil {
		e.code = *p.Code
	}
	if p.Detail != nil {
		e.detail = *p.Detail
	}
	return e
}

// Error 实现 error。
func (e *apiError) Error() string {
	if e.detail != "" {
		return fmt.Sprintf("%s（HTTP %d）：%s", e.title, e.status, e.detail)
	}
	return fmt.Sprintf("%s（HTTP %d）", e.title, e.status)
}

// Code 返回机器可读的错误码。
func (e *apiError) Code() string { return e.code }
