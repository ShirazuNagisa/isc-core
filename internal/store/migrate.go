package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
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
//
// # 已应用的迁移会被校验和比对
//
// `schema_migrations` 里记着每个迁移内容的 sha256。若一个**已经应用过**的
// 脚本被改动，这里会拒绝启动，而不是静默跳过。
//
// 为什么必须这样：注释里写着"迁移脚本是代码的一部分，必须与二进制严格对应"，
// 而在这之前**没有任何东西在保证它**。有人想给某张表加一列，图省事直接改了
// `0001_init.sql` 而不是新建 `0007_xxx.sql`，于是：
//
//   - 新装的机器建表时就有那一列，一切正常；
//   - 已升级的机器因为 0001 的版本号已在表里而**静默跳过**，那一列不存在。
//
// 两个数据库的 schema_migrations 内容完全相同，实际 schema 却不同 ——
// 之后所有在老库上的查询都会报 `no such column`，而新库上一切正常。
// 那是最难复现、也最容易被误判成"用户环境有问题"的一类故障。
func (s *Store) migrate(ctx context.Context) error {
	if err := s.ensureMigrationsTable(ctx); err != nil {
		return err
	}

	applied, err := s.appliedChecksums(ctx)
	if err != nil {
		return err
	}

	all, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range all {
		sum := migrationChecksum(m.body)

		if prev, done := applied[m.version]; done {
			switch {
			case prev == "":
				// 老库：这一行是在引入校验和之前写的，回填而不是报错 ——
				// 否则所有既有用户在升级后都会起不来。
				if err := s.backfillChecksum(ctx, m.version, sum); err != nil {
					return err
				}
			case prev != sum:
				return fmt.Errorf(i18n.T("store.migration_changed"),
					m.version, m.name, short(prev), short(sum),
					nextVersion(all), filepathOf(m))
			}
			continue
		}

		if err := s.applyMigration(ctx, m, sum); err != nil {
			return err
		}
	}
	return nil
}

// migrationChecksum 是迁移内容的指纹。
//
// 用文件的**原文**而不是规范化后的形式：规范化会掩盖空白与注释的改动，
// 而那些改动同样意味着"这个脚本变了"。
func migrationChecksum(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// short 取校验和的前 8 位，用于错误信息。
//
// 完整值对定位问题没有帮助，而它会让那一行难以阅读。
func short(sum string) string {
	if len(sum) <= 8 {
		return sum
	}
	return sum[:8]
}

// filepathOf 给出迁移在源码树里的路径，供错误信息指向具体文件。
func filepathOf(m migration) string {
	return fmt.Sprintf("internal/store/migrations/%04d_%s.sql", m.version, m.name)
}

// nextVersion 建议一个新版本号（当前最大值 + 1）。
func nextVersion(all []migration) int {
	max := 0
	for _, m := range all {
		if m.version > max {
			max = m.version
		}
	}
	return max + 1
}

func (s *Store) ensureMigrationsTable(ctx context.Context) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    applied_at TEXT    NOT NULL,
    checksum   TEXT    NOT NULL DEFAULT ''
)`
	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf(i18n.T("store.err.mk_mig_table"), err)
	}
	// 表可能是更早的版本建的（没有 checksum 列）。
	//
	// 这一步是**鸡生蛋**：补列本身不能走迁移机制 —— 它得在读取
	// schema_migrations 之前完成，而迁移机制正是靠那张表工作的。
	return s.ensureChecksumColumn(ctx)
}

// ensureChecksumColumn 为早于校验和机制建的表补上 checksum 列。
func (s *Store) ensureChecksumColumn(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(schema_migrations)`)
	if err != nil {
		return fmt.Errorf(i18n.T("store.migrate_table_info"), err)
	}
	defer rows.Close() //nolint:errcheck // 只读游标

	found := false
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notNull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return fmt.Errorf(i18n.T("store.migrate_table_scan"), err)
		}
		if name == "checksum" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf(i18n.T("store.migrate_table_iter"), err)
	}
	if found {
		return nil
	}

	if _, err := s.db.ExecContext(ctx,
		`ALTER TABLE schema_migrations ADD COLUMN checksum TEXT NOT NULL DEFAULT ''`,
	); err != nil {
		return fmt.Errorf(i18n.T("store.migrate_add_column"), err)
	}
	return nil
}

// appliedChecksums 返回 版本号 → 校验和（老行是空串）。
func (s *Store) appliedChecksums(ctx context.Context) (map[int]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("store.migrate_read_applied"), err)
	}
	defer rows.Close() //nolint:errcheck // 只读游标，关闭失败无影响

	applied := make(map[int]string)
	for rows.Next() {
		var (
			v   int
			sum string
		)
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, fmt.Errorf(i18n.T("store.migrate_scan_version"), err)
		}
		applied[v] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(i18n.T("store.migrate_iter_version"), err)
	}
	return applied, nil
}

// backfillChecksum 为引入校验和之前应用过的迁移补上指纹。
func (s *Store) backfillChecksum(ctx context.Context, version int, sum string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE schema_migrations SET checksum = ? WHERE version = ?`,
		sum, version,
	); err != nil {
		return fmt.Errorf(i18n.T("store.migrate_backfill"), version, err)
	}
	return nil
}

// applyMigration 在事务中应用单个迁移。
//
// 注意：迁移脚本里**不得**包含 BEGIN/COMMIT —— 事务由这里控制，
// 脚本里再开一个会导致嵌套事务错误（SQLite 不支持真正的嵌套事务）。
func (s *Store) applyMigration(ctx context.Context, m migration, sum string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf(i18n.T("store.err.begin_mig_tx"), err)
	}
	defer func() { _ = tx.Rollback() }() // 已提交时此调用是空操作

	if _, err := tx.ExecContext(ctx, m.body); err != nil {
		return fmt.Errorf(i18n.T("store.err.apply_mig"), m.version, m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at, checksum)
		 VALUES (?, ?, ?, ?)`,
		m.version, m.name, now(), sum,
	); err != nil {
		return fmt.Errorf(i18n.T("store.err.record_mig"), m.version, m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf(i18n.T("store.err.commit_mig"), m.version, m.name, err)
	}
	return nil
}

// loadMigrations 读取并解析内嵌的迁移脚本，按版本号升序返回。
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf(i18n.T("store.err.read_mig_dir"), err)
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
				i18n.T("store.err.dup_mig"), version, prev, e.Name())
		}
		seen[version] = e.Name()

		body, err := migrationsFS.ReadFile(path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf(i18n.T("store.err.read_mig"), e.Name(), err)
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
			i18n.T("store.err.mig_bad_name"), filename)
	}
	version, err = strconv.Atoi(numStr)
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf(
			i18n.T("store.err.mig_bad_ver"), filename)
	}
	if rest == "" {
		return 0, "", fmt.Errorf(i18n.T("store.err.mig_no_desc"), filename)
	}
	return version, rest, nil
}
