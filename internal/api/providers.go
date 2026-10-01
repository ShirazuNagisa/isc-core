package api

import (
	"net/http"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/credential"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/provider"
)

// ListProviders 实现 GET /v1/providers。
//
// 这个端点存在的意义不只是"给界面一份下拉列表"：它让下游 GUI
// **不需要为每家 DNS 服务商写死表单**。GUI 遍历 credential_fields
// 生成输入框、依据 capabilities 决定哪些按钮可用，新增服务商时
// 内核与 GUI 都不用改代码。
func (s *Server) ListProviders(w http.ResponseWriter, _ *http.Request) {
	list := s.Providers.List()

	items := make([]gen.Provider, 0, len(list))
	for _, p := range list {
		items = append(items, toGenProvider(p))
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json",
		struct {
			Items []gen.Provider `json:"items"`
		}{Items: items})
}

func toGenProvider(p provider.Provider) gen.Provider {
	tier := gen.ProviderTier(p.Tier)
	out := gen.Provider{
		Name:         p.Name,
		DisplayName:  p.DisplayName,
		Tier:         &tier,
		Capabilities: toGenProviderCapabilities(p.Capabilities()),
		// 一律初始化为空切片而不是 nil：JSON 序列化 nil 切片会得到
		// null，而客户端普遍按数组处理，null 会让它们在这里分支。
		CredentialFields: make([]gen.ProviderField, 0, len(p.CredentialFields)),
	}
	for _, f := range p.CredentialFields {
		out.CredentialFields = append(out.CredentialFields, gen.ProviderField{
			Key:         f.Key,
			Label:       i18n.T(f.LabelKey),
			Secret:      f.Secret,
			Required:    f.Required,
			Placeholder: optionalT(f.PlaceholderKey),
			Help:        optionalT(f.HelpKey),
			Example:     strPtr(f.Example),
		})
	}
	return out
}

func toGenProviderCapabilities(c provider.Capabilities) gen.ProviderCapabilities {
	return gen.ProviderCapabilities{
		Available:      c.Available,
		Dynamic:        c.Dynamic,
		ZoneList:       c.ZoneList,
		RecordList:     c.RecordList,
		RecordCreate:   c.RecordCreate,
		RecordUpdate:   c.RecordUpdate,
		RecordDelete:   c.RecordDelete,
		AllRecordTypes: c.AllRecordTypes,
		CustomTtl:      c.CustomTTL,
		Proxy:          c.Proxy,
		Dns01:          c.DNS01,
	}
}

// optionalT 翻译一个可选的 i18n key；空 key 返回 nil。
func optionalT(key string) *string {
	if key == "" {
		return nil
	}
	return strPtr(i18n.T(key))
}

// toGenCredential 把领域实体转成接口模型。
//
// **它会掩码敏感字段** —— 这是明文离开内核的唯一出口，
// 因此掩码发生在这里而不是各个 handler 里，避免漏掉一处。
func toGenCredential(c credential.Credential, specs []credential.FieldSpec) gen.Credential {
	masked := credential.Masked(c.Fields, specs)
	fields := make(map[string]string, len(masked))
	for k, v := range masked {
		fields[k] = v
	}

	out := gen.Credential{
		Id:              c.ID,
		Provider:        c.Provider,
		Label:           c.Label,
		Fields:          fields,
		CreatedAt:       c.CreatedAt,
		UpdatedAt:       c.UpdatedAt,
		LastVerifiedAt:  c.LastVerifiedAt,
		LastVerifyOk:    c.LastVerifyOK,
		LastVerifyError: strPtr(c.LastVerifyError),
	}
	return out
}
