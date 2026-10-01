package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/credential"
	"github.com/ShirazuNagisa/isc-core/internal/job"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "isc.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestMigrationsAreIdempotent 验证重复打开不会重复执行迁移。
//
// 这条要是坏了，症状是"内核第二次启动就报错"，而第一次完全正常 ——
// 属于最容易被漏测的一类问题。
//
// 刻意不断言"迁移数量 == 某个具体数字"：那个数字每加一个迁移都要改一次，
// 而改的人往往只是把数字改对、并没有真的检查幂等性。这里改为
// "第二次之后的数量必须与第一次一致" —— 它直接表达要验证的性质。
func TestMigrationsAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "isc.db")
	ctx := context.Background()

	var baseline int
	for i := 1; i <= 3; i++ {
		st, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("第 %d 次打开失败: %v", i, err)
		}
		var count int
		if err := st.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
			t.Fatalf("读取迁移记录失败: %v", err)
		}
		if count == 0 {
			t.Fatal("迁移记录为空")
		}
		if i == 1 {
			baseline = count
		} else if count != baseline {
			t.Errorf("第 %d 次打开后迁移记录数为 %d，首次为 %d —— 迁移被重复执行了",
				i, count, baseline)
		}
		_ = st.Close()
	}
}

// TestMigrationFilesAreWellFormed 检查内嵌迁移文件的命名与唯一性。
func TestMigrationFilesAreWellFormed(t *testing.T) {
	all, err := loadMigrations()
	if err != nil {
		t.Fatalf("加载迁移失败: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("没有找到任何迁移文件")
	}
	for i, m := range all {
		if m.version != i+1 {
			t.Errorf("第 %d 个迁移的版本号为 %d，期望连续递增", i, m.version)
		}
		if m.name == "" || m.body == "" {
			t.Errorf("迁移 %04d 缺少名称或内容", m.version)
		}
		// 迁移脚本里不得自带事务：事务由 applyMigration 控制，
		// 脚本里再开一个会导致 SQLite 报嵌套事务错误。
		for _, kw := range []string{"BEGIN TRANSACTION", "BEGIN;", "COMMIT;"} {
			if containsFold(m.body, kw) {
				t.Errorf("迁移 %04d 不应包含 %q —— 事务由迁移器控制", m.version, kw)
			}
		}
	}
}

func containsFold(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		indexFold(haystack, needle) >= 0
}

func indexFold(h, n string) int {
	hl := toLower(h)
	nl := toLower(n)
	for i := 0; i+len(nl) <= len(hl); i++ {
		if hl[i:i+len(nl)] == nl {
			return i
		}
	}
	return -1
}

func toLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// 设置
// ---------------------------------------------------------------------------

func TestSettingsRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	got, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("空库读取设置失败: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("空库应当返回空 map，得到 %v", got)
	}

	want := map[string]string{"lang": "en", "log_level": "debug"}
	if err := st.SaveSettings(ctx, want); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}
	got, err = st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("设置 %q = %q, 期望 %q", k, got[k], v)
		}
	}

	// 覆盖写。
	if err := st.SaveSettings(ctx, map[string]string{"lang": "zh-CN"}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.LoadSettings(ctx)
	if got["lang"] != "zh-CN" {
		t.Errorf("覆盖写失败，得到 %q", got["lang"])
	}
	if got["log_level"] != "debug" {
		t.Error("覆盖写不应影响未提交的键")
	}
}

// ---------------------------------------------------------------------------
// 凭据
// ---------------------------------------------------------------------------

