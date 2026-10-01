package configio

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ShirazuNagisa/isc-core/internal/credential"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/provider"
)

// 本文件把 ddns-go 的配置迁移成 ISC 的实体。
//
// ddns-go 的配置结构（`~/.ddns_go_config.yaml`）里，每条 `dnsconf`
// 条目把"用哪家服务商 × 用哪组凭据 × 解析哪些域名 × 怎么获取 IP"
// 全部揉在一起。ISC 把它们拆成两类实体：
//
//	凭据      服务商 + 一组字段（去重后单独存）
//	解析任务  凭据 + 域名 + IP 获取方式（M2 接入调度器后生效）
//
// 字段映射是**位置式**的：ddns-go 只提供 id / secret / extparam 三个
// 位置化的值，按目标服务商声明的字段顺序依次填入。这让迁移逻辑对
// 新增服务商保持透明，不需要为每家写一张转换表。
//
// 之所以用显式的 yaml tag 而不是依赖 yaml.v3 的字段名小写化默认行为：
// 后者是"隐式契约"，一旦有人重命名 Go 字段就会静默地让解析全部落空，
// 而症状是"导入后什么都没发生"，极难归因。

// ddnsGoDocument 是 ddns-go 配置文件的结构（只取我们需要的部分）。
//
// 其余字段（user / webhook / lang 等）刻意不解析：ISC 有自己的
// 用户体系与通知中心，把 ddns-go 的 bcrypt 密码哈希搬过来没有意义。
type ddnsGoDocument struct {
	DnsConf []ddnsGoEntry `yaml:"dnsconf"`

	// WebhookURL 被识别出来只是为了给用户一句明确的提示
	// （ISC 的通知中心在 M4 接入），而不是静默丢弃。
	WebhookURL string `yaml:"webhookurl"`
}

type ddnsGoEntry struct {
	Ipv4          ddnsGoIPSource `yaml:"ipv4"`
	Ipv6          ddnsGoIPSource `yaml:"ipv6"`
	DNS           ddnsGoDNS      `yaml:"dns"`
	TTL           string         `yaml:"ttl"`
	HTTPInterface string         `yaml:"httpinterface"`
}

type ddnsGoIPSource struct {
	Enable       bool     `yaml:"enable"`
	GetType      string   `yaml:"gettype"`
	URL          string   `yaml:"url"`
	NetInterface string   `yaml:"netinterface"`
	Cmd          string   `yaml:"cmd"`
	Ipv6Reg      string   `yaml:"ipv6reg"`
	Domains      []string `yaml:"domains"`
}

type ddnsGoDNS struct {
	Name     string `yaml:"name"`
	ID       string `yaml:"id"`
	Secret   string `yaml:"secret"`
	ExtParam string `yaml:"extparam"`
}

