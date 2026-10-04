package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/remote"
)

// 本文件是远程设备的 SQLite 仓储。
//
// 它直接实现 remote.Store（方法挂在 *Store 上而不是另开一个子仓储）：
// 与代理路由不同，设备没有"整体替换"这种语义 —— 它是一条一条增删改的
// 独立实体，因此没有需要跨行保证的自洽性，也就不需要单独一层。

const remoteDeviceColumns = `id, label, role, parent_device_id, token_hash,
	platform, model, os_version, app_version, notifications_enabled,
	apns_token, apns_environment, apns_topic,
	created_at, updated_at, last_seen_at, last_seen_ip, revoked_at`

// CreateDevice 插入一台新设备。
func (s *Store) CreateDevice(ctx context.Context, d remote.Device) error {
	const q = `INSERT INTO remote_devices (` + remoteDeviceColumns + `)
	           VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

	_, err := s.db.ExecContext(ctx, q,
		d.ID, d.Label, string(d.Role), nullIfEmpty(d.ParentDeviceID), d.TokenHash,
		nullIfEmpty(d.Platform), nullIfEmpty(d.Model), nullIfEmpty(d.OSVersion), nullIfEmpty(d.AppVersion),
		boolToIntValue(d.NotificationsEnabled),
		nullIfEmpty(d.APNSToken), nullIfEmpty(d.APNSEnvironment), nullIfEmpty(d.APNSTopic),
		formatTime(d.CreatedAt), formatTime(d.UpdatedAt),
		formatTimePtr(timeOrNil(d.LastSeenAt)), nullIfEmpty(d.LastSeenIP),
		formatTimePtr(timeOrNil(d.RevokedAt)))
	if err != nil {
		return fmt.Errorf(i18n.T("store.err.insert_remote_device"), err)
	}
	return nil
}

// ListDevices 返回全部设备，新建的在前。
//
// 已吊销的设备**也在里面**：界面要能把它们显示成"已断开"，
// 而过滤掉之后用户只会觉得"我的设备怎么不见了"。
func (s *Store) ListDevices(ctx context.Context) ([]remote.Device, error) {
	q := `SELECT ` + remoteDeviceColumns + ` FROM remote_devices ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("store.err.list_remote_devices"), err)
	}
	defer rows.Close() //nolint:errcheck // 只读游标

	var out []remote.Device
	for rows.Next() {
		d, err := scanRemoteDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(i18n.T("store.err.iter_remote_devices"), err)
	}
	return out, nil
}

// Device 按 ID 返回一台设备。
func (s *Store) Device(ctx context.Context, id string) (remote.Device, error) {
	q := `SELECT ` + remoteDeviceColumns + ` FROM remote_devices WHERE id = ?`
	return s.queryRemoteDevice(ctx, q, id, "store.err.get_remote_device")
}

// DeviceByTokenHash 按令牌哈希返回设备。
//
// 这是每一次远程请求都会走的查询，因此 token_hash 上有唯一索引。
// 索引同时保证了"至多命中一台"这件事是数据库层面的性质。
func (s *Store) DeviceByTokenHash(ctx context.Context, hash []byte) (remote.Device, error) {
	q := `SELECT ` + remoteDeviceColumns + ` FROM remote_devices WHERE token_hash = ?`
	return s.queryRemoteDevice(ctx, q, hash, "store.err.get_remote_device")
}

// queryRemoteDevice 是单行查询的公共实现。
func (s *Store) queryRemoteDevice(ctx context.Context, q string, arg any, errKey string) (remote.Device, error) {
	row := s.db.QueryRowContext(ctx, q, arg)
	d, err := scanRemoteDevice(row)
	if err != nil {
		if isNoRows(err) {
			// 把 sql.ErrNoRows 收敛成领域错误：调用方要区分的是
			// "这台设备不存在"与"数据库出问题了"，而不是 SQL 细节。
			return remote.Device{}, remote.ErrDeviceNotFound
		}
		return remote.Device{}, fmt.Errorf(i18n.T(errKey), err)
	}
	return d, nil
}

