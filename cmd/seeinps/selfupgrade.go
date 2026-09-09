package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/seeinp/seeinp/internal/logx"
	"github.com/seeinp/seeinp/internal/protocol"
	versionpkg "github.com/seeinp/seeinp/internal/version"
)

// selfUpgrade B 端自主升级状态（自上传 / 从 seeinpm 拉取，内存态）。
// exec 重启后进程更替，状态自然归位（新进程 idle），前端以 /auth/status 的版本号判定完成。
type selfUpgrade struct {
	mu         sync.Mutex
	version    string
	stage      string // idle | pulling(向A端请求拉取) | receiving(接收中) | applying(校验通过,应用中) | failed
	errMsg     string
	totalBytes int64
	doneBytes  int64
	startedAt  time.Time
	updatedAt  time.Time
}

var selfUp = &selfUpgrade{stage: "idle"}

var selfVersionRe = regexp.MustCompile(`^\d{1,4}(\.\d{1,4}){4}$`)

func selfBusy() bool {
	selfUp.mu.Lock()
	defer selfUp.mu.Unlock()
	if selfUp.stage != "receiving" && selfUp.stage != "pulling" && selfUp.stage != "applying" {
		return false
	}
	return time.Since(selfUp.updatedAt) < 10*time.Minute
}

// selfSet 全量更新状态（total/done 传 -1 表示保持不变）
func selfSet(version, stage, errMsg string, total, done int64) {
	selfUp.mu.Lock()
	defer selfUp.mu.Unlock()
	if selfUp.stage == "idle" || version != selfUp.version {
		selfUp.startedAt = time.Now()
	}
	selfUp.version, selfUp.stage, selfUp.errMsg = version, stage, errMsg
	if total >= 0 {
		selfUp.totalBytes = total
	}
	if done >= 0 {
		selfUp.doneBytes = done
	}
	selfUp.updatedAt = time.Now()
}

func selfSnapshot() map[string]interface{} {
	selfUp.mu.Lock()
	defer selfUp.mu.Unlock()
	if selfUp.stage != "idle" && selfUp.stage != "failed" && time.Since(selfUp.updatedAt) > 10*time.Minute {
		// 闲置超时（如传输协程被写截止时间终止且未更新状态）视为失败
		selfUp.stage = "failed"
		selfUp.errMsg = "升级过程超时无进展"
	}
	return map[string]interface{}{
		"stage": selfUp.stage, "version": selfUp.version, "error": selfUp.errMsg,
		"totalBytes": selfUp.totalBytes, "doneBytes": selfUp.doneBytes,
	}
}

// handleSelfUpgradeStatus GET /api/v1/self-upgrade/status
func (c *Client) handleSelfUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	writeOK(w, selfSnapshot())
}

// handleSelfUpgrade POST /api/v1/self-upgrade（multipart: version + file）：B 端上传新版本自升级
func (c *Client) handleSelfUpgrade(w http.ResponseWriter, r *http.Request) {
	if selfBusy() {
		writeErr(w, http.StatusConflict, 3001, "已有升级正在进行中")
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, 2000, "请求须为 multipart/form-data")
		return
	}
	version := ""
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
		case "version":
			b, _ := io.ReadAll(io.LimitReader(part, 64))
			version = string(b)
		case "file":
			if err := os.MkdirAll("data", 0755); err != nil {
				writeErr(w, http.StatusInternalServerError, 5000, "创建数据目录失败")
				return
			}
			tmp, err := os.Create(upgradeTmpPath)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, 5000, "写入临时文件失败")
				return
			}
			n, copyErr := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(part, maxUpgradeSize+1))
			tmp.Close()
			if copyErr != nil {
				os.Remove(upgradeTmpPath)
				writeErr(w, http.StatusInternalServerError, 5000, "接收文件失败")
				return
			}
			size = n
		}
	}
	version = strings.TrimSpace(version)
	if size <= 0 || size > maxUpgradeSize {
		os.Remove(upgradeTmpPath)
		writeErr(w, http.StatusBadRequest, 2000, "安装包为空或超过 200MB 限制")
		return
	}
	// 自动识别包内版本（构建时注入 seeinp-version: 标识）；手填了则校验一致
	detected, derr := versionpkg.ExtractVersionFromFile(upgradeTmpPath)
	if derr == nil && detected != "" {
		if version == "" {
			version = detected
		} else if version != detected {
			os.Remove(upgradeTmpPath)
			writeErr(w, http.StatusBadRequest, 2002,
				fmt.Sprintf("包内版本为 %s，与填写的 %s 不一致，请修正后重试", detected, version))
			return
		}
	} else if version == "" {
		os.Remove(upgradeTmpPath)
		writeErr(w, http.StatusBadRequest, 2002, "无法识别包内版本，请使用 build/build.sh 构建或手动填写版本号")
		return
	}

	sha := hex.EncodeToString(hash.Sum(nil))
	selfSet(version, "applying", "", size, size)
	logx.Infof("[UPGRADE] self-upgrade uploaded, applying: version=%s size=%d sha256=%s", version, size, sha[:12])
	// 后台应用：校验已随接收完成，直接换二进制重启（失败回滚并置 failed）
	go func() {
		defer func() { _ = recover() }()
		exe, err := os.Executable()
		if err != nil {
			os.Remove(upgradeTmpPath)
			selfSet(version, "failed", "定位自身可执行文件失败", -1, -1)
			return
		}
		if err := applyAndRestart(exe, upgradeTmpPath); err != nil {
			os.Remove(upgradeTmpPath)
			logx.Errorf("[UPGRADE] self-upgrade failed: version=%s err=%v", version, err)
			selfSet(version, "failed", err.Error(), -1, -1)
			return
		}
		// unix：exec 已替换进程映像，不会执行到这里
		// windows 开发平台仅校验不重启，置终态避免前端一直等待
		selfSet(version, "failed", "当前为 Windows 开发环境，仅校验未重启（实际升级在 Linux 上执行）", -1, -1)
	}()

	// 上传成功即视为已受理（应用阶段秒级完成，重启由前端轮询 /auth/status 版本号判定）
	c.audit(r, "self_upgrade", version, fmt.Sprintf("size=%d sha256=%s", size, sha[:12]))
	writeOK(w, map[string]interface{}{"started": true, "version": version, "sha256": sha})
}

