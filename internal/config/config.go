// Package config 集中处理运行期配置。全部通过环境变量注入，不引入配置文件格式。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config 是服务的完整配置。
type Config struct {
	// Addr 是监听地址。默认只听回环——这个面板持有所有租户的控制权，
	// 直接暴露公网风险过高。需要外部访问时应由用户显式配置，并自行套 TLS。
	Addr string

	// StaticDir 指向一个前端产物目录。非空时优先于嵌入的资源，
	// 便于前端开发期用真实后端调试而不必每次重新编译 Go。
	StaticDir string

	// DataDir 存放 SQLite 数据库与主密钥文件。
	DataDir string

	// MasterKeyHex 从环境变量直接注入主密钥（十六进制）。
	// 留空则退回到 DataDir 下的密钥文件，文件不存在时自动生成。
	MasterKeyHex string

	// SessionTTL 是会话有效期，每次请求滑动续期。
	SessionTTL time.Duration

	// TrustProxyHeaders 决定是否采信 X-Forwarded-For / X-Forwarded-Proto。
	// 只有确实部署在反向代理之后才应开启，否则客户端可以伪造来源 IP 绕过登录限流。
	TrustProxyHeaders bool
}

// DBPath 返回数据库文件路径。
//
// 文件名保留改名前的 oci-tools.db：里面存着所有账号的加密私钥，
// 主密钥在同一个目录下。改文件名的收益只是好看，代价是任何一次
// 迁移出错都可能让人打不开自己的账号。
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "oci-tools.db") }

// MasterKeyPath 返回主密钥文件路径。
func (c Config) MasterKeyPath() string { return filepath.Join(c.DataDir, "master.key") }

// legacyPrefix 是最初那版（oci-tools）的环境变量前缀。
//
// 中间还叫过一阵 OCIPerch，但那个名字只活了一个提交、没有任何部署，
// 所以不为它保留兼容——多留一个前缀就多一份要一直维护的历史包袱。
//
// 继续识别它，是因为已经部署的实例大多把 OCI_TOOLS_MASTER_KEY 写进了
// systemd unit 或 docker-compose。改名不该让别人的服务在下次重启时
// 突然读不到主密钥——那等于所有已保存的 OCI 私钥都解不开了。
const legacyPrefix = "OCI_TOOLS_"

// lookupEnv 依次尝试新前缀与旧前缀，返回第一个非空值。
func lookupEnv(name string) string {
	if v := strings.TrimSpace(os.Getenv(envPrefix + name)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(legacyPrefix + name))
}

// envPrefix 是当前的环境变量前缀。
const envPrefix = "OCICORE_"

// Load 从环境变量读取配置并填充默认值。
func Load() (Config, error) {
	cfg := Config{
		// 端口与前端开发服务器的代理目标保持一致（见 web/vite.config.ts）。
		Addr:              orDefault(lookupEnv("ADDR"), "127.0.0.1:8080"),
		DataDir:           orDefault(lookupEnv("DATA_DIR"), "./data"),
		MasterKeyHex:      lookupEnv("MASTER_KEY"),
		StaticDir:         lookupEnv("STATIC_DIR"),
		SessionTTL:        12 * time.Hour,
		TrustProxyHeaders: parseBool(lookupEnv("TRUST_PROXY")),
	}

	if v := lookupEnv("SESSION_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, fmt.Errorf("config: %sSESSION_TTL 格式无效 (%q): %w", envPrefix, v, err)
		}
		if d < time.Minute {
			return cfg, fmt.Errorf("config: %sSESSION_TTL 不能短于 1 分钟", envPrefix)
		}
		cfg.SessionTTL = d
	}

	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return cfg, fmt.Errorf("config: 解析数据目录失败: %w", err)
	}
	cfg.DataDir = abs

	return cfg, nil
}

// ListensPublicly 报告监听地址是否会接受来自外部网络的连接，
// 用于在启动日志里给出安全提示。
func (c Config) ListensPublicly() bool {
	host := c.Addr
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return false
	default:
		return true
	}
}

func parseBool(v string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	return err == nil && b
}

func orDefault(v, fallback string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return fallback
}
