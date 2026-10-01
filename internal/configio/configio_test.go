package configio

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/credential"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
	"github.com/ShirazuNagisa/isc-core/internal/provider"
	"github.com/ShirazuNagisa/isc-core/internal/secret"
	"github.com/ShirazuNagisa/isc-core/internal/settings"
)

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

// memRepo 是内存版凭据仓储。
//
// 这里用替身是合理的：本包要验证的是"导入导出与迁移的语义"，
// 不是 SQLite 的持久化行为 —— 后者由 internal/store 自己的测试覆盖。
// 用真实数据库只会让这层测试变慢、失败原因变模糊。
type memRepo struct {
	mu     sync.Mutex
	order  []string
	byID   map[string]credential.Record
	nextID int
}

func newMemRepo() *memRepo {
	return &memRepo{byID: make(map[string]credential.Record)}
}

func (r *memRepo) List(_ context.Context, cursor string, limit int) ([]credential.Record, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if limit <= 0 {
		limit = 50
	}
	start := 0
	if cursor != "" {
		for i, id := range r.order {
			if id == cursor {
				start = i + 1
				break
			}
		}
	}
	end := start + limit
	if end > len(r.order) {
		end = len(r.order)
	}

	// 按创建时间倒序，与真实实现的排序一致。
	ids := make([]string, 0, len(r.order))
	ids = append(ids, r.order...)
	sort.SliceStable(ids, func(i, j int) bool {
		return r.byID[ids[i]].CreatedAt.After(r.byID[ids[j]].CreatedAt)
	})

	out := make([]credential.Record, 0, limit)
	for _, id := range ids[start:end] {
		out = append(out, r.byID[id])
	}
	next := ""
	if end < len(ids) && len(out) > 0 {
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

func (r *memRepo) Get(_ context.Context, id string) (credential.Record, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byID[id]
	return rec, ok, nil
}

func (r *memRepo) FindByProviderLabel(_ context.Context, providerName, label string) (credential.Record, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.byID {
		if rec.Provider == providerName && rec.Label == label {
			return rec, true, nil
		}
	}
	return credential.Record{}, false, nil
}

func (r *memRepo) Insert(_ context.Context, rec credential.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.byID {
		if existing.Provider == rec.Provider && existing.Label == rec.Label {
			return credential.ErrDuplicateLabel
		}
	}
	r.byID[rec.ID] = rec
	r.order = append(r.order, rec.ID)
	return nil
}

func (r *memRepo) Update(_ context.Context, rec credential.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[rec.ID]; !ok {
		return credential.ErrNotFound
	}
	r.byID[rec.ID] = rec
	return nil
}

func (r *memRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[id]; !ok {
		return credential.ErrNotFound
	}
	delete(r.byID, id)
	return nil
}

// memSettings 是内存版设置存储。
type memSettings struct {
	mu sync.Mutex
	kv map[string]string
}

func (m *memSettings) LoadSettings(context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.kv))
	for k, v := range m.kv {
		out[k] = v
	}
	return out, nil
}

func (m *memSettings) SaveSettings(_ context.Context, kv map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.kv == nil {
		m.kv = make(map[string]string)
	}
	for k, v := range kv {
		m.kv[k] = v
	}
	return nil
}

// newTestService 组装一套可用的服务。
func newTestService(t *testing.T) (*Service, *memRepo) {
	t.Helper()
	ctx := context.Background()

	repo := newMemRepo()
	secrets, err := secret.Open(ctx, platform.NewSecretStore(t.TempDir()))
	if err != nil {
		t.Fatalf("打开主密钥失败: %v", err)
	}
	reg := provider.Default()
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	creds := credential.NewService(repo, secrets, func(name string) ([]credential.FieldSpec, bool) {
		p, ok := reg.Get(name)
		if !ok {
			return nil, false
		}
		return p.CredentialFields, true
	}, discard)

	setSvc, err := settings.Load(ctx, &memSettings{})
	if err != nil {
		t.Fatalf("加载设置失败: %v", err)
	}
	return New(creds, setSvc, reg), repo
}

