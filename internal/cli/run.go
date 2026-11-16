package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/asks"
	"github.com/jrmarcco/herdr-chat/internal/chats"
	"github.com/jrmarcco/herdr-chat/internal/chats/feishu"
	"github.com/jrmarcco/herdr-chat/internal/config"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/engine"
	"github.com/jrmarcco/herdr-chat/internal/readers"
	"github.com/jrmarcco/herdr-chat/internal/readers/claude"
	"github.com/jrmarcco/herdr-chat/internal/readers/codex"
	"github.com/jrmarcco/herdr-chat/internal/readers/opencode"
	"github.com/jrmarcco/herdr-chat/internal/state"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// run 前台跑引擎，把真实的 hc、三个 reader 与飞书接在一起 —— 这就是bot 本体。
// `herdrchat start` 的服务与 hc 终端循环跑的也是它。
func (a *App) cmdRun([]string) int {
	pl, cfg, ok := a.ready()
	if !ok {
		return 1
	}
	cleanupLog := xlog.SetupDaily(a.logDir(pl), 14)

	defer cleanupLog()

	log := xlog.For("main")

	store := &state.FileStore{Path: filepath.Join(pl.StateDir, "state.json")}
	hc := a.HC
	shared := asks.New(hc, a.Home)

	reg := readers.NewRegistry()
	for _, rd := range []readers.Reader{claude.New(hc, shared), codex.New(hc, shared), opencode.New(hc)} {
		if err := reg.Register(rd); err != nil {
			log.Error("reader 装不上", "app", rd.App(), "error", err)
			return 1
		}
	}

	chat := feishu.New(cfg.AppID, cfg.AppSecret, cfg.OwnerOpenID, pl.Profile, store, pl.StateDir)
	chat.Muted = cfg.Muted
	chat.Silent = cfg.Silent
	chat.BotTags = cfg.BotTags
	chat.UserTags = cfg.UserTags
	chat.EncryptKey = cfg.EncryptKey
	chat.VerificationToken = cfg.VerificationToken

	if err := chats.CheckChat(chat); err != nil {
		log.Error("chat 不合法", "error", err)
		return 1
	}

	e := engine.New(chat, hc, reg, store, cfg.Sessions)
	e.Env = a.Env
	e.Version = a.Version
	e.Latest = func(ctx context.Context) (string, error) {
		rel, err := latestOf(ctx)
		return rel.Tag, err
	}
	e.History = cfg.History
	e.Inbox = filepath.Join(pl.StateDir, "inbox")
	e.SetAsks(shared)
	e.SetOwner = func(ownerID string) error {
		return config.SetConfigValue(a.configFile(pl), chat.OwnerKey(), ownerID)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	e.Restart = func() {
		log.Info("配对完成：退出让位，由服务或终端循环拉起")
		stop()
	}

	// 上次没退干净 ( 崩了、被杀 ) → 起来说一声 ( 放在 e.Start 之后：chat 的 client 那时才建好，早一步 TellOwner 会空指针 )；
	// 这次跑着的心跳文件在优雅退出时删掉，在就说明上一次是意外断的。
	running := filepath.Join(pl.StateDir, "running")
	backNotice := func() {
		info, err := os.Stat(running)
		if err != nil {
			return
		}
		mins := int(time.Since(info.ModTime()).Minutes() + 0.5)
		_ = os.Remove(running)
		if chat.Ready() {
			_ = chat.TellOwner(context.Background(), core.Look{
				Title: "✅ herdrchat is back", Tone: core.ToneGood,
				Markdown: fmt.Sprintf("Back after ~%d min — it had stopped without a word. `herdrchat logs --errors` says why.", mins),
			})
		} else {
			log.Info("上次没退干净，但还没有 owner：无从通知")
		}
	}

	// 心跳文件两行：
	// 启动时刻与 pid——mtime 是"还活着"的心跳 ( 分钟级 touch )，
	// pid 给 `herdrchat update` 用 ( SIGTERM 这个进程，hc 终端里的循环 3 秒后 // 拉起新二进制 )。
	beat := time.NewTicker(time.Minute)
	defer beat.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-beat.C:
				_ = os.Chtimes(running, a.now(), a.now())
			}
		}
	}()

	// 主 goroutine 的 panic：带上栈记日志、尽力告诉 owner，再让外层拉起。
	crashed := false
	defer func() {
		if p := recover(); p != nil {
			crashed = true
			log.Error("panic 崩溃", "panic", p, "stack", string(debug.Stack()))

			// 心跳先删：通知再出岔子也不能把"崩溃→重启→又看见心跳"变成环。
			_ = os.Remove(running)
			if chat.Ready() {
				_ = chat.TellOwner(context.Background(), core.Look{
					Title: "🔴 herdrchat is down", Tone: core.ToneBad,
					Markdown: clipWords(fmt.Sprint(p), 300) + "\nIt comes back by itself in a moment.",
				})
			}
			cleanupLog()
			os.Exit(1)
		}
	}()

	log.Info("herdrchat run", "chat", chat.Name, "sessions", cfg.Sessions)
	if err := e.Start(ctx, nil); err != nil {
		log.Error("引擎起不来", "error", err)
		return 1
	}

	backNotice()

	// 心跳最后落盘：backNotice 只认"上一次"的残留。
	_ = os.WriteFile(running, []byte(a.now().Format(time.RFC3339)+"\n"+strconv.Itoa(os.Getpid())+"\n"), 0o600)

	<-ctx.Done()
	e.Stop()

	if !crashed {
		// 说好的退出：不是崩溃。
		_ = os.Remove(running)
	}
	return 0
}

// clipWords 把崩溃原因压到至多 n 个字符。
func clipWords(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
