// Command herdrchat 是 bot 的 CLI：每个子命令的实现在 internal/cli。
// 这里只做进程级的事：忽略 SIGPIPE ( 管道断了就安静地走 )、装配 App、
// 把 panic 带栈记进日志再退出 1。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/jrmarcco/herdr-chat/internal/cli"
	"github.com/jrmarcco/herdr-chat/internal/cliversion"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// version：发布构建由 goreleaser 经 ldflags 写进 cliversion.Version；
// 本地构建退回 build info ( go install 带 module 版本 )，再退回 "dev"。
func version() string {
	if !cliversion.Dev() {
		return cliversion.Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return cliversion.Version
}

func main() {
	signal.Ignore(syscall.SIGPIPE)

	// CLI 层的日志 ( run 子命令会换成按天落盘的那套 )。
	slog.SetDefault(slog.New(xlog.NewConsoleHandler(os.Stderr, xlog.ParseLevel(os.Getenv("GOAW_LOG_LEVEL")), false)))

	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	bin, err := os.Executable()
	if err != nil {
		bin = os.Args[0]
	} else if resolved, rerr := filepath.EvalSymlinks(bin); rerr == nil {
		bin = resolved
	}

	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}

	app := &cli.App{
		Env:      env,
		Home:     home,
		Stdin:    os.Stdin,
		Stdout:   cli.PipeSafe(os.Stdout, func() { os.Exit(0) }),
		Stderr:   os.Stderr,
		TTY:      isTTY(os.Stdin),
		Bin:      bin,
		Platform: runtime.GOOS,
		Version:  version(),
		HC:       hccli.New(),
	}

	code := func() (code int) {
		defer func() {
			if p := recover(); p != nil {
				fmt.Fprintf(os.Stderr, "herdrchat: panic: %v\n%s\n", p, debug.Stack())
				xlog.For("main").Error("panic 崩溃", "panic", p, "stack", string(debug.Stack()))
				os.Exit(1)
			}
		}()
		return app.Run(os.Args[1:])
	}
	os.Exit(code())
}

// isTTY 老老实实问终端本身：
// stty 只在一个真 tty 的 stdin 上成功。
// /dev/null ( 也是字符设备 )，ModeCharDevice 分不出来它。
// stty 不在就退而求其次。
func isTTY(f *os.File) bool {
	cmd := exec.CommandContext(context.Background(), "stty", "-a")
	cmd.Stdin = f
	return cmd.Run() == nil
}
