package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// 本文件是 D21 的**棘轮**：硬编码的用户可见中文字符串不得增加。
//
// # 为什么是棘轮而不是"一律禁止"
//
// D21（docs/DECISIONS.md）写得很明确：
//
//	**禁止硬编码用户可见字符串**。所有面向用户的文案（API 错误、CLI 输出、
//	通知模板、控制台）都必须经由本包的消息 key 获取。
//
// 而实际状况是**离达标还很远**（见下面的基线）。PLAN 里那条 CI 检查
// 一直写着"此阶段先占位" —— 也就是说这个缺口从未被度量过。
//
// 一个"一律禁止"的检查会立刻红掉全仓，而那种检查的下场是被人加
// `//nolint` 或者干脆从 CI 里摘掉。**棘轮**是这类大规模迁移的通行做法：
//
//	不允许**新增**硬编码串（这是最容易发生的）
//	允许逐步减少（每转换一处就调低基线）
//	基线本身就是进度表
//
// # 用户可见的症状
//
// 这不是纯粹的内部整洁问题。真机上确认过：`isc --lang en credential list`
// 输出**仍然是中文** —— 标志存在、有文档、对 CLI 输出毫无作用。
// 而接口层是正确的（`任务不存在` 来自消息目录）。
//
// # 基线怎么调
//
// 转换完一处之后，把对应包的基线改成实际值（**只能调低**）。
// 某个包降到 0 时，从下面的表里删掉它 —— 那时它会受"完全不许有"的约束。

// convertedFiles 是**已经完全转换**的文件。
//
// 按文件而不是按包记录进度：一个包有几十个文件，而迁移是一文件一文件
// 推进的。这个列表让"哪些已经做完"一目了然，而不是只能从总数推断。
var convertedFiles = []string{
	"internal/cli/credential.go",
	"internal/cli/records.go",
	"internal/cli/root.go",
	"internal/cli/ddns.go",
	"internal/cli/cert.go",
	"internal/cli/client.go",
	"internal/cli/ddns_add.go",
	"internal/cli/notify.go",
	"internal/cli/console.go",
	"internal/cli/service.go",
	"internal/cli/daemon.go",
	"internal/cli/verify.go",
	"internal/cli/doctor.go",
	"internal/cli/proxy.go",
	"internal/cli/expose.go",
	"internal/cli/init.go",
}

// hardcodedBaseline 是各包当前硬编码中文串的数量。
//
// 数字由 TestNoNewHardcodedStrings 自己统计并对照，因此它同时是
// **进度表**：改小它是这个迁移唯一的推进方式。
var hardcodedBaseline = map[string]int{
	// 318 → 10，两段：
	//
	// 一、318 → 48：移植代码里 62 个去重消息 key 的 ~270 个调用点由
	// isLogCall 排除了。`Log(key, ...)` / `LogStr(key, ...)` / `Errorf(key, ...)`
	// 的第一个参数是**消息 key 兼格式串**，不是待翻译的文案 —— 把它们算进来
	// 会让这个数字永远降不到 0，"达标"变成不可能。
	//
	// 覆盖情况由 ddnsgo_coverage_test.go 守着（每个 key 都必须有译文，
	// 否则英文界面上显示中文而机制不报错）。
	//
	// 二、48 → 10：剩下的 38 处原本是**硬编码**在
	// `fmt.Errorf("创建 dnsla 请求失败: %w", err)` 里的中文 ——
	// 移植代码的约定只覆盖了 Log/LogStr，这些错误漏在外面。已改走
	// ddnsgo.Errorf（先取译文格式串再交给 fmt.Errorf，%w 的包裹语义完好）。
	//
	// 剩下的 10 处**全部是数据值，不是文案**，因此停在这里：
	//
	//	provider_dnspod.go / provider_tencent_cloud.go  "默认"
	//	    DNS 记录的线路名，API 参数收的就是这个中文串
	//	provider_namesilo.go / provider_vercel.go       "新增" / "更新"
	//	    拼进消息前缀，构成"新增域名解析…"/"更新域名解析…"两个 key
	//	types.go  "未改变" / "失败" / "成功"
	//	    updateStatusType 的**内部哨兵值**。它在 tier2.go 里被
	//	    `string(ddnsgo.UpdatedSuccess)` 比较，而用户在界面上看到的状态
	//	    来自 dns.UpdateStatus（"success"/"failed"/"unchanged"）——
	//	    边界上就翻译过了，因此这三个不是文案
	"internal/ddnsgo":   10,
	"internal/platform": 4,
	// 只剩两个 API 数据值，不是文案：
	//
	//	dnspod.go     const dnspodDefaultLine = "默认"
	//	tencentcloud.go const tcDefaultLine  = "默认"
	//
	// 它们是 DNSPod / 腾讯云 `record_line` 参数**收的中文串**（免费套餐也只
	// 允许默认线路）。翻译成英文会让英文系统上的请求直接失败。
	//
	// 迁移时它们被误换成了 i18n.T(...)，是**编译器**拦下的（const 不能是
	// 函数调用）—— 这是第四次遇到"把数据当成文案"。
	"internal/provider/tier1": 2,
}

