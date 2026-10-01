package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/credential"
)

// 本文件覆盖 M1 的硬验收标准：
//
//  1. 能通过接口增删改 DNS 凭据；
//  2. 直接用数据库文件检查，凭据字段是密文；
//  3. 重启内核后凭据仍能正常解密使用；
//  4. 能导入一份真实的 ddns-go 配置。
//
// 这些断言刻意全部走 HTTP 接口而不是直接调内部函数 —— 它们要验证的
// 是"用户能做到这件事"，而不是"函数能跑通"。

// ---------------------------------------------------------------------------
// 请求辅助
// ---------------------------------------------------------------------------

func (h *harness) do(method, path string, body []byte) *http.Response {
	h.t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(),
		method, h.baseURL+path, reader)
	if err != nil {
		h.t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+h.info.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s 失败: %v", method, path, err)
	}
	return resp
}

func (h *harness) decode(resp *http.Response, out any) {
	h.t.Helper()
	defer resp.Body.Close() //nolint:errcheck // 测试清理
	if out == nil {
		return
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		h.t.Fatalf("解码响应失败: %v", err)
	}
}

func (h *harness) createCredential(providerName, label string, fields map[string]string) gen.Credential {
	h.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"provider": providerName, "label": label, "fields": fields,
	})
	resp := h.do(http.MethodPost, "/v1/credentials", body)
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close() //nolint:errcheck // 测试清理
		h.t.Fatalf("创建凭据返回 %d: %s", resp.StatusCode, raw)
	}
	var out gen.Credential
	h.decode(resp, &out)
	return out
}

// ---------------------------------------------------------------------------
// 契约：服务商元信息
// ---------------------------------------------------------------------------

// TestProvidersExposeCredentialFields 验证 GUI 渲染表单所需的信息齐全。
//
// 这是"下游 GUI 不需要为每家服务商写死界面"这条设计承诺的可执行证据。
func TestProvidersExposeCredentialFields(t *testing.T) {
	h := startDaemon(t)

	var resp struct {
		Items []gen.Provider `json:"items"`
	}
	h.getJSON("/v1/providers", &resp)

	if len(resp.Items) == 0 {
		t.Fatal("服务商列表不应为空")
	}

	byName := make(map[string]gen.Provider, len(resp.Items))
	for _, p := range resp.Items {
		byName[p.Name] = p
		if p.DisplayName == "" {
			t.Errorf("服务商 %s 缺少展示名称", p.Name)
		}
		if len(p.CredentialFields) == 0 {
			t.Errorf("服务商 %s 没有声明任何凭据字段，界面无法渲染表单", p.Name)
		}
		for _, f := range p.CredentialFields {
			if f.Key == "" {
				t.Errorf("服务商 %s 有一个字段缺少 key", p.Name)
			}
			if f.Label == "" {
				t.Errorf("服务商 %s 的字段 %s 缺少展示名称（i18n 未生效？）", p.Name, f.Key)
			}
			// 漏翻的表现是界面上出现 "provider.field.xxx" 这样的原始 key。
			if strings.HasPrefix(f.Label, "provider.") {
				t.Errorf("服务商 %s 的字段 %s 标签落回了 i18n key: %q", p.Name, f.Key, f.Label)
			}
		}
	}

	// Tier-1 五家必须在列表里。
	for _, want := range []string{"cloudflare", "alidns", "tencentcloud", "dnspod", "huaweicloud", "godaddy"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("Tier-1 服务商 %s 未出现在列表中", want)
		}
	}

	// Cloudflare 在 M1 已能校验凭据，必须如实报告。
	cf := byName["cloudflare"]
	if !cf.Capabilities.Available {
		t.Error("Cloudflare 的凭据校验已实现，应当报告 available=true")
	}
	// Tier-2 尚未实现，也必须如实报告 —— 界面要能显示"尚未实现"，
	// 而不是让用户对着一排灰按钮猜原因。
	if byName["porkbun"].Capabilities.Available {
		t.Error("尚未实现的服务商不应报告 available=true")
	}
}

