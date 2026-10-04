package presets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	seen := map[string]bool{}
	for _, preset := range All() {
		if err := Validate(preset); err != nil {
			t.Errorf("preset %s is invalid: %v", preset.ID, err)
		}
		if seen[preset.ID] {
			t.Errorf("duplicate preset id %s", preset.ID)
		}
		seen[preset.ID] = true
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
	if plan.Port != 41234 {
		t.Fatalf("plan port = %d", plan.Port)
	}
	if !containsEnv(plan.Run.Env, "PORT=41234") {
		t.Fatalf("the port must reach the application environment, got %v", plan.Run.Env)
	}

	php := inspect(t, map[string]string{"index.php": "<?php"})
	phpPreset, _ := Lookup(php.Recommended)
	phpPlan, err := BuildPlan(php.Root, phpPreset, php.Facts, 41235)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(phpPlan.Run.Args, " "), "127.0.0.1:41235") {
		t.Fatalf("php -S must bind the allocated port, got %v", phpPlan.Run.Args)
	}
	if strings.Contains(strings.Join(phpPlan.Run.Args, " "), PortPlaceholder) {
		t.Fatalf("the placeholder must be substituted: %v", phpPlan.Run.Args)
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
	if !strings.Contains(strings.Join(plan.Run.Args, " "), "manage.py runserver") {
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
	if !strings.Contains(strings.Join(plan.Run.Args, " "), "--server.port=9000") {
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
	if !containsEnv(plan.Run.Env, "ASPNETCORE_URLS=http://127.0.0.1:5100") {
		t.Fatalf(".NET reads its URL from the environment, got %v", plan.Run.Env)
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
