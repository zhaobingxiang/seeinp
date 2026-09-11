package main

// 客户端升级模块：检查（官网 /api/updates/seeinpc/latest）→ 下载（SHA256 校验）
// → 静默覆盖安装（复用 Inno Setup 安装包，/SILENT 装完自动启动新版本）。
// 版本号为五段数字，逐段数值比较。

import (
	"context"
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

	"github.com/seeinp/seeinp/internal/version"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

// updateBaseURL 官网正式站（升级检查与安装包下载）
const updateBaseURL = "https://see.timemsee.cn"

// UpdateTarget 弹窗可选的升级目标：latest=最新包，floor=强制版本地板（高于当前版本的最低被强制版本）
type UpdateTarget struct {
	ID         string `json:"id"`
	Version    string `json:"version"`
	URL        string `json:"url"`
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	ReleasedAt string `json:"releasedAt"`
	Note       string `json:"note"`
}

// UpdateInfo 最新版本信息（来自官网接口）
type UpdateInfo struct {
	Available  bool   `json:"available"`
	Version    string `json:"version"`
	URL        string `json:"url"` // 完整下载地址
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	ReleasedAt string `json:"releasedAt"`
	Note       string `json:"note"`
	Forced     bool   `json:"forced"`

	MinRequired *UpdateTarget  `json:"minRequired,omitempty"` // 强制版本地板（官网按 current 计算）
	Targets     []UpdateTarget `json:"targets,omitempty"`    // 弹窗可选目标（1~2 个，latest 在前）
}

// PickTarget 按 id 选择升级目标；无 Targets 时回退为 latest 自身（兼容旧响应）。
func (i *UpdateInfo) PickTarget(id string) *UpdateTarget {
	if id == "" {
		id = "latest"
	}
	for _, t := range i.Targets {
		if t.ID == id {
			cp := t
			return &cp
		}
	}
	if id == "latest" && len(i.Targets) == 0 && i.Available {
		return &UpdateTarget{ID: "latest", Version: i.Version, URL: i.URL, Filename: i.Filename,
			Size: i.Size, SHA256: i.SHA256, ReleasedAt: i.ReleasedAt, Note: i.Note}
	}
	return nil
}

// asUpdateInfo 把选中目标包装为下载函数需要的 UpdateInfo 形态
func (i *UpdateInfo) asUpdateInfo(t *UpdateTarget) *UpdateInfo {
	return &UpdateInfo{Available: true, Version: t.Version, URL: t.URL, Filename: t.Filename,
		Size: t.Size, SHA256: t.SHA256, ReleasedAt: t.ReleasedAt, Note: t.Note, Forced: i.Forced}
}

// UpdateCheckResult 检查结果（前端升级弹窗数据源）
type UpdateCheckResult struct {
	UpdateInfo
	Current string `json:"current"`
	Ignored bool   `json:"ignored"` // 该版本已被用户跳过（手动检查时仍会提示）
	Message string `json:"message"`
}

// fetchUpdateInfo 拉取官网最新版本信息（带 current，官网据此返回强制版本地板 min_required）
func fetchUpdateInfo(ctx context.Context) (*UpdateInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		updateBaseURL+"/api/updates/seeinpc/latest?current="+url.QueryEscape(version.Version), nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接官网（%s）: %w", updateBaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("官网尚未发布任何 seeinpc 版本包")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("官网响应异常（HTTP %d）", resp.StatusCode)
	}
	type pkgDTO struct {
		Version     string `json:"version"`
		URL         string `json:"url"`
		Filename    string `json:"filename"`
		Size        int64  `json:"size"`
		SHA256      string `json:"sha256"`
		ReleasedAt  string `json:"released_at"`
		ReleaseNote string `json:"release_note"`
	}
	var d struct {
		pkgDTO
		Forced      bool    `json:"forced"`
		MinRequired *pkgDTO `json:"min_required"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return nil, fmt.Errorf("解析官网响应失败: %w", err)
	}
	toTarget := func(id string, p *pkgDTO) *UpdateTarget {
		return &UpdateTarget{ID: id, Version: p.Version, URL: updateBaseURL + p.URL, Filename: p.Filename,
			Size: p.Size, SHA256: strings.ToLower(p.SHA256), ReleasedAt: p.ReleasedAt, Note: p.ReleaseNote}
	}
	info := &UpdateInfo{
		Version:    d.Version,
		URL:        updateBaseURL + d.URL,
		Filename:   d.Filename,
		Size:       d.Size,
		SHA256:     strings.ToLower(d.SHA256),
		ReleasedAt: d.ReleasedAt,
		Note:       d.ReleaseNote,
	}
	cur := version.Version
	latestNewer := compareVersions(d.Version, cur) > 0
	info.Available = latestNewer
	info.Forced = d.Forced
	if d.MinRequired != nil && compareVersions(d.MinRequired.Version, cur) > 0 {
		// 当前版本低于强制地板：必须升级（地板语义覆盖在 latest.forced 之上）
		info.MinRequired = toTarget("floor", d.MinRequired)
		info.Available = true
		info.Forced = true
	}
	if info.Available {
		// 目标列表：latest 须比当前新且不低于地板；floor 与 latest 版本不同才作为第二选项
		hasLatest := latestNewer
		if info.MinRequired != nil && latestNewer && compareVersions(d.Version, info.MinRequired.Version) < 0 {
			hasLatest = false
		}
		if hasLatest {
			info.Targets = append(info.Targets, *toTarget("latest", &d.pkgDTO))
		}
		if info.MinRequired != nil && (!hasLatest || info.MinRequired.Version != d.Version) {
			info.Targets = append(info.Targets, *info.MinRequired)
		}
		if len(info.Targets) == 0 {
			info.Available = false // 理论上不可达的兜底
		}
	}
	return info, nil
}

// compareVersions 五段数字版本号比较：a>b 返回 1，a<b 返回 -1，相等返回 0
func compareVersions(a, b string) int {
	pa := strings.Split(strings.SplitN(a, "-", 2)[0], ".")
	pb := strings.Split(strings.SplitN(b, "-", 2)[0], ".")
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		xa, xb := 0, 0
		if i < len(pa) {
			xa, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			xb, _ = strconv.Atoi(pb[i])
		}
		if xa != xb {
			if xa > xb {
				return 1
			}
			return -1
		}
	}
	return 0
}

// downloadUpdatePath 升级包下载目录（%TEMP%\seeinpc-update）
func downloadUpdatePath(filename string) string {
	return filepath.Join(os.TempDir(), "seeinpc-update", filename)
}

// downloadUpdate 下载安装包并校验 SHA256，进度经 "update-progress" 事件推送。
// 返回本地文件路径；已存在且哈希匹配的包直接复用（断点重试场景）。
func (a *App) downloadUpdate(ctx context.Context, info *UpdateInfo) (string, error) {
	if info.URL == "" {
		return "", fmt.Errorf("下载地址为空")
	}
	dest := downloadUpdatePath(info.Filename)
	if h, err := fileSHA256(dest); err == nil && info.SHA256 != "" && h == info.SHA256 {
		return dest, nil // 已下载过且校验通过
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	tmp := dest + ".part"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.URL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败（HTTP %d）", resp.StatusCode)
	}

	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	total := info.Size
	if resp.ContentLength > 0 {
		total = resp.ContentLength
	}
	hasher := sha256.New()
	buf := make([]byte, 64*1024)
	var downloaded int64
	lastEmit := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return "", werr
			}
			hasher.Write(buf[:n])
			downloaded += int64(n)
			// 进度推送：限频 200ms，避免刷爆前端
			if time.Since(lastEmit) >= 200*time.Millisecond || downloaded == total {
				lastEmit = time.Now()
				wruntime.EventsEmit(a.ctx, "update-progress", map[string]any{
					"downloaded": downloaded, "total": total,
				})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return "", fmt.Errorf("下载中断: %w", rerr)
		}
		if ctx.Err() != nil {
			out.Close()
			return "", ctx.Err()
		}
	}
	out.Close()

	// SHA256 校验
	got := hex.EncodeToString(hasher.Sum(nil))
	if info.SHA256 != "" && got != info.SHA256 {
		os.Remove(tmp)
		return "", fmt.Errorf("安装包校验失败（SHA256 不匹配），已删除重试即可")
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	a.logf("[UPDATE] downloaded file=%s size=%d sha256=ok", info.Filename, downloaded)
	return dest, nil
}

// applyUpdateSilently 以 /SILENT 启动安装包（当前进程已提权，子进程继承，
// 不再弹 UAC）；安装器会自动结束本进程、覆盖安装并启动新版本。
func applyUpdateSilently(installerPath string) error {
	exe, err := windows.UTF16PtrFromString(installerPath)
	if err != nil {
		return err
	}
	args, _ := windows.UTF16PtrFromString("/SILENT /NORESTART")
	return windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), exe, args, nil, windows.SW_SHOWNORMAL)
}