// ---------------------------------------------------------------------------
// 凭据 CRUD
// ---------------------------------------------------------------------------

// TestCredentialLifecycle 覆盖 M1 验收的第 1 条。
func TestCredentialLifecycle(t *testing.T) {
	h := startDaemon(t)

	const plaintext = "cf-super-secret-token-12345"

	// --- 新建 ---
	created := h.createCredential("cloudflare", "主账号", map[string]string{"token": plaintext})
	if created.Id == "" {
		t.Fatal("创建后应当返回 ID")
	}
	if created.Provider != "cloudflare" || created.Label != "主账号" {
		t.Errorf("创建结果字段不符: %+v", created)
	}
	// 响应里绝不能出现明文。
	if created.Fields["token"] != credential.Mask {
		t.Errorf("接口返回的敏感字段应为掩码，得到 %q", created.Fields["token"])
	}

	// --- 列表 ---
	var list gen.CredentialList
	h.getJSON("/v1/credentials", &list)
	if len(list.Items) != 1 {
		t.Fatalf("列表应有 1 条，得到 %d", len(list.Items))
	}
	if list.Items[0].Fields["token"] != credential.Mask {
		t.Error("列表中的敏感字段也应被掩码")
	}

	// --- 更新：只改标签，敏感字段原样回传掩码 ---
	patch, _ := json.Marshal(map[string]any{
		"provider": "cloudflare",
		"label":    "改过名的账号",
		"fields":   map[string]string{"token": credential.Mask},
	})
	resp := h.do(http.MethodPatch, "/v1/credentials/"+created.Id, patch)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close() //nolint:errcheck // 测试清理
		t.Fatalf("更新返回 %d: %s", resp.StatusCode, raw)
	}
	var updated gen.Credential
	h.decode(resp, &updated)
	if updated.Label != "改过名的账号" {
		t.Errorf("标签未更新: %q", updated.Label)
	}

	// 关键断言：回传掩码不应把密钥改成八个星号。
	// 用一次真实导出（含明文）来验证。
	assertStoredSecret(t, h, plaintext)

	// --- 删除 ---
	resp = h.do(http.MethodDelete, "/v1/credentials/"+created.Id, nil)
	resp.Body.Close() //nolint:errcheck // 测试清理
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("删除应返回 204，得到 %d", resp.StatusCode)
	}

	h.getJSON("/v1/credentials", &list)
	if len(list.Items) != 0 {
		t.Errorf("删除后列表应为空，得到 %d 条", len(list.Items))
	}
}

// assertStoredSecret 用一次含明文的导出验证密钥原值未被破坏。
func assertStoredSecret(t *testing.T, h *harness, want string) {
	t.Helper()
	resp := h.do(http.MethodGet, "/v1/config/export?include_secrets=true", nil)
	defer resp.Body.Close() //nolint:errcheck // 测试清理
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), want) {
		t.Errorf("导出内容中未找到原始密钥，说明它已被破坏:\n%s", body)
	}
}

// TestCredentialValidationRejected 验证非法输入被明确拒绝。
func TestCredentialValidationRejected(t *testing.T) {
	h := startDaemon(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"未知服务商", map[string]any{
			"provider": "not-a-provider", "label": "x",
			"fields": map[string]string{"token": "t"}}},
		{"缺少必填字段", map[string]any{
			"provider": "cloudflare", "label": "x", "fields": map[string]string{}}},
		{"标签为空", map[string]any{
			"provider": "cloudflare", "label": "", "fields": map[string]string{"token": "t"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.body)
			resp := h.do(http.MethodPost, "/v1/credentials", raw)
			defer resp.Body.Close() //nolint:errcheck // 测试清理

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("期望 400，得到 %d", resp.StatusCode)
			}
			var p gen.Problem
			if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
				t.Fatalf("错误响应应当是 problem+json: %v", err)
			}
			if p.Code == nil || *p.Code != "invalid_request" {
				t.Errorf("错误码 = %v", p.Code)
			}
		})
	}
}

