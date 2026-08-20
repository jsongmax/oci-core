package ociclient

import (
	"bufio"
	"strings"
)

// utf8BOM 是 UTF-8 字节序标记。用户从网页或记事本复制配置时经常把它一起带上，
// 而它会让第一行的 key 匹配失败——这是"明明粘贴对了却解析不出来"的常见成因。
const utf8BOM = "\ufeff"

// ConfigProfile 是从 Oracle 控制台下载的 API 密钥配置文件里解析出的一段 profile。
//
// 用户在控制台"添加 API 密钥"后会拿到一段这样的文本：
//
//	[DEFAULT]
//	user=ocid1.user.oc1..aaaa…
//	fingerprint=20:3b:97:13:…
//	tenancy=ocid1.tenancy.oc1..aaaa…
//	region=ap-tokyo-1
//	key_file=<path to your private keyfile>
//
// 让用户整段粘贴、由前端自动拆字段，能消除绝大多数手抄导致的配置错误。
type ConfigProfile struct {
	Name        string // profile 名，通常是 DEFAULT
	User        string
	Fingerprint string
	Tenancy     string
	Region      string
	KeyFile     string
	// HasPassPhrase 表示配置里声明了私钥口令。本工具不支持带口令的私钥，
	// 解析出来是为了给用户一条明确的提示而非静默忽略。
	HasPassPhrase bool
}

// Complete 报告解析结果是否已包含建立连接所需的全部字段（私钥另行粘贴）。
func (p ConfigProfile) Complete() bool {
	return p.User != "" && p.Fingerprint != "" && p.Tenancy != "" && p.Region != ""
}

// MissingFields 返回仍然缺失的字段名，用于在表单上高亮提示。
func (p ConfigProfile) MissingFields() []string {
	var missing []string
	for _, f := range []struct {
		name  string
		value string
	}{
		{"user", p.User},
		{"fingerprint", p.Fingerprint},
		{"tenancy", p.Tenancy},
		{"region", p.Region},
	} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	return missing
}

// ParseConfigFile 解析 OCI 配置文件文本，返回其中的所有 profile。
//
// 容错要宽松：用户可能只粘贴了几行、可能带 BOM、可能用了全角空格，
// 也可能连 [DEFAULT] 头都没复制。这些都应当解析成功而不是报错。
func ParseConfigFile(text string) []ConfigProfile {
	var profiles []ConfigProfile
	current := ConfigProfile{Name: "DEFAULT"}
	started := false

	flush := func() {
		// 只要抓到任意一个字段就算有效，避免因为用户少粘一行就整段丢弃。
		if current.User != "" || current.Tenancy != "" || current.Fingerprint != "" || current.Region != "" {
			profiles = append(profiles, current)
		}
	}

	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		// 去掉可能存在的 UTF-8 BOM：用户从网页或记事本复制时常常带上它。
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), utf8BOM))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if started {
				flush()
			}
			current = ConfigProfile{Name: strings.TrimSpace(line[1 : len(line)-1])}
			started = true
			continue
		}

		key, value, ok := splitKeyValue(line)
		if !ok {
			continue
		}
		started = true

		switch strings.ToLower(key) {
		case "user":
			current.User = value
		case "fingerprint":
			current.Fingerprint = strings.ToLower(value)
		case "tenancy":
			current.Tenancy = value
		case "region":
			current.Region = NormalizeRegion(value)
		case "key_file":
			current.KeyFile = value
		case "pass_phrase", "passphrase":
			current.HasPassPhrase = value != ""
		}
	}

	if started {
		flush()
	}
	return profiles
}

// splitKeyValue 按第一个 = 或 : 拆分一行。OCI 官方用 =，但用户手写时常写成 :。
func splitKeyValue(line string) (key, value string, ok bool) {
	idx := strings.IndexAny(line, "=:")
	if idx <= 0 {
		return "", "", false
	}
	// OCID 里含有冒号，必须只在第一个分隔符处拆分——上面的 IndexAny 已保证这点。
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}
