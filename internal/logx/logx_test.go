package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	// 轮转文件形如 app_YYYYMMDD.log / app_YYYYMMDD.1.log ...
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

// 级别过滤：info 拦截 [DEBUG]，warn 拦截 [INFO] 但放行无标记行
func TestLevelFilter(t *testing.T) {
	cfg := &Config{Level: "info"}
	if cfg.shouldKeep("[DEBUG] detail\n") {
		t.Fatal("info should drop [DEBUG]")
	}
	if !cfg.shouldKeep("[INFO] ok\n") {
		t.Fatal("info should keep [INFO]")
	}
	if !cfg.shouldKeep("[CTRL] Connecting...\n") {
		t.Fatal("unmarked lines should be kept at info")
	}
	cfg = &Config{Level: "warn"}
	if cfg.shouldKeep("[INFO] ok\n") {
		t.Fatal("warn should drop [INFO]")
	}
	if !cfg.shouldKeep("[WARN] slow\n") {
		t.Fatal("warn should keep [WARN]")
	}
	if !cfg.shouldKeep("[ERROR] boom\n") {
		t.Fatal("warn should keep [ERROR]")
	}
	if !cfg.shouldKeep("[CTRL] Connecting...\n") {
		t.Fatal("unmarked lines should be kept at warn")
	}
	cfg = &Config{Level: "debug"}
	if !cfg.shouldKeep("[DEBUG] detail\n") {
		t.Fatal("debug should keep [DEBUG]")
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
