package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seeinp/seeinp/internal/protocol"
	"github.com/seeinp/seeinp/internal/store"
	versionpkg "github.com/seeinp/seeinp/internal/version"
)

// writeOK/writeErr 与 seeinps B 端响应格式保持一致：{"code":..,"message":..,"data":..}
func writeOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(map[string]interface{}{"code": 0, "message": "ok", "data": data})
	w.Write(b)
}

func writeErr(w http.ResponseWriter, httpStatus, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	b, _ := json.Marshal(map[string]interface{}{"code": code, "message": message})
	w.Write(b)
}

// releaseDir 版本包落盘根目录（相对 PM 工作目录）
const releaseDir = "data/releases"

// maxReleaseSize 升级包大小上限
const maxReleaseSize = 200 << 20

// versionPattern 五段数字版本号（1.0.26.0829.01：大版本.大版本.年.月日.当日序号）
var versionPattern = regexp.MustCompile(`^\d{1,4}(\.\d{1,4}){4}$`)

// 平台白名单：升级包必须标注已知 GOOS/GOARCH，且与目标节点完全一致才能推送
var knownGOOS = map[string]bool{"linux": true, "windows": true, "darwin": true}
var knownGOARCH = map[string]bool{"amd64": true, "arm64": true, "386": true, "arm": true}

// upgradeState 一次升级的进行时状态（内存态，PM 重启即清），供前端轮询进度
type upgradeState struct {
	mu         sync.Mutex
	version    string
	stage      string // notifying(通知节点) -> transferring(传输中) -> sent(已下发) -> verified(校验通过,重启中) / failed
	sentBytes  int64
	totalBytes int64
	errMsg     string
	startedAt  time.Time
	updatedAt  time.Time
}

// upgradeIdleExpiry 状态多久无更新视为死掉（传输中每个写入块都会刷新）
const upgradeIdleExpiry = 5 * time.Minute

var (
	upgradeStatesMu sync.Mutex
	upgradeStates   = map[string]*upgradeState{}
)

func beginUpgrade(username, version string, total int64) {
	upgradeStatesMu.Lock()
	defer upgradeStatesMu.Unlock()
	now := time.Now()
	upgradeStates[username] = &upgradeState{version: version, stage: "notifying", totalBytes: total, startedAt: now, updatedAt: now}
}

func upgradeStage(username, stage, errMsg string) {
	upgradeStatesMu.Lock()
	defer upgradeStatesMu.Unlock()
	if st, ok := upgradeStates[username]; ok {
		st.mu.Lock()
		st.stage, st.errMsg, st.updatedAt = stage, errMsg, time.Now()
		st.mu.Unlock()
	}
}

func upgradeProgress(username string, sent int64) {
	upgradeStatesMu.Lock()
	st, ok := upgradeStates[username]
	upgradeStatesMu.Unlock()
	if !ok {
		return
	}
	st.mu.Lock()
	st.sentBytes, st.updatedAt = sent, time.Now()
	st.mu.Unlock()
}