func TestCredentialsCRUD(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := st.Credentials()

	rec := credential.Record{
		ID: "cred-1", Provider: "cloudflare", Label: "主账号",
		SecretCipher: []byte{1, 2, 3}, KeyVersion: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.Insert(ctx, rec); err != nil {
		t.Fatalf("插入失败: %v", err)
	}

	got, found, err := repo.Get(ctx, "cred-1")
	if err != nil || !found {
		t.Fatalf("查询失败: found=%v err=%v", found, err)
	}
	if got.Provider != "cloudflare" || got.Label != "主账号" {
		t.Errorf("字段不一致: %+v", got)
	}
	if string(got.SecretCipher) != string([]byte{1, 2, 3}) {
		t.Error("密文未被原样保存")
	}

	// 不存在的 ID。
	if _, found, err := repo.Get(ctx, "nope"); err != nil || found {
		t.Errorf("查询不存在的凭据应返回 found=false，得到 found=%v err=%v", found, err)
	}

	// 更新。
	rec.Label = "改名了"
	if err := repo.Update(ctx, rec); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	got, _, _ = repo.Get(ctx, "cred-1")
	if got.Label != "改名了" {
		t.Errorf("更新未生效，得到 %q", got.Label)
	}

	// 删除。
	if err := repo.Delete(ctx, "cred-1"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if err := repo.Delete(ctx, "cred-1"); !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("重复删除应返回 ErrNotFound，得到 %v", err)
	}
}

// TestCredentialLabelUniqueness 验证唯一索引生效并被正确翻译。
func TestCredentialLabelUniqueness(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := st.Credentials()

	base := credential.Record{
		Provider: "cloudflare", Label: "同名",
		SecretCipher: []byte{1}, KeyVersion: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	a := base
	a.ID = "a"
	if err := repo.Insert(ctx, a); err != nil {
		t.Fatal(err)
	}

	b := base
	b.ID = "b"
	err := repo.Insert(ctx, b)
	if !errors.Is(err, credential.ErrDuplicateLabel) {
		t.Fatalf("同一服务商下的重名应返回 ErrDuplicateLabel，得到 %v", err)
	}

	// 不同服务商下同名是允许的。
	c := base
	c.ID = "c"
	c.Provider = "alidns"
	if err := repo.Insert(ctx, c); err != nil {
		t.Errorf("不同服务商下同名应当允许，得到 %v", err)
	}
}

// TestCredentialsPagination 验证游标分页不重不漏。
func TestCredentialsPagination(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := st.Credentials()

	const total = 7
	for i := 0; i < total; i++ {
		rec := credential.Record{
			ID: string(rune('a' + i)), Provider: "cloudflare", Label: string(rune('a' + i)),
			SecretCipher: []byte{byte(i)}, KeyVersion: 1,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := repo.Insert(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	seen := make(map[string]bool, total)
	cursor := ""
	for page := 0; page < 10; page++ {
		items, next, err := repo.List(ctx, cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, rec := range items {
			if seen[rec.ID] {
				t.Fatalf("分页出现重复项: %s", rec.ID)
			}
			seen[rec.ID] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}

	if len(seen) != total {
		t.Errorf("分页共取到 %d 条，期望 %d 条", len(seen), total)
	}
}

// TestCredentialCursorGarbageIsTolerated 验证垃圾游标不会让列表打不开。
func TestCredentialCursorGarbageIsTolerated(t *testing.T) {
	st := openTestStore(t)
	repo := st.Credentials()

	items, _, err := repo.List(context.Background(), "not-a-number", 10)
	if err != nil {
		t.Fatalf("垃圾游标应当被容忍并返回第一页，得到错误: %v", err)
	}
	if items == nil {
		t.Error("应当返回非 nil 的切片")
	}
}

// ---------------------------------------------------------------------------
// 审计
// ---------------------------------------------------------------------------

func TestAuditAppendAndList(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		err := st.AppendAudit(ctx, audit.Record{
			TS:     time.Now().UTC().Format(time.RFC3339Nano),
			Action: audit.ActionCredentialCreate,
			Target: "cred-" + string(rune('a'+i)),
			Result: audit.ResultSuccess,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// 一条不同动作的记录，用于验证过滤。
	if err := st.AppendAudit(ctx, audit.Record{
		TS: time.Now().UTC().Format(time.RFC3339Nano), Action: audit.ActionConfigExport,
		Result: audit.ResultFailure, Detail: "测试",
	}); err != nil {
		t.Fatal(err)
	}

	all, _, err := st.ListAudit(ctx, audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 6 {
		t.Fatalf("应当有 6 条审计，得到 %d", len(all))
	}
	// 最新在前。
	if all[0].Action != audit.ActionConfigExport {
		t.Errorf("应当按时间倒序，首条为 %q", all[0].Action)
	}

	byAction, _, err := st.ListAudit(ctx, audit.Filter{Action: audit.ActionCredentialCreate})
	if err != nil {
		t.Fatal(err)
	}
	if len(byAction) != 5 {
		t.Errorf("按动作过滤应得到 5 条，得到 %d", len(byAction))
	}

	byResult, _, err := st.ListAudit(ctx, audit.Filter{Result: audit.ResultFailure})
	if err != nil {
		t.Fatal(err)
	}
	if len(byResult) != 1 {
		t.Errorf("按结果过滤应得到 1 条，得到 %d", len(byResult))
	}
}

// ---------------------------------------------------------------------------
// 任务
// ---------------------------------------------------------------------------

func TestJobsStoreImplementsInterface(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	jobs := st.Jobs()

	now := time.Now().UTC()
	j := job.Job{
		ID: "job-1", Kind: "debug.noop", Status: job.StatusRunning,
		Progress: 0.5, Message: "进行中", CreatedAt: now,
	}
	if err := jobs.Save(ctx, j); err != nil {
		t.Fatalf("保存任务失败: %v", err)
	}

	got, found, err := jobs.Get(ctx, "job-1")
	if err != nil || !found {
		t.Fatalf("读取任务失败: found=%v err=%v", found, err)
	}
	if got.Status != job.StatusRunning || got.Progress != 0.5 || got.Message != "进行中" {
		t.Errorf("字段不一致: %+v", got)
	}

	// 同一 ID 再存一次应当是更新而不是报错（任务状态会被反复写入）。
	j.Status = job.StatusSucceeded
	j.Progress = 1
	j.FinishedAt = &now
	j.Result = []byte(`{"ok":true}`)
	if err := jobs.Save(ctx, j); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	got, _, _ = jobs.Get(ctx, "job-1")
	if got.Status != job.StatusSucceeded || got.Progress != 1 {
		t.Errorf("更新未生效: %+v", got)
	}
	if string(got.Result) != `{"ok":true}` {
		t.Errorf("结果未正确保存: %s", got.Result)
	}

	// 失败任务的结构化错误。
	failed := job.Job{
		ID: "job-2", Kind: "debug.noop", Status: job.StatusFailed,
		CreatedAt: now, FinishedAt: &now,
		Err: &job.Error{Code: "boom", Title: "炸了", Detail: "细节"},
	}
	if err := jobs.Save(ctx, failed); err != nil {
		t.Fatal(err)
	}
	got, _, _ = jobs.Get(ctx, "job-2")
	if got.Err == nil || got.Err.Code != "boom" {
		t.Fatalf("结构化错误未正确保存: %+v", got.Err)
	}
}

// TestJobsPagination 验证任务分页用 seq 而不是时间戳。
//
// 同一毫秒内创建的任务靠时间戳排序会得到不确定的顺序，
// 翻页时就会出现重复或漏项 —— 这条测试专门覆盖那个场景。
func TestJobsPagination(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	jobs := st.Jobs()

	// 刻意用完全相同的时间戳创建全部任务。
	now := time.Now().UTC()
	const total = 10
	for i := 0; i < total; i++ {
		j := job.Job{
			ID: string(rune('a' + i)), Kind: "test", Status: job.StatusSucceeded,
			CreatedAt: now, FinishedAt: &now,
		}
		if err := jobs.Save(ctx, j); err != nil {
			t.Fatal(err)
		}
	}

	seen := make(map[string]bool, total)
	cursor := ""
	for page := 0; page < 20; page++ {
		items, next, err := jobs.List(ctx, job.Filter{Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range items {
			if seen[j.ID] {
				t.Fatalf("分页出现重复项: %s", j.ID)
			}
			seen[j.ID] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != total {
		t.Errorf("分页共取到 %d 条，期望 %d 条", len(seen), total)
	}
}

// TestJobsPruneKeepsRunningTasks 验证清理不会删掉在途任务。
//
// 删掉在途任务会让它完成时"查不到自己"，而引擎仍持有它 ——
// 那是一个很难复现的状态。
func TestJobsPruneKeepsRunningTasks(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	jobs := st.Jobs()

	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		j := job.Job{
			ID: "done-" + string(rune('a'+i)), Kind: "test",
			Status: job.StatusSucceeded, CreatedAt: now, FinishedAt: &now,
		}
		if err := jobs.Save(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	running := job.Job{ID: "running", Kind: "test", Status: job.StatusRunning, CreatedAt: now}
	if err := jobs.Save(ctx, running); err != nil {
		t.Fatal(err)
	}

	if _, err := jobs.Prune(ctx, 3); err != nil {
		t.Fatalf("清理失败: %v", err)
	}

	if _, found, _ := jobs.Get(ctx, "running"); !found {
		t.Error("在途任务不应被清理")
	}
	items, _, err := jobs.List(ctx, job.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	// 剩下的应当是 3 条终态 + 1 条在途。
	if len(items) != 4 {
		t.Errorf("清理后应剩 4 条，得到 %d", len(items))
	}
}

func TestJobFilterByStatus(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	jobs := st.Jobs()

	now := time.Now().UTC()
	for _, st := range []job.Status{job.StatusSucceeded, job.StatusFailed, job.StatusRunning} {
		j := job.Job{ID: string(st), Kind: "test", Status: st, CreatedAt: now}
		if err := jobs.Save(ctx, j); err != nil {
			t.Fatal(err)
		}
	}

	items, _, err := jobs.List(ctx, job.Filter{
		Statuses: []job.Status{job.StatusSucceeded, job.StatusFailed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("按状态过滤应得到 2 条，得到 %d", len(items))
	}
	for _, j := range items {
		if j.Status == job.StatusRunning {
			t.Error("过滤结果中不应出现在途任务")
		}
	}
}
