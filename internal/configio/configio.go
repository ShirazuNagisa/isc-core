// Package configio 实现配置的导出、导入与 ddns-go 迁移。
//
// 两条贯穿始终的原则：
//
//  1. **默认不导出明文密钥**。一个"顺手导出"产生的明文密钥文件
//     是常见的事故来源，因此导出必须显式要求 `include_secrets`。
//  2. **导入默认是预览**。导入是破坏性操作，先返回"将会发生什么"
//     让人确认，与 docs/DECISIONS.md D10 的"计划 → 预览 → 应用"
//     是同一套思路。
package configio

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ShirazuNagisa/isc-core/internal/credential"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/provider"
	"github.com/ShirazuNagisa/isc-core/internal/settings"
)

// FormatVersion 是导出文件的格式版本。
//
// 导入时据此判断能否理解这份文件。将来格式变化时可以据此做兼容处理，
// 而不是靠"字段在不在"来猜。
const FormatVersion = 1

// ---------------------------------------------------------------------------
// 导出格式
// ---------------------------------------------------------------------------

// Document 是导出的 YAML 结构。
type Document struct {
	FormatVersion int                `yaml:"format_version"`
	ExportedAt    string             `yaml:"exported_at"`
	Generator     string             `yaml:"generator"`
	Settings      *settings.Settings `yaml:"settings,omitempty"`
	Credentials   []ExportCredential `yaml:"credentials,omitempty"`
}

// ExportCredential 是导出文件中的一条凭据。
type ExportCredential struct {
	Provider string            `yaml:"provider"`
	Label    string            `yaml:"label"`
	Fields   map[string]string `yaml:"fields"`
}

// ---------------------------------------------------------------------------
// 导出
// ---------------------------------------------------------------------------

// Service 提供导出、导入与迁移。
type Service struct {
	credentials *credential.Service
	settings    *settings.Service
	registry    *provider.Registry
}

// New 构造服务。
func New(creds *credential.Service, set *settings.Service, reg *provider.Registry) *Service {
	return &Service{credentials: creds, settings: set, registry: reg}
}

// Export 导出配置。
//
// includeSecrets 为 false（默认）时，敏感字段写掩码值而不是明文。
// 这样导出文件可以安全地贴进 issue 或放进共享目录，代价是导入到
// 另一台机器后需要重新填写密钥 —— 这个代价是刻意的。
func (s *Service) Export(ctx context.Context, includeSecrets bool) ([]byte, error) {
	doc := Document{
		FormatVersion: FormatVersion,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		Generator:     "isc-core",
	}

	if s.settings != nil {
		cur := s.settings.Get()
		doc.Settings = &cur
	}

	// 逐页拉取，避免一次性把所有凭据读进内存（虽然规模很小，
	// 但这里顺便验证了分页逻辑在真实路径上可用）。
	cursor := ""
	for {
		items, next, err := s.credentials.List(ctx, cursor, 100)
		if err != nil {
			return nil, err
		}
		for _, c := range items {
			specs, _ := s.credentials.SpecsFor(c.Provider)
			fields := c.Fields
			if !includeSecrets {
				fields = credential.Masked(c.Fields, specs)
			}
			doc.Credentials = append(doc.Credentials, ExportCredential{
				Provider: c.Provider,
				Label:    c.Label,
				Fields:   fields,
			})
		}
		if next == "" {
			break
		}
		cursor = next
	}

	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("configio.err.marshal"), err)
	}
	return body, nil
}

// ---------------------------------------------------------------------------
// 导入
// ---------------------------------------------------------------------------

// Summary 是导入结果的统计。
type Summary struct {
	CredentialsCreated int `json:"credentials_created"`
	CredentialsUpdated int `json:"credentials_updated"`
	CredentialsSkipped int `json:"credentials_skipped"`
	TasksCreated       int `json:"tasks_created"`
	TasksUpdated       int `json:"tasks_updated"`
	TasksSkipped       int `json:"tasks_skipped"`
	SettingsUpdated    int `json:"settings_updated"`
}