// completeUpgradeIfMatches 节点重连上报版本后调用：存在升级记录且版本一致，
// 则推进为 verified——校验回报可能在节点 exec 重启瞬间丢失（写完即重启），不能依赖它判定成功
func completeUpgradeIfMatches(username, version string) {
	upgradeStatesMu.Lock()
	defer upgradeStatesMu.Unlock()
	st, ok := upgradeStates[username]
	if !ok {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	// sent：正常完成路径；failed/transferring：流写入报 session shutdown 但节点实际
	// 已收完包并重启成功（以重连 HELLO 版本为准），同样判定升级成功
	if st.version == version && st.stage != "verified" {
		st.stage = "verified"
		st.errMsg = ""
		st.updatedAt = time.Now()
	}
}

// upgradeFailIfNotVerified 仅当当前阶段不是 verified 时才标记失败（原子防覆盖）：
// 节点收完包立即重启会使 PM 侧流写入报错，但包实际已完整接收并被节点校验通过
func upgradeFailIfNotVerified(username, errMsg string) {
	upgradeStatesMu.Lock()
	defer upgradeStatesMu.Unlock()
	st, ok := upgradeStates[username]
	if !ok {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.stage != "verified" {
		st.stage = "failed"
		st.errMsg = errMsg
		st.updatedAt = time.Now()
	}
}

// isUpgrading 仅在通知/传输阶段且状态新鲜时为真；闲置超时自动过期（兜底防卡）
func isUpgrading(username string) bool {
	upgradeStatesMu.Lock()
	defer upgradeStatesMu.Unlock()
	st, ok := upgradeStates[username]
	if !ok {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.stage != "notifying" && st.stage != "transferring" {
		return false
	}
	return time.Since(st.updatedAt) < upgradeIdleExpiry
}

func getUpgradeState(username string) (stage, version, errMsg string, sent, total int64, startedAt, updatedAt int64) {
	upgradeStatesMu.Lock()
	defer upgradeStatesMu.Unlock()
	st, ok := upgradeStates[username]
	if !ok {
		return "none", "", "", 0, 0, 0, 0
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	// 超过 1 小时的记录直接清掉（正常链路下升级全程不会超过 1 小时）
	if time.Since(st.startedAt) > time.Hour {
		delete(upgradeStates, username)
		return "none", "", "", 0, 0, 0, 0
	}
	return st.stage, st.version, st.errMsg, st.sentBytes, st.totalBytes, st.startedAt.Unix(), st.updatedAt.Unix()
}

// parseVersionSegments 五段数字版本号解析；不合法（如旧版 0.1.0-dev）返回 false
func parseVersionSegments(v string) ([]int64, bool) {
	parts := strings.Split(strings.TrimSpace(v), ".")
	if len(parts) != 5 {
		return nil, false
	}
	segs := make([]int64, 5)
	for i, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n < 0 {
			return nil, false
		}
		segs[i] = n
	}
	return segs, true
}

// compareVersions 逐段数值比较五段版本号；无法解析的一侧视为最低。返回 -1/0/1
func compareVersions(a, b string) int {
	pa, aok := parseVersionSegments(a)
	pb, bok := parseVersionSegments(b)
	if aok != bok {
		if aok {
			return 1
		}
		return -1
	}
	if !aok {
		return 0
	}
	for i := 0; i < 5; i++ {
		if pa[i] != pb[i] {
			if pa[i] > pb[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}

// handleVersionsList GET /api/v1/versions?endpoint=seeinps
func (s *Server) handleVersionsList(w http.ResponseWriter, r *http.Request) {
	endpoint := r.URL.Query().Get("endpoint")
	if endpoint == "" {
		endpoint = "seeinps"
	}
	list, err := s.store.ListVersions(endpoint)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, 5000, "查询版本列表失败")
		return
	}
	type versionResp struct {
		ID         int64  `json:"id"`
		Endpoint   string `json:"endpoint"`
		Version    string `json:"version"`
		GoOS       string `json:"goos"`
		GoArch     string `json:"goarch"`
		FileName   string `json:"fileName"`
		FileSize   int64  `json:"fileSize"`
		Sha256     string `json:"sha256"`
		Note       string `json:"note"`
		ReleasedBy string `json:"releasedBy"`
		CreatedAt  int64  `json:"createdAt"`
	}
	items := make([]versionResp, 0, len(list))
	for _, v := range list {
		items = append(items, versionResp{ID: v.ID, Endpoint: v.Endpoint, Version: v.Version, GoOS: v.GoOS, GoArch: v.GoArch, FileName: v.FileName,
			FileSize: v.FileSize, Sha256: v.Sha256, Note: v.Note, ReleasedBy: v.ReleasedBy, CreatedAt: v.CreatedAt})
	}
	data, _ := json.Marshal(map[string]interface{}{"code": 0, "data": items})
	w.Write(data)
}

// handleVersionUpload POST /api/v1/versions（multipart：endpoint/version/note/file）
func (s *Server) handleVersionUpload(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, 2000, "请求须为 multipart/form-data")
		return
	}
	var endpoint, version, note, fileName, goos, goarch string
	var tmpPath string
	var size int64
	hash := sha256.New()

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, 2000, "解析上传数据失败")
			return
		}
		switch part.FormName() {
		case "endpoint":
			b, _ := io.ReadAll(io.LimitReader(part, 64))
			endpoint = strings.TrimSpace(string(b))
		case "version":
			b, _ := io.ReadAll(io.LimitReader(part, 64))
			version = strings.TrimSpace(string(b))
		case "note":
			b, _ := io.ReadAll(io.LimitReader(part, 4096))
			note = strings.TrimSpace(string(b))
		case "goos":
			b, _ := io.ReadAll(io.LimitReader(part, 32))
			goos = strings.TrimSpace(string(b))
		case "goarch":
			b, _ := io.ReadAll(io.LimitReader(part, 32))
			goarch = strings.TrimSpace(string(b))
		case "file":
			if tmpPath != "" {
				writeErr(w, http.StatusBadRequest, 2000, "只能上传一个文件")
				return
			}
			fileName = filepath.Base(part.FileName())
			if fileName == "" || fileName == "." || fileName == "/" {
				writeErr(w, http.StatusBadRequest, 2000, "文件名非法")
				return
			}
			if err := os.MkdirAll(releaseDir, 0755); err != nil {
				writeErr(w, http.StatusInternalServerError, 5000, "创建版本目录失败")
				return
			}
			tmp, err := os.CreateTemp(releaseDir, "upload-*.tmp")
			if err != nil {
				writeErr(w, http.StatusInternalServerError, 5000, "写入临时文件失败")
				return
			}
			tmpPath = tmp.Name()
			n, copyErr := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(part, maxReleaseSize+1))
			tmp.Close()
			if copyErr != nil {
				os.Remove(tmpPath)
				writeErr(w, http.StatusInternalServerError, 5000, "接收文件失败")
				return
			}
			size = n
		}
	}

	if endpoint != "seeinps" {
		os.Remove(tmpPath)
		writeErr(w, http.StatusBadRequest, 2000, "v1 仅支持 seeinps 端版本包")
		return
	}
	// 平台字段：缺省按 linux/amd64 处理（兼容不带平台参数的调用），填写时必须在白名单内
	if goos == "" && goarch == "" {
		goos, goarch = "linux", "amd64"
	}
	if !knownGOOS[goos] || !knownGOARCH[goarch] {
		os.Remove(tmpPath)
		writeErr(w, http.StatusBadRequest, 2000, "平台参数非法（goos: linux/windows/darwin，goarch: amd64/arm64/arm/386）")
		return
	}
	if size <= 0 || size > maxReleaseSize {
		os.Remove(tmpPath)
		writeErr(w, http.StatusBadRequest, 2000, "升级包为空或超过 200MB 限制")
		return
	}
	// 自动识别包内版本（构建时注入 seeinp-version: 标识）：未填写则采用，填写了则校验一致
	detected, derr := versionpkg.ExtractVersionFromFile(tmpPath)
	if derr == nil && detected != "" {
		if version == "" {
			version = detected
		} else if version != detected {
			os.Remove(tmpPath)
			writeErr(w, http.StatusBadRequest, 2002,
				fmt.Sprintf("包内版本为 %s，与填写的 %s 不一致，请修正后重试", detected, version))
			return
		}
	} else if version == "" {
		os.Remove(tmpPath)
		writeErr(w, http.StatusBadRequest, 2002, "无法识别包内版本，请使用 build/build.sh 构建或手动填写版本号")
		return
	}
	if !versionPattern.MatchString(version) {
		os.Remove(tmpPath)
		writeErr(w, http.StatusBadRequest, 2002, "版本号格式应为五段数字，如 1.0.26.0829.01")
		return
	}
	// 先查重：同端同版本号同平台已存在时只清理本次临时文件，绝不能动已发布版本的目录
	if _, err := s.store.GetVersionByName(endpoint, version, goos, goarch); err == nil {
		os.Remove(tmpPath)
		writeErr(w, http.StatusConflict, 3001, "该版本号已存在")
		return
	}

	// 包文件按平台分目录：releases/{endpoint}/{version}/{goos}-{goarch}/
	dir := filepath.Join(releaseDir, endpoint, version, goos+"-"+goarch)
	if err := os.MkdirAll(dir, 0755); err != nil {
		os.Remove(tmpPath)
		writeErr(w, http.StatusInternalServerError, 5000, "创建版本目录失败")
		return
	}
	finalPath := filepath.Join(dir, fileName)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		writeErr(w, http.StatusInternalServerError, 5000, "保存版本文件失败")
		return
	}

	v := &store.Version{
		Endpoint: endpoint, Version: version, GoOS: goos, GoArch: goarch, FileName: fileName, FileSize: size,
		Sha256: hex.EncodeToString(hash.Sum(nil)), Note: note, ReleasedBy: operatorFrom(r),
	}
	if err := s.store.CreateVersion(v); err != nil {
		os.Remove(finalPath)
		_ = os.Remove(dir) // 仅当目录已空时生效，不影响其他版本
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, 3001, "该版本号已存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, 5000, "保存版本记录失败")
		return
	}
	fmt.Printf("[VERSION] uploaded %s %s %s/%s (%d bytes, sha256=%s) by %s\n", endpoint, version, goos, goarch, size, v.Sha256[:12], v.ReleasedBy)
	s.audit(r, "version_upload", version, fmt.Sprintf("endpoint=%s platform=%s/%s size=%d sha256=%s", endpoint, goos, goarch, size, v.Sha256[:16]))
	writeOK(w, map[string]interface{}{"id": v.ID, "version": v.Version, "sha256": v.Sha256, "fileSize": size})
}

