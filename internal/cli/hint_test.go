package cli

import (
	"strings"
	"testing"
)

// 本文件守住"内核没找到"时那句提示的**判断**（说不说），而不是它的措辞。
//
// 背景是 2026-10-03 在 macOS 真机上看到的：
//
//	$ isc status
//	内核未运行。请先执行 'isc daemon run' 或安装为系统服务。
//
// 而内核完全可能**已经**装成系统服务在跑 —— 只是它用系统目录
//（/Library/Application Support/ISC 或 /var/lib/isc），而普通用户的 CLI
// 用的是自己的回退目录（D20 的"后果"那一节写的就是这件事）。
// 两句话指向完全不同的下一步动作，因此必须区分开。

func TestSystemDirHintOnlyWhenItHelps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		current     string
		sys         string
		sysExists   bool
		wantMessage bool
		why         string
	}{
		{
			name: "系统目录存在且与我无关", current: "/Users/x/Library/Application Support/ISC",
			sys: "/Library/Application Support/ISC", sysExists: true, wantMessage: true,
			why: "这正是要提示的情形：内核可能在系统目录里跑着",
		},
		{
			name: "系统目录不存在", current: "/Users/x/Library/Application Support/ISC",
			sys: "/Library/Application Support/ISC", sysExists: false, wantMessage: false,
			why: "没装过系统服务 —— 提示只会让人以为自己漏装了什么",
		},
		{
			name: "我自己就在系统目录里（以 root 运行）", current: "/Library/Application Support/ISC",
			sys: "/Library/Application Support/ISC", sysExists: true, wantMessage: false,
			why: "找的就是同一个地方，再提示一遍是噪音",
		},
		{
			name: "平台没有系统目录", current: "/home/x/.local/share/isc",
			sys: "", sysExists: false, wantMessage: false,
			why: "freebsd 等平台走 platform_other，那里没有这个概念",
		},
		{
			name: "路径还没解析", current: "",
			sys: "/Library/Application Support/ISC", sysExists: true, wantMessage: false,
			why: "空路径说明状态未知，不该据此下结论",
		},
	}

	for _, tc := range cases {
		got := systemDirHint(tc.current, tc.sys, tc.sysExists)
		if tc.wantMessage && got == "" {
			t.Errorf("%s：应当给出提示（%s）", tc.name, tc.why)
		}
		if !tc.wantMessage && got != "" {
			t.Errorf("%s：不该给提示（%s），得到 %q", tc.name, tc.why, got)
		}
		// 提示里必须带上那个目录：只说"可能在别处"等于没说。
		if got != "" && !strings.Contains(got, tc.sys) {
			t.Errorf("%s：提示里没有说明是哪个目录：%q", tc.name, got)
		}
	}
}
