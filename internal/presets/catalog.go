// Package presets 是"从一份源码目录到一套可执行的构建/启动方案"的领域层。
//
// 它由两部分组成，故意放在同一个包里：**预设目录**（每种技术栈该怎么装、
// 怎么构建、怎么启动）与**只读识别**（这份源码属于哪种技术栈）。两者必须
// 一起演进 —— 识别器给出的证据决定选中哪个预设，而预设的构建步骤取决于
// 源码里到底有什么（例如 package.json 里有没有 build 脚本）。
//
// # 命令一律是固定 argv
//
// 所有步骤都是"可执行文件 + 参数数组"，**不经 shell**（见 D30）。参数里
// 支持极少数占位符（{port}），在计划阶段替换。这让"预设覆盖不到"的场景
// 由 custom 预设显式承担，而不是靠字符串拼接去凑。
package presets

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/runtime"
)

// Kind 是预设所需的运行时类型。
//
// 它是 runtime.Kind 的别名而不是另立一套：运行时的定义只应有一处，
// 否则两边会各自演化出不同的取值。
type Kind = runtime.Kind

const (
	// KindNone 表示不需要外部运行时（静态站点由内核自己托管）。
	KindNone = runtime.KindNone
	// KindNode / KindPython / KindPHP / KindGo / KindJava / KindDotNet 由内核
	// 供给：优先用系统已装的解释器，缺失时下载固定版本的预编译发行版（D28）。
	KindNode   = runtime.KindNode
	KindPython = runtime.KindPython
	KindPHP    = runtime.KindPHP
	KindGo     = runtime.KindGo
	KindJava   = runtime.KindJava
	KindDotNet = runtime.KindDotNet
	// KindDocker 不由内核供给：Docker Desktop 必须由用户自己安装
	// （既有决定：内核不代装 Docker，只检测并引导）。
	KindDocker = runtime.KindDocker
)

// PortPlaceholder 会在计划阶段被替换为分配到的本地端口。
//
// 这一条是对既有实现的修正：Swift 侧把端口写死在命令里（`-m http.server 8080`），
// 而端口是协调器分配的元数据 —— 结果是"协调器以为在 8080，实际启动参数是别的"。
const PortPlaceholder = "{port}"

// ErrUnknownPreset 表示请求的预设 id 不在目录里。
var ErrUnknownPreset = errors.New("unknown preset")

// Message 是一条待本地化的用户可见消息。
//
// 本包只产出 key 与参数，不产出成品文案：语言由**看的人**决定，而识别与
// 计划是纯逻辑，不该依赖某个全局语言设置（也因此可以在测试里断言 key，
// 而不是断言某一种语言的措辞）。
type Message struct {
	Key  string
	Args []any
}

// Text 用默认语言渲染消息。
func (m Message) Text() string {
	if m.Key == "" {
		return ""
	}
	return i18n.T(m.Key, m.Args...)
}

// TextIn 用**请求自己的**语言目录渲染消息。
//
// API 层必须用这个：语言是跟着请求走的（见 i18n.FromContext），
// 用默认目录会让英文界面上出现中文提示。
func (m Message) TextIn(cat *i18n.Catalog) string {
	if m.Key == "" || cat == nil {
		return m.Text()
	}
	return cat.T(m.Key, m.Args...)
}

// Messages 把一个消息切片渲染成文本。
func Messages(list []Message) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, item.Text())
	}
	return out
}

// MessagesIn 用给定目录渲染消息切片。
func MessagesIn(list []Message, cat *i18n.Catalog) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, item.TextIn(cat))
	}
	return out
}

func msg(key string, args ...any) Message { return Message{Key: key, Args: args} }

// Step 是一次构建步骤。
type Step struct {
	// Executable 是可执行文件名。
	//
	// 不含路径分隔符时在运行时根与 PATH 中查找；含分隔符时视为相对源码根的路径
	// （用于运行自己构建出来的产物，例如 go 编译出的二进制）。
	Executable string
	// Args 支持 {port} 占位符。
	Args []string
	// Dir 是相对源码根的工作目录；空表示源码根本身。
	Dir string
	// Env 是该步骤额外需要的环境变量（KEY=VALUE）。
	//
	// 业务进程**不继承内核环境**（见 D30），因此像 PORT / ASPNETCORE_URLS
	// 这种"框架靠环境变量读端口"的约定必须显式列出，否则应用会去监听
	// 它自己的默认端口，而协调器以为它在分配的端口上。
	Env []string
}

