package engine

import (
	"time"

	"github.com/jrmarcco/herdr-chat/internal/cliversion"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// 新版提示：
// 起跑后问一次、之后每天问一次；有更新的版本且这个版本没提示过，就给 owner 一张卡，
// 只提示，不自己升级 ( `herdrchat update` 才动二进制 )。
const updateEvery = 24 * time.Hour

// startUpdateWatch 起跑 30 秒后问第一次 ( 刚起连日志都还没写完 )，
// 此后每天一次。失败静默 ( 网络不通不是 owner 的事 )。
func (e *Engine) startUpdateWatch() {
	go func() {
		t := time.NewTimer(e.every(30 * time.Second))
		defer t.Stop()
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-t.C:
				e.updateTick()
				t.Reset(e.every(updateEvery))
			}
		}
	}()
}

// updateTick 问一次新版；有就提示 ( 同一个版本只提示一次，记在 state )。
func (e *Engine) updateTick() {
	if e.ctx == nil || e.ctx.Err() != nil || e.Latest == nil {
		return
	}
	current := e.Version
	if current == "" {
		current = cliversion.Version
	}
	if cliversion.DevVersion(current) {
		return // 不是发布构建：提示"有新版"没有意义
	}
	latest, err := e.Latest(e.ctx)
	if err != nil {
		xlog.For("engine").Debug("查新版失败", "error", err)
		return
	}
	if latest == "" || !cliversion.Newer(latest, current) {
		return
	}

	st := e.read()
	if st.UpdateNoticed == latest {
		return
	}

	xlog.For("engine").Info("有新版", "latest", latest, "current", current)
	if e.Chat.Ready() {
		if err := e.Chat.TellOwner(e.ctx, core.Look{
			Title: "📦 herdrchat " + latest + " is out",
			Tone:  core.ToneWorking,
			Markdown: "herdrchat " + latest + " is out (this one is " + current + ").\n" +
				"On the machine it runs on, `herdrchat update` brings it in and restarts the bot.",
		}); err != nil {
			xlog.For("engine").Warn("新版提示没发出去", "error", err)
			return
		}
	}
	st.UpdateNoticed = latest
	e.write(st)
}
