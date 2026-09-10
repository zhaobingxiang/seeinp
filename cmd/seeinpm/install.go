package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/seeinp/seeinp/internal/auth"
	"github.com/seeinp/seeinp/internal/logx"
	"github.com/seeinp/seeinp/internal/store"
)

// seeinps 一键部署所需的免登录能力（供 seeinps-deployer 使用）：
//   - GET /api/v1/ps-release/versions  用 seeinpm 用户名+授权码校验后，返回某平台可用的 seeinps 版本列表
//   - GET /api/v1/ps-release/download  校验通过后按版本/平台流式返回 seeinps 安装包
//
// 与 handleVerifyCode 共享同一套账号授权码校验逻辑，且不复用管理员 JWT——
// 部署工具只持有普通 seeinpm 用户（username+authCode），并未持有管理员凭证。

// verifyUserAuth 校验 seeinpm 用户名+授权码是否有效且启用。
// 与 handleVerifyCode 的逻辑保持一致，供部署下载/列表接口复用。
func (s *Server) verifyUserAuth(username, authCode string) (*store.User, error) {
	if username == "" || authCode == "" {
		return nil, errors.New("missing username or authCode")
	}
	user, err := s.store.GetUserByUsername(username)
	if err != nil {
		return nil, errors.New("user not found")
	}
	if user.Status != 1 {
		return nil, errors.New("user disabled")
	}
	expectedHash := auth.HashAuthCode(authCode, user.AuthCodeSalt)
	if expectedHash != user.AuthCodeHash {
		return nil, errors.New("invalid auth code")
	}
	return user, nil
}

// 免登录接口返回错误：统一 {code,message}，1001 为账号校验失败类
func writeInstallErr(w http.ResponseWriter, httpStatus int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	b, _ := json.Marshal(map[string]interface{}{"code": 1001, "message": message})
	w.Write(b)
}

// handlePSReleaseVersions GET /api/v1/ps-release/versions?username=&authCode=&goos=&goarch=
// 返回该平台可用的 seeinps 版本（新版本在前，仅元信息不含文件体）。
func (s *Server) handlePSReleaseVersions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	username := q.Get("username")
	authCode := q.Get("authCode")
	goos := q.Get("goos")
	goarch := q.Get("goarch")
	if !knownGOOS[goos] || !knownGOARCH[goarch] {
		writeInstallErr(w, http.StatusBadRequest, "平台参数非法（goos/amd64 等）")
		return
	}
	if _, err := s.verifyUserAuth(username, authCode); err != nil {
		logx.Warnf("[RELEASE] versions query rejected user=%s ip=%s err=%v", username, clientIP(r), err)
		writeInstallErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	list, err := s.store.ListVersions("seeinps")
	if err != nil {
		logx.Errorf("[RELEASE] versions query failed user=%s err=%v", username, err)
		writeInstallErr(w, http.StatusInternalServerError, "查询版本失败")
		return
	}
	type item struct {
		Version   string `json:"version"`
		Note      string `json:"note"`
		FileSize  int64  `json:"fileSize"`
		Sha256    string `json:"sha256"`
		CreatedAt int64  `json:"createdAt"`
	}
	items := make([]item, 0, len(list))
	for _, v := range list {
		if v.GoOS != goos || v.GoArch != goarch {
			continue
		}
		items = append(items, item{Version: v.Version, Note: v.Note, FileSize: v.FileSize, Sha256: v.Sha256, CreatedAt: v.CreatedAt})
	}
	writeOK(w, items)
}

// handlePSReleaseDownload GET /api/v1/ps-release/download?username=&authCode=&version=&goos=&goarch=
// version 为空时取该平台最新版本。校验通过后流式返回安装包，HTTP 头携带 sha256 供部署工具校验完整性。
func (s *Server) handlePSReleaseDownload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	username := q.Get("username")
	authCode := q.Get("authCode")
	version := q.Get("version")
	goos := q.Get("goos")
	goarch := q.Get("goarch")
	if !knownGOOS[goos] || !knownGOARCH[goarch] {
		writeInstallErr(w, http.StatusBadRequest, "平台参数非法（goos/amd64 等）")
		return
	}
	if _, err := s.verifyUserAuth(username, authCode); err != nil {
		logx.Warnf("[RELEASE] download rejected user=%s ip=%s err=%v", username, clientIP(r), err)
		writeInstallErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var v *store.Version
	var err error
	if version == "" {
		v, err = s.store.GetLatestVersion("seeinps", goos, goarch)
	} else {
		v, err = s.store.GetVersionByName("seeinps", version, goos, goarch)
	}
	if err != nil || v == nil {
		logx.Warnf("[RELEASE] download: package not found user=%s version=%q platform=%s/%s",
			username, version, goos, goarch)
		writeInstallErr(w, http.StatusNotFound, "未找到对应平台的 seeinps 安装包")
		return
	}
	f, err := os.Open(filepath.Join(releaseDir, v.Endpoint, v.Version, v.GoOS+"-"+v.GoArch, v.FileName))
	if err != nil {
		logx.Errorf("[RELEASE] download: package file missing version=%s file=%s err=%v", v.Version, v.FileName, err)
		writeInstallErr(w, http.StatusNotFound, "安装包文件缺失")
		return
	}
	defer f.Close()
	// 免登录接口，此前下载安装包不留任何痕迹：谁在何时取走了哪个版本无法追溯
	logx.Infof("[RELEASE] downloading seeinps package user=%s version=%s platform=%s/%s size=%d ip=%s",
		username, v.Version, v.GoOS, v.GoArch, v.FileSize, clientIP(r))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filepath.Base(v.FileName)))
	w.Header().Set("X-Seeinps-Version", v.Version)
	w.Header().Set("X-Seeinps-Sha256", v.Sha256)
	w.Header().Set("X-Seeinps-Size", fmt.Sprintf("%d", v.FileSize))
	// http.ServeContent 处理 Range 请求和 Content-Length，但必须在无 gzip 的独立 mux 上运行
	// （gzhttp 的 ReadFrom/sendfile 绕过问题导致远程客户端收不到数据体）。
	http.ServeContent(w, r, filepath.Base(v.FileName), time.Unix(v.CreatedAt, 0), f)
}