// TestNoNewHardcodedStrings 统计各包的硬编码中文串，与基线对照。
//
// 超出基线即失败 —— 那就意味着**新增了**未经消息目录的用户可见文案。
func TestNoNewHardcodedStrings(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	counts := countHardcodedCJK(t, root)

	// 豁免的包不参与：它们的理由写在 exemptPackages 里。
	for pkg := range exemptPackages {
		delete(counts, pkg)
	}

	for pkg, n := range counts {
		limit, known := hardcodedBaseline[pkg]
		if !known {
			// 新出现的包：要么它没有硬编码串（那就不该出现），
			// 要么它需要一条基线（那就把它加进表里并说明）。
			t.Errorf("包 %s 有 %d 处硬编码中文串，但基线表里没有它。\n"+
				"如果这是新增的包，请把它的基线加进 hardcodedBaseline。",
				pkg, n)
			continue
		}
		if n > limit {
			t.Errorf("包 %s 的硬编码中文串从 %d 涨到了 %d。\n"+
				"D21 要求所有面向用户的文案经由消息目录（internal/i18n）。\n"+
				"新增文案请用 i18n.T(\"some.key\") 并同时补 zh-CN 与 en 两份译文。",
				pkg, limit, n)
		}
	}

	// 减少了也要报出来 —— 那时基线该跟着调低，否则棘轮会松掉。
	for pkg, limit := range hardcodedBaseline {
		if n, ok := counts[pkg]; !ok {
			t.Errorf("包 %s 已经没有任何硬编码中文串（或已不存在），"+
				"请把它从 hardcodedBaseline 里删掉", pkg)
		} else if n < limit {
			t.Logf("包 %s 的硬编码中文串已从 %d 降到 %d —— "+
				"请把基线调低到 %d，让棘轮跟上", pkg, limit, n, n)
		}
	}
}