// TestDuplicateLabelRejected 验证同名凭据被拒绝。
func TestDuplicateLabelRejected(t *testing.T) {
	h := startDaemon(t)

	h.createCredential("cloudflare", "同名", map[string]string{"token": "a"})

	raw, _ := json.Marshal(map[string]any{
		"provider": "cloudflare", "label": "同名",
		"fields": map[string]string{"token": "b"},
	})
	resp := h.do(http.MethodPost, "/v1/credentials", raw)
	defer resp.Body.Close() //nolint:errcheck // 测试清理

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("重名应返回 409，得到 %d", resp.StatusCode)
	}
}

// TestProviderImmutable 验证服务商不可修改。
//
// 换了服务商就意味着字段语义全变了，保留旧字段只会得到一份
// 看起来正常、实际用不了的凭据。
func TestProviderImmutable(t *testing.T) {
	h := startDaemon(t)
	c := h.createCredential("cloudflare", "x", map[string]string{"token": "t"})

	raw, _ := json.Marshal(map[string]any{
		"provider": "alidns", "label": "x",
		"fields": map[string]string{"access_key_id": "a", "access_key_secret": "b"},
	})
	resp := h.do(http.MethodPatch, "/v1/credentials/"+c.Id, raw)
	defer resp.Body.Close() //nolint:errcheck // 测试清理

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("修改服务商应返回 400，得到 %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// 静态加密 —— M1 验收的第 2 条
// ---------------------------------------------------------------------------

// TestCredentialsAreEncryptedAtRest 直接读数据库文件做检查。
//
// 这是整个 M1 里最重要的一条断言：它不看接口、不看日志、
// 只看磁盘上真实躺着什么。凭据的机密性最终取决于这个事实。
func TestCredentialsAreEncryptedAtRest(t *testing.T) {
	h := startDaemon(t)

	const (
		secretToken = "PLAINTEXT-TOKEN-MUST-NOT-APPEAR-0123456789"
		accessID    = "PLAINTEXT-ACCESS-KEY-9876543210"
	)

	h.createCredential("alidns", "加密检查", map[string]string{
		"access_key_id":     accessID,
		"access_key_secret": secretToken,
	})

	// 接口必须先确认凭据真的写进去了。
	var list gen.CredentialList
	h.getJSON("/v1/credentials", &list)
	if len(list.Items) != 1 {
		t.Fatalf("凭据未创建成功，列表有 %d 条", len(list.Items))
	}

	// 直接读数据库文件（含 WAL：SQLite 的 WAL 模式下，
	// 最近写入的数据可能还在 -wal 文件里，只读主文件会漏掉）。
	assertFileHasNoPlaintext(t, h.paths.DBFile(), secretToken)
	assertFileHasNoPlaintext(t, h.paths.DBFile(), accessID)

	for _, suffix := range []string{"-wal", "-shm"} {
		path := h.paths.DBFile() + suffix
		if _, err := os.Stat(path); err == nil {
			assertFileHasNoPlaintext(t, path, secretToken)
			assertFileHasNoPlaintext(t, path, accessID)
		}
	}

	// 反向验证：明文确实能通过接口取回（含明文导出），
	// 否则上面两条断言可能只是"什么都没写进去"。
	resp := h.do(http.MethodGet, "/v1/config/export?include_secrets=true", nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck // 测试清理
	if !strings.Contains(string(body), secretToken) {
		t.Fatal("导出中找不到明文密钥 —— 说明凭据根本没被保存，" +
			"上面的加密断言是无意义的")
	}
}

func assertFileHasNoPlaintext(t *testing.T, path, needle string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	if bytes.Contains(raw, []byte(needle)) {
		t.Fatalf("%s 中出现了明文 %q —— 凭据没有真正加密",
			path, truncate(needle, 20))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------------------------------------------------------------------------
// 重启后仍可解密 —— M1 验收的第 3 条
// ---------------------------------------------------------------------------

// TestCredentialSurvivesRestart 验证主密钥被持久化。
//
// 这条要是坏了，症状是"重启后所有凭据都解不开"，而用户会以为
// 是自己的密钥填错了。
func TestCredentialSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	const secretToken = "survives-restart-token"

	// --- 第一次启动：写入凭据 ---
	first := startDaemonIn(t, dir)
	created := first.createCredential("cloudflare", "重启测试",
		map[string]string{"token": secretToken})
	assertStoredSecret(t, first, secretToken)
	first.stop()

	// --- 第二次启动：同一个数据目录 ---
	second := startDaemonIn(t, dir)
	defer second.stop()

	var list gen.CredentialList
	second.getJSON("/v1/credentials", &list)
	if len(list.Items) != 1 {
		t.Fatalf("重启后应有 1 条凭据，得到 %d", len(list.Items))
	}
	if list.Items[0].Id != created.Id {
		t.Errorf("凭据 ID 变了：%q != %q", list.Items[0].Id, created.Id)
	}
	// 关键：重启后密钥仍能被解密出来。
	assertStoredSecret(t, second, secretToken)
}

// ---------------------------------------------------------------------------
// 配置导入导出 —— M1 验收的第 4 条
// ---------------------------------------------------------------------------

// TestExportHidesSecretsByDefault 验证"顺手导出"不会产生明文密钥文件。
func TestExportHidesSecretsByDefault(t *testing.T) {
	h := startDaemon(t)

	const secretToken = "should-not-leak-by-default"
	h.createCredential("cloudflare", "导出检查", map[string]string{"token": secretToken})

	resp := h.do(http.MethodGet, "/v1/config/export", nil)
	defer resp.Body.Close() //nolint:errcheck // 测试清理

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导出应返回 200，得到 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("导出应当是 YAML，得到 Content-Type=%q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), secretToken) {
		t.Fatalf("默认导出中出现了明文密钥:\n%s", body)
	}
	if !strings.Contains(string(body), credential.Mask) {
		t.Error("默认导出应当用掩码占位，而不是省略字段")
	}
}

// TestImportDdnsGoThroughAPI 覆盖 M1 验收的第 4 条。
func TestImportDdnsGoThroughAPI(t *testing.T) {
	h := startDaemon(t)

	// 真实 ddns-go 配置的形态：webhookurl / username 在顶层
	//（ddns-go 的 Config 匿名内嵌了这两个结构，yaml 会内联它们）。
	const ddnsGoYAML = `dnsconf:
    - ipv4:
        enable: true
        gettype: url
        url: https://myip4.ipip.net
        netinterface: ""
        cmd: ""
        domains:
            - home.example.com
      ipv6:
        enable: true
        gettype: netInterface
        netinterface: 以太网
        cmd: ""
        ipv6reg: ""
        domains:
            - home.example.com
      dns:
        name: cloudflare
        id: ""
        secret: ddns-go-token-value
        extparam: ""
      ttl: "600"
      httpinterface: ""
username: admin
password: $2a$10$abcdefghijklmnopqrstuv
webhookurl: https://example.com/hook
webhookrequestbody: ""
webhookheaders: ""
lang: zh
`

	// --- 先预览 ---
	resp := h.do(http.MethodPost, "/v1/config/import/ddns-go?dry_run=true",
		[]byte(ddnsGoYAML))
	var preview gen.ImportResult
	h.decode(resp, &preview)

	if !preview.DryRun {
		t.Error("结果应当标记为 dry_run")
	}
	if preview.Summary.CredentialsCreated == nil || *preview.Summary.CredentialsCreated != 1 {
		t.Errorf("预览应当报告将新建 1 条凭据，得到 %+v", preview.Summary)
	}

	// 预览之后库里必须还是空的。
	var list gen.CredentialList
	h.getJSON("/v1/credentials", &list)
	if len(list.Items) != 0 {
		t.Fatalf("预览模式不应写入任何数据，却出现了 %d 条", len(list.Items))
	}

	// --- 真正导入 ---
	resp = h.do(http.MethodPost, "/v1/config/import/ddns-go?dry_run=false",
		[]byte(ddnsGoYAML))
	var applied gen.ImportResult
	h.decode(resp, &applied)

	if applied.DryRun {
		t.Error("dry_run=false 时不应标记为预览")
	}
	if applied.Summary.CredentialsCreated == nil || *applied.Summary.CredentialsCreated != 1 {
		t.Errorf("应当新建 1 条凭据，得到 %+v", applied.Summary)
	}

	h.getJSON("/v1/credentials", &list)
	if len(list.Items) != 1 {
		t.Fatalf("导入后应有 1 条凭据，得到 %d", len(list.Items))
	}
	if list.Items[0].Provider != "cloudflare" {
		t.Errorf("服务商应为 cloudflare，得到 %q", list.Items[0].Provider)
	}

	// 密钥必须被正确迁移 —— 早先按字段顺序映射的版本会在这里失败：
	// Cloudflare 只用 ddns-go 的 secret 槽位，按顺序映射会拿到空的 id。
	assertStoredSecret(t, h, "ddns-go-token-value")

	// webhook 与动态解析任务都应当被明确告知"暂未迁移"。
	if applied.Warnings == nil || len(*applied.Warnings) == 0 {
		t.Fatal("应当有关于未迁移内容的警告")
	}
	warnings := strings.Join(*applied.Warnings, " | ")
	if !strings.Contains(warnings, "webhook") {
		t.Errorf("应当提示 webhook 未迁移，得到: %s", warnings)
	}
	if !strings.Contains(warnings, "动态解析") {
		t.Errorf("应当提示解析任务未迁移，得到: %s", warnings)
	}

	// 导入动作必须留痕。
	var auditList gen.AuditList
	h.getJSON("/v1/audit", &auditList)
	if len(auditList.Items) == 0 {
		t.Fatal("审计日志不应为空 —— 导入是破坏性操作，必须留痕")
	}
	if !hasAuditAction(auditList, "config.import.ddnsgo") {
		t.Errorf("审计中缺少 config.import.ddnsgo，实际动作: %v", auditActions(auditList))
	}
}

func hasAuditAction(list gen.AuditList, action string) bool {
	for _, e := range list.Items {
		if e.Action == action {
			return true
		}
	}
	return false
}

func auditActions(list gen.AuditList) []string {
	out := make([]string, 0, len(list.Items))
	for _, e := range list.Items {
		out = append(out, e.Action)
	}
	return out
}

// TestAuditRecordsWrites 验证写操作留痕。
//
// 内核以系统服务身份运行、能改防火墙、能重写整个 DNS 区域 ——
// 出问题时"最后一次改动是什么、谁改的"是排查的第一入口。
func TestAuditRecordsWrites(t *testing.T) {
	h := startDaemon(t)

	c := h.createCredential("cloudflare", "审计检查", map[string]string{"token": "t"})

	// 一次失败的操作也应当被记录。
	raw, _ := json.Marshal(map[string]any{
		"provider": "cloudflare", "label": "审计检查",
		"fields": map[string]string{"token": "t"},
	})
	resp := h.do(http.MethodPost, "/v1/credentials", raw)
	resp.Body.Close() //nolint:errcheck // 测试清理

	var list gen.AuditList
	h.getJSON("/v1/audit", &list)

	if !hasAuditAction(list, "credential.create") {
		t.Errorf("缺少 credential.create 审计，实际: %v", auditActions(list))
	}
	if !hasAuditResult(list, gen.Failure) {
		t.Errorf("失败的操作也应当留痕，实际: %v", auditActions(list))
	}
	if !hasAuditTarget(list, c.Id) {
		t.Error("审计记录应当带上被操作对象的 ID")
	}

	// 审计内容绝不能包含敏感值。
	body := auditDump(t, h)
	if strings.Contains(body, `"token"`) {
		t.Error("审计内容中出现了凭据字段名 —— 审计不应记录请求体")
	}
}

func hasAuditResult(list gen.AuditList, want gen.AuditResult) bool {
	for _, e := range list.Items {
		if e.Result == want {
			return true
		}
	}
	return false
}

func hasAuditTarget(list gen.AuditList, target string) bool {
	for _, e := range list.Items {
		if e.Target != nil && *e.Target == target {
			return true
		}
	}
	return false
}

func auditDump(t *testing.T, h *harness) string {
	t.Helper()
	resp := h.do(http.MethodGet, "/v1/audit?limit=200", nil)
	defer resp.Body.Close() //nolint:errcheck // 测试清理
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// ---------------------------------------------------------------------------
// 设置
// ---------------------------------------------------------------------------

func TestSettingsRoundTripThroughAPI(t *testing.T) {
	h := startDaemon(t)

	var initial gen.Settings
	h.getJSON("/v1/settings", &initial)
	if initial.Lang == "" {
		t.Error("应当返回语言设置")
	}
	if initial.EventBufferSize == nil || *initial.EventBufferSize <= 0 {
		t.Errorf("应当返回事件缓冲容量，得到 %v", initial.EventBufferSize)
	}

	// 改一项。
	falseVal := false
	raw, _ := json.Marshal(map[string]any{"notify_on_ip_change": falseVal})
	resp := h.do(http.MethodPatch, "/v1/settings", raw)
	var updated gen.Settings
	h.decode(resp, &updated)
	if updated.NotifyOnIpChange == nil || *updated.NotifyOnIpChange {
		t.Error("设置未生效")
	}

	// 越界的值必须被拒绝，而不是存进去以后以奇怪的方式表现出来。
	tooBig := 10_000_000
	raw, _ = json.Marshal(map[string]any{"event_buffer_size": tooBig})
	resp = h.do(http.MethodPatch, "/v1/settings", raw)
	resp.Body.Close() //nolint:errcheck // 测试清理
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("越界的事件缓冲容量应返回 400，得到 %d", resp.StatusCode)
	}

	badLang := "klingon"
	raw, _ = json.Marshal(map[string]any{"lang": badLang})
	resp = h.do(http.MethodPatch, "/v1/settings", raw)
	resp.Body.Close() //nolint:errcheck // 测试清理
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("不支持的语言应返回 400，得到 %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// 任务持久化
// ---------------------------------------------------------------------------

// TestJobsSurviveRestart 验证任务落进了数据库。
//
// M0 时任务只存在内存里，重启即丢。M1 换成了 SQLite 存储 ——
// 这条测试是"引擎代码一行没改就换了后端"的证据。
func TestJobsSurviveRestart(t *testing.T) {
	dir := t.TempDir()

	first := startDaemonIn(t, dir)

	accepted := gen.JobAccepted{}
	first.postJSON("/v1/debug/noop", strings.NewReader(`{"steps":1,"step_ms":0}`), &accepted)

	// 等它跑完再重启，避免"在途任务"这个额外变量。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var j gen.Job
		first.getJSON("/v1/jobs/"+accepted.JobId, &j)
		if j.Status == gen.Succeeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	first.stop()

	second := startDaemonIn(t, dir)
	defer second.stop()

	var j gen.Job
	second.getJSON("/v1/jobs/"+accepted.JobId, &j)
	if j.Id != accepted.JobId {
		t.Errorf("重启后任务 ID 不匹配: %q != %q", j.Id, accepted.JobId)
	}
	if j.Status != gen.Succeeded {
		t.Errorf("重启后任务状态 = %q, 期望 succeeded", j.Status)
	}
	if j.Result == nil {
		t.Error("重启后任务结果应当仍可读")
	}
}

// startDaemonIn 与 harness.stop 定义在 daemon_test.go —— 它们与 M0
// 的测试共用同一套夹具，避免两处各维护一份启动逻辑而逐渐漂移。
