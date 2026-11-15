package xlog

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// 按天落盘：bot 自己的日志写在 state 目录的 logs/<YYYY-MM-DD>.log ( 行格式
// 与 xlog 一致，仅文件按天分 )，超过保留天数的前一天的旧文件删掉。
// 只删得了本包写出的名字 ( \d{4}-\d{2}-\d{2}\.log )，别的东西不碰。 // keepDefault 与 的 14 天一致。
const keepDefault = 14

var dayFileRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.log$`)

// dailySink 是一个按天换文件的 io.Writer。
type dailySink struct {
	mu sync.Mutex

	dir  string
	keep int

	day  string
	file *os.File
}

func (d *dailySink) Write(p []byte) (int, error) {
	day := time.Now().Format("2006-01-02")
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file == nil || day != d.day {
		if d.file != nil {
			_ = d.file.Close
		}
		if err := os.MkdirAll(d.dir, 0o700); err != nil {
			return 0, err
		}
		f, err := os.OpenFile(filepath.Join(d.dir, day+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: 路径由本包按天拼出，非不可信输入
		if err != nil {
			return 0, err
		}
		d.file, d.day = f, day
		d.prune(day)
	}
	return d.file.Write(p)
}

func (d *dailySink) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file != nil {
		_ = d.file.Close
		d.file = nil
	}
}

// prune 删掉保留窗口之外的天文件。
func (d *dailySink) prune(today string) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return
	}
	cutoff, err := time.ParseInLocation("2006-01-02", today, time.Local)
	if err != nil {
		return
	}
	cutoff = cutoff.AddDate(0, 0, -d.keep)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !dayFileRe.MatchString(name) {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", name[:len(name)-len(".log")], time.Local)
		if err != nil || !day.Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(d.dir, name))
	}
}

// SetupDaily 让全局日志同时写终端与 logs/<YYYY-MM-DD>.log
// ( 行格式不变，旧天文件按 keepDays 修剪 )。
//
// 返回的 cleanup 冲刷并关闭文件。
func SetupDaily(dir string, keepDays int) func() {
	if keepDays <= 0 {
		keepDays = keepDefault
	}
	level := ParseLevel(os.Getenv("GOAW_LOG_LEVEL"))
	sink := &dailySink{dir: dir, keep: keepDays}
	terminal := NewConsoleHandler(os.Stdout, level, shouldColor(os.Getenv("GOAW_LOG_COLOR")))
	file := NewConsoleHandler(sink, level, false)
	slog.SetDefault(slog.New(multiHandler{handlers: []slog.Handler{terminal, file}}))
	return sink.close
}
