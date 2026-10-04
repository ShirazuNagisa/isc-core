-- Hosted applications: one row per site/service the kernel runs for the user.
--
-- Why this lives in the kernel rather than in the GUI: from v0.2.0 the kernel owns
-- the whole path from a source directory to a public HTTPS site (see D25). The
-- record has to survive a kernel restart, because it is what the restart logic
-- reconciles against — a process handle does not survive, but "this app should be
-- running on this port with this plan" does.
--
-- plan_json carries the materialised build/run plan (steps, ports, environment).
-- Storing the plan rather than re-deriving it on every start is deliberate: the
-- source directory may have changed since, and silently re-detecting would turn
-- "restart my site" into "redeploy something else".
CREATE TABLE apps (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    preset_id     TEXT NOT NULL,
    -- Runtime kind ('' for static sites, which the kernel serves itself).
    kind          TEXT NOT NULL DEFAULT '',
    source_path   TEXT NOT NULL,
    local_port    INTEGER NOT NULL,
    state         TEXT NOT NULL,
    health        TEXT NOT NULL DEFAULT 'unknown',
    health_detail TEXT,
    auto_start    INTEGER NOT NULL DEFAULT 1,
    max_restarts  INTEGER NOT NULL DEFAULT 3,
    restart_count INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT,
    plan_json     TEXT NOT NULL DEFAULT '{}',
    -- One domain per line, same convention as proxy_routes.domains.
    domains       TEXT NOT NULL DEFAULT '',
    -- References into proxy_routes / ddns_tasks. Not foreign keys: the user may
    -- delete a route from the reverse-proxy page while the app stays, and the UI
    -- reports that as "public access not ready" rather than dropping the app.
    route_id      TEXT,
    ddns_task_id  TEXT,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE INDEX idx_apps_state ON apps (state);
