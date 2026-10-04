package presets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// writeFiles 在临时目录里造出一份源码树。
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// --- 目录 -------------------------------------------------------------------

func TestCatalogIsSelfConsistent(t *testing.T) {
	known := map[string]bool{}
	for _, key := range i18n.Keys() {
		known[key] = true
	}
	seen := map[string]bool{}
	for _, preset := range All() {
		if err := Validate(preset); err != nil {
			t.Errorf("preset %s is invalid: %v", preset.ID, err)
		}
		if seen[preset.ID] {
			t.Errorf("duplicate preset id %s", preset.ID)
		}
		seen[preset.ID] = true
		// 说明文案存的是 key，因此在这里核对它真的在消息目录里 ——
		// 拼错的 key 会把 key 本身显示给用户。
		if preset.NoteKey != "" && !known[preset.NoteKey] {
			t.Errorf("preset %s: note key %q is not in the message catalog", preset.ID, preset.NoteKey)
		}
	}
	// v0.2.0 承诺覆盖的建站方式必须都在。
	for _, want := range []string{"static-html", "node-auto", "python-auto", "php-composer", "go-module", "java-build", "dotnet-web", "docker-compose", "custom"} {
		if !seen[want] {
			t.Errorf("preset %s is missing from the catalog", want)
		}
	}
}

// 目录里不该出现 shell 语法：我们从不经 shell 执行，写进去的管道或重定向
// 会被当成普通参数传给程序，结果是"看起来能跑、其实没生效"。
func TestCatalogStepsContainNoShellSyntax(t *testing.T) {
	for _, preset := range WebsitePresets() {
		steps := append(append([]Step{}, preset.Install...), preset.Build...)
		steps = append(steps, preset.Run)
		for _, step := range steps {
			for _, field := range append([]string{step.Executable}, step.Args...) {
				if strings.ContainsAny(field, "|&;><`$") {
					t.Errorf("preset %s: %q contains shell syntax", preset.ID, field)
				}
			}
		}
	}
}

func TestLookupRejectsUnknownPreset(t *testing.T) {
	if _, err := Lookup("nope"); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("want ErrUnknownPreset, got %v", err)
	}
	if _, err := Lookup(CustomPresetID); err != nil {
		t.Fatalf("the custom preset must resolve: %v", err)
	}
}

// --- 识别 -------------------------------------------------------------------

func inspect(t *testing.T, files map[string]string) Inspection {
	t.Helper()
	result, err := Inspect(writeFiles(t, files))
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	return result
}

func TestInspectRecognisesEachWebsiteStack(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"static", map[string]string{"index.html": "<html></html>"}, "static-html"},
		{"node", map[string]string{"package.json": `{"scripts":{"start":"node server.js"}}`}, "node-auto"},
		{"python-django", map[string]string{"manage.py": "x", "requirements.txt": "django"}, "python-auto"},
		{"python-asgi", map[string]string{"app.py": "x", "requirements.txt": "fastapi\nuvicorn"}, "python-auto"},
		{"php", map[string]string{"index.php": "<?php"}, "php-composer"},
		{"go", map[string]string{"go.mod": "module x"}, "go-module"},
		{"java", map[string]string{"pom.xml": "<project/>"}, "java-build"},
		{"dotnet", map[string]string{"Web.csproj": "<Project/>"}, "dotnet-web"},
		{"compose", map[string]string{"compose.yaml": "services: {}"}, "docker-compose"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inspect(t, tc.files)
			if got.Recommended != tc.want {
				t.Fatalf("recommended %q, want %q (candidates=%v)", got.Recommended, tc.want, presetIDs(got.Candidates))
			}
			if len(got.Evidence) == 0 {
				t.Fatalf("a recommendation must come with evidence")
			}
		})
	}
}

