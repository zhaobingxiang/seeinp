// Package logx 提供按级别过滤、按天 + 按大小的日志轮转写入器，并接管 os.Stdout 加行首时间戳。
//
// # 输出格式
//
//	2026-09-10 15:42:46.123 [INFO] [TUNNEL] engine started adapter=SeeinpcVpn
//
// 时间戳由 Install 的泵协程统一附加；"级别"与"模块"分列两个方括号，互不干扰：
//   - 级别只可能是 DEBUG / INFO / WARN / ERROR（由 Level 类型保证，不再依赖字符串嗅探）；
//   - 模块由 Logger 携带（New("TUNNEL")），仅用于人读与前端过滤，不参与级别判定。
//
// # 过滤
//
// 过滤在写入侧按 Level 判定（Enabled / Logger.*f / 包级 Xxxf），SetLevel 对之后的新行即时生效。
// 仅有 filteredWriter（log 包输出）与无法类型化的第三方输出需要回退到行首标记识别，
// 此时不认识的行一律保留，避免误滤。
//
// # 文件命名策略
//   - 当前活动文件始终为 <name>.log（如 seeinpm.log），兼容现有日志 API/前端；
//   - 跨天或大小超限时，将活动文件归档为 <name>_<内容末次写入时刻>.log
//     （如 seeinpm_20260902-235958.log）：时间取归档内容的末次写入时刻，
//     日期即内容所属日（跨天轮转不张冠李戴），文件名字典序即时间序，同日多次轮转也不冲突；
//   - 极端情况下归档时刻重名时追加序号 .1/.2 ... 兜底；
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

// Level 日志级别，数值越大越严重。
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// levelNames 与 Level 一一对应（下标即 Level 数值）。
var levelNames = [...]string{"DEBUG", "INFO", "WARN", "ERROR"}

// String 返回级别的大写名（DEBUG/INFO/WARN/ERROR）。
func (l Level) String() string {
	if l < LevelDebug || l > LevelError {
		return levelNames[LevelInfo]
	}
	return levelNames[l]
}

// ParseLevel 把配置里的级别字符串规范化为 Level；非法值返回 false。
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug, true
	case "info":
		return LevelInfo, true
	case "warn", "warning":
		return LevelWarn, true
	case "error":
		return LevelError, true
	}
	return LevelInfo, false
}

// levelWord 供 filteredWriter 识别行首级别标记（log 包等无法类型化的输出）。
var levelWord = map[string]Level{
	"debug": LevelDebug, "info": LevelInfo, "warn": LevelWarn, "error": LevelError,
}

// 运行时日志级别，由 SetLevel 更新，写入侧读取，实现不重启进程即可切换过滤级别。默认 info。
var (
	levelMu  sync.RWMutex
	curLevel = LevelInfo
)

// SetLevel 运行时修改日志过滤级别（debug/info/warn/error），即时生效。
func SetLevel(level string) error {
	lv, ok := ParseLevel(level)
	if !ok {
		return fmt.Errorf("invalid log level %q (want debug/info/warn/error)", level)
	}
	levelMu.Lock()
	curLevel = lv
	levelMu.Unlock()
	return nil
}

// GetLevel 返回当前生效的过滤级别名（小写）。
func GetLevel() string {
	return strings.ToLower(CurrentLevel().String())
}

// CurrentLevel 返回当前生效的过滤级别。
func CurrentLevel() Level {
	levelMu.RLock()
	defer levelMu.RUnlock()
	return curLevel
}

// Enabled 报告给定级别在当前设置下是否会输出。
// 上层（如 seeinpc 的内存环形缓冲）应据此与实际落盘共用同一次判定，避免二者内容不一致。
func Enabled(lv Level) bool { return lv >= CurrentLevel() }

// Format 拼装不含时间戳的日志正文：有模块时为 "[LEVEL] [MOD] msg"，否则为 "[LEVEL] msg"。
// 模块名会经 normalizeModule 规范化，调用方无需预先处理。
func Format(lv Level, mod, msg string) string {
	mod = normalizeModule(mod)
	if mod == "" {
		return "[" + lv.String() + "] " + msg
	}
	return "[" + lv.String() + "] [" + mod + "] " + msg
}

// normalizeModule 规范化模块名：去空白、去首尾方括号、转大写。
func normalizeModule(mod string) string {
	m := strings.TrimSpace(mod)
	m = strings.Trim(m, "[]")
	return strings.ToUpper(m)
}

// -------------------- 分级日志便捷函数 --------------------

// write 按级别写出（级别过滤在此判定），供包级函数与 Logger 共用。
func write(lv Level, mod, format string, args ...interface{}) {
	if !Enabled(lv) {
		return
	}
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	_, _ = os.Stdout.WriteString(Format(lv, mod, msg) + "\n")
}

// Write 直接写出一条已格式化的日志（不做 %-展开），级别过滤仍会生效。
// 供需要自行拼装消息的调用方使用。
func Write(lv Level, mod, msg string) { write(lv, mod, "%s", msg) }

