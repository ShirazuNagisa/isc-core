package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件验证**CLI 引用的每一个接口路径都真的存在**。
//
// # 与 docs_test.go 同样的思路
//
// 那里查的是"文档里的命令是否存在"，这里查"CLI 调的接口是否存在"。
// 两者防的是同一类静默缺陷：字符串写错了，编译器不会说话，而失败
// 只表现为一句泛泛的 404 —— 用户会以为是自己操作错了。
//
// 控制台那边已经栽过一次（"测试连接"按钮调的端点不存在），因此这里
// 从一开始就加上。

// cliAPICall 匹配客户端方法里的路径字面量。
//
// # 方法参数有两种写法，都必须覆盖
//
//	client.getInto(ctx, "/v1/credentials", &out)           没有方法参数
//	c.do(ctx, http.MethodGet, "/v1/health", &h)            方法是个常量
//	client.doBody(ctx, "PATCH", "/v1/settings", ...)       方法是字符串
//
// 最初的正则只认字符串字面量，于是 `http.MethodGet` 那一类**完全没被
// 检查到** —— 而它是查出来的：把 /v1/health 改成不存在的路径之后
// 测试照样通过。**一条不会失败的测试等于没有测试。**
var cliAPICall = regexp.MustCompile(
	`(?:getInto|postInto|putInto|delete|do|doBody)\(\s*ctx\s*,\s*` +
		`(?:(?:"[A-Z]+"|http\.Method[A-Za-z]+)\s*,\s*)?` +
		`"([^"]+)"`)

// TestCLIAPIPathsExist 逐条验证。
func TestCLIAPIPathsExist(t *testing.T) {
	t.Parallel()

	root := findRepoRoot(t)
	specPath := filepath.Join(root, "api", "openapi.yaml")

	byt, err := os.ReadFile(specPath)
	if err != nil {
		t.Skipf("读不到接口规格: %v", err)
	}
	spec := string(byt)

	pathRe := regexp.MustCompile(`(?m)^  (/v1/[^:]*):\s*$`)
	existing := map[string]bool{}
	for _, m := range pathRe.FindAllStringSubmatch(spec, -1) {
		existing[strings.TrimSpace(m[1])] = true
	}
	if len(existing) == 0 {
		t.Fatal("没有从规格里解析出任何路径 —— 正则可能过时了")
	}

	// 扫本包的全部源文件。
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") ||
			strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			continue
		}
		for _, m := range cliAPICall.FindAllStringSubmatch(string(src), -1) {
			p := strings.Split(m[1], "?")[0]
			// 纯前缀（以 / 结尾）由后面的拼接补全，跳过。
			if strings.HasSuffix(p, "/") {
				continue
			}
			seen[p] = true
		}
	}

	if len(seen) == 0 {
		t.Fatal("没有从 CLI 解析出任何接口调用 —— 正则可能过时了")
	}

	var paths []string
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		if !apiPathExists(existing, p) {
			t.Errorf("CLI 调用了不存在的接口：%s", p)
		}
	}
}

// apiPathExists 判断一条实际路径是否匹配规格里的某个模板。
//
// 占位段**双向**匹配：规格里写作 {id}，而调用侧可能带一个具体值
// （`"/v1/credentials/" + id`）。
func apiPathExists(existing map[string]bool, p string) bool {
	if existing[p] {
		return true
	}

	segs := strings.Split(strings.Trim(p, "/"), "/")
	for template := range existing {
		ts := strings.Split(strings.Trim(template, "/"), "/")
		if len(ts) != len(segs) {
			continue
		}
		ok := true
		for i := range ts {
			if strings.HasPrefix(ts[i], "{") {
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
