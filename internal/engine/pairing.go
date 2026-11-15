package engine

import (
	"crypto/subtle"
	"fmt"
	"strings"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// 配对与 owner。
// bot 还没有 owner 时，直聊里来的任何话都只走这里：
// 码对了就认下这个人，其余一概不理 —— 没有 owner 的 bot 不替任何人起终端、杀终端。

// RestartWait 是配对成功后、进程退出让位给新 owner 实例之前的等待。
const RestartWait = 2 * time.Second

// tryPairing 直聊里的一句话是不是那个配对码。
func (e *Engine) tryPairing(in core.Incoming) {
	reply := func(s string) {
		if _, err := e.Chat.Reply(e.ctx, in.Where, s); err != nil {
			xlog.For("engine").Warn("回复失败", "error", err)
		}
	}

	text := strings.TrimSpace(in.Text)
	st := e.read()
	p := st.Pairing
	if p != nil && time.Now().UnixMilli() > p.Until {
		// 过期的码不算数。
		st.Pairing, p = nil, nil
		e.write(st)
	}
	if p == nil {
		byHand := ""
		if in.Sender != "" {
			byHand = fmt.Sprintf(" Or by hand: put `%s=%s` in its config.env and start it again.", e.Chat.OwnerKey(), in.Sender)
		}
		reply("This bot has no owner yet. On the machine it runs on, `herdrchat pair` shows a 6-digit code: send it here." + byHand)
		return
	}

	if subtle.ConstantTimeCompare([]byte(text), []byte(p.Code)) != 1 {
		p.Tries++
		if p.Tries >= core.PairTries {
			st.Pairing = nil
			e.write(st)
			xlog.For("engine").Warn("配对码错了太多次，已作废")
			reply("Not the code, too many times: it is void now. `herdrchat pair` shows a new one.")
			return
		}
		st.Pairing = p
		e.write(st)
		reply(fmt.Sprintf("Not the code (%d of %d tries left).", core.PairTries-p.Tries, core.PairTries))
		return
	}
	if in.Sender == "" {
		reply("The code is right, but this chat does not say who you are: nothing changed.")
		return
	}

	st.Pairing = nil
	e.write(st)
	if e.SetOwner != nil {
		if err := e.SetOwner(in.Sender); err != nil {
			xlog.For("engine").Error("owner 写入 config 失败", "error", err)
			reply("The code is right, but the owner could not be saved: " + err.Error())
			return
		}
	}
	xlog.For("engine").Info("配对成功，重启为 owner 的实例")
	reply(fmt.Sprintf("✅ You are this bot's owner now. It starts again in a moment; then each terminal gets its %s.", e.Chat.RoomWord()))
	if e.Restart != nil {
		go func() {
			e.sleep(RestartWait)
			e.Restart()
		}()
	}
}
