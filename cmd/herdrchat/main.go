// Command herdrchat 是 bot 的 CLI：每个子命令 ( setup、pair、start… ) 随各自的
// 里程碑到来；目前它报出版本并客气地拒绝其余，让二进制从第一天起就存在、
// `go install` 有东西可跑。
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

const usage = `herdrchat — see and answer every hc terminal from Feishu or Telegram

usage: herdrchat <command> [options]

commands:
  setup      set a profile up: the chat app, its keys, the sessions, the first start
  pair       hand the bot to its owner with a 6-digit code
  init       write a starter config.env
  start      start the bot (a service, or a terminal that keeps it up)
  stop       stop it
  status     say whether it runs, and what is wrong if anything
  logs       its log, --errors for the problems
  install    keep it running as a system service
  uninstall  remove the service
  hooks      show, install or remove the PermissionRequest hook
  run        run the engine in the foreground
  version    say which herdrchat this is

options:
  --profile <name>   another setup beside the default (default: default)`

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

// notYet 是对里程碑尚未落地的子命令的诚实回答：如实说明，而不是假装。
func notYet(cmd, milestone string) int {
	fmt.Fprintf(os.Stderr, "herdrchat %s: not implemented yet (lands with %s)\n", cmd, milestone)
	return 1
}

func main() {
	cleanupLog := xlog.Setup()
	defer cleanupLog()
	// panic 在进程退出前带栈记入日志；服务或终端循环会把它重新拉起
	// ( 崩溃时的 owner 卡片属于 M2 的活 )。
	defer func() {
		if p := recover(); p != nil {
			xlog.For("main").Error("panic 崩溃", "panic", p, "stack", string(debug.Stack()))
			cleanupLog()
			os.Exit(1)
		}
	}()

	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, rest := args[0], args[1:]
	_ = rest
	var code int
	switch cmd {
	case "version":
		fmt.Println("herdrchat " + version())
	case "--help", "-h", "help":
		fmt.Println(usage)
	case "run":
		code = notYet(cmd, "M2: the engine")
	case "setup":
		code = notYet(cmd, "M7: the CLI")
	case "pair", "init", "start", "stop", "status", "logs", "install", "uninstall", "hooks", "update":
		code = notYet(cmd, "M7: the CLI")
	default:
		fmt.Fprintf(os.Stderr, "herdrchat: unknown command %q\n\n%s\n", cmd, usage)
		code = 2
	}
	os.Exit(code)
}
