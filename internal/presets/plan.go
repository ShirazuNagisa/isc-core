package presets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Plan 是一份可直接执行的构建与启动方案。
//
// 它是"预设 + 这份源码的事实 + 分配到的端口"的产物：预设给出骨架，
// 事实决定去掉哪一步（没有 build 脚本就没有构建步骤）、换成哪一步
// （Django 用 manage.py runserver），端口替换掉占位符。
type Plan struct {
	PresetID   string
	Kind       Kind
	Root       string
	Port       int
	Install    []Step
	Build      []Step
	Run        Step
	HealthPath string
	// Warnings 是"能继续，但用户应当知道"的提示。
	Warnings []Message
}

// ErrNotRunnable 表示按识别到的事实给不出可信的启动命令。
//
// 这种情况下**不猜**：猜出来的命令会让用户对着一个"部署成功但打不开"
// 的站点排查。返回错误并引导到自定义服务器，比猜一个更诚实。
var ErrNotRunnable = errors.New("no runnable entry point could be determined")

// BuildPlan 把预设与识别事实合成为可执行方案。
func BuildPlan(root string, preset Preset, facts Facts, port int) (Plan, error) {
	if port < 1 || port > 65535 {
		return Plan{}, fmt.Errorf("port %d is out of range", port)
	}
	// 计划里**保留** {port} 占位符，到启动时才渲染。
	//
	// 这不是偷懒：端口在"规划"与"真正启动"之间可能被别的进程抢走，
	// 那时协调器会重新分配一个端口。若计划里已经写死了旧端口，重新分配
	// 就得去改写参数里的字符串 —— 而"把 8080 替换成 8081"会误伤任何
	// 恰好含这个数字的参数。保留占位符让重分配只是换一个数字。
	plan := Plan{
		PresetID:   preset.ID,
		Kind:       preset.Kind,
		Root:       root,
		Port:       port,
		Install:    preset.Install,
		Build:      preset.Build,
		Run:        preset.Run,
		HealthPath: preset.HealthPath,
	}

	switch preset.ID {
	case "node-auto":
		if !facts.NodeBuildScript {
			// 没有 build 脚本时跑 `npm run build` 会失败，而失败信息
			// （"Missing script: build"）与"站点起不来"看起来毫不相干。
			plan.Build = nil
		}
		if !facts.NodeStartScript {
			entry := facts.NodeMain
			if entry == "" {
				entry = "index.js"
			}
			plan.Run = Step{Executable: "node", Args: []string{entry}, Env: portEnv()}
			plan.Warnings = append(plan.Warnings, msg("preset.warn.node_no_start_script", entry))
		} else {
			plan.Run.Env = portEnv()
		}

	case "python-auto":
		plan.Install = nil
		if fileExists(filepath.Join(root, "requirements.txt")) {
			plan.Install = preset.Install
		}
		switch facts.PythonEntry {
		case PythonDjango:
			plan.Run = Step{Executable: "python3", Args: []string{"manage.py", "runserver", "127.0.0.1:" + PortPlaceholder}, Env: portEnv()}
		case PythonASGI:
			module := pythonASGIModule(root)
			plan.Run = Step{
				Executable: "python3",
				Args:       []string{"-m", "uvicorn", module + ":app", "--host", "127.0.0.1", "--port", PortPlaceholder},
				Env:        portEnv(),
			}
		case PythonWSGI:
			// WSGI 应用没有统一的启动约定（gunicorn/flask run/uwsgi 各有各的
			// 调用方式），因此用最保守的一条：直接跑入口文件，并把端口通过
			// 环境变量告诉它。
			plan.Run = Step{Executable: "python3", Args: []string{pythonEntryFile(root)}, Env: portEnv()}
			plan.Warnings = append(plan.Warnings, msg("preset.warn.python_entry_file"))
		default:
			return Plan{}, fmt.Errorf("%w: no Python entry point (app.py, main.py or manage.py)", ErrNotRunnable)
		}

	case "php-composer":
		if !fileExists(filepath.Join(root, "composer.json")) {
			plan.Install = nil
		}
		webRoot := facts.PHPWebRoot
		if webRoot == "" {
			webRoot = "."
		}
		plan.Run = Step{Executable: "php", Args: []string{"-S", "127.0.0.1:" + PortPlaceholder, "-t", webRoot}}

	case "java-build":
		if !facts.JavaSpringBoot {
			// 非 Spring Boot 的 Java 项目给不出可信的启动命令：jar 的名字与
			// 主类要等构建之后才知道，而 war 还需要一个 servlet 容器。
			//
			// 刻意**不去猜** target/ 下已有的 jar：那可能是上一次构建的残留，
			// 而"跑起来的是旧版本"是最难被发现的一类故障（界面显示部署成功，
			// 用户看到的是旧站点）。引导到自定义服务器，让用户自己写
			// `java -jar target/xxx.jar`。
			return Plan{}, fmt.Errorf("%w: the Java preset supports Spring Boot projects; for other layouts use the custom server option with an explicit start command", ErrNotRunnable)
		}
		// Spring Boot 不需要预先知道 jar 名，端口也能从参数传进去。
		plan.Run = Step{
			Executable: "mvn",
			Args:       []string{"-B", "spring-boot:run", "-Dspring-boot.run.arguments=--server.port=" + PortPlaceholder},
		}
		plan.Warnings = append(plan.Warnings, msg("preset.warn.java_spring_boot"))

	case "dotnet-web":
		assembly := facts.DotNetAssembly
		if assembly == "" {
			return Plan{}, fmt.Errorf("%w: no .csproj or .fsproj was found", ErrNotRunnable)
		}
		plan.Run = Step{
			Executable: "dotnet",
			Args:       []string{filepath.ToSlash(filepath.Join(".isc", "publish", assembly+".dll"))},
			Env:        []string{"ASPNETCORE_URLS=http://127.0.0.1:" + PortPlaceholder},
		}

	case "docker-compose":
		// 容器方案由 docker 层单独处理，这里不生成进程步骤。
		plan.Run = Step{}
	}

	return plan, nil
}

