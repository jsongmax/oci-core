// Package store 是 SQLite 持久层。
//
// 选 SQLite 而非 Postgres：本工具的部署形态是一台小 VPS 上的单进程，
// 数据量是几十个账号加几千条审计记录。引入独立数据库进程只会增加运维负担。
// 所有查询都走本包暴露的方法，便于日后需要时整体替换。
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 实现的 SQLite，免 CGO，可交叉编译

	"ocicore/internal/cryptobox"
)

// ErrNotFound 表示按 ID 查询的记录不存在。
var ErrNotFound = errors.New("store: 记录不存在")

// ErrConflict 表示违反了唯一约束（重复的用户名、租户或账号代号）。
var ErrConflict = errors.New("store: 记录已存在")

// Store 持有数据库连接与用于私钥加解密的 Box。
type Store struct {
	db  *sql.DB
	box *cryptobox.Box
}

// Open 打开（必要时创建）数据库并执行建表语句。
func Open(path string, box *cryptobox.Box) (*Store, error) {
	if box == nil {
		return nil, errors.New("store: 缺少 cryptobox，私钥无法加密存储")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("store: 创建数据目录失败: %w", err)
		}
	}

	// WAL 提升并发读性能；busy_timeout 避免瞬时锁冲突直接报错；
	// foreign_keys 让 ON DELETE CASCADE 真正生效（SQLite 默认是关的）。
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: 打开数据库失败: %w", err)
	}

	// SQLite 的写是串行的，放开连接数只会制造锁竞争。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: 连接数据库失败: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: 初始化表结构失败: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}

	s := &Store{db: db, box: box}
	// 数据迁移放在建表与列迁移之后：它要写 proxies 表，也要用到 box 加密。
	// 失败不阻断启动——旧的 proxy_url 字段还在，建连时有兜底分支。
	if err := s.migrateLegacyProxies(context.Background()); err != nil {
		slog.Warn("旧代理配置迁移失败，将继续使用账号上的原字段", "err", err)
	}
	return s, nil
}

// migrations 是对已有库的增量变更。
//
// SQLite 的 ALTER TABLE ADD COLUMN 不支持 IF NOT EXISTS，重复执行会报
// "duplicate column name"。这里把那一种错误当作"已经加过了"放行，
// 其余错误照常抛出——比自己去查 pragma table_info 简单且不易出错。
var migrations = []string{
	`ALTER TABLE accounts ADD COLUMN subscribed_regions TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE accounts ADD COLUMN home_region TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE accounts ADD COLUMN email TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE accounts ADD COLUMN tenancy_name TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE accounts ADD COLUMN payment_model TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE accounts ADD COLUMN subscription_state TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE accounts ADD COLUMN subscription_ends_at INTEGER`,
	`ALTER TABLE instances ADD COLUMN running_since INTEGER`,
	`ALTER TABLE accounts ADD COLUMN subscription_starts_at INTEGER`,
	`ALTER TABLE instances ADD COLUMN note TEXT NOT NULL DEFAULT ''`,
	// hunt_tasks 在这个字段之前就已经发布过了，光靠 CREATE TABLE IF NOT EXISTS
	// 补不上——已有的库里那张表还是旧结构。
	`ALTER TABLE hunt_tasks ADD COLUMN precheck_capacity INTEGER NOT NULL DEFAULT 1`,
	// 代理从"账号的一个字符串字段"升级成独立实体。
	//
	// 旧的 proxy_url 列保留不动：已有部署里可能填过值，启动时会被
	// 迁移进 proxies 表并回填 proxy_id（见 migrateLegacyProxies）。
	// 直接删列会让回滚到旧版本的人丢配置。
	`ALTER TABLE accounts ADD COLUMN proxy_id TEXT`,
}

func migrate(db *sql.DB) error {
	for _, stmt := range migrations {
		if _, err := db.Exec(stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("store: 执行迁移失败 (%s): %w", stmt, err)
		}
	}
	return nil
}

// Close 关闭数据库连接。
func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层连接，仅供测试与迁移工具使用。
func (s *Store) DB() *sql.DB { return s.db }

