// Package tslog 将进程内所有写 os.Stdout 的日志输出（fmt.Printf 等）加上行首时间戳，
// 使运行日志文件每行带 `2006-01-02 15:04:05` 前缀，便于按时间筛选与排查。
package tslog

import (
	"bufio"
	"os"
	"sync"
	"time"
)

// Install 把 os.Stdout 替换为带时间戳行前缀的写入器（返回的恢复函数可还原）。
// 实现方式：os.Stdout 换为 pipe 写端，后台协程从 pipe 读端按行加时间戳后写回原 stdout。
// 注意：必须在任何日志输出之前调用。
func Install() (restore func()) {
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return func() {}
	}
	os.Stdout = w
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		br := bufio.NewReaderSize(r, 64*1024)
		for {
			line, err := br.ReadString('\n')
			if len(line) > 0 {
				ts := time.Now().Format("2006-01-02 15:04:05")
				mu.Lock()
				orig.WriteString(ts + " " + line)
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return func() {
		w.Close()
		<-done
		os.Stdout = orig
	}
}