// Preset 描述一种建站方式。
type Preset struct {
	ID      string
	Version string
	Title   string
	Kind    Kind
	// MinVersion 是运行时最低版本；空表示不限。
	MinVersion string
	// DefaultPort 是建议端口；实际端口由部署时分配，并替换 {port}。
	DefaultPort int
	// DetectorFiles 是精确文件名的识别依据。
	DetectorFiles []string
	// DetectorSuffixes 是后缀识别依据（例如 .csproj）。
	DetectorSuffixes []string
	// DockerOnly 表示需要本机已存在 Docker（内核不代装）。
	DockerOnly bool
	// Install / Build 是固定的构建步骤，可为空。
	Install []Step
	Build   []Step
	// Run 是启动步骤；静态站点为空。
	Run Step
	// HealthPath 是启动后用于 HTTP 健康检查的路径；空表示只做端口探测。
	HealthPath string
	// NoteKey 是给用户看的补充说明的 i18n key。
	//
	// 存 key 而不是成品文案：文案的语言取决于**看的人**，而预设目录是
	// 包级常量，在它上面定语言会把首次调用的语言永久固化。
	NoteKey string
}

// WebsitePresets 是 v0.2.0 支持的建站预设。
//
// 顺序即"识别置信度相同时的呈现顺序"：越靠前越常见。
func WebsitePresets() []Preset {
	return []Preset{
		{
			ID: "static-html", Version: "1", Title: "Static HTML", Kind: KindNone,
			DefaultPort: 8080, DetectorFiles: []string{"index.html"}, DetectorSuffixes: []string{".html"},
			HealthPath: "/",
		},
		{
			ID: "node-auto", Version: "1", Title: "Node.js", Kind: KindNode, MinVersion: "18.0.0",
			DefaultPort: 3000, DetectorFiles: []string{"package.json"},
			Install:    []Step{{Executable: "npm", Args: []string{"install"}}},
			Build:      []Step{{Executable: "npm", Args: []string{"run", "build"}}},
			Run:        Step{Executable: "npm", Args: []string{"start"}},
			HealthPath: "/",
		},
		{
			ID: "python-auto", Version: "1", Title: "Python", Kind: KindPython, MinVersion: "3.10.0",
			DefaultPort: 8000, DetectorFiles: []string{"requirements.txt", "pyproject.toml", "app.py", "main.py", "manage.py"},
			Install:    []Step{{Executable: "python3", Args: []string{"-m", "pip", "install", "-r", "requirements.txt"}}},
			Run:        Step{Executable: "python3", Args: []string{"-m", "http.server", PortPlaceholder, "--bind", "127.0.0.1"}},
			HealthPath: "/",
		},
		{
			ID: "php-composer", Version: "1", Title: "PHP", Kind: KindPHP, MinVersion: "8.0.0",
			DefaultPort: 8080, DetectorFiles: []string{"index.php", "composer.json"},
			Install:    []Step{{Executable: "composer", Args: []string{"install", "--no-interaction", "--no-progress", "--no-scripts"}}},
			Run:        Step{Executable: "php", Args: []string{"-S", "127.0.0.1:" + PortPlaceholder, "-t", "."}},
			HealthPath: "/",
		},
		{
			ID: "go-module", Version: "1", Title: "Go", Kind: KindGo, MinVersion: "1.21.0",
			DefaultPort: 8080, DetectorFiles: []string{"go.mod"},
			Install:    []Step{{Executable: "go", Args: []string{"mod", "download"}}},
			Build:      []Step{{Executable: "go", Args: []string{"build", "-o", ".isc/bin/app", "."}}},
			Run:        Step{Executable: ".isc/bin/app"},
			HealthPath: "/",
		},
		{
			ID: "java-build", Version: "1", Title: "Java", Kind: KindJava, MinVersion: "17.0.0",
			DefaultPort: 8080, DetectorFiles: []string{"pom.xml", "build.gradle", "build.gradle.kts"},
			Install: []Step{{Executable: "mvn", Args: []string{"-B", "dependency:go-offline"}}},
			Build:   []Step{{Executable: "mvn", Args: []string{"-B", "package", "-DskipTests"}}},
			// 具体 jar 在计划阶段确定（target/*.jar）。
			Run:        Step{Executable: "java", Args: []string{"-jar", PortPlaceholder}},
			HealthPath: "/",
		},
		{
			ID: "dotnet-web", Version: "1", Title: ".NET", Kind: KindDotNet, MinVersion: "8.0.0",
			DefaultPort: 5000, DetectorSuffixes: []string{".csproj", ".fsproj", ".sln"},
			Install: []Step{{Executable: "dotnet", Args: []string{"restore"}}},
			Build:   []Step{{Executable: "dotnet", Args: []string{"publish", "-c", "Release", "-o", ".isc/publish"}}},
			// 具体 dll 在计划阶段确定；端口通过环境变量与参数一起给出。
			Run:        Step{Executable: "dotnet", Args: []string{".isc/publish", "--urls", "http://127.0.0.1:" + PortPlaceholder}},
			HealthPath: "/",
		},
		{
			ID: "docker-compose", Version: "1", Title: "Docker Compose", Kind: KindDocker, DockerOnly: true,
			DefaultPort: 8080, DetectorFiles: []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"},
			NoteKey: "preset.note.docker_requires_desktop",
		},
		{
			ID: "docker-image", Version: "1", Title: "Docker image", Kind: KindDocker, DockerOnly: true,
			DefaultPort: 8080,
			NoteKey:     "preset.note.docker_requires_desktop",
		},
	}
}

