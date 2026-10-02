package store

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"

	"github.com/ShirazuNagisa/isc-core/internal/ddns"
)

// Tasks 是 ddns.Repository 的 SQLite 实现。
//
// 与 Credentials / Jobs 一样做成独立类型：*Store 上已经有 Get/List 等同名
// 方法，Go 不允许在同一类型上重复定义。
type Tasks struct{ s *Store }

// 编译期断言：接口实现必须完整。
var _ ddns.Repository = (*Tasks)(nil)

// Tasks 返回动态解析任务仓储。
func (s *Store) Tasks() *Tasks { return &Tasks{s: s} }

const taskColumns = `
    id, credential_id, label, enabled,
    ipv4_enable, ipv4_get_type, ipv4_source, ipv4_domains,
    ipv6_enable, ipv6_get_type, ipv6_source, ipv6_domains, ipv6_selector,
    ttl, http_interface,
    last_run_at, last_status, last_message, last_ipv4, last_ipv6,
    created_at, updated_at`

// List 实现 ddns.Repository。
func (t *Tasks) List(ctx context.Context, enabledOnly bool) ([]ddns.Task, error) {
	q := `SELECT` + taskColumns + ` FROM ddns_tasks`
	if enabledOnly {
		q += ` WHERE enabled = 1`
	}
	// 按创建时间正序：任务的展示顺序应当稳定，否则每次刷新界面
	// 列表都会跳来跳去。用 rowid 而不是 created_at 保证同一毫秒内
	// 创建的任务也有确定顺序。
	q += ` ORDER BY rowid ASC`

	rows, err := t.s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("store.err.list_tasks"), err)
	}
	defer rows.Close() //nolint:errcheck // 只读游标

	var out []ddns.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(i18n.T("store.err.iter_tasks"), err)
	}
	return out, nil
}

// Get 实现 ddns.Repository。
func (t *Tasks) Get(ctx context.Context, id string) (ddns.Task, bool, error) {
	q := `SELECT` + taskColumns + ` FROM ddns_tasks WHERE id = ?`
	row := t.s.db.QueryRowContext(ctx, q, id)
	task, err := scanTask(row)
	if err != nil {
		if isNoRows(err) {
			return ddns.Task{}, false, nil
		}
		return ddns.Task{}, false, fmt.Errorf(i18n.T("store.err.get_task"), err)
	}
	return task, true, nil
}

// Insert 实现 ddns.Repository。
func (t *Tasks) Insert(ctx context.Context, task ddns.Task) error {
	const q = `INSERT INTO ddns_tasks (
	    id, credential_id, label, enabled,
	    ipv4_enable, ipv4_get_type, ipv4_source, ipv4_domains,
	    ipv6_enable, ipv6_get_type, ipv6_source, ipv6_domains, ipv6_selector,
	    ttl, http_interface,
	    last_run_at, last_status, last_message, last_ipv4, last_ipv6,
	    created_at, updated_at
	) VALUES (?,?,?,?, ?,?,?,?, ?,?,?,?,?, ?,?, ?,?,?,?,?, ?,?)`

	_, err := t.s.db.ExecContext(ctx, q, taskArgs(task)...)
	if err != nil {
		return fmt.Errorf(i18n.T("store.err.insert_task"), err)
	}
	return nil
}

