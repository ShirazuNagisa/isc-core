package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/ShirazuNagisa/isc-core/internal/change"
)

// Changes 是 change.Journal 的 SQLite 实现。
type Changes struct{ s *Store }

// 编译期断言：接口实现必须完整。
var _ change.Journal = (*Changes)(nil)

// Changes 返回变更日志仓储。
func (s *Store) Changes() *Changes { return &Changes{s: s} }

const changeColumns = `plan_id, kind, title, risk, status, steps, warnings, notes, payload, created_at, updated_at`

// Save 实现 change.Journal。
//
// upsert 语义：同一个 plan_id 会被覆盖。执行过程中每完成一步都会调用它，
// 因此这里必须是"更新"而不是"插入失败"。
func (c *Changes) Save(ctx context.Context, rec change.Record) error {
	steps, err := json.Marshal(orEmptySteps(rec.Steps))
	if err != nil {
		return fmt.Errorf("store: 序列化变更步骤失败: %w", err)
	}
	warnings, err := json.Marshal(orEmptyStrings(rec.Warnings))
	if err != nil {
		return fmt.Errorf("store: 序列化变更警告失败: %w", err)
	}
	notes, err := json.Marshal(orEmptyStrings(rec.Notes))
	if err != nil {
		return fmt.Errorf("store: 序列化变更说明失败: %w", err)
	}
	// payload 是后端私有的回滚数据，原样存取、不做解释。
	//
	// 用 sql.NullString 而不是空串：要区分"这条变更没有回滚数据"
	// 与"回滚数据是空 JSON"。前者意味着跨进程撤销做不了，
	// 后者是一个合法的（虽然无用的）值。
	var payload sql.NullString
	if len(rec.Payload) > 0 {
		payload = sql.NullString{String: string(rec.Payload), Valid: true}
	}

	const q = `INSERT INTO change_journal (` + changeColumns + `)
	VALUES (?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(plan_id) DO UPDATE SET
	    kind = excluded.kind,
	    title = excluded.title,
	    risk = excluded.risk,
	    status = excluded.status,
	    steps = excluded.steps,
	    warnings = excluded.warnings,
	    notes = excluded.notes,
	    payload = excluded.payload,
	    updated_at = excluded.updated_at`

	_, err = c.s.db.ExecContext(ctx, q,
		rec.PlanID, rec.Kind, rec.Title, string(rec.Risk), string(rec.Status),
		string(steps), string(warnings), string(notes), payload,
		formatTime(rec.CreatedAt), formatTime(rec.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store: 写入变更记录失败: %w", err)
	}
	return nil
}

// Get 实现 change.Journal。
func (c *Changes) Get(ctx context.Context, planID string) (change.Record, bool, error) {
	q := `SELECT ` + changeColumns + ` FROM change_journal WHERE plan_id = ?`
	row := c.s.db.QueryRowContext(ctx, q, planID)

	rec, err := scanChange(row)
	if err != nil {
		if isNoRows(err) {
			return change.Record{}, false, nil
		}
		return change.Record{}, false, err
	}
	return rec, true, nil
}

// List 实现 change.Journal。
func (c *Changes) List(ctx context.Context, limit int) ([]change.Record, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + changeColumns + ` FROM change_journal
	      ORDER BY created_at DESC, rowid DESC LIMIT ?`

	rows, err := c.s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("store: 查询变更记录失败: %w", err)
	}
	defer rows.Close() //nolint:errcheck // 只读游标

	return collectChanges(rows)
}

// ListInterrupted 实现 change.Journal。
func (c *Changes) ListInterrupted(ctx context.Context) ([]change.Record, error) {
	q := `SELECT ` + changeColumns + ` FROM change_journal
	      WHERE status = ? ORDER BY created_at ASC`

	rows, err := c.s.db.QueryContext(ctx, q, string(change.StatusApplying))
	if err != nil {
		return nil, fmt.Errorf("store: 查询中断的变更失败: %w", err)
	}
	defer rows.Close() //nolint:errcheck // 只读游标

	return collectChanges(rows)
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func collectChanges(rows *sql.Rows) ([]change.Record, error) {
	var out []change.Record
	for rows.Next() {
		rec, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历变更记录失败: %w", err)
	}
	return out, nil
}

func scanChange(sc rowScanner) (change.Record, error) {
	var (
		rec       change.Record
		risk      string
		status    string
		stepsRaw  string
		warnRaw   string
		notesRaw  string
		payload   sql.NullString
		createdAt string
		updatedAt string
	)
	err := sc.Scan(&rec.PlanID, &rec.Kind, &rec.Title, &risk, &status,
		&stepsRaw, &warnRaw, &notesRaw, &payload, &createdAt, &updatedAt)
	if err != nil {
		return change.Record{}, err
	}

	rec.Risk = change.Risk(risk)
	rec.Status = change.Status(status)
	rec.CreatedAt = parseTime(createdAt)
	rec.UpdatedAt = parseTime(updatedAt)

	// 反序列化失败不返回错误。
	//
	// 理由：步骤快照是**展示用**的附属信息，而记录本身（谁、什么时候、
	// 什么类型、什么状态）才是回滚所必需的。因为一段展示文本解析不了
	// 就让整条记录读不出来，会让用户失去撤销一次危险变更的唯一入口 ——
	// 那是拿关键功能给附属信息陪葬。
	if stepsRaw != "" {
		if err := json.Unmarshal([]byte(stepsRaw), &rec.Steps); err != nil {
			rec.Steps = []change.StepRecord{{
				ID:    "unparsed",
				Title: "（步骤详情无法解析，但变更本身仍可撤销）",
				State: change.StepApplied,
			}}
		}
	}
	if warnRaw != "" {
		_ = json.Unmarshal([]byte(warnRaw), &rec.Warnings)
	}
	if notesRaw != "" {
		_ = json.Unmarshal([]byte(notesRaw), &rec.Notes)
	}
	if payload.Valid {
		rec.Payload = []byte(payload.String)
	}
	return rec, nil
}

func orEmptySteps(v []change.StepRecord) []change.StepRecord {
	if v == nil {
		return []change.StepRecord{}
	}
	return v
}

func orEmptyStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
