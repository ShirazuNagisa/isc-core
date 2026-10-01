package dns

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Service 是记录管理的领域服务：按能力分发到具体服务商实现。
//
// 它做的唯一一件有分量的事是**能力协商**：把"这家服务商不支持这个操作"
// 变成一条明确的、可以展示给用户的错误，而不是一次 panic 或一个
// 语义不明的接口断言失败。
//
// 为什么需要它：dns.Provider 只有 Meta()，其余能力靠可选接口表达。
// 调用方若直接做类型断言，每个 handler 里都会散落着
// `if lister, ok := impl.(ZoneLister); ok { ... } else { ... }`。
// 集中到一处之后，接口层只需要处理 error。
type Service struct {
	creds     CredentialResolver
	providers ImplLookup
}

// CredentialResolver 按 ID 取出解密后的凭据。
//
// 与 ddns.CredentialResolver 的签名完全一致 —— 因此装配处只需要
// 一个适配器就能同时满足两者，不必写两遍。
type CredentialResolver interface {
	Resolve(ctx context.Context, id string) (Credential, error)
}

// ImplLookup 按服务商名取出实现。
//
// 返回的 Provider 为 nil 表示这家没有实现（或名字不存在）。
type ImplLookup func(providerName string) (Provider, bool)

// NewService 构造记录管理服务。
func NewService(creds CredentialResolver, providers ImplLookup) *Service {
	return &Service{creds: creds, providers: providers}
}

// ErrUnsupported 表示服务商不支持该操作。
type ErrUnsupported struct {
	// Provider 是服务商标识。
	Provider string
	// Op 是操作名称（如"列出区域"）。
	Op string
}

// Error 实现 error。
func (e *ErrUnsupported) Error() string {
	return fmt.Sprintf("dns: 服务商 %s 不支持%s", e.Provider, e.Op)
}

// ErrNotFound 表示记录不存在。
var ErrNotFound = errors.New("dns: 记录不存在")

// resolve 取出凭据与它的服务商实现。
func (s *Service) resolve(ctx context.Context, credentialID string) (Credential, Provider, error) {
	cred, err := s.creds.Resolve(ctx, credentialID)
	if err != nil {
		return Credential{}, nil, err
	}
	impl, ok := s.providers(cred.Provider)
	if !ok || impl == nil {
		return Credential{}, nil, &ErrUnsupported{
			Provider: cred.Provider, Op: "记录管理",
		}
	}
	return cred, impl, nil
}

// ListZones 列出凭据可管理的区域。
func (s *Service) ListZones(ctx context.Context, credentialID string) ([]Zone, error) {
	cred, impl, err := s.resolve(ctx, credentialID)
	if err != nil {
		return nil, err
	}
	lister, ok := impl.(ZoneLister)
	if !ok {
		return nil, &ErrUnsupported{Provider: cred.Provider, Op: "列出区域"}
	}
	zones, err := lister.ListZones(ctx, cred)
	if err != nil {
		return nil, err
	}
	// 返回空切片而不是 nil：JSON 序列化 nil 切片会得到 null，
	// 而客户端普遍按数组处理。
	if zones == nil {
		zones = []Zone{}
	}
	return zones, nil
}

// ListRecords 列出区域内的记录。
func (s *Service) ListRecords(ctx context.Context, credentialID, zoneID string,
	filter RecordFilter) ([]Record, error) {

	cred, impl, err := s.resolve(ctx, credentialID)
	if err != nil {
		return nil, err
	}
	lister, ok := impl.(RecordLister)
	if !ok {
		return nil, &ErrUnsupported{Provider: cred.Provider, Op: "列出记录"}
	}

	zone, err := s.zoneOf(ctx, cred, impl, zoneID)
	if err != nil {
		return nil, err
	}

	records, err := lister.ListRecords(ctx, cred, zone, filter)
	if err != nil {
		return nil, err
	}
	if records == nil {
		records = []Record{}
	}
	return records, nil
}

