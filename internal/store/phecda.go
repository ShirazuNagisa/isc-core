package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/google/uuid"
)

// PhecdaRepository persists Phecda metadata without owning runtime supervision.
type PhecdaRepository struct{ s *Store }

func (s *Store) Phecda() *PhecdaRepository { return &PhecdaRepository{s: s} }

func (p *PhecdaRepository) ListProjects(ctx context.Context) ([]gen.PhecdaProject, error) {
	rows, err := p.s.db.QueryContext(ctx, `SELECT id, name, purpose, source_json, COALESCE(detected_runtime,''), COALESCE(selected_preset_id,'') FROM phecda_projects ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list Phecda projects: %w", err)
	}
	defer rows.Close()
	out := make([]gen.PhecdaProject, 0)
	for rows.Next() {
		project, id, err := scanPhecdaProject(rows)
		if err != nil {
			return nil, err
		}
		project.Evidence, err = p.listEvidence(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Phecda projects: %w", err)
	}
	return out, nil
}

func (p *PhecdaRepository) GetProject(ctx context.Context, id uuid.UUID) (gen.PhecdaProject, bool, error) {
	row := p.s.db.QueryRowContext(ctx, `SELECT id, name, purpose, source_json, COALESCE(detected_runtime,''), COALESCE(selected_preset_id,'') FROM phecda_projects WHERE id = ?`, id.String())
	project, projectID, err := scanPhecdaProject(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return gen.PhecdaProject{}, false, nil
		}
		return gen.PhecdaProject{}, false, err
	}
	project.Evidence, err = p.listEvidence(ctx, projectID)
	return project, err == nil, err
}

func (p *PhecdaRepository) SaveProject(ctx context.Context, project gen.PhecdaProject) error {
	source, err := json.Marshal(project.Source)
	if err != nil {
		return fmt.Errorf("marshal Phecda source: %w", err)
	}
	var runtime, preset any
	if project.DetectedRuntime != nil {
		runtime = string(*project.DetectedRuntime)
	}
	if project.SelectedPresetId != nil {
		preset = *project.SelectedPresetId
	}
	tx, err := p.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO phecda_projects (id,name,purpose,source_json,detected_runtime,selected_preset_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,purpose=excluded.purpose,source_json=excluded.source_json,detected_runtime=excluded.detected_runtime,selected_preset_id=excluded.selected_preset_id,updated_at=excluded.updated_at`, project.Id.String(), project.Name, project.Purpose, string(source), runtime, preset, now(), now())
	if err != nil {
		return fmt.Errorf("save Phecda project: %w", err)
	}
	if err = p.replaceEvidenceTx(ctx, tx, project.Id.String(), project.Evidence); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *PhecdaRepository) DeleteProject(ctx context.Context, id uuid.UUID) (bool, error) {
	result, err := p.s.db.ExecContext(ctx, `DELETE FROM phecda_projects WHERE id = ?`, id.String())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func (p *PhecdaRepository) SaveEvidence(ctx context.Context, id uuid.UUID, evidence []gen.PhecdaScanEvidence) error {
	tx, err := p.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := p.replaceEvidenceTx(ctx, tx, id.String(), evidence); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *PhecdaRepository) listEvidence(ctx context.Context, id string) ([]gen.PhecdaScanEvidence, error) {
	rows, err := p.s.db.QueryContext(ctx, `SELECT file, signal, confidence FROM phecda_scan_evidence WHERE project_id = ? ORDER BY file, signal`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]gen.PhecdaScanEvidence, 0)
	for rows.Next() {
		var e gen.PhecdaScanEvidence
		if err := rows.Scan(&e.File, &e.Signal, &e.Confidence); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (p *PhecdaRepository) replaceEvidenceTx(ctx context.Context, tx *sql.Tx, id string, evidence []gen.PhecdaScanEvidence) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM phecda_scan_evidence WHERE project_id = ?`, id); err != nil {
		return err
	}
	for _, e := range evidence {
		if _, err := tx.ExecContext(ctx, `INSERT INTO phecda_scan_evidence (project_id,file,signal,confidence) VALUES (?,?,?,?)`, id, e.File, e.Signal, e.Confidence); err != nil {
			return err
		}
	}
	return nil
}

type phecdaScanner interface{ Scan(dest ...any) error }

func scanPhecdaProject(row phecdaScanner) (gen.PhecdaProject, string, error) {
	var id, source string
	var purpose, name, runtime, preset string
	if err := row.Scan(&id, &name, &purpose, &source, &runtime, &preset); err != nil {
		return gen.PhecdaProject{}, "", err
	}
	parsed := gen.PhecdaProject{Id: uuid.MustParse(id), Name: name, Purpose: purpose, Evidence: []gen.PhecdaScanEvidence{}}
	if err := json.Unmarshal([]byte(source), &parsed.Source); err != nil {
		return gen.PhecdaProject{}, "", fmt.Errorf("decode Phecda source: %w", err)
	}
	if runtime != "" {
		value := gen.PhecdaProjectDetectedRuntime(runtime)
		parsed.DetectedRuntime = &value
	}
	if preset != "" {
		parsed.SelectedPresetId = &preset
	}
	return parsed, id, nil
}

func (p *PhecdaRepository) SaveDeployment(ctx context.Context, deployment gen.PhecdaDeployment) error {
	_, err := p.s.db.ExecContext(ctx, `INSERT INTO phecda_deployments (id, project_id, preset_id, state, local_port, last_error) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET project_id=excluded.project_id, preset_id=excluded.preset_id, state=excluded.state, local_port=excluded.local_port, last_error=excluded.last_error`, deployment.Id.String(), deployment.ProjectId.String(), deployment.PresetId, string(deployment.State), nullableInt(deployment.LocalPort), nullableString(deployment.LastError))
	return err
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}
func nullableString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}
func (p *PhecdaRepository) ListDeployments(ctx context.Context) ([]gen.PhecdaDeployment, error) {
	rows, err := p.s.db.QueryContext(ctx, `SELECT id, project_id, preset_id, state, local_port, last_error FROM phecda_deployments ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]gen.PhecdaDeployment, 0)
	for rows.Next() {
		var id, project, preset, state string
		var port sql.NullInt64
		var last sql.NullString
		if err := rows.Scan(&id, &project, &preset, &state, &port, &last); err != nil {
			return nil, err
		}
		d := gen.PhecdaDeployment{Id: uuid.MustParse(id), ProjectId: uuid.MustParse(project), PresetId: preset, State: gen.PhecdaDeploymentState(state)}
		if port.Valid {
			v := int(port.Int64)
			d.LocalPort = &v
		}
		if last.Valid {
			d.LastError = &last.String
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (p *PhecdaRepository) GetDeployment(ctx context.Context, id uuid.UUID) (gen.PhecdaDeployment, bool, error) {
	var project, preset, state string
	var port sql.NullInt64
	var last sql.NullString
	err := p.s.db.QueryRowContext(ctx, `SELECT project_id,preset_id,state,local_port,last_error FROM phecda_deployments WHERE id=?`, id.String()).Scan(&project, &preset, &state, &port, &last)
	if err == sql.ErrNoRows {
		return gen.PhecdaDeployment{}, false, nil
	}
	if err != nil {
		return gen.PhecdaDeployment{}, false, err
	}
	d := gen.PhecdaDeployment{Id: id, ProjectId: uuid.MustParse(project), PresetId: preset, State: gen.PhecdaDeploymentState(state)}
	if port.Valid {
		v := int(port.Int64)
		d.LocalPort = &v
	}
	if last.Valid {
		d.LastError = &last.String
	}
	return d, true, nil
}
