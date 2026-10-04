package presets

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 扫描边界。
//
// 源码目录是用户给的，可能是一个巨大的 monorepo。识别只需要找到"根部的
// 技术栈标志文件"，因此深度与条目数都必须有界 —— 否则"选个目录"会变成
// 一次全盘遍历，界面上表现为卡住。
const (
	maxScanDepth   = 3
	maxScanEntries = 2000
	// maxProbeBytes 限制读取标志文件内容的字节数。
	//
	// 读内容只为了拿到少量事实（package.json 的 scripts、pom.xml 里有没有
	// spring-boot）。一个几百 MB 的 JSON 不应该被整份读进内存。
	maxProbeBytes = 256 << 10
)

// ignoredDirs 是识别时跳过的目录。
//
// 既包括版本控制与依赖目录（遍历它们只会浪费时间并撞上几万个文件），
// 也包括明确不该进入结果的敏感目录与产物目录。
var ignoredDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true,
	"node_modules": true, "vendor": true, "target": true,
	"dist": true, "build": true, "bin": true, "obj": true,
	".env": true, ".ssh": true, ".isc": true,
	"__pycache__": true, ".venv": true, "venv": true,
}

// Evidence 是一条识别证据。
type Evidence struct {
	File string `json:"file"`
	// Signal 说明这个文件意味着什么（已本地化）。
	Signal string `json:"signal"`
	// Confidence 是 0..1 的置信度。
	Confidence float64 `json:"confidence"`
}

// PythonEntry 描述 Python 项目的入口形态。
type PythonEntry string

const (
	PythonNone       PythonEntry = ""
	PythonDjango     PythonEntry = "django"
	PythonASGI       PythonEntry = "asgi"
	PythonWSGI       PythonEntry = "wsgi"
	PythonStaticOnly PythonEntry = "static"
)

// Facts 是识别过程中读到的、会影响构建方案的源码事实。
//
// 单靠"存在 package.json"不足以决定怎么构建：有没有 build 脚本、
// 是 Django 还是普通脚本，会导出完全不同的启动命令。
type Facts struct {
	// NodeBuildScript / NodeStartScript 来自 package.json 的 scripts。
	NodeBuildScript bool
	NodeStartScript bool
	// NodeMain 是 package.json 的 main 字段。
	NodeMain string
	// PythonEntry 是 Python 项目的入口形态。
	PythonEntry PythonEntry
	// PythonUseUvicorn / PythonUseGunicorn 表示依赖里出现了对应的服务器。
	PythonUseUvicorn  bool
	PythonUseGunicorn bool
	// PHPWebRoot 是 PHP 的站点根（"." 或 "public"）。
	PHPWebRoot string
	// JavaSpringBoot 表示 pom.xml 看起来是 Spring Boot 项目。
	JavaSpringBoot bool
	// DotNetAssembly 是 .NET 的主程序集名（csproj/fsproj 文件名去掉扩展名）。
	DotNetAssembly string
	// HasDockerfile / HasCompose 表示存在容器化描述。
	HasDockerfile bool
	HasCompose    bool
}

// Inspection 是一次只读识别的结果。
type Inspection struct {
	Root        string     `json:"root"`
	Evidence    []Evidence `json:"evidence"`
	Candidates  []Preset   `json:"candidates"`
	Recommended string     `json:"recommended_preset_id"`
	Facts       Facts      `json:"-"`
	Warnings    []Message  `json:"warnings"`
}

// ErrNotADirectory 表示给的路径不是一个可读目录。
var ErrNotADirectory = errors.New("source path is not a directory")

