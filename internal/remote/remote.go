// Package remote 实现内核的**远程管理面** —— 手机端（ISC Mizar）接入的那条链路。
//
// # 为什么是独立的一层，而不是"把本地接口暴露出去"
//
// 本地管理面的安全性建立在**绑定地址**上：`LoopbackGuard` 只看 `Host` 头，
// 而 `/v1/console/bootstrap` 免鉴权就会交出令牌。也就是说，那条监听一旦能被
// 局域网上的机器连到，整套假设同时失效 —— 任何能发一个 `Host: 127.0.0.1`
// 的请求的进程都能拿到完整的管理权限。
//
// 因此远程面是**第二个 http.Server**，见 docs/DECISIONS.md D09 与 D38：
//
//   - 独立的 TLS 配置（自签证书 + 公钥指纹固定）；
//   - 独立的鉴权链（按设备签发的令牌，可单独吊销，带角色）；
//   - 独立的路由白名单（默认拒绝）。
//
// 它不复用本地中间件，也不挂载控制台与引导端点。
//
// # 令牌为什么不落库明文
//
// 设备令牌和本地访问令牌一样，**等价于一份权限**。存哈希而不是原文，
// 换来的是"数据库被读走"不再等于"令牌被读走"。代价是令牌只能在签发时
// 返回一次 —— 这是刻意的：丢了就重新配对，而不是回去数据库里翻。
package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"time"
)

// Role 是远程设备的权限级别。
//
// 只有两级，见 api/openapi.yaml 里 `RemoteRole` 的说明：判据是"这台设备
// 能不能改到用户的东西"，而不是按接口分类。多一级就要在契约、存储、
// 中间件与授权界面四处都引入能力位，而多出来的表达力在三五个接口上
// 换不回等价的可理解性。
type Role string

const (
	// RoleViewer 只读监控。
	RoleViewer Role = "viewer"
	// RoleOperator 只读，外加改 DNS 记录、启停 DDNS 任务、启停站点、续期证书。
	RoleOperator Role = "operator"
)

// Valid 报告角色是否是已知值。
func (r Role) Valid() bool { return r == RoleViewer || r == RoleOperator }