// ---------------------------------------------------------------------------
// 导出 / 导入
// ---------------------------------------------------------------------------

func TestExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	// 先建两条凭据。
	if err := createCred(ctx, t, svc, "cloudflare", "主账号", map[string]string{
		"token": "cf-token-abcdefghijklmnop",
	}); err != nil {
		t.Fatal(err)
	}
	if err := createCred(ctx, t, svc, "alidns", "阿里云", map[string]string{
		"access_key_id": "LTAI-public", "access_key_secret": "very-secret",
	}); err != nil {
		t.Fatal(err)
	}

	body, err := svc.Export(ctx, true)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if !strings.Contains(string(body), "cloudflare") {
		t.Fatal("导出内容里没有 cloudflare")
	}

	// 导入到一个全新的服务实例：模拟"换一台机器恢复"。
	svc2, repo2 := newTestService(t)
	res, err := svc2.Import(ctx, body, false)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if res.Summary.CredentialsCreated != 2 {
		t.Errorf("应新建 2 条凭据，得到 %d", res.Summary.CredentialsCreated)
	}
	if len(res.Errors) != 0 {
		t.Errorf("不应有错误: %v", res.Errors)
	}

	items, _, err := repo2.List(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("导入后应有 2 条凭据，得到 %d", len(items))
	}
}

// TestExportHidesSecretsByDefault 是这份功能里最重要的一条保证。
//
// 一个"顺手导出"产生的明文密钥文件是常见的事故来源。
func TestExportHidesSecretsByDefault(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	const secretValue = "super-secret-token-value"
	if err := createCred(ctx, t, svc, "cloudflare", "主账号",
		map[string]string{"token": secretValue}); err != nil {
		t.Fatal(err)
	}

	// 默认导出。
	masked, err := svc.Export(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(masked), secretValue) {
		t.Fatal("默认导出中出现了明文密钥")
	}
	if !strings.Contains(string(masked), credential.Mask) {
		t.Error("默认导出应当用掩码占位，而不是省略字段")
	}

	// 显式要求时才导出明文。
	plain, err := svc.Export(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plain), secretValue) {
		t.Error("显式要求 include_secrets 时应当导出明文")
	}
}

// TestDryRunWritesNothing 验证预览模式真的不写。
func TestDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestService(t)

	if err := createCred(ctx, t, svc, "cloudflare", "主账号",
		map[string]string{"token": "t"}); err != nil {
		t.Fatal(err)
	}
	body, err := svc.Export(ctx, true)
	if err != nil {
		t.Fatal(err)
	}

	svc2, repo2 := newTestService(t)
	res, err := svc2.Import(ctx, body, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun {
		t.Error("结果应当标记为 dry_run")
	}
	if res.Summary.CredentialsCreated != 1 {
		t.Errorf("预览应当报告将会新建 1 条，得到 %d", res.Summary.CredentialsCreated)
	}

	items, _, err := repo2.List(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("预览模式不应写入任何数据，却出现了 %d 条", len(items))
	}

	// 原库也不该被改动。
	orig, _, _ := repo.List(ctx, "", 10)
	if len(orig) != 1 {
		t.Errorf("原库应仍是 1 条，得到 %d", len(orig))
	}
}

