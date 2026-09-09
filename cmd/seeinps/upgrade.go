package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/seeinp/seeinp/internal/logx"
	"github.com/seeinp/seeinp/internal/protocol"
)

// upgradeTmpPath 升级包临时落盘路径；校验通过后由 applyAndRestart 原子换位
const upgradeTmpPath = "data/seeinps.upgrade.tmp"

// maxUpgradeSize 升级包大小上限
const maxUpgradeSize = 200 << 20

// handleUpgradePush 应答 A 端的升级推送通知（预检：包元信息合法性、磁盘空间）；
// 真正的二进制随后经 0x05 数据流传输
func (c *Client) handleUpgradePush(msg *protocol.Message) *protocol.Message {
	d := &protocol.UpgradePushData{}
	if b, err := json.Marshal(msg.Data); err == nil {
		json.Unmarshal(b, d)
	}
	code := protocol.CodeOK
	respData := &protocol.UpgradeReportData{Version: d.Version}
	switch {
	case d.Size <= 0 || d.Size > maxUpgradeSize || len(d.Sha256) != 64:
		code = protocol.CodeBadRequest
		respData.Stage = "failed"
		respData.Error = "升级包元信息非法"
	case d.GoOS != runtime.GOOS || d.GoArch != runtime.GOARCH:
		// 双保险：PM 侧已校验，这里再防一次跨平台误推（exec 外平台二进制会打挂节点）
		code = protocol.CodeBadRequest
		respData.Stage = "failed"
		respData.Error = fmt.Sprintf("升级包平台不匹配: 需要 %s/%s，收到 %s/%s", runtime.GOOS, runtime.GOARCH, d.GoOS, d.GoArch)
	case checkDiskSpace(d.Size+(32<<20)) != nil:
		code = protocol.CodeInternal
		respData.Stage = "failed"
		respData.Error = "磁盘空间不足: " + checkDiskSpace(d.Size+(32<<20)).Error()
	default:
		_ = os.MkdirAll("data", 0755)
		respData.Stage = "accepted"
	}
	logx.Infof("[UPGRADE] push received: version=%s platform=%s/%s size=%d accepted=%v", d.Version, d.GoOS, d.GoArch, d.Size, code == protocol.CodeOK)
	return &protocol.Message{Type: protocol.TypeUpgradePushResp, ID: msg.ID, Ts: time.Now().Unix(), Code: &code, Data: respData}
}

// handleUpgradeStream 接收 0x05 升级流：4B 长度 + JSON 元信息 + 二进制内容，边收边算 sha256；
// 校验通过后换二进制重启（unix），结果经 UPGRADE_REPORT 异步上报 A 端（最终成功以重连 HELLO 版本为准）
func (c *Client) handleUpgradeStream(stream net.Conn) {
	defer stream.Close()
	stream.SetReadDeadline(time.Now().Add(30 * time.Minute))

	var lenBuf [4]byte
	if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
		return
	}
	hlen := binary.BigEndian.Uint32(lenBuf[:])
	if hlen == 0 || hlen > 64<<10 {
		logx.Warnf("[UPGRADE] invalid meta length: %d", hlen)
		return
	}
	metaBuf := make([]byte, hlen)
	if _, err := io.ReadFull(stream, metaBuf); err != nil {
		return
	}
	meta := &protocol.UpgradePushData{}
	if err := json.Unmarshal(metaBuf, meta); err != nil || meta.Size <= 0 || meta.Size > maxUpgradeSize || len(meta.Sha256) != 64 {
		logx.Warnf("[UPGRADE] invalid stream meta")
		return
	}
	if meta.GoOS != runtime.GOOS || meta.GoArch != runtime.GOARCH {
		logx.Warnf("[UPGRADE] stream platform mismatch: need %s/%s got %s/%s", runtime.GOOS, runtime.GOARCH, meta.GoOS, meta.GoArch)
		c.sendUpgradeReport("failed", meta.Version, "升级包平台不匹配")
		c.selfFailed(meta.Version, "升级包平台不匹配")
		return
	}
	// B 端状态跟踪（PM 推送与自主拉取共用此流路径）
	c.selfReceiveStart(meta.Version, meta.Size)

	if err := os.MkdirAll("data", 0755); err != nil {
		c.sendUpgradeReport("failed", meta.Version, "创建数据目录失败: "+err.Error())
		return
	}
	tmp, err := os.Create(upgradeTmpPath)
	if err != nil {
		c.sendUpgradeReport("failed", meta.Version, "创建临时文件失败: "+err.Error())
		return
	}
	hash := sha256.New()
	n, copyErr := io.CopyBuffer(io.MultiWriter(tmp, hash), &progressReader{r: stream, f: func(done int64) { c.selfReceiveProgress(done) }}, make([]byte, 64*1024))
	tmp.Close()
	if copyErr != nil {
		os.Remove(upgradeTmpPath)
		c.selfFailed(meta.Version, "接收升级包中断: "+copyErr.Error())
		c.sendUpgradeReport("failed", meta.Version, "接收升级包中断: "+copyErr.Error())
		return
	}
	if n != meta.Size {
		os.Remove(upgradeTmpPath)
		c.selfFailed(meta.Version, fmt.Sprintf("包大小不符: 收到 %d 期望 %d", n, meta.Size))
		c.sendUpgradeReport("failed", meta.Version, fmt.Sprintf("包大小不符: 收到 %d 期望 %d", n, meta.Size))
		return
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(sum, meta.Sha256) {
		os.Remove(upgradeTmpPath)
		c.selfFailed(meta.Version, "SHA256 校验失败")
		c.sendUpgradeReport("failed", meta.Version, "SHA256 校验失败")
		return
	}
	logx.Infof("[UPGRADE] package verified, applying: version=%s size=%d", meta.Version, n)
	c.sendUpgradeReport("downloaded", meta.Version, "")
	c.selfApplying(meta.Version)
	time.Sleep(300 * time.Millisecond) // 等日志落盘再重启

	exe, err := os.Executable()
	if err != nil {
		os.Remove(upgradeTmpPath)
		c.selfFailed(meta.Version, "定位自身可执行文件失败")
		c.sendUpgradeReport("failed", meta.Version, "定位自身可执行文件失败")
		return
	}
	if err := applyAndRestart(exe, upgradeTmpPath); err != nil {
		os.Remove(upgradeTmpPath)
		c.selfFailed(meta.Version, err.Error())
		c.sendUpgradeReport("failed", meta.Version, err.Error())
		return
	}
	// unix：exec 已替换进程映像，不会执行到这里；windows：仅校验不重启（开发平台）
}

// progressReader 透传读取并回调进度（升级包接收进度回写 B 端状态）
type progressReader struct {
	r    io.Reader
	f    func(done int64)
	done int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		p.f(p.done)
	}
	return n, err
}

// sendUpgradeReport 异步上报升级包接收结果（连接可能已断，静默跳过）
func (c *Client) sendUpgradeReport(stage, version, errMsg string) {
	link := c.getLink()
	if link == nil {
		return
	}
	code := protocol.CodeOK
	_ = link.send(&protocol.Message{
		Type: protocol.TypeUpgradeReport,
		ID:   protocol.GenerateID(),
		Ts:   time.Now().Unix(),
		Code: &code,
		Data: &protocol.UpgradeReportData{Version: version, Stage: stage, Error: errMsg},
	})
}