// TestUserFacingPackagesHaveI18n 记录哪些包**已经**完全合规。
//
// 这个列表只增不减：一个包进了这里就不该再退出去。
var i18nComplete = []string{
	// 第一个达标的包。
	//
	// 443 处 → 0，用了七轮。它的每一轮都同时在做两件事：把文案搬进目录，
	// 以及**发现那些"搬进去之后才显形"的问题** —— 帮助文本的求值时机、
	// 用了不存在的 key、并行测试与全局语言的冲突。
	"internal/cli",

	// 接口层。近百条错误说明与操作名，全部搬进 api_zh.go / api_en.go。
	//
	// 它比 cli 更容易被忽略：那些文案不会直接打在终端上，而是藏在
	// HTTP 响应的 detail 字段里 —— 但 GUI 与脚本读的正是它。
	"internal/api",

	// 设置项的校验错误。它们会作为 API 的 detail 返回给调用方，
	// 因此是面向用户的 —— 与 event/console/audit 那三个纯日志包不同，
	// 后者的理由见 exemptPackages。
	"internal/settings",

	// 可达性检查。它的文案就是 `isc doctor` 的正文，而每条检查刻意保留了
	// "这不是你的配置问题"这类判断 —— 用户看到一句失败时最需要知道的
	// 正是"这该不该我来修"。
	"internal/reach",

	// 变更编排。整条路径 —— 计划、执行、失败、回滚 —— 的文案都在这里，
	// 而"回滚没走完时说得足够严重"是它的重点：那句话意味着防火墙规则
	// 可能只放行了一半。
	"internal/change",

	// 证书签发。这一层的用途很明确：**签不下来时用户要能自己找到原因**。
	// DNS-01 校验失败那条把四种成因逐条列了出来（NS 指向别家、记录还没传播、
	// 凭据没有编辑权限、域名不存在）—— 因为它们需要完全不同的处理。
	"internal/acme",

	// 通知中心。配置校验要指出是哪个通道、哪个字段；投递时的合并说明
	// 必须**如实说明合并了多少次** —— 否则用户会以为事件只发生了一次。
	"internal/notify",

	// 持久化层的数据库错误。它们高度格式化（store: <做了什么>失败: %w），
	// 但出现在"配置出错、磁盘满、库被别的进程锁住"这些时刻，
	// 而"哪一步失败了"正是排查的起点。
	"internal/store",

	// 凭据、主密钥与路径。出错时它们是同一条链上的相邻环节，
	// 而用户看到的报错往往需要跨过这三层才能定位
	//（"解密失败"的根因可能是"刚迁移过数据目录"）。
	"internal/credential",
	"internal/secret",
	"internal/paths",

	// DNS 服务层、任务引擎、运行时文件、配置导入导出、服务商注册表。
	// 它们都出现在"配置或装配出了问题"的时刻 —— 用户需要的是
	// "哪一环没接上"，而不是一个笼统的失败。
	"internal/dns",
	"internal/job",
	"internal/runtimeinfo",
	"internal/configio",
	"internal/provider",

	// 动态解析（把本机地址同步到 DNS 记录）。它的文案会同时进审计记录与
	// 通知 —— 是用户判断"它到底干活了没有"的依据。
	"internal/ddns",

	// 内置反向代理。它服务的是"用户把上游配错了"这一类问题，而症状
	//（从外面打开是 502）与原因隔着好几层，因此每条错误都尽量把
	// "下一步该看什么"写进去。另有三条是安全检查（上游不得指向自己、
	// 必须在本机或内网、域名不得重复绑定），措辞刻意解释了为什么 ——
	// 因为用户的本能是"我就想这么配"。
	"internal/proxy",

	// 外部验证会话。它里面有一张**发给手机的 HTML 页面** —— 整个项目里
	// 唯一会被外部设备看到的界面，因此除了文案本身，还多守两条：
	// 结论在第一屏、页面不含本机的版本与路径。
	"internal/verify",

	// 守护进程。它的中文几乎全是 slog 日志（已由计数器按调用排除），
	// 剩下的是启动失败这类返回给调用方的错误，以及**推送出去的通知正文**。
	"internal/daemon",
}

func TestUserFacingPackagesHaveI18n(t *testing.T) {
	t.Parallel()

	if len(i18nComplete) == 0 {
		t.Skip("尚无完全合规的包 —— 迁移未开始")
	}

	root := repoRoot(t)
	counts := countHardcodedCJK(t, root)

	for _, pkg := range i18nComplete {
		if n := counts[pkg]; n > 0 {
			t.Errorf("包 %s 曾被标记为完全合规，现在有 %d 处硬编码中文串",
				pkg, n)
		}
	}
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

// 控制台前端的文案**不在 Go 源码里**，因此早先的统计完全看不见它。
//
// 这是 D21 明确列出的四个面之一（API 错误、CLI 输出、通知模板、控制台），
// 而一个"只数 Go 文件"的棘轮会给出**虚假的安心**：数字在降，而四个面里
// 有一个根本没被数过。
//
// 这里把前端资源也纳入统计。粒度按"文件"而不是"包" —— 前端只有四个
// 文件，而它们的体积差异很大。
var consoleAssets = []string{
	"internal/console/assets/index.html",
	"internal/console/assets/app.js",
	"internal/console/assets/panels.js",

	// 本地化的**机制**本身。它含中文的地方只有一处 —— 消息表加载失败时
	// 写给开发者看的那条 warn —— 而其它中文都在索引里（键名与注释）。
	"internal/console/assets/i18n.js",
}

// TestConsoleAssetsAreCounted 记录控制台前端的硬编码文案数。
//
// 它现在是**报告**而非失败项：前端本地化需要一套与 Go 侧不同的机制
// （静态资源在浏览器里运行，拿不到 i18n 目录），而那是一件独立的工作。
//
// 但数字必须被看见。在这之前它连数字都没有 —— 而"没有被测量的东西
// 不会被改进"。
func TestConsoleAssetsAreCounted(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var lines, chars int
	broken := map[string]int{}

	for _, rel := range consoleAssets {
		byt, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("读不到 %s: %v", rel, err)
			continue
		}
		// 先剥掉注释：见 console_comments.go。
		//
		// 注释本来就该留着（而且越多越好），把它们算进来会让这个数字
		// **永远降不到 0** —— 一个降不到 0 的达标线不是达标线。
		body := stripConsoleComments(rel, string(byt))

		n := 0
		for _, line := range strings.Split(body, "\n") {
			if containsHan(line) {
				n++
				chars += len([]rune(line))
			}
		}
		t.Logf("  %s: %d 行含中文", rel, n)
		if n > consoleHardcodedLines[rel] {
			broken[rel] = n
		}
		lines += n
	}

	t.Logf("控制台前端：%d 行含中文（%d 个字符）—— 尚未迁移到消息目录",
		lines, chars)

	for rel, n := range broken {
		t.Errorf("%s 的含中文行数从 %d 涨到了 %d。\n"+
			"控制台是 D21 列出的四个面之一。前端本地化尚未开始，"+
			"因此这里只拦住**新增**。",
			rel, consoleHardcodedLines[rel], n)
	}
}

