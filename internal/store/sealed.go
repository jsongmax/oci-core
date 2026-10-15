package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// sealedPrefix 标记一个 TEXT 列里存的是密文而不是旧版明文。
//
// 用前缀而不是新增 BLOB 列：TOTP 密钥与渠道配置原本就是 TEXT，
// 加前缀能让新旧数据在同一列共存，读取时按前缀分流，迁移可以逐行进行，
// 中途失败也不会让任何一行变得不可读。
const sealedPrefix = "enc:v1:"

// 各类密文的 AAD 前缀。AAD 再拼上行 ID：即使攻击者能写库，
// 也无法把 A 用户的 TOTP 密文搬到 B 用户，或把渠道配置搬到另一个渠道上冒用。
const (
	aadTOTPSecret     = "users.totp_secret:"
	aadChannelConfig  = "notification_channels.config:"
	aadLegacyProxyURL = "accounts.proxy_url:"
)

// sealText 把 plain 加密成可存入 TEXT 列的形式：前缀 + base64(nonce ‖ 密文)。
// 空串原样返回，保持"未设置"的语义。
func (s *Store) sealText(plain, aad string) (string, error) {
	if plain == "" {
		return "", nil
	}
	ciphertext, nonce, err := s.box.Seal([]byte(plain), aad)
	if err != nil {
		return "", err
	}
	buf := make([]byte, 0, len(nonce)+len(ciphertext))
	buf = append(buf, nonce...)
	buf = append(buf, ciphertext...)
	return sealedPrefix + base64.StdEncoding.EncodeToString(buf), nil
}

// openText 是 sealText 的逆操作。不带前缀的值是升级前写入的明文，原样返回——
// 启动迁移会把它们改写成密文，这里只是迁移没跑成时的兼容。
func (s *Store) openText(stored, aad string) (string, error) {
	if !isSealed(stored) {
		return stored, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, sealedPrefix))
	if err != nil {
		return "", fmt.Errorf("store: 密文格式错误: %w", err)
	}
	n := s.box.NonceSize()
	if len(raw) < n {
		return "", errors.New("store: 密文长度不足")
	}
	plain, err := s.box.Open(raw[n:], raw[:n], aad)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func isSealed(v string) bool { return strings.HasPrefix(v, sealedPrefix) }

func (s *Store) sealTOTPSecret(userID, secret string) (string, error) {
	sealed, err := s.sealText(secret, aadTOTPSecret+userID)
	if err != nil {
		return "", fmt.Errorf("store: 加密 TOTP 密钥失败: %w", err)
	}
	return sealed, nil
}

// migratePlaintextSecrets 把升级前以明文落库的 TOTP 密钥与通知渠道配置改写成密文。
//
// 幂等：已带前缀的行不会被选中。逐行更新，任何一行失败都不影响其余行，
// 未迁移的行仍能按明文读出。changed 是实际改写的行数。
func (s *Store) migratePlaintextSecrets(ctx context.Context) (changed int, err error) {
	type row struct{ id, value string }
	collect := func(query string) ([]row, error) {
		rows, err := s.db.QueryContext(ctx, query, sealedPrefix+"%")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.value); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}

	users, err := collect(`SELECT id, totp_secret FROM users WHERE totp_secret != '' AND totp_secret NOT LIKE ?`)
	if err != nil {
		return 0, fmt.Errorf("store: 查询待加密的 TOTP 密钥失败: %w", err)
	}
	channels, err := collect(`SELECT id, config FROM notification_channels WHERE config NOT LIKE ?`)
	if err != nil {
		return 0, fmt.Errorf("store: 查询待加密的渠道配置失败: %w", err)
	}

	var errs []error
	for _, u := range users {
		sealed, err := s.sealTOTPSecret(u.id, u.value)
		if err == nil {
			// 条件里带上旧值：迁移期间若有并发写入，以新写入的为准。
			err = s.execCounting(ctx, &changed,
				`UPDATE users SET totp_secret = ? WHERE id = ? AND totp_secret = ?`, sealed, u.id, u.value)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("用户 %s: %w", u.id, err))
		}
	}
	for _, c := range channels {
		sealed, err := s.sealText(c.value, aadChannelConfig+c.id)
		if err == nil {
			err = s.execCounting(ctx, &changed,
				`UPDATE notification_channels SET config = ? WHERE id = ? AND config = ?`, sealed, c.id, c.value)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("渠道 %s: %w", c.id, err))
		}
	}
	return changed, errors.Join(errs...)
}

// execCounting 执行一条写语句，把影响行数累加到 changed。
func (s *Store) execCounting(ctx context.Context, changed *int, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	*changed += int(n)
	return nil
}

// checkpointAfterMigration 在启动迁移改写过凭据之后，把 WAL 并回主库并截断日志。
//
// 迁移把明文改成了密文（或清空），但改动先落在 WAL 里：checkpoint 之前，主库文件中
// 那一页仍是旧内容，WAL 文件里也留着中间页。secure_delete 只负责页内被删内容清零，
// 不会替我们把 WAL 合回主库。SQLite 在最后一个连接关闭时也会 checkpoint，所以这类
// 残留本是暂时的；这里是为了不让它在进程整个生命周期里都留在盘上。
//
// busy 非 0 表示有读者占着旧快照，没能全部合并；记日志，下次启动或关闭时会再合并。
// 主库空闲页之外的历史备份不在迁移能触及的范围内。
func (s *Store) checkpointAfterMigration(ctx context.Context) {
	var busy, logFrames, checkpointed int
	err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed)
	switch {
	case err != nil:
		slog.Warn("凭据迁移后 WAL checkpoint 失败，旧明文可能暂留在 WAL 或主库文件中", "err", err)
	case busy != 0:
		slog.Warn("凭据迁移后 WAL checkpoint 未完成，旧明文可能暂留在 WAL 或主库文件中",
			"logFrames", logFrames, "checkpointed", checkpointed)
	}
}
