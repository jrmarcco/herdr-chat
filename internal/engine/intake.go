package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jrmarcco/herdr-chat/internal/asks"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/readers"
	"github.com/jrmarcco/herdr-chat/internal/state"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// 来自聊天侧的入口 ( spec §6.8，移植自原版 engine.mjs 的 from the chat 一节 )。

var answerRe = regexp.MustCompile(`^/answer\s+([\s\S]+)$`)

// onMessage：直聊是命令 ( /ls、/new、/kill 与帮助 )；房间里的话送进终端。
func (e *Engine) onMessage(in core.Incoming) {
	if e.ctx == nil || e.ctx.Err() != nil {
		return
	}
	if in.Room == "" {
		if len(in.Images) > 0 {
			if _, err := e.Chat.Reply(e.ctx, in.Where, fmt.Sprintf("Send images in a terminal's %s.", e.Chat.RoomWord())); err != nil {
				xlog.For("engine").Warn("回复失败", "error", err)
			}
			return
		}
		e.direct(in.Where, in.Text)
		return
	}
	session, address, ok := e.terminalOfRoom(in.Room)
	if !ok {
		xlog.For("engine").Info("这个房间不是 bot 建的", "room", in.Room)
		if _, err := e.Chat.Reply(e.ctx, in.Where, fmt.Sprintf(
			"Not delivered: this %s is not a terminal's. Each terminal has its own %s; the direct chat takes /ls, /new and /kill.",
			e.Chat.RoomWord(), e.Chat.RoomWord())); err != nil {
			xlog.For("engine").Warn("回复失败", "error", err)
		}
		return
	}
	files := e.imagesInto(in.Message, session, address, in.Images)
	text := in.Text
	for _, f := range files {
		if text == "" {
			text = f
		} else {
			text += " " + f
		}
	}
	e.later(session, address, "type", func() error {
		return e.typeInto(in.Where, in.Message, session, address, text)
	})
}

// direct：直聊的命令。
func (e *Engine) direct(where core.Where, text string) {
	cmd, rest, _ := strings.Cut(strings.TrimSpace(text), " ")
	var args []string
	if strings.TrimSpace(rest) != "" {
		args = strings.Fields(rest)
	}
	reply := func(s string) {
		if _, err := e.Chat.Reply(e.ctx, where, s); err != nil {
			xlog.For("engine").Warn("回复失败", "error", err)
		}
	}
	switch cmd {
	case "/ls":
		e.refreshOverview(true)
	case "/new":
		reply(e.newTerminal(args))
	case "/kill":
		if len(args) < 2 || !e.knownSession(args[0]) {
			reply("give `/kill <session> <address>`")
			return
		}
		if err := e.HC.Kill(e.ctx, args[0], args[1]); err != nil {
			reply("Not delivered: " + hccli.PlainError(err))
			return
		}
		reply(fmt.Sprintf("Ended `%s` in %s.", args[1], args[0]))
	default:
		// 平台自有命令 ( 由 chat 实现 )，其余给帮助。
		if handled, err := e.Chat.Command(cmd, args, where); handled || err != nil {
			return
		}
		reply(e.help())
	}
}

// help：直聊的帮助文本。
func (e *Engine) help() string {
	lines := []string{
		"`/ls` a new overview of every terminal (the overview also keeps itself up to date)",
		"`/new <session> <space>/<tab> [command]` start a terminal (default `claude`); `--cwd <dir>` first to pick its folder",
		"`/kill <session> <address>` end a terminal",
		fmt.Sprintf("In a terminal's %s: type to talk to it; `/commands` (what it can do here), `/screen`, `/answer <text>`, `/esc`, `/history` (the conversation so far, as a file).", e.Chat.RoomWord()),
	}
	return strings.Join(lines, "\n")
}

