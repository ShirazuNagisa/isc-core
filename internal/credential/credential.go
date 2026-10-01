// Package credential 定义 DNS 服务商凭据这一领域实体。
//
// 它与持久化解耦：本包不知道凭据存在 SQLite 里，也不知道字段被
// AES-GCM 加密过 —— 那些是 store 与 secret 包的事。
package credential

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Mask 是所有敏感字段对外呈现时的固定占位值。
//
// 设计取舍：**不泄露密钥的任何片段**。
//
// ddns-go 的做法是显示前 4 位与后 4 位，方便用户辨认"我填的是哪一把
// 密钥"。但识别凭据这件事已经由 `label` 字段承担了，而密钥片段一旦
// 出现在界面上，就会顺带出现在截图、录屏、issue 附件与日志里。
// 用一个不携带任何信息的固定值更安全，代价只是少了一点便利。
//
// 它同时是"客户端原样回传掩码"的判定依据 —— 见 Merge。
const Mask = "********"

// Credential 是一条 DNS 服务商凭据。
//
// Fields 在内存中始终是**明文**；落库前由上层用主密钥加密。
type Credential struct {
	ID       string
	Provider string
	Label    string
	Fields   map[string]string

	CreatedAt time.Time
	UpdatedAt time.Time

	LastVerifiedAt  *time.Time
	LastVerifyOK    *bool
	LastVerifyError string
}

// 校验错误。
var (
	// ErrLabelEmpty 表示标签为空。
	ErrLabelEmpty = errors.New("credential: 标签不能为空")
	// ErrProviderEmpty 表示服务商标识为空。
	ErrProviderEmpty = errors.New("credential: 服务商不能为空")
	// ErrMissingField 表示缺少必填字段。
	ErrMissingField = errors.New("credential: 缺少必填字段")
	// ErrUnknownField 表示出现了未声明的字段。
	ErrUnknownField = errors.New("credential: 存在未声明的字段")
)

// FieldSpec 描述一个凭据字段。
//
// 它同时是界面渲染依据（GUI 遍历它生成表单）与校验依据，
// 因此放在领域层而不是接口层。
//
// LabelKey / HelpKey / PlaceholderKey 存的是 **i18n 消息 key** 而不是
// 文案本身：领域层不该知道用户当前用什么语言，而且语言是可以在运行时
// 切换的 —— 把已翻译的字符串固化在注册表里，切语言后就会留在旧语言。
// 由接口层在输出时翻译。
type FieldSpec struct {
	// Key 是字段名，用作 Fields 中的键。
	Key string
	// LabelKey 是展示名称的 i18n key。
	LabelKey string
	// Secret 为 true 表示该字段是敏感值，读取时应被掩码。
	Secret bool
	// Required 为 true 表示必填。
	Required bool
	// PlaceholderKey 是输入提示的 i18n key（可为空）。
	PlaceholderKey string
	// HelpKey 是补充说明的 i18n key，例如"在何处获取该密钥"（可为空）。
	HelpKey string
	// Example 是示例值。它是值不是文案，因此不走 i18n。
	Example string

	// DdnsGoSlot 指出该字段对应 ddns-go 凭据模型的哪个槽位：
	//
	//	DdnsGoUnmapped (0)  不从 ddns-go 迁移 —— **零值即此，是安全的默认**
	//	DdnsGoID       (1)  dns.id
	//	DdnsGoSecret   (2)  dns.secret
	//	DdnsGoExtParam (3)  dns.extparam
	//
	// 之所以是 1 基而不是 0 基：Go 的零值是 0，如果 0 表示 "id"，
	// 那么任何一个**忘了声明槽位**的字段都会静默抢走 id 槽位，
	// 而症状是"导入的凭据是空的"——极难归因。让零值表示"未映射"，
	// 忘记声明就只是不参与迁移，一眼可见。
	//
	// 之所以不能按"字段声明顺序"推断：ddns-go 的模型永远是
	// (id, secret, extparam) 三元组，而各服务商真正用到的槽位不同 ——
	// Cloudflare 只用 secret（API Token），id 是空的。
	DdnsGoSlot int
}

