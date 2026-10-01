package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// migrationsFS 内嵌全部迁移脚本。
//
// 内嵌而不是读磁盘：迁移脚本是**代码**的一部分，必须与二进制严格对应。
// 从磁盘读会引入"用户机器上的脚本被改过"这种无法复现的故障。
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// migration 是一个待执行的迁移。
type migration struct {
	version int
	name    string
	body    string
}

// migrate 执行全部尚未应用的迁移。
//
// 每个迁移在**独立事务**中执行：任一迁移失败时，它自身的改动会回滚，
// 而此前成功的迁移保持已应用。这样用户可以修正失败的脚本后重跑，
// 而不是面对一个"改了一半"的数据库。
func (s *Store) migrate(ctx context.Context) error {
	if err := s.ensureMigrationsTable(ctx); err != nil {
		return err
	}

	applied, err := s.appliedVersions(ctx)
	if err != nil {
		return err
	}

	all, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range all {
		if applied[m.version] {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureMigrationsTable(ctx context.Context) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    applied_at TEXT    NOT NULL
)`
	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("store: 创建迁移记录表失败: %w", err)
	}
	return nil
}

func (s *Store) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: 读取已应用迁移失败: %w", err)
	}
	defer rows.Close() //nolint:errcheck // 只读游标，关闭失败无影响

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("store: 扫描迁移版本失败: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历迁移版本失败: %w", err)
	}
	return applied, nil
}

// applyMigration 在事务中应用单个迁移。
//
// 注意：迁移脚本里**不得**包含 BEGIN/COMMIT —— 事务由这里控制，
// 脚本里再开一个会导致嵌套事务错误（SQLite 不支持真正的嵌套事务）。
func (s *Store) applyMigration(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: 开启迁移事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // 已提交时此调用是空操作

	if _, err := tx.ExecContext(ctx, m.body); err != nil {
		return fmt.Errorf("store: 应用迁移 %04d_%s 失败: %w", m.version, m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, now(),
	); err != nil {
		return fmt.Errorf("store: 记录迁移 %04d_%s 失败: %w", m.version, m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交迁移 %04d_%s 失败: %w", m.version, m.name, err)
	}
	return nil
}

// loadMigrations 读取并解析内嵌的迁移脚本，按版本号升序返回。
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: 读取迁移目录失败: %w", err)
	}

	out := make([]migration, 0, len(entries))
	seen := make(map[int]string, len(entries))

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(e.Name())
		if err != nil {
			return nil, err
		}
		// 版本号重复意味着两个脚本都想成为"第 N 号" ——
		// 这通常来自合并分支时的命名冲突，必须当场失败而不是
		// 随机跳过其中一个。
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf(
				"store: 迁移版本号 %d 重复（%s 与 %s）", version, prev, e.Name())
		}
		seen[version] = e.Name()

		body, err := migrationsFS.ReadFile(path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("store: 读取迁移 %s 失败: %w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: name, body: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// parseMigrationName 解析 `NNNN_描述.sql` 形式的文件名。
func parseMigrationName(filename string) (version int, name string, err error) {
	base := strings.TrimSuffix(filename, ".sql")
	numStr, rest, ok := strings.Cut(base, "_")
	if !ok {
		return 0, "", fmt.Errorf(
			"store: 迁移文件名 %q 不符合 NNNN_描述.sql 格式", filename)
	}
	version, err = strconv.Atoi(numStr)
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf(
			"store: 迁移文件名 %q 的版本号不是正整数", filename)
	}
	if rest == "" {
		return 0, "", fmt.Errorf("store: 迁移文件名 %q 缺少描述部分", filename)
	}
	return version, rest, nil
}