// Inspect 只读识别一份源码目录。
//
// # 绝不执行源码提供的任何东西
//
// 只做目录列举与少量标志文件的内容读取（有上限）。不执行脚本、不解析
// 依赖、不联网、不写任何文件。这是既有的安全约定，v0.2.0 不变。
func Inspect(root string) (Inspection, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Inspection{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return Inspection{}, err
	}
	if !info.IsDir() {
		return Inspection{}, fmt.Errorf("%w: %s", ErrNotADirectory, root)
	}

	files, err := collectFiles(resolved)
	if err != nil {
		return Inspection{}, err
	}

	facts := gatherFacts(resolved, files)
	evidence, scores := matchPresets(files, facts)
	candidates := rankCandidates(scores)

	result := Inspection{
		Root:       resolved,
		Evidence:   evidence,
		Candidates: candidates,
		Facts:      facts,
	}
	if len(candidates) > 0 {
		result.Recommended = candidates[0].ID
	} else {
		result.Recommended = CustomPresetID
		result.Warnings = append(result.Warnings, msg("preset.warn.no_stack"))
	}
	if facts.HasCompose || facts.HasDockerfile {
		result.Warnings = append(result.Warnings, msg("preset.warn.container_found"))
	}
	if len(candidates) > 1 {
		result.Warnings = append(result.Warnings, msg("preset.warn.multiple_stacks", candidates[0].ID))
	}
	return result, nil
}

// collectFiles 列举相对路径，跳过忽略目录，并受深度与条目数限制。
func collectFiles(root string) ([]string, error) {
	var files []string
	rootDepth := strings.Count(root, string(os.PathSeparator))
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// 权限不足的目录跳过即可：识别是尽力而为，不该因为一个
			// 不可读的子目录就让整次扫描失败。
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if ignoredDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			if strings.Count(path, string(os.PathSeparator))-rootDepth >= maxScanDepth {
				return fs.SkipDir
			}
			return nil
		}
		if len(files) >= maxScanEntries {
			return fs.SkipAll
		}
		// 点文件一律不进清单。
		//
		// 识别本身只把"命中探测器的文件"变成证据，因此 `.env` 本来也不会
		// 出现在结果里；但把它挡在入口处，"扫描结果里不会有敏感文件"就成
		// 了一条**结构性**保证，而不是依赖匹配规则恰好没命中。
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// matchPresets 用文件清单与事实给每个预设打分。
func matchPresets(files []string, facts Facts) ([]Evidence, map[string]float64) {
	evidence := make([]Evidence, 0)
	scores := make(map[string]float64)

	basenames := make(map[string][]string, len(files))
	suffixes := make(map[string][]string)
	for _, f := range files {
		base := filepath.Base(f)
		basenames[base] = append(basenames[base], f)
		ext := strings.ToLower(filepath.Ext(base))
		if ext != "" {
			suffixes[ext] = append(suffixes[ext], f)
		}
	}

	for _, preset := range WebsitePresets() {
		for _, detector := range preset.DetectorFiles {
			matches, ok := basenames[detector]
			if !ok {
				continue
			}
			score := detectorConfidence(detector)
			for _, m := range matches {
				evidence = append(evidence, Evidence{File: m, Signal: detectorSignal(detector), Confidence: score})
			}
			if score > scores[preset.ID] {
				scores[preset.ID] = score
			}
		}
		for _, suffix := range preset.DetectorSuffixes {
			matches, ok := suffixes[strings.ToLower(suffix)]
			if !ok {
				continue
			}
			score := 0.85
			if suffix == ".html" {
				// 任意 .html 是最弱的信号：很多项目里都有模板。
				score = 0.5
			}
			for _, m := range matches {
				evidence = append(evidence, Evidence{File: m, Signal: detectorSignal(suffix), Confidence: score})
			}
			if score > scores[preset.ID] {
				scores[preset.ID] = score
			}
		}
	}

	// 容器描述是**最强**信号：它明确表达了"请用容器跑"，而不是"这个项目
	// 用什么写的"。用 maxFloat 而不是直接赋值 —— 直接赋值会把这个信号
	// 压到比语言级探测器还低，于是"有 compose 文件、也有 package.json"
	// 的项目会被推荐成 node 直跑，而用户写的 compose 文件被完全忽略。
	if facts.HasCompose {
		scores["docker-compose"] = maxFloat(scores["docker-compose"], 0.99)
	}
	// 只有 Dockerfile 时没有可用的启动命令（镜像名与容器内端口猜不出来），
	// 因此不产生候选，只在上面的 container_found 提醒里出现。

	// package.json 有 start 脚本时提高 Node 的置信度：它明确可启动。
	if facts.NodeStartScript {
		scores["node-auto"] = maxFloat(scores["node-auto"], 0.96)
	}
	// Django / ASGI 入口让 Python 更明确。
	switch facts.PythonEntry {
	case PythonDjango:
		scores["python-auto"] = maxFloat(scores["python-auto"], 0.95)
	case PythonASGI, PythonWSGI:
		scores["python-auto"] = maxFloat(scores["python-auto"], 0.92)
	}
	if facts.PHPWebRoot == "public" {
		scores["php-composer"] = maxFloat(scores["php-composer"], 0.95)
	}

	return dedupeEvidence(evidence), scores
}

