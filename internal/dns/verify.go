package dns

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件实现凭据校验（"测试连接"）。
//
// # 为什么放在 dns.Service 上
//
// 校验需要两样东西：**解密后的凭据**与**服务商实现**。而这两者的
// 组合逻辑已经在 resolve 里了 —— 在 API 层重新拼一遍意味着那段
// 逻辑有两份实现，而它们迟早会漂移。
//
// 这也是本项目里一个反复出现的教训：共用的编排逻辑必须有**唯一的
// 落点**，而不是"碰巧和某个调用方写在一起"。

// Verify 校验一个凭据是否可用。
//
// 返回的 error 有两种性质完全不同的情况，调用方**必须区分**：
//
//	nil                     校验通过
//	ErrVerifyUnsupported    该服务商不支持校验 —— 不是失败
//	其它                    真的校验失败了，错误来自服务商
//
// 把它们混在一起会让用户去查一个不存在的连接问题：阿里云 / 腾讯云 /
// 华为云 / GoDaddy 都没有只读的校验端点，而"不支持"与"连不上"
// 需要用户采取的行动完全不同。
func (s *Service) Verify(ctx context.Context, credentialID string) error {
	cred, impl, err := s.resolve(ctx, credentialID)
	if err != nil {
		return err
	}

	verifier, ok := impl.(Verifier)
	if !ok {
		return fmt.Errorf("%w：%s", ErrVerifyUnsupported, cred.Provider)
	}
	return verifier.Verify(ctx, cred)
}

// VerifySupported 报告某个凭据的服务商是否支持校验。
//
// 界面用它决定"测试连接"按钮该不该置灰 —— 而不是让用户点一下
// 才发现这家根本不支持。
func (s *Service) VerifySupported(ctx context.Context, credentialID string) bool {
	_, impl, err := s.resolve(ctx, credentialID)
	if err != nil {
		return false
	}
	_, ok := impl.(Verifier)
	return ok
}

// ErrVerifyUnsupported 表示该服务商不支持凭据校验。
//
// 它**不是失败**：这些服务商没有只读的校验端点，而用"列一次域名"
// 来冒充会要求额外的权限 —— 那会把只有 DNS 编辑权限的最小权限账号
// 误判为无效。
var ErrVerifyUnsupported = errors.New(i18n.T("dns.err.no_verify"))