// rank 是角色的权限序。数字越大权限越高。
//
// 派生令牌的规则是"子设备不高于父设备"，它依赖这个序而不是角色名字 ——
// 用字符串比较会在改名或新增角色时静默出错。
func (r Role) rank() int {
	switch r {
	case RoleOperator:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// AtLeast 报告 r 的权限是否不低于 other。
func (r Role) AtLeast(other Role) bool { return r.rank() >= other.rank() }

// 会话与令牌的长度。
const (
	// tokenBytes 是设备令牌的随机字节数。
	//
	// 32 字节（256 位）与本地访问令牌一致。它不参与任何人类输入，
	// 因此没有理由做得更短。
	tokenBytes = 32

	// secretBytes 是二维码里配对密钥的随机字节数。
	//
	// 同样是 256 位：扫码路径是自动完成的，用户感觉不到长度，
	// 因此这里没有任何"为了好输入而牺牲熵"的理由。
	secretBytes = 32
)

// ErrDeviceNotFound 表示设备不存在（或已被删除）。
var ErrDeviceNotFound = errors.New("remote: device not found")

// ErrDeviceRevoked 表示设备存在但已被吊销。
var ErrDeviceRevoked = errors.New("remote: device revoked")

// Device 是一台已配对的远程设备。
type Device struct {
	ID string
	// Label 是用户看得见的名字，例如"iPhone 15"。
	Label string
	Role  Role
	// ParentDeviceID 是派生令牌的来源设备；空串表示直接配对而来。
	ParentDeviceID string

	// TokenHash 是设备令牌的 SHA-256。
	TokenHash []byte

	// 客户端自称的信息。它们只用于展示与排查，**不参与任何判定**。
	Platform   string
	Model      string
	OSVersion  string
	AppVersion string

	NotificationsEnabled bool
	APNSToken            string
	APNSEnvironment      string
	APNSTopic            string

	CreatedAt time.Time
	UpdatedAt time.Time

	// LastSeenAt 为零值表示从未访问过。
	LastSeenAt time.Time
	LastSeenIP string

	// RevokedAt 为零值表示仍然有效。
	//
	// 已吊销的设备**保留**在列表里：用户需要看到"这台设备是什么时候
	// 被谁断开的"，而删掉它就什么都看不到了。
	RevokedAt time.Time
}

// Active 报告设备当前是否可用。
func (d Device) Active() bool { return d.RevokedAt.IsZero() }

// DevicePatch 是设备的部分更新。nil 表示"这一项不改"。
type DevicePatch struct {
	Label                *string
	Role                 *Role
	NotificationsEnabled *bool
}

// PushDelivery 是一条推送投递记录。
//
// 它存在的原因是排查：推送是本项目里失败现场最远的一环（凭据、topic、
// 令牌、环境、Apple 那一侧），没有投递记录就只能靠猜。
type PushDelivery struct {
	TS         time.Time
	DeviceID   string
	Kind       string
	DedupeKey  string
	Status     string // sent | failed | skipped
	HTTPStatus int
	Reason     string
	APNSID     string
}

// APNs 环境。
//
// 两者是**不同的主机**（api.push.apple.com 与 api.sandbox.push.apple.com），
// 而用错环境的症状是 400 BadDeviceToken —— 它看起来像"令牌不对"，
// 实际上是"你把开发版装到了真机上"。
const (
	PushEnvProduction = "production"
	PushEnvSandbox    = "sandbox"
)

// 推送投递状态。
const (
	PushStatusSent    = "sent"
	PushStatusFailed  = "failed"
	PushStatusSkipped = "skipped"
)

// Store 是远程设备的持久化接口。
//
// 定义在本包而不是存储包：这样本包不依赖任何具体的存储实现，
// 测试可以用一个内存实现（与 internal/proxy 的 RouteStore 同一个理由）。
type Store interface {
	CreateDevice(ctx context.Context, d Device) error
	ListDevices(ctx context.Context) ([]Device, error)
	Device(ctx context.Context, id string) (Device, error)
	DeviceByTokenHash(ctx context.Context, hash []byte) (Device, error)
	UpdateDevice(ctx context.Context, id string, p DevicePatch) (Device, error)
	// RevokeDevice 吊销一台设备并**级联**吊销它的子设备，返回受影响的行数。
	//
	// 级联是必要的：手表的令牌来自手机，只吊销手机而留下手表，
	// 会让"丢的手机已经断开了"这句话不成立。
	RevokeDevice(ctx context.Context, id string, at time.Time) (int, error)
	TouchDevice(ctx context.Context, id string, at time.Time, ip string) error
	// SetPushToken 登记或清除一台设备的 APNs 令牌。
	//
	// 单独一个方法而不是并进 DevicePatch：令牌有环境与 topic 两个伴生字段，
	// 而它们必须**一起**更新 —— 分成三次 patch 会让"令牌换了但环境还是旧的"
	// 成为一个可以进入数据库的中间状态。
	SetPushToken(ctx context.Context, id, token, environment, topic string) error
	AppendPushDelivery(ctx context.Context, d PushDelivery) error
}

// newToken 生成一个设备令牌（base64url，无填充）。
//
// 注意哈希的是**文本**而不是随机字节：客户端手里拿到的、发回来的都是
// 这段文本，而鉴权时只能对它做哈希。两者算的不是同一个东西的话，
// 每一个刚签发出来的令牌都验不过 —— 这个缺陷在真机上表现为
// "配对成功了但立刻就说令牌无效"，且没有任何线索指向哈希。
func newToken() (string, []byte, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, tokenHash(token), nil
}

// tokenHash 计算一个 base64url 文本令牌的哈希。
func tokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// hashEqual 做常数时间比较。
//
// 与本地令牌一致：比较用常数时间，避免通过响应耗时逐字节猜测。
// 这里的输入是哈希而不是原文，因此时序攻击的收益本来就更低，
// 但保持一致性的成本是零。
func hashEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}