// handleVersionDelete DELETE /api/v1/versions/{id}
func (s *Server) handleVersionDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, 2000, "非法版本 ID")
		return
	}
	v, err := s.store.GetVersion(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, 2004, "版本不存在")
		return
	}
	ok, err := s.store.DeleteVersion(id)
	if err != nil || !ok {
		writeErr(w, http.StatusInternalServerError, 5000, "删除版本记录失败")
		return
	}
	os.RemoveAll(filepath.Join(releaseDir, v.Endpoint, v.Version, v.GoOS+"-"+v.GoArch))
	fmt.Printf("[VERSION] deleted %s %s %s/%s\n", v.Endpoint, v.Version, v.GoOS, v.GoArch)
	s.audit(r, "version_delete", v.Version, fmt.Sprintf("endpoint=%s platform=%s/%s file=%s", v.Endpoint, v.GoOS, v.GoArch, v.FileName))
	writeOK(w, nil)
}

// handleClientUpgrade POST /api/v1/clients/{username}/upgrade {versionId}
// 流程：UPGRADE_PUSH 通知（预检应答）→ 0x05 数据流直传二进制 → B 端校验后自替换重启；
// 最终成功以重连后 HELLO 上报的新版本为准（前端轮询 /api/v1/clients 观察）。
func (s *Server) handleClientUpgrade(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	var req struct {
		VersionID int64 `json:"versionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.VersionID <= 0 {
		writeErr(w, http.StatusBadRequest, 2000, "请求需包含 versionId")
		return
	}
	v, err := s.store.GetVersion(req.VersionID)
	if err != nil || v.Endpoint != "seeinps" {
		writeErr(w, http.StatusNotFound, 2004, "版本不存在")
		return
	}
	client, active := s.isClientActive(username)
	if !active {
		writeErr(w, http.StatusBadGateway, 5002, "该 seeinps 不在线，无法升级")
		return
	}
	// 平台强校验：升级包平台必须与节点平台完全一致，杜绝跨平台误推导致节点 exec 失败
	if v.GoOS != client.goos || v.GoArch != client.goarch {
		writeErr(w, http.StatusBadRequest, 2000,
			fmt.Sprintf("升级包平台不匹配：节点是 %s/%s，包是 %s/%s", client.goos, client.goarch, v.GoOS, v.GoArch))
		return
	}
	// 相同或更低版本也允许推送（即手动回滚到旧包），最终版本号变化由前端轮询观察
	if isUpgrading(username) {
		writeErr(w, http.StatusConflict, 3001, "该节点正在升级中")
		return
	}
	beginUpgrade(username, v.Version, v.FileSize)

	// 1. 通知 B 端（预检：平台匹配、磁盘空间等），复用 pending 请求-应答机制
	push := protocol.NewMessage(protocol.TypeUpgradePush, &protocol.UpgradePushData{
		ReleaseID: v.ID, Version: v.Version, GoOS: v.GoOS, GoArch: v.GoArch, Sha256: v.Sha256, Size: v.FileSize,
	})
	resp, err := s.clientRequest(client, push, 15*time.Second)
	if err != nil {
		upgradeStage(username, "failed", "升级通知超时: "+err.Error())
		writeErr(w, http.StatusGatewayTimeout, 5003, "升级通知超时: "+err.Error())
		return
	}
	if resp.Code == nil || *resp.Code != protocol.CodeOK {
		msgText := "seeinps 拒绝升级"
		if resp.Data != nil {
			b, _ := json.Marshal(resp.Data)
			d := &protocol.UpgradeReportData{}
			if json.Unmarshal(b, d) == nil && d.Error != "" {
				msgText = d.Error
			}
		}
		upgradeStage(username, "failed", msgText)
		writeErr(w, http.StatusBadGateway, 5002, msgText)
		return
	}

	// 2. 0x05 数据流直传二进制放后台协程：慢链路可能持续数分钟，
	// HTTP 请求立即返回，前端经 upgrade-status 轮询进度
	go func() {
		defer func() { _ = recover() }()
		upgradeStage(username, "transferring", "")
		if err := s.sendUpgradeStream(client, v); err != nil {
			fmt.Printf("[UPGRADE] %s stream send error: %v\n", username, err)
			// 节点收完全部字节并校验通过后会立即重启（旧连接关闭 → session shutdown），
			// 此时 UPGRADE_REPORT 已把状态推进为 verified，不能再覆盖为 failed
			upgradeFailIfNotVerified(username, "传输升级包失败: "+err.Error())
			return
		}
		upgradeStage(username, "sent", "")
		fmt.Printf("[UPGRADE] %s package %s sent, waiting for node restart\n", username, v.Version)
	}()
	s.audit(r, "client_upgrade", username, fmt.Sprintf("version=%s sha256=%s", v.Version, v.Sha256[:16]))
	writeOK(w, map[string]interface{}{"started": true, "version": v.Version, "totalBytes": v.FileSize})
}

// handleUpgradeStatus GET /api/v1/clients/{username}/upgrade-status：前端轮询升级进度
func (s *Server) handleUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	stage, version, errMsg, sent, total, startedAt, updatedAt := getUpgradeState(username)
	writeOK(w, map[string]interface{}{
		"stage": stage, "version": version, "error": errMsg,
		"sentBytes": sent, "totalBytes": total, "startedAt": startedAt, "updatedAt": updatedAt,
	})
}

// sendUpgradeStream 经 yamux 0x05 流发送升级包：头部 4 字节长度 + JSON 元信息，随后为文件字节
func (s *Server) sendUpgradeStream(client *Client, v *store.Version) error {
	f, err := os.Open(filepath.Join(releaseDir, v.Endpoint, v.Version, v.GoOS+"-"+v.GoArch, v.FileName))
	if err != nil {
		return fmt.Errorf("open release file: %w", err)
	}
	defer f.Close()

	stream, err := client.session.Open()
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	// 流头与代理数据流同构：先写 1B stype（0x05 升级流），随后 4B 长度 + JSON 元信息 + 文件字节
	if _, err := stream.Write([]byte{protocol.StreamTypeUpgrade}); err != nil {
		return fmt.Errorf("write stype: %w", err)
	}
	meta, _ := json.Marshal(&protocol.UpgradePushData{
		ReleaseID: v.ID, Version: v.Version, GoOS: v.GoOS, GoArch: v.GoArch, Sha256: v.Sha256, Size: v.FileSize,
	})
	lenBuf := []byte{byte(len(meta) >> 24), byte(len(meta) >> 16), byte(len(meta) >> 8), byte(len(meta))}
	if _, err := stream.Write(lenBuf); err != nil {
		return fmt.Errorf("write header len: %w", err)
	}
	if _, err := stream.Write(meta); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	// 写截止时间：慢链路传输给足时间但有界，防止节点侧停读导致协程永久阻塞
	stream.SetWriteDeadline(time.Now().Add(30 * time.Minute))
	pw := &progressWriterFn{w: stream, f: func(sent int64) { upgradeProgress(client.username, sent) }}
	n, err := io.CopyBuffer(pw, f, make([]byte, 64*1024))
	if err != nil {
		return fmt.Errorf("send package: %w", err)
	}
	if n != v.FileSize {
		return fmt.Errorf("sent %d bytes, expect %d", n, v.FileSize)
	}
	return nil
}

// progressWriterFn 透传写入并上报进度（供升级传输协程刷新 sentBytes）
type progressWriterFn struct {
	w    io.Writer
	f    func(sent int64)
	sent int64
}

func (p *progressWriterFn) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	if n > 0 {
		p.sent += int64(n)
		p.f(p.sent)
	}
	return n, err
}

// handleUpgradeReport 处理 B 端升级包接收结果（downloaded/failed），刷新进度状态；成功以 HELLO 版本为准
func (s *Server) handleUpgradeReport(msg *protocol.Message, client *Client) {
	b, _ := json.Marshal(msg.Data)
	d := &protocol.UpgradeReportData{}
	json.Unmarshal(b, d)
	if d.Stage == "failed" {
		fmt.Printf("[UPGRADE] %s FAILED: %s\n", client.username, d.Error)
		upgradeStage(client.username, "failed", d.Error)
		s.store.InsertAuditLog(client.username, "upgrade_failed", d.Version, d.Error)
		return
	}
	fmt.Printf("[UPGRADE] %s package %s verified, node restarting\n", client.username, d.Version)
	upgradeStage(client.username, "verified", "")
	s.store.InsertAuditLog(client.username, "client_upgrade", d.Version, "via=publish verified")
}

// handleVersionListReq 处理 B 端版本列表查询：按节点平台过滤后返回（不含文件内容）
func (s *Server) handleVersionListReq(msg *protocol.Message, client *Client) {
	list, err := s.store.ListVersions("seeinps")
	code := int(protocol.CodeOK)
	resp := &protocol.VersionListRespData{Items: []protocol.VersionListItem{}}
	if err != nil {
		code = int(protocol.CodeInternal)
	} else {
		for _, v := range list {
			if v.GoOS != client.goos || v.GoArch != client.goarch {
				continue
			}
			resp.Items = append(resp.Items, protocol.VersionListItem{ID: v.ID, Version: v.Version, Note: v.Note, FileSize: v.FileSize, CreatedAt: v.CreatedAt})
		}
	}
	client.writeControl(&protocol.Message{Type: protocol.TypeVersionListResp, ID: msg.ID, Ts: time.Now().Unix(), Code: &code, Data: resp})
}

// handleVersionPullReq 处理 B 端拉包请求：应答包元信息，随后经 0x05 数据流直传（与推送升级共用传输路径）
func (s *Server) handleVersionPullReq(msg *protocol.Message, client *Client) {
	b, _ := json.Marshal(msg.Data)
	d := &protocol.VersionPullReqData{}
	json.Unmarshal(b, d)
	v, err := s.store.GetVersionByName("seeinps", d.Version, client.goos, client.goarch)
	code := int(protocol.CodeOK)
	respData := &protocol.UpgradePushData{}
	if err != nil {
		code = int(protocol.CodeBadRequest)
		fmt.Printf("[VERSION] %s pull %s: not found\n", client.username, d.Version)
	} else {
		respData = &protocol.UpgradePushData{ReleaseID: v.ID, Version: v.Version, GoOS: v.GoOS, GoArch: v.GoArch, Sha256: v.Sha256, Size: v.FileSize}
		fmt.Printf("[VERSION] %s pulls %s (%d bytes)\n", client.username, v.Version, v.FileSize)
		s.store.InsertAuditLog(client.username, "client_upgrade", v.Version, fmt.Sprintf("via=self-pull size=%d", v.FileSize))
	}
	client.writeControl(&protocol.Message{Type: protocol.TypeVersionPullResp, ID: msg.ID, Ts: time.Now().Unix(), Code: &code, Data: respData})
	if code == int(protocol.CodeOK) {
		go func() {
			defer func() { _ = recover() }()
			if err := s.sendUpgradeStream(client, v); err != nil {
				fmt.Printf("[VERSION] %s pull stream error: %v\n", client.username, err)
			}
		}()
	}
}