// newTerminal：/new 起一个终端 ( spec §6.8 )。space 复用同 space 兄弟终端的
// herdr.space 标签，没有就生成；登录 shell + exec，免得留下一个光壳 shell。
// ( herdr-app 里的 tab 命名随 M7 的 nameTab 一起来。 )
func (e *Engine) newTerminal(args []string) string {
	cwd := ""
	if len(args) > 0 && args[0] == "--cwd" {
		if len(args) < 2 {
			return "give `/new --cwd <dir> <session> <space>/<tab> [command]`"
		}
		cwd, args = args[1], args[2:]
	}
	if len(args) < 2 {
		return "give `/new <session> <space>/<tab> [command]`"
	}
	session, place := args[0], args[1]
	command := args[2:]
	if !e.knownSession(session) {
		return fmt.Sprintf("unknown session %q; known: %s", session, strings.Join(e.Sessions, ", "))
	}
	slash := strings.Index(place, "/")
	if slash < 1 || slash == len(place)-1 {
		return "give the place as <space>/<tab>"
	}
	space, tab := place[:slash], place[slash+1:]
	spaceID := "sp" + randHex()
	folder := cwd
	if folder == "" {
		folder = homeDir()
	}
	// 同 space 的兄弟终端：沿用它的 space id 与工作目录。
	if v := e.viewOf(session); v != nil {
		for _, t := range v.All() {
			if s, _, ok := hccli.SplitWhere(t.Where()); ok && s == space && t.Label("herdr.space") != "" {
				spaceID = t.Label("herdr.space")
				if cwd == "" {
					if fg, _ := t["foreground"].(map[string]any); fg != nil {
						if c, _ := fg["cwd"].(string); c != "" {
							folder = c
						}
					}
				}
				break
			}
		}
	}
	line := "claude"
	if len(command) > 0 {
		line = strings.Join(command, " ")
	}
	tabID := "tb" + randHex()
	out, err := e.HC.RunTerminal(e.ctx, session, hccliRunOpts(folder, spaceID, tabID, space, tab, line))
	if err != nil {
		return "Not started: " + hccli.PlainError(err)
	}
	address := addressOfRun(out)
	return fmt.Sprintf("Started `%s` as `%s` in %s / %s / %s (`%s`). Its %s appears in a moment.",
		line, address, session, space, tab, folder, e.Chat.RoomWord())
}

func hccliRunOpts(folder, spaceID, tabID, space, tab, line string) hccli.RunOpts {
	return hccli.RunOpts{
		Cwd: folder,
		Labels: map[string]string{
			"herdr.space": spaceID,
			"herdr.tab":   tabID,
			"where":       space + " / " + tab,
		},
		Argv: []string{"/bin/zsh", "-lc", "exec " + line},
	}
}

func addressOfRun(out []byte) string {
	var parsed struct {
		Address string `json:"address"`
	}
	if json.Unmarshal(out, &parsed) == nil && parsed.Address != "" {
		return parsed.Address
	}
	return "?"
}

// typeInto：把一段文本送进终端——/history、/screen、/esc、/answer 这些房间
// 命令之外，被对话框挡住时先暂存 ( 永不丢弃 )，正常时 hc send。
func (e *Engine) typeInto(where core.Where, message core.Message, session, address, text string) error {
	record, ok := e.viewOf(session).Get(address)
	if !ok || !record.Live() {
		_, err := e.Chat.Reply(e.ctx, where, EndedReply)
		return err
	}
	reader := e.agentOf(record)
	k := e.keyOf(session, address)
	switch text {
	case "/commands":
		return e.onCommands(session, record)
	case "/history":
		room, has := e.read().Chats[k]
		if reader == nil || !has {
			_, err := e.Chat.Reply(e.ctx, where, "No agent runs here, so there is no conversation to show.")
			return err
		}
		e.backfill(session, record, k, core.Room(room.Chat), room.Name, map[string]bool{"file": true, "folded": true})
		return nil
	case "/screen", "/esc":
		if text == "/esc" {
			if err := e.HC.Keys(e.ctx, session, address, []string{"esc"}); err != nil {
				xlog.For("engine").Warn("Esc 按键失败", "error", err)
			}
			_, err := e.Chat.Reply(e.ctx, where, "Esc pressed.\n\n"+e.screen(session, address, ScreenLines))
			return err
		}
		_, err := e.Chat.Reply(e.ctx, where, e.screen(session, address, ScreenLines))
		return err
	}
	force := false
	typed := text
	if m := answerRe.FindStringSubmatch(text); m != nil {
		typed, force = m[1], true
	}
	// 现在是否被挡住：问 hc 实时状态 ( 事件侧可能慢半拍 )。
	if !force && reader != nil {
		if term, err := e.HC.GetTerminal(e.ctx, session, address); err == nil && (term.State() == stateBlocked || asks.AskOf(term) != "") {
			// 它等着的对话框带 "Type something" 框：你的话直接进框；没有框则暂存，
			// 对话框让开后按序送进，绝不丢弃。
			dialog := e.askedNow(session, address, record)
			if dialog.Menu != nil && dialog.Menu.Free != nil {
				if dialog.Menu.Own != nil {
					if answerer, ok := reader.(readers.Answerer); ok {
						_, _ = answerer.Answer(e.ctx, session, address, dialog, core.Reply{Pick: fmt.Sprint(dialog.Menu.Free.N), Text: text})
						xlog.For("engine").Info("消息作为自由输入作答", "session", session, "address", address)
						e.markWorking(k, message)
						return nil
					}
				} else {
					if err := e.answerFree(session, address, dialog.Menu, text); err != nil {
						return err
					}
					xlog.For("engine").Info("消息作为自由输入作答", "session", session, "address", address)
					e.markWorking(k, message)
					return nil
				}
			}
			e.hold(k, state.WaitingMsg{Where: whereLabel(where), Message: string(message), Session: session, Address: address, Text: text})
			// "上面的卡"必须在：有菜单却没有开着的卡，现在补发 ( again：已发过也发 )。
			if dialog.Menu != nil && !e.openCardExists(k) {
				if err := e.postBlocked(session, address, record, true, false); err != nil {
					xlog.For("engine").Warn("对话框卡片补发失败", "error", err)
				}
			}
			_, err := e.Chat.Reply(e.ctx, where, "It asks something first (the card above). Your message goes in once that is answered.")
			return err
		}
	}
	if err := e.HC.Send(e.ctx, session, address, typed, force); err != nil {
		waits := strings.Contains(err.Error(), stateBlocked)
		note := "Not delivered: " + hccli.PlainError(err)
		if waits {
			note += "\n\n" + e.screen(session, address, ScreenLines) + "\n\nIt waits on a dialog. Reply `/answer <text>` to answer it, or `/esc` to cancel it."
		}
		_, replyErr := e.Chat.Reply(e.ctx, where, note)
		if replyErr != nil {
			xlog.For("engine").Warn("回复失败", "error", replyErr)
		}
		return nil // 已如实告知，不进重试
	}
	e.noteFromChat(k, typed)
	if reader != nil {
		e.noteUnanswered(k, true)
	}
	xlog.For("engine").Info("消息送入终端", "session", session, "address", address, "text", truncateRunes(text, 80))
	if reader == nil {
		// 不是 agent 的终端自己不会说话：回一屏它的屏幕。
		e.sleep(ShellEcho)
		if _, err := e.Chat.Reply(e.ctx, where, e.screen(session, address, ScreenLines)); err != nil {
			return err
		}
		e.markDone(k, 0)
	} else {
		e.markWorking(k, message)
	}
	return nil
}