// Debugf 输出 debug 级日志（级别<=debug 时显示）。
func Debugf(format string, args ...interface{}) { write(LevelDebug, "", format, args...) }

// Infof 输出 info 级日志（级别<=info 时显示）。
func Infof(format string, args ...interface{}) { write(LevelInfo, "", format, args...) }

// Warnf 输出 warn 级日志（级别<=warn 时显示）。
func Warnf(format string, args ...interface{}) { write(LevelWarn, "", format, args...) }

// Errorf 输出 error 级日志（所有有效级别均显示）。
func Errorf(format string, args ...interface{}) { write(LevelError, "", format, args...) }

// Logger 带模块名的日志器。模块名只用于人读与前端过滤，不参与级别判定。
//
// 推荐用法：在组件初始化时创建一个，作为字段持有，避免每次调用重复分配。
//
//	var lg = logx.New("TUNNEL")
//	lg.Warnf("dial fail target=%s err=%v", target, err)
type Logger struct{ mod string }

// New 创建一个带模块名的 Logger；模块名会去方括号并转大写。
func New(module string) *Logger { return &Logger{mod: normalizeModule(module)} }

// Module 返回该 Logger 的模块名（nil 安全）。
func (l *Logger) Module() string {
	if l == nil {
		return ""
	}
	return l.mod
}

// Debugf 输出 debug 级日志。
func (l *Logger) Debugf(format string, args ...interface{}) {
	write(LevelDebug, l.Module(), format, args...)
}

// Infof 输出 info 级日志。
func (l *Logger) Infof(format string, args ...interface{}) {
	write(LevelInfo, l.Module(), format, args...)
}

// Warnf 输出 warn 级日志。
func (l *Logger) Warnf(format string, args ...interface{}) {
	write(LevelWarn, l.Module(), format, args...)
}

// Errorf 输出 error 级日志。
func (l *Logger) Errorf(format string, args ...interface{}) {
	write(LevelError, l.Module(), format, args...)
}

// RotateWriter 并发安全的轮转写入器
type RotateWriter struct {
	cfg Config

	mu        sync.Mutex
	file      *os.File
	day       string
	size      int64
	maxSize   int64
	lastWrite time.Time // 最近一次成功写入的时刻，归档文件名据此命名
}

// Config 轮转配置
type Config struct {
	Dir        string // 日志目录（不存在自动创建）
	Name       string // 日志基础名，如 seeinpm / seeinps
	MaxSizeMB  int    // 单文件大小上限（MB），<=0 表示仅按天轮转
	MaxBackups int    // 保留的轮转文件份数（含当前活动文件），<=0 表示不清理
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

// Dir 返回当前写入器使用的日志目录。
func (w *RotateWriter) Dir() string { return w.cfg.Dir }

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
	w.lastWrite = time.Now()
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
	if n > 0 {
		w.lastWrite = now
	}
	return n, err
}

// Sync 把已写入的内容刷到磁盘。进程异常退出（panic）前调用可减少尾部日志丢失。
func (w *RotateWriter) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	return w.file.Sync()
}

// rotateArchive 将当前活动文件归档到带日期的文件名，再打开新的活动文件；调用方需持有锁
func (w *RotateWriter) rotateArchive(now time.Time) {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	// 归档仅当活动文件已存在内容（避免产生空归档）
	if info, err := os.Stat(w.activePath()); err == nil && info.Size() > 0 {
		archive := w.nextArchivePath()
		_ = os.Rename(w.activePath(), archive)
	}
	if err := w.openActive(); err != nil {
		fmt.Fprintf(os.Stderr, "logx: reopen log: %v\n", err)
		return
	}
	w.cleanup()
}