// consoleHardcodedLines 是前端各文件当前的含中文行数。
//
// 与 Go 侧的基线同理：它是一张进度表，只降不升。
var consoleHardcodedLines = map[string]int{
	// 数字是**剥掉注释之后**的（见 console_comments.go）。
	// 剥离之前分别是 129 / 195 / 94 / 26 —— 也就是说有 90 行是注释，
	// 而注释本来就该留着。不剥的话这个数字**永远降不到 0**。
	"internal/console/assets/index.html": 0,
	"internal/console/assets/app.js":     132,
	"internal/console/assets/panels.js":  29,
	// i18n.js 只剩一行：消息表加载失败时写给开发者看的那条 warn。
	// 它是**开发者**信息，不是用户文案，因此留着。
	"internal/console/assets/i18n.js": 1,
}

// exemptPackages 是**不要求迁移**的包，每条都写明理由。
//
// # 为什么"把这里的中文也翻译了"不是目标
//
// D21 要求的是"面向用户的文案"。有两类中文**不满足这个定义**，理由不同：
//
// ## 一、日志行是给运维看的
//
// 把它们翻译了反而有害：
//
//   - 运维靠 grep 稳定的字符串来定位问题。日志随语言设置变来变去，
//     会让"我上周见过这条错误"变成一件做不到的事；
//   - 报错时用户贴出来的日志，会与文档、issue 里的英文/中文原文对不上；
//   - 而这些行本来就带 `pkg: ` 前缀（如 `event: `），是明确的内部信号。
//
// ## 二、构建工具跑在维护者的机器上，拿不到用户的语言设置
//
// `scripts/release` 是发布构建脚本。给它接 i18n 只会让**构建机的 locale**
// 决定输出语言 —— 那没有意义，因为读它的人不是最终用户。
//
// 它里面确实有一处**装在 .deb 里发给用户**的文本（版权声明），但那正是
// **不该**机器翻译的东西：GPL 自己就写明译本不具法律效力，而许可证原文
// 是唯一权威的版本。
//
// # 所以这里的做法
//
// **显式地**把这类包列出来并写明理由，而不是机械地一条条翻译过去。
// 棘轮因此仍然有意义 —— 它挡住的是"新加了一条面向用户的中文文案"，
// 而不是"日志或构建工具里出现了中文"。
//
// 判定标准：该包剩余的中文串**全部**是日志、内部错误、或工具输出，
// 且不会作为 API 的 detail 返回给调用方、也不会出现在最终用户读的界面上。
var exemptPackages = map[string]string{
	"internal/event": "仅剩总线自身的日志（订阅者过慢、总线已关闭）",
	"internal/console": "仅剩内嵌资源结构异常这一条开发者错误，" +
		"它表示二进制被破坏，用户看到的会是 500",
	"internal/audit": "仅剩写入审计失败这一条日志 —— 它是**刻意**只记日志的：" +
		"审计写不进去不该让业务操作失败",
	"scripts/release": "发布构建工具，跑在维护者的机器上、拿不到用户的语言设置。" +
		"其中装在 .deb 里的版权声明**不该**被机器翻译 ——" +
		"GPL 自己就写明译本不具法律效力，许可证原文是唯一权威的版本",
}

