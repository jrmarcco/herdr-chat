package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// 文件夹信任确认：
// /new 开出的 claude 在新目录首次启动会停在「Is this a project you trust?」上
// ( ❯ No, exit / Yes, I trust this folder )。
// 它发生在 claude 的状态集成起跑之前， hc 不报 blocked、对话框管线够不着，
// 你的第一句话只会被发送守卫拦下。
// /new 点名这个目录即是信任的决定——起跑后看着屏幕，认出信任确认就替你选 Yes；
// herdr-app 里开的终端 ( 不经 /new ) 则由房间命令 /trust 手动代答。
// 两条路都走 core.TrustAnswerKeys：认不出那个屏，一个键也不按。

const (
	trustTries  = 12
	trustWait   = 500 * time.Millisecond
	trustSettle = 800 * time.Millisecond
)

// autoTrust：起跑后的几眼里，信任确认出现即代答，并在房间里说一声。
// 只对 claude 出手 ( 别的 agent 各有自己的信任形态，不代答 )。
func (e *Engine) autoTrust(session, address, folder, line string) {
	if first := strings.Fields(line); len(first) == 0 || first[0] != cmdClaude {
		return
	}
	e.later(session, address, "trust", func() error {
		var lastErr error
		for i := 0; i < trustTries; i++ {
			if e.ctx == nil || e.ctx.Err() != nil {
				// 正在退出：走收尾。
				break
			}
			screen, err := e.HC.ScreenTail(e.ctx, session, address, ScreenLines)
			if err != nil {
				lastErr = err
				e.sleep(trustWait)
				continue
			}
			keys, ok := core.TrustAnswerKeys(screen)
			if !ok {
				// 还不是信任确认 ( 可能还在起跑 )，再看。
				e.sleep(trustWait)
				continue
			}
			if kerr := e.HC.Keys(e.ctx, session, address, keys); kerr != nil {
				// 按键失败交回 lane 如实记录。
				return kerr
			}
			e.sleep(trustSettle)
			xlog.For("engine").Info("已代答文件夹信任确认", "session", session, "address", address, "folder", folder)
			e.trustSaid(session, address, folder)
			return nil
		}

		// 到时也没等到信任确认：正常退出；读屏一直失败才有个说法。
		return lastErr
	})
}

// trustSaid 在房间里说一声代答的事；房间还没建成就免了 ( 第一帖自然说明 )。
func (e *Engine) trustSaid(session, address, folder string) {
	record, err := e.HC.GetTerminal(e.ctx, session, address)
	if err != nil {
		return
	}
	room, err := e.ensureRoom(session, record)
	if err != nil || room == "" {
		return
	}
	if _, err := e.Chat.Post(e.ctx, room, fmt.Sprintf(
		"✅ claude's folder-trust question answered for `%s` (you picked this folder in `/new`).", folder), ""); err != nil {
		xlog.For("engine").Warn("信任代答通知发送失败", "error", err)
	}
}

// onTrust：房间命令 /trust——认出信任屏即代答，认不出就把屏幕发回去。
func (e *Engine) onTrust(where core.Where, session, address string) error {
	text, err := e.HC.ScreenTail(e.ctx, session, address, ScreenLines)
	if err != nil {
		_, rerr := e.Chat.Reply(e.ctx, where, "Not delivered: "+hccli.PlainError(err))
		return rerr
	}
	keys, ok := core.TrustAnswerKeys(text)
	if !ok {
		_, rerr := e.Chat.Reply(e.ctx, where, "No folder-trust question on the screen.\n\n"+e.screen(session, address, ScreenLines))
		return rerr
	}
	if kerr := e.HC.Keys(e.ctx, session, address, keys); kerr != nil {
		_, rerr := e.Chat.Reply(e.ctx, where, "Not delivered: "+hccli.PlainError(kerr))
		return rerr
	}
	e.sleep(KeySettle)
	xlog.For("engine").Info("信任确认已按 /trust 代答", "session", session, "address", address)
	_, rerr := e.Chat.Reply(e.ctx, where, "Trusted.\n\n"+e.screen(session, address, ScreenLines))
	return rerr
}
