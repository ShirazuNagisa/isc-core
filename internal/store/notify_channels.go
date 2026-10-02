package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/notify"
)

// NotifyChannels 是通知通道的 SQLite 仓储。
type NotifyChannels struct{ s *Store }

// 编译期断言用不到具体接口（通道是运行期构造的），因此只保留类型。
// 编译期断言：接口实现必须完整。
var _ notify.ChannelStore = (*NotifyChannels)(nil)

// NotifyChannels 返回通知通道仓储。
func (s *Store) NotifyChannels() *NotifyChannels { return &NotifyChannels{s: s} }

// 通道配置的类型直接用 notify.ChannelConfig。
//
// **不再定义第二份**：早先存储层有一份逐字段重复的结构体，而那是
// 一处必然出错的重复 —— 给一边加字段、忘了另一边时，接口返回 200
// 而值根本没存进去（这个项目里已经栽过两次）。
//
// 与 notify 包的依赖方向也是自然的：存储层需要知道"配置长什么样"，
// 而通知中心不需要知道"它从哪来"。
type ChannelConfig = notify.ChannelConfig

const notifyChannelColumns = `id, name, kind, enabled, url, method, headers, body_template, min_severity`

// List 返回全部通道配置。
func (n *NotifyChannels) List(ctx context.Context) ([]ChannelConfig, error) {
	q := `SELECT ` + notifyChannelColumns + ` FROM notify_channels ORDER BY rowid ASC`

	rows, err := n.s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("store.err.query_channels"), err)
	}
	defer rows.Close() //nolint:errcheck // 只读游标

	var out []ChannelConfig
	for rows.Next() {
		var (
			cfg      ChannelConfig
			enabled  int64
			headers  string
			severity string
		)
		if err := rows.Scan(&cfg.ID, &cfg.Name, &cfg.Kind, &enabled,
			&cfg.URL, &cfg.Method, &headers, &cfg.BodyTemplate, &severity); err != nil {
			return nil, fmt.Errorf(i18n.T("store.err.scan_channel"), err)
		}
		cfg.Enabled = enabled != 0
		cfg.MinSeverity = notify.Severity(severity)

		// 解析失败不报错：headers 是附属信息，而因为一段坏 JSON
		// 让整条通道读不出来，会让用户失去一个本该能工作的通知渠道。
		if headers != "" && headers != "{}" {
			_ = json.Unmarshal([]byte(headers), &cfg.Headers)
		}
		out = append(out, cfg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(i18n.T("store.err.iter_channels"), err)
	}
	return out, nil
}

// Replace 用给定集合整体替换全部通道配置。
//
// 与代理路由同样的理由：顺序与冲突（同一个 ID）在整体替换下才有
// 明确的语义，而增量修改会让"删掉第三条"这类操作在并发下失去意义。
func (n *NotifyChannels) Replace(ctx context.Context, configs []ChannelConfig) error {
	tx, err := n.s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf(i18n.T("store.err.begin_chan_tx"), err)
	}
	defer tx.Rollback() //nolint:errcheck // 提交成功后回滚是空操作

	if _, err := tx.ExecContext(ctx, `DELETE FROM notify_channels`); err != nil {
		return fmt.Errorf(i18n.T("store.err.clear_channels"), err)
	}

	const q = `INSERT INTO notify_channels (` + notifyChannelColumns + `,
	           created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`

	now := formatTime(time.Now().UTC())
	for _, cfg := range configs {
		headers := "{}"
		if len(cfg.Headers) > 0 {
			byt, err := json.Marshal(cfg.Headers)
			if err != nil {
				return fmt.Errorf(i18n.T("store.err.marshal_headers"), err)
			}
			headers = string(byt)
		}

		method := cfg.Method
		if method == "" {
			method = "POST"
		}
		severity := string(cfg.MinSeverity)
		if severity == "" {
			severity = string(notify.SeverityInfo)
		}

		if _, err := tx.ExecContext(ctx, q,
			cfg.ID, cfg.Name, cfg.Kind, boolToIntValue(cfg.Enabled),
			cfg.URL, method, headers, cfg.BodyTemplate, severity,
			now, now); err != nil {
			return fmt.Errorf(i18n.T("store.err.write_channel"), err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf(i18n.T("store.err.commit_channels"), err)
	}
	return nil
}