// logLevels 是日志级别的方法名。
//
// 只看方法名不够 —— `x.Error("用户可见文案")` 也可能存在。因此还要看
// **接收者**：只有形如 `r.log` / `d.log` / `logger` / `slog` 的才当成日志。
var logLevels = map[string]bool{
	"Debug": true, "Info": true, "Warn": true, "Error": true,
}

// isLogCall 判断一个调用是不是日志调用。
//
// 判据是"接收者的名字里含 log"（不区分大小写）+ 方法名是日志级别。
// 这个判断刻意保守：宁可漏掉一条日志（那它会被当成文案要求迁移），
// 也不要把一条用户可见的错误当成日志而放过。
// 另有一类**裸标识符**的日志调用：移植过来的 ddns-go 代码用的是包级
// `Log(...)` / `LogStr(...)`（见 internal/ddnsgo/log.go）。它们的第一个
// 参数是**消息 key 兼格式串**，因此那些中文字符串不是"硬编码文案"，
// 而是消息标识符 —— 把它们算进来会让 ddnsgo 的基线停在 318 而永远降不到 0。
//
// 覆盖情况由 TestDdnsGoKeysAreTranslated 守着：每个 key 都必须有译文，
// 否则英文界面上会显示中文，而机制不会报错。
func isLogCall(call *ast.CallExpr) bool {
	if id, ok := call.Fun.(*ast.Ident); ok && ddnsGoCallNames[id.Name] {
		return true
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !logLevels[sel.Sel.Name] {
		return false
	}
	return receiverLooksLikeLogger(sel.X)
}

// receiverLooksLikeLogger 检查接收者表达式里是否出现 "log"。
//
// 覆盖 `r.log`、`d.log`、`s.log`、`logger`、`slog`、`a.b.logger` 这些写法。
func receiverLooksLikeLogger(x ast.Expr) bool {
	switch v := x.(type) {
	case *ast.Ident:
		return strings.Contains(strings.ToLower(v.Name), "log")
	case *ast.SelectorExpr:
		return strings.Contains(strings.ToLower(v.Sel.Name), "log") ||
			receiverLooksLikeLogger(v.X)
	default:
		return false
	}
}

// countHardcodedCJK 统计各包里的中文字符串字面量。
//
// 只统计**字符串字面量**，不统计注释 —— 注释里的中文是好的
// （它们解释了"为什么"），而 D21 管的是面向用户的文案。
//
// # 日志调用的参数不算
//
// D21 要求的是"面向用户的文案"，而日志行是给运维看的。把它们翻译了反而
// 有害（运维靠 grep 稳定字符串定位问题，而用户贴出来的日志会与文档对不上）。
//
// 这一点此前是靠 exemptPackages **逐个包**手工豁免的 —— 那对"整包只剩日志"
// 的情形够用，但对 internal/change 这种**用户可见错误与日志混在一起**的包
// 就失效了：棘轮会把日志行也算进去，于是它在测量一个 D21 不关心的东西，
// 而那个数字永远降不到 0。
//
// 改成按**调用**排除之后，棘轮测的才是它该测的东西。
func countHardcodedCJK(t *testing.T, root string) map[string]int {
	t.Helper()

	fset := token.NewFileSet()
	counts := map[string]int{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "ddns-go-master", "vendor", ".git", ".tmp", "dist", "bin", "gen":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// i18n 包自己是消息目录，它的字符串就是译文本身。
		if strings.Contains(path, string(filepath.Separator)+"i18n"+string(filepath.Separator)) {
			return nil
		}

		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		// 统一成斜杠，让基线表在三个平台上一致。
		pkg := filepath.ToSlash(filepath.Dir(rel))

		// 第一遍：记下所有日志调用里的字符串位置。
		logged := map[token.Pos]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isLogCall(call) {
				return true
			}
			for _, arg := range call.Args {
				ast.Inspect(arg, func(m ast.Node) bool {
					if lit, ok := m.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						logged[lit.Pos()] = true
					}
					return true
				})
			}
			return true
		})

		// 第二遍：统计剩下的。
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if logged[lit.Pos()] {
				return true
			}
			s, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			if containsHan(s) {
				counts[pkg]++
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("遍历源码失败: %v", err)
	}
	return counts
}

func containsHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// repoRoot 向上找到含 go.mod 的目录。
func repoRoot(t *testing.T) string {
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
