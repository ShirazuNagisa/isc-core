package platform

import (
	"encoding/xml"
	"runtime"
	"strings"
	"testing"
)

// 本文件覆盖服务定义文件的生成。
//
// # 为什么这些测试能在 Windows 上跑
//
// 生成 unit / plist 的内容是纯字符串逻辑，被单独放在没有构建标签的
// service_def.go 里。而 Linux 与 macOS 的**真机验证**在本机做不到
//（见 docs/PLAN.md R8）—— 因此这部分逻辑能被测到，是这两块唯一的
// 质量保证。
//
// 重点在三处最容易出错的地方：
//
//	引号     含空格的路径不加引号会被拆成两个参数
//	转义     plist 是 XML，路径里的 & 会让它无法解析
//	注入     参数里的换行符会越出那一行配置

// ---------------------------------------------------------------------------
// systemd
// ---------------------------------------------------------------------------

func TestRenderSystemdUnitBasics(t *testing.T) {
	t.Parallel()

	unit := RenderSystemdUnit(ServiceConfig{
		Executable:       "/usr/local/bin/isc",
		Arguments:        []string{"daemon", "run"},
		WorkingDirectory: "/var/lib/isc",
		AutoStart:        true,
		RestartOnFailure: true,
	})

	// ExecStart 必须带上参数，否则服务起来了但不做任何事。
	if !strings.Contains(unit, "ExecStart=/usr/local/bin/isc daemon run") {
		t.Errorf("ExecStart 不对:\n%s", unit)
	}
	if !strings.Contains(unit, "WorkingDirectory=/var/lib/isc") {
		t.Error("缺少 WorkingDirectory")
	}
	// 开机自启靠 [Install] 段，缺了它 enable 会失败。
	if !strings.Contains(unit, "WantedBy=multi-user.target") {
		t.Error("缺少 [Install] 段 —— 开机自启会失败")
	}
	// 等网络就绪。
	//
	// 少了它，开机时第一轮地址检测会拿到空结果。
	if !strings.Contains(unit, "After=network-online.target") {
		t.Error("缺少 network-online.target 依赖 —— 开机时网络可能还没就绪")
	}
}

// TestSystemdQuotesPathsWithSpaces 钉住含空格路径的处理。
//
// systemd 的 ExecStart 按**它自己的规则**分词。不加引号的话
// `/opt/my app/isc` 会被拆成两个参数 —— 而症状是"服务启动失败"，
// 看不出根因是路径里的空格。
func TestSystemdQuotesPathsWithSpaces(t *testing.T) {
	t.Parallel()

	unit := RenderSystemdUnit(ServiceConfig{
		Executable: "/opt/my app/isc",
		Arguments:  []string{"daemon", "run", "--data-dir", "/var/lib/my data"},
	})

	if !strings.Contains(unit, `ExecStart="/opt/my app/isc" daemon run --data-dir "/var/lib/my data"`) {
		t.Errorf("含空格的路径没有被正确引用:\n%s", unit)
	}
}

func TestSystemdQuotesOtherSpecials(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"/usr/bin/isc", "/usr/bin/isc"},       // 无需引号
		{"/opt/a b/isc", `"/opt/a b/isc"`},     // 空格
		{`/opt/a"b/isc`, `"/opt/a\"b/isc"`},    // 引号要转义
		{`/opt/a\b/isc`, `"/opt/a\\b/isc"`},    // 反斜杠要转义
		{"/opt/a\tb/isc", "\"/opt/a\tb/isc\""}, // 制表符
	}

	for _, tc := range cases {
		if got := quoteSystemdArg(tc.in); got != tc.want {
			t.Errorf("quoteSystemdArg(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// TestSystemdRestartPolicy 验证重启策略。
func TestSystemdRestartPolicy(t *testing.T) {
	t.Parallel()

	withRestart := RenderSystemdUnit(ServiceConfig{
		Executable: "/usr/bin/isc", RestartOnFailure: true,
	})
	// on-failure 而不是 always：用户主动 stop 时不该被拉起来。
	if !strings.Contains(withRestart, "Restart=on-failure") {
		t.Error("应当用 on-failure 而不是 always")
	}
	// 重启次数上限：没有它的话，一个必然启动失败的配置会让系统日志
	// 被刷满，而用户完全看不出"它在反复重启"。
	if !strings.Contains(withRestart, "StartLimitBurst=5") {
		t.Error("缺少重启次数上限 —— 会变成无限重启")
	}

	withoutRestart := RenderSystemdUnit(ServiceConfig{Executable: "/usr/bin/isc"})
	if strings.Contains(withoutRestart, "Restart=") {
		t.Error("未开启崩溃重启时不该写 Restart 指令")
	}
}

func TestSystemdWorkingDirectoryDefaultsToExecutableDir(t *testing.T) {
	t.Parallel()

	unit := RenderSystemdUnit(ServiceConfig{Executable: "/usr/local/bin/isc"})

	if !strings.Contains(unit, "WorkingDirectory=/usr/local/bin") {
		t.Errorf("工作目录应当默认为可执行文件所在目录:\n%s", unit)
	}
}

// ---------------------------------------------------------------------------
// launchd
// ---------------------------------------------------------------------------

// TestRenderLaunchdPlistIsValidXML 是最重要的一条。
//
// plist 是 XML，而解析失败时报错只有一句"格式错误" ——
// 因此必须**真的解析一遍**，而不是靠字符串包含来断言。
func TestRenderLaunchdPlistIsValidXML(t *testing.T) {
	t.Parallel()

	plist := RenderLaunchdPlist(ServiceConfig{
		Executable:       "/usr/local/bin/isc",
		Arguments:        []string{"daemon", "run"},
		WorkingDirectory: "/var/lib/isc",
		AutoStart:        true,
		RestartOnFailure: true,
	})

	// 整份文档必须能被 XML 解析器读下来。
	dec := xml.NewDecoder(strings.NewReader(plist))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("plist 不是合法的 XML: %v\n%s", err, plist)
		}
	}

	for _, want := range []string{
		"<string>/usr/local/bin/isc</string>",
		"<string>daemon</string>",
		"<string>run</string>",
		"<key>Label</key>",
		"<string>" + darwinServiceLabel + "</string>",
		"<key>RunAtLoad</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist 缺少 %q:\n%s", want, plist)
		}
	}
}

