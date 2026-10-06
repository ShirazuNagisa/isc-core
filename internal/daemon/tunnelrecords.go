package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件把"域名指向隧道"这件事做完。
//
// # 为什么它必须替换掉动态解析，而不是并存
//
// 直连模式下域名靠动态解析任务维护 A/AAAA（家宽地址会变）。隧道模式下
// 那个地址**不再被使用** —— 客户端连的是 Cloudflare 边缘，不是这台机器。
// 两条路同时维护同一个域名会打架：动态解析把 CNAME 覆盖成 AAAA，于是
// 域名指向一个外部根本连不上的地址，而站点在界面上显示一切正常。
//
// 因此隧道就绪时**只**走这一条，并且把已有的 A/AAAA 改写成 CNAME。

// tunnelRecordTTL 是隧道记录用的 TTL。
//
// 取 60 而不是默认值：隧道 id 不变时这条记录基本不动，但用户可能删掉
// 隧道重建 —— 那时 TTL 太长会让"改了却还连到旧隧道"持续很久。
const tunnelRecordTTL = 60

// ensureTunnelHost 把某个域名指向隧道。
//
// 幂等：已经是正确的 CNAME 就什么都不做。已存在但类型/内容不对时**改写
// 它**而不是新建一条 —— 同一个名字下有两条地址记录是 DNS 里最容易让人
// 困惑的状态之一，而客户端会在其中随机挑。
func (b *appBinder) ensureTunnelHost(ctx context.Context, domain string) error {
	if b.zoneFinder == nil || b.dns == nil || b.tunnel == nil {
		return errors.New("tunnel: dns binding is unavailable in this build")
	}
	target := b.tunnel.Hostname()
	if target == "" {
		return errors.New("tunnel: no tunnel hostname yet")
	}

	credentialID, zone, err := b.zoneFinder.Find(ctx, domain)
	if err != nil {
		return fmt.Errorf("could not find the zone for %s: %w", domain, err)
	}

	want := dns.Record{
		Name:    domain,
		Type:    dns.TypeCNAME,
		Content: target,
		TTL:     tunnelRecordTTL,
		// 橙云是**必须**的：这条 CNAME 指向 Cloudflare 自己的隧道端点，
		// 只有经由 Cloudflare 才解析得到。灰云会让它变成一条谁也解不开
		// 的记录 —— 而症状是"域名存在但打不开"。
		Proxied: true,
	}

	records, err := b.dns.ListRecords(ctx, credentialID, zone.ID, dns.RecordFilter{Name: domain})
	if err != nil {
		return err
	}
	for _, rec := range records {
		if !strings.EqualFold(strings.TrimSpace(rec.Name), domain) {
			continue
		}
		if rec.Type == dns.TypeCNAME && rec.Content == target && rec.Proxied {
			return nil // 已经是对的。
		}
		_, err := b.dns.UpdateRecord(ctx, credentialID, zone.ID, rec.ID, want)
		return err
	}

	_, err = b.dns.CreateRecord(ctx, credentialID, zone.ID, want)
	return err
}

// removeTunnelHost 撤销某个域名到隧道的指向。
//
// 只删**指向我们这条隧道**的那一条：同一个名字下可能有用户自己建的记录，
// 删应用时把它一并删掉是越权（与 RemoveRoute 同样的理由）。
func (b *appBinder) removeTunnelHost(ctx context.Context, domain string) error {
	if b.zoneFinder == nil || b.dns == nil || b.tunnel == nil {
		return nil
	}
	target := b.tunnel.Hostname()
	if target == "" {
		return nil
	}
	credentialID, zone, err := b.zoneFinder.Find(ctx, domain)
	if err != nil {
		return err
	}
	records, err := b.dns.ListRecords(ctx, credentialID, zone.ID, dns.RecordFilter{Name: domain})
	if err != nil {
		return err
	}
	for _, rec := range records {
		if !strings.EqualFold(strings.TrimSpace(rec.Name), domain) {
			continue
		}
		if rec.Type != dns.TypeCNAME || rec.Content != target {
			continue
		}
		return b.dns.DeleteRecord(ctx, credentialID, zone.ID, rec.ID)
	}
	return nil
}
