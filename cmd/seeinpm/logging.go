package main

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"

	"github.com/seeinp/seeinp/internal/logx"
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
		return nil // 未匹配到 level 行，忽略（配置可能在别处）
	}
	return os.WriteFile(configPath, updated, 0o644)
}

// handleLoggingGet GET /api/v1/logging：返回当前日志级别等配置。
func (s *Server) handleLoggingGet(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]interface{}{
		"level":       logx.GetLevel(),
		"path":        s.config.Logging.Path,
		"max_size":    s.config.Logging.MaxSize,
		"max_backups": s.config.Logging.MaxBackups,
	})
}

// handleLoggingSet PUT /api/v1/logging：在线修改日志级别并持久化到配置文件。
func (s *Server) handleLoggingSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, 2000, "请求体格式错误")
		return
	}
	if err := logx.SetLevel(req.Level); err != nil {
		writeErr(w, http.StatusBadRequest, 2000, err.Error())
		return
	}
	// 持久化；失败不影响运行时生效，但需告警
	if err := persistLogLevel(s.configPath, logx.GetLevel()); err != nil {
		logx.Warnf("[SYS] persist log level failed: path=%s err=%v", s.configPath, err)
	}
	// 同步内存配置，便于后续 GET 与展示
	s.config.Logging.Level = logx.GetLevel()
	s.audit(r, "log_level_update", "", "level="+logx.GetLevel())
	logx.Infof("[SYS] log level changed: level=%s", logx.GetLevel())
	writeOK(w, map[string]interface{}{"level": logx.GetLevel()})
}
