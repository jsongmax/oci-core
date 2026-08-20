package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"ocicore/internal/ociclient"
)

// 账号连通性状态。与前端 AccountCard 的四种状态一一对应
// （checking 是前端的瞬时态，不落库）。
const (
	StatusUnchecked = "unchecked"
	StatusOK        = "ok"
	StatusError     = "error"
)

// ColorCount 是账号身份色的数量，对应前端的 acct-1..acct-8。
const ColorCount = 8

// Account 是一个已接入的 OCI 账号。
//
// 这个结构体会被直接序列化成 API 响应，因此**刻意不包含任何私钥字段**。
// 需要私钥时必须显式调用 Credentials，那是唯一的解密入口。
type Account struct {
	ID              string     `json:"id"`
	Alias           string     `json:"alias"`
	Code            string     `json:"code"`
	ColorIndex      int        `json:"colorIndex"`
	TenancyOCID     string     `json:"tenancyOcid"`
	UserOCID        string     `json:"userOcid"`
	Fingerprint     string     `json:"fingerprint"`
	DefaultRegion   string     `json:"defaultRegion"`
	CompartmentOCID string     `json:"compartmentOcid"`
	ProxyURL        string     `json:"proxyUrl"`
	Enabled         bool       `json:"enabled"`
	Status          string     `json:"status"`
	StatusMessage   string     `json:"statusMessage"`
	LastCheckedAt   *time.Time `json:"lastCheckedAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`

	// SubscribedRegions 是该租户已订阅的区域，在连通性校验时写入。
	// 跨账号聚合靠它决定要去哪些区域拉实例——没有这份清单就只能盲扫全部
	// 三十多个区域，绝大多数都是空跑。
	SubscribedRegions []string `json:"subscribedRegions"`
	HomeRegion        string   `json:"homeRegion"`

	// Email 与 TenancyName 来自校验时的 GetUser / GetTenancy，
	// 让账号卡片能显示"这到底是哪个甲骨文账号"，而不是只有一串 OCID。
	Email       string `json:"email"`
	TenancyName string `json:"tenancyName"`

	// PaymentModel 区分试用号与升级号：FREE_TRIAL / PAY_AS_YOU_GO。
	//
	// 不能拿配额值反推。试用期内的账号拿到的限额远高于永久免费额度
	// （实测 ARM 16 OCPU / 96 GB，而 2026-06 起永久免费只有 2 / 12），看起来和升级号
	// 一模一样——直到试用到期被打回原形，超出的实例会被回收。
	PaymentModel string `json:"paymentModel"`
	// SubscriptionState 常见值 ACTIVE / CANCELED。
	SubscriptionState string `json:"subscriptionState"`
	// SubscriptionStartsAt 是这个甲骨文账号开户的时刻。
	//
	// 注意与 CreatedAt 的区别：CreatedAt 是"什么时候接进本面板"，
	// 这个才是"这个甲骨文账号存在了多久"。两者差别可以很大，
	// 界面上必须分清楚，否则一个刚接进来的老号会显示成新号。
	SubscriptionStartsAt *time.Time `json:"subscriptionStartsAt"`
	// SubscriptionEndsAt 在试用订阅上就是试用到期日。
	SubscriptionEndsAt *time.Time `json:"subscriptionEndsAt"`
}

// EffectiveRegions 返回同步实例时应当遍历的区域列表。
// 尚未取到订阅列表时退回默认区域，保证功能可用而不是直接空转。
func (a *Account) EffectiveRegions() []string {
	if len(a.SubscribedRegions) > 0 {
		return a.SubscribedRegions
	}
	if a.DefaultRegion != "" {
		return []string{a.DefaultRegion}
	}
	return nil
}

// NewAccount 是创建账号所需的输入。PrivateKeyPEM 仅在本次调用中存在，
// 加密后即被丢弃，不会保留在任何可导出的结构里。
type NewAccount struct {
	Alias           string
	Code            string
	ColorIndex      int // 0 表示自动分配
	TenancyOCID     string
	UserOCID        string
	Fingerprint     string
	PrivateKeyPEM   string
	DefaultRegion   string
	CompartmentOCID string
	ProxyURL        string
}

// AccountUpdate 描述可修改的字段。指针为 nil 表示不改动该字段。
type AccountUpdate struct {
	Alias           *string
	Code            *string
	ColorIndex      *int
	DefaultRegion   *string
	CompartmentOCID *string
	ProxyURL        *string
	Enabled         *bool
	// PrivateKeyPEM 非 nil 时轮换私钥。空字符串是非法输入，由调用方拦截。
	PrivateKeyPEM *string
	Fingerprint   *string
	UserOCID      *string
}