// dedupeEvidence 让每个文件只出现一次，保留最高的置信度。
//
// 同一个文件可能同时命中精确名与后缀（index.html 既在 DetectorFiles 里，
// 又是 .html 后缀）。重复条目会让界面把一条证据显示两遍，也会让
// "证据条数"看起来比实际多 —— 而证据条数是用户判断"识别得准不准"的依据。
func dedupeEvidence(evidence []Evidence) []Evidence {
	best := make(map[string]Evidence, len(evidence))
	order := make([]string, 0, len(evidence))
	for _, item := range evidence {
		prev, seen := best[item.File]
		if !seen {
			order = append(order, item.File)
			best[item.File] = item
			continue
		}
		if item.Confidence > prev.Confidence {
			best[item.File] = item
		}
	}
	out := make([]Evidence, 0, len(order))
	for _, file := range order {
		out = append(out, best[file])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].File < out[j].File
	})
	return out
}

// rankCandidates 按分数排候选；同分时保持目录顺序（越靠前越常见）。
func rankCandidates(scores map[string]float64) []Preset {
	catalog := WebsitePresets()
	order := make(map[string]int, len(catalog))
	for i, p := range catalog {
		order[p.ID] = i
	}
	type scored struct {
		preset Preset
		score  float64
		rank   int
	}
	list := make([]scored, 0, len(scores))
	for id, score := range scores {
		preset, err := Lookup(id)
		if err != nil {
			continue
		}
		list = append(list, scored{preset: preset, score: score, rank: order[id]})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].rank < list[j].rank
	})
	out := make([]Preset, 0, len(list))
	for _, item := range list {
		out = append(out, item.preset)
	}
	return out
}

func detectorConfidence(name string) float64 {
	switch name {
	case "compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml":
		// compose 文件排在语言级探测器之上：它不是"这个项目用什么写的"，
		// 而是用户**明确写下的**"这个东西该怎么跑"。一个目录里同时有
		// package.json 与 compose.yaml 时，按 compose 起来才是用户的本意。
		return 0.99
	case "package.json", "go.mod", "pom.xml", "composer.json", "requirements.txt":
		return 0.95
	case "pyproject.toml", "build.gradle", "build.gradle.kts":
		return 0.9
	case "index.php", "manage.py":
		return 0.9
	case "app.py", "main.py":
		return 0.75
	case "index.html":
		return 0.7
	default:
		return 0.8
	}
}

func detectorSignal(name string) string {
	switch {
	case name == "package.json":
		return "Node.js package manifest"
	case name == "go.mod":
		return "Go module"
	case name == "pom.xml" || strings.HasPrefix(name, "build.gradle"):
		return "Java build manifest"
	case strings.HasPrefix(name, "compose") || strings.HasPrefix(name, "docker-compose"):
		return "Docker Compose file"
	case name == "Dockerfile":
		return "Dockerfile"
	case name == "composer.json":
		return "PHP dependency manifest"
	case name == "requirements.txt" || name == "pyproject.toml":
		return "Python dependency manifest"
	case name == "manage.py":
		return "Django management script"
	case name == "app.py" || name == "main.py":
		return "Python entry point"
	case name == "index.php":
		return "PHP entry point"
	case name == "index.html":
		return "Static entry point"
	case strings.HasPrefix(name, "."):
		return "Project file"
	default:
		return "Project marker"
	}
}

