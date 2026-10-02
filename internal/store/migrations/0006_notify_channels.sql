-- ---------------------------------------------------------------------------
-- 0006_notify_channels —— 通知通道
--
-- 一条记录 = 一个投递目标（一个 Webhook 地址）。
--
-- headers 与 body_template 用 TEXT 存：前者是一个 JSON 对象，后者是
-- 一段模板文本。它们都只被整体读写，从不被单独查询 —— 拆成列或子表
-- 只会增加复杂度而没有收益。
--
-- 注意：**不要在 headers 里存明文密钥**。那个字段会被接口原样返回。
-- 需要鉴权时用 body_template 或者让接收端校验来源 IP。
-- ---------------------------------------------------------------------------

CREATE TABLE notify_channels (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    -- webhook | log
    kind          TEXT NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 1,

    -- Webhook 专用
    url           TEXT NOT NULL DEFAULT '',
    method        TEXT NOT NULL DEFAULT 'POST',
    headers       TEXT NOT NULL DEFAULT '{}',
    body_template TEXT NOT NULL DEFAULT '',

    -- 该通道的最低发送级别：info | warning | error
    min_severity  TEXT NOT NULL DEFAULT 'info',

    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE INDEX idx_notify_channels_enabled ON notify_channels (enabled);
