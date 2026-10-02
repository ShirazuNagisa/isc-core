//go:build windows

package platform

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// 本文件覆盖 Windows 服务后端里的十个辅助函数。它们此前一条测试都没有，
// 而其中一半决定**用户被告知什么**，另一半决定**注册进系统的配置**。

// ---------------------------------------------------------------------------
// 错误分类
// ---------------------------------------------------------------------------

// TestServiceErrorClassificationSurvivesWrapping 是本文件**最重要**的一条。
//
// 这四个函数都用 `errors.Is` 而不是 `==`，而这不是风格问题：
// 内核的错误在向上传递的过程中会被 `fmt.Errorf("...: %w", err)` 层层包装
// （服务管理器 → 平台层 → API 层 → CLI）。用 `==` 比较的话，任何一层包装
// 都会让它**静默失效** —— 于是"服务已经装过了"变成一句红色的失败，
// 而用户会去手工卸载一个本来就装好的服务。
//
// 这条测试专门用包装过的错误来验证。
func TestServiceErrorClassificationSurvivesWrapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		code  error
		check func(error) bool
	}{
		{"未安装", windows.ERROR_SERVICE_DOES_NOT_EXIST, isNotInstalled},
		{"已在运行", windows.ERROR_SERVICE_ALREADY_RUNNING, isAlreadyRunning},
		{"未在运行", windows.ERROR_SERVICE_NOT_ACTIVE, isNotRunning},
		{"权限不足", windows.ERROR_ACCESS_DENIED, isAccessDenied},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 裸错误。
			if !tc.check(tc.code) {
				t.Errorf("裸错误应当被识别")
			}

			// 包装一层。
			wrapped := fmt.Errorf("安装服务失败: %w", tc.code)
			if !tc.check(wrapped) {
				t.Errorf("包装一层的错误应当被识别 —— " +
					"内核的错误在向上传递时会被层层包装，" +
					"用 == 比较会静默失效")
			}

			// 包装三层，混入无关信息。
			deep := fmt.Errorf("api: %w",
				fmt.Errorf("platform: %w",
					fmt.Errorf("安装服务失败: %w", tc.code)))
			if !tc.check(deep) {
				t.Errorf("多层包装的错误应当被识别")
			}
		})
	}
}

// TestServiceErrorClassificationRejectsOthers 是上一条的反面。
//
// 防止"全都返回 true"式的假通过 —— 那个方向的错误更危险：
// 任何失败都会被当成"服务已经装好了"，于是内核静默地没有装上。
func TestServiceErrorClassificationRejectsOthers(t *testing.T) {
	t.Parallel()

	// 每个分类函数都必须对**其它**三个错误码说 false。
	checks := map[string]func(error) bool{
		"isNotInstalled":   isNotInstalled,
		"isAlreadyRunning": isAlreadyRunning,
		"isNotRunning":     isNotRunning,
		"isAccessDenied":   isAccessDenied,
	}
	codes := map[string]error{
		"未安装":  windows.ERROR_SERVICE_DOES_NOT_EXIST,
		"已在运行": windows.ERROR_SERVICE_ALREADY_RUNNING,
		"未在运行": windows.ERROR_SERVICE_NOT_ACTIVE,
		"权限不足": windows.ERROR_ACCESS_DENIED,
	}

	for checkName, check := range checks {
		for codeName, code := range codes {
			if strings.TrimPrefix(checkName, "is") == "" {
				continue
			}
			// 同名的那一对允许为 true，其余必须为 false。
			if isMatchingPair(checkName, codeName) {
				continue
			}
			if check(code) {
				t.Errorf("%s 不应当把 %q 判为真 —— "+
					"那会让用户看到完全错误的处置建议",
					checkName, codeName)
			}
		}
		// 一个无关的错误也必须为 false。
		if check(errors.New("something else entirely")) {
			t.Errorf("%s 不应当把无关错误判为真", checkName)
		}
		if check(nil) {
			t.Errorf("%s 对 nil 应当返回 false", checkName)
		}
	}
}

// isMatchingPair 报告某个分类函数与某个错误码是否是"本该匹配"的那一对。
func isMatchingPair(checkName, codeName string) bool {
	return (checkName == "isNotInstalled" && codeName == "未安装") ||
		(checkName == "isAlreadyRunning" && codeName == "已在运行") ||
		(checkName == "isNotRunning" && codeName == "未在运行") ||
		(checkName == "isAccessDenied" && codeName == "权限不足")
}

// ---------------------------------------------------------------------------
// 状态映射
// ---------------------------------------------------------------------------

