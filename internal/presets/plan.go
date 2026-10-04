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
	plan := Plan{
		PresetID:   preset.ID,
		Kind:       preset.Kind,
		Root:       root,
		Port:       port,
		Install:    substitutePort(preset.Install, port),
		Build:      substitutePort(preset.Build, port),
		Run:        substitutePortStep(preset.Run, port),
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
			plan.Run = Step{Executable: "node", Args: []string{entry}, Env: portEnv(port)}
			plan.Warnings = append(plan.Warnings, msg("preset.warn.node_no_start_script", entry))
		} else {
			plan.Run.Env = portEnv(port)
		}

	case "python-auto":
		plan.Install = nil
		if fileExists(filepath.Join(root, "requirements.txt")) {
			plan.Install = substitutePort(preset.Install, port)
		}
		switch facts.PythonEntry {
		case PythonDjango:
			plan.Run = Step{Executable: "python3", Args: []string{"manage.py", "runserver", fmt.Sprintf("127.0.0.1:%d", port)}, Env: portEnv(port)}
		case PythonASGI:
			module := pythonASGIModule(root)
			app := "app"
			plan.Run = Step{
				Executable: "python3",
				Args:       []string{"-m", "uvicorn", module + ":" + app, "--host", "127.0.0.1", "--port", strconv.Itoa(port)},
				Env:        portEnv(port),
			}
		case PythonWSGI:
			// WSGI 应用没有统一的启动约定（gunicorn/flask run/uwsgi 各有各的
			// 调用方式），因此用最保守的一条：直接跑入口文件，并把端口通过
			// 环境变量告诉它。
			plan.Run = Step{Executable: "python3", Args: []string{pythonEntryFile(root)}, Env: portEnv(port)}
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
		plan.Run = Step{Executable: "php", Args: []string{"-S", fmt.Sprintf("127.0.0.1:%d", port), "-t", webRoot}}

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
			Args:       []string{"-B", "spring-boot:run", fmt.Sprintf("-Dspring-boot.run.arguments=--server.port=%d", port)},
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
			Env:        []string{fmt.Sprintf("ASPNETCORE_URLS=http://127.0.0.1:%d", port)},
		}

	case "docker-compose":
		// 容器方案由 docker 层单独处理，这里不生成进程步骤。
		plan.Run = Step{}
	}

	return plan, nil
}

// portEnv 把端口同时以 PORT 暴露。
//
// 很多框架/示例工程读 PORT 环境变量。它不冲突：命令行参数优先，
// 而没有读参数的框架至少还能从 PORT 拿到正确的值。
func portEnv(port int) []string {
	return []string{"PORT=" + strconv.Itoa(port), "HOST=127.0.0.1"}
}

// substitutePort 替换步骤参数里的端口占位符。
func substitutePort(steps []Step, port int) []Step {
	out := make([]Step, 0, len(steps))
	for _, step := range steps {
		out = append(out, substitutePortStep(step, port))
	}
	return out
}

func substitutePortStep(step Step, port int) Step {
	step.Args = substituteArgs(step.Args, port)
	return step
}

func substituteArgs(args []string, port int) []string {
	if len(args) == 0 {
		return args
	}
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = strings.ReplaceAll(arg, PortPlaceholder, strconv.Itoa(port))
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