func TestInspectFallsBackToCustomWhenNothingMatches(t *testing.T) {
	got := inspect(t, map[string]string{"notes.txt": "hello"})
	if got.Recommended != CustomPresetID {
		t.Fatalf("recommended %q, want %q", got.Recommended, CustomPresetID)
	}
	if len(got.Warnings) == 0 {
		t.Fatalf("falling back to custom should explain why")
	}
}

// 敏感文件与依赖目录不进入结果，也不拖慢扫描。
func TestInspectExcludesSecretsAndNoise(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"index.html":                  "<html>",
		".env":                        "SECRET=1",
		".ssh/id_rsa":                 "PRIVATE",
		"node_modules/pkg/index.html": "<html>",
		".git/config":                 "repo",
		"server/cert.pem":             "CERT",
	})
	got, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := evidenceFiles(got)
	for _, forbidden := range []string{".env", ".ssh", "node_modules", ".git"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("scan results must not contain %q: %v", forbidden, joined)
		}
	}
	// index.html 在 node_modules 里也有，但只应命中根部那一个。
	if strings.Count(joined, "index.html") != 1 {
		t.Fatalf("expected exactly one index.html evidence, got %v", joined)
	}
}

func TestInspectIgnoresFilesDeeperThanTheScanLimit(t *testing.T) {
	deep := strings.Repeat("a/", maxScanDepth+2) + "package.json"
	got := inspect(t, map[string]string{deep: `{"scripts":{"start":"x"}}`})
	if got.Recommended == "node-auto" {
		t.Fatalf("a manifest nested %d levels deep should not be treated as this project's stack", maxScanDepth+2)
	}
}

func TestInspectExtractsFacts(t *testing.T) {
	t.Run("node", func(t *testing.T) {
		got := inspect(t, map[string]string{"package.json": `{"main":"server.js","scripts":{"build":"tsc","start":"node ."}}`})
		if !got.Facts.NodeBuildScript || !got.Facts.NodeStartScript || got.Facts.NodeMain != "server.js" {
			t.Fatalf("unexpected node facts: %#v", got.Facts)
		}
	})
	t.Run("node without scripts", func(t *testing.T) {
		got := inspect(t, map[string]string{"package.json": `{"name":"x"}`})
		if got.Facts.NodeBuildScript || got.Facts.NodeStartScript {
			t.Fatalf("unexpected node facts: %#v", got.Facts)
		}
	})
	t.Run("php public docroot", func(t *testing.T) {
		got := inspect(t, map[string]string{"composer.json": "{}", "public/index.php": "<?php"})
		if got.Facts.PHPWebRoot != "public" {
			t.Fatalf("expected the public/ docroot, got %q", got.Facts.PHPWebRoot)
		}
	})
	t.Run("dotnet assembly name", func(t *testing.T) {
		got := inspect(t, map[string]string{"MyWeb.csproj": "<Project/>"})
		if got.Facts.DotNetAssembly != "MyWeb" {
			t.Fatalf("expected assembly MyWeb, got %q", got.Facts.DotNetAssembly)
		}
	})
	t.Run("java spring boot", func(t *testing.T) {
		got := inspect(t, map[string]string{"pom.xml": `<project><artifactId>spring-boot-starter-web</artifactId></project>`})
		if !got.Facts.JavaSpringBoot {
			t.Fatalf("expected spring-boot to be detected")
		}
	})
}

func TestInspectRejectsNonDirectories(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(file); !errors.Is(err, ErrNotADirectory) {
		t.Fatalf("want ErrNotADirectory, got %v", err)
	}
}

// --- 计划 -------------------------------------------------------------------

