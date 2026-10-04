package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/apps"
	"github.com/ShirazuNagisa/isc-core/internal/runtime"
)

// AppsRepository 持久化托管应用。
//
// 它实现 apps.Store：接口定义在领域包里、实现在这里，依赖方向单向
// （store → apps），与 job.Store 的做法一致。
type AppsRepository struct{ s *Store }

// 编译期断言：接口实现必须完整。
var _ apps.Store = (*AppsRepository)(nil)

// Apps 返回应用仓储。
func (s *Store) Apps() *AppsRepository { return &AppsRepository{s: s} }

const appColumns = `id, name, preset_id, kind, source_path, local_port, state, health,
	COALESCE(health_detail,''), auto_start, max_restarts, restart_count,
	COALESCE(last_error,''), plan_json, domains, COALESCE(route_id,''), COALESCE(ddns_task_id,''),
	created_at, updated_at`

// SaveApp 写入或更新一个应用。
func (a *AppsRepository) SaveApp(ctx context.Context, app apps.App) error {
	plan, err := json.Marshal(app.Plan)
	if err != nil {
		return fmt.Errorf("marshal app plan: %w", err)
	}
	stamp := now()
	created := formatTime(app.CreatedAt)
	if app.CreatedAt.IsZero() {
		created = stamp
	}
	_, err = a.s.db.ExecContext(ctx, `INSERT INTO apps (
			id, name, preset_id, kind, source_path, local_port, state, health, health_detail,
			auto_start, max_restarts, restart_count, last_error, plan_json, domains,
			route_id, ddns_task_id, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, preset_id=excluded.preset_id, kind=excluded.kind,
			source_path=excluded.source_path, local_port=excluded.local_port,
			state=excluded.state, health=excluded.health, health_detail=excluded.health_detail,
			auto_start=excluded.auto_start, max_restarts=excluded.max_restarts,
			restart_count=excluded.restart_count, last_error=excluded.last_error,
			plan_json=excluded.plan_json, domains=excluded.domains,
			route_id=excluded.route_id, ddns_task_id=excluded.ddns_task_id,
			updated_at=excluded.updated_at`,
		app.ID, app.Name, app.PresetID, string(app.Kind), app.SourcePath, app.LocalPort,
		string(app.State), string(app.Health), nullIfEmpty(app.HealthDetail),
		boolToIntValue(app.AutoStart), app.MaxRestarts, app.RestartCount,
		nullIfEmpty(app.LastError), string(plan), strings.Join(app.Domains, "\n"),
		nullIfEmpty(app.RouteID), nullIfEmpty(app.DDNSTaskID), created, stamp)
	if err != nil {
		return fmt.Errorf("save app: %w", err)
	}
	return nil
}

// GetApp 读取单个应用。
func (a *AppsRepository) GetApp(ctx context.Context, id string) (apps.App, bool, error) {
	row := a.s.db.QueryRowContext(ctx, `SELECT `+appColumns+` FROM apps WHERE id = ?`, id)
	app, err := scanApp(row)
	if err != nil {
		if isNoRows(err) {
			return apps.App{}, false, nil
		}
		return apps.App{}, false, err
	}
	return app, true, nil
}

// ListApps 返回全部应用，按创建时间排序。
func (a *AppsRepository) ListApps(ctx context.Context) ([]apps.App, error) {
	rows, err := a.s.db.QueryContext(ctx, `SELECT `+appColumns+` FROM apps ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	defer rows.Close()

	out := make([]apps.App, 0)
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate apps: %w", err)
	}
	return out, nil
}

// DeleteApp 删除一个应用。
func (a *AppsRepository) DeleteApp(ctx context.Context, id string) (bool, error) {
	result, err := a.s.db.ExecContext(ctx, `DELETE FROM apps WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("delete app: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func scanApp(row rowScanner) (apps.App, error) {
	var (
		app                                          apps.App
		kind, state, health                          string
		autoStart, maxRestarts, restartCount         int64
		planJSON, domains, createdAt, updatedAt      string
		healthDetail, lastError, routeID, ddnsTaskID string
	)
	if err := row.Scan(&app.ID, &app.Name, &app.PresetID, &kind, &app.SourcePath,
		&app.LocalPort, &state, &health, &healthDetail, &autoStart, &maxRestarts,
		&restartCount, &lastError, &planJSON, &domains, &routeID, &ddnsTaskID,
		&createdAt, &updatedAt); err != nil {
		return apps.App{}, err
	}
	app.Kind = runtime.Kind(kind)
	app.State = apps.State(state)
	app.Health = apps.Health(health)
	app.HealthDetail = healthDetail
	app.AutoStart = autoStart != 0
	app.MaxRestarts = int(maxRestarts)
	app.RestartCount = int(restartCount)
	app.LastError = lastError
	app.RouteID = routeID
	app.DDNSTaskID = ddnsTaskID
	app.Domains = splitLines(domains)
	if planJSON != "" {
		if err := json.Unmarshal([]byte(planJSON), &app.Plan); err != nil {
			// 计划读不出来时不静默用一个空计划：那会让"启动"变成
			// 启动一个没有命令的东西。把应用置为失败并说明原因。
			app.State = apps.StateFailed
			app.LastError = "stored plan could not be read: " + err.Error()
		}
	}
	app.CreatedAt = parseTime(createdAt)
	app.UpdatedAt = parseTime(updatedAt)
	return app, nil
}
