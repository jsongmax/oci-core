// Package i18n 是服务端文案的多语言支持。
//
// 服务端生成的文字（错误信息、任务步骤、审计详情、通知）不在生成时就决定语言，
// 而是写成一个「消息信封」：消息键加参数，编码后夹在两个控制字符之间。
// 信封可以原样存进数据库、嵌进 error 的字符串、拼进更长的文字里，
// 到了输出端（HTTP 响应、SSE、CSV、通知）再按读者的语言渲染。
//
// 这样做的好处：
//   - 业务代码不必一路传语言参数，后台任务、定时器里生成的文字也能按读者语言显示；
//   - 存下来的任务记录、审计详情在用户切换语言后跟着变，不会一半中文一半英文；
//   - error 照常用 errors.Is / %w，语言只在最后一步决定。
//
// 文案登记在 catalog_*.go 里，中英文写在同一行，漏写一边测试会失败。
package i18n

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Lang 是界面语言。
type Lang string

const (
	ZH Lang = "zh-CN"
	EN Lang = "en"
	// Default 是没有任何语言信息时用的语言：保持升级前的行为，老用户不受影响。
	Default = ZH
)

// Supported 列出支持的语言，顺序即设置页里的顺序。
var Supported = []Lang{ZH, EN}

// Parse 把 "en"、"en-US,en;q=0.9"、"zh-CN" 这类写法归到支持的语言上。
// 取第一个能认出来的语言；都认不出来时返回 Default。
func Parse(s string) Lang {
	for _, part := range strings.Split(s, ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		switch {
		case tag == "":
			continue
		case strings.HasPrefix(tag, "zh"):
			return ZH
		case strings.HasPrefix(tag, "en"):
			return EN
		}
	}
	return Default
}

// Valid 报告 s 是否正好是一个支持的语言代码。设置项入库前用它校验。
func Valid(s string) bool {
	for _, l := range Supported {
		if string(l) == s {
			return true
		}
	}
	return false
}

// ---- 文案登记 ----

var (
	catalogMu sync.RWMutex
	catalog   = map[string][2]string{}
)

// register 登记一组文案，值是 {中文, 英文}。重复的键说明两处登记撞了，直接 panic。
func register(m map[string][2]string) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	for k, v := range m {
		if _, dup := catalog[k]; dup {
			panic("i18n: duplicate key " + k)
		}
		catalog[k] = v
	}
}

// Has 报告某个键是否已登记，测试用。
func Has(key string) bool {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	_, ok := catalog[key]
	return ok
}

// T 按语言取文案并填入参数。参数里的信封和 error 会先按同一语言渲染。
// 未登记的键原样返回键名——宁可露出一个看得懂的键，也不要一片空白。
func T(lang Lang, key string, args ...any) string {
	catalogMu.RLock()
	e, ok := catalog[key]
	catalogMu.RUnlock()
	if !ok {
		return key
	}
	tmpl := e[0]
	if lang == EN && e[1] != "" {
		tmpl = e[1]
	}
	if len(args) == 0 {
		return tmpl
	}
	rendered := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case string:
			rendered[i] = Render(lang, v)
		case error:
			rendered[i] = Render(lang, v.Error())
		default:
			rendered[i] = v
		}
	}
	return fmt.Sprintf(tmpl, rendered...)
}

// ---- 信封 ----

const (
	open  = "\x1e"
	close = "\x1f"
)

type payload struct {
	K string `json:"k"`
	A []any  `json:"a,omitempty"`
}

// M 生成一个消息信封。参数只能是字符串、数字、布尔或 error（取其 Error()）；
// 其它类型按 %v 转成字符串存。
func M(key string, args ...any) string {
	p := payload{K: key}
	for _, a := range args {
		switch v := a.(type) {
		case nil:
			p.A = append(p.A, "")
		case string, bool,
			int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64:
			p.A = append(p.A, v)
		case error:
			p.A = append(p.A, v.Error())
		default:
			p.A = append(p.A, fmt.Sprint(v))
		}
	}
	b, _ := json.Marshal(p)
	return open + base64.RawURLEncoding.EncodeToString(b) + close
}

var envelopeRe = regexp.MustCompile(open + `([A-Za-z0-9_-]+)` + close)

// Render 把字符串里的所有信封按语言展开。不含信封的字符串原样返回。
func Render(lang Lang, s string) string {
	if !strings.Contains(s, open) {
		return s
	}
	return envelopeRe.ReplaceAllStringFunc(s, func(m string) string {
		raw, err := base64.RawURLEncoding.DecodeString(m[1 : len(m)-1])
		if err != nil {
			return ""
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var p payload
		if err := dec.Decode(&p); err != nil {
			return ""
		}
		args := make([]any, len(p.A))
		for i, a := range p.A {
			args[i] = normalizeNumber(a)
		}
		return T(lang, p.K, args...)
	})
}

// normalizeNumber 把 json.Number 还原成 int64 或 float64，%d / %g 才认得。
func normalizeNumber(a any) any {
	n, ok := a.(json.Number)
	if !ok {
		return a
	}
	if i, err := n.Int64(); err == nil {
		return i
	}
	if f, err := n.Float64(); err == nil {
		return f
	}
	return n.String()
}

// Contains 报告字符串里是否带信封。
func Contains(s string) bool { return strings.Contains(s, open) }

// ---- error ----

// Err 生成一个文案可翻译的 error。它的 Error() 是信封，渲染发生在输出端。
func Err(key string, args ...any) error { return errors.New(M(key, args...)) }

// Wrap 给 err 加一层可翻译的说明，形如「读取私网 IP 失败: <原错误>」。
// 原错误保留在链上，errors.Is / errors.As 照常可用。
func Wrap(err error, key string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", M(key, args...), err)
}

// Message 是 error 按语言渲染后的文字。
func Message(lang Lang, err error) string {
	if err == nil {
		return ""
	}
	return Render(lang, err.Error())
}

// ---- context ----

type ctxKey struct{}

// WithLang 把请求的语言放进 context，给少数需要在中途就定稿文字的地方用。
func WithLang(ctx context.Context, lang Lang) context.Context {
	return context.WithValue(ctx, ctxKey{}, lang)
}

// FromContext 取出 context 里的语言，没有时返回 Default。
func FromContext(ctx context.Context) Lang {
	if l, ok := ctx.Value(ctxKey{}).(Lang); ok {
		return l
	}
	return Default
}

// ---- JSON ----

// escapedOpen 是信封起始符在 JSON 里的样子。encoding/json 会把控制字符转义成 \u001e。
var escapedOpen = []byte(`\u001e`)

// RenderJSON 把一段 JSON 里所有字符串值中的信封按语言展开。
// 不含信封时原样返回，绝大多数响应走这条快路。
func RenderJSON(lang Lang, data []byte) []byte {
	if !bytes.Contains(data, escapedOpen) {
		return data
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return data
	}
	v = renderValue(lang, v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return data
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func renderValue(lang Lang, v any) any {
	switch x := v.(type) {
	case string:
		return Render(lang, x)
	case []any:
		for i := range x {
			x[i] = renderValue(lang, x[i])
		}
		return x
	case map[string]any:
		for k, val := range x {
			x[k] = renderValue(lang, val)
		}
		return x
	default:
		return v
	}
}
