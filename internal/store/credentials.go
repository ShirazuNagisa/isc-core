package store

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"strconv"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/credential"
)

// Credentials 是 credential.Repository 的 SQLite 实现。
//
// 之所以做成独立的类型而不是直接挂在 *Store 上：*Store 同时还要实现
// job.Store，而两者都有 Get 与 List 方法 —— Go 不允许在同一类型上
// 定义两个同名方法。分成两个适配器既解决了冲突，也让"某个方法属于
// 哪个领域接口"在类型上一目了然。
//
// 这一层**只搬运密文**：SecretCipher 原样写入、原样读出，
// 它不知道也不关心里面是什么。加解密是 internal/credential 的事。
//
// 分页用 SQLite 的隐式 rowid 而不是 created_at：
// 同一毫秒内创建的记录靠时间戳排序会得到不确定的顺序，
// 翻页时就会出现重复项或漏项。
type Credentials struct{ s *Store }

// 编译期断言：接口实现必须完整。
var _ credential.Repository = (*Credentials)(nil)

// Credentials 返回凭据仓储。
func (s *Store) Credentials() *Credentials { return &Credentials{s: s} }

// credentialColumns 是统一的列清单，避免各处手写不一致。
const credentialColumns = `
    rowid, id, provider, label, secret_cipher, key_version,
    created_at, updated_at, last_verified_at, last_verify_ok, last_verify_error`

// List 实现 credential.Repository。
func (c *Credentials) List(ctx context.Context, cursor string, limit int) ([]credential.Record, string, error) {
	if limit <= 0 {
		limit = 50
	}

	before := int64(0)
	if cursor != "" {
		parsed, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			// 游标不是我们发的（客户端传了垃圾）。当作第一页处理而不是报错：
			// 界面拿到"最新一页"比弹一个 400 更能自愈。
			parsed = 0
		}
		before = parsed
	}

	const q = `SELECT` + credentialColumns + ` FROM credentials
	           WHERE (? = 0 OR rowid < ?)
	           ORDER BY rowid DESC LIMIT ?`

	rows, err := c.s.db.QueryContext(ctx, q, before, before, limit)
	if err != nil {
		return nil, "", fmt.Errorf(i18n.T("store.err.list_creds"), err)
	}
	defer rows.Close() //nolint:errcheck // 只读游标

	out := make([]credential.Record, 0, limit)
	var lastRowID int64
	for rows.Next() {
		rec, rowID, err := scanCredential(rows)
		if err != nil {
			return nil, "", fmt.Errorf(i18n.T("store.err.scan_cred"), err)
		}
		out = append(out, rec)
		lastRowID = rowID
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf(i18n.T("store.err.iter_creds"), err)
	}

	next := ""
	// 只有"可能还有下一页"时才给游标：取满一页就意味着本次可能被截断。
	if len(out) == limit && lastRowID > 0 {
		next = strconv.FormatInt(lastRowID, 10)
	}
	return out, next, nil
}

// Get 实现 credential.Repository。
func (c *Credentials) Get(ctx context.Context, id string) (credential.Record, bool, error) {
	const q = `SELECT` + credentialColumns + ` FROM credentials WHERE id = ?`

	row := c.s.db.QueryRowContext(ctx, q, id)
	rec, _, err := scanCredential(row)
	if err != nil {
		if isNoRows(err) {
			return credential.Record{}, false, nil
		}
		return credential.Record{}, false, fmt.Errorf(i18n.T("store.err.get_cred"), err)
	}
	return rec, true, nil
}

// FindByProviderLabel 实现 credential.Repository。
func (c *Credentials) FindByProviderLabel(ctx context.Context, providerName, label string) (credential.Record, bool, error) {
	const q = `SELECT` + credentialColumns + `
	           FROM credentials WHERE provider = ? AND label = ?`

	row := c.s.db.QueryRowContext(ctx, q, providerName, label)
	rec, _, err := scanCredential(row)
	if err != nil {
		if isNoRows(err) {
			return credential.Record{}, false, nil
		}
		return credential.Record{}, false, fmt.Errorf(i18n.T("store.err.cred_by_label"), err)
	}
	return rec, true, nil
}

