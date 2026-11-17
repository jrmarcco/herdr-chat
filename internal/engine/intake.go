package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/asks"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/readers"
	"github.com/jrmarcco/herdr-chat/internal/state"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// 来自聊天侧的入口。
var answerRe = regexp.MustCompile(`^/answer\s+([\s\S]+)$`)

// waitHintRe 是确认类对话框底部的提示行 ( "Enter to confirm · Esc to cancel"、
// Press enter to continue、↑/↓ Navigate · enter Confirm )。
var waitHintRe = regexp.MustCompile(`(?i)enter[^\n]{0,40}(confirm|continue)|navigate[^\n]{0,20}enter`)

// cursorSelects：光标 ( ❯ › > ) 选中了一个非空选项，空闲提示符的光标行是空的，不会命中。
var cursorSelectsRe = regexp.MustCompile(`(?m)^\s*[❯›>]\s*\S`)

// screenWaitsConfirm 屏幕上有一个等确认的对话框 ( 信任确认这类，hc 不报 blocked 的 )。
// 判据来自 testdata/screens 的真实语料：尾部的确认提示行 + ( 编号菜单 | 光标选中的选项 )，两条都要有，宁漏勿拦。
func screenWaitsConfirm(screen string) bool {
	lines := strings.Split(screen, "\n")
	if n := len(lines); n > 8 {
		lines = lines[n-8:]
	}
	tail := strings.Join(lines, "\n")
	if !waitHintRe.MatchString(tail) {
		return false
	}
	return core.MenuOf(screen) != nil || cursorSelectsRe.MatchString(screen)
}