func TestBuildPlanSubstitutesTheAllocatedPort(t *testing.T) {
	// 端口是协调器分配的，必须真的进到启动参数里 —— 既有实现把端口写死在
	// 命令里，于是"协调器以为在 8080、实际启动了别的端口"。
	got := inspect(t, map[string]string{"package.json": `{"scripts":{"start":"node ."}}`})
	preset, err := Lookup(got.Recommended)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(got.Root, preset, got.Facts, 41234)
	if err != nil {
		t.Fatal(err)
	}
	// 计划里**保留**占位符：端口在规划与启动之间可能被抢走而重新分配，
	// 写死旧端口会让重分配只能去改写字符串（会误伤任何含该数字的参数）。
	if !strings.Contains(strings.Join(plan.Run.Env, " "), PortPlaceholder) {
		t.Fatalf("the stored plan should keep the placeholder, got %v", plan.Run.Env)
	}

	rendered := Render(plan, 41234)
	if rendered.Port != 41234 {
		t.Fatalf("rendered port = %d", rendered.Port)
	}
	if !containsEnv(rendered.Run.Env, "PORT=41234") {
		t.Fatalf("the port must reach the application environment, got %v", rendered.Run.Env)
	}

	php := inspect(t, map[string]string{"index.php": "<?php"})
	phpPreset, _ := Lookup(php.Recommended)
	phpPlan, err := BuildPlan(php.Root, phpPreset, php.Facts, 41235)
	if err != nil {
		t.Fatal(err)
	}
	phpRendered := Render(phpPlan, 41235)
	if !strings.Contains(strings.Join(phpRendered.Run.Args, " "), "127.0.0.1:41235") {
		t.Fatalf("php -S must bind the allocated port, got %v", phpRendered.Run.Args)
	}
	if strings.Contains(strings.Join(phpRendered.Run.Args, " "), PortPlaceholder) {
		t.Fatalf("rendering must remove the placeholder: %v", phpRendered.Run.Args)
	}
	// 换一个端口重新渲染：这正是端口被抢走时发生的事。
	again := Render(phpPlan, 41236)
	if !strings.Contains(strings.Join(again.Run.Args, " "), "127.0.0.1:41236") {
		t.Fatalf("re-rendering must follow the new port, got %v", again.Run.Args)
	}
}

func TestBuildPlanDropsTheNodeBuildStepWhenThereIsNoBuildScript(t *testing.T) {
	got := inspect(t, map[string]string{"package.json": `{"scripts":{"start":"node ."}}`})
	preset, _ := Lookup(got.Recommended)
	plan, err := BuildPlan(got.Root, preset, got.Facts, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Build) != 0 {
		t.Fatalf("`npm run build` would fail without a build script: %v", plan.Build)
	}

	got2 := inspect(t, map[string]string{"package.json": `{"scripts":{"build":"tsc","start":"node ."}}`})
	plan2, err := BuildPlan(got2.Root, preset, got2.Facts, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan2.Build) != 1 {
		t.Fatalf("a build script should produce a build step: %v", plan2.Build)
	}
}

func TestBuildPlanPythonVariants(t *testing.T) {
	django := inspect(t, map[string]string{"manage.py": "x"})
	p, _ := Lookup(django.Recommended)
	plan, err := BuildPlan(django.Root, p, django.Facts, 8001)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(Render(plan, 8001).Run.Args, " "), "manage.py runserver") {
		t.Fatalf("django should run through manage.py, got %v", plan.Run.Args)
	}

	asgi := inspect(t, map[string]string{"app.py": "x", "requirements.txt": "uvicorn"})
	plan2, err := BuildPlan(asgi.Root, p, asgi.Facts, 8002)
	if err != nil {
		t.Fatal(err)
	}
	if plan2.Run.Executable != "python3" || !strings.Contains(strings.Join(plan2.Run.Args, " "), "uvicorn app:app") {
		t.Fatalf("expected uvicorn app:app, got %v %v", plan2.Run.Executable, plan2.Run.Args)
	}

	// 没有入口文件时**不猜**：猜出来的命令会让用户对着打不开的站点排查。
	empty := inspect(t, map[string]string{"requirements.txt": "requests"})
	if _, err := BuildPlan(empty.Root, p, empty.Facts, 8003); !errors.Is(err, ErrNotRunnable) {
		t.Fatalf("want ErrNotRunnable, got %v", err)
	}
}