const accountColumns = `id, alias, code, color_index, tenancy_ocid, user_ocid, fingerprint,
	default_region, compartment_ocid, proxy_url, enabled, status, status_message,
	last_checked_at, created_at, updated_at, subscribed_regions, home_region,
	email, tenancy_name, payment_model, subscription_state,
	subscription_starts_at, subscription_ends_at`

// CreateAccount 校验凭据格式、加密私钥并落库。
//
// 这里会做一次完整的离线校验（PEM 可解析、指纹与私钥匹配、OCID 格式正确），
// 目的是让配置错误在写库之前就暴露出来，而不是等到第一次调用 API 才报 401。
func (s *Store) CreateAccount(ctx context.Context, in NewAccount) (*Account, error) {
	key, err := ociclient.ParsePrivateKey([]byte(in.PrivateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("私钥解析失败: %w", err)
	}

	creds := &ociclient.Credentials{
		TenancyOCID: strings.TrimSpace(in.TenancyOCID),
		UserOCID:    strings.TrimSpace(in.UserOCID),
		Fingerprint: strings.ToLower(strings.TrimSpace(in.Fingerprint)),
		Region:      ociclient.NormalizeRegion(in.DefaultRegion),
		PrivateKey:  key,
	}
	if err := creds.Validate(); err != nil {
		return nil, err
	}

	code, err := NormalizeCode(in.Code)
	if err != nil {
		return nil, err
	}
	alias := strings.TrimSpace(in.Alias)
	if alias == "" {
		return nil, errors.New("账号别名不能为空")
	}

	colorIndex := in.ColorIndex
	if colorIndex == 0 {
		if colorIndex, err = s.nextColorIndex(ctx); err != nil {
			return nil, err
		}
	}
	if colorIndex < 1 || colorIndex > ColorCount {
		return nil, fmt.Errorf("身份色序号必须在 1..%d 之间", ColorCount)
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}

	// AAD 绑定账号 ID：即使攻击者能改库，也无法把别的账号的密文搬到这一行冒用。
	ciphertext, nonce, err := s.box.Seal([]byte(in.PrivateKeyPEM), id)
	if err != nil {
		return nil, err
	}

	now := nowUnix()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO accounts (id, alias, code, color_index, tenancy_ocid, user_ocid, fingerprint,
			key_ciphertext, key_nonce, default_region, compartment_ocid, proxy_url,
			enabled, status, status_message, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1,?,'',?,?)`,
		id, alias, code, colorIndex, creds.TenancyOCID, creds.UserOCID, creds.Fingerprint,
		ciphertext, nonce, creds.Region, strings.TrimSpace(in.CompartmentOCID), strings.TrimSpace(in.ProxyURL),
		StatusUnchecked, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: 该租户或代号已被占用", ErrConflict)
		}
		return nil, fmt.Errorf("store: 写入账号失败: %w", err)
	}

	return s.GetAccount(ctx, id)
}

// ListAccounts 返回全部账号，按创建时间升序——这样身份色的分配顺序稳定可预期。
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountColumns+` FROM accounts ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: 查询账号列表失败: %w", err)
	}
	defer rows.Close()

	accounts := make([]Account, 0, 8)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, *a)
	}
	return accounts, rows.Err()
}