func whereLabel(where core.Where) string {
	return string(where.Room)
}

// hold：暂存对话框期间的消息 ( 进 state，重启不丢 )。
func (e *Engine) hold(k string, m state.WaitingMsg) {
	st := e.read()
	if st.Waiting == nil {
		st.Waiting = map[string][]state.WaitingMsg{}
	}
	st.Waiting[k] = append(st.Waiting[k], m)
	e.write(st)
}

// sendWaiting：对话框让开了，暂存的消息按序送进终端。
func (e *Engine) sendWaiting(k string) {
	st := e.read()
	held := st.Waiting[k]
	if len(held) == 0 {
		return
	}
	delete(st.Waiting, k)
	e.write(st)
	for _, m := range held {
		_ = e.lanes.Do(k, func() error {
			return e.typeInto(core.Where{Room: core.Room(m.Where)}, core.Message(m.Message), m.Session, m.Address, m.Text)
		})
	}
}

// extOf 按魔数猜图片的扩展名 ( 默认 png )。
func extOf(head []byte) string {
	switch {
	case len(head) >= 4 && head[0] == 0x89 && string(head[1:4]) == "PNG":
		return "png"
	case len(head) >= 2 && head[0] == 0xff && head[1] == 0xd8:
		return "jpg"
	case len(head) >= 4 && string(head[:4]) == "GIF8":
		return "gif"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "webp"
	}
	return "png"
}

// imagesInto：你发到房间的图片——落地、`hc put` 到终端旁边 ( 它的机器上 )，
// 再把路径随文字一起输入 ( Claude Code 这类 agent 会自己打开 )。
func (e *Engine) imagesInto(message core.Message, session, address string, refs [][]byte) []string {
	if len(refs) == 0 || e.Inbox == "" {
		return nil
	}
	if err := os.MkdirAll(e.Inbox, 0o700); err != nil {
		xlog.For("engine").Warn("图片收件目录创建失败", "error", err)
		return nil
	}
	var landed []string
	for i, ref := range refs {
		raw := filepath.Join(e.Inbox, fmt.Sprintf("%s-%d", sanitizeName(string(message)), i))
		if err := os.WriteFile(raw, ref, 0o600); err != nil {
			xlog.For("engine").Warn("图片落地失败", "error", err)
			continue
		}
		head := ref
		if len(head) > 12 {
			head = head[:12]
		}
		ext := extOf(head)
		file := raw + "." + ext
		if err := os.Rename(raw, file); err != nil {
			continue
		}
		remote, err := e.HC.PutFile(e.ctx, session, address, file)
		_ = os.Remove(file) // 终端那边有自己的副本了
		if err != nil {
			xlog.For("engine").Warn("图片送往终端失败", "error", err)
			continue
		}
		landed = append(landed, remote)
	}
	return landed
}

func randHex() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "0"
	}
	return hex.EncodeToString(b)
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