func TestBuildPlanJavaRequiresSpringBoot(t *testing.T) {
	spring := inspect(t, map[string]string{"pom.xml": "<project>spring-boot</project>"})
	p, _ := Lookup(spring.Recommended)
	plan, err := BuildPlan(spring.Root, p, spring.Facts, 9000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(Render(plan, 9000).Run.Args, " "), "--server.port=9000") {
		t.Fatalf("spring boot must get the allocated port, got %v", plan.Run.Args)
	}

	// 非 Spring Boot：给不出可信的启动命令，引导到自定义服务器。
	plain := inspect(t, map[string]string{"pom.xml": "<project/>"})
	if _, err := BuildPlan(plain.Root, p, plain.Facts, 9001); !errors.Is(err, ErrNotRunnable) {
		t.Fatalf("want ErrNotRunnable for a non-Spring-Boot project, got %v", err)
	}

	// 即便 target/ 下已经有一个 jar 也不能拿它当运行目标：那可能是上一次
	// 构建的残留，而"跑起来的是旧版本"是最难发现的故障之一。
	stale := writeFiles(t, map[string]string{"pom.xml": "<project/>", "target/app.jar": "jar"})
	inspected, err := Inspect(stale)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPlan(stale, p, inspected.Facts, 9002); !errors.Is(err, ErrNotRunnable) {
		t.Fatalf("a pre-existing jar must not be treated as this build's output, got %v", err)
	}
}

func TestBuildPlanDotNetSetsTheListeningURL(t *testing.T) {
	got := inspect(t, map[string]string{"Web.csproj": "<Project/>"})
	p, _ := Lookup(got.Recommended)
	plan, err := BuildPlan(got.Root, p, got.Facts, 5100)
	if err != nil {
		t.Fatal(err)
	}
	if !containsEnv(Render(plan, 5100).Run.Env, "ASPNETCORE_URLS=http://127.0.0.1:5100") {
		t.Fatalf(".NET reads its URL from the environment, got %v", Render(plan, 5100).Run.Env)
	}
	if !strings.Contains(strings.Join(plan.Run.Args, " "), "Web.dll") {
		t.Fatalf("expected the built assembly name, got %v", plan.Run.Args)
	}
}

func TestBuildPlanRejectsOutOfRangePort(t *testing.T) {
	got := inspect(t, map[string]string{"index.html": "x"})
	p, _ := Lookup(got.Recommended)
	if _, err := BuildPlan(got.Root, p, got.Facts, 0); err == nil {
		t.Fatalf("port 0 must be rejected")
	}
	if _, err := BuildPlan(got.Root, p, got.Facts, 70000); err == nil {
		t.Fatalf("port 70000 must be rejected")
	}
}

// --- helpers ----------------------------------------------------------------

func presetIDs(list []Preset) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, p.ID)
	}
	return out
}

func evidenceFiles(result Inspection) string {
	parts := make([]string, 0, len(result.Evidence))
	for _, e := range result.Evidence {
		parts = append(parts, e.File)
	}
	return strings.Join(parts, ",")
}