// ImportDdnsGo 迁移一份 ddns-go 配置。
func (s *Service) ImportDdnsGo(ctx context.Context, body []byte, dryRun bool) (Result, error) {
	var doc ddnsGoDocument
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	if len(doc.DnsConf) == 0 {
		return Result{}, ErrNotDdnsGo
	}

	res := Result{DryRun: dryRun}

	// 同一条 ddns-go 配置里，多条 dnsconf 常常共用同一组凭据
	// （比如同一个阿里云账号解析十几个域名）。按"服务商 + 字段内容"
	// 去重，避免导入后出现十几条一模一样的凭据。
	type credKey struct{ provider, fingerprint string }
	seen := make(map[credKey]bool)
	labelsUsed := make(map[string]int)
	var recognizedConfigs int

	for idx, entry := range doc.DnsConf {
		providerName := strings.TrimSpace(entry.DNS.Name)
		if providerName == "" {
			res.Errors = append(res.Errors, i18n.T("config.import.skipped", idx+1, "该条目未指定服务商"))
			continue
		}

		spec, ok := s.registry.Get(providerName)
		if !ok {
			res.Errors = append(res.Errors, i18n.T("config.import.skipped",
				idx+1, fmt.Sprintf("内核不认识服务商 %q", providerName)))
			continue
		}

		fields := mapDdnsGoFields(spec, entry.DNS)
		key := credKey{provider: providerName, fingerprint: fingerprintFields(spec, fields)}
		if seen[key] {
			// 已导入过同一组凭据，跳过但不算失败。
			recognizedConfigs += countEnabledSources(entry)
			continue
		}
		seen[key] = true
		labelsUsed[providerName]++

		label := spec.DisplayName
		if labelsUsed[providerName] > 1 {
			label = fmt.Sprintf("%s (%d)", spec.DisplayName, labelsUsed[providerName])
		}

		if err := s.importCredential(ctx, ExportCredential{
			Provider: providerName, Label: label, Fields: fields,
		}, dryRun, &res); err != nil {
			res.Errors = append(res.Errors, i18n.T("config.import.skipped", idx+1, err.Error()))
			res.Summary.CredentialsSkipped++
			continue
		}

		recognizedConfigs += countEnabledSources(entry)
	}

	if doc.WebhookURL != "" {
		res.Warnings = append(res.Warnings, i18n.T("config.import.webhook_unsupported"))
	}

	// 动态解析任务的迁移要等 M2 的调度器就位。此时如实说明，
	// 而不是假装成功 —— 用户需要知道"域名还没开始解析"。
	if recognizedConfigs > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"识别到 %d 条启用中的动态解析配置；解析任务的迁移与调度将在 M2 接入后生效",
			recognizedConfigs))
	}

	if dryRun {
		res.Warnings = append(res.Warnings, i18n.T("config.import.dry_run"))
	}
	return res, nil
}

// mapDdnsGoFields 把 ddns-go 的槽位映射到目标服务商的字段。
//
// **按字段声明的 DdnsGoSlot 映射，而不是按声明顺序。**
// ddns-go 的模型永远是 (id, secret, extparam) 三元组，但各服务商真正
// 用到的槽位不同 —— Cloudflare 只用 secret（API Token）。
// 早先按顺序映射的版本会让 Cloudflare 拿到空的 id 槽位，导致
// "导入成功但凭据是空的"，而错误直到真正解析时才暴露。
//
// 槽位是 1 基的，零值（忘了声明）会被安全忽略 —— 见 credential.DdnsGoSlot。
func mapDdnsGoFields(spec provider.Provider, dns ddnsGoDNS) map[string]string {
	slots := [3]string{
		strings.TrimSpace(dns.ID),
		strings.TrimSpace(dns.Secret),
		strings.TrimSpace(dns.ExtParam),
	}

	out := make(map[string]string, len(spec.CredentialFields))
	for _, f := range spec.CredentialFields {
		idx := f.DdnsGoSlot - 1 // 1 基 → 0 基
		if idx < 0 || idx >= len(slots) {
			continue
		}
		if v := slots[idx]; v != "" {
			out[f.Key] = v
		}
	}
	return out
}

// fingerprintFields 生成凭据内容的指纹，用于去重。
//
// 按字段定义的声明顺序拼接，因此同一组凭据无论 map 的遍历顺序如何
// 都会得到相同的指纹（map 遍历顺序在 Go 中是随机的，直接拼接 map
// 会让去重完全失效）。
func fingerprintFields(spec provider.Provider, fields map[string]string) string {
	keys := credential.FieldKeys(spec.CredentialFields)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(fields[k])
		b.WriteByte('\x00')
	}
	return b.String()
}

// countEnabledSources 统计该条目里启用中的 IP 来源数量。
func countEnabledSources(e ddnsGoEntry) int {
	n := 0
	if e.Ipv4.Enable && len(nonEmpty(e.Ipv4.Domains)) > 0 {
		n++
	}
	if e.Ipv6.Enable && len(nonEmpty(e.Ipv6.Domains)) > 0 {
		n++
	}
	return n
}

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	sort.Strings(out)
	return out
}

// ErrNotDdnsGo 表示内容不是一份 ddns-go 配置。
var ErrNotDdnsGo = errors.New("configio: 这不是一份 ddns-go 配置（缺少 dnsconf 段）")
