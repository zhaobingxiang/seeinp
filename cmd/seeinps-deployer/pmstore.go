package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// pmClient 与 seeinpm 的免登录发布/校验接口交互。
// seeinpm 服务器统一 see.timemsee.cn:90（HTTP/API 端口），可在"服务器参数配置"中修改。
type pmClient struct {
	BaseURL  string // 形如 http://see.timemsee.cn:90
	Username string
	AuthCode string
	HTTP     *http.Client
}

func newPMClient(baseURL, username, authCode string) *pmClient {
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &pmClient{
		BaseURL:  baseURL,
		Username: strings.TrimSpace(username),
		AuthCode: strings.TrimSpace(authCode),
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// validate 用 seeinpm 用户名+授权码验证身份（复用手工可控的 verify-code 接口）。
// 通过后返回 seeinpm 侧 serverAddr（控制地址），失败返回错误。
func (p *pmClient) validate() (serverAddr string, err error) {
	body := strings.NewReader(fmt.Sprintf(`{"username":%q,"authCode":%q}`, p.Username, p.AuthCode))
	req, err := http.NewRequest("POST", p.BaseURL+"/api/v1/auth/verify-code", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("无法连接 seeinpm 服务器: %v", err)
	}
	defer resp.Body.Close()
	var r struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			ServerAddr string `json:"serverAddr"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("seeinpm 响应解析失败: %v", err)
	}
	if r.Code != 0 {
		return "", fmt.Errorf("授权校验失败(%d): %s", r.Code, r.Message)
	}
	return r.Data.ServerAddr, nil
}

// release 版本元信息（与 seeinpm /ps-release/versions 返回字段对齐）
type release struct {
	Version   string `json:"version"`
	Note      string `json:"note"`
	FileSize  int64  `json:"fileSize"`
	Sha256    string `json:"sha256"`
	CreatedAt int64  `json:"createdAt"`
}

// listReleases 获取某平台（goos/goarch）可用的 seeinps 版本列表
func (p *pmClient) listReleases(goos, goarch string) ([]release, error) {
	u := p.BaseURL + "/api/v1/ps-release/versions?" + url.Values{
		"username": {p.Username},
		"authCode": {p.AuthCode},
		"goos":     {goos},
		"goarch":   {goarch},
	}.Encode()
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求版本列表失败: %v", err)
	}
	defer resp.Body.Close()
	var r struct {
		Code    int       `json:"code"`
		Message string    `json:"message"`
		Data    []release `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("版本响应解析失败: %v", err)
	}
	if r.Code != 0 {
		return nil, fmt.Errorf("获取版本失败(%d): %s", r.Code, r.Message)
	}
	return r.Data, nil
}

// download 下载指定版本安装包到 destPath（version 为空取最新），
// 下载后校验 sha256；返回实际版本号与包文件名（取自 Content-Disposition）。
// progressFunc 下载进度回调：received/total 为已接收与总字节数（total<=0 表示未知）
type progressFunc func(received, total int64)

// progressReader 包装响应体，按最小间隔节流上报下载进度
type progressReader struct {
	r          io.Reader
	total      int64
	received   int64
	lastTick   time.Time
	onProgress progressFunc
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	pr.received += int64(n)
	if pr.onProgress != nil && time.Since(pr.lastTick) >= 200*time.Millisecond {
		pr.lastTick = time.Now()
		pr.onProgress(pr.received, pr.total)
	}
	return n, err
}

// download 下载指定版本安装包到 destPath（version 为空取最新），
// 下载后校验 sha256；返回实际版本号与包文件名（取自 Content-Disposition）。
// 使用 Range 分片下载（64KB/片）：某些网络路径对大 HTTP 响应有限制（TCP 窗口/中间设备），
// 分片可以绕过该限制。每片通过独立 HTTP Range 请求获取。
// fallbackTotal 为版本元数据中的包大小（用于进度显示兜底）。
// onProgress 非空时按约 200ms 间隔上报进度，结束时会回调一次 100%。
func (p *pmClient) download(version, goos, goarch, destPath string, fallbackTotal int64, onProgress progressFunc) (fileName, actualVersion string, err error) {
	q := url.Values{
		"username": {p.Username},
		"authCode": {p.AuthCode},
		"goos":     {goos},
		"goarch":   {goarch},
	}
	if version != "" {
		q.Set("version", version)
	}
	fullURL := p.BaseURL + "/api/v1/ps-release/download?" + q.Encode()

	// HEAD 请求获取元数据
	headReq, err := http.NewRequest("HEAD", fullURL, nil)
	if err != nil {
		return "", "", err
	}
	headResp, err := p.HTTP.Do(headReq)
	if err != nil {
		return "", "", fmt.Errorf("HEAD 请求失败: %v", err)
	}
	headResp.Body.Close()
	if headResp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HEAD 请求失败(%d)", headResp.StatusCode)
	}
	expectedSha := headResp.Header.Get("X-Seeinps-Sha256")
	if v := headResp.Header.Get("X-Seeinps-Version"); v != "" {
		actualVersion = v
	}
	fileName = basenameFromDisposition(headResp.Header.Get("Content-Disposition"))
	totalSize := headResp.ContentLength
	if totalSize <= 0 {
		totalSize = fallbackTotal
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return "", "", err
	}
	tmp := destPath + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", "", err
	}
	h := sha256.New()

	// 分片下载：8KB/片，绕过某些网络路径对大 HTTP 响应的限制（实测 >10KB 的 Range 响应会被阻断）。
	// seeinpm 侧已改用 io.Copy 直接传输 + 手动 Range 支持，不经过 http.ServeContent/gzip 管道。
	const chunkSize int64 = 8 * 1024
	var downloaded int64
	for downloaded < totalSize || totalSize <= 0 {
		end := downloaded + chunkSize - 1
		if totalSize > 0 && end >= totalSize {
			end = totalSize - 1
		}
		rangeHdr := fmt.Sprintf("bytes=%d-%d", downloaded, end)
		req, err := http.NewRequest("GET", fullURL, nil)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return "", "", err
		}
		req.Header.Set("Range", rangeHdr)
		resp, err := p.HTTP.Do(req)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return "", "", fmt.Errorf("Range 请求失败(%s): %v", rangeHdr, err)
		}
		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			f.Close()
			os.Remove(tmp)
			return "", "", fmt.Errorf("Range 请求失败(%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		n, cErr := io.Copy(h, io.TeeReader(resp.Body, f))
		resp.Body.Close()
		if cErr != nil {
			f.Close()
			os.Remove(tmp)
			return "", "", fmt.Errorf("Range 写入失败: %v", cErr)
		}
		downloaded += n
		if onProgress != nil && totalSize > 0 {
			onProgress(downloaded, totalSize)
		}
		if n < chunkSize {
			break // 最后一片或服务端不支持完整 Range
		}
	}
	f.Close()
	if onProgress != nil && totalSize > 0 {
		onProgress(totalSize, totalSize)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if expectedSha != "" && !strings.EqualFold(sum, expectedSha) {
		os.Remove(tmp)
		return "", "", fmt.Errorf("校验和校验失败（sha256 不一致）")
	}
	if err := os.Rename(tmp, destPath); err != nil {
		os.Remove(tmp)
		return "", "", err
	}
	if actualVersion == "" {
		actualVersion = version
	}
	return fileName, actualVersion, nil
}

