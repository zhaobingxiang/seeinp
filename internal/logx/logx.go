// Package logx 提供按天 + 按大小的日志轮转写入器，并接管 os.Stdout 加行首时间戳。
//
// 文件命名策略：
//   - 当前活动文件始终为 <name>.log（如 seeinpm.log），兼容现有日志 API/前端；
//   - 跨天或大小超限时，将活动文件归档为 <name>_YYYYMMDD.log；
//   - 同一天内多次超限，追加序号 <name>_YYYYMMDD.1.log / .2.log ...；
//   - 轮转后按 <name>_*.log 清理，仅保留最近 maxBackups 份。
package logx

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config 轮转配置
type Config struct {
	Dir        string // 日志目录（不存在自动创建）
	Name       string // 日志基础名，如 seeinpm / seeinps
	Level      string // debug/info/warn/error；空按 info
	MaxSizeMB  int    // 单文件大小上限（MB），<=0 表示仅按天轮转
	MaxBackups int    // 保留的轮转文件份数（含当前活动文件），<=0 表示不清理
}

// levelRank 级别数值：debug=0 < info=1 < warn=2 < error=3
var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// shouldKeep 判断一行日志是否应输出（当前配置级别）。
// 只有行首明确带 [DEBUG]/[INFO]/[WARN]/[ERROR] 级别标记的行才参与过滤；
// 未标记或未知标记（如 [CTRL]/[ALLOC]）的行始终保留，保证现有日志不被误滤。
func (c *Config) shouldKeep(line string) bool {
	threshold, ok := levelRank[strings.ToLower(c.Level)]
	if !ok {
		threshold = 1 // 默认 info
	}
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) >= 7 && trimmed[0] == '[' {
		if end := strings.IndexByte(trimmed, ']'); end > 1 && end <= 7 {
			tag := strings.ToLower(trimmed[1:end])
			if rank, known := levelRank[tag]; known {
				return rank >= threshold
			}
		}
	}
	return true
}

// RotateWriter 并发安全的轮转写入器
type RotateWriter struct {
	cfg Config

	mu      sync.Mutex
	file    *os.File
	day     string
	size    int64
	maxSize int64
}

// NewRotateWriter 创建轮转写入器，立即打开活动文件（不存在则创建）
func NewRotateWriter(cfg Config) (*RotateWriter, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("logx: dir is required")
	}
	if cfg.Name == "" {
		cfg.Name = "app"
	}
	if cfg.MaxSizeMB < 0 {
		cfg.MaxSizeMB = 0
	}
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("logx: mkdir %s: %w", cfg.Dir, err)
	}
	w := &RotateWriter{cfg: cfg, maxSize: int64(cfg.MaxSizeMB) * 1024 * 1024}
	if err := w.openActive(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotateWriter) activePath() string {
	return filepath.Join(w.cfg.Dir, w.cfg.Name+".log")
}

// openActive 打开（或创建）活动文件并记录起始大小；调用方需持有锁
func (w *RotateWriter) openActive() error {
	f, err := os.OpenFile(w.activePath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("logx: open %s: %w", w.activePath(), err)
	}
	var size int64
	if info, statErr := f.Stat(); statErr == nil {
		size = info.Size()
	}
	w.file = f
	w.day = time.Now().Format("20060102")
	w.size = size
	return nil
}

// Write 实现 io.Writer；跨天或超限时先轮转
func (w *RotateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	day := now.Format("20060102")
	if day != w.day {
		w.rotateArchive(now)
	} else if w.maxSize > 0 && w.size+int64(len(p)) > w.maxSize {
		w.rotateArchive(now)
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// rotateArchive 将当前活动文件归档到带日期的文件名，再打开新的活动文件；调用方需持有锁
func (w *RotateWriter) rotateArchive(now time.Time) {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	// 归档仅当活动文件已存在内容（避免产生空归档）
	if info, err := os.Stat(w.activePath()); err == nil && info.Size() > 0 {
		archive := w.nextArchivePath(now)
		_ = os.Rename(w.activePath(), archive)
	}
	if err := w.openActive(); err != nil {
		fmt.Fprintf(os.Stderr, "logx: reopen log: %v\n", err)
		return
	}
	w.cleanup()
}

// nextArchivePath 计算归档文件名：<name>_YYYYMMDD[.N].log（N 从 1 起，避开已存在文件）
func (w *RotateWriter) nextArchivePath(now time.Time) string {
	day := now.Format("20060102")
	for seq := 0; ; seq++ {
		suffix := ""
		if seq > 0 {
			suffix = fmt.Sprintf(".%d", seq)
		}
		candidate := filepath.Join(w.cfg.Dir, fmt.Sprintf("%s_%s%s.log", w.cfg.Name, day, suffix))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// cleanup 删除超出 maxBackups 份数的旧归档；调用方需持有锁
func (w *RotateWriter) cleanup() {
	if w.cfg.MaxBackups <= 0 {
		return
	}
	entries, err := os.ReadDir(w.cfg.Dir)
	if err != nil {
		return
	}
	prefix := w.cfg.Name + "_"
	var archives []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, ".log") {
			archives = append(archives, n)
		}
	}
	sort.Strings(archives)
	// 保留最近 maxBackups-1 份归档（活动文件算 1 份）
	for len(archives) > w.cfg.MaxBackups-1 {
		_ = os.Remove(filepath.Join(w.cfg.Dir, archives[0]))
		archives = archives[1:]
	}
}

// Close 关闭活动文件
func (w *RotateWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}

// Install 将 os.Stdout / os.Stderr 替换为「级别过滤 + 行首时间戳 + 轮转」写入器，并接管 log 包输出。
// 原理：os.Stdout/Stderr 换成 pipe 写端，后台协程按行加时间戳（并按级别过滤）后写入 RotateWriter。
// 返回的 restore 可还原。
func Install(dir, name, level string, maxSizeMB, maxBackups int) (restore func()) {
	rw, err := NewRotateWriter(Config{Dir: dir, Name: name, MaxSizeMB: maxSizeMB, MaxBackups: maxBackups})
	if err != nil {
		fmt.Fprintf(os.Stderr, "logx: init rotate writer: %v\n", err)
		return func() {}
	}
	filter := &Config{Level: level}
	origOut, origErr := os.Stdout, os.Stderr
	r1, w1, err := os.Pipe()
	if err != nil {
		return func() {}
	}
	r2, w2, err := os.Pipe()
	if err != nil {
		return func() {}
	}
	os.Stdout = w1
	os.Stderr = w2
	// log 包默认 writer 在 init 时绑定 os.Stderr 指针，需显式接管并去掉自带时间戳（避免重复）
	log.SetOutput(rw)
	log.SetFlags(0)

	done := make(chan struct{})
	var wg sync.WaitGroup
	// 统一把一行（含结尾换行）按级别过滤后写入 rw
	writeLine := func(b []byte) {
		if len(b) == 0 {
			return
		}
		if !filter.shouldKeep(string(b)) {
			return
		}
		ts := time.Now().Format("2006-01-02 15:04:05")
		rw.Write([]byte(ts + " " + string(b)))
	}
	pump := func(r *os.File) {
		defer wg.Done()
		br := bufio.NewReaderSize(r, 64*1024)
		for {
			line, err := br.ReadString('\n')
			writeLine([]byte(line))
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump(r1)
	go pump(r2)
	go func() { wg.Wait(); close(done) }()
	return func() {
		w1.Close()
		w2.Close()
		<-done
		rw.Close()
		os.Stdout = origOut
		os.Stderr = origErr
	}
}