// onMessage 直聊是命令 ( /ls、/new、/kill 与帮助 )；房间里的话送进终端。
func (e *Engine) onMessage(in core.Incoming) {
	if e.ctx == nil || e.ctx.Err() != nil {
		return
	}
	if in.Room == "" && !e.Chat.Ready() {
		// 没有 owner：只认配对码。
		e.tryPairing(in)
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

// direct 直聊的命令。
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
		if said := e.newTerminal(args, where); said != "" {
			reply(said)
		}
	case "/clean":
		if said := e.cleanEnded(args, where); said != "" {
			reply(said)
		}
	case "/sessions":
		if said := e.sessionsCmd(args); said != "" {
			reply(said)
		}
	case "/kill":
		if len(args) == 0 {
			if said := e.killTerminalCard(where); said != "" {
				reply(said)
			}
			return
		}
		if len(args) < 2 || !e.knownSession(args[0]) {
			reply("give `/kill` (a card asks), or `/kill <session> <address>`")
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

// help 直聊的帮助文本。
func (e *Engine) help() string {
	lines := []string{
		"`/ls` a new overview of every terminal (the overview also keeps itself up to date)",
		"`/new` a card asks the folder, the place (`<space>/<tab>`, optional) and the agent; `/new <folder> [<session>] [<space>/<tab>] [command]` starts one right away (default `claude`; the place and everything after it can be left out)",
		"`/kill` a card picks the terminal to end (or `/kill <session> <address>` by name)",
		"`/clean` clean up ended terminals' groups and stale screen files (asks first)",
		"`/sessions` the sessions on this machine; `/sessions add|drop <name>` what the bot follows (it starts again to apply)",
		fmt.Sprintf("In a terminal's %s: type to talk to it; `/commands` (what it can do here), `/screen`, `/answer <text>`, `/enter`, `/esc`, `/history` (the conversation so far, as a file).", e.Chat.RoomWord()),
	}
	return strings.Join(lines, "\n")
}

// killKey 是 /kill 终端卡的对话框 key ( onAction 据此拦截，不走终端 )。
const killKey = "kill-terminal"

// killTerminalCard 裸 /kill 的终端卡下拉点名要结束的终端 ( 显示房间名，值回传 session|address )，
// 提交即结束。
//
// /kill 与文字形态一样不做二次确认：
// 选择 + Kill 两个动作已比打字郑重，且终端随时 /new 再起。
func (e *Engine) killTerminalCard(where core.Where) string {
	var options []core.SelectOption
	for _, session := range e.Sessions {
		if v := e.viewOf(session); v != nil {
			for _, record := range v.All() {
				if !record.Live() || !whereShown(record) || !e.handles(session, record.Address()) {
					continue
				}
				space, tab := shownWhere(record)
				options = append(options, core.SelectOption{
					Label: e.roomName(space, tab, record) + " (" + record.Address() + ")",
					Value: session + "|" + record.Address(),
				})
			}
		}
	}
	if len(options) == 0 {
		return "Nothing to end: no live terminal is shown."
	}
	dialog := core.Dialog{
		Submit:    "End",
		Title:     "🔚 End a terminal",
		Markdown:  "Which one should end? Its group stays until `/clean`.",
		DoneTitle: DoneTitle,
		Menu:      &core.Menu{Key: killKey},
		Selects:   []core.Select{{Name: "terminal", Options: options}},
	}
	if _, err := e.Chat.PostDialog(e.ctx, where.Room, dialog, map[string]any{"k": killKey}); err != nil {
		xlog.For("engine").Warn("kill 卡发送失败", "error", err)
		return "Not sent: " + hccli.PlainError(err) + "\n`/kill <session> <address>` ends one without the card."
	}
	return "" // 卡已发，不再回文字
}

// onKillCard /kill 卡的提交。
// 值是 session|address；终端可能已结束 ( 卡躺了一阵 )，如实告知。
func (e *Engine) onKillCard(click core.Click) (core.ActionResult, error) {
	v := valueOf(click)
	if v.Pick == pickEsc {
		return answeredToast("Cancelled. Nothing was ended."), nil
	}
	pick := strings.TrimSpace(click.Form["terminal"])
	session, address, ok := strings.Cut(pick, "|")
	if !ok || !e.knownSession(session) {
		return core.ActionResult{Toast: "The card's terminal is unknown; send /kill again."}, nil
	}
	if record, live := e.viewOf(session).Get(address); !live || !record.Live() {
		return answeredToast("The terminal has ended already."), nil
	}
	if err := e.HC.Kill(e.ctx, session, address); err != nil {
		return core.ActionResult{Toast: "Not ended: " + hccli.PlainError(err)}, nil
	}
	done := fmt.Sprintf("Ended `%s` in %s. Its group stays until `/clean`.", address, session)
	return core.ActionResult{
		Toast: "✔ " + done,
		Card:  &core.Look{Title: DoneTitle, Tone: core.ToneDone, Markdown: done},
	}, nil
}

// sessionsCmd /sessions 本机 hc session 一览与跟随范围。
// 聊天只管 bot 的视野：add 纳入一个本机已存在的 session，drop 移出；
// 造 core 属于 CLI/技能包的事，这里不做。
func (e *Engine) sessionsCmd(args []string) string {
	if len(args) == 0 {
		return e.sessionsList()
	}
	if len(args) != 2 || (args[0] != "add" && args[0] != "drop") {
		return "usage: `/sessions` · `/sessions add <name>` · `/sessions drop <name>`"
	}
	return e.sessionFollow(args[1], args[0] == "add")
}

// sessionsList 每台 core 的死活、是否跟随、终端数。
func (e *Engine) sessionsList() string {
	here := hccli.SessionsHere(e.Env, e.homeDirOr())
	if len(here) == 0 {
		return "no hc session on this machine (`hc server start` makes the default one)"
	}
	var lines []string
	for _, s := range here {
		state := "core down"
		if hccli.CoreUp(context.Background(), e.Env, e.homeDirOr(), s, time.Second) {
			state = "core up"
		}
		followed := "not followed — `/sessions add " + s + "`"
		if slices.Contains(e.Sessions, s) {
			n := 0
			if v := e.viewOf(s); v != nil {
				n = len(v.All())
			}
			followed = fmt.Sprintf("followed · %d terminal(s)", n)
		}
		lines = append(lines, "- `"+s+"` · "+state+" · "+followed)
	}
	return "sessions on this machine:\n" + strings.Join(lines, "\n")
}

// sessionFollow：纳入 / 移出一个本机 session 的跟随，写入设置后重启生效。
func (e *Engine) sessionFollow(name string, follow bool) string {
	if follow && !slices.Contains(hccli.SessionsHere(e.Env, e.homeDirOr()), name) {
		return fmt.Sprintf("session %q is not on this machine (`ls ~/.local/state/herdr-core` lists them)", name)
	}
	var next []string
	for _, s := range e.Sessions {
		if !follow && s == name {
			continue
		}
		next = append(next, s)
	}
	if follow && !slices.Contains(next, name) {
		next = append(next, name)
	}
	if slices.Equal(next, e.Sessions) {
		if follow {
			return "already following `" + name + "`"
		}
		return "not following `" + name + "`"
	}
	if len(next) == 0 {
		return "the bot must follow at least one session"
	}
	if !follow {
		// 活着的终端先处理干净：它们的房间还指着这个 session。
		if v := e.viewOf(name); v != nil {
			for _, t := range v.All() {
				if t.Live() {
					return fmt.Sprintf("`%s` still has a live terminal (%s): /kill it (or wait for it) first", name, t.Address())
				}
			}
		}
	}
	if e.SetSessions == nil {
		return "cannot save it here (the setting writer is not wired)"
	}
	if err := e.SetSessions(next); err != nil {
		return "Not saved: " + err.Error()
	}

	// 进程内同步：重复 add 幂等，重启后配置本就是同一份。
	e.Sessions = next
	if e.Restart != nil {
		go func() {
			e.sleep(RestartWait)
			e.Restart()
		}()
	}
	if follow {
		return "Following `" + name + "` now — it starts again in a moment; each of its terminals gets a group."
	}
	return "Not following `" + name + "` anymore — it starts again in a moment; its groups stay until `/clean`."
}

// newKey 是 /new 开终端卡的对话框 key ( onAction 据此拦截，不走终端 )。
const newKey = "new-terminal"

// /new 可选的 agent 命令。
const (
	cmdClaude   = "claude"
	cmdCodex    = "codex"
	cmdOpencode = "opencode"
)

// agentCommands：reader 的 app 名 → 启动命令 ( /new 卡的下拉与提交校验共用 )。
var agentCommands = map[string]string{
	"claude-code": cmdClaude,
	"codex":       cmdCodex,
	"opencode":    cmdOpencode,
}

// agentOptions：开终端卡下拉里的命令，claude 永远在列 ( 兜底 )。
func (e *Engine) agentOptions() []string {
	out := []string{cmdClaude}
	seen := map[string]bool{cmdClaude: true}
	for _, app := range e.Readers.Apps() {
		if cmd, ok := agentCommands[app]; ok && !seen[cmd] {
			out = append(out, cmd)
			seen[cmd] = true
		}
	}
	return out
}

// startRequest：一次开终端请求的已解析形态。
type startRequest struct {
	session   string // "" → 第一个 session
	space     string // space 名 ( "" → 最近终端的 space，没有则默认 )
	tabName   string // "" → 目录名做 tab 名 ( 同 space 重名自动 -2 )
	folder    string // home 下的相对路径 ( 用户输入 )
	folderAbs string // 已解析的绝对路径 ( 兄弟终端承继时用 )
	command   string // "" → claude
}

// newTerminal：/new 的三个入口。
//
//	/new                          → 一张卡：目录输入 + agent 下拉，一次提交开终端；
//	/new <folder> [command…]      → 默认 session、space/tab 自动 ( tab 名取目录名 )；
//	/new <session> <space>/<tab> [command…]
//	                              → 旧语法；--cwd 已移除，目录承自同 space 兄弟终端。
//
// ( 登录 shell + exec，免得留下一个光壳 shell。 )
func (e *Engine) newTerminal(args []string, where core.Where) string {
	if len(args) == 0 {
		return e.newTerminalCard(where)
	}
	if len(args) >= 2 && e.knownSession(args[0]) && strings.Contains(args[1], "/") {
		return e.newTerminalNamed(args)
	}
	// 目录之后：是已知 session 的 token 收作 session，带 / 的收作去处，
	// 其余全是命令 /new <folder> [<session>] [<space>/<tab>] [command] 一套语法通吃。
	req := startRequest{folder: args[0]}
	rest := args[1:]
	if len(rest) > 0 && e.knownSession(rest[0]) {
		req.session, rest = rest[0], rest[1:]
	}
	if len(rest) > 0 && strings.Contains(rest[0], "/") {
		space, tab, err := splitPlace(rest[0])
		if err != nil {
			return err.Error()
		}
		req.space, req.tabName, rest = space, tab, rest[1:]
	}
	req.command = strings.Join(rest, " ")
	msg, _ := e.startTerminal(req)
	return msg
}

// splitPlace 拆一个 "<space>/<tab>"；两头都得有字，
// 两侧空格收掉 ( 卡上照着显示的样子打 "dev / jit" 是常态 )。
func splitPlace(place string) (space, tab string, err error) {
	slash := strings.Index(place, "/")
	if slash < 1 || slash == len(place)-1 {
		return "", "", fmt.Errorf("give the place as <space>/<tab>, not `%s`", place)
	}
	return strings.TrimSpace(place[:slash]), strings.TrimSpace(place[slash+1:]), nil
}

// firstSession：跟随的第一个 session ( 单 session 卡面点名用 )。
func (e *Engine) firstSession() string {
	if len(e.Sessions) > 0 {
		return e.Sessions[0]
	}
	return "default"
}

// newCardSelects /new 卡的下拉，agent 恒有。
// session 只在跟随多个时出现 ( 单 session 的下拉是噪音 )，
// 且只能选 bot 自己跟随的。
// 不跟随的 session 开出来的终端没有镜像，等于黑洞。
func (e *Engine) newCardSelects() []core.Select {
	selects := []core.Select{{Name: "agent", Options: pickOptions(e.agentOptions())}}
	if len(e.Sessions) > 1 {
		selects = append(selects, core.Select{Name: "session", Options: pickOptions(e.Sessions)})
	}
	return selects
}

// pickOptions 显示即值的选项串 ( /new 的 agent 与 session )。
func pickOptions(values []string) []core.SelectOption {
	out := make([]core.SelectOption, 0, len(values))
	for _, v := range values {
		out = append(out, core.SelectOption{Label: v, Value: v})
	}
	return out
}

// onNewCard：/new 卡的提交。目录输入与 agent 下拉同一张表单，一次带齐；
// 校验失败只回 toast ( 卡留着、输入还在，改一下再提交 )。
func (e *Engine) onNewCard(click core.Click, v dialogValueParts) (core.ActionResult, error) {
	if v.Pick == pickEsc {
		return answeredToast("Cancelled. Nothing was started."), nil
	}
	folder := strings.TrimSpace(click.Answer)
	if folder == "" {
		return core.ActionResult{Toast: "Type the folder first."}, nil
	}
	command := cmdClaude
	if agent := click.Form["agent"]; slices.Contains(e.agentOptions(), agent) {
		command = agent
	}
	req := startRequest{folder: folder, command: command}
	if place := strings.TrimSpace(click.Form["place"]); place != "" {
		space, tab, err := splitPlace(place)
		if err != nil {
			//nolint:nilerr // 已如实告知，不进重试——卡不动：输入还在。
			return core.ActionResult{Toast: err.Error()}, nil
		}
		req.space, req.tabName = space, tab
	}
	if session := click.Form["session"]; session != "" {
		if !e.knownSession(session) {
			return core.ActionResult{Toast: "unknown session " + session + "; known: " + strings.Join(e.Sessions, ", ")}, nil
		}
		req.session = session
	}
	msg, ok := e.startTerminal(req)
	if !ok {
		return core.ActionResult{Toast: msg}, nil
	}
	return core.ActionResult{
		Toast: "✔ " + msg,
		Card:  &core.Look{Title: DoneTitle, Tone: core.ToneDone, Markdown: msg},
	}, nil
}

// newTerminalCard /new 的开终端卡目录输入与 agent 下拉同在一张表单：
// 按钮点击不携带输入内容、只有表单提交带，两者要一次带走就得同表单。
func (e *Engine) newTerminalCard(where core.Where) string {
	markdown := "Which folder should it work in? Under your home, relative (`Projects/foo`)."
	if len(e.Sessions) < 2 {
		// 单 session 没有下拉：卡面点名它，免得"会开到哪个 core"没有落点。
		markdown += "\nSession: " + e.firstSession() + "."
	}
	dialog := core.Dialog{
		Title:     "🆕 New terminal",
		Markdown:  markdown,
		DoneTitle: DoneTitle,
		Menu: &core.Menu{
			Key:  newKey,
			Free: &core.MenuOption{N: 1, Label: "Working folder, e.g. Projects/foo"},
		},
		Place:   "Where to show it: <space>/<tab> — empty keeps the default",
		Selects: e.newCardSelects(),
	}
	if _, err := e.Chat.PostDialog(e.ctx, where.Room, dialog, map[string]any{"k": newKey}); err != nil {
		xlog.For("engine").Warn("new 卡发送失败", "error", err)
		return "Not sent: " + hccli.PlainError(err) + "\n`/new <folder> [command]` starts one without the card."
	}

	// 卡已发，不再回文字。
	return ""
}

// newTerminalNamed 旧语法 session 与 space/tab 点名。
func (e *Engine) newTerminalNamed(args []string) string {
	session, place := args[0], args[1]
	req := startRequest{session: session, command: strings.Join(args[2:], " ")}
	var err error
	if req.space, req.tabName, err = splitPlace(place); err != nil {
		return err.Error()
	}

	// 目录承自同 space 的兄弟终端 ( --cwd 已移除；只承 home 之内的 )。
	home := e.homeDirOr()
	if v := e.viewOf(session); v != nil {
		for _, t := range v.All() {
			if s, _, ok := hccli.SplitWhere(t.Where()); ok && s == req.space {
				if fg, _ := t["foreground"].(map[string]any); fg != nil {
					if c, _ := fg["cwd"].(string); c != "" && c != home && strings.HasPrefix(c, home+string(filepath.Separator)) {
						req.folderAbs = c
					}
				}
				break
			}
		}
	}
	if req.folderAbs == "" {
		return fmt.Sprintf("no terminal in space %q to take a folder from; `/new <folder>` starts one (it asks)", req.space)
	}
	msg, _ := e.startTerminal(req)
	return msg
}

// startTerminal 解析默认值并开终端，返回 ( 说给用户的话, 是否开成了 )。
func (e *Engine) startTerminal(req startRequest) (string, bool) {
	session := req.session
	if session == "" {
		if len(e.Sessions) > 0 {
			session = e.Sessions[0]
		} else {
			session = "default"
		}
	}
	// 工作目录只认 $HOME 之下的相对路径：
	// $HOME 本身太宽 ( 一个终端会把整个 home 当仓库，agent 的信任确认也因此每次都来 )，
	// 越出 $HOME 的相对路径 ( ../ ) 同样不收。
	folder := req.folderAbs
	if folder == "" {
		abs, err := e.resolveFolder(req.folder)
		if err != nil {
			return err.Error(), false
		}
		folder = abs
	}

	// space 与 space id：点名的 space 找兄弟；没点名就跟最近起的那台同一个，
	// 再没有就用默认 ( HERDRCHAT_SPACE )。
	space, spaceID := req.space, ""
	if v := e.viewOf(session); v != nil {
		newest := ""
		for _, t := range v.All() {
			s, _, ok := hccli.SplitWhere(t.Where())
			if !ok || t.Label("herdr.space") == "" {
				continue
			}
			if req.space != "" {
				// 点名的 space：它的兄弟给出 space id。
				if s == req.space {
					space, spaceID = s, t.Label("herdr.space")
					break
				}
				continue
			}
			// 没点名：跟最近起的那台同一个 space。
			created := fmt.Sprint(t["created_ms"])
			if newest == "" || created > newest {
				newest = created
				space, spaceID = s, t.Label("herdr.space")
			}
		}
	}
	if space == "" {
		space = e.spaceDefault()
	}
	if spaceID == "" {
		spaceID = "sp" + randHex()
	}
	tabName := req.tabName
	if tabName == "" {
		tabName = e.freeTabName(space, filepath.Base(strings.TrimRight(req.folder, "/")))
	}
	line := req.command
	if line == "" {
		line = cmdClaude
	}
	out, err := e.HC.RunTerminal(e.ctx, session, hccliRunOpts(folder, spaceID, "tb"+randHex(), space, tabName, line))
	if err != nil {
		return "Not started: " + hccli.PlainError(err), false
	}
	return fmt.Sprintf("Started `%s` as `%s` in %s / %s / %s (`%s`). Its %s appears in a moment.",
		line, addressOfRun(out), session, space, tabName, folder, e.Chat.RoomWord()), true
}

// resolveFolder home 下的相对路径 → 绝对路径；越界的拒绝。
func (e *Engine) resolveFolder(rel string) (string, error) {
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "~") {
		return "", fmt.Errorf("give a folder under your home (relative), not `%s`", rel)
	}
	home := e.homeDirOr()
	folder := filepath.Join(home, rel)
	if folder == home || !strings.HasPrefix(folder, home+string(filepath.Separator)) {
		return "", fmt.Errorf("`%s` is not a folder under your home: a terminal cannot work in $HOME itself", rel)
	}
	return folder, nil
}

// spaceDefault /new 的默认 space 名 ( HERDRCHAT_SPACE；未注入时 dev )。
func (e *Engine) spaceDefault() string {
	if e.DefaultSpace != "" {
		return e.DefaultSpace
	}
	return "dev"
}

// freeTabName 目录名做 tab 名；同 space 已有同名 tab 就 -2、-3 排下去。
func (e *Engine) freeTabName(space, base string) string {
	if base == "" || base == "." || base == "/" {
		base = "term"
	}
	taken := map[string]bool{}
	for _, session := range e.Sessions {
		if v := e.viewOf(session); v != nil {
			for _, t := range v.All() {
				if s, tab, ok := hccli.SplitWhere(t.Where()); ok && s == space {
					taken[tab] = true
				}
			}
		}
	}
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
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

// typeInto 把一段文本送进终端 ( /history、/screen、/enter、/esc、/answer 这些房间命令之外 )，
// 被对话框挡住时先暂存 ( 永不丢弃 )，正常时 hc send。
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
	case "/screen", "/esc", "/enter":
		if text != "/screen" {
			key, word := "esc", "Esc"
			if text == "/enter" {
				key, word = "enter", "Enter"
			}
			if err := e.HC.Keys(e.ctx, session, address, []string{key}); err != nil {
				xlog.For("engine").Warn(word+" 按键失败", "error", err)
			}
			_, err := e.Chat.Reply(e.ctx, where, word+" pressed.\n\n"+e.screen(session, address, ScreenLines))
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

	// 发之前看一眼屏幕：
	// 新生终端可能停在它自己的确认上 ( Claude/Codex 的目 // 录信任这一类 )，
	// hc 不报 blocked、菜单常常没有编号，盲发的话文本连着回车一起进去，
	// 恰好确认了高亮的 "No, exit"，终端当场退出。
	// 屏幕在等确认就一个字都不发，
	// 把屏幕发回去指路 ( 在 herdr-app 里选；这里无法替你选 )。
	if !force {
		if screen := e.screen(session, address, ScreenLines); screenWaitsConfirm(screen) {
			_, err := e.Chat.Reply(e.ctx, where, "It waits on its own confirm (trust this folder?) — nothing was typed, so it stays put. Answer it in herdr-app: the ❯ marks the choice, Enter confirms, Esc cancels (and ends it).\n\n```\n"+hccli.TidyScreen(screen, DialogLines)+"\n```")
			return err
		}
	}
	if err := e.HC.Send(e.ctx, session, address, typed, force); err != nil {
		waits := strings.Contains(err.Error(), stateBlocked)
		note := "Not delivered: " + hccli.PlainError(err)
		if waits {
			note += "\n\n" + e.screen(session, address, ScreenLines) + "\n\nIt waits on a dialog. Reply `/answer <text>` to answer it, `/enter` to confirm it, or `/esc` to cancel it."
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

// hold 暂存对话框期间的消息 ( 进 state，重启不丢 )。
func (e *Engine) hold(k string, m state.WaitingMsg) {
	st := e.read()
	if st.Waiting == nil {
		st.Waiting = map[string][]state.WaitingMsg{}
	}
	st.Waiting[k] = append(st.Waiting[k], m)
	e.write(st)
}

// sendWaiting 对话框让开了，暂存的消息按序送进终端。
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

// imagesInto 你发到房间的图片落地、`hc put` 到终端旁边 ( 它的机器上 )，
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

		// 终端那边有自己的副本了。
		_ = os.Remove(file)
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

// homeDirOr 优先用注入的 HomeDir ( 测试隔离 )，退回真实 home。
func (e *Engine) homeDirOr() string {
	if e.HomeDir != "" {
		return e.HomeDir
	}
	return homeDir()
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
