package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 大小触发轮转 + 归档命名 + max_backups 清理
func TestRotateBySize(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotateWriter(Config{Dir: dir, Name: "app", MaxSizeMB: 1, MaxBackups: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	chunk := make([]byte, 1024*1024+64) // 略超 1MB
	chunk[len(chunk)-1] = '\n'
	for i := 0; i < 4; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}

	entries := listLogs(dir)
	t.Logf("files: %v", entries)
	// 活动文件必须存在
	if !fileExists(filepath.Join(dir, "app.log")) {
		t.Fatal("active file app.log missing")
	}
	// 轮转文件形如 app_YYYYMMDD-HHMMSS.log（极端重名时 app_YYYYMMDD-HHMMSS.1.log）
	var archives []string
	for _, e := range entries {
		if strings.HasPrefix(e, "app_") {
			archives = append(archives, e)
		}
	}
	if len(archives) == 0 {
		t.Fatal("expected archive files")
	}
	// max_backups=3 → 归档最多 2 份（活动文件占 1 份）
	if len(archives) > 2 {
		t.Fatalf("expected <=2 archives, got %d: %v", len(archives), archives)
	}
}

// max_backups<=0 时不清理
func TestNoCleanupWhenZero(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotateWriter(Config{Dir: dir, Name: "app", MaxSizeMB: 1, MaxBackups: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	chunk := make([]byte, 1024*1024+64)
	chunk[len(chunk)-1] = '\n'
	for i := 0; i < 5; i++ {
		w.Write(chunk)
	}
	archives := 0
	for _, e := range listLogs(dir) {
		if strings.HasPrefix(e, "app_") {
			archives++
		}
	}
	if archives < 2 {
		t.Fatalf("expected many archives when no cleanup, got %d", archives)
	}
}

// Install 接管 stdout：写入的文件带时间戳且轮转生效
func TestInstallWritesTimestamped(t *testing.T) {
	dir := t.TempDir()
	restore := Install(dir, "app", "info", 0, 0)
	defer restore()
	os.Stdout.WriteString("hello world\n")
	os.Stderr.WriteString("err line\n")
	// 让 pump 协程落盘
	restore() // 内部会关闭 pipe 并等待
	restore = func() {}

	b, err := os.ReadFile(filepath.Join(dir, "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "hello world") || !strings.Contains(s, "err line") {
		t.Fatalf("content missing: %q", s)
	}
	if !strings.Contains(s, "20") || !strings.Contains(s, ":") {
		t.Fatalf("expected timestamp prefix: %q", s)
	}
}

// 级别过滤：info 拦截 [DEBUG]，warn 拦截 [INFO] 但放行无标记行；
// 新格式 "[LEVEL] [MOD] msg" 只按第一个方括号判定级别，模块名不干扰。
func TestLevelFilter(t *testing.T) {
	cases := []struct {
		level string
		line  string
		want  bool
	}{
		{"info", "[DEBUG] detail\n", false},
		{"info", "[INFO] ok\n", true},
		{"info", "[CTRL] Connecting...\n", true},
		{"info", "[TUNNEL] unmarked module line\n", true},
		{"warn", "[INFO] ok\n", false},
		{"warn", "[WARN] slow\n", true},
		{"warn", "[ERROR] boom\n", true},
		{"warn", "[CTRL] Connecting...\n", true},
		{"debug", "[DEBUG] detail\n", true},
		{"error", "[WARN] slow\n", false},
		{"debug", "[INFO] [TUNNEL] engine started\n", true},
		{"error", "[INFO] [TUNNEL] engine started\n", false},
		{"error", "[ERROR] [DIAL] dial fail\n", true},
	}
	defer func() { _ = SetLevel("info") }()
	for _, c := range cases {
		if err := SetLevel(c.level); err != nil {
			t.Fatal(err)
		}
		if got := lineShouldKeep(c.line); got != c.want {
			t.Errorf("level=%s line=%q: got %v want %v", c.level, c.line, got, c.want)
		}
	}
}

// Level 解析与格式化：模块名规范化、格式拼装、Enabled 语义
func TestLevelParseAndFormat(t *testing.T) {
	if lv, ok := ParseLevel("WARN"); !ok || lv != LevelWarn {
		t.Fatalf("ParseLevel(WARN) = %v,%v", lv, ok)
	}
	if lv, ok := ParseLevel("warning"); !ok || lv != LevelWarn {
		t.Fatalf("ParseLevel(warning) = %v,%v", lv, ok)
	}
	if _, ok := ParseLevel("verbose"); ok {
		t.Fatal("ParseLevel(verbose) should fail")
	}
	if got := Format(LevelInfo, "", "[CTRL] ok"); got != "[INFO] [CTRL] ok" {
		t.Fatalf("Format with empty module = %q", got)
	}
	if got := Format(LevelWarn, "tunnel", "dial fail"); got != "[WARN] [TUNNEL] dial fail" {
		t.Fatalf("Format = %q", got)
	}
	if got := New(" [tray] ").Module(); got != "TRAY" {
		t.Fatalf("normalizeModule = %q", got)
	}
	defer func() { _ = SetLevel("info") }()
	_ = SetLevel("warn")
	if Enabled(LevelInfo) || !Enabled(LevelWarn) || !Enabled(LevelError) {
		t.Fatal("Enabled semantics wrong at warn")
	}
	_ = SetLevel("debug")
	if !Enabled(LevelDebug) {
		t.Fatal("debug must be enabled at debug")
	}
}

// Logger 输出必须带上模块名，且与包级函数一样受级别过滤
func TestLoggerModuleAndFilter(t *testing.T) {
	dir := t.TempDir()
	restore := Install(dir, "app", "info", 0, 0)
	lg := New("TUNNEL")
	lg.Infof("engine started mtu=%d", 65535)
	lg.Warnf("dial fail target=%s", "10.0.1.5:443")
	lg.Debugf("per-conn detail (should be dropped at info)")
	SetLevel("warn")
	lg.Infof("dropped at warn")
	SetLevel("info")
	restore()

	b, err := os.ReadFile(filepath.Join(dir, "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "[INFO] [TUNNEL] engine started mtu=65535") {
		t.Fatalf("module info line missing: %q", s)
	}
	if !strings.Contains(s, "[WARN] [TUNNEL] dial fail target=10.0.1.5:443") {
		t.Fatalf("module warn line missing: %q", s)
	}
	if strings.Contains(s, "per-conn detail") {
		t.Fatalf("debug must be dropped at info: %q", s)
	}
	if strings.Contains(s, "dropped at warn") {
		t.Fatalf("info must be dropped at warn: %q", s)
	}
}

// 分级便捷函数输出 "[LEVEL] msg" 行：带级别标记可被过滤，且行首时间戳精确到毫秒
func TestLeveledFormatAndFilter(t *testing.T) {
	dir := t.TempDir()
	restore := Install(dir, "app", "info", 0, 0)
	Infof("[CTRL] listening on :99")
	Warnf("[ALLOC] quota reached")
	SetLevel("error")
	Infof("[CTRL] should be dropped")
	Errorf("[DATA] dial fail")
	SetLevel("info")
	restore()

	b, err := os.ReadFile(filepath.Join(dir, "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "[INFO] [CTRL] listening on :99") {
		t.Fatalf("info line missing: %q", s)
	}
	if !strings.Contains(s, "[WARN] [ALLOC] quota reached") {
		t.Fatalf("warn line missing: %q", s)
	}
	if strings.Contains(s, "should be dropped") {
		t.Fatalf("level=error must drop [INFO] lines: %q", s)
	}
	if !strings.Contains(s, "[ERROR] [DATA] dial fail") {
		t.Fatalf("error line missing: %q", s)
	}
	// 时间戳前缀：2006-01-02 15:04:05.000
	line := s[:strings.IndexByte(s, '\n')]
	if len(line) < 23 || line[4] != '-' || line[10] != ' ' || line[19] != '.' {
		t.Fatalf("expected millisecond timestamp prefix, got line: %q", line)
	}
}

// 跨天轮转：归档文件名使用内容所属日（末次写入时刻），而不是轮转发生的新日期
func TestDayCrossingArchiveName(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotateWriter(Config{Dir: dir, Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	w.Write([]byte("yesterday content\n"))
	// 模拟跨天：内容属于 2026-01-01，轮转发生在之后的某一天
	w.mu.Lock()
	w.day = "20260101"
	w.lastWrite = time.Date(2026, 1, 1, 23, 59, 58, 0, time.Local)
	w.mu.Unlock()
	w.Write([]byte("today content\n"))

	var archive string
	for _, e := range listLogs(dir) {
		if strings.HasPrefix(e, "app_") {
			archive = e
		}
	}
	if archive == "" {
		t.Fatal("expected an archive file")
	}
	if !strings.HasPrefix(archive, "app_20260101-") {
		t.Fatalf("archive must use content's own date 20260101, got %s", archive)
	}
	if !strings.Contains(archive, "-235958") {
		t.Fatalf("archive must use last-write time 235958, got %s", archive)
	}
}

func listLogs(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			names = append(names, e.Name())
		}
	}
	return names
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
