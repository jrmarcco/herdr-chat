package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/asks"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/readers"
	"github.com/jrmarcco/herdr-chat/internal/state"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// 对话框，三条通路按优先级：
// agent 自己上报 ( hook /它的 API )，屏幕上画的编号菜单，无菜单的整屏。
//
// 编号之外还有一种可点：
// 屏幕自己声明了「Enter 前进 · Esc 取消」的确认 ( core.ConfirmMenuOf 合成转述按钮 )；
// 光标选择 ( ❯ ) 屏绝不合成。
// 一个对话框一张卡；
// 点卡片前先复核屏幕还是那个对话框，旧卡永远答不了长得像的新问题。
//
// 采信一屏菜单的节奏：
// blocked 到达的瞬间屏幕可能还没画完，要顶边框、且相隔DRAW_WAIT 的两次读到同一个 key，
// 或读满 DRAW_TRIES 次才信。

const (
	DrawTries   = 5
	DrawWait    = 400 * time.Millisecond
	KeySettle   = 700 * time.Millisecond // 按键后等它画下一步
	ClickBudget = 2 * time.Second        // 慢机器：超了就先回 toast，卡片异步补
	BoxGap      = 120 * time.Millisecond // 多选题逐项按键的间隔
	DialogLines = 15                     // 无菜单对话框展示的屏尾行数
)

// escapeMarkdown 把屏幕原文放进卡片时转义 markdown 记号。
var mdEscape = regexp.MustCompile(`[\\` + "`" + `*_\[\]#>~|]`)

