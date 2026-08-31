package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/seeinp/seeinp/internal/logx"
)

// 应用日志：用户目录 seeinps-deployer-logs/ 下按天轮转（保留 30 天），
// 记录所有操作、HTTP 访问、部署流程、下载进度里程碑、报错与成功。
var (
	logMu sync.Mutex
	logWr *logx.RotateWriter
)

// initAppLog 初始化轮转日志；日志不可用时静默降级（部署功能不受影响）
func initAppLog() {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	wr, err := logx.NewRotateWriter(logx.Config{
		Dir:        filepath.Join(home, "seeinps-deployer-logs"),
		Name:       "seeinps-deployer",
		Level:      "debug",
		MaxBackups: 30,
	})
	if err != nil {
		return
	}
	logMu.Lock()
	logWr = wr
	logMu.Unlock()
	// 标准库 log（net/http 内部错误、连接异常等）也落到同一份日志
	log.SetOutput(logWriter{})
}

// logWriter 让标准库 log 与 net/http 的内部输出也进入轮转日志
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	return writeLog("INFO", strings.TrimRight(string(p), "\n")), nil
}

// appLog 记录一条应用日志（level: info/warn/error）
func appLog(level, format string, args ...interface{}) {
	writeLog(strings.ToUpper(level), fmt.Sprintf(format, args...))
}

func writeLog(level, msg string) int {
	logMu.Lock()
	defer logMu.Unlock()
	if logWr == nil {
		return 0
	}
	msg = strings.TrimRight(msg, "\n")
	if msg == "" {
		return 0
	}
	n, _ := logWr.Write([]byte(time.Now().Format("2006-01-02 15:04:05.000") + " [" + level + "] " + msg + "\n"))
	return n
}