// gatherFacts 读取少量标志文件，得出会影响构建方案的事实。
//
// 读失败一律当作"没有这条事实"，不中断识别：识别是尽力而为，
// 而一个读不了的 package.json 不应该让整个流程失败。
func gatherFacts(root string, files []string) Facts {
	var facts Facts
	byBase := make(map[string]string, len(files))
	for _, f := range files {
		base := filepath.Base(f)
		if _, exists := byBase[base]; !exists {
			byBase[base] = f
		}
	}

	if rel, ok := byBase["package.json"]; ok {
		facts.NodeBuildScript, facts.NodeStartScript, facts.NodeMain = readPackageScripts(filepath.Join(root, rel))
	}
	if rel, ok := byBase["composer.json"]; ok {
		_ = rel // 存在即足够；依赖安装由 composer 自己决定装什么。
		facts.PHPWebRoot = phpWebRoot(root)
	} else if _, ok := byBase["index.php"]; ok {
		facts.PHPWebRoot = phpWebRoot(root)
	}
	if rel, ok := byBase["requirements.txt"]; ok {
		body := readProbe(filepath.Join(root, rel))
		facts.PythonUseUvicorn = strings.Contains(body, "uvicorn")
		facts.PythonUseGunicorn = strings.Contains(body, "gunicorn")
	}
	if rel, ok := byBase["pyproject.toml"]; ok {
		body := readProbe(filepath.Join(root, rel))
		facts.PythonUseUvicorn = facts.PythonUseUvicorn || strings.Contains(body, "uvicorn")
		facts.PythonUseGunicorn = facts.PythonUseGunicorn || strings.Contains(body, "gunicorn")
	}
	switch {
	case byBase["manage.py"] != "":
		facts.PythonEntry = PythonDjango
	case byBase["app.py"] != "":
		if facts.PythonUseUvicorn || facts.PythonUseGunicorn {
			facts.PythonEntry = PythonASGI
		} else {
			facts.PythonEntry = PythonWSGI
		}
	case byBase["main.py"] != "":
		if facts.PythonUseUvicorn || facts.PythonUseGunicorn {
			facts.PythonEntry = PythonASGI
		} else {
			facts.PythonEntry = PythonWSGI
		}
	case facts.PythonUseUvicorn || facts.PythonUseGunicorn:
		facts.PythonEntry = PythonASGI
	}
	if rel, ok := byBase["pom.xml"]; ok {
		facts.JavaSpringBoot = strings.Contains(readProbe(filepath.Join(root, rel)), "spring-boot")
	}
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f)) {
		case ".csproj", ".fsproj":
			if facts.DotNetAssembly == "" {
				facts.DotNetAssembly = strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
			}
		}
	}
	for base := range byBase {
		if base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile.") {
			facts.HasDockerfile = true
		}
		if strings.HasPrefix(base, "compose.") || strings.HasPrefix(base, "docker-compose.") {
			facts.HasCompose = true
		}
	}
	return facts
}

// phpWebRoot 判断 PHP 项目的站点根。
//
// 现代 PHP 框架把入口放在 public/ 下；把 docroot 指错会让站点直接 404，
// 而用户看到的现象是"部署成功但打开是空白"。
func phpWebRoot(root string) string {
	if info, err := os.Stat(filepath.Join(root, "public", "index.php")); err == nil && !info.IsDir() {
		return "public"
	}
	return "."
}

// readPackageScripts 只取需要的三个字段。
func readPackageScripts(path string) (hasBuild, hasStart bool, main string) {
	body := readProbe(path)
	if body == "" {
		return false, false, ""
	}
	var parsed struct {
		Scripts map[string]string `json:"scripts"`
		Main    string            `json:"main"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return false, false, ""
	}
	_, hasBuild = parsed.Scripts["build"]
	_, hasStart = parsed.Scripts["start"]
	return hasBuild, hasStart, parsed.Main
}

// readProbe 读取标志文件的内容片段，带硬上限。
func readProbe(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, maxProbeBytes)
	n, err := f.Read(buf)
	if n <= 0 {
		return ""
	}
	if err != nil && n == 0 {
		return ""
	}
	return string(buf[:n])
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
