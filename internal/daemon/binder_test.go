package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/apps"
	"github.com/ShirazuNagisa/isc-core/internal/ddns"
)

// memoryTaskRepo 是 ddns.Repository 的内存实现。
type memoryTaskRepo struct {
	tasks map[string]ddns.Task
	seq   int
}

func newMemoryTaskRepo() *memoryTaskRepo { return &memoryTaskRepo{tasks: map[string]ddns.Task{}} }

func (r *memoryTaskRepo) List(context.Context, bool) ([]ddns.Task, error) {
	out := make([]ddns.Task, 0, len(r.tasks))
	for _, task := range r.tasks {
		out = append(out, task)
	}
	return out, nil
}

func (r *memoryTaskRepo) Get(_ context.Context, id string) (ddns.Task, bool, error) {
	task, ok := r.tasks[id]
	return task, ok, nil
}

func (r *memoryTaskRepo) Insert(_ context.Context, task ddns.Task) error {
	r.seq++
	if task.ID == "" {
		task.ID = "task-" + string(rune('a'+r.seq))
	}
	r.tasks[task.ID] = task
	return nil
}

func (r *memoryTaskRepo) Update(_ context.Context, task ddns.Task) error {
	if _, ok := r.tasks[task.ID]; !ok {
		return errors.New("not found")
	}
	r.tasks[task.ID] = task
	return nil
}

func (r *memoryTaskRepo) Delete(_ context.Context, id string) error {
	delete(r.tasks, id)
	return nil
}

func (r *memoryTaskRepo) CountByCredential(_ context.Context, credentialID string) (int, error) {
	count := 0
	for _, task := range r.tasks {
		if task.CredentialID == credentialID {
			count++
		}
	}
	return count, nil
}

// newTestBinder 构造一个只有动态解析能力的 binder（反代部分留空）。
func newTestBinder(t *testing.T, credentialID string) (*appBinder, *memoryTaskRepo, *ddns.Service) {
	t.Helper()
	repo := newMemoryTaskRepo()
	service := ddns.NewService(repo, nil, nil)
	binder := &appBinder{
		tasks: service,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		dnsCredential: func(context.Context) string {
			return credentialID
		},
	}
	return binder, repo, service
}

// 部署一个带域名的站点时，应当自动建一个动态解析任务。
//
// 只配反向代理的话，域名指向的是**配置那一刻**的地址；家宽的地址会变，
// 于是"发布成功了，第二天打不开"。用户没有理由知道这两件事要分别配置。
func TestEnsureDNSCreatesATaskForTheAppDomains(t *testing.T) {
	binder, _, service := newTestBinder(t, "cred-1")
	ctx := context.Background()

	app := apps.App{ID: "app-1", Name: "我的站点", Domains: []string{"home.example.com"}}
	taskID, err := binder.EnsureDNS(ctx, app)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if taskID == "" {
		t.Fatalf("a task should have been created")
	}

	tasks, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected exactly one task, got %d", len(tasks))
	}
	task := tasks[0]
	if task.CredentialID != "cred-1" {
		t.Fatalf("credential = %q", task.CredentialID)
	}
	if task.Label != "我的站点" {
		t.Fatalf("label = %q", task.Label)
	}
	if !task.Enabled {
		t.Fatalf("the task should be enabled")
	}
	// 两种记录类型都开：内核的地址快照会过滤掉不能用于公网的地址，
	// 因此没有可用 IPv4 时它不会硬写一个私网地址进去。
	if !task.IPv4.Enable || !task.IPv6.Enable {
		t.Fatalf("both families should be maintained: %#v", task)
	}
	if len(task.IPv4.Domains) != 1 || task.IPv4.Domains[0] != "home.example.com" {
		t.Fatalf("domains = %v", task.IPv4.Domains)
	}
}

