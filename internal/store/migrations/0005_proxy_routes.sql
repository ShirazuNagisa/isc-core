-- ---------------------------------------------------------------------------
-- 0005_proxy_routes —— 反向代理的转发规则
--
-- 一条规则 = 一组域名 + 一个本机上游。
--
-- domains 用换行分隔的 TEXT 而不是子表：与 ddns_tasks 的理由相同 ——
-- 域名是有序的、数量在个位数，且**整体替换**（用户在界面上一次编辑
-- 全部）。拆成子表会引入排序字段与"部分更新"的语义，而这两种复杂度
-- 在这个场景里没有任何收益。
-- ---------------------------------------------------------------------------

CREATE TABLE proxy_routes (
    id         TEXT PRIMARY KEY,
    label      TEXT NOT NULL DEFAULT '',
    domains    TEXT NOT NULL DEFAULT '',
    -- 形如 http://127.0.0.1:8096。写入前必须经过 proxy.ValidateUpstream。
    upstream   TEXT NOT NULL,
    -- 该域名是否需要 HTTPS。
    tls        INTEGER NOT NULL DEFAULT 0,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_proxy_routes_enabled ON proxy_routes (enabled);