// TestLaunchdEscapesXMLSpecials 钉住 XML 转义。
//
// 路径里出现 `&` 是可能的（尤其在用户自定的数据目录里），不转义会让
// plist 无法解析。
func TestLaunchdEscapesXMLSpecials(t *testing.T) {
	t.Parallel()

	plist := RenderLaunchdPlist(ServiceConfig{
		Executable: "/opt/a&b/isc",
		Arguments:  []string{"--data-dir", "/var/lib/a<b>c"},
	})

	// 原始字符不该出现在 XML 里。
	if strings.Contains(plist, "a&b") {
		t.Error("& 没有被转义")
	}
	if strings.Contains(plist, "a<b>c") {
		t.Error("< 与 > 没有被转义")
	}
	if !strings.Contains(plist, "a&amp;b") {
		t.Error("& 应当被转义成 &amp;")
	}

	// 而且必须仍然能被解析。
	dec := xml.NewDecoder(strings.NewReader(plist))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("含特殊字符的 plist 无法解析: %v\n%s", err, plist)
		}
	}
}

func TestXmlEscape(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"plain":    "plain",
		"a&b":      "a&amp;b",
		"a<b":      "a&lt;b",
		"a>b":      "a&gt;b",
		`a"b`:      "a&quot;b",
		"a'b":      "a&apos;b",
		"/usr/bin": "/usr/bin",
	}
	for in, want := range cases {
		if got := xmlEscape(in); got != want {
			t.Errorf("xmlEscape(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestLaunchdKeepAliveIsConditional 钉住 KeepAlive 的取值。
//
// 用笼统的 KeepAlive=true 会让用户主动 stop 之后服务立刻被拉起来 ——
// 那是"我明明停掉了它"这类困惑的来源。
func TestLaunchdKeepAliveIsConditional(t *testing.T) {
	t.Parallel()

	withRestart := RenderLaunchdPlist(ServiceConfig{
		Executable: "/usr/bin/isc", RestartOnFailure: true,
	})
	if !strings.Contains(withRestart, "<key>SuccessfulExit</key>") {
		t.Error("应当用 SuccessfulExit=false 而不是 KeepAlive=true")
	}
	if strings.Contains(withRestart, "<key>KeepAlive</key>\n  <true/>") {
		t.Error("不该用笼统的 KeepAlive=true")
	}
	// 限流：一个必然启动失败的配置会变成无限重启。
	if !strings.Contains(withRestart, "ThrottleInterval") {
		t.Error("缺少 ThrottleInterval —— 无限重启会刷满系统日志")
	}

	withoutRestart := RenderLaunchdPlist(ServiceConfig{Executable: "/usr/bin/isc"})
	if strings.Contains(withoutRestart, "KeepAlive") {
		t.Error("未开启崩溃重启时不该写 KeepAlive")
	}
}

func TestLaunchdAutoStart(t *testing.T) {
	t.Parallel()

	on := RenderLaunchdPlist(ServiceConfig{Executable: "/usr/bin/isc", AutoStart: true})
	if !strings.Contains(on, "<key>RunAtLoad</key>") {
		t.Error("开机自启需要 RunAtLoad")
	}

	off := RenderLaunchdPlist(ServiceConfig{Executable: "/usr/bin/isc"})
	if strings.Contains(off, "RunAtLoad") {
		t.Error("未开启自启时不该写 RunAtLoad")
	}
}

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

// TestValidateRejectsRelativePath 钉住绝对路径的要求。
//
// 相对路径在服务启动时的解析基准与用户 shell 不同（Windows 上是
// System32，systemd 上是 /），而可执行文件显然不在那里 ——
// 症状是"服务装好了但一启动就退出"，错误信息还很含糊。
// absPathForHost 返回一个**当前宿主平台**上的绝对路径。
//
// ValidateServiceConfig 校验的是当前主机的配置，因此测试用的路径
// 也必须与宿主匹配：`/usr/local/bin/isc` 在 Windows 上不是绝对路径，
// 而在 Linux 上是。
func absPathForHost(parts ...string) string {
	if runtime.GOOS == "windows" {
		return `C:\` + strings.Join(parts, `\`)
	}
	return "/" + strings.Join(parts, "/")
}

func TestValidateRejectsRelativePath(t *testing.T) {
	t.Parallel()

	err := ValidateServiceConfig(ServiceConfig{Executable: "isc"})
	if err == nil {
		t.Fatal("相对路径应当被拒绝")
	}
	if !strings.Contains(err.Error(), "绝对路径") {
		t.Errorf("错误信息应当说明要求: %v", err)
	}
}

// TestValidateRejectsNewlines 钉住换行注入的防护。
//
// 参数里的换行符会**越出那一行配置**，把后面的内容变成新的指令 ——
// 对 unit 文件与 plist 都是一个改写文件其余部分的注入点。
func TestValidateRejectsNewlines(t *testing.T) {
	t.Parallel()

	abs := absPathForHost("usr", "bin", "isc")
	cases := []ServiceConfig{
		{Executable: abs, Arguments: []string{"run\nRestart=always"}},
		{Executable: abs, Arguments: []string{"a\rb"}},
		{Executable: abs + "\n[Service]"},
		{Executable: abs, WorkingDirectory: absPathForHost("tmp") + "\nrm -rf /"},
	}

	for i, cfg := range cases {
		if err := ValidateServiceConfig(cfg); err == nil {
			t.Errorf("第 %d 组含换行符的配置应当被拒绝", i+1)
		}
	}
}

func TestValidateRejectsQuotes(t *testing.T) {
	t.Parallel()

	// 引号会破坏命令行解析。
	err := ValidateServiceConfig(ServiceConfig{
		Executable: absPathForHost("usr", "bin", "isc"),
		Arguments:  []string{`--label="坏"`},
	})
	if err == nil {
		t.Error("含引号的参数应当被拒绝")
	}
}

func TestValidateAcceptsGoodConfig(t *testing.T) {
	t.Parallel()

	good := ServiceConfig{
		Executable:       absPathForHost("usr", "local", "bin", "isc"),
		Arguments:        []string{"daemon", "run"},
		WorkingDirectory: absPathForHost("var", "lib", "isc"),
		AutoStart:        true,
		RestartOnFailure: true,
	}
	if err := ValidateServiceConfig(good); err != nil {
		t.Errorf("合法配置被拒绝: %v", err)
	}
}

func TestValidateRejectsEmptyExecutable(t *testing.T) {
	t.Parallel()

	if err := ValidateServiceConfig(ServiceConfig{}); err == nil {
		t.Error("缺少可执行文件路径时应当被拒绝")
	}
	if err := ValidateServiceConfig(ServiceConfig{Executable: "   "}); err == nil {
		t.Error("只有空白的路径应当被拒绝")
	}
}

// TestValidateRejectsRelativeWorkingDirectory 验证工作目录也要绝对路径。
func TestValidateRejectsRelativeWorkingDirectory(t *testing.T) {
	t.Parallel()

	err := ValidateServiceConfig(ServiceConfig{
		Executable:       absPathForHost("usr", "bin", "isc"),
		WorkingDirectory: "data",
	})
	if err == nil {
		t.Error("相对的工作目录应当被拒绝")
	}
}

// TestServiceDefinitionsArePlatformIndependent 说明这些函数在任何平台都能跑。
//
// 这一点是刻意的：Linux 与 macOS 的服务安装在本机无法真机验证，
// 因此把它们的内容生成逻辑放在无构建标签的文件里，是那两块唯一的
// 质量保证。
func TestServiceDefinitionsArePlatformIndependent(t *testing.T) {
	t.Parallel()

	// 在 Windows 上也要能生成出合法的 unit 与 plist。
	unit := RenderSystemdUnit(ServiceConfig{Executable: "/usr/bin/isc"})
	if !strings.Contains(unit, "[Unit]") {
		t.Error("systemd 单元生成失败")
	}
	plist := RenderLaunchdPlist(ServiceConfig{Executable: "/usr/bin/isc"})
	if !strings.Contains(plist, "<plist version=\"1.0\">") {
		t.Error("launchd plist 生成失败")
	}

	t.Logf("当前平台: %s（这些逻辑不依赖平台）", runtime.GOOS)
}