// GetRecord 读取单条记录。
//
// 没有"读取单条"的服务商接口 —— 各家的实现差异太大（有的根本没有
// 这个端点）。这里用列表 + 过滤实现，对用户而言语义一致。
func (s *Service) GetRecord(ctx context.Context, credentialID, zoneID, recordID string) (Record, error) {
	records, err := s.ListRecords(ctx, credentialID, zoneID, RecordFilter{})
	if err != nil {
		return Record{}, err
	}
	for _, r := range records {
		if r.ID == recordID {
			return r, nil
		}
	}
	return Record{}, ErrNotFound
}

// CreateRecord 新增记录。
func (s *Service) CreateRecord(ctx context.Context, credentialID, zoneID string,
	rec Record) (Record, error) {

	cred, impl, err := s.resolve(ctx, credentialID)
	if err != nil {
		return Record{}, err
	}
	creator, ok := impl.(RecordCreator)
	if !ok {
		return Record{}, &ErrUnsupported{Provider: cred.Provider, Op: "新增记录"}
	}
	if err := validateRecord(rec); err != nil {
		return Record{}, err
	}

	zone, err := s.zoneOf(ctx, cred, impl, zoneID)
	if err != nil {
		return Record{}, err
	}
	return creator.CreateRecord(ctx, cred, zone, rec)
}

// UpdateRecord 修改记录。
func (s *Service) UpdateRecord(ctx context.Context, credentialID, zoneID, recordID string,
	rec Record) (Record, error) {

	cred, impl, err := s.resolve(ctx, credentialID)
	if err != nil {
		return Record{}, err
	}
	updater, ok := impl.(RecordUpdater)
	if !ok {
		return Record{}, &ErrUnsupported{Provider: cred.Provider, Op: "修改记录"}
	}
	if err := validateRecord(rec); err != nil {
		return Record{}, err
	}

	// 记录 ID 来自路径，而不是请求体 —— 请求体里即便带了也以路径为准。
	// 这避免了"改了 A 记录却因为请求体里写着 B 的 ID 而改错对象"。
	rec.ID = recordID

	zone, err := s.zoneOf(ctx, cred, impl, zoneID)
	if err != nil {
		return Record{}, err
	}
	return updater.UpdateRecord(ctx, cred, zone, rec)
}

// DeleteRecord 删除记录。
func (s *Service) DeleteRecord(ctx context.Context, credentialID, zoneID, recordID string) error {
	cred, impl, err := s.resolve(ctx, credentialID)
	if err != nil {
		return err
	}
	deleter, ok := impl.(RecordDeleter)
	if !ok {
		return &ErrUnsupported{Provider: cred.Provider, Op: "删除记录"}
	}

	zone, err := s.zoneOf(ctx, cred, impl, zoneID)
	if err != nil {
		return err
	}
	return deleter.DeleteRecord(ctx, cred, zone, recordID)
}

// zoneOf 用 ID 拼出一个 Zone。
//
// 只填 ID 与 Name 中的 ID：记录操作只需要 zone ID，而为了拿到名字
// 去列一次区域是多余的往返。少数服务商确实需要区域名（GoDaddy 的
// 路径里就是域名），它们的实现应当自行处理 —— 见各实现里的说明。
func (s *Service) zoneOf(_ context.Context, _ Credential, _ Provider, zoneID string) (Zone, error) {
	if zoneID == "" {
		return Zone{}, errors.New("dns: 缺少区域 ID")
	}
	return Zone{ID: zoneID}, nil
}

// validateRecord 校验记录的基本字段。
//
// 只挡住"一定提交不上去"的情况（类型或名字为空）。不校验内容格式：
// 各记录类型的内容规则差异极大（TXT 可以有空格、CAA 有固定结构、
// SRV 有下划线前缀），在本地做格式猜测只会误伤合法输入。
// 真正的校验交给服务商 —— 它的错误信息比我们猜的更准确。
//
// 也不维护"支持哪些记录类型"的白名单：各家的支持范围随产品演进，
// 硬编码白名单会让"服务商新支持了 HTTPS 记录"变成一次内核发版。
func validateRecord(rec Record) error {
	if strings.TrimSpace(string(rec.Type)) == "" {
		return errors.New("dns: 记录类型不能为空")
	}
	if strings.TrimSpace(rec.Name) == "" {
		return errors.New("dns: 记录名不能为空")
	}
	return nil
}