func escapeMarkdown(text string) string {
	return mdEscape.ReplaceAllStringFunc(text, func(b string) string { return `\` + b })
}

// dialogCards 记住每张卡展示了什么 ( 点击按 message 找回 )。
func (e *Engine) remember(message core.Message, dialog core.Dialog) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dialogCards == nil {
		e.dialogCards = map[core.Message]core.Dialog{}
	}
	e.dialogCards[message] = dialog
	if len(e.dialogCards) > 200 {
		for k := range e.dialogCards {
			delete(e.dialogCards, k)
			break
		}
	}
}

func (e *Engine) dialogAt(message core.Message) (core.Dialog, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.dialogCards[message]
	return d, ok
}

// dialogName 一个对话框的名字，有菜单用菜单 key，没有用状态词。
// 状态词也可能全空 ( hc 没说 kind/msg 的提问 )：
// 兜底一个固定的词，绝不能是空串，那会与 postedDialog 的零值 ( 从未发过 ) 撞车，
// 第一张卡就被"已发过"拦下。
// postedDialog 靠它一题一卡；
// 发卡失败要忘记，让下一个事件 ( 或暂存的消息 ) 再发。
func dialogName(dialog core.Dialog, status hccli.Terminal) string {
	if dialog.Menu != nil {
		return dialog.Menu.Key
	}
	kind, _ := status["status"].(map[string]any)
	words := []string{}
	if kind != nil {
		for _, k := range []string{"kind", "msg"} {
			if v, _ := kind[k].(string); v != "" {
				words = append(words, v)
			}
		}
	}
	if len(words) == 0 {
		return "dialog"
	}
	return strings.Join(words, "|")
}

func (e *Engine) claimDialog(k string, dialog core.Dialog, status hccli.Terminal) bool {
	name := dialogName(dialog, status)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.postedDialog[k] == name {
		return false
	}
	e.postedDialog[k] = name
	return true
}

func (e *Engine) forgetDialog(k string, name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.postedDialog[k] == name {
		delete(e.postedDialog, k)
	}
}

// openCard 是每个终端仍在等的卡
// ( state.openCards 持久化：重启后不重发、也不留一张没人认的按钮卡 )。
func (e *Engine) openCardOf(k string) (state.OpenCard, bool) {
	oc, ok := e.read().OpenCards[k]
	return oc, ok
}

func (e *Engine) setOpenCard(k string, message core.Message, dialog core.Dialog) {
	st := e.read()
	if st.OpenCards == nil {
		st.OpenCards = map[string]state.OpenCard{}
	}
	raw, err := json.Marshal(dialog)
	if err != nil {
		raw = nil
	}
	st.OpenCards[k] = state.OpenCard{Message: string(message), Dialog: raw}
	e.write(st)
}

func (e *Engine) dropOpenCard(k string) {
	st := e.read()
	if st.OpenCards == nil {
		return
	}
	if _, ok := st.OpenCards[k]; !ok {
		return
	}
	delete(st.OpenCards, k)
	e.write(st)
}

// restoreDialogs 在启动时把 state 里的 openCards 请回内存
// ( postedDialog 也随之恢复，已答过的卡不会重发 )。
func (e *Engine) restoreDialogs() {
	for k, oc := range e.read().OpenCards {
		var dialog core.Dialog
		if json.Unmarshal(oc.Dialog, &dialog) == nil {
			e.remember(core.Message(oc.Message), dialog)
			e.mu.Lock()
			e.postedDialog[k] = dialogName(dialog, nil)
			e.mu.Unlock()
		}
	}
}

// closeCard 把等待中的卡去按钮、写上结论。
func (e *Engine) closeCard(k string, done string) {
	oc, ok := e.openCardOf(k)
	if !ok {
		return
	}
	e.dropOpenCard(k)

	var dialog core.Dialog
	_ = json.Unmarshal(oc.Dialog, &dialog)
	if err := e.Chat.CloseDialog(e.ctx, core.Message(oc.Message), dialog, done); err != nil {
		xlog.For("engine").Warn("对话框卡片关闭失败", "message", oc.Message, "error", err)
	}
}

// dialogOf 从一屏文字做出对话框，编号菜单照常。
// 无编号但屏幕自己声明了 Enter/Esc 的确认合成转述按钮 ( 正文仍是屏幕原文 )；
// 再没有菜单时正文是屏幕最后几行。
func dialogOf(statusWord string, screenText, footer string) core.Dialog {
	menu := core.MenuWithConfirm(screenText)
	title := NeedsTitle
	if statusWord != "" {
		title = NeedsTitle + " (" + statusWord + ")"
	}

	d := core.Dialog{
		Menu:      menu,
		Title:     title,
		DoneTitle: DoneTitle,
		Footer:    footer,
	}
	switch {
	case menu == nil || menu.Keys != nil:
		d.Markdown = "```\n" + hccli.TidyScreen(screenText, DialogLines) + "\n```"
	default:
		d.Markdown = escapeMarkdown(menu.Text)
	}
	return d
}

// agentDialog 把 agent 自己上报的提问做成对话框；
// Menu.Own 留它的引用，点击沿原路回答。
func agentDialog(d *core.AgentDialog, status hccli.Terminal) core.Dialog {
	menu := &core.Menu{Key: d.Key, Multi: d.Multi, Text: d.Text, Framed: true, Cursor: 1, Own: &asks.Ref{ID: d.Key, Raw: d}}
	for _, o := range d.Options {
		menu.Options = append(menu.Options, core.MenuOption(o))
		if d.Free != 0 && o.N == d.Free {
			free := menu.Options[len(menu.Options)-1]
			menu.Free = &free
		}
	}

	kind := d.Kind
	if kind == "" {
		if s, _ := status["status"].(map[string]any); s != nil {
			kind, _ = s["kind"].(string)
		}
		if kind == "" {
			kind = "question"
		}
	}

	title := NeedsTitle + " (" + kind + ")"
	if d.Title != "" {
		title = NeedsTitle + " (" + d.Title + ")"
	}
	return core.Dialog{Menu: menu, Title: title, Markdown: escapeMarkdown(d.Text), DoneTitle: DoneTitle}
}

// askedNow 终端现在在等什么 agent 自己说优先 ( 它屏幕的重画打不断 )，
// 否则读屏。
func (e *Engine) askedNow(session, address string, record hccli.Terminal) core.Dialog {
	if reader := e.agentOf(record); reader != nil {
		if dialoger, ok := reader.(readers.Dialoger); ok {
			d, err := dialoger.Dialog(e.ctx, session, address)
			if err != nil {
				xlog.For("engine").Warn("读取 agent 对话框失败", "session", session, "address", address, "error", err)
			}
			if d != nil {
				return agentDialog(d, record)
			}
		}
	}
	screenText, _ := e.HC.ScreenTail(e.ctx, session, address, ScreenLines)
	return dialogOf(record.State(), screenText, "")
}

// dialogValue 是卡片的值：
// 点击凭它找回终端与对话框 ( c 存当时的终端宽度，resize 与换对话框分得开 )。
func dialogValue(session, address string, dialog core.Dialog, cols int) core.DialogValue {
	v := map[string]any{"s": session, "a": address}
	if dialog.Menu != nil {
		v["k"] = dialog.Menu.Key
	}
	if cols > 0 {
		v["c"] = cols
	}
	return v
}

type dialogValueParts struct {
	S     string
	A     string
	K     string
	Label string
	Pick  string
	Ticks map[string]bool
	C     int
}

// valueOf 拆开卡片的值与点击本身 ( pick/label/ticks/answer 由聊天侧拼进 value )。
func valueOf(click core.Click) dialogValueParts {
	out := dialogValueParts{Ticks: map[string]bool{}}
	if m, ok := click.Value.(map[string]any); ok {
		out.S, _ = m["s"].(string)
		out.A, _ = m["a"].(string)
		out.K, _ = m["k"].(string)
		if c, ok := m["c"].(float64); ok {
			out.C = int(c)
		}
		if t, ok := m["ticks"].(map[string]any); ok {
			for k, v := range t {
				if b, _ := v.(bool); b {
					out.Ticks[k] = true
				}
			}
		}
	}

	if pick, ok := click.Value.(map[string]any); ok {
		if p, ok := pick["pick"].(string); ok {
			out.Pick = p
		}
		if p, ok := pick["pick"].(float64); ok {
			out.Pick = fmt.Sprint(int(p))
		}
		if l, ok := pick["label"].(string); ok {
			out.Label = l
		}
	}
	return out
}

// stillAsks 终端是否仍在等这张卡的问题 blocked 着；
// 或它的 hook 还在问 ( Codex 一边干活一边等 hook )；
// 或不挡工作的提问 ( idle ) 还没开工。
func (e *Engine) stillAsks(record hccli.Terminal, dialog core.Dialog) bool {
	if record != nil && record.State() == stateBlocked {
		return true
	}
	if dialog.Menu == nil || dialog.Menu.Own == nil {
		return false
	}
	if ref, ok := dialog.Menu.Own.(*asks.Ref); ok && ref.Raw != nil && ref.Raw.Idle {
		return record != nil && record.State() != "" && record.State() != stateWorking
	}
	return record != nil && asks.AskOf(record) != ""
}

// postBlocked：blocked 时的发卡入口。
//   - own：只信 agent 自己上报的 ( 它的 hook 刚问了 )，不读屏；
//   - again：已发过也再发 ( 你在它等待时往房间打了字 )；
//   - 屏幕通路要顶边框 + 两次读到同一个 key 才采信。
func (e *Engine) postBlocked(session, address string, record hccli.Terminal, again, own bool) error {
	var dialog *core.Dialog
	reader := e.agentOf(record)
	if dialoger, canAsk := reader.(readers.Dialoger); canAsk && reader != nil {
		for i := 0; i < DrawTries; i++ {
			d, err := dialoger.Dialog(e.ctx, session, address)
			if err != nil {
				if errors.Is(err, readers.ErrNoHook) {
					// 这台机器没装 hook：问一百次也不会有，直接读屏。
					break
				}
				xlog.For("engine").Warn("读取 agent 对话框失败", "session", session, "address", address, "error", err)
			}
			if d != nil {
				made := agentDialog(d, record)
				dialog = &made
				break
			}
			if i+1 < DrawTries {
				e.sleep(DrawWait)
			}
		}
	}

	if dialog == nil && own {
		return nil
	}

	if dialog == nil {
		footer := "" // footer ( 屏幕来源标注 ) 随 meter 一起来
		screenText, _ := e.HC.ScreenTail(e.ctx, session, address, ScreenLines)
		current := dialogOf(record.State(), screenText, footer)
		// 两次读到同一个 ( 相隔 DRAW_WAIT )，或两次都没有菜单，才算画完；
		// 最多再读 DRAW_TRIES-1 次。
		for i := 1; i < DrawTries; i++ {
			e.sleep(DrawWait)
			text, _ := e.HC.ScreenTail(e.ctx, session, address, ScreenLines)
			next := dialogOf(record.State(), text, footer)
			steady := (current.Menu == nil && next.Menu == nil) ||
				(current.Menu != nil && current.Menu.Framed && next.Menu != nil && next.Menu.Key == current.Menu.Key)
			current = next
			if steady {
				break
			}
		}
		dialog = &current
	}

	k := e.keyOf(session, address)

	// 读屏期间已在终端里答掉：没人在等，不发卡
	// ( agent 自己上报的除外；Codex 干着活等 hook，状态说了不算 )。
	if dialog.Menu == nil || dialog.Menu.Own == nil {
		if term, err := e.HC.GetTerminal(e.ctx, session, address); err == nil {
			if s := term.State(); s != "" && s != stateBlocked {
				xlog.For("engine").Info("不发卡：它已不再等待", "session", session, "address", address, "state", s)
				return nil
			}
		}
	}

	name := dialogName(*dialog, record)
	if !e.claimDialog(k, *dialog, record) && !again {
		return nil
	}
	if err := e.sendBlocked(session, address, record, k, *dialog); err != nil {
		e.forgetDialog(k, name)
		return err
	}
	options := 0
	if dialog.Menu != nil {
		options = len(dialog.Menu.Options)
	}

	xlog.For("engine").Info("对话框卡片发出", "session", session, "address", address, "options", options)
	return nil
}

// postIdleAsk 不挡工作的提问 ( reader 的 Dialog 标了 Idle )，turn 发完再发卡，
// sweep 里没有卡也补一张。
func (e *Engine) postIdleAsk(session, address string, record hccli.Terminal) {
	reader := e.agentOf(record)
	if reader == nil {
		return
	}
	dialoger, ok := reader.(readers.Dialoger)
	if !ok {
		return
	}
	d, err := dialoger.Dialog(e.ctx, session, address)
	if err != nil || d == nil || !d.Idle {
		return
	}
	k := e.keyOf(session, address)
	dialog := agentDialog(d, record)
	name := dialogName(dialog, record)
	if !e.claimDialog(k, dialog, record) {
		return
	}
	if err := e.sendBlocked(session, address, record, k, dialog); err != nil {
		e.forgetDialog(k, name)
		return
	}
	xlog.For("engine").Info("idle 提问卡片发出", "session", session, "address", address)
}

// sendBlocked 把对话框发出去，有菜单是一张带按钮的卡 ( 置顶 + 响铃 )；
// 没有菜单是一段文字加 /answer /enter /esc 指引。
func (e *Engine) sendBlocked(session, address string, record hccli.Terminal, k string, dialog core.Dialog) error {
	e.closeCard(k, "It moved on; see the next card.")
	if dialog.Menu == nil {
		msg, err := e.postTo(
			session,
			record,
			fmt.Sprintf(
				"**%s**\n\n%s\n\nReply `/answer <text>` to answer it, `/enter` to confirm it, or `/esc` to cancel it.",
				dialog.Title,
				dialog.Markdown,
			),
			dialog.Footer,
		)
		if err != nil {
			return err
		}
		e.pinIfRoom(k, msg)
		_ = e.Chat.Ring(e.ctx, k, msg, "⚠️ needs you")
		return nil
	}

	cols := 0
	if c, ok := record["cols"].(float64); ok {
		cols = int(c)
	}
	message, err := e.deliver(session, record, func(room core.Room) (core.Message, error) {
		return e.Chat.PostDialog(e.ctx, room, dialog, dialogValue(session, address, dialog, cols))
	})
	if err != nil {
		return err
	}
	if message == "" {
		return fmt.Errorf("the room for its card is gone")
	}

	e.remember(message, dialog)
	e.setOpenCard(k, message, dialog)
	e.pinIfRoom(k, message)
	_ = e.Chat.Ring(e.ctx, k, message, "⚠️ needs you")

	// 卡片在路上时已答掉：没人会再关它，现在就关。
	var live hccli.Terminal
	if v := e.viewOf(session); v != nil {
		live, _ = v.Get(address)
	}
	if !e.stillAsks(live, dialog) {
		e.closeCard(k, DoneTitle)
	}
	return nil
}

func (e *Engine) pinIfRoom(k string, msg core.Message) {
	room, ok := e.read().Chats[k]
	if !ok {
		return
	}
	if err := e.Chat.Pin(e.ctx, k, core.Room(room.Chat), msg); err != nil {
		xlog.For("engine").Warn("置顶失败", "k", k, "error", err)
	}
}

// pickKeys 选一个选项要按的键数字；
// 带预览框的问题不吃数字，从光标用方向键走到它，再 Enter。
func pickKeys(menu *core.Menu, option core.MenuOption) []string {
	if !menu.Beside {
		return []string{fmt.Sprint(option.N)}
	}
	moves := option.N - menu.Cursor
	keys := []string{}
	for i := 0; i < moves || i < -moves; i++ {
		if moves > 0 {
			keys = append(keys, "down")
		} else {
			keys = append(keys, "up")
		}
	}
	return append(keys, "enter")
}

// answerFree "Type something" 先选中那一项，再把话打进去。
func (e *Engine) answerFree(session, address string, menu *core.Menu, text string) error {
	if err := e.HC.Keys(e.ctx, session, address, pickKeys(menu, *menu.Free)); err != nil {
		return err
	}
	e.sleep(300 * time.Millisecond)
	return e.HC.Send(e.ctx, session, address, text, true)
}

func answeredToast(done string) core.ActionResult {
	return core.ActionResult{Toast: done, Card: &core.Look{Title: DoneTitle, Tone: core.ToneGood, Markdown: done}}
}

// onAction 聊天里点了一下卡片的按钮。
// 先复核 ( 屏幕还是那张卡的问题、选项没变 ) 才按键；
// 多选在卡上本地勾选，Next 一次性把差异按出去。
func (e *Engine) onActionDialog(click core.Click) (core.ActionResult, error) {
	started := time.Now()
	v := valueOf(click)

	// /clean 的确认卡：不走终端 ( 没有 s/a 对应的终端，点了直接执行或取消 )。
	if v.K == cleanKey {
		if v.Pick == "1" {
			result := e.doClean()
			return core.ActionResult{
				Toast: "✔ Cleaned.",
				Card:  &core.Look{Title: DoneTitle, Tone: core.ToneDone, Markdown: result},
			}, nil
		}
		return answeredToast("Cancelled. Nothing was deleted."), nil
	}

	// /new 的开终端卡：同样不走终端，表单提交 ( 目录 + agent 下拉 ) 直接开。
	if v.K == newKey {
		return e.onNewCard(click, v)
	}

	// /kill 的终端卡：下拉点名，提交即结束。
	if v.K == killKey {
		return e.onKillCard(click)
	}

	shown, known := e.dialogAt(click.Message)
	if !known {
		shown = core.Dialog{Title: NeedsTitle, DoneTitle: DoneTitle}
	}

	// 多选框：只在卡上翻勾，立刻回应；Next 才真正发送。
	if shown.Menu != nil && shown.Menu.Multi && v.Pick != "" && v.Pick != "tab" && v.Pick != "esc" {
		n, _ := strconv.Atoi(v.Pick)
		for i, o := range shown.Menu.Options {
			if o.N == n && o.Checked != nil && (shown.Menu.Free == nil || shown.Menu.Free.N != n) {
				toggledDialog := shown
				menu := *shown.Menu
				menu.Options = append([]core.MenuOption(nil), shown.Menu.Options...)
				flipped := !*o.Checked
				menu.Options[i].Checked = &flipped
				toggledDialog.Menu = &menu
				e.remember(click.Message, toggledDialog)
				mark := "☑"
				if flipped {
					mark = "☐"
				}
				return core.ActionResult{
					Toast: mark + " " + o.Label,
					Card: &core.Look{
						Title:    shown.Title,
						Tone:     core.ToneNeeds,
						Markdown: escapeMarkdown(menu.Text),
					},
				}, nil
			}
		}
	}

	if v.S == "" || v.A == "" {
		return core.ActionResult{Toast: "This card is not a terminal's."}, nil
	}

	record, live := e.viewOf(v.S).Get(v.A)
	if !live || !record.Live() {
		xlog.For("engine").Info("点击未送达：终端已结束", "session", v.S, "address", v.A)
		return answeredToast("The terminal has ended."), nil
	}

	term, err := e.HC.GetTerminal(e.ctx, v.S, v.A)
	if err != nil {
		return core.ActionResult{Toast: core.UserWords(err)}, nil
	}

	k := e.keyOf(v.S, v.A)
	// 通路 A：agent 自己上报的卡，沿它的 API 回答，不碰终端按键。
	if shown.Menu != nil && shown.Menu.Own != nil {
		return e.answerOwn(click, v, record, term, k)
	}

	// 通路 C：读屏复核 ( 编号菜单与合成的确认按钮同一管线，key 才对得上 )。
	screenText, _ := e.HC.ScreenTail(e.ctx, v.S, v.A, ScreenLines)
	var menu *core.Menu
	if term.State() == stateBlocked {
		menu = core.MenuWithConfirm(screenText)
	}
	if menu == nil || menu.Key != v.K {
		xlog.For("engine").Info("点击未按键：对话框已变或无人等待", "session", v.S, "address", v.A, "card", v.K)
		if menu == nil {
			e.closeClickCard(click, shown, "It no longer waits on this.")
			return core.ActionResult{
				Toast: "It no longer waits on this.",
				Card:  doneLook(shown, "It no longer waits on this."),
			}, nil
		}

		// 手里的卡变成屏幕现在问的，请再点一次。
		// 绝不另发新卡：新卡会被 "已发过" 拦下，那就没人可答、你的消息全干等。
		now := dialogOf(term.State(), screenText, shown.Footer)
		if now.Menu == nil {
			e.closeClickCard(click, shown, "It no longer waits on this.")
			return core.ActionResult{
				Toast: "It no longer waits on this.",
				Card:  doneLook(shown, "It no longer waits on this."),
			}, nil
		}
		if oc, ok := e.openCardOf(k); ok && oc.Message != string(click.Message) {
			e.closeCard(k, "It moved on; see the next card.")
		}

		e.mu.Lock()
		e.postedDialog[k] = dialogName(now, term)
		e.mu.Unlock()

		e.remember(click.Message, now)
		e.setOpenCard(k, click.Message, now)
		return core.ActionResult{
			Toast: "It changed before your tap, so nothing was pressed. The card shows it now: tap again.",
			Card:  &core.Look{Title: now.Title, Tone: core.ToneNeeds, Markdown: escapeMarkdown(now.Markdown)},
			Value: dialogValue(v.S, v.A, now, termCols(term)),
		}, nil
	}

	if oc, ok := e.openCardOf(k); ok && oc.Message == string(click.Message) {
		e.dropOpenCard(k)
	}

	var picked string
	switch {
	case v.Pick == pickTab && shown.Menu.Multi:
		// 卡上勾成的那副样子 ( Next 提交，或逐次翻勾攒下的 )，在屏幕上做出来：
		// 与屏幕不同的每一项按一下，再 tab。
		wanted := *shown.Menu
		if len(v.Ticks) > 0 {
			wanted.Options = append([]core.MenuOption(nil), shown.Menu.Options...)
			for i, o := range wanted.Options {
				if o.Checked == nil {
					continue
				}
				on := v.Ticks[fmt.Sprint(o.N)]
				wanted.Options[i].Checked = &on
			}
		}

		for _, o := range wanted.Options {
			if o.Checked == nil {
				continue
			}
			screenHas := false
			for _, m := range menu.Options {
				if m.N == o.N && m.Checked != nil {
					screenHas = *m.Checked
				}
			}
			if *o.Checked != screenHas {
				if kerr := e.HC.Keys(e.ctx, v.S, v.A, []string{fmt.Sprint(o.N)}); kerr != nil {
					return core.ActionResult{Toast: core.UserWords(kerr)}, nil
				}
				e.sleep(BoxGap)
			}
		}

		if kerr := e.HC.Keys(e.ctx, v.S, v.A, []string{"tab"}); kerr != nil {
			return core.ActionResult{Toast: core.UserWords(kerr)}, nil
		}

		var ticked []string
		for _, o := range wanted.Options {
			if o.Checked != nil && *o.Checked {
				ticked = append(ticked, o.Label)
			}
		}

		if len(ticked) == 0 {
			picked = "nothing ticked"
		} else {
			picked = strings.Join(ticked, ", ")
		}
	case v.Pick == pickEsc || v.Pick == pickTab:
		if kerr := e.HC.Keys(e.ctx, v.S, v.A, []string{v.Pick}); kerr != nil {
			return core.ActionResult{Toast: core.UserWords(kerr)}, nil
		}
		if v.Pick == pickEsc {
			picked = "Esc pressed"
		} else {
			picked = "Next"
		}
	default:
		var option *core.MenuOption
		n, _ := strconv.Atoi(v.Pick)
		for i, o := range menu.Options {
			if o.N == n {
				option = &menu.Options[i]
				break
			}
		}

		if option == nil || option.Label != v.Label {
			xlog.For("engine").Info("点击未按键：选项已变", "session", v.S, "address", v.A, "pick", v.Pick)
			return answeredToast("The options changed; nothing was pressed."), nil
		}

		if menu.Free != nil && option.N == menu.Free.N {
			text := strings.TrimSpace(click.Answer)
			if text == "" {
				return core.ActionResult{Toast: "Type your answer first."}, nil
			}
			if kerr := e.answerFree(v.S, v.A, menu, text); kerr != nil {
				return core.ActionResult{Toast: core.UserWords(kerr)}, nil
			}
			picked = text
		} else {
			keys := pickKeys(menu, *option)
			if bound, ok := menu.Keys[option.N]; ok {
				// 合成菜单：按屏幕声明的键 ( enter / esc )，不按数字。
				keys = bound
			}
			if kerr := e.HC.Keys(e.ctx, v.S, v.A, keys); kerr != nil {
				return core.ActionResult{Toast: core.UserWords(kerr)}, nil
			}
			picked = fmt.Sprintf("%d. %s", option.N, option.Label)
		}
	}

	xlog.For("engine").Info("点击已按键", "session", v.S, "address", v.A, "picked", picked)

	// 按完键对话框已不在 ( 答掉了 )：
	// 卡片要收场登记在按键前已摘，"离开 blocked" 的关卡不会再来补这一刀 ( 教训记在这 )。
	closeIfAnswered := func(term hccli.Terminal, err error) {
		if err != nil || term.State() == stateBlocked {
			return
		}
		if err := e.Chat.CloseDialog(e.ctx, click.Message, shown, "✔ "+picked); err != nil {
			xlog.For("engine").Warn("对话框卡片关闭失败", "message", string(click.Message), "error", err)
		}
	}

	// 慢机器：先答点击，下一步的卡异步补。
	if time.Since(started) > ClickBudget {
		e.later(v.S, v.A, "dialog", func() error {
			e.sleep(KeySettle)
			aterm, aerr := e.HC.GetTerminal(e.ctx, v.S, v.A)
			if aerr == nil && aterm.State() == stateBlocked {
				rec, ok := e.viewOf(v.S).Get(v.A)
				if !ok {
					rec = record
				}
				return e.postBlocked(v.S, v.A, rec, false, false)
			}
			closeIfAnswered(aterm, aerr)
			return nil
		})
		return core.ActionResult{Toast: "✔ " + picked, Card: doneLook(shown, "✔ "+picked)}, nil
	}

	e.sleep(KeySettle)
	term, gerr := e.HC.GetTerminal(e.ctx, v.S, v.A)
	if gerr == nil && term.State() == stateBlocked {
		text, _ := e.HC.ScreenTail(e.ctx, v.S, v.A, ScreenLines)
		next := dialogOf(term.State(), text, shown.Footer)
		if next.Menu != nil && (next.Menu.Key == v.K || e.claimDialog(k, next, term)) {
			e.remember(click.Message, next)
			e.setOpenCard(k, click.Message, next)
			return core.ActionResult{
				Toast: "✔ " + picked,
				Card:  &core.Look{Title: next.Title, Tone: core.ToneNeeds, Markdown: escapeMarkdown(next.Markdown)},
				Value: dialogValue(v.S, v.A, next, termCols(term)),
			}, nil
		}
		if next.Menu != nil && e.openCardExists(k) {
			return answeredToast("✔ " + picked), nil
		}
		if next.Menu != nil {
			e.mu.Lock()
			e.postedDialog[k] = dialogName(next, term)
			e.mu.Unlock()
			e.remember(click.Message, next)
			e.setOpenCard(k, click.Message, next)
			return core.ActionResult{
				Toast: "✔ " + picked,
				Card:  &core.Look{Title: next.Title, Tone: core.ToneNeeds, Markdown: escapeMarkdown(next.Markdown)},
				Value: dialogValue(v.S, v.A, next, termCols(term)),
			}, nil
		}

		// 状态还说着 blocked、屏幕已没有菜单：
		// 答案多半已进去，先把这张卡收了 ( 状态字段翻得慢，等它就错过关卡 )；
		// 真还等着，补发会作为新卡。
		e.closeClickCard(click, shown, "✔ "+picked)

		// 还在等，等的东西没有菜单：hc 不会再发 turn，补发。
		rec, ok := e.viewOf(v.S).Get(v.A)
		if !ok {
			rec = record
		}
		e.later(v.S, v.A, "dialog", func() error {
			return e.postBlocked(v.S, v.A, rec, false, false)
		})
	}
	closeIfAnswered(term, gerr)
	return core.ActionResult{Toast: "✔ " + picked, Card: doneLook(shown, "✔ "+picked)}, nil
}

// closeClickCard 点击路径的关卡：手上有卡片自己的 message，不依赖登记
// ( 登记可能在按键前就被摘掉，"离开 blocked"的关卡从此扑空 )。
func (e *Engine) closeClickCard(click core.Click, d core.Dialog, done string) {
	if err := e.Chat.CloseDialog(e.ctx, click.Message, d, done); err != nil {
		xlog.For("engine").Warn("对话框卡片关闭失败", "message", string(click.Message), "error", err)
	}
}

func (e *Engine) openCardExists(k string) bool {
	_, ok := e.openCardOf(k)
	return ok
}

// answerOwn ( 通路 A 的点击 )：先重新问 agent 它在等什么，key 相同才答；
// 它的下一切题接管这张卡的位置。
func (e *Engine) answerOwn(click core.Click, v dialogValueParts, record hccli.Terminal, term hccli.Terminal, k string) (core.ActionResult, error) {
	reader := e.agentOf(record)
	dialoger, ok := reader.(readers.Dialoger)
	if !ok {
		return answeredToast("It no longer waits on this."), nil
	}
	now, err := dialoger.Dialog(e.ctx, v.S, v.A)
	if err != nil || now == nil {
		// 点击失败如实告知，不进重试。
		//nolint:nilerr // err 已折进"没在等"的结论。
		return answeredToast("It no longer waits on this."), nil
	}

	asked := agentDialog(now, term)
	if now.Key != v.K {
		xlog.For("engine").Info("点击未回答：它问的已是另一个问题", "session", v.S, "address", v.A, "now", now.Key)
		if oc, has := e.openCardOf(k); has && oc.Message != string(click.Message) {
			e.closeCard(k, "It moved on; see the next card.")
		}
		e.mu.Lock()
		e.postedDialog[k] = dialogName(asked, term)
		e.mu.Unlock()
		e.remember(click.Message, asked)
		e.setOpenCard(k, click.Message, asked)
		return core.ActionResult{
			Toast: "It asks something else now, so nothing was answered. The card shows it: tap again.",
			Card:  &core.Look{Title: asked.Title, Tone: core.ToneNeeds, Markdown: escapeMarkdown(asked.Markdown)},
			Value: dialogValue(v.S, v.A, asked, termCols(term)),
		}, nil
	}

	var picked string
	var reply core.Reply
	switch {
	case v.Pick == pickEsc:
		reply = core.Reply{Esc: true}
		picked = "Cancelled"
	case v.Pick == pickTab && asked.Menu.Multi:
		ticks := v.Ticks
		if len(ticks) == 0 {
			for _, o := range asked.Menu.Options {
				if o.Checked != nil {
					ticks[fmt.Sprint(o.N)] = *o.Checked
				}
			}
		}
		reply = core.Reply{Ticks: tickList(ticks)}
		var labels []string
		for _, o := range asked.Menu.Options {
			if o.Checked != nil && ticks[fmt.Sprint(o.N)] {
				labels = append(labels, o.Label)
			}
		}
		if len(labels) == 0 {
			picked = "nothing ticked"
		} else {
			picked = strings.Join(labels, ", ")
		}
	default:
		var option *core.AgentOption
		n, _ := strconv.Atoi(v.Pick)
		for i, o := range now.Options {
			if o.N == n {
				option = &now.Options[i]
				break
			}
		}
		if option == nil || option.Label != v.Label {
			return answeredToast("The options changed; nothing was answered."), nil
		}
		if now.Free != 0 && option.N == now.Free {
			text := strings.TrimSpace(click.Answer)
			if text == "" {
				return core.ActionResult{Toast: "Type your answer first."}, nil
			}
			reply = core.Reply{Pick: fmt.Sprint(option.N), Text: text}
			picked = text
		} else {
			reply = core.Reply{Pick: fmt.Sprint(option.N)}
			picked = fmt.Sprintf("%d. %s", option.N, option.Label)
		}
	}

	answerer, ok := reader.(readers.Answerer)
	if !ok {
		return answeredToast("It no longer waits on this."), nil
	}
	took, err := answerer.Answer(e.ctx, v.S, v.A, asked, reply)
	if err != nil || !took {
		xlog.For("engine").Warn("agent 没有收下答案", "session", v.S, "address", v.A, "error", err)
		// 点击失败已如实告知，不进重试。
		return core.ActionResult{Toast: reader.App() + " did not take it; nothing changed. Tap again, or answer in herdr-app."}, nil
	}
	if oc, has := e.openCardOf(k); has && oc.Message == string(click.Message) {
		e.dropOpenCard(k)
		// 卡片本体要收场：去按钮、写结论。
		// 只摘登记不编辑的话，卡永远停在提问的样子 ( 摘了登记，"离开 blocked"的关卡也不会再补这一刀 )。
		if err := e.Chat.CloseDialog(e.ctx, click.Message, asked, "✔ "+picked); err != nil {
			xlog.For("engine").Warn("对话框卡片关闭失败", "message", string(click.Message), "error", err)
		}
	}

	xlog.For("engine").Info("点击已沿 agent API 回答", "session", v.S, "address", v.A, "picked", picked)
	e.sleep(KeySettle)
	// 它的下一题 ( 同一表的，或新的 ) 接管这张卡。
	if next, err := dialoger.Dialog(e.ctx, v.S, v.A); err == nil && next != nil && next.Key != now.Key {
		nd := agentDialog(next, term)
		e.mu.Lock()
		e.postedDialog[k] = dialogName(nd, term)
		e.mu.Unlock()
		e.remember(click.Message, nd)
		e.setOpenCard(k, click.Message, nd)
		return core.ActionResult{
			Toast: "✔ " + picked,
			Card:  &core.Look{Title: nd.Title, Tone: core.ToneNeeds, Markdown: escapeMarkdown(nd.Markdown)},
			Value: dialogValue(v.S, v.A, nd, termCols(term)),
		}, nil
	}

	return core.ActionResult{Toast: "✔ " + picked, Card: doneLook(asked, "✔ "+picked)}, nil
}

// doneLook 是关卡后的样子：灰头、原文、一行结论，
// 随点击的回调响应整卡换上 ( 按钮随之消失；PATCH 换不掉交互组件，只有这条路 )。
func doneLook(d core.Dialog, done string) *core.Look {
	md := d.Markdown
	if md == "" {
		md = done
	} else {
		md += "\n\n" + done
	}
	return &core.Look{Title: DoneTitle, Tone: core.ToneDone, Markdown: md}
}

// tickList 把 {n: on} 变成按编号升序的按键序列 ( map 无序，顺序要自己定 )。
func tickList(ticks map[string]bool) []string {
	var nums []int
	for n, on := range ticks {
		if on {
			i, err := strconv.Atoi(n)
			if err != nil {
				continue
			}
			nums = append(nums, i)
		}
	}
	sort.Ints(nums)
	out := make([]string, 0, len(nums))
	for _, n := range nums {
		out = append(out, strconv.Itoa(n))
	}
	return out
}

// termCols 读终端记录里的宽度 ( resize 与换对话框分得开 )。
func termCols(term hccli.Terminal) int {
	if c, ok := term["cols"].(float64); ok {
		return int(c)
	}
	return 0
}
