package i18n

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

// 格式动词：%s %d %q %v %g，以及带显式下标的 %[2]s。%% 不算。
var verbRe = regexp.MustCompile(`%(?:\[(\d+)\])?[-+# 0]*\d*(?:\.\d+)?[sdqvgf]`)

func verbCount(s string) int {
	s = strings.ReplaceAll(s, "%%", "")
	n, explicitMax := 0, 0
	for _, m := range verbRe.FindAllStringSubmatch(s, -1) {
		if m[1] != "" {
			var i int
			for _, c := range m[1] {
				i = i*10 + int(c-'0')
			}
			if i > explicitMax {
				explicitMax = i
			}
			continue
		}
		n++
	}
	if explicitMax > n {
		return explicitMax
	}
	return n
}

// 每条文案中英文都得有，参数个数得对得上——漏翻或少一个 %s，
// 界面上就会冒出 %!s(MISSING) 这种东西。
func TestCatalogComplete(t *testing.T) {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	if len(catalog) == 0 {
		t.Fatal("catalog is empty")
	}
	for k, v := range catalog {
		if strings.TrimSpace(v[0]) == "" || strings.TrimSpace(v[1]) == "" {
			t.Errorf("%s: missing translation (zh=%q en=%q)", k, v[0], v[1])
			continue
		}
		if zc, ec := verbCount(v[0]), verbCount(v[1]); zc != ec {
			t.Errorf("%s: zh has %d args, en has %d", k, zc, ec)
		}
		// 英文里混进中文，多半是复制时忘了改。
		if regexp.MustCompile(`[\p{Han}]`).MatchString(v[1]) {
			t.Errorf("%s: English text contains Chinese: %q", k, v[1])
		}
	}
}

func TestParse(t *testing.T) {
	cases := map[string]Lang{
		"": Default, "en": EN, "en-US,en;q=0.9": EN, "zh-CN": ZH, "zh-TW,zh;q=0.8": ZH,
		"fr-FR,en;q=0.5": EN, "fr": Default,
	}
	for in, want := range cases {
		if got := Parse(in); got != want {
			t.Errorf("Parse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	register(map[string][2]string{
		"test.hello": {"你好 %s，共 %d 台", "Hello %s, %d total"},
		"test.wrap":  {"外层（%s）", "outer (%s)"},
	})
	m := M("test.hello", "web-01", 3)
	if !Contains(m) {
		t.Fatal("M should produce an envelope")
	}
	if got := Render(EN, m); got != "Hello web-01, 3 total" {
		t.Errorf("en = %q", got)
	}
	if got := Render(ZH, m); got != "你好 web-01，共 3 台" {
		t.Errorf("zh = %q", got)
	}
	// 信封可以嵌在更长的文字里、也可以作参数嵌套。
	nested := "prefix " + M("test.wrap", m) + " suffix"
	if got := Render(EN, nested); got != "prefix outer (Hello web-01, 3 total) suffix" {
		t.Errorf("nested = %q", got)
	}
	// 未登记的键露出键名，不吞成空白。
	if got := Render(EN, M("test.nope")); got != "test.nope" {
		t.Errorf("unknown = %q", got)
	}
}

func TestErrors(t *testing.T) {
	register(map[string][2]string{"test.err": {"读取 %s 失败", "couldn't read %s"}})
	base := errors.New("timeout")
	err := Wrap(base, "test.err", "vnic")
	if !errors.Is(err, base) {
		t.Error("Wrap must keep the chain")
	}
	if got := Message(EN, err); got != "couldn't read vnic: timeout" {
		t.Errorf("Message = %q", got)
	}
	sentinel := Err("test.err", "x")
	if !errors.Is(Wrap(sentinel, "test.err", "y"), sentinel) {
		t.Error("sentinel lost")
	}
}

func TestRenderJSON(t *testing.T) {
	register(map[string][2]string{"test.json": {"第 %d 步", "step %d"}})
	in := []byte(`{"title":"` + strings.ReplaceAll(M("test.json", 2), "\x1e", `\u001e`) + `","n":12345678901,"ok":true}`)
	in = []byte(strings.ReplaceAll(string(in), "\x1f", `\u001f`))
	out := string(RenderJSON(EN, in))
	if !strings.Contains(out, `"title":"step 2"`) || !strings.Contains(out, `"n":12345678901`) {
		t.Errorf("RenderJSON = %s", out)
	}
	plain := []byte(`{"a":"b"}`)
	if string(RenderJSON(EN, plain)) != string(plain) {
		t.Error("plain JSON should pass through untouched")
	}
}
