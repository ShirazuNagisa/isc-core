package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/proxy"
)

// ProxyRoutes 是代理路由的 SQLite 仓储。
//
// 它直接读写 proxy.Route —— 路由的字段全部是可序列化的简单值，
// 再引入一层实体只会多一次转换而没有收益。校验由 proxy 包负责
// （ValidateUpstream / Route.Validate），仓储层不做业务判断。
type ProxyRoutes struct{ s *Store }

// ProxyRoutes 返回代理路由仓储。
func (s *Store) ProxyRoutes() *ProxyRoutes { return &ProxyRoutes{s: s} }

const proxyRouteColumns = `id, label, domains, upstream, tls, enabled, created_at, updated_at`

// List 返回全部代理路由。
func (p *ProxyRoutes) List(ctx context.Context) ([]proxy.Route, error) {
	q := `SELECT ` + proxyRouteColumns + ` FROM proxy_routes ORDER BY rowid ASC`

	rows, err := p.s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: 查询代理路由失败: %w", err)
	}
	defer rows.Close() //nolint:errcheck // 只读游标

	var out []proxy.Route
	for rows.Next() {
		var (
			r         proxy.Route
			domains   string
			tlsOn     int64
			enabled   int64
			createdAt string
			updatedAt string
		)
		if err := rows.Scan(&r.ID, &r.Label, &domains, &r.Upstream,
			&tlsOn, &enabled, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("store: 扫描代理路由失败: %w", err)
		}
		r.Hosts = splitLines(domains)
		r.TLS = tlsOn != 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历代理路由失败: %w", err)
	}
	return out, nil
}

// Replace 用给定的集合整体替换全部代理路由。
//
// # 为什么是整体替换而不是增删改
//
// 路由表是用户在界面上一份一份编出来的，而它**必须始终自洽**：
// 同一个域名不能同时指向两个上游。增量修改会让"检查冲突"变成一件
// 需要跨多次调用才能完成的事，而中间任何一个时刻的状态都可能是
// 有冲突的 —— 那期间进来的请求会打到哪一条是不确定的。
//
// 整体替换让"检查 + 生效"变成一次原子操作。
func (p *ProxyRoutes) Replace(ctx context.Context, routes []proxy.Route) error {
	tx, err := p.s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: 开始事务失败: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // 提交成功后回滚是空操作

	if _, err := tx.ExecContext(ctx, `DELETE FROM proxy_routes`); err != nil {
		return fmt.Errorf("store: 清空代理路由失败: %w", err)
	}

	const q = `INSERT INTO proxy_routes (` + proxyRouteColumns + `)
	           VALUES (?,?,?,?,?,?,?,?)`

	now := formatTime(time.Now().UTC())
	for _, r := range routes {
		_, err := tx.ExecContext(ctx, q,
			r.ID, r.Label, strings.Join(r.Hosts, "\n"), r.Upstream,
			boolToIntValue(r.TLS), 1, now, now)
		if err != nil {
			return fmt.Errorf("store: 写入代理路由失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交代理路由失败: %w", err)
	}
	return nil
}

// splitLines 把换行分隔的文本拆成列表。
func splitLines(raw string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
