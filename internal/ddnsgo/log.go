package ddnsgo

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件把 ddns-go 的 util.Log / util.LogStr 桥接到 ISC 的日志与 i18n。
//
// # 为什么这里允许一个包级全局
//
// 项目里其它地方一律禁止包级可变状态（见 docs/PLAN.md R11）。这里是
// 刻意的例外，理由有三条：
//
//  1. 移植过来的 provider 里有 233 处 `util.Log(...)` 调用。把它们改成
//     方法调用意味着逐行改写，而逐行改写正是移植中最容易出错的部分 ——
//     收益只是"形式更纯"，代价是可能引入难以发现的缺陷。
//  2. 它**不参与任何逻辑判断**。危险的不是全局变量本身，而是"被中途修改
//     且影响行为"的全局状态（ddns-go 的 util.ForceCompareGlobal、
//     dns.Ipcache 正是这一类，它们在本包里已被移除）。
//     日志落点只写一次、之后只读。
//  3. 用 atomic.Pointer 保证并发安全，且未设置时退化为 slog.Default()，
//     不会出现 nil 解引用。
var logger atomic.Pointer[slog.Logger]

// SetLogger 设置本包的日志落点。进程启动时调用一次。
func SetLogger(l *slog.Logger) {
	if l == nil {
		return
	}
	logger.Store(l)
}

func log() *slog.Logger {
	if l := logger.Load(); l != nil {
		return l
	}
	return slog.Default()
}

// Log 记录一条消息，签名与 ddns-go 的 util.Log 完全一致。
//
// 参数 key 是**格式串兼 i18n 消息 key**：ddns-go 直接拿中文原文当 key，
// 本包沿用这一约定（对应的英文翻译已并入 internal/i18n 的目录）。
// 未登记的 key 会原样输出中文，不会丢失信息。
//
// 级别固定为 Info。失败语义不靠日志级别表达 —— provider 通过返回的
// Domains 上每条域名的 UpdateStatus 表达成功与否，上层据此产生事件与
// 通知。用日志文本猜级别是脆弱的，这里刻意不做。
func Log(key string, args ...interface{}) {
	msg := format(key, args)
	log().Info(msg)
}

// LogStr 返回格式化后的消息，不写日志。
//
// ddns-go 用它构造要向上返回的错误文本。
func LogStr(key string, args ...interface{}) string {
	return format(key, args)
}

// format 先走 i18n 目录翻译格式串，再填充参数。
func format(key string, args []interface{}) string {
	tmpl := i18n.T(key)
	if len(args) == 0 {
		return tmpl
	}
	return fmt.Sprintf(tmpl, args...)
}
