package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/seeinp/seeinp/internal/logx"
	"github.com/seeinp/seeinp/internal/protocol"
)

// logLevelRe 匹配 toml 中 [logging] 段的 level 行，用于在线修改后写回持久化。
// 捕获行首缩进与引号内的 level 值，保持原格式（含注释）不变。
var logLevelRe = regexp.MustCompile(`(?m)^(\s*level\s*=\s*")[^"]*("\s*(?:#.*)?)$`)

// persistLogLevel 将新的日志级别写回配置文件 [logging] level 行（保留其余内容与注释）。
func persistLogLevel(configPath, newLevel string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	updated := logLevelRe.ReplaceAll(data, []byte("${1}"+newLevel+"${2}"))
	if string(updated) == string(data) {
		return nil
	}
	return os.WriteFile(configPath, updated, 0o644)
}

// handleLoggingGet GET /api/v1/logging：返回当前日志级别等配置。
func (c *Client) handleLoggingGet(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]interface{}{
		"level":       logx.GetLevel(),
		"path":        c.config.Logging.Path,
		"max_size":    c.config.Logging.MaxSize,
		"max_backups": c.config.Logging.MaxBackups,
	})
}

// handleLoggingSet PUT /api/v1/logging：在线修改日志级别并持久化到配置文件。
func (c *Client) handleLoggingSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, 2000, "请求体格式错误")
		return
	}
	if _, ok := logx.ParseLevel(req.Level); !ok {
		writeErr(w, http.StatusBadRequest, 2000, fmt.Sprintf("invalid log level %q (want debug/info/warn/error)", req.Level))
		return
	}
	// 级别变更需在收紧与放宽两个方向都留痕（requested 按旧级别、changed 按新级别）
	prev := logx.GetLevel()
	logx.Infof("[SYS] log level change requested: %s -> %s", prev, strings.ToLower(req.Level))
	if err := logx.SetLevel(req.Level); err != nil {
		writeErr(w, http.StatusBadRequest, 2000, err.Error())
		return
	}
	if err := persistLogLevel(c.configPath, logx.GetLevel()); err != nil {
		logx.Warnf("[SYS] persist log level failed: path=%s err=%v", c.configPath, err)
	}
	c.config.Logging.Level = logx.GetLevel()
	logx.Infof("[SYS] log level changed: %s -> %s", prev, logx.GetLevel())
	c.audit(r, "log_level_update", "", "level="+logx.GetLevel())
	writeOK(w, map[string]interface{}{"level": logx.GetLevel()})
}

// handleLoggingGetReq 控制通道：A 端查询 B 端当前日志级别（LOGGING_GET_REQ -> LOGGING_GET_RESP）。
func (c *Client) handleLoggingGetReq(msg *protocol.Message) *protocol.Message {
	code := int(protocol.CodeOK)
	return &protocol.Message{Type: protocol.TypeLoggingGetResp, ID: msg.ID, Ts: time.Now().Unix(),
		Code: &code,
		Data: &protocol.LoggingGetRespData{
			Level:      logx.GetLevel(),
			Path:       c.config.Logging.Path,
			MaxSize:    c.config.Logging.MaxSize,
			MaxBackups: c.config.Logging.MaxBackups,
		}}
}

// handleLoggingSetReq 控制通道：A 端（seeinpm）在线修改 B 端日志级别。
// 与 handleLoggingSet 共用生效/持久化/审计逻辑，操作者记为 A 端管理员。
// 注意：必须显式构造 LOGGING_SET_RESP（NewResponse 会生成 *_REQ_RESP 后缀，与 A 端期待不符）。
func (c *Client) handleLoggingSetReq(msg *protocol.Message) *protocol.Message {
	b, _ := json.Marshal(msg.Data)
	req := &protocol.LoggingSetReqData{}
	_ = json.Unmarshal(b, req)

	resp := &protocol.Message{Type: protocol.TypeLoggingSetResp, ID: msg.ID, Ts: time.Now().Unix()}
	if _, ok := logx.ParseLevel(req.Level); !ok {
		code := int(protocol.CodeBadRequest)
		resp.Code = &code
		resp.Data = &protocol.LoggingSetRespData{Level: logx.GetLevel()}
		return resp
	}
	prev := logx.GetLevel()
	// requested 按旧级别、changed 按新级别，两个方向都能留痕
	logx.Infof("[SYS] log level change requested by seeinpm: %s -> %s", prev, strings.ToLower(req.Level))
	if err := logx.SetLevel(req.Level); err != nil {
		code := int(protocol.CodeBadRequest)
		resp.Code = &code
		resp.Data = &protocol.LoggingSetRespData{Level: logx.GetLevel()}
		return resp
	}
	if err := persistLogLevel(c.configPath, logx.GetLevel()); err != nil {
		logx.Warnf("[SYS] persist log level failed: path=%s err=%v", c.configPath, err)
	}
	c.config.Logging.Level = logx.GetLevel()
	logx.Infof("[SYS] log level changed by seeinpm: %s -> %s", prev, logx.GetLevel())
	// 操作者是 A 端管理员（无 B 端 JWT），operator 记为 seeinpm-admin 以便统一审计视图区分
	_ = c.auditRecord("seeinpm-admin", "log_level_update", "", "via=seeinpm level="+logx.GetLevel())

	code := int(protocol.CodeOK)
	resp.Code = &code
	resp.Data = &protocol.LoggingSetRespData{Level: logx.GetLevel()}
	return resp
}