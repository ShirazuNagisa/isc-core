-- Published services: the binding between a Phecda project's deployment and the DNS,
-- reverse-proxy and certificate objects the kernel already owns.
--
-- Why the record lives here rather than in a file the GUI keeps: phecda_deployments
-- already carried a public_service_id, so the kernel could reference an id whose record
-- only the GUI knew about. Deleting that record left the kernel pointing at nothing, and
-- nothing could tell the difference. With the record in the same database, the reference
-- is resolvable by construction and PublicServices.Replace can clear it in the same
-- transaction that removes the service.
CREATE TABLE public_services (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL,
    -- One domain per line, like proxy_routes.domains: ordered, single digits in count,
    -- and always edited as a whole in the UI. A child table would add ordering and
    -- partial-update semantics that nothing here needs.
    domains    TEXT NOT NULL DEFAULT '',
    -- References into ddns_tasks / proxy_routes. Deliberately not foreign keys: the user
    -- may delete a task from the Dynamic DNS page while the published service stays, and
    -- the UI reports that as "missing" rather than silently dropping the service.
    ddns_id    TEXT,
    route_id   TEXT,
    favorite   INTEGER NOT NULL DEFAULT 0,
    -- User ordering, kept as a column so List can sort in SQL.
    sort_order INTEGER NOT NULL DEFAULT 0,
    verified_at          TEXT,
    verified_fingerprint TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_public_services_order ON public_services (sort_order);