// TestImportUpdatesExisting 验证按 (服务商, 标签) 匹配更新。
func TestImportUpdatesExisting(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	if err := createCred(ctx, t, svc, "cloudflare", "主账号",
		map[string]string{"token": "old-token"}); err != nil {
		t.Fatal(err)
	}

	doc := `format_version: 1
credentials:
  - provider: cloudflare
    label: 主账号
    fields:
      token: new-token
`
	res, err := svc.Import(ctx, []byte(doc), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.CredentialsUpdated != 1 {
		t.Errorf("应更新 1 条，得到 %+v", res.Summary)
	}

	// 确认值真的变了。
	body, _ := svc.Export(ctx, true)
	if !strings.Contains(string(body), "new-token") {
		t.Error("更新未生效")
	}
	if strings.Contains(string(body), "old-token") {
		t.Error("旧值仍在")
	}
}

func TestImportRejectsMalformedDocument(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	cases := []string{
		"this is not yaml: [",                // 语法错误
		"foo: bar",                           // 既无 format_version 也无凭据
		"format_version: 0\ncredentials: []", // 版本号为 0 视为未声明
	}
	for i, doc := range cases {
		if _, err := svc.Import(ctx, []byte(doc), true); err == nil {
			t.Errorf("第 %d 个畸形文档应当被拒绝", i)
		}
	}
}

// TestImportAcceptsEmptyDocument 验证一份合法的空导出是允许的。
//
// 它是有意义的场景：用户导出了一份还没配置任何凭据的配置，
// 之后再导入回来。拒绝它会让"恢复备份"这个动作莫名其妙地失败。
func TestImportAcceptsEmptyDocument(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	res, err := svc.Import(ctx, []byte("format_version: 1\n"), true)
	if err != nil {
		t.Fatalf("合法的空文档不应被拒绝: %v", err)
	}
	if res.Summary.CredentialsCreated != 0 {
		t.Errorf("空文档不应创建任何凭据，得到 %d", res.Summary.CredentialsCreated)
	}
}

// TestImportRejectsFutureFormatVersion 验证不会误读更高版本的文件。
func TestImportRejectsFutureFormatVersion(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	doc := `format_version: 999
credentials:
  - provider: cloudflare
    label: x
    fields: {token: t}
`
	_, err := svc.Import(ctx, []byte(doc), true)
	if err == nil {
		t.Fatal("更高格式版本应当被拒绝，而不是按当前规则猜测")
	}
	if !strings.Contains(err.Error(), "999") {
		t.Errorf("错误信息应当指出格式版本，得到: %v", err)
	}
}

// TestImportSkipsUnknownProvider 验证未知服务商被跳过但不中断整体导入。
func TestImportSkipsUnknownProvider(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	doc := `format_version: 1
credentials:
  - provider: not-a-real-provider
    label: x
    fields: {token: t}
  - provider: cloudflare
    label: 好的一条
    fields: {token: t}
`
	res, err := svc.Import(ctx, []byte(doc), false)
	if err != nil {
		t.Fatalf("整体导入不应失败: %v", err)
	}
	if res.Summary.CredentialsCreated != 1 {
		t.Errorf("应成功导入 1 条，得到 %d", res.Summary.CredentialsCreated)
	}
	if res.Summary.CredentialsSkipped != 1 {
		t.Errorf("应跳过 1 条，得到 %d", res.Summary.CredentialsSkipped)
	}
	if len(res.Errors) != 1 {
		t.Errorf("应当报告 1 条错误，得到 %v", res.Errors)
	}
}

// ---------------------------------------------------------------------------
// ddns-go 迁移
// ---------------------------------------------------------------------------

// sampleDdnsGo 是一份贴近真实的 ddns-go 配置。
//
// 注意 webhookurl 与 username 都在**顶层**：ddns-go 的 Config 匿名内嵌了
// Webhook 与 User 结构，而 yaml.v3 会内联匿名结构体。
// 这一点很容易搞错，而且搞错之后的表现是"webhook 被静默忽略"。
const sampleDdnsGo = `dnsconf:
    - ipv4:
        enable: true
        gettype: url
        url: https://myip4.ipip.net,https://ddns.oray.com/checkip
        netinterface: ""
        cmd: ""
        domains:
            - home.example.com
            - nas.example.com
      ipv6:
        enable: true
        gettype: netInterface
        url: ""
        netinterface: 以太网
        cmd: ""
        ipv6reg: ""
        domains:
            - home.example.com
      dns:
        name: cloudflare
        id: ""
        secret: cf-token-value
        extparam: ""
      ttl: "600"
      httpinterface: ""
    - ipv4:
        enable: true
        gettype: netInterface
        netinterface: 以太网
        domains:
            - second.example.com
      ipv6:
        enable: false
        domains: []
      dns:
        name: cloudflare
        id: ""
        secret: cf-token-value
        extparam: ""
      ttl: ""
username: admin
password: $2a$10$abcdefghijklmnopqrstuv
webhookurl: https://example.com/hook
webhookrequestbody: ""
webhookheaders: ""
notallowwanaccess: false
lang: zh
`

func TestImportDdnsGo(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestService(t)

	res, err := svc.ImportDdnsGo(ctx, []byte(sampleDdnsGo), false)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Errorf("不应有错误: %v", res.Errors)
	}

	// 两条 dnsconf 用的是同一组凭据，按内容去重后只应建一条。
	if res.Summary.CredentialsCreated != 1 {
		t.Errorf("应当只建 1 条凭据（内容相同被去重），得到 %d",
			res.Summary.CredentialsCreated)
	}

	items, _, err := repo.List(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("库中应有 1 条凭据，得到 %d", len(items))
	}

	// 凭据内容应当被正确映射：ddns-go 的 secret → cloudflare 的 token。
	exported, err := svc.Export(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exported), "cf-token-value") {
		t.Errorf("凭据内容未正确迁移:\n%s", exported)
	}

	// webhook 与动态解析任务都应当被明确告知"暂未迁移"，
	// 而不是静默丢弃。
	assertWarningContains(t, res.Warnings, "webhook")
	assertWarningContains(t, res.Warnings, "动态解析")
}

