package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件覆盖迁移的**校验和**机制。
//
// # 它防的是什么
//
// 在这之前，`schema_migrations` 只有 version / name / applied_at，而
// `migrate.go` 的注释却写着"迁移脚本是代码的一部分，必须与二进制严格对应"
// —— **没有任何东西在保证那句话**。
//
// 具体后果不是理论问题。有人想给某张表加一列，图省事直接改了
// `0001_init.sql` 而不是新建 `0007_xxx.sql`：
//
//   - 新装的机器：建表时就有那一列，一切正常；
//   - 已升级的机器：0001 的版本号已在表里，迁移被**静默跳过**，那一列不存在。
//
// 两个数据库的 schema_migrations 内容**完全相同**，实际 schema 却不同。
// 之后所有在老库上的查询都会报 `no such column`，而新库上一切正常 ——
// 那是最难复现、也最容易被误判成"用户环境有问题"的一类故障。

// openStore 打开一个位于临时目录的库。
func openStore(t *testing.T, path string) (*Store, error) {
	t.Helper()
	return Open(context.Background(), path)
}

// TestMigrationsRecordChecksums 验证新库会记下每个迁移的指纹。
func TestMigrationsRecordChecksums(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "isc.db")
	s, err := openStore(t, path)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer func() { _ = s.Close() }()

	rows, err := s.DB().QueryContext(context.Background(),
		`SELECT version, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	n := 0
	for rows.Next() {
		var (
			v   int
			sum string
		)
		if err := rows.Scan(&v, &sum); err != nil {
			t.Fatal(err)
		}
		n++
		if len(sum) != 64 {
			t.Errorf("迁移 %04d 的校验和长度是 %d，期望 64（sha256 十六进制）",
				v, len(sum))
		}
		for _, r := range sum {
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
			if !isHex {
				t.Errorf("迁移 %04d 的校验和含非十六进制字符: %q", v, sum)
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("没有任何迁移记录 —— 测试没有覆盖到东西")
	}
}

// TestChangedMigrationIsRejected 是本文件**最重要**的一条。
//
// 模拟"已应用的脚本被改过"：直接篡改库里记的指纹（等价于文件内容变了），
// 然后重新打开。必须**拒绝启动**，而不是静默跳过。
func TestChangedMigrationIsRejected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "isc.db")

	s, err := openStore(t, path)
	if err != nil {
		t.Fatal(err)
	}
	// 篡改第 1 号迁移的指纹 —— 等价于它的文件内容被改过。
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE schema_migrations SET checksum = ? WHERE version = 1`,
		strings.Repeat("0", 64),
	); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s2, err := openStore(t, path)
	if err == nil {
		// 校验和机制失效时这条路会走到这里 —— 那时必须关掉句柄，
		// 否则临时目录清理会报"文件被另一个进程占用"，
		// 而那会把注意力从真正的问题上引开。
		_ = s2.Close()
		t.Fatal("脚本被改过之后必须拒绝启动 —— " +
			"静默跳过会让老库缺列，而新库正常，症状极难归因")
	}

	msg := err.Error()
	// 错误信息必须**可行动**：指出哪个文件、以及该怎么办。
	for _, want := range []string{"0001_", "不能再改", "新的迁移文件"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息里应当含 %q，得到:\n%s", want, msg)
		}
	}
}

// TestLegacyDatabaseIsUpgradedAndBackfilled 验证老库能平滑升级。
//
// 这是**最关键的一条兼容性断言**：引入校验和之前建的库没有 checksum 列，
// 而所有既有用户都是那种库。如果这里报错，**所有人升级后都会起不来**。
func TestLegacyDatabaseIsUpgradedAndBackfilled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "isc.db")

	// 先正常建库。
	s, err := openStore(t, path)
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	// 把 schema_migrations 换成**旧结构**（没有 checksum 列）。
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE sm_legacy AS
		   SELECT version, name, applied_at FROM schema_migrations`,
		`DROP TABLE schema_migrations`,
		`ALTER TABLE sm_legacy RENAME TO schema_migrations`,
	} {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("构造旧库失败（%s）: %v", stmt, err)
		}
	}
	_ = raw.Close()

	// 重新打开：必须成功，而不是报"no such column: checksum"。
	s2, err := openStore(t, path)
	if err != nil {
		t.Fatalf("老库必须能平滑升级，得到: %v", err)
	}
	defer func() { _ = s2.Close() }()

	// 迁移条数不该变 —— 升级过程不得重复执行任何迁移。
	var after int
	if err := s2.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("迁移条数从 %d 变成了 %d —— 升级过程不该重复执行迁移",
			before, after)
	}

	// 而且指纹必须**已被回填**，否则下一次打开会拿空串去比对。
	var empty int
	if err := s2.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE checksum = ''`).Scan(&empty); err != nil {
		t.Fatal(err)
	}
	if empty != 0 {
		t.Errorf("还有 %d 条迁移的指纹是空的 —— 回填没有生效", empty)
	}
}

// TestReopenAfterBackfillIsClean 验证回填之后不再报错。
//
// 回填是一次性的：如果它在每次打开时都重做，或者回填的值与实际内容不符，
// 第二次打开就会失败 —— 而那正是"升级后能用一次、再启动就坏了"的形状。
func TestReopenAfterBackfillIsClean(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "isc.db")

	s, err := openStore(t, path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE sm_legacy AS
		   SELECT version, name, applied_at FROM schema_migrations`,
		`DROP TABLE schema_migrations`,
		`ALTER TABLE sm_legacy RENAME TO schema_migrations`,
	} {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()

	// 连续打开三次：第一次回填，之后都应当干净通过。
	for i := 1; i <= 3; i++ {
		s, err := openStore(t, path)
		if err != nil {
			t.Fatalf("第 %d 次打开失败: %v", i, err)
		}
		_ = s.Close()
	}
}

// TestMigrationChecksumIsStableAndSensitive 是校验和本身的单元测试。
//
// 两个方向都要钉住：
//
//	同样的内容 → 同样的值（否则每次启动都会误报"被改过"）
//	不同的内容 → 不同的值（否则这个机制完全无效）
func TestMigrationChecksumIsStableAndSensitive(t *testing.T) {
	t.Parallel()

	const body = "CREATE TABLE t (id INTEGER PRIMARY KEY);\n"

	if migrationChecksum(body) != migrationChecksum(body) {
		t.Error("同样的内容必须得到同样的校验和 —— 否则每次启动都会误报")
	}

	// **空白与注释的改动也算改动**。
	//
	// 刻意不做规范化：那些改动同样意味着"这个脚本变了"，
	// 而规范化会掩盖它们。
	differ := []string{
		body + "\n",
		body + "-- 加了一行注释\n",
		"CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT);\n",
		"create table t (id integer primary key);\n",
		"",
	}
	for _, other := range differ {
		if migrationChecksum(other) == migrationChecksum(body) {
			t.Errorf("内容不同却得到相同校验和: %q", other)
		}
	}
}

// TestEmptyChecksumIsOnlyForLegacyRows 钉住"空指纹"的语义。
//
// 空串是**给老行的特例**，不是"跳过校验"的开关。一个空指纹只应当出现在
// 引入校验和之前的库里；回填之后它就该消失。
func TestEmptyChecksumIsOnlyForLegacyRows(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "isc.db")
	s, err := openStore(t, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	var empty int
	if err := s.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM schema_migrations WHERE checksum = ''`).Scan(&empty); err != nil {
		t.Fatal(err)
	}
	if empty != 0 {
		t.Errorf("新库里有 %d 条迁移的指纹是空的", empty)
	}
}