// schema 是完整的建表语句，全部 IF NOT EXISTS，可重复执行。
//
// 时间统一存 INTEGER Unix 秒：SQLite 没有原生时间类型，
// 存文本会引入时区和格式歧义，存整数则完全无歧义。
const schema = `
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    totp_secret   TEXT NOT NULL DEFAULT '',
    totp_enabled  INTEGER NOT NULL DEFAULT 0,
    -- 记录最近一次成功使用的时间窗，用于拒绝同一验证码的重放
    totp_last_counter INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    -- 存令牌的 SHA-256，库泄露时无法反推出可用的会话令牌
    token_hash    TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- 通过密码但尚未通过 TOTP 的会话为 0，此状态只允许调用 TOTP 校验接口
    totp_verified INTEGER NOT NULL DEFAULT 0,
    ip            TEXT NOT NULL DEFAULT '',
    user_agent    TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user    ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS accounts (
    id               TEXT PRIMARY KEY,
    alias            TEXT NOT NULL,
    -- 三字母短代号，与身份色一同构成账号身份。全局唯一
    code             TEXT NOT NULL UNIQUE,
    -- 身份色序号 1..8，对应前端的 acct-1..acct-8
    color_index      INTEGER NOT NULL,
    tenancy_ocid     TEXT NOT NULL UNIQUE,
    user_ocid        TEXT NOT NULL,
    fingerprint      TEXT NOT NULL,
    -- AES-256-GCM 密文与 nonce。明文私钥从不落盘
    key_ciphertext   BLOB NOT NULL,
    key_nonce        BLOB NOT NULL,
    default_region   TEXT NOT NULL,
    compartment_ocid TEXT NOT NULL DEFAULT '',
    proxy_url        TEXT NOT NULL DEFAULT '',
    enabled          INTEGER NOT NULL DEFAULT 1,
    -- unchecked / ok / error
    status           TEXT NOT NULL DEFAULT 'unchecked',
    status_message   TEXT NOT NULL DEFAULT '',
    last_checked_at  INTEGER,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    TEXT NOT NULL DEFAULT '',
    action     TEXT NOT NULL,
    account_id TEXT NOT NULL DEFAULT '',
    target     TEXT NOT NULL DEFAULT '',
    detail     TEXT NOT NULL DEFAULT '',
    ip         TEXT NOT NULL DEFAULT '',
    -- ok / fail
    result     TEXT NOT NULL DEFAULT 'ok',
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_account ON audit_logs(account_id);

-- 实例缓存。跨账号聚合要对每个（账号 × 区域）发一轮 API 调用，
-- 没有缓存的话用户每次打开列表都得等上好几秒。
CREATE TABLE IF NOT EXISTS instances (
    id                  TEXT PRIMARY KEY,
    account_id          TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    region              TEXT NOT NULL,
    compartment_id      TEXT NOT NULL DEFAULT '',
    display_name        TEXT NOT NULL DEFAULT '',
    availability_domain TEXT NOT NULL DEFAULT '',
    fault_domain        TEXT NOT NULL DEFAULT '',
    shape               TEXT NOT NULL DEFAULT '',
    ocpus               REAL NOT NULL DEFAULT 0,
    memory_gb           REAL NOT NULL DEFAULT 0,
    lifecycle_state     TEXT NOT NULL DEFAULT '',
    image_id            TEXT NOT NULL DEFAULT '',
    public_ip           TEXT NOT NULL DEFAULT '',
    private_ip          TEXT NOT NULL DEFAULT '',
    ipv6                TEXT NOT NULL DEFAULT '',
    vnic_id             TEXT NOT NULL DEFAULT '',
    subnet_id           TEXT NOT NULL DEFAULT '',
    boot_volume_id      TEXT NOT NULL DEFAULT '',
    boot_volume_gb      INTEGER NOT NULL DEFAULT 0,
    boot_volume_vpus    INTEGER NOT NULL DEFAULT 0,
    time_created        INTEGER NOT NULL DEFAULT 0,
    synced_at           INTEGER NOT NULL DEFAULT 0,
    -- 最近一次操作失败的原因。非空时前端在该行浮出错误条，
    -- 对应设计规格里"失败必须可见地回滚"那一条。
    last_error          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_instances_account ON instances(account_id);
CREATE INDEX IF NOT EXISTS idx_instances_region  ON instances(region);
CREATE INDEX IF NOT EXISTS idx_instances_state   ON instances(lifecycle_state);

-- 全局设置。键值对存储，避免每加一个开关就改一次表结构。
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

-- 通知渠道。config 是各渠道自己的 JSON 配置（token、webhook 地址等），
-- events 是订阅的事件类型 JSON 数组。
CREATE TABLE IF NOT EXISTS notification_channels (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,
    name       TEXT NOT NULL,
    config     TEXT NOT NULL DEFAULT '{}',
    events     TEXT NOT NULL DEFAULT '[]',
    enabled    INTEGER NOT NULL DEFAULT 1,
    last_error TEXT NOT NULL DEFAULT '',
    last_sent_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
-- 容量守候（抢机）任务。
--
-- Oracle 的免费 ARM 长期没有容量，LaunchInstance 大概率返回容量不足，
-- 需要在容量释放的那一刻恰好发出请求。任务表让这件事能跨进程重启存活。
CREATE TABLE IF NOT EXISTS hunt_tasks (
    id               TEXT PRIMARY KEY,
    account_id       TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    region           TEXT NOT NULL,
    name             TEXT NOT NULL DEFAULT '',
    -- 创建参数的 JSON 快照。拆成列的话每加一个 OCI 参数都要迁一次表。
    spec             TEXT NOT NULL,
    -- 轮换范围，逗号分隔。空表示该区域全部可用域。
    ads              TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL DEFAULT 'running',
    attempts         INTEGER NOT NULL DEFAULT 0,
    interval_seconds INTEGER NOT NULL DEFAULT 60,
    -- 先查容量报告，报告说没货就跳过这一轮，不发创建请求。
    -- 默认开：容量报告是只读的，而 LaunchInstance 才是风控盯的那个。
    precheck_capacity INTEGER NOT NULL DEFAULT 1,
    last_class       TEXT NOT NULL DEFAULT '',
    last_error       TEXT NOT NULL DEFAULT '',
    last_ad          TEXT NOT NULL DEFAULT '',
    last_try_at      INTEGER NOT NULL DEFAULT 0,
    -- 下次可尝试的时刻。调度器只看这一个字段，且它是持久化的——
    -- 只存内存的话每次重启都把退避清零，等于变相提高请求频率。
    next_at          INTEGER NOT NULL DEFAULT 0,
    instance_id      TEXT NOT NULL DEFAULT '',
    max_attempts     INTEGER NOT NULL DEFAULT 0,
    expires_at       INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_hunt_due ON hunt_tasks(state, next_at);
CREATE INDEX IF NOT EXISTS idx_hunt_account ON hunt_tasks(account_id);

-- 容量监控。
--
-- 数据来自 Oracle 官方的容量报告接口（只读，不创建任何资源），不是靠反复调
-- 创建接口试探出来的。每行是「账号 × 区域 × 可用域 × 规格」的一个监控项。
CREATE TABLE IF NOT EXISTS capacity_watches (
    id            TEXT PRIMARY KEY,
    account_id    TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    region        TEXT NOT NULL,
    -- 完整可用域名，不是 AD-1 这种显示用的简写。
    availability_domain TEXT NOT NULL,
    shape         TEXT NOT NULL,
    ocpus         REAL NOT NULL DEFAULT 0,
    memory_gb     REAL NOT NULL DEFAULT 0,
    enabled       INTEGER NOT NULL DEFAULT 1,

    -- AVAILABLE / OUT_OF_HOST_CAPACITY / HARDWARE_NOT_SUPPORTED
    last_status   TEXT NOT NULL DEFAULT '',
    last_count    INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT NOT NULL DEFAULT '',
    -- 最近一次查询的时刻，和最近一次「状态发生变化」的时刻。
    -- 通知只在后者变动时推：天天告诉用户"还是没货"没有意义。
    last_checked_at INTEGER NOT NULL DEFAULT 0,
    last_changed_at INTEGER NOT NULL DEFAULT 0,

    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    UNIQUE(account_id, availability_domain, shape, ocpus, memory_gb)
);
CREATE INDEX IF NOT EXISTS idx_capacity_enabled ON capacity_watches(enabled, last_checked_at);

-- 代理池。
--
-- 代理是一等实体而不是账号的一个字段：要能独立查看存活状态、独立管理，
-- 而且必须能检测「同一条代理绑给了两个账号」——那种情况下隔离是假的，
-- 反而把两个账号绑在同一个出口 IP 上，凭空制造关联信号。
CREATE TABLE IF NOT EXISTS proxies (
    id            TEXT PRIMARY KEY,
    label         TEXT NOT NULL DEFAULT '',
    -- http / https / socks5
    scheme        TEXT NOT NULL,
    host          TEXT NOT NULL,
    port          INTEGER NOT NULL,
    username      TEXT NOT NULL DEFAULT '',
    -- 密码与 OCI 私钥同等待遇：AES-256-GCM 加密，AAD 绑定本行 id，
    -- 明文从不落盘。无密码时两列为 NULL。
    pass_ciphertext BLOB,
    pass_nonce      BLOB,
    enabled       INTEGER NOT NULL DEFAULT 1,

    -- ok / fail / unknown
    last_status     TEXT NOT NULL DEFAULT 'unknown',
    last_latency_ms INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL DEFAULT '',
    last_region     TEXT NOT NULL DEFAULT '',
    last_checked_at INTEGER NOT NULL DEFAULT 0,
    last_ok_at      INTEGER NOT NULL DEFAULT 0,

    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    -- 同一个出口不该重复录入。scheme 也算进来：同一台机器的 http 与
    -- socks5 端口是两条独立的路。
    UNIQUE(scheme, host, port, username)
);
`

// newID 生成 16 位十六进制的主键。对本工具的规模而言，128 位熵远超需求。
func newID() (string, error) {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("store: 生成 ID 失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func nowUnix() int64 { return time.Now().Unix() }

// unixToTime 把数据库里的 Unix 秒转成 time.Time。
func unixToTime(v int64) time.Time { return time.Unix(v, 0) }

// nullUnixToTime 处理可空的时间列。
func nullUnixToTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0)
	return &t
}

// isUniqueViolation 判断错误是否来自唯一约束冲突。
// modernc 驱动不暴露结构化错误码，只能匹配消息文本。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

// PurgeExpiredSessions 删除过期会话。调用方应定期执行。
func (s *Store) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, nowUnix())
	if err != nil {
		return 0, fmt.Errorf("store: 清理过期会话失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
