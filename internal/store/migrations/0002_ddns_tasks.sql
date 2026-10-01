-- ---------------------------------------------------------------------------
-- 0002_ddns_tasks —— 动态解析任务
--
-- 一条任务 = 一组凭据 + 一组域名 + 一组 IP 获取方式。
-- 它对应 ddns-go 配置里的一条 `dnsconf` 条目。
--
-- 域名字段用换行分隔的 TEXT 而不是子表：域名是有序的、数量在个位数到
-- 几十，且**整体替换**（用户在界面上一次编辑全部）。拆成子表会引入
-- 排序字段与"部分更新"的语义，而这两种复杂度在这个场景里没有任何收益。
-- ---------------------------------------------------------------------------

CREATE TABLE ddns_tasks (
    id            TEXT PRIMARY KEY,
    credential_id TEXT NOT NULL,
    label         TEXT NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 1,

    -- IPv4 来源
    ipv4_enable      INTEGER NOT NULL DEFAULT 0,
    -- netInterface | url | cmd
    ipv4_get_type    TEXT NOT NULL DEFAULT '',
    -- 网卡名 / URL 列表 / 命令，含义由 get_type 决定
    ipv4_source      TEXT NOT NULL DEFAULT '',
    ipv4_domains     TEXT NOT NULL DEFAULT '',
    -- IPv6 来源
    ipv6_enable      INTEGER NOT NULL DEFAULT 0,
    ipv6_get_type    TEXT NOT NULL DEFAULT '',
    ipv6_source      TEXT NOT NULL DEFAULT '',
    ipv6_domains     TEXT NOT NULL DEFAULT '',
    -- IPv6 地址选择：正则或 @N（取第 N 个）
    ipv6_selector    TEXT NOT NULL DEFAULT '',

    ttl            TEXT NOT NULL DEFAULT '',
    http_interface TEXT NOT NULL DEFAULT '',

    -- 运行状态（由调度器写入）
    last_run_at   TEXT,
    last_status   TEXT NOT NULL DEFAULT '',
    last_message  TEXT NOT NULL DEFAULT '',
    last_ipv4     TEXT NOT NULL DEFAULT '',
    last_ipv6     TEXT NOT NULL DEFAULT '',

    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE INDEX idx_ddns_tasks_enabled ON ddns_tasks (enabled);

-- 注意：这里**刻意不加外键**指向 credentials。
--
-- 理由是删除凭据时应当由应用层给出"它仍被 N 个任务使用"的明确提示，
-- 而不是让数据库抛一个外键约束错误 —— 后者的信息量对用户为零。
-- 应用层的检查见 credential 的删除路径。