// 幂等：重复部署同一个站点不该堆出一串任务。
func TestEnsureDNSIsIdempotent(t *testing.T) {
	binder, _, service := newTestBinder(t, "cred-1")
	ctx := context.Background()
	app := apps.App{ID: "app-1", Name: "站点", Domains: []string{"home.example.com"}}

	first, err := binder.EnsureDNS(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	second, err := binder.EnsureDNS(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("the same app should reuse its task: %q vs %q", first, second)
	}
	tasks, _ := service.List(ctx)
	if len(tasks) != 1 {
		t.Fatalf("expected one task, got %d", len(tasks))
	}
}

// 同一个站点加了新域名时，把它补进已有任务，而不是另建一个 ——
// 两个任务同时更新同一个域名会互相打架。
func TestEnsureDNSAddsMissingDomainsToTheExistingTask(t *testing.T) {
	binder, _, service := newTestBinder(t, "cred-1")
	ctx := context.Background()

	base := apps.App{ID: "app-1", Name: "站点", Domains: []string{"a.example.com"}}
	if _, err := binder.EnsureDNS(ctx, base); err != nil {
		t.Fatal(err)
	}
	widened := base
	widened.Domains = []string{"a.example.com", "b.example.com"}
	taskID, err := binder.EnsureDNS(ctx, widened)
	if err != nil {
		t.Fatal(err)
	}

	tasks, _ := service.List(ctx)
	if len(tasks) != 1 {
		t.Fatalf("expected the existing task to be reused, got %d tasks", len(tasks))
	}
	if tasks[0].ID != taskID {
		t.Fatalf("returned id does not match the stored task")
	}
	if len(tasks[0].IPv4.Domains) != 2 {
		t.Fatalf("the new domain should have been added: %v", tasks[0].IPv4.Domains)
	}
	// 已有域名不能重复添加。
	if tasks[0].IPv4.Domains[0] == tasks[0].IPv4.Domains[1] {
		t.Fatalf("a domain was added twice: %v", tasks[0].IPv4.Domains)
	}
}

// 没有 DNS 凭据时什么都不做，而且**不算错误** —— 那不是失败，
// 只是这件事现在做不了。
func TestEnsureDNSWithoutACredentialIsANoOp(t *testing.T) {
	binder, _, service := newTestBinder(t, "")
	ctx := context.Background()

	taskID, err := binder.EnsureDNS(ctx, apps.App{ID: "a", Domains: []string{"x.example.com"}})
	if err != nil {
		t.Fatalf("a missing credential must not be an error: %v", err)
	}
	if taskID != "" {
		t.Fatalf("nothing should have been created")
	}
	tasks, _ := service.List(ctx)
	if len(tasks) != 0 {
		t.Fatalf("no task should exist")
	}
}

// 没有域名时也不该凭空建任务。
func TestEnsureDNSWithoutDomainsIsANoOp(t *testing.T) {
	binder, _, service := newTestBinder(t, "cred-1")
	if _, err := binder.EnsureDNS(context.Background(), apps.App{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := service.List(context.Background())
	if len(tasks) != 0 {
		t.Fatalf("no domain, no task")
	}
}

// 删除站点时要把它从任务里摘掉；任务空了就一并删掉。
func TestRemoveDNSStripsTheDomainAndDropsAnEmptyTask(t *testing.T) {
	binder, _, service := newTestBinder(t, "cred-1")
	ctx := context.Background()

	app := apps.App{ID: "app-1", Name: "站点", Domains: []string{"a.example.com", "b.example.com"}}
	taskID, err := binder.EnsureDNS(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	app.DDNSTaskID = taskID

	// 先摘一个域名：任务还在，另一个域名仍受维护。
	if err := binder.RemoveDNS(ctx, app, "a.example.com"); err != nil {
		t.Fatal(err)
	}
	tasks, _ := service.List(ctx)
	if len(tasks) != 1 {
		t.Fatalf("the task should survive while it still has a domain")
	}
	if len(tasks[0].IPv4.Domains) != 1 || tasks[0].IPv4.Domains[0] != "b.example.com" {
		t.Fatalf("remaining domains = %v", tasks[0].IPv4.Domains)
	}

	// 再摘一个：一个域名都不剩的任务什么也不做，应当被删掉。
	if err := binder.RemoveDNS(ctx, app, "b.example.com"); err != nil {
		t.Fatal(err)
	}
	tasks, _ = service.List(ctx)
	if len(tasks) != 0 {
		t.Fatalf("an empty task should have been removed, got %#v", tasks)
	}
}

// 用户手工建的任务不该被删站点的动作碰掉。
func TestRemoveDNSIgnoresTasksTheAppDidNotCreate(t *testing.T) {
	binder, _, service := newTestBinder(t, "cred-1")
	ctx := context.Background()

	manual, err := service.Create(ctx, ddns.Task{
		CredentialID: "cred-1", Label: "手工建的", Enabled: true,
		IPv4: ddns.Source{Enable: true, GetType: ddns.GetTypeNetInterface, Domains: []string{"keep.example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 这个应用没有 DDNSTaskID（不是它建的）。
	app := apps.App{ID: "app-1", Domains: []string{"keep.example.com"}}
	if err := binder.RemoveDNS(ctx, app, "keep.example.com"); err != nil {
		t.Fatal(err)
	}
	tasks, _ := service.List(ctx)
	if len(tasks) != 1 || tasks[0].ID != manual.ID {
		t.Fatalf("a task the app did not create must be left alone: %#v", tasks)
	}
}