// GetAccount 按 ID 取回账号。
func (s *Store) GetAccount(ctx context.Context, id string) (*Account, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = ?`, id)
	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// UpdateAccount 修改账号。轮换私钥时会重新校验指纹与新私钥是否匹配。
func (s *Store) UpdateAccount(ctx context.Context, id string, up AccountUpdate) (*Account, error) {
	current, err := s.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}

	sets := []string{"updated_at = ?"}
	args := []any{nowUnix()}

	add := func(clause string, value any) {
		sets = append(sets, clause)
		args = append(args, value)
	}

	if up.Alias != nil {
		alias := strings.TrimSpace(*up.Alias)
		if alias == "" {
			return nil, errors.New("账号别名不能为空")
		}
		add("alias = ?", alias)
	}
	if up.Code != nil {
		code, err := NormalizeCode(*up.Code)
		if err != nil {
			return nil, err
		}
		add("code = ?", code)
	}
	if up.ColorIndex != nil {
		if *up.ColorIndex < 1 || *up.ColorIndex > ColorCount {
			return nil, fmt.Errorf("身份色序号必须在 1..%d 之间", ColorCount)
		}
		add("color_index = ?", *up.ColorIndex)
	}
	if up.DefaultRegion != nil {
		region := ociclient.NormalizeRegion(*up.DefaultRegion)
		if region == "" {
			return nil, errors.New("默认区域不能为空")
		}
		add("default_region = ?", region)
	}
	if up.CompartmentOCID != nil {
		add("compartment_ocid = ?", strings.TrimSpace(*up.CompartmentOCID))
	}
	if up.ProxyURL != nil {
		add("proxy_url = ?", strings.TrimSpace(*up.ProxyURL))
	}
	if up.Enabled != nil {
		add("enabled = ?", boolToInt(*up.Enabled))
	}
	if up.UserOCID != nil {
		add("user_ocid = ?", strings.TrimSpace(*up.UserOCID))
	}

	// 私钥轮换：新私钥必须与（新的或现有的）指纹匹配，否则拒绝写入。
	if up.PrivateKeyPEM != nil {
		key, err := ociclient.ParsePrivateKey([]byte(*up.PrivateKeyPEM))
		if err != nil {
			return nil, fmt.Errorf("私钥解析失败: %w", err)
		}
		fingerprint := current.Fingerprint
		if up.Fingerprint != nil {
			fingerprint = strings.ToLower(strings.TrimSpace(*up.Fingerprint))
		}
		actual := ociclient.FingerprintOf(&key.PublicKey)
		if !strings.EqualFold(fingerprint, actual) {
			return nil, fmt.Errorf("指纹与新私钥不匹配：配置为 %s，私钥实际指纹为 %s", fingerprint, actual)
		}

		ciphertext, nonce, err := s.box.Seal([]byte(*up.PrivateKeyPEM), id)
		if err != nil {
			return nil, err
		}
		add("key_ciphertext = ?", ciphertext)
		add("key_nonce = ?", nonce)
		add("fingerprint = ?", fingerprint)
		// 密钥变了，之前的校验结论作废。
		add("status = ?", StatusUnchecked)
		add("status_message = ?", "")
	} else if up.Fingerprint != nil {
		return nil, errors.New("修改指纹必须同时提供匹配的新私钥")
	}

	args = append(args, id)
	_, err = s.db.ExecContext(ctx, `UPDATE accounts SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: 该代号已被占用", ErrConflict)
		}
		return nil, fmt.Errorf("store: 更新账号失败: %w", err)
	}
	return s.GetAccount(ctx, id)
}

// DeleteAccount 删除账号及其密文。
func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: 删除账号失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Credentials 解密私钥并组装出可用于调用 OCI 的凭据。
//
// 这是全项目**唯一**的私钥解密入口。返回值只应传给 ociclient，
// 绝不能进入任何 HTTP 响应、日志或错误信息。
func (s *Store) Credentials(ctx context.Context, id string) (*ociclient.Credentials, error) {
	var (
		ciphertext, nonce               []byte
		tenancy, user, fingerprint, reg string
	)
	row := s.db.QueryRowContext(ctx, `
		SELECT key_ciphertext, key_nonce, tenancy_ocid, user_ocid, fingerprint, default_region
		FROM accounts WHERE id = ?`, id)
	if err := row.Scan(&ciphertext, &nonce, &tenancy, &user, &fingerprint, &reg); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: 读取账号凭据失败: %w", err)
	}

	pem, err := s.box.Open(ciphertext, nonce, id)
	if err != nil {
		return nil, err
	}
	key, err := ociclient.ParsePrivateKey(pem)
	if err != nil {
		return nil, fmt.Errorf("已存储的私钥无法解析: %w", err)
	}

	return &ociclient.Credentials{
		TenancyOCID: tenancy,
		UserOCID:    user,
		Fingerprint: fingerprint,
		Region:      reg,
		PrivateKey:  key,
	}, nil
}