// UpdateDevice 部分更新一台设备。
//
// 只有非 nil 的字段会进 SET 子句：nil 表示"这一项不改"，
// 而不是"改成零值"。这个区分在界面上是必要的 —— 改名字的那次请求
// 不该把角色也重置成默认值。
func (s *Store) UpdateDevice(ctx context.Context, id string, p remote.DevicePatch) (remote.Device, error) {
	var (
		sets []string
		args []any
	)
	if p.Label != nil {
		sets = append(sets, "label = ?")
		args = append(args, *p.Label)
	}
	if p.Role != nil {
		sets = append(sets, "role = ?")
		args = append(args, string(*p.Role))
	}
	if p.NotificationsEnabled != nil {
		sets = append(sets, "notifications_enabled = ?")
		args = append(args, boolToIntValue(*p.NotificationsEnabled))
	}
	if len(sets) == 0 {
		return s.Device(ctx, id)
	}

	sets = append(sets, "updated_at = ?")
	args = append(args, formatTime(time.Now().UTC()), id)

	q := `UPDATE remote_devices SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return remote.Device{}, fmt.Errorf(i18n.T("store.err.update_remote_device"), err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return remote.Device{}, remote.ErrDeviceNotFound
	}
	return s.Device(ctx, id)
}

// RevokeDevice 吊销一台设备，并级联吊销它派生的全部设备，返回受影响的行数。
//
// # 为什么级联是必须的
//
// 手表的令牌是从手机派生出来的。如果只吊销手机而留下手表，
// 那么用户在 Phecda 上看到的"已吊销"是一句假话 —— 那台手表仍然
// 以同样的权限访问着这台内核。
//
// 递归 CTE 而不是循环查询：链深最多两层（手机 → 手表），但把它写成
// 通用的遍历不需要额外代价，而写死两层会在将来加入第三种设备时变成一个
// 静默的漏洞。
//
// 已经吊销过的行不重复计数（`revoked_at IS NULL`）：这样返回值的语义
// 是"这次操作断开了几台设备"，而不是"它们本来就被断开过"。
func (s *Store) RevokeDevice(ctx context.Context, id string, at time.Time) (int, error) {
	const q = `
		WITH RECURSIVE affected(id) AS (
			SELECT id FROM remote_devices WHERE id = ?
			UNION ALL
			SELECT d.id FROM remote_devices d JOIN affected a ON d.parent_device_id = a.id
		)
		UPDATE remote_devices
		SET revoked_at = ?, updated_at = ?
		WHERE id IN (SELECT id FROM affected) AND revoked_at IS NULL`

	ts := formatTime(at)
	res, err := s.db.ExecContext(ctx, q, id, ts, ts)
	if err != nil {
		return 0, fmt.Errorf(i18n.T("store.err.revoke_remote_device"), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf(i18n.T("store.err.delete_result"), err)
	}
	return int(n), nil
}

// TouchDevice 记录一次成功的访问。
func (s *Store) TouchDevice(ctx context.Context, id string, at time.Time, ip string) error {
	const q = `UPDATE remote_devices SET last_seen_at = ?, last_seen_ip = ? WHERE id = ?`
	if _, err := s.db.ExecContext(ctx, q, formatTime(at), nullIfEmpty(ip), id); err != nil {
		return fmt.Errorf(i18n.T("store.err.touch_remote_device"), err)
	}
	return nil
}

// SetPushToken 登记或清除一台设备的 APNs 令牌。
//
// 空串表示清除（注销），因此这里把空串写成 NULL 而不是空文本：
// `apns_token IS NULL` 与 `apns_token = ”` 在查询里含义不同，
// 而前者才是"这台设备没有令牌"。
func (s *Store) SetPushToken(ctx context.Context, id, token, environment, topic string) error {
	const q = `UPDATE remote_devices
	           SET apns_token = ?, apns_environment = ?, apns_topic = ?, updated_at = ?
	           WHERE id = ?`

	_, err := s.db.ExecContext(ctx, q,
		nullIfEmpty(token), nullIfEmpty(environment), nullIfEmpty(topic),
		formatTime(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf(i18n.T("store.err.update_remote_device"), err)
	}
	return nil
}

// AppendPushDelivery 记一条推送投递。
func (s *Store) AppendPushDelivery(ctx context.Context, d remote.PushDelivery) error {
	const q = `INSERT INTO remote_push_deliveries
	           (ts, device_id, kind, dedupe_key, status, http_status, reason, apns_id)
	           VALUES (?,?,?,?,?,?,?,?)`

	var status any
	if d.HTTPStatus > 0 {
		status = d.HTTPStatus
	}

	_, err := s.db.ExecContext(ctx, q,
		formatTime(d.TS), d.DeviceID, d.Kind, d.DedupeKey, d.Status,
		status, nullIfEmpty(d.Reason), nullIfEmpty(d.APNSID))
	if err != nil {
		return fmt.Errorf(i18n.T("store.err.push_delivery"), err)
	}
	return nil
}

// scanRemoteDevice 扫描一行设备。
//
// 参数是 sql.Row 与 sql.Rows 的公共接口：两者都只有 Scan，
// 而把这一段写成两份会让"加了列但只改了一处"变成可能的错误。
func scanRemoteDevice(row interface{ Scan(...any) error }) (remote.Device, error) {
	var (
		d             remote.Device
		role          string
		parentID      sql.NullString
		platform      sql.NullString
		model         sql.NullString
		osVersion     sql.NullString
		appVersion    sql.NullString
		notifications int64
		apnsToken     sql.NullString
		apnsEnv       sql.NullString
		apnsTopic     sql.NullString
		createdAt     string
		updatedAt     string
		lastSeenAt    sql.NullString
		lastSeenIP    sql.NullString
		revokedAt     sql.NullString
	)

	if err := row.Scan(&d.ID, &d.Label, &role, &parentID, &d.TokenHash,
		&platform, &model, &osVersion, &appVersion, &notifications,
		&apnsToken, &apnsEnv, &apnsTopic,
		&createdAt, &updatedAt, &lastSeenAt, &lastSeenIP, &revokedAt); err != nil {
		return remote.Device{}, err
	}

	d.Role = remote.Role(role)
	d.ParentDeviceID = parentID.String
	d.Platform = platform.String
	d.Model = model.String
	d.OSVersion = osVersion.String
	d.AppVersion = appVersion.String
	d.NotificationsEnabled = notifications != 0
	d.APNSToken = apnsToken.String
	d.APNSEnvironment = apnsEnv.String
	d.APNSTopic = apnsTopic.String
	d.CreatedAt = parseTime(createdAt)
	d.UpdatedAt = parseTime(updatedAt)
	d.LastSeenIP = lastSeenIP.String
	if t := parseTimePtr(lastSeenAt); t != nil {
		d.LastSeenAt = *t
	}
	if t := parseTimePtr(revokedAt); t != nil {
		d.RevokedAt = *t
	}
	return d, nil
}

// timeOrNil 把零值时间转成 nil，让可空列真的写 NULL。
//
// 直接存零值时间会写成 "0001-01-01T00:00:00Z" —— 那是一个**合法的时间
// 字符串**，于是所有 `last_seen_at IS NULL` 的判断都会失效，
// 界面上也会显示一个公元 1 年的"最后访问"。
func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