// Insert 实现 credential.Repository。
func (c *Credentials) Insert(ctx context.Context, rec credential.Record) error {
	const q = `INSERT INTO credentials
	    (id, provider, label, secret_cipher, key_version,
	     created_at, updated_at, last_verified_at, last_verify_ok, last_verify_error)
	    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := c.s.db.ExecContext(ctx, q,
		rec.ID, rec.Provider, rec.Label, rec.SecretCipher, rec.KeyVersion,
		formatTime(rec.CreatedAt), formatTime(rec.UpdatedAt),
		formatTimePtr(rec.LastVerifiedAt), boolToInt(rec.LastVerifyOK), rec.LastVerifyError)
	if err != nil {
		if isUniqueViolation(err) {
			return credential.ErrDuplicateLabel
		}
		return fmt.Errorf(i18n.T("store.err.insert_cred"), err)
	}
	return nil
}

// Update 实现 credential.Repository。
func (c *Credentials) Update(ctx context.Context, rec credential.Record) error {
	const q = `UPDATE credentials SET
	        provider = ?, label = ?, secret_cipher = ?, key_version = ?,
	        updated_at = ?, last_verified_at = ?, last_verify_ok = ?, last_verify_error = ?
	    WHERE id = ?`

	_, err := c.s.db.ExecContext(ctx, q,
		rec.Provider, rec.Label, rec.SecretCipher, rec.KeyVersion,
		formatTime(rec.UpdatedAt),
		formatTimePtr(rec.LastVerifiedAt), boolToInt(rec.LastVerifyOK), rec.LastVerifyError,
		rec.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return credential.ErrDuplicateLabel
		}
		return fmt.Errorf(i18n.T("store.err.update_cred"), err)
	}
	return nil
}

// Delete 实现 credential.Repository。
func (c *Credentials) Delete(ctx context.Context, id string) error {
	res, err := c.s.db.ExecContext(ctx, `DELETE FROM credentials WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf(i18n.T("store.err.delete_cred"), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf(i18n.T("store.err.delete_result"), err)
	}
	if n == 0 {
		return credential.ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// 扫描辅助
// ---------------------------------------------------------------------------

// rowScanner 同时被 *sql.Row 与 *sql.Rows 满足。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanCredential(sc rowScanner) (credential.Record, int64, error) {
	var (
		rec       credential.Record
		rowID     int64
		createdAt string
		updatedAt string
		verified  sql.NullString
		verifyOK  sql.NullInt64
		verifyErr sql.NullString
	)
	err := sc.Scan(
		&rowID, &rec.ID, &rec.Provider, &rec.Label, &rec.SecretCipher, &rec.KeyVersion,
		&createdAt, &updatedAt, &verified, &verifyOK, &verifyErr,
	)
	if err != nil {
		return credential.Record{}, 0, err
	}

	rec.CreatedAt = parseTime(createdAt)
	rec.UpdatedAt = parseTime(updatedAt)
	rec.LastVerifiedAt = parseTimePtr(verified)
	if verifyOK.Valid {
		v := verifyOK.Int64 != 0
		rec.LastVerifyOK = &v
	}
	rec.LastVerifyError = verifyErr.String
	return rec, rowID, nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return now()
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func boolToInt(b *bool) any {
	if b == nil {
		return nil
	}
	if *b {
		return int64(1)
	}
	return int64(0)
}

// isUniqueViolation 判断错误是否为唯一约束冲突。
//
// modernc.org/sqlite 没有公开稳定的错误码常量，因此只能按错误文本判断。
// 这不够优雅，但比硬编码一套驱动内部的错误码映射更可靠 ——
// 后者会在驱动升级时静默失效，而失效的表现是"重复标签不再被拒绝"，
// 属于很难被发现的那类退化。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}