// Update 实现 ddns.Repository。
func (t *Tasks) Update(ctx context.Context, task ddns.Task) error {
	const q = `UPDATE ddns_tasks SET
	    credential_id = ?, label = ?, enabled = ?,
	    ipv4_enable = ?, ipv4_get_type = ?, ipv4_source = ?, ipv4_domains = ?,
	    ipv6_enable = ?, ipv6_get_type = ?, ipv6_source = ?, ipv6_domains = ?,
	    ipv6_selector = ?, ttl = ?, http_interface = ?,
	    last_run_at = ?, last_status = ?, last_message = ?,
	    last_ipv4 = ?, last_ipv6 = ?, updated_at = ?
	WHERE id = ?`

	// 参数顺序必须与上面的 SET 子句逐项对应。
	// 刻意不写成 taskArgs(task)[n:] 之类的切片技巧：那种写法在
	// SET 子句调整时不会有任何编译错误，只会让某个字段被静默写错。
	args := []any{
		task.CredentialID, task.Label, boolToIntValue(task.Enabled),
		boolToIntValue(task.IPv4.Enable), string(task.IPv4.GetType),
		task.IPv4.Value, ddns.JoinDomains(task.IPv4.Domains),
		boolToIntValue(task.IPv6.Enable), string(task.IPv6.GetType),
		task.IPv6.Value, ddns.JoinDomains(task.IPv6.Domains), task.IPv6.Selector,
		task.TTL, task.HTTPInterface,
		formatTimePtr(task.LastRunAt), string(task.LastStatus), task.LastMessage,
		task.LastIPv4, task.LastIPv6,
		formatTime(task.UpdatedAt),
		task.ID,
	}

	res, err := t.s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf(i18n.T("store.err.update_task"), err)
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ddns.ErrNotFound
	}
	return nil
}

// Delete 实现 ddns.Repository。
func (t *Tasks) Delete(ctx context.Context, id string) error {
	res, err := t.s.db.ExecContext(ctx, `DELETE FROM ddns_tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf(i18n.T("store.err.delete_task"), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf(i18n.T("store.err.delete_result"), err)
	}
	if n == 0 {
		return ddns.ErrNotFound
	}
	return nil
}

// CountByCredential 实现 ddns.Repository。
//
// 用于删除凭据前的检查：给出"仍被 N 个任务使用"这样的明确提示，
// 而不是让数据库抛一个外键约束错误 —— 后者的信息量对用户为零。
func (t *Tasks) CountByCredential(ctx context.Context, credentialID string) (int, error) {
	var n int
	err := t.s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ddns_tasks WHERE credential_id = ?`, credentialID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf(i18n.T("store.err.count_cred_refs"), err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func taskArgs(task ddns.Task) []any {
	return []any{
		task.ID, task.CredentialID, task.Label, boolToIntValue(task.Enabled),
		boolToIntValue(task.IPv4.Enable), string(task.IPv4.GetType),
		task.IPv4.Value, ddns.JoinDomains(task.IPv4.Domains),
		boolToIntValue(task.IPv6.Enable), string(task.IPv6.GetType),
		task.IPv6.Value, ddns.JoinDomains(task.IPv6.Domains), task.IPv6.Selector,
		task.TTL, task.HTTPInterface,
		formatTimePtr(task.LastRunAt), string(task.LastStatus), task.LastMessage,
		task.LastIPv4, task.LastIPv6,
		formatTime(task.CreatedAt), formatTime(task.UpdatedAt),
	}
}

func scanTask(sc rowScanner) (ddns.Task, error) {
	var (
		task       ddns.Task
		enabled    int64
		v4Enable   int64
		v6Enable   int64
		v4Domains  string
		v6Domains  string
		lastRunAt  sql.NullString
		lastStatus string
		createdAt  string
		updatedAt  string
	)
	err := sc.Scan(
		&task.ID, &task.CredentialID, &task.Label, &enabled,
		&v4Enable, &task.IPv4.GetType, &task.IPv4.Value, &v4Domains,
		&v6Enable, &task.IPv6.GetType, &task.IPv6.Value, &v6Domains,
		&task.IPv6.Selector, &task.TTL, &task.HTTPInterface,
		&lastRunAt, &lastStatus, &task.LastMessage, &task.LastIPv4, &task.LastIPv6,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return ddns.Task{}, err
	}

	task.Enabled = enabled != 0
	task.IPv4.Enable = v4Enable != 0
	task.IPv6.Enable = v6Enable != 0
	task.IPv4.Domains = ddns.SplitDomains(v4Domains)
	task.IPv6.Domains = ddns.SplitDomains(v6Domains)
	task.LastRunAt = parseTimePtr(lastRunAt)
	task.LastStatus = ddns.Status(lastStatus)
	task.CreatedAt = parseTime(createdAt)
	task.UpdatedAt = parseTime(updatedAt)
	return task, nil
}

func boolToIntValue(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
