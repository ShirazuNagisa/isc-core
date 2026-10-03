-- Phecda project metadata, source references, scan evidence and deployment state.
CREATE TABLE phecda_projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    purpose TEXT NOT NULL,
    source_json TEXT NOT NULL,
    detected_runtime TEXT,
    selected_preset_id TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE phecda_scan_evidence (
    project_id TEXT NOT NULL,
    file TEXT NOT NULL,
    signal TEXT NOT NULL,
    confidence REAL NOT NULL,
    PRIMARY KEY (project_id, file, signal),
    FOREIGN KEY (project_id) REFERENCES phecda_projects(id) ON DELETE CASCADE
);

CREATE TABLE phecda_deployments (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    preset_id TEXT NOT NULL,
    state TEXT NOT NULL,
    local_port INTEGER,
    last_error TEXT,
    FOREIGN KEY (project_id) REFERENCES phecda_projects(id) ON DELETE CASCADE
);

CREATE INDEX idx_phecda_deployments_project ON phecda_deployments(project_id);
