package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// 本文件验证**文档里出现的每一条 isc 命令都真的存在**。
//
// # 它为什么值得是一条测试
//
// 这个项目里已经栽过两次同一类问题：
//
//	README 与 isc init 教用户运行 isc credential add —— 而那个命令不存在
//	README 的快速上手第 3 步是 isc ddns add —— 而 isc ddns 只有 list 与 run
//
// 两次都不是从代码里看出来的，而是**照着文档敲一遍**才发现的。
// 而人不会每次都把每条命令敲一遍，因此这件事必须由测试保证。
//
// 文档与实现不一致比缺少文档更糟：用户会照着敲、失败、然后不知道该信哪个。

// docCmdRe 从文档里抽出一条 `isc …` 命令。
var docCmdRe = regexp.MustCompile("`?isc ([a-z][a-z0-9-]*(?: [a-z][a-z0-9-]*)?)([^`\\n]*)`?")

// TestDocumentedCommandsExist 逐条验证文档里的命令。
func TestDocumentedCommandsExist(t *testing.T) {
	t.Parallel()

	root := New()

	// 找仓库根目录：测试的工作目录是包目录（internal/cli）。
	repoRoot := findRepoRoot(t)

	var docs []string
	for _, d := range []string{"README.md", filepath.Join("docs", "PLAN.md")} {
		if _, err := os.Stat(filepath.Join(repoRoot, d)); err == nil {
			docs = append(docs, d)
		}
	}
	if len(docs) == 0 {
		t.Skip("找不到文档，跳过")
	}

	type occ struct {
		cmd  string
		file string
		line int
	}
	seen := map[string]occ{}

	for _, doc := range docs {
		byt, err := os.ReadFile(filepath.Join(repoRoot, doc))
		if err != nil {
			continue
		}

		// **先把续行拼起来**：多行命令的标志在后续几行上，
		// 逐行扫描会完全看不到它们（这正是早先那个检查器的盲点）。
		raw := strings.Split(string(byt), "\n")
		for i := 0; i < len(raw); i++ {
			line := raw[i]
			start := i + 1
			for strings.HasSuffix(strings.TrimRight(line, " \t"), "\\") && i+1 < len(raw) {
				line = strings.TrimRight(strings.TrimRight(line, " \t"), "\\") +
					" " + strings.TrimSpace(raw[i+1])
				i++
			}

			// 明确的"这是反例"标记要跳过。
			//
			// PLAN 里有若干**故意**写错命令的段落 —— 它们记录的是曾经
			// 修过的缺陷（"文档在教用户运行不存在的命令"）。那些例子
			// 必须留着，否则读的人不知道当初错在哪；而它们不该让这条
			// 测试失败。
			//
			// 标记写在**同一行**（`<!-- 反例：… -->`），因为逐行扫描时
			// 跨行的标记是看不到的。
			if strings.Contains(line, "不存在") || strings.Contains(line, "❌") ||
				strings.Contains(line, "反例") {
				continue
			}

			for _, m := range docCmdRe.FindAllStringSubmatch(line, -1) {
				key := strings.TrimSpace(m[1] + m[2])
				key = strings.TrimRight(key, "。，、；：！？ \t")
				if key == "" {
					continue
				}
				if _, ok := seen[key]; !ok {
					seen[key] = occ{key, doc, start}
				}
			}
		}
	}

	if len(seen) == 0 {
		t.Fatal("没有从文档里解析出任何命令 —— 正则可能过时了")
	}

	var keys []string
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		oc := seen[k]
		fields := strings.Fields(k)

		// 取到第一个标志为止的动词序列。
		var words []string
		for _, f := range fields {
			if strings.HasPrefix(f, "-") {
				break
			}
			words = append(words, f)
		}
		if len(words) == 0 {
			continue
		}

		cmd := findSubcommand(root, words[0])
		if cmd == nil {
			t.Errorf("%s:%d  `isc %s` —— 顶层命令不存在",
				oc.file, oc.line, k)
			continue
		}

		// 第二层必须真的是子命令。
		//
		// 两类误报要跳过：散文（"isc doctor 能明确指出…"）与简写列表
		//（"isc service install/uninstall/…"）—— 它们的共同特征是含
		// 非 ASCII 字符或斜杠。
		if len(words) >= 2 && isCommandWord(words[1]) {
			sub := findSubcommand(cmd, words[1])
			if sub == nil {
				t.Errorf("%s:%d  `isc %s` —— 子命令 %q 不存在",
					oc.file, oc.line, k, words[1])
				continue
			}
			cmd = sub
		}

		// 标志必须在这一层或全局的帮助里出现。
		//
		// 用 cobra 的命令树而不是跑 --help：**退出码判断不了子命令是否存在**
		// —— cobra 对未知子命令会打印父命令帮助并退出 0，早先的检查器
		// 正是因此漏掉了 `isc ddns add`。
		for _, f := range fields {
			if !strings.HasPrefix(f, "--") {
				continue
			}
			name := f
			if idx := strings.Index(name, "="); idx > 0 {
				name = name[:idx]
			}
			name = strings.TrimRight(name, "\\")
			if name == "--" || name == "" {
				continue
			}

			// --help 由 cobra 自动加，而它是在 Execute 时才注册的 ——
			// 新建的命令树上查不到它。它是永远合法的。
			if name == "--help" || name == "-h" {
				continue
			}
			if !hasFlag(cmd, name) {
				t.Errorf("%s:%d  `isc %s` —— 标志 %s 不存在",
					oc.file, oc.line, k, name)
			}
		}
	}
}

// findRepoRoot 向上找到含 go.mod 的目录。
func findRepoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("找不到仓库根目录")
	return ""
}

// findSubcommand 在命令的子命令里按名字找。
func findSubcommand(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
		// 别名也算 —— 文档里可能用别名。
		for _, a := range c.Aliases {
			if a == name {
				return c
			}
		}
	}
	return nil
}

// hasFlag 判断命令（含继承的持久标志）上有没有这个标志。
func hasFlag(cmd *cobra.Command, name string) bool {
	want := strings.TrimPrefix(name, "--")

	// LocalFlags 含本层与继承的持久标志。
	f := cmd.LocalFlags()
	if f.Lookup(want) != nil {
		return true
	}
	// 也认帮助文本里出现过的标志（有些是运行时注册的）。
	return strings.Contains(cmd.UsageString(), name)
}

// isCommandWord 判断一个词是不是真的子命令名。
func isCommandWord(s string) bool {
	if s == "" || strings.Contains(s, "/") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}
