package job

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// idBytes 是任务 ID 的随机字节数。
//
// 16 字节 = 128 位随机性，碰撞概率可忽略；base64url 编码后为 22 个字符，
// 比 UUID 短且不需要连字符，适合出现在 URL 路径与日志里。
const idBytes = 16

// newID 生成一个任务 ID。
//
// 刻意不使用时间戳或自增序号：任务 ID 会出现在 API 路径与日志中，
// 可预测的 ID 让攻击者能枚举历史任务（其中可能含域名、错误详情等信息）。
func newID() (string, error) {
	buf := make([]byte, idBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("job: 生成任务 ID 失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
