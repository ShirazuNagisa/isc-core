package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/job"
)

// Jobs 是 job.Store 的 SQLite 实现。
//
// 为什么这事值得单独说明：M0 的任务引擎用的是内存实现，当时就把存储
// 抽成了接口，注释里写的是"M1 接入 SQLite 后换实现，引擎本身不需要
// 改动"。现在兑现这句话 —— internal/job 的引擎代码一行没动。
//
// 做成独立类型而不是挂在 *Store 上，是因为 *Store 还要实现
// credential.Repository，而两者都有 Get 与 List（见 credentials.go）。
type Jobs struct{ s *Store }

// 编译期断言：接口实现必须完整且签名一致。
var _ job.Store = (*Jobs)(nil)

// Jobs 返回任务仓储。
func (s *Store) Jobs() *Jobs { return &Jobs{s: s} }

// Save 实现 job.Store。
//
// 用 UPSERT 而不是"先查再决定插入还是更新"：任务状态在生命周期内会被
// 反复写入（每次进度更新一次），两步操作既慢又会在并发下产生竞态。
func (j *Jobs) Save(ctx context.Context, r job.Job) error {
	const q = `INSERT INTO jobs
	    (id, kind, status, progress, message, result, error, created_at, started_at, finished_at)
	    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	    ON CONFLICT(id) DO UPDATE SET
	        status      = excluded.status,
	        progress    = excluded.progress,
	        message     = excluded.message,
	        result      = excluded.result,
	        error       = excluded.error,
	        started_at  = excluded.started_at,
	        finished_at = excluded.finished_at`

	var (
		resultJSON any
		errorJSON  any
		message    any
	)
	if len(r.Result) > 0 {
		resultJSON = string(r.Result)
	}
	if r.Err != nil {
		if byt, err := json.Marshal(r.Err); err == nil {
			errorJSON = string(byt)
		}
	}
	if r.Message != "" {
		message = r.Message
	}

	_, err := j.s.db.ExecContext(ctx, q,
		r.ID, r.Kind, string(r.Status), r.Progress, message, resultJSON, errorJSON,
		formatTime(r.CreatedAt), formatTimePtr(r.StartedAt), formatTimePtr(r.FinishedAt))
	if err != nil {
		return fmt.Errorf("store: 保存任务失败: %w", err)
	}
	return nil
}

// Get 实现 job.Store。
func (j *Jobs) Get(ctx context.Context, id string) (job.Job, bool, error) {
	const q = `SELECT seq, id, kind, status, progress, COALESCE(message,''),
	                  COALESCE(result,''), COALESCE(error,''),
	                  created_at, started_at, finished_at
	           FROM jobs WHERE id = ?`

	row := j.s.db.QueryRowContext(ctx, q, id)
	rec, _, err := scanJob(row)
	if err != nil {
		if isNoRows(err) {
			return job.Job{}, false, nil
		}
		return job.Job{}, false, fmt.Errorf("store: 查询任务失败: %w", err)
	}
	return rec, true, nil
}

// List 实现 job.Store。
//
// 按 seq 倒序（最新在前）并用 seq 作游标：created_at 在同一毫秒内
// 可能重复，靠时间戳排序会让翻页出现重复或漏项。
func (j *Jobs) List(ctx context.Context, f job.Filter) ([]job.Job, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = job.DefaultLimit
	}

	var (
		where []string
		args  []any
	)
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if len(f.Statuses) > 0 {
		placeholders := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			placeholders[i] = "?"
			args = append(args, string(st))
		}
		where = append(where, "status IN ("+strings.Join(placeholders, ",")+")")
	}
	if f.Cursor != "" {
		seq, err := strconv.ParseInt(f.Cursor, 10, 64)
		if err != nil {
			// 游标不是我们发的：当作第一页，让界面自愈而不是报 400。
			seq = 0
		}
		if seq > 0 {
			where = append(where, "seq < ?")
			args = append(args, seq)
		}
	}

	q := `SELECT seq, id, kind, status, progress, COALESCE(message,''),
	             COALESCE(result,''), COALESCE(error,''),
	             created_at, started_at, finished_at
	      FROM jobs`
	if len(where) > 0 {
		q += " WHERE " + joinAnd(where)
	}
	q += " ORDER BY seq DESC LIMIT ?"
	args = append(args, limit)

	rows, err := j.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("store: 查询任务列表失败: %w", err)
	}
	defer rows.Close() //nolint:errcheck // 只读游标

	out := make([]job.Job, 0, limit)
	var lastSeq int64
	for rows.Next() {
		rec, seq, err := scanJob(rows)
		if err != nil {
			return nil, "", fmt.Errorf("store: 扫描任务失败: %w", err)
		}
		out = append(out, rec)
		lastSeq = seq
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("store: 遍历任务失败: %w", err)
	}

	next := ""
	if len(out) == limit && lastSeq > 0 {
		next = strconv.FormatInt(lastSeq, 10)
	}
	return out, next, nil
}

// Prune 删除超出保留数量的**历史**任务。
//
// 存在的必要性：定时任务每几分钟产生一条记录，一年就是十几万条，
// 而用户几乎不会去翻三个月前的任务。保留最近 N 条即可。
//
// 两点语义必须说清楚：
//
//  1. **只删终态任务**。在途任务被删掉会让它完成时查不到自己，
//     而引擎仍持有它并会尝试写回 —— 那是个很难复现的状态。
//  2. **配额只算终态任务**。若把在途任务也算进 N，那么在途任务一多
//     就会把配额占满，导致清理彻底失效（一条历史记录都删不掉）。
func (j *Jobs) Prune(ctx context.Context, keep int) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}
	const terminal = `('succeeded','failed','canceled')`
	q := `DELETE FROM jobs
	      WHERE status IN ` + terminal + `
	        AND seq NOT IN (
	            SELECT seq FROM jobs
	            WHERE status IN ` + terminal + `
	            ORDER BY seq DESC LIMIT ?
	        )`
	res, err := j.s.db.ExecContext(ctx, q, keep)
	if err != nil {
		return 0, fmt.Errorf("store: 清理历史任务失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil // 驱动不支持时不影响主流程
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// 扫描辅助
// ---------------------------------------------------------------------------

func scanJob(sc rowScanner) (job.Job, int64, error) {
	var (
		seq       int64
		rec       job.Job
		id        string
		kind      string
		status    string
		progress  float64
		message   string
		result    string
		errJSON   string
		createdAt string
		startedAt sql.NullString
		finished  sql.NullString
	)
	err := sc.Scan(&seq, &id, &kind, &status, &progress, &message,
		&result, &errJSON, &createdAt, &startedAt, &finished)
	if err != nil {
		return job.Job{}, 0, err
	}

	rec = job.Job{
		ID:        id,
		Kind:      kind,
		Status:    job.Status(status),
		Progress:  progress,
		Message:   message,
		CreatedAt: parseTime(createdAt),
	}
	if result != "" {
		rec.Result = json.RawMessage(result)
	}
	if errJSON != "" {
		var je job.Error
		if err := json.Unmarshal([]byte(errJSON), &je); err == nil {
			rec.Err = &je
		}
	}
	rec.StartedAt = parseTimePtr(startedAt)
	rec.FinishedAt = parseTimePtr(finished)
	return rec, seq, nil
}
