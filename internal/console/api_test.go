package console

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件验证**控制台引用的每一个接口路径都真的存在**。
//
// # 它为什么值得是一条测试
//
// 这个项目里已经栽过一次：控制台的"测试连接"按钮调的是
// POST /v1/credentials/{id}/verify —— 而那个路径**根本不在接口规格里**。
// 也就是说那个按钮从来没有工作过。
//
// 这类缺陷的特别之处在于它的**静默**：控制台的 JS 没有类型检查、没有
// 编译期验证，而失败只表现为"点了没反应"或一句泛泛的报错。用户会以为
// 是自己操作错了。
//
// 与 docs_test.go 同样的思路：把"文档/界面与实现一致"这件事交给工具，
// 因为人不会每次都把每个按钮点一遍。

// consoleAPICall 匹配 api('METHOD', 'PATH') 与 api("METHOD", "PATH")。
var consoleAPICall = regexp.MustCompile(`api\(\s*['"]([A-Z]+)['"]\s*,\s*['"]([^'"]+)['"]`)

// 从 JS 里找路径拼接：api('POST', '/v1/credentials/' + id + '/verify')
var consoleConcat = regexp.MustCompile(`api\(\s*['"]([A-Z]+)['"]\s*,\s*['"]([^'"]+)['"]\s*\+([^,)]+)`)

// TestConsoleAPIPathsExist 逐条验证。
func TestConsoleAPIPathsExist(t *testing.T) {
	t.Parallel()

	root := repoRootForAPI(t)
	spec := readSpec(t, root)

	// 从规格里收集全部路径模板。
	pathRe := regexp.MustCompile(`(?m)^  (/v1/[^:]*):\s*$`)
	existing := map[string]bool{}
	for _, m := range pathRe.FindAllStringSubmatch(spec, -1) {
		existing[strings.TrimSpace(m[1])] = true
	}
	if len(existing) == 0 {
		t.Fatal("没有从规格里解析出任何路径 —— 正则可能过时了")
	}

	js := readAsset(t, "app.js") + "\n" + readAsset(t, "panels.js")

	type call struct {
		method string
		path   string
	}
	seen := map[string]call{}

	// 直接字面量。
	//
	// **以 / 结尾的要跳过**：那是路径前缀，实际路径由后面的拼接产生
	//（`'/v1/service/' + action`），而把它当成一条独立路径会误报。
	for _, m := range consoleAPICall.FindAllStringSubmatch(js, -1) {
		p := strings.Split(m[2], "?")[0]
		if strings.HasSuffix(p, "/") {
			continue
		}
		seen[m[1]+" "+p] = call{m[1], p}
	}

	// 带拼接的：把路径前缀补成规格里的模板形式。
	//
	// 控制台里大量出现 `api('DELETE', '/v1/credentials/' + id)` ——
	// 路径的尾段是运行期才有的 ID，而规格里写作 {id}。
	// 不处理它们会让这条测试对最常见的形态视而不见。
	for _, m := range consoleConcat.FindAllStringSubmatch(js, -1) {
		base := strings.Split(m[2], "?")[0]
		// 追出这一行里后续的 + '/xxx' 片段。
		line := m[0]
		extra := regexp.MustCompile(`\+\s*['"](/[^'"]*)['"]`).FindAllStringSubmatch(
			strings.TrimPrefix(line, m[0][:len(m[0])]), -1)
		suffix := ""
		for _, e := range extra {
			suffix += e[1]
		}
		p := base
		if suffix != "" {
			p += suffix
		} else {
			p += "{id}"
		}
		// 前缀以 / 结尾而后面没有已知后缀 → 补一个占位段。
		if strings.HasSuffix(p, "/") {
			p += "{id}"
		}
		seen[m[1]+" "+p] = call{m[1], p}
	}

	if len(seen) == 0 {
		t.Fatal("没有从控制台解析出任何接口调用 —— 正则可能过时了")
	}

	var keys []string
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		c := seen[k]
		if !pathExists(existing, c.path) {
			t.Errorf("控制台调用了不存在的接口：%s %s", c.method, c.path)
		}
	}
}

// pathExists 判断一条实际路径是否匹配规格里的某个模板。
func pathExists(existing map[string]bool, p string) bool {
	if existing[p] {
		return true
	}

	// 把具体段换成 {…} 再比。
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for template := range existing {
		ts := strings.Split(strings.Trim(template, "/"), "/")
		if len(ts) != len(segs) {
			continue
		}
		ok := true
		for i := range ts {
			// 占位段**双向**匹配。
			//
			// 规格里写作 {id}，而调用侧可能是运行期才知道的变量
			//（`'/v1/service/' + action`），我也把它标成了 {id}。
			// 只允许规格侧有占位符会让这类调用一律误报。
			if strings.HasPrefix(ts[i], "{") || strings.HasPrefix(segs[i], "{") {
				continue
			}
			if ts[i] != segs[i] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// repoRootForAPI 向上找到含 api/openapi.yaml 的目录。
func repoRootForAPI(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "api", "openapi.yaml")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("找不到 api/openapi.yaml")
	return ""
}

func readSpec(t *testing.T, root string) string {
	t.Helper()

	byt, err := os.ReadFile(filepath.Join(root, "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("读取接口规格失败: %v", err)
	}
	return string(byt)
}