// TestImportDdnsGoDeduplicatesDistinctCredentials 验证不同凭据不会被误合并。
func TestImportDdnsGoDeduplicatesDistinctCredentials(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	doc := strings.ReplaceAll(sampleDdnsGo, "cf-token-value", "TOKEN_PLACEHOLDER")
	doc = strings.Replace(doc, "secret: TOKEN_PLACEHOLDER", "secret: token-one", 1)
	doc = strings.Replace(doc, "secret: TOKEN_PLACEHOLDER", "secret: token-two", 1)

	res, err := svc.ImportDdnsGo(ctx, []byte(doc), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.CredentialsCreated != 2 {
		t.Errorf("两组不同凭据应当各建一条，得到 %d", res.Summary.CredentialsCreated)
	}
}

// TestImportDdnsGoRejectsOtherYaml 验证非 ddns-go 内容被明确拒绝。
func TestImportDdnsGoRejectsOtherYaml(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	for _, doc := range []string{
		"foo: bar",
		"format_version: 1\ncredentials: []",
		"dnsconf: []",
	} {
		_, err := svc.ImportDdnsGo(ctx, []byte(doc), true)
		if !errors.Is(err, ErrNotDdnsGo) {
			t.Errorf("应当返回 ErrNotDdnsGo，得到 %v", err)
		}
	}
}

func TestImportDdnsGoUnknownProviderIsReported(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	doc := `dnsconf:
    - ipv4:
        enable: true
        gettype: url
        url: https://example.com
        domains: [a.example.com]
      ipv6:
        enable: false
      dns:
        name: totally-unknown-provider
        id: x
        secret: y
`
	res, err := svc.ImportDdnsGo(ctx, []byte(doc), false)
	if err != nil {
		t.Fatalf("整体不应失败: %v", err)
	}
	if len(res.Errors) != 1 {
		t.Errorf("应当报告 1 条错误，得到 %v", res.Errors)
	}
	if res.Summary.CredentialsCreated != 0 {
		t.Error("未知服务商不应产生凭据")
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func createCred(ctx context.Context, t *testing.T, svc *Service,
	providerName, label string, fields map[string]string) error {
	t.Helper()
	_, err := svc.credentials.Create(ctx, credential.Credential{
		Provider: providerName, Label: label, Fields: fields,
	})
	return err
}

func assertWarningContains(t *testing.T, warnings []string, needle string) {
	t.Helper()
	for _, w := range warnings {
		if strings.Contains(w, needle) {
			return
		}
	}
	t.Errorf("警告中应当包含 %q，实际为 %v", needle, warnings)
}

var _ = time.Now // 保留 time 引用，供将来加入时间相关断言
