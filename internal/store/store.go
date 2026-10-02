// Package store 是 ISC 的持久化层：嵌入式 SQLite + 版本化迁移。
//
// 为什么用 SQLite（见 docs/DECISIONS.md D06）：
//
//   - 内核要管理的实体（凭据/区域/记录/任务/服务/证书/规则/计划/事件/审计）
//     是关系型的，列表分页与过滤是刚需；
//   - 驱动选 modernc.org/sqlite 而非 mattn/go-sqlite3，因为后者需要 cgo，
//     而本项目禁止 cgo（见 docs/PLAN.md §0）—— 那会让三平台交叉编译
//     彻底失去意义。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"time"

	// 纯 Go 的 SQLite 驱动，注册为 "sqlite"。
	_ "modernc.org/sqlite"
)

// Store 是持久化层的入口。
//
// 它同时实现了 job.Store，因此任务引擎可以直接把它当作存储后端
// （见 docs/PLAN.md M0 的说明：M0 用内存实现，M1 换成 SQLite，
// 引擎代码不需要改动 —— 这就是当初把它抽成接口的目的）。
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）数据库并执行迁移。
func Open(ctx context.Context, path string) (*Store, error) {
	// 连接参数说明：
	//
	//	journal_mode(WAL)  写入不阻塞读取，避免界面查询被写事务卡住
	//	busy_timeout(5000) 遇到锁时等待而不是立刻报 SQLITE_BUSY
	//	foreign_keys(1)    启用外键约束（SQLite 默认关闭，是个陷阱）
	//	synchronous(NORMAL) WAL 下的推荐值：崩溃不会损坏库，
	//	                   只在断电瞬间可能丢最后几个事务
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("store.err.open"), err)
	}

	// 单连接。
	//
	// 取舍说明：这会串行化所有查询，牺牲理论吞吐换取"绝不可能出现
	// SQLITE_BUSY 或连接间不一致"的确定性。本内核的负载是"单用户偶尔
	// 点几下界面 + 每几分钟一次定时任务"，串行化完全够用，
	// 而并发问题带来的排查成本远高于那点吞吐。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf(i18n.T("store.err.ping"), err)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB 暴露底层连接，仅供同包的仓储实现使用。
func (s *Store) DB() *sql.DB { return s.db }

// now 返回统一格式的当前时间。
//
// 全部时间以 RFC 3339（UTC，纳秒精度）存为 TEXT：SQLite 没有原生
// 时间类型，而 TEXT 形式的 RFC 3339 既可直接字符串比较排序，
// 也可被人用 sqlite3 命令行直接读懂 —— 排查问题时这一点很值钱。
func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// parseTime 解析存储的时间；空串返回零值。
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// parseTimePtr 解析可空时间列。
func parseTimePtr(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return nil
	}
	return &t
}

// formatTimePtr 格式化可空时间列。
func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// isNoRows 判断"没有数据"，把 sql.ErrNoRows 收敛到一处。
func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
