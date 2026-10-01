-- ---------------------------------------------------------------------------
-- 0001_init —— M1 的基础表
--
-- 约定（全库一致）：
--   * 主键统一用 TEXT（随机 ID），避免自增 ID 泄露"总共创建过多少条"
--     这类信息，也让将来做同步/合并时不必处理 ID 冲突；
--     唯一例外是 jobs 与 audit_log，它们需要稳定的时间序，用自增列。
--   * 时间统一以 TEXT 存 RFC 3339（UTC，纳秒精度）。SQLite 没有原生
--     时间类型，而 TEXT 形式既可直接字符串比较排序，也能用 sqlite3
--     命令行直接读懂 —— 排查问题时这一点很值钱。
--   * 一切敏感值以 `*_cipher` 命名并存 BLOB（AES-256-GCM 信封），
--     明文绝不入库。密钥版本随密文一起存，为将来的密钥轮换留路。
-- ---------------------------------------------------------------------------

-- 运行时设置：简单的键值对。
-- 之所以不做成宽表：设置项会随里程碑不断增加，宽表每次都要迁移，
-- 而键值对只需在应用层定义默认值与校验。
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- DNS 服务商凭据。
--
-- secret_cipher 里是凭据字段的 JSON（含 API Key），用主密钥加密。
-- 之所以整体加密而不是逐字段加密：凭据是一个整体，部分加密会让
-- "哪些字段是敏感的"变成每个查询都要关心的问题，容易漏。
CREATE TABLE credentials (
    id               TEXT PRIMARY KEY,
    provider         TEXT NOT NULL,
    label            TEXT NOT NULL,
    secret_cipher    BLOB NOT NULL,
    key_version      INTEGER NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    last_verified_at TEXT,
    last_verify_ok   INTEGER,
    last_verify_error TEXT
);

-- 同一服务商下标签唯一：否则界面上会出现两个无法区分的凭据，
-- 用户改错一个就会莫名其妙地解析失败。
CREATE UNIQUE INDEX idx_credentials_provider_label
    ON credentials (provider, label);

-- 审计日志：所有写操作的留痕。
--
-- 存在的意义：内核以系统服务身份运行、能改防火墙、能重写整个 DNS 区域。
-- 出问题时"最后一次改动是什么、谁改的、结果如何"是排查的第一入口。
--
-- 刻意不设外键：审计记录必须在被审计对象删除后依然存在，
-- 否则"谁删掉了这条凭据"这个最关键的问题恰好查不到。
CREATE TABLE audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         TEXT NOT NULL,
    action     TEXT NOT NULL,
    target     TEXT,
    result     TEXT NOT NULL,
    detail     TEXT,
    request_id TEXT,
    remote     TEXT
);

CREATE INDEX idx_audit_ts ON audit_log (ts DESC);
CREATE INDEX idx_audit_action ON audit_log (action);

-- 异步任务。
--
-- seq 是自增的时间序，用于稳定分页：created_at 在同一毫秒内可能重复，
-- 靠时间戳排序会得到不确定的顺序（翻页时出现重复或漏项）。
CREATE TABLE jobs (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    id          TEXT NOT NULL UNIQUE,
    kind        TEXT NOT NULL,
    status      TEXT NOT NULL,
    progress    REAL NOT NULL DEFAULT 0,
    message     TEXT,
    result      TEXT,
    error       TEXT,
    created_at  TEXT NOT NULL,
    started_at  TEXT,
    finished_at TEXT
);

CREATE INDEX idx_jobs_created ON jobs (seq DESC);
CREATE INDEX idx_jobs_status ON jobs (status);
CREATE INDEX idx_jobs_kind ON jobs (kind);