// CustomPresetID 是"预设覆盖不到"时使用的 id。
//
// 它不在 WebsitePresets 里：自定义服务器的构建与启动命令由用户提供，
// 而不是来自目录。识别器永远不会推荐它。
const CustomPresetID = "custom"

// CustomPreset 返回自定义服务器的占位定义。
//
// 它存在是为了让 API 层有一个统一的"预设"概念；命令为空，必须由请求填入。
func CustomPreset() Preset {
	return Preset{
		ID: CustomPresetID, Version: "1", Title: "Custom server", Kind: KindNone,
		DefaultPort: 8080,
		NoteKey:     "preset.note.custom_commands",
	}
}

// Lookup 按 id 查找预设。
func Lookup(id string) (Preset, error) {
	if id == CustomPresetID {
		return CustomPreset(), nil
	}
	for _, p := range WebsitePresets() {
		if p.ID == id {
			return p, nil
		}
	}
	return Preset{}, fmt.Errorf("%w: %s", ErrUnknownPreset, id)
}

// All 返回目录里的全部预设，外加自定义服务器。
func All() []Preset {
	out := append([]Preset{}, WebsitePresets()...)
	return append(out, CustomPreset())
}

// Validate 检查一个预设定义是否自洽。
//
// 目录是手写的常量表，因此这里在启动时（由测试与 daemon 调用）挡掉
// "复制粘贴时忘了改 id"这类错误，而不是等用户点下去才发现。
func Validate(p Preset) error {
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("preset id is required")
	}
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("preset %s: title is required", p.ID)
	}
	if p.DefaultPort < 1 || p.DefaultPort > 65535 {
		return fmt.Errorf("preset %s: default port %d is out of range", p.ID, p.DefaultPort)
	}
	for _, step := range append(append([]Step{}, p.Install...), append(p.Build, p.Run)...) {
		if step.Executable == "" {
			continue
		}
		if strings.ContainsAny(step.Executable, "|&;><$`\n") {
			return fmt.Errorf("preset %s: executable %q contains shell syntax", p.ID, step.Executable)
		}
		for _, arg := range step.Args {
			if strings.ContainsAny(arg, "|&;><$`\n") {
				return fmt.Errorf("preset %s: argument %q contains shell syntax", p.ID, arg)
			}
		}
	}
	if p.Kind == KindDocker && !p.DockerOnly {
		return fmt.Errorf("preset %s: docker presets must set DockerOnly", p.ID)
	}
	return nil
}

// SortedIDs 返回全部预设 id（已排序），便于测试与文档。
func SortedIDs() []string {
	all := All()
	ids := make([]string, 0, len(all))
	for _, p := range all {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	return ids
}
