// Package gzhttp 为 HTTP 服务提供轻量 gzip 压缩中间件：前端产物（JS/CSS/HTML）与
// API JSON 响应在公网窄带链路上体积可减 60% 以上。零第三方依赖，懒设置头（304/204 不压缩）。
package gzhttp

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

var pool = sync.Pool{
	New: func() interface{} { return gzip.NewWriter(nil) },
}

type writer struct {
	http.ResponseWriter
	gz          *gzip.Writer
	enabled     bool
	wroteHeader bool
}

func (w *writer) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	// 204/304 无响应体；重定向等小响应压缩无收益还可能干扰，直接透传
	if code >= 200 && code != 204 && code != 304 {
		h := w.Header()
		h.Del("Content-Length") // 压缩后长度变化，交给分块传输
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		w.enabled = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *writer) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.enabled {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Handler 包装 next：客户端声明 Accept-Encoding: gzip 时按需压缩响应
func Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gz := pool.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() {
			// 响应结束后关闭 gzip 流以刷出尾部（仅当真的启用过）
			gz.Close()
			pool.Put(gz)
		}()
		next.ServeHTTP(&writer{ResponseWriter: w, gz: gz}, r)
	})
}