// handlePMVersions GET /api/v1/pm-versions：向 seeinpm 查询本平台可用版本列表
func (c *Client) handlePMVersions(w http.ResponseWriter, r *http.Request) {
	link := c.getLink()
	if link == nil {
		writeErr(w, http.StatusBadGateway, 5002, "未连接 seeinpm，无法获取版本列表")
		return
	}
	resp, err := link.request(protocol.NewMessage(protocol.TypeVersionListReq, &protocol.VersionListReqData{}), 15*time.Second)
	if err != nil {
		writeErr(w, http.StatusGatewayTimeout, 5003, "查询 seeinpm 版本超时: "+err.Error())
		return
	}
	if resp.Code == nil || *resp.Code != protocol.CodeOK {
		writeErr(w, http.StatusBadGateway, 5002, "seeinpm 返回错误")
		return
	}
	b, _ := json.Marshal(resp.Data)
	d := &protocol.VersionListRespData{}
	json.Unmarshal(b, d)
	if d.Items == nil {
		d.Items = []protocol.VersionListItem{}
	}
	writeOK(w, d.Items)
}

// handlePMUpgrade POST /api/v1/pm-upgrade {version}：从 seeinpm 拉取指定版本并升级
func (c *Client) handlePMUpgrade(w http.ResponseWriter, r *http.Request) {
	if selfBusy() {
		writeErr(w, http.StatusConflict, 3001, "已有升级正在进行中")
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !selfVersionRe.MatchString(req.Version) {
		writeErr(w, http.StatusBadRequest, 2000, "请求需包含合法的版本号")
		return
	}
	link := c.getLink()
	if link == nil {
		writeErr(w, http.StatusBadGateway, 5002, "未连接 seeinpm")
		return
	}
	selfSet(req.Version, "pulling", "", 0, 0)
	// 后台发起拉取：应答元信息后，seeinpm 经 0x05 流直传，handleUpgradeStream 接收并应用
	go func() {
		defer func() { _ = recover() }()
		resp, err := link.request(protocol.NewMessage(protocol.TypeVersionPullReq, &protocol.VersionPullReqData{Version: req.Version}), 15*time.Second)
		if err != nil {
			selfSet(req.Version, "failed", "拉取请求超时: "+err.Error(), -1, -1)
			return
		}
		if resp.Code == nil || *resp.Code != protocol.CodeOK {
			selfSet(req.Version, "failed", "seeinpm 无此版本或平台不匹配", -1, -1)
			return
		}
		// 元信息已收到；后续进度由 handleUpgradeStream 接收时刷新（receiving -> applying）
	}()
	c.audit(r, "pm_upgrade", req.Version, "via=seeinpm pull")
	writeOK(w, map[string]interface{}{"started": true, "version": req.Version})
}

// selfReceiveProgress 升级流接收进度回写（PM 推送与自主拉取共用），供 B 端展示
func (c *Client) selfReceiveStart(version string, total int64) { selfSet(version, "receiving", "", total, 0) }
func (c *Client) selfReceiveProgress(done int64)              { selfUp.mu.Lock(); selfUp.doneBytes = done; selfUp.updatedAt = time.Now(); selfUp.mu.Unlock() }
func (c *Client) selfApplying(version string)                 { selfUp.mu.Lock(); selfUp.stage, selfUp.errMsg, selfUp.updatedAt = "applying", "", time.Now(); selfUp.version = version; selfUp.mu.Unlock() }
func (c *Client) selfFailed(version, msg string)              { selfSet(version, "failed", msg, -1, -1) }

