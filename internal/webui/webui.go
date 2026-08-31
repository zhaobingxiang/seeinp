// Package webui 通过 go:embed 内嵌 PM/PS 两套 Vue 前端构建产物，使二进制版本包自包含：
// 一键升级二进制即同步更新前端，不再依赖磁盘上的 dist 目录（也杜绝命中旧构建的坑）。
// 构建时由 build/build.sh 先构建前端并拷贝到本包 files 目录；未构建时使用占位页（可编译）。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:pm/files
var pmFiles embed.FS

//go:embed all:ps/files
var psFiles embed.FS

// PM 返回 seeinpm 管理端前端（web-pm 构建产物）
func PM() fs.FS { return sub(pmFiles, "pm/files") }

// PS 返回 seeinps B 端前端（web-ps 构建产物）
func PS() fs.FS { return sub(psFiles, "ps/files") }

func sub(fsys embed.FS, dir string) fs.FS {
	s, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err) // 内嵌目录在编译期固定，不会发生
	}
	return s
}

// SPAHandler 从内嵌 FS 提供前端：assets（带内容哈希）长缓存直出；
// 其余路径（SPA 路由与 index.html 本身）no-cache 回退 index.html，保证升级后浏览器立即拉到新版
func SPAHandler(fsys fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(fsys))
	return func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := fsys.Open(p); err == nil {
			f.Close()
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				fileServer.ServeHTTP(w, r)
				return
			}
			// 非 assets（含 index.html 自身）：no-cache，防止浏览器启发式缓存旧入口
			w.Header().Set("Cache-Control", "no-cache")
			fileServer.ServeHTTP(w, r)
			return
		}
		data, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.Error(w, "frontend not built", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
	}
}
