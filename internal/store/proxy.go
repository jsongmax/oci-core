package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"ocicore/internal/proxypool"
)

// Proxy 是代理池里的一条。
//
// 注意这个结构体会被直接序列化成 API 响应，因此**不含密码明文**——
// 密码只在 ProxyURL() 内部解密一次，用完即弃。这跟账号私钥是同一条规矩：
// 界面上没有任何回显凭据的入口。
type Proxy struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Scheme   string `json:"scheme"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	// HasPassword 只说明有没有配密码，不透露内容。
	HasPassword bool `json:"hasPassword"`
	Enabled     bool `json:"enabled"`

	LastStatus    string    `json:"lastStatus"`
	LastLatencyMs int64     `json:"lastLatencyMs"`
	LastError     string    `json:"lastError"`
	LastRegion    string    `json:"lastRegion"`
	LastCheckedAt time.Time `json:"lastCheckedAt"`
	LastOKAt      time.Time `json:"lastOkAt"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Display 是给人看的地址，密码已打码。
func (p Proxy) Display() string {
	var auth string
	if p.Username != "" {
		auth = p.Username + ":****@"
	}
	return fmt.Sprintf("%s://%s%s:%d", p.Scheme, auth, p.Host, p.Port)
}

const proxyColumns = `id, label, scheme, host, port, username,
	pass_ciphertext, pass_nonce, enabled,
	last_status, last_latency_ms, last_error, last_region,
	last_checked_at, last_ok_at, created_at, updated_at`

// ErrProxyExists 表示同一个出口已经录过了。
var ErrProxyExists = errors.New("该代理已存在")