// SetAccountStatus 记录一次连通性校验的结果。
func (s *Store) SetAccountStatus(ctx context.Context, id, status, message string) error {
	now := nowUnix()
	res, err := s.db.ExecContext(ctx, `
		UPDATE accounts SET status = ?, status_message = ?, last_checked_at = ?, updated_at = ?
		WHERE id = ?`, status, message, now, now, id)
	if err != nil {
		return fmt.Errorf("store: 更新账号状态失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// nextColorIndex 挑一个当前使用最少的身份色。
//
// 简单的"取最小未用序号"在删除账号后会造成颜色反复复用，
// 按使用次数挑选能让色彩分布在增删之后依然均匀。
func (s *Store) nextColorIndex(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT color_index, COUNT(*) FROM accounts GROUP BY color_index`)
	if err != nil {
		return 0, fmt.Errorf("store: 统计身份色失败: %w", err)
	}
	defer rows.Close()

	usage := make([]int, ColorCount+1)
	for rows.Next() {
		var idx, count int
		if err := rows.Scan(&idx, &count); err != nil {
			return 0, err
		}
		if idx >= 1 && idx <= ColorCount {
			usage[idx] = count
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	best, bestCount := 1, usage[1]
	for i := 2; i <= ColorCount; i++ {
		if usage[i] < bestCount {
			best, bestCount = i, usage[i]
		}
	}
	return best, nil
}

// NormalizeCode 校验并归一化账号短代号：2–4 位字母或数字，统一转大写。
//
// 代号是身份色的文字搭档。可访问性要求身份色不能单独承载信息，
// 因此代号是必填项而非可选装饰。
func NormalizeCode(code string) (string, error) {
	c := strings.ToUpper(strings.TrimSpace(code))
	if len(c) < 2 || len(c) > 4 {
		return "", errors.New("账号代号必须是 2–4 个字符")
	}
	for _, r := range c {
		isLetter := r >= 'A' && r <= 'Z'
		isDigit := r >= '0' && r <= '9'
		if !isLetter && !isDigit {
			return "", errors.New("账号代号只能包含字母和数字")
		}
	}
	return c, nil
}

// SuggestCode 从区域名推导一个默认代号，例如 ap-tokyo-1 → TYO。
// 仅作为表单预填，用户可以随意改。
func SuggestCode(region string) string {
	r := ociclient.NormalizeRegion(region)
	parts := strings.Split(r, "-")
	if len(parts) >= 2 && len(parts[1]) >= 2 {
		city := parts[1]
		if len(city) > 3 {
			city = city[:3]
		}
		return strings.ToUpper(city)
	}
	return "ACC"
}

// rowScanner 抽象 *sql.Row 与 *sql.Rows 的共同接口。
type rowScanner interface {
	Scan(dest ...any) error
}

// AccountIdentity 是连通性校验带回来的账号身份信息。
type AccountIdentity struct {
	Regions     []string
	HomeRegion  string
	Email       string
	TenancyName string

	PaymentModel         string
	SubscriptionState    string
	SubscriptionStartsAt *time.Time
	SubscriptionEndsAt   *time.Time
}

// SetAccountIdentity 记录校验时探测到的租户信息。
//
// 空字段一律不覆盖已有值：某次校验因为权限不足没取到邮箱，
// 不该把上次成功取到的值抹掉。
func (s *Store) SetAccountIdentity(ctx context.Context, id string, in AccountIdentity) error {
	sets := []string{"updated_at = ?"}
	args := []any{nowUnix()}

	if len(in.Regions) > 0 {
		sets = append(sets, "subscribed_regions = ?")
		args = append(args, strings.Join(in.Regions, ","))
	}
	if in.HomeRegion != "" {
		sets = append(sets, "home_region = ?")
		args = append(args, in.HomeRegion)
	}
	if in.Email != "" {
		sets = append(sets, "email = ?")
		args = append(args, in.Email)
	}
	if in.TenancyName != "" {
		sets = append(sets, "tenancy_name = ?")
		args = append(args, in.TenancyName)
	}
	if in.PaymentModel != "" {
		sets = append(sets, "payment_model = ?")
		args = append(args, in.PaymentModel)
	}
	if in.SubscriptionState != "" {
		sets = append(sets, "subscription_state = ?")
		args = append(args, in.SubscriptionState)
	}
	if in.SubscriptionStartsAt != nil {
		sets = append(sets, "subscription_starts_at = ?")
		args = append(args, in.SubscriptionStartsAt.Unix())
	}
	if in.SubscriptionEndsAt != nil {
		sets = append(sets, "subscription_ends_at = ?")
		args = append(args, in.SubscriptionEndsAt.Unix())
	}
	if len(sets) == 1 {
		return nil
	}

	args = append(args, id)
	_, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("store: 更新账号身份信息失败: %w", err)
	}
	return nil
}

func scanAccount(sc rowScanner) (*Account, error) {
	var (
		a          Account
		enabled    int
		lastCheck  sql.NullInt64
		created    int64
		updated    int64
		regionsCSV string
		subStarts  sql.NullInt64
		subEnds    sql.NullInt64
	)
	err := sc.Scan(
		&a.ID, &a.Alias, &a.Code, &a.ColorIndex, &a.TenancyOCID, &a.UserOCID, &a.Fingerprint,
		&a.DefaultRegion, &a.CompartmentOCID, &a.ProxyURL, &enabled, &a.Status, &a.StatusMessage,
		&lastCheck, &created, &updated, &regionsCSV, &a.HomeRegion,
		&a.Email, &a.TenancyName,
		&a.PaymentModel, &a.SubscriptionState, &subStarts, &subEnds,
	)
	if err != nil {
		return nil, err
	}
	a.Enabled = enabled != 0
	a.LastCheckedAt = nullUnixToTime(lastCheck)
	a.SubscriptionStartsAt = nullUnixToTime(subStarts)
	a.SubscriptionEndsAt = nullUnixToTime(subEnds)
	a.CreatedAt = unixToTime(created)
	a.UpdatedAt = unixToTime(updated)
	// 区域名里不含逗号，用 CSV 存比 JSON 更省事也更容易人工查看。
	if regionsCSV != "" {
		a.SubscribedRegions = strings.Split(regionsCSV, ",")
	}
	return &a, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