// TestMapWindowsState 覆盖 Windows 服务状态到本项目状态的映射。
//
// 中间的"过渡态"是容易出错的地方：
//
//	StartPending  正在启动 —— 归到 Running（它**将要**运行）
//	StopPending   正在停止 —— 归到 Stopped
//	PausePending  正在暂停 —— 归到 Stopped（暂停的服务不提供服务）
//
// 把过渡态归到 Unknown 会让"刚点了启动"的那几秒里界面显示"未知"，
// 而用户会以为启动失败了。
func TestMapWindowsState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   svc.State
		want ServiceStatus
		why  string
	}{
		{svc.Running, ServiceRunning, "运行中"},
		{svc.StartPending, ServiceRunning, "正在启动 —— 它将要运行"},
		{svc.ContinuePending, ServiceRunning, "正在恢复 —— 它将要运行"},
		{svc.Stopped, ServiceStopped, "已停止"},
		{svc.StopPending, ServiceStopped, "正在停止"},
		{svc.Paused, ServiceStopped, "已暂停 —— 暂停的服务不提供服务"},
		{svc.PausePending, ServiceStopped, "正在暂停"},
	}
	for _, tc := range cases {
		if got := mapWindowsState(tc.in); got != tc.want {
			t.Errorf("状态 %d（%s）映射为 %v，期望 %v",
				tc.in, tc.why, got, tc.want)
		}
	}

	// 未知状态要如实报 Unknown，而不是猜一个。
	if got := mapWindowsState(svc.State(9999)); got != ServiceUnknown {
		t.Errorf("未知状态应当映射为 ServiceUnknown，得到 %v", got)
	}
}

// TestMapWindowsStateNeverReturnsEmpty 钉住"永不返回零值"。
//
// ServiceStatus 的零值是空字符串，而空字符串在界面上会显示成一片空白。
func TestMapWindowsStateNeverReturnsEmpty(t *testing.T) {
	t.Parallel()

	for i := 0; i < 12; i++ {
		if got := mapWindowsState(svc.State(i)); got == "" {
			t.Errorf("状态 %d 映射成了空字符串 —— 界面上会是一片空白", i)
		}
	}
}

// ---------------------------------------------------------------------------
// 注册配置
// ---------------------------------------------------------------------------

// TestBuildBinaryPathQuotesSpacedPath 钉住带空格的路径必须被引号包起来。
//
// Windows 的 ImagePath 是一整条命令行，因此没有引号的
// `C:\Program Files\isc\isc.exe daemon run` 会被解析成
// 程序 `C:\Program` 加三个参数 —— 而错误信息只会说"系统找不到指定的文件"。
//
// 而用户把内核装在 `C:\Program Files\` 下是**最常见**的选择。
func TestBuildBinaryPathQuotesSpacedPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  ServiceConfig
		want string
	}{
		{
			name: "无空格的路径不加引号",
			cfg: ServiceConfig{
				Executable: `C:\isc\isc.exe`,
				Arguments:  []string{"daemon", "run"},
			},
			want: `C:\isc\isc.exe daemon run`,
		},
		{
			name: "带空格的路径必须加引号",
			cfg: ServiceConfig{
				Executable: `C:\Program Files\isc\isc.exe`,
				Arguments:  []string{"daemon", "run"},
			},
			want: `"C:\Program Files\isc\isc.exe" daemon run`,
		},
		{
			name: "制表符也算分隔符",
			cfg: ServiceConfig{
				Executable: "C:\\a\tb\\isc.exe",
				Arguments:  []string{"daemon"},
			},
			want: "\"C:\\a\tb\\isc.exe\" daemon",
		},
		{
			name: "没有参数时只有可执行文件",
			cfg: ServiceConfig{
				Executable: `C:\isc\isc.exe`,
			},
			want: `C:\isc\isc.exe`,
		},
		{
			name: "带空格的路径且没有参数",
			cfg: ServiceConfig{
				Executable: `C:\Program Files\isc\isc.exe`,
			},
			want: `"C:\Program Files\isc\isc.exe"`,
		},
	}

	for _, tc := range cases {
		if got := buildBinaryPath(tc.cfg); got != tc.want {
			t.Errorf("%s:\n  得到 %q\n  期望 %q", tc.name, got, tc.want)
		}
	}
}

// TestStartTypeOf 验证自启开关到注册类型的映射。
func TestStartTypeOf(t *testing.T) {
	t.Parallel()

	if got := startTypeOf(ServiceConfig{AutoStart: true}); got != mgr.StartAutomatic {
		t.Errorf("AutoStart=true 应当映射为 StartAutomatic，得到 %d", got)
	}
	if got := startTypeOf(ServiceConfig{AutoStart: false}); got != mgr.StartManual {
		t.Errorf("AutoStart=false 应当映射为 StartManual，得到 %d", got)
	}
}

// TestDisplayNameAndDescriptionFallBack 验证默认值与覆盖。
func TestDisplayNameAndDescriptionFallBack(t *testing.T) {
	t.Parallel()

	// 没有给值时用默认值。
	def := ServiceConfig{}
	if displayNameOf(def) == "" {
		t.Error("显示名不该为空 —— 它在 services.msc 里是唯一的标识")
	}
	if descriptionOf(def) == "" {
		t.Error("描述不该为空")
	}

	// 给了值时用给的值。
	custom := ServiceConfig{DisplayName: "我的 ISC", Description: "自定义描述"}
	if got := displayNameOf(custom); got != "我的 ISC" {
		t.Errorf("显示名是 %q，期望用配置里的值", got)
	}
	if got := descriptionOf(custom); got != "自定义描述" {
		t.Errorf("描述是 %q，期望用配置里的值", got)
	}
}
