package api

import (
	"context"
	"errors"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net/http"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件实现「测试连接」。
//
// # 它为什么需要一个端点
//
// 在此之前，控制台的"测试连接"按钮调的是 POST /v1/credentials/{id}/verify ——
// **而那个端点根本不在接口里**。也就是说那个按钮从来没有工作过。
//
// 底层能力一直是齐全的：dns.Verifier 接口、Cloudflare 的实现、以及
// ProviderCapabilities.Verify 这个能力位。缺的只是把它暴露出来的这一步。
//
// 这是本项目里第三次遇到同一类缺口（cert.renewed 从未发出、CheckBlocked
// 从未设置、以及这个）：**能力在、常量在、界面引用了它，但中间那一段
// 从来没有人接上**。它们的共同特征是编译、测试、日志都不会有任何提示。

// verifyTimeout 是单次校验的超时。
//
// 校验只做一次轻量的只读请求，而 15 秒足够 —— 超过它多半是网络问题，
// 而让用户对着一个转圈的界面等更久没有意义。
const verifyTimeout = 15 * time.Second

// VerifyCredential 实现 POST /v1/credentials/{id}/verify。
func (s *Server) VerifyCredential(w http.ResponseWriter, r *http.Request, id string) {
	if s.DNS == nil {
		s.internalError(w, r, i18n.T("api.verify.no_service"),
			errors.New(i18n.T("api.verify.no_dns")))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), verifyTimeout)
	defer cancel()

	err := s.DNS.Verify(ctx, id)

	// 把结果记进凭据（last_verified_at / last_error）。
	//
	// 失败**不阻断响应**：校验结果已经拿到了，而记录失败只影响
	// 界面上的一个时间戳。但要用一个**独立的上下文** —— 校验超时
	// 时 ctx 已经取消了，而那时我们仍然需要把"校验超时了"这件事
	// 记下来。
	if s.Credentials != nil {
		if markErr := s.Credentials.MarkVerified(
			context.WithoutCancel(r.Context()), id, err); markErr != nil {
			s.Log.Warn(i18n.T("api.verify.save_failed"), "id", id, "err", markErr)
		}
	}

	// 校验会更新凭据的 last_verified_at，因此算一次写操作 —— 审计它。
	//
	// 注意 ActionCredentialVerify 这个常量一直存在，而**上一轮加这个
	// 端点时我忘了接上它**。这是本轮扫描（找出"定义了却没人用"的
	// 常量和函数）发现的。
	switch {
	case err == nil:
		s.auditSuccess(r, audit.ActionCredentialVerify, "credential", id)
		writeJSON(w, s.Log, http.StatusOK, "application/json",
			verifyResult(true, i18n.T("api.verify.ok")))

	case errors.Is(err, dns.ErrVerifyUnsupported):
		// **不支持校验不是失败。**
		//
		// 阿里云 / 腾讯云 / 华为云 / GoDaddy 没有只读的校验端点，而
		// 用"列一次域名"来冒充会要求额外的权限 —— 那会把只有 DNS 编辑
		// 权限的最小权限账号误判为无效。
		//
		// 这里必须说清是"不支持"而不是i18n.T("api.verify.mark_unreachable")，否则用户会去查一个
		// 根本不存在的连接问题。
		writeJSON(w, s.Log, http.StatusOK, "application/json",
			verifyResult(false,
				i18n.T("api.verify.unsupported")))

	default:
		s.auditFailure(r, audit.ActionCredentialVerify, "credential", err)
		writeJSON(w, s.Log, http.StatusOK, "application/json",
			verifyResult(false, humanizeVerifyError(err)))
	}
}

// verifyResult 构造响应。
func verifyResult(ok bool, msg string) gen.CredentialVerifyResult {
	return gen.CredentialVerifyResult{Ok: ok, Message: &msg}
}

// humanizeVerifyError 把服务商的错误翻译成用户能据此行动的话。
//
// 各家返回的错误文本格式完全不同（有的是 JSON、有的是纯文本、
// 有的是 HTTP 状态码），而共同点是**它们都不告诉用户该去改什么**。
//
// 这里只区分两类最常见的，因为它们的"下一步"截然不同：
//
//	权限不足   去改 Token 的权限范围
//	网络问题   去查这台机器的出网能力
//
// 认不出来的原样返回 —— 编一个可能错的诊断比不诊断更糟。
func humanizeVerifyError(err error) string {
	msg := err.Error()
	lower := strings.ToLower(msg)

	for _, marker := range []string{
		"403", "401", "authentication", "unauthorized", "forbidden",
		"permission", "denied", "invalid token", i18n.T("api.verify.mark_auth"), i18n.T("api.verify.mark_auth2"), i18n.T("api.verify.mark_perm"),
	} {
		if strings.Contains(lower, marker) {
			return msg + i18n.T("api.verify.hint_auth")
		}
	}

	for _, marker := range []string{
		"timeout", "deadline", "connection refused", "no such host",
		i18n.T("api.verify.mark_timeout"), i18n.T("api.verify.mark_unreachable"), "dial", "eof",
	} {
		if strings.Contains(lower, marker) {
			return msg + i18n.T("api.verify.hint_network")
		}
	}

	return msg
}