// nextArchivePath 计算归档文件名：<name>_<末次写入时刻 YYYYMMDD-HHMMSS>.log。
// 时间取归档内容（活动文件）的末次写入时刻：跨天轮转时日期即内容所属日；
// 字典序即时间序，天然避免同日多份归档的排序错乱。极端重名时追加 .1/.2 兜底。
func (w *RotateWriter) nextArchivePath() string {
	base := filepath.Join(w.cfg.Dir, w.cfg.Name+"_"+w.lastWrite.Format("20060102-150405"))
	candidate := base + ".log"
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}
	for seq := 1; ; seq++ {
		candidate := fmt.Sprintf("%s.%d.log", base, seq)
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

// 当前活动的写入器，供 Sync / Dir 使用（Install 时接管，restore 时归还）
var (
	activeMu sync.RWMutex
	activeRW *RotateWriter
)

// Sync 刷新当前活动写入器的缓冲区。建议在 recover 分支与退出前调用。
func Sync() {
	activeMu.RLock()
	w := activeRW
	activeMu.RUnlock()
	if w != nil {
		_ = w.Sync()
	}
}

// Dir 返回当前日志目录（未 Install 时返回空串）。
func Dir() string {
	activeMu.RLock()
	w := activeRW
	activeMu.RUnlock()
	if w == nil {
		return ""
	}
	return w.Dir()
}

// WriteDirect 绕过 stdout 管道直接写入日志文件（逐行加时间戳），随后不做等待。
// 用途：进程即将 os.Exit（defer 不会执行、泵协程也来不及排空管道）的崩溃/退出路径。
// 正常业务日志请使用 Xxxf / Logger，那才是受 SetLevel 统一管辖的通道。
func WriteDirect(lv Level, mod, msg string) {
	activeMu.RLock()
	w := activeRW
	activeMu.RUnlock()
	if w == nil || !Enabled(lv) {
		return
	}
	prefix := Format(lv, mod, "")
	for _, line := range strings.Split(strings.TrimRight(msg, "\n"), "\n") {
		ts := time.Now().Format("2006-01-02 15:04:05.000")
		_, _ = w.Write([]byte(ts + " " + prefix + line + "\n"))
	}
}

// Panicf 记录崩溃信息并立即刷盘。专供 recover / os.Exit 前的收尾路径：
// 管道是异步的，此时改用直写才能保证堆栈真正落到文件里。
func Panicf(format string, args ...interface{}) {
	activeMu.RLock()
	w := activeRW
	activeMu.RUnlock()
	if w == nil {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		return
	}
	WriteDirect(LevelError, "", fmt.Sprintf(format, args...))
	Sync()
}

// Install 将 os.Stdout / os.Stderr 替换为「行首时间戳 + 轮转」写入器，并接管 log 包输出。
// 原理：os.Stdout/Stderr 换成 pipe 写端，后台协程按行加时间戳后写入 RotateWriter。
// 级别过滤发生在写入侧（logx.Debugf 等与 log 包的 filteredWriter），SetLevel 即时生效。
// 返回的 restore 可还原（关闭管道、冲刷残留日志、恢复标准输出）。
// 过滤级别以 level 初始化为全局运行时级别（之后可通过 SetLevel 在线调整），传入非法级别时按 info 兜底。
func Install(dir, name, level string, maxSizeMB, maxBackups int) (restore func()) {
	rw, err := NewRotateWriter(Config{Dir: dir, Name: name, MaxSizeMB: maxSizeMB, MaxBackups: maxBackups})
	if err != nil {
		fmt.Fprintf(os.Stderr, "logx: init rotate writer: %v\n", err)
		return func() {}
	}
	if lv, ok := ParseLevel(level); ok {
		levelMu.Lock()
		curLevel = lv
		levelMu.Unlock()
	} else {
		levelMu.Lock()
		curLevel = LevelInfo
		levelMu.Unlock()
	}
	activeMu.Lock()
	activeRW = rw
	activeMu.Unlock()

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
	// log 包默认 writer 在 init 时绑定 os.Stderr 指针，需显式接管并去掉自带时间戳（避免重复）。
	// 经带级别过滤的 writer 写入，使 log.Printf 也受 SetLevel 约束。
	log.SetOutput(&filteredWriter{rw: rw})
	log.SetFlags(0)

	done := make(chan struct{})
	var wg sync.WaitGroup
	// 统一把一行（含结尾换行）加时间戳后写入 rw。
	// 注意：级别过滤在写入侧完成（write/filteredWriter 按写入时刻的级别判定），
	// 泵内不再二次过滤——否则 SetLevel 会追溯丢弃已入管道但尚未泵出的行（时序竞态）。
	// 未带级别标记的行（如 panic 堆栈）始终保留。
	writeLine := func(b []byte) {
		if len(b) == 0 {
			return
		}
		ts := time.Now().Format("2006-01-02 15:04:05.000")
		_, _ = rw.Write([]byte(ts + " " + string(b)))
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
		_ = rw.Sync()
		_ = rw.Close()
		os.Stdout = origOut
		os.Stderr = origErr
		activeMu.Lock()
		if activeRW == rw {
			activeRW = nil
		}
		activeMu.Unlock()
	}
}

// filteredWriter 包装 RotateWriter，逐行应用全局级别过滤并为落盘行加时间戳。
// 用于 log 包输出（log.Printf/log.Fatalf 等），保证其同样受 SetLevel 约束且不重复时间戳。
type filteredWriter struct {
	rw *RotateWriter
}

// Write 实现 io.Writer：log 包一次写入可能含多行，逐行过滤并加时间戳落盘。
func (w *filteredWriter) Write(p []byte) (int, error) {
	data := string(p)
	for _, line := range strings.Split(data, "\n") {
		if len(line) == 0 {
			continue
		}
		if !lineShouldKeep(line) {
			continue
		}
		ts := time.Now().Format("2006-01-02 15:04:05.000")
		_, _ = w.rw.Write([]byte(ts + " " + line + "\n"))
	}
	return len(p), nil
}

// lineShouldKeep 按行首级别标记过滤 log 包等非类型化输出（需要读取全局级别）。
func lineShouldKeep(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) >= 7 && trimmed[0] == '[' {
		if end := strings.IndexByte(trimmed, ']'); end > 1 && end <= 7 {
			if rank, known := levelWord[strings.ToLower(trimmed[1:end])]; known {
				return Enabled(rank)
			}
		}
	}
	return true
}
