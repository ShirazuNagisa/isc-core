-- ---------------------------------------------------------------------------
-- 0011_remote —— 远程管理面（ISC Mizar）的设备表与推送投递
--
-- 与前几张表不同，这张表里装的主要是**访问凭据**，而不是用户的数据。
-- 由此有两条额外的约定：
--
--   * 设备令牌只存 sha256（`token_hash`）。明文只在签发的那一个响应里
--     出现过一次，之后**任何地方都取不回来** —— 数据库被读走不等于
--     令牌被读走。代价是丢了令牌只能重新配对，这是刻意的。
--
--   * 吊销是**软删除**（`revoked_at`）。已吊销的设备保留在列表里：
--     "这台设备是什么时候被断开的"是排查丢手机那件事的第一入口，
--     行删掉就什么都查不到了。
--
-- UNIQUE 索引建在 token_hash 上：令牌是随机的，碰撞概率可以忽略，
-- 而这个约束保证"一次查询至多命中一台设备"是数据库层面的性质，
-- 而不是靠代码里记得加 LIMIT。
-- ---------------------------------------------------------------------------

CREATE TABLE remote_devices (
    id                   TEXT PRIMARY KEY,
    label                TEXT    NOT NULL,
    -- viewer | operator。取值由应用层校验，这里不加 CHECK：
    -- 将来多一级角色时，加 CHECK 会让一次纯应用层的改动变成一次迁移。
    role                 TEXT    NOT NULL,
    -- 派生令牌的来源设备。NULL 表示直接配对而来。
    parent_device_id     TEXT,
    token_hash           BLOB    NOT NULL,
    -- 客户端自称的信息，只用于展示与排查，不参与任何判定。
    platform             TEXT,
    model                TEXT,
    os_version           TEXT,
    app_version          TEXT,
    notifications_enabled INTEGER NOT NULL DEFAULT 0,
    apns_token           TEXT,
    apns_environment     TEXT,
    apns_topic           TEXT,
    created_at           TEXT    NOT NULL,
    updated_at           TEXT    NOT NULL,
    last_seen_at         TEXT,
    last_seen_ip         TEXT,
    revoked_at           TEXT
);

CREATE UNIQUE INDEX idx_remote_devices_token ON remote_devices (token_hash);

-- 级联吊销要按父设备找子设备，这条索引是那个递归查询的入口。
CREATE INDEX idx_remote_devices_parent ON remote_devices (parent_device_id);

-- 推送投递流水。
--
-- 存在的理由是排查：推送是本项目里失败现场最远的一环（凭据、topic、
-- 令牌、sandbox/production 环境、Apple 那一侧），没有这份流水就只能靠猜。
--
-- 刻意**不建外键**：与 audit_log 同一个理由 —— 投递记录必须在设备
-- 被吊销之后依然可读，否则"这条推送到底发出去了没有"恰好查不到。
CREATE TABLE remote_push_deliveries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          TEXT    NOT NULL,
    device_id   TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    dedupe_key  TEXT    NOT NULL,
    status      TEXT    NOT NULL,
    http_status INTEGER,
    reason      TEXT,
    apns_id     TEXT
);

CREATE INDEX idx_remote_push_device ON remote_push_deliveries (device_id, id DESC);
