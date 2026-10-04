// Package audit 记录内核的写操作。
//
// 存在的意义：内核以系统服务身份运行、能改防火墙、能重写整个 DNS 区域。
// 出问题时"最后一次改动是什么、谁改的、结果如何"是排查的第一入口。
//
// 设计上的两条硬规则：
//
//  1. **审计失败不能阻断业务**。写审计出错时记日志即可，不能让用户的
//     合法操作失败 —— 审计的价值在于事后追溯，不在于阻止操作。
//     反过来做会让"磁盘满了"升级成"什么都干不了"。
//  2. **审计内容绝不含敏感值**。detail 里只放对象标识与结果，
//     不放密钥、令牌、密码。审计表往往比业务表被更多人看到。
package audit

import (
	"context"
	"log/slog"
	"time"
)

// 结果常量。它们是机器可读的稳定值。
//
// # 为什么没有 "denied"
//
// 这里曾经有一个 ResultDenied，但**没有任何地方会写它** —— 认证失败
// 不写审计。想清楚之后那个决定是对的，而缺的只是把它写下来：
//
// 令牌存在于一个用户可读的文件里，而接口只监听本机（命名管道 /
// 回环 + 令牌）。**猜令牌在这个接口上不是一个有意义的威胁** ——
// 能读到令牌文件的进程根本不需要猜，读不到的进程也够不着那个管道。
//
// 而记录每一次 401 会把噪音灌进审计日志：内核重启后控制台拿着
// 旧令牌重试是常态，而那与"有人在试探"完全无法区分。
//
// 删掉这个常量而不是留着它：一个定义了却没人用、又没说清为什么的
// 常量是一个陷阱 —— 迟早有人会把它接上，然后开始记录那些噪音。
const (
	ResultSuccess = "success"
	ResultFailure = "failure"
)

// 动作常量。
//
// 命名约定 `<领域>.<动作>`，与 API 的资源命名保持一致。
// 这些值会出现在客户的查询条件里，**一旦发布不可更改**。
const (
	ActionCredentialCreate = "credential.create"
	ActionCredentialUpdate = "credential.update"
	ActionCredentialDelete = "credential.delete"
	ActionCredentialVerify = "credential.verify"

	ActionSettingsUpdate = "settings.update"

	ActionTaskCreate = "ddns_task.create"
	ActionTaskUpdate = "ddns_task.update"
	ActionTaskDelete = "ddns_task.delete"

	ActionChangePlan     = "change.plan"
	ActionChangeApply    = "change.apply"
	ActionChangeRollback = "change.rollback"

	ActionProxyRoutes = "proxy.routes"

	// Phecda 的公网服务集合（整体替换，与代理路由同形）。
	ActionPublicServices = "phecda.public_services"

	// 建站侧（v0.2.0，见 D25）。
	ActionSourceInspect    = "hosting.source_inspect"
	ActionRuntimeProvision = "hosting.runtime_provision"
	ActionRuntimeRemove    = "hosting.runtime_remove"

	ActionCertRenew      = "cert.renew"
	ActionNotifyChannels = "notify.channels"

	// 系统服务。
	ActionServiceInstall   = "service.install"
	ActionServiceUninstall = "service.uninstall"
	ActionServiceStart     = "service.start"
	ActionServiceStop      = "service.stop"

	ActionRecordCreate = "dns_record.create"
	ActionRecordUpdate = "dns_record.update"
	ActionRecordDelete = "dns_record.delete"

	ActionConfigImport       = "config.import"
	ActionConfigImportDdnsGo = "config.import.ddnsgo"
	ActionConfigExport       = "config.export"
)

// Record 是一条审计记录。
type Record struct {
	// ID 由存储层分配，写入时留空。
	ID int64

	TS     string
	Action string
	Target string
	Result string
	Detail string

	// RequestID 关联到产生这次操作的 HTTP 请求，
	// 便于把审计记录与日志对上。
	RequestID string

	// Remote 描述来源。本机管理通道下通常是传输类型（命名管道 /
	// Unix 套接字 / 回环），而不是 IP —— 记 IP 在这里没有意义。
	Remote string
}

// Filter 是审计查询条件。
type Filter struct {
	Action string
	Result string
	Cursor string
	Limit  int
}

// Writer 是审计记录的持久化接口。
type Writer interface {
	AppendAudit(ctx context.Context, rec Record) error
	ListAudit(ctx context.Context, f Filter) ([]Record, string, error)
}

// Recorder 是写审计的入口。
type Recorder struct {
	writer Writer
	log    *slog.Logger
}

// NewRecorder 构造审计记录器。
//
// writer 为 nil 时所有记录只写日志 —— 这让内核在没有数据库的场景
// （测试、极端降级）下依然能启动。
func NewRecorder(w Writer, log *slog.Logger) *Recorder {
	if log == nil {
		log = slog.Default()
	}
	return &Recorder{writer: w, log: log}
}

// Record 写入一条审计记录。
//
// 永远不返回错误：见本包文档的第 1 条硬规则。
func (r *Recorder) Record(ctx context.Context, action, target, result, detail, requestID, remote string) {
	if r == nil {
		return
	}
	rec := Record{
		TS:        time.Now().UTC().Format(time.RFC3339Nano),
		Action:    action,
		Target:    target,
		Result:    result,
		Detail:    detail,
		RequestID: requestID,
		Remote:    remote,
	}

	if r.writer == nil {
		return
	}
	if err := r.writer.AppendAudit(ctx, rec); err != nil {
		r.log.Warn("写入审计失败（业务操作不受影响）",
			"action", action, "target", target, "err", err)
	}
}

// Success 记录一次成功。
func (r *Recorder) Success(ctx context.Context, action, target, detail, requestID, remote string) {
	r.Record(ctx, action, target, ResultSuccess, detail, requestID, remote)
}

// Failure 记录一次失败。
func (r *Recorder) Failure(ctx context.Context, action, target, detail, requestID, remote string) {
	r.Record(ctx, action, target, ResultFailure, detail, requestID, remote)
}
