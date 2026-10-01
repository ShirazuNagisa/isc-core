-- ---------------------------------------------------------------------------
-- 0003_change_journal —— 系统变更日志
--
-- 每一次对系统状态的修改（防火墙规则、服务注册、证书文件……）都必须在
-- **动手之前**先在这里留下记录。原因见 internal/change 的包注释：
-- 数据库可以回滚事务，防火墙不行 —— 我们必须自己记住改了什么。
--
-- 记录里存的是**渲染后的文本**（steps 里含人类可读的差异），而不是
-- 重建差异所需的原始状态。日志的用途是"事后看清发生了什么"，
-- 而那时原始状态早已不存在，重新计算差异既不可能也没有意义。
-- ---------------------------------------------------------------------------

CREATE TABLE change_journal (
    plan_id    TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,
    title      TEXT NOT NULL,
    risk       TEXT NOT NULL DEFAULT 'low',
    -- applying | applied | failed | rolled_back | rollback_failed
    status     TEXT NOT NULL,

    -- 步骤快照（JSON 数组）。用 JSON 而不是子表：
    -- 步骤只被整体读写，从不被单独查询，拆表只会增加连接成本。
    steps      TEXT NOT NULL DEFAULT '[]',
    warnings   TEXT NOT NULL DEFAULT '[]',
    notes      TEXT NOT NULL DEFAULT '[]',

    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- 按时间倒序列出是控制台与 CLI 的主要查询方式。
CREATE INDEX idx_change_journal_created ON change_journal (created_at DESC);

-- 启动时要查"停留在 applying 状态的记录"（可能的中断变更）。
CREATE INDEX idx_change_journal_status ON change_journal (status);