// CreateProxy 落库一条代理，密码加密存储。
func (s *Store) CreateProxy(ctx context.Context, p proxypool.Parsed) (*Proxy, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}

	var cipher, nonce []byte
	if p.Password != "" {
		cipher, nonce, err = s.box.Seal([]byte(p.Password), id)
		if err != nil {
			return nil, fmt.Errorf("store: 加密代理密码失败: %w", err)
		}
	}

	now := nowUnix()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO proxies (id, label, scheme, host, port, username,
			pass_ciphertext, pass_nonce, enabled, last_status,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 'unknown', ?, ?)`,
		id, p.Label, p.Scheme, p.Host, p.Port, p.Username,
		cipher, nonce, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrProxyExists
		}
		return nil, fmt.Errorf("store: 写入代理失败: %w", err)
	}
	return s.GetProxy(ctx, id)
}

// GetProxy 按 id 取一条。
func (s *Store) GetProxy(ctx context.Context, id string) (*Proxy, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+proxyColumns+` FROM proxies WHERE id = ?`, id)
	p, _, _, err := scanProxy(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// ListProxies 返回全部代理，按备注名与地址排序，顺序稳定。
func (s *Store) ListProxies(ctx context.Context) ([]Proxy, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+proxyColumns+` FROM proxies ORDER BY label, host, port`)
	if err != nil {
		return nil, fmt.Errorf("store: 查询代理失败: %w", err)
	}
	defer rows.Close()

	out := make([]Proxy, 0)
	for rows.Next() {
		p, _, _, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ProxyURL 解密并拼出可直接交给 http.Transport 的地址。
//
// 这是密码明文唯一出现的地方，返回值不该被记日志、不该进 API 响应。
func (s *Store) ProxyURL(ctx context.Context, id string) (string, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+proxyColumns+` FROM proxies WHERE id = ?`, id)
	p, cipher, nonce, err := scanProxy(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}

	parsed := proxypool.Parsed{
		Scheme: p.Scheme, Host: p.Host, Port: p.Port, Username: p.Username,
	}
	if len(cipher) > 0 {
		plain, err := s.box.Open(cipher, nonce, p.ID)
		if err != nil {
			return "", fmt.Errorf("store: 解密代理密码失败（主密钥是否换过？）: %w", err)
		}
		parsed.Password = string(plain)
	}
	return parsed.URL(), nil
}

// ProxyUpdate 描述可修改的字段，nil 表示不动。
type ProxyUpdate struct {
	Label   *string
	Enabled *bool
	// Password 非 nil 时重设密码；空字符串表示清除密码。
	Password *string
}

// UpdateProxy 修改一条代理。
func (s *Store) UpdateProxy(ctx context.Context, id string, up ProxyUpdate) (*Proxy, error) {
	sets := []string{"updated_at = ?"}
	args := []any{nowUnix()}
	add := func(clause string, v any) {
		sets = append(sets, clause)
		args = append(args, v)
	}

	if up.Label != nil {
		add("label = ?", strings.TrimSpace(*up.Label))
	}
	if up.Enabled != nil {
		add("enabled = ?", boolToInt(*up.Enabled))
	}
	if up.Password != nil {
		if *up.Password == "" {
			add("pass_ciphertext = ?", nil)
			add("pass_nonce = ?", nil)
		} else {
			cipher, nonce, err := s.box.Seal([]byte(*up.Password), id)
			if err != nil {
				return nil, fmt.Errorf("store: 加密代理密码失败: %w", err)
			}
			add("pass_ciphertext = ?", cipher)
			add("pass_nonce = ?", nonce)
		}
	}

	args = append(args, id)
	res, err := s.db.ExecContext(ctx,
		`UPDATE proxies SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 更新代理失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}

	// 顺手把绑定了这条代理的账号的 updated_at 推一下。
	//
	// ociconn 的客户端缓存是按账号的 updated_at 判失效的。改了代理密码
	// 却不动账号行，那些账号会继续用**旧密码**建好的连接，直到下次
	// 账号本身被改动为止——表现是"密码明明改对了还是 407"。
	if _, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET updated_at = ? WHERE proxy_id = ?`, nowUnix(), id); err != nil {
		return nil, fmt.Errorf("store: 刷新账号连接失效标记失败: %w", err)
	}
	return s.GetProxy(ctx, id)
}

// migrateLegacyProxies 把旧的 accounts.proxy_url 搬进 proxies 表。
//
// 代理原先只是账号上的一个字符串字段。升级成独立实体后，已有部署里
// 那些值必须自动接过来，否则用户升级完会发现代理静默失效——而这类
// 失效不会报错，只会表现为"这个账号突然连不上了"。
//
// 幂等：已经有 proxy_id 的账号跳过；同一个出口被多个账号用过时，
// 只有第一个能绑上（唯一约束 + 禁止共用），其余留空并保留原字段，
// 由用户到界面上显式处理——那种情况下他本来就该重新分配。
func (s *Store) migrateLegacyProxies(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, alias, proxy_url FROM accounts
		WHERE proxy_url != '' AND (proxy_id IS NULL OR proxy_id = '')`)
	if err != nil {
		return fmt.Errorf("store: 查询待迁移代理失败: %w", err)
	}
	type legacy struct{ id, alias, url string }
	var pending []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.id, &l.alias, &l.url); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, l := range pending {
		parsed, err := proxypool.ParseLine(l.url)
		if err != nil {
			// 解析不了就留着原样，不阻断启动。旧字段还在，
			// proxyFor 的兜底分支仍会用它。
			continue
		}
		if parsed.Label == "" {
			parsed.Label = l.alias
		}
		p, err := s.CreateProxy(ctx, parsed)
		if errors.Is(err, ErrProxyExists) {
			continue
		}
		if err != nil {
			continue
		}
		if err := s.BindProxy(ctx, l.id, p.ID); err != nil {
			continue
		}
	}
	return nil
}