// basenameFromDisposition 从 Content-Disposition 头提取文件名（形如 attachment; filename=xxx）
func basenameFromDisposition(disp string) string {
	if disp == "" {
		return ""
	}
	for _, part := range strings.Split(disp, ";") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "filename="):
			name := strings.Trim(strings.TrimPrefix(part, "filename="), `"`)
			return filepath.Base(name)
		case strings.HasPrefix(part, "filename*="):
			// RFC 5987: filename*=UTF-8''<urlencoded>
			raw := strings.TrimPrefix(part, "filename*=")
			if idx := strings.Index(raw, "''"); idx >= 0 {
				raw = raw[idx+2:]
			}
			if dec, err := url.QueryUnescape(strings.Trim(raw, `"`)); err == nil {
				return filepath.Base(dec)
			}
		}
	}
	return ""
}

// seeinpm 端口约定：90 = B端 Web/API（部署工具拉取版本包），99 = 信令/控制通道（seeinps 拨入）
const (
	defaultPMPort        = 90
	defaultPMControlPort = 99
)

// serverConfig seeinpm 服务器参数（默认 see.timemsee.cn:90）
type serverConfig struct {
	ServerAddr  string `json:"serverAddr"`  // 形如 see.timemsee.cn:90
	Port        int    `json:"port"`        // 展示用，可修改但界面不强调
	ControlPort int    `json:"controlPort"` // 控制端口（默认 99），通常不修改
}

func defaultServerConfig() serverConfig {
	return serverConfig{ServerAddr: "see.timemsee.cn", Port: defaultPMPort, ControlPort: defaultPMControlPort}
}

// parseAddr 把 "host:port" 拆开，port 缺省用 90
func parseServerAddr(in string) (host string, port int) {
	host = strings.TrimSpace(in)
	if h, p, err := netSplitHostPort(host); err == nil {
		if n, perr := strconv.Atoi(p); perr == nil {
			return h, n
		}
		return h, defaultPMPort
	}
	return host, defaultPMPort
}

// 组合 host+port 为 HTTP base（补 http:// 与端口）
func buildPMBase(host string, port int) string {
	return fmt.Sprintf("http://%s:%d", host, port)
}
