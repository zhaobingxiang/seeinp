// Package gzhttp 为 HTTP 服务提供轻量 gzip 压缩中间件：前端产物（JS/CSS/HTML）与
// API JSON 响应在公网窄带链路上体积可减 60% 以上。零第三方依赖，懒设置头（304/204 不压缩）。
// 二进制文件下载（application/octet-stream 等）自动跳过压缩，避免 ReadFrom 零拷贝绕过
// gzip 管道导致客户端收到与 Content-Encoding 不一致的原始数据。
package gzhttp

import (
	"compress/gzip"
	"io"
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

// isBinaryContentType 判断是否为不可压缩的二进制内容类型
func isBinaryContentType(ct string) bool {
	return strings.HasPrefix(ct, "application/octet-stream") ||
		strings.HasPrefix(ct, "video/") ||
		strings.HasPrefix(ct, "image/") ||
		strings.HasPrefix(ct, "audio/") ||
		strings.HasPrefix(ct, "application/zip") ||
		strings.HasPrefix(ct, "application/gzip")
}

func (w *writer) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	// 204/304 无响应体；重定向等小响应压缩无收益还可能干扰，直接透传
	if code >= 200 && code != 204 && code != 304 {
		ct := w.Header().Get("Content-Type")
		if isBinaryContentType(ct) {
			// 二进制文件下载：不压缩，保留 Content-Length（http.ServeContent 设置），
			// ReadFrom 零拷贝直接写 socket 即可
			w.ResponseWriter.WriteHeader(code)
			return
		}
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

// ReadFrom 拦截 io.ReaderFrom 接口：始终走 Write 路径，禁用 sendfile 零拷贝。
// 原因：sendfile 在某些 Linux 网络路径下远程客户端收不到数据体（本地回环正常），
// 强制走 Write 可靠传输。启用压缩时 Write 经 gzip；禁用时直接写底层 writer。
// 注意：不能在这里 io.Copy(w, r) —— w 自身实现了 ReaderFrom，io.Copy 会回调 w.ReadFrom
// 造成无限递归（栈溢出）。压缩时拷贝到 gz（非 ReaderFrom，安全）；未压缩时透传底层。
func (w *writer) ReadFrom(r io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.enabled {
		return io.Copy(w.gz, r)
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(w.ResponseWriter, r)
}

// Handler 包装 next：客户端声明 Accept-Encoding: gzip 时按需压缩响应
func Handler(next http.Handler) http.Handler {
	return HandlerWithSkip(next, nil)
}

// HandlerWithSkip 包装 next，skip 返回 true 时跳过 gzip 直接透传（用于文件下载等不可压缩响应）。
// skip 为 nil 时对所有请求启用压缩。
func HandlerWithSkip(next http.Handler, skip func(r *http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skip != nil && skip(r) || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gz := pool.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() {
			gz.Close()
			pool.Put(gz)
		}()
		next.ServeHTTP(&writer{ResponseWriter: w, gz: gz}, r)
	})
}