func containsEnv(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// Docker Compose 预设必须真的产出一个能执行的计划。
//
// 它此前只是一个"能识别但跑不起来"的占位条目：有探测器、没有命令，
// 于是用户在向导里选中它、点部署，然后在最后一步失败。
func TestDockerComposePresetBuildsARunnablePlan(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services:\n  web:\n    image: nginx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preset, err := Lookup("docker-compose")
	if err != nil {
		t.Fatal(err)
	}
	if !preset.DockerOnly || preset.Kind != KindDocker {
		t.Fatalf("unexpected preset: %#v", preset)
	}

	inspected, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Recommended != "docker-compose" {
		t.Fatalf("a compose file should be recognised, got %q", inspected.Recommended)
	}

	plan, err := BuildPlan(root, preset, inspected.Facts, 8080)
	if err != nil {
		t.Fatalf("the docker preset must be runnable: %v", err)
	}
	if plan.Run.Executable != "docker" {
		t.Fatalf("run executable = %q", plan.Run.Executable)
	}
	if strings.Join(plan.Run.Args, " ") != "compose up" {
		t.Fatalf("run args = %v", plan.Run.Args)
	}
	// 前台运行：容器日志因此会流进应用日志。
	// 命令里不能出现 -d —— 那会让 compose 立刻退出，内核就此认为服务停了。
	for _, arg := range plan.Run.Args {
		if arg == "-d" || arg == "--detach" {
			t.Fatalf("compose must run in the foreground so its logs reach the app log: %v", plan.Run.Args)
		}
	}
}

// 目录里没有 compose 文件时，Docker 预设不该被认出来。
func TestDockerPresetIsNotRecommendedWithoutAComposeFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<h1>hi</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspected, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Recommended == "docker-compose" {
		t.Fatalf("no compose file was present")
	}
}

// 目录里**有** compose 文件时不能悄悄改推荐别的：那会让用户在一个
// 本该用容器的项目上跑别的东西。
func TestComposeFileWinsOverOtherSignals(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"compose.yaml": "services:\n  web:\n    image: nginx\n",
		"package.json": `{"scripts":{"start":"node ."}}`,
		"index.html":   "<h1>hi</h1>",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inspected, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Recommended != "docker-compose" {
		t.Fatalf("a compose file is the most specific signal, got %q", inspected.Recommended)
	}
	// 但也应当提醒用户"还认出了别的技术栈"。
	if len(inspected.Warnings) == 0 {
		t.Fatalf("multiple stacks were present, the user should be told")
	}
}

// 每个会起进程的预设都必须把**分配到的端口**告诉应用。
//
// 否则应用会去监听它自己默认的端口，而内核在分配到的端口上等它 ——
// 症状是"部署超时"，离真正原因（预设少写了一个环境变量）很远。
// 这个测试是结构性的：新增预设时忘了传端口，它会直接失败。
func TestEveryRunnablePresetConveysThePort(t *testing.T) {
	for _, preset := range WebsitePresets() {
		if preset.Kind == KindNone {
			continue // 静态站点由内核自己托管，没有进程。
		}
		if preset.ID == "docker-compose" {
			// 容器的端口由 compose 文件里的映射决定，内核分配的那个必须被
			// 发布出来 —— 这一点在预设说明里写明，无法在计划里断言。
			continue
		}
		run := preset.Run
		conveyed := len(run.Env) > 0 || strings.Contains(strings.Join(run.Args, " "), PortPlaceholder)
		if !conveyed {
			t.Errorf("preset %s: the run step never tells the app which port to use (args %v, env %v)",
				preset.ID, run.Args, run.Env)
		}
	}
}

// 端口占位符必须真的到了应用手里，而不是只出现在计划里。
func TestRenderedPlanCarriesThePortToEveryPreset(t *testing.T) {
	for _, preset := range WebsitePresets() {
		if preset.Kind == KindNone || preset.ID == "docker-compose" {
			continue
		}
		if preset.Run.Executable == "" {
			t.Errorf("preset %s has no run command", preset.ID)
			continue
		}
		rendered := RenderStep(preset.Run, 45678)
		joined := strings.Join(rendered.Args, " ") + " " + strings.Join(rendered.Env, " ")
		if !strings.Contains(joined, "45678") {
			t.Errorf("preset %s: the allocated port never reaches the app (args %v, env %v)",
				preset.ID, rendered.Args, rendered.Env)
		}
		if strings.Contains(joined, PortPlaceholder) {
			t.Errorf("preset %s: an unrendered placeholder would be passed verbatim: %v", preset.ID, joined)
		}
	}
}