// Result 是一次导入的结果。
type Result struct {
	DryRun   bool     `json:"dry_run"`
	Summary  Summary  `json:"summary"`
	Warnings []string `json:"warnings,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

// Import 导入一份 ISC 格式的配置。
//
// dryRun 为 true 时不写入任何改动，只回报"将会发生什么"。
func (s *Service) Import(ctx context.Context, body []byte, dryRun bool) (Result, error) {
	var doc Document
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	if doc.FormatVersion == 0 && len(doc.Credentials) == 0 {
		return Result{}, ErrInvalidDocument
	}
	if doc.FormatVersion > FormatVersion {
		return Result{}, fmt.Errorf(
			i18n.T("configio.err.too_new"),
			ErrInvalidDocument, doc.FormatVersion, FormatVersion)
	}

	res := Result{DryRun: dryRun}

	if doc.Settings != nil && !dryRun && s.settings != nil {
		if _, err := s.settings.Update(ctx, settings.Patch{
			Lang:             &doc.Settings.Lang,
			LogLevel:         &doc.Settings.LogLevel,
			EventBufferSize:  &doc.Settings.EventBufferSize,
			NotifyOnIPChange: &doc.Settings.NotifyOnIPChange,
		}); err != nil {
			res.Errors = append(res.Errors, err.Error())
		} else {
			res.Summary.SettingsUpdated = 1
		}
	}

	for idx, ec := range doc.Credentials {
		if err := s.importCredential(ctx, ec, dryRun, &res); err != nil {
			res.Errors = append(res.Errors, i18n.T("config.import.skipped", idx+1, err.Error()))
			res.Summary.CredentialsSkipped++
		}
	}

	if dryRun {
		res.Warnings = append(res.Warnings, i18n.T("config.import.dry_run"))
	}
	return res, nil
}

// importCredential 导入（或更新）一条凭据。
func (s *Service) importCredential(ctx context.Context, ec ExportCredential, dryRun bool, res *Result) error {
	if strings.TrimSpace(ec.Provider) == "" || strings.TrimSpace(ec.Label) == "" {
		return errors.New(i18n.T("configio.err.no_provider"))
	}
	if _, ok := s.registry.Get(ec.Provider); !ok {
		return fmt.Errorf(i18n.T("configio.err.unknown_prov"), ec.Provider)
	}

	// 按 (服务商, 标签) 判断是新建还是更新 —— 与数据库的唯一索引一致。
	existing, err := s.findExisting(ctx, ec.Provider, ec.Label)
	if err != nil {
		return err
	}

	if existing == nil {
		res.Summary.CredentialsCreated++
		if dryRun {
			return nil
		}
		_, err := s.credentials.Create(ctx, credential.Credential{
			Provider: ec.Provider, Label: ec.Label, Fields: ec.Fields,
		})
		return err
	}

	res.Summary.CredentialsUpdated++
	if dryRun {
		return nil
	}
	_, err = s.credentials.Update(ctx, existing.ID, credential.Credential{
		Provider: ec.Provider, Label: ec.Label, Fields: ec.Fields,
	})
	return err
}

// findExisting 按 (服务商, 标签) 查找现有凭据。
//
// 走列表扫描而不是直接查库：credential.Repository 没有暴露这个查询，
// 而为一个导入场景去扩大领域接口不划算。凭据数量是个位数到几十，
// 扫描的代价可以忽略。
func (s *Service) findExisting(ctx context.Context, providerName, label string) (*credential.Credential, error) {
	cursor := ""
	for {
		items, next, err := s.credentials.List(ctx, cursor, 100)
		if err != nil {
			return nil, err
		}
		for i := range items {
			if items[i].Provider == providerName && items[i].Label == label {
				return &items[i], nil
			}
		}
		if next == "" {
			return nil, nil
		}
		cursor = next
	}
}

// ErrInvalidDocument 表示导入内容无法识别。
var ErrInvalidDocument = errors.New(i18n.T("configio.err.unrecognised"))