// RecordProxyCheck 写回一次存活检测的结果。
//
// last_ok_at 只在成功时推进：界面要能区分"刚才失败了"和"从来没通过"。
func (s *Store) RecordProxyCheck(ctx context.Context, id string, r proxypool.CheckResult) error {
	now := nowUnix()
	okAt := int64(0)
	if r.Status == proxypool.StatusOK {
		okAt = now
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE proxies SET
			last_status = ?, last_latency_ms = ?, last_error = ?, last_region = ?,
			last_checked_at = ?, last_ok_at = MAX(last_ok_at, ?), updated_at = ?
		WHERE id = ?`,
		r.Status, r.LatencyMs, r.Error, r.Region, now, okAt, now, id)
	if err != nil {
		return fmt.Errorf("store: 写入检测结果失败: %w", err)
	}
	return nil
}

// DeleteProxy 删除一条代理。
//
// 仍被账号绑定时拒绝：静默解绑会让那个账号在用户不知情的情况下
// 回落到本机直连——而用代理的全部目的就是不要那样。
func (s *Store) DeleteProxy(ctx context.Context, id string) error {
	var bound int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM accounts WHERE proxy_id = ?`, id).Scan(&bound); err != nil {
		return fmt.Errorf("store: 检查代理绑定失败: %w", err)
	}
	if bound > 0 {
		return fmt.Errorf("该代理仍被 %d 个账号绑定，请先解绑", bound)
	}

	res, err := s.db.ExecContext(ctx, `DELETE FROM proxies WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: 删除代理失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

/* ---------- 绑定 ---------- */

// ProxyBindings 返回 accountID -> proxyID 的映射，只含已绑定的账号。
func (s *Store) ProxyBindings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, proxy_id FROM accounts WHERE proxy_id IS NOT NULL AND proxy_id != ''`)
	if err != nil {
		return nil, fmt.Errorf("store: 查询代理绑定失败: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var accID, proxyID string
		if err := rows.Scan(&accID, &proxyID); err != nil {
			return nil, err
		}
		out[accID] = proxyID
	}
	return out, rows.Err()
}

// BindProxy 把代理绑到账号上。proxyID 为空表示解绑（回到本机直连）。
//
// 一条代理只能绑一个账号，重复绑定直接拒绝而不是警告：共用出口比不用代理
// 更糟——它把两个本来从不同网络访问的账号绑在同一个 IP 上，凭空制造一个
// 关联信号，与这个功能的目的正好相反。
func (s *Store) BindProxy(ctx context.Context, accountID, proxyID string) error {
	proxyID = strings.TrimSpace(proxyID)

	if proxyID != "" {
		if _, err := s.GetProxy(ctx, proxyID); err != nil {
			return err
		}
		bindings, err := s.ProxyBindings(ctx)
		if err != nil {
			return err
		}
		if dup := proxypool.DuplicateOf(bindings, proxyID, accountID); len(dup) > 0 {
			return proxypool.ErrDuplicateBinding
		}
	}

	var val any
	if proxyID != "" {
		val = proxyID
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET proxy_id = ?, updated_at = ? WHERE id = ?`,
		val, nowUnix(), accountID)
	if err != nil {
		return fmt.Errorf("store: 绑定代理失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AccountProxyID 返回账号绑定的代理 id，未绑定时为空串。
func (s *Store) AccountProxyID(ctx context.Context, accountID string) (string, error) {
	var id sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT proxy_id FROM accounts WHERE id = ?`, accountID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: 查询账号代理失败: %w", err)
	}
	return id.String, nil
}

/* ---------- 内部 ---------- */

func scanProxy(row rowScanner) (*Proxy, []byte, []byte, error) {
	var (
		p                          Proxy
		cipher, nonce              []byte
		enabled                    int
		lastCheckedAt, lastOKAt    int64
		createdAt, updatedAt       int64
		label, username, lastError sql.NullString
		lastRegion                 sql.NullString
	)
	err := row.Scan(&p.ID, &label, &p.Scheme, &p.Host, &p.Port, &username,
		&cipher, &nonce, &enabled,
		&p.LastStatus, &p.LastLatencyMs, &lastError, &lastRegion,
		&lastCheckedAt, &lastOKAt, &createdAt, &updatedAt)
	if err != nil {
		return nil, nil, nil, err
	}

	p.Label = label.String
	p.Username = username.String
	p.LastError = lastError.String
	p.LastRegion = lastRegion.String
	p.HasPassword = len(cipher) > 0
	p.Enabled = enabled == 1
	p.LastCheckedAt = unixToTime(lastCheckedAt)
	p.LastOKAt = unixToTime(lastOKAt)
	p.CreatedAt = unixToTime(createdAt)
	p.UpdatedAt = unixToTime(updatedAt)
	return &p, cipher, nonce, nil
}