// portEnv 把端口同时以 PORT 暴露（占位符形式，渲染时替换）。
//
// 很多框架/示例工程读 PORT 环境变量。它不冲突：命令行参数优先，
// 而没有读参数的框架至少还能从 PORT 拿到正确的值。
func portEnv() []string {
	return []string{"PORT=" + PortPlaceholder, "HOST=127.0.0.1"}
}

// Render 把计划里的端口占位符替换成实际端口。
//
// 执行前调用：参数、环境变量、以及 Plan.Port 一并渲染。计划本身保持
// 未渲染状态存库，这样重新分配端口只是换一个数字（见 BuildPlan 的说明）。
func Render(plan Plan, port int) Plan {
	plan.Port = port
	plan.Run = RenderStep(plan.Run, port)
	plan.Install = renderSteps(plan.Install, port)
	plan.Build = renderSteps(plan.Build, port)
	return plan
}

// RenderStep 渲染单个步骤。
func RenderStep(step Step, port int) Step {
	step.Args = renderArgs(step.Args, port)
	step.Env = renderArgs(step.Env, port)
	return step
}

func renderSteps(steps []Step, port int) []Step {
	out := make([]Step, 0, len(steps))
	for _, step := range steps {
		out = append(out, RenderStep(step, port))
	}
	return out
}

func renderArgs(values []string, port int) []string {
	if len(values) == 0 {
		return values
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = strings.ReplaceAll(value, PortPlaceholder, strconv.Itoa(port))
	}
	return out
}

func pythonEntryFile(root string) string {
	for _, name := range []string{"main.py", "app.py"} {
		if fileExists(filepath.Join(root, name)) {
			return name
		}
	}
	return "main.py"
}

func pythonASGIModule(root string) string {
	for _, name := range []string{"main", "app", "asgi", "server"} {
		if fileExists(filepath.Join(root, name+".py")) {
			return name
		}
	}
	return "main"
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
