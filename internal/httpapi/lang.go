package httpapi

import (
	"net/http"

	"ocicore/internal/i18n"
)

// langWriter 把这次请求的语言随 ResponseWriter 带下去。
//
// writeJSON 等输出函数只拿得到 w，拿不到 r；改所有调用点去多传一个参数太伤，
// 而语言恰好只在「写出去」那一刻才需要。
type langWriter struct {
	http.ResponseWriter
	lang i18n.Lang
}

// Unwrap 让 http.NewResponseController 能找到底层连接（SSE 要解除写超时）。
func (w *langWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush 透传给底层：SSE 靠类型断言 http.Flusher 判断能不能流式输出。
func (w *langWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// requestLang 取请求的语言：查询参数 lang 优先（EventSource 带不了请求头），
// 其次 Accept-Language，都没有时用默认语言——老客户端的行为不变。
func requestLang(r *http.Request) i18n.Lang {
	if q := r.URL.Query().Get("lang"); q != "" {
		return i18n.Parse(q)
	}
	if h := r.Header.Get("Accept-Language"); h != "" {
		return i18n.Parse(h)
	}
	return i18n.Default
}

// withLang 给每个请求挂上语言：ResponseWriter 上一份，context 里一份。
func withLang(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := requestLang(r)
		next.ServeHTTP(&langWriter{ResponseWriter: w, lang: lang}, r.WithContext(i18n.WithLang(r.Context(), lang)))
	})
}

// langOf 找出 w 上挂的语言。中间件可能又包了一层，顺着 Unwrap 往下找。
func langOf(w http.ResponseWriter) i18n.Lang {
	for w != nil {
		if lw, ok := w.(*langWriter); ok {
			return lw.lang
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		w = u.Unwrap()
	}
	return i18n.Default
}
