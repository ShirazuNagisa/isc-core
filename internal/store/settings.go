package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"

	"github.com/ShirazuNagisa/isc-core/internal/audit"
)

// 本文件实现运行时设置的持久化与审计日志。

// ---------------------------------------------------------------------------
// 设置
// ---------------------------------------------------------------------------

// LoadSettings 读取全部设置项。
//
// 返回空 map 而不是错误：首次启动时表是空的，这是正常状态。
func (s *Store) LoadSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("store.err.read_settings"), err)
	}
	defer rows.Close() //nolint:errcheck // 只读游标

	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf(i18n.T("store.err.scan_setting"), err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(i18n.T("store.err.iter_settings"), err)
	}
	return out, nil
}

// SaveSettings 写入设置项（存在则覆盖）。
//
// 整体放在一个事务里：一次 PATCH 可能改多项，只写成功一半会让
// 内核处在一个自相矛盾的配置下（例如语言改了但日志级别没改），
// 而用户看到的回执是"成功"。
func (s *Store) SaveSettings(ctx context.Context, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf(i18n.T("store.err.begin_set_tx"), err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // 已提交时是空操作

	const q = `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
	           ON CONFLICT(key) DO UPDATE SET value = excluded.value,
	                                          updated_at = excluded.updated_at`
	ts := now()
	for k, v := range kv {
		if _, err := tx.ExecContext(ctx, q, k, v, ts); err != nil {
			return fmt.Errorf(i18n.T("store.err.write_setting"), k, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf(i18n.T("store.err.commit_settings"), err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 审计
// ---------------------------------------------------------------------------

// AppendAudit 实现 audit.Writer。
//
// 失败只返回错误、不影响调用方的业务操作 —— 是否让业务失败由
// audit.Recorder 决定（它选择只记日志）。理由见 internal/audit 的包文档。
func (s *Store) AppendAudit(ctx context.Context, r audit.Record) error {
	const q = `INSERT INTO audit_log (ts, action, target, result, detail, request_id, remote)
	           VALUES (?, ?, ?, ?, ?, ?, ?)`

	_, err := s.db.ExecContext(ctx, q,
		r.TS, r.Action, nullIfEmpty(r.Target), r.Result,
		nullIfEmpty(r.Detail), nullIfEmpty(r.RequestID), nullIfEmpty(r.Remote))
	if err != nil {
		return fmt.Errorf(i18n.T("store.err.audit_write"), err)
	}
	return nil
}

// ListAudit 实现 audit.Writer，按时间倒序返回。
func (s *Store) ListAudit(ctx context.Context, f audit.Filter) ([]audit.Record, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}

	var (
		where []string
		args  []any
	)
	if f.Action != "" {
		where = append(where, "action = ?")
		args = append(args, f.Action)
	}
	if f.Result != "" {
		where = append(where, "result = ?")
		args = append(args, f.Result)
	}
	if f.Cursor != "" {
		// 游标就是上一页最后一条的 id：id 自增，天然是稳定全序，
		// 不受同一毫秒内多条记录的影响。
		where = append(where, "id < ?")
		args = append(args, f.Cursor)
	}

	q := `SELECT id, ts, action, COALESCE(target,''), result,
	             COALESCE(detail,''), COALESCE(request_id,''), COALESCE(remote,'')
	      FROM audit_log`
	if len(where) > 0 {
		q += " WHERE " + joinAnd(where)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf(i18n.T("store.err.audit_query"), err)
	}
	defer rows.Close() //nolint:errcheck // 只读游标

	out := make([]audit.Record, 0, limit)
	for rows.Next() {
		var r audit.Record
		if err := rows.Scan(&r.ID, &r.TS, &r.Action, &r.Target, &r.Result,
			&r.Detail, &r.RequestID, &r.Remote); err != nil {
			return nil, "", fmt.Errorf(i18n.T("store.err.audit_scan"), err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf(i18n.T("store.err.audit_iter"), err)
	}

	next := ""
	if len(out) == limit && len(out) > 0 {
		next = fmt.Sprintf("%d", out[len(out)-1].ID)
	}
	return out, next, nil
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func nullIfEmpty(s string) any {
	if s == "" {
		return sql.NullString{}
	}
	return s
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}

// ErrClosed 表示在已关闭的数据库上执行操作。
var ErrClosed = errors.New(i18n.T("store.err.closed"))