// ddns-go 凭据模型的槽位编号。
//
// 定义在领域层而不是各个使用方：导入与运行期调用必须用**同一份**声明，
// 否则两边迟早漂移，表现为"导入的凭据一开始能用，某次升级后突然失效"。
const (
	// DdnsGoUnmapped 表示该字段不从 ddns-go 迁移。它是零值。
	DdnsGoUnmapped = 0
	// DdnsGoID 对应 ddns-go 的 dns.id。
	DdnsGoID = 1
	// DdnsGoSecret 对应 ddns-go 的 dns.secret。
	DdnsGoSecret = 2
	// DdnsGoExtParam 对应 ddns-go 的 dns.extparam。
	DdnsGoExtParam = 3
)

// Validate 校验凭据是否满足字段定义。
//
// 校验**只检查必填与未知字段**，不检查格式：各家服务商的 ID/Secret
// 形态差异极大（有的是 32 位十六进制，有的是带前缀的长串），
// 在本地做格式猜测只会误伤合法输入。真正有效的校验是拿它去调一次
// 服务商 API —— 那是 Verify 的职责。
func (c Credential) Validate(specs []FieldSpec) error {
	if strings.TrimSpace(c.Provider) == "" {
		return ErrProviderEmpty
	}
	if strings.TrimSpace(c.Label) == "" {
		return ErrLabelEmpty
	}

	known := make(map[string]bool, len(specs))
	for _, s := range specs {
		known[s.Key] = true
	}
	for key := range c.Fields {
		if !known[key] {
			return fmt.Errorf("%w: %s", ErrUnknownField, key)
		}
	}
	for _, s := range specs {
		if s.Required && strings.TrimSpace(c.Fields[s.Key]) == "" {
			return fmt.Errorf("%w: %s", ErrMissingField, s.Key)
		}
	}
	return nil
}

// Merge 把"更新"合并进现有凭据，返回合并后的字段。
//
// 关键规则：传入值等于 Mask 时**保留原值**。
//
// 这让客户端可以"读取 → 改个标签 → 原样回传"而不会把掩码当成新密钥
// 写进去。没有这条规则，界面上任何一次编辑都会静默地把密钥改成
// 八个星号，而下一次解析失败时用户完全不知道发生了什么。
//
// 空字符串表示"不修改"还是"清空"？这里选择**清空**：
// 客户端要清空一个字段就传空串，要保留就传掩码。二者语义清晰，
// 不需要额外的三态类型（那会让 OpenAPI 契约变得别扭）。
func Merge(existing, incoming map[string]string) map[string]string {
	out := make(map[string]string, len(existing)+len(incoming))
	for k, v := range existing {
		out[k] = v
	}
	for k, v := range incoming {
		if isMasked(v) {
			continue
		}
		out[k] = strings.TrimSpace(v)
	}
	return out
}

// Masked 返回字段的掩码副本，供接口输出。
//
// 只有**已设置**的敏感字段才会出现键 —— 空字段直接省略，
// 这样界面能区分"没填"与"填了但看不到"。
func Masked(fields map[string]string, specs []FieldSpec) map[string]string {
	secret := make(map[string]bool, len(specs))
	for _, s := range specs {
		secret[s.Key] = s.Secret
	}

	out := make(map[string]string, len(fields))
	for k, v := range fields {
		if v == "" {
			continue
		}
		if secret[k] {
			out[k] = Mask
			continue
		}
		out[k] = v
	}
	return out
}

// isMasked 判断一个值是否是掩码占位。
func isMasked(v string) bool {
	return strings.TrimSpace(v) == Mask
}

// FieldKeys 返回字段定义的键，按声明顺序排列。
//
// 顺序只影响界面上的呈现次序，**不再承担任何语义** ——
// 与 ddns-go 的对应关系由 FieldSpec.DdnsGoSlot 显式声明。
func FieldKeys(specs []FieldSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Key)
	}
	return out
}
