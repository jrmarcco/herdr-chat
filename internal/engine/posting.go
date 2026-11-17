package engine

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/readers"
	"github.com/jrmarcco/herdr-chat/internal/state"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// 发帖管线。

// deliver 一次发帖带重试；
// 仍失败则抛出且游标不动，同样的内容随下一帖一起发出去，重复可接受，丢失不可。
func (e *Engine) deliver(session string, record hccli.Terminal, send func(core.Room) (core.Message, error)) (core.Message, error) {
	for attempt := 1; ; attempt++ {
		room, err := e.ensureRoom(session, record)
		if err == nil && room == "" {
			// 没有错也没有房间：这个终端不该有房间 ( 未在 herdr-app 展示、或 tab 还被旧终端占着 )，
			// 重试改变不了它，立刻按无事收场。
			return "", nil
		}
		if err == nil && room != "" {
			msg, sendErr := send(room)
			if sendErr == nil {
				return msg, nil
			}
			err = sendErr
		}
		if attempt >= PostTries {
			return "", err
		}

		xlog.For("engine").Warn("发帖失败，稍后重试", "session", session, "address", record.Address(), "attempt", attempt, "error", err)
		// 1/2/4/8 秒指数退避 ( Quick 模式下由 e.sleep 压扁 )。
		e.sleep(time.Duration(1<<uint(attempt)) * time.Second)
	}
}

// postTo deliver 的最常用形态，把一段文本发进终端的房间。
func (e *Engine) postTo(session string, record hccli.Terminal, text, footer string) (core.Message, error) {
	return e.deliver(session, record, func(room core.Room) (core.Message, error) {
		return e.Chat.Post(e.ctx, room, text, footer)
	})
}

// HC_NOTE：
// 别的终端用 `hc send` 发来的消息会被 hc 包成 <hc from="t7 review" reach="...">...</hc> ( 任何 agent 都一样，是 hc 加的 )，
// 展示为来自那个终端，而不是你；包裹之外的内容才是你的。
var hcNote = regexp.MustCompile(`<hc from="([^"]*)"(?:\s+reach="([^"]*)")?>([\s\S]*?)</hc>`)

func quotedBlock(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// youOrOthers 把一段"你说的话"排版出来，中间夹杂的跨终端留言各自成段。
func youOrOthers(text, at string) string {
	var parts []string
	rest := text
	for _, m := range hcNote.FindAllStringSubmatch(text, -1) {
		note := fmt.Sprintf("**From %s**%s\n%s", m[1], at, quotedBlock(strings.TrimSpace(m[3])))
		if m[2] != "" {
			note = fmt.Sprintf("**From %s**%s _(answer: %s)_\n%s", m[1], at, m[2], quotedBlock(strings.TrimSpace(m[3])))
		}
		parts = append(parts, note)
		rest = strings.Replace(rest, m[0], "", 1)
	}
	rest = strings.TrimSpace(rest)
	if rest != "" {
		prefix := "**You:**"
		if at != "" {
			prefix += " " + strings.TrimPrefix(at, " · ")
		}
		parts = append([]string{prefix + "\n" + quotedBlock(rest)}, parts...)
	}
	return strings.Join(parts, "\n\n")
}

// formatItems 条目变成房间的文字。
// 工具行连续合并；你从聊天里发过的话不再重复出现 ( 房间里本来就有 )。
func (e *Engine) formatItems(k string, items []core.Item) string {
	e.mu.Lock()
	sent := append([]string(nil), e.fromChat[k]...)
	consumed := make([]int, 0, len(items))
	e.mu.Unlock()

	var parts []string
	for _, it := range items {
		switch it.Role {
		case core.RoleTool:
			if len(parts) > 0 && strings.HasPrefix(parts[len(parts)-1], "▸ ") {
				parts[len(parts)-1] += "\n" + it.Text
			} else {
				parts = append(parts, it.Text)
			}
		case core.RoleImage:
			shown := it.Key
			if shown == "" {
				who := "it looked at"
				if it.From == core.RoleYou {
					who = "you pasted"
				}
				shown = fmt.Sprintf("_(an image %s was not sent: %s)_", who, it.Err)
			}
			if it.From == core.RoleYou {
				shown = "**You:**\n" + shown
			}
			parts = append(parts, shown)
		case core.RoleYou:
			found := -1
			for i, s := range sent {
				if s == strings.TrimSpace(it.Text) {
					found = i
					break
				}
			}
			if found >= 0 {
				consumed = append(consumed, found)
				continue
			}
			parts = append(parts, youOrOthers(it.Text, ""))
		case core.RoleAI:
			parts = append(parts, it.Text)
		}
	}

	// 用掉的那些"你发过"的记录随之移除。
	if len(consumed) > 0 {
		e.mu.Lock()
		kept := e.fromChat[k]
		for _, i := range consumed {
			if i < len(kept) {
				kept[i] = "\x00" // 标记后过滤
			}
		}
		next := kept[:0]
		for _, v := range kept {
			if v != "\x00" {
				next = append(next, v)
			}
		}
		e.fromChat[k] = next
		e.mu.Unlock()
	}
	return strings.Join(parts, "\n\n")
}

// noteFromChat 记下你从聊天里发进终端的文本 ( 近 20 条 )。
func (e *Engine) noteFromChat(k, text string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	list := append(e.fromChat[k], strings.TrimSpace(text))
	if len(list) > 20 {
		list = list[len(list)-20:]
	}
	e.fromChat[k] = list
}

func (e *Engine) noteUnanswered(k string, unanswered bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if unanswered {
		e.unanswered[k] = true
	} else {
		delete(e.unanswered, k)
	}
}

func (e *Engine) isUnanswered(k string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.unanswered[k]
}

// newLive 开一张 ( 或多张 ) 活卡片：超长的按平台切段，最后一段保持 working。
func (e *Engine) newLive(session string, record hccli.Terminal, text, footer string) *state.LiveCard {
	parts := e.Chat.Split(text)
	var message core.Message
	for i, part := range parts {
		last := i == len(parts)-1
		look := core.Look{Title: FullTitle, Tone: core.ToneClosed, Markdown: part, Footer: footer}
		if last {
			look.Title, look.Tone = LiveTitle, core.ToneWorking
		}
		msg, err := e.deliver(session, record, func(room core.Room) (core.Message, error) {
			return e.Chat.PostCard(e.ctx, room, look)
		})
		if err != nil {
			xlog.For("engine").Error("活卡片发送失败", "error", err)
			return nil
		}
		message = msg
	}
	return &state.LiveCard{Message: string(message), Text: parts[len(parts)-1], Footer: footer}
}

// addLive 把内容续进活卡片：装不下就封卡另开；编辑失败整卡重发
// ( 重复可以，丢失不行 )。
func (e *Engine) addLive(session string, record hccli.Terminal, live *state.LiveCard, add, footer string) *state.LiveCard {
	if live == nil {
		return e.newLive(session, record, add, footer)
	}
	text := live.Text + "\n\n" + add
	if len(text) > e.Chat.LiveBytes() {
		e.closeLive(live, FullTitle)
		return e.newLive(session, record, add, footer)
	}
	if err := e.Chat.EditCard(e.ctx, core.Message(live.Message), core.Look{
		Title: LiveTitle, Tone: core.ToneWorking, Markdown: text, Footer: footer,
	}); err != nil {
		xlog.For("engine").Error("活卡片编辑失败，整卡重发", "message", live.Message, "error", err)
		return e.newLive(session, record, text, footer)
	}
	return &state.LiveCard{Message: live.Message, Text: text, Footer: footer}
}

func (e *Engine) closeLive(live *state.LiveCard, title string) {
	if live == nil {
		return
	}
	if title == "" {
		title = ClosedTitle
	}
	if err := e.Chat.EditCard(e.ctx, core.Message(live.Message), core.Look{
		Title: title, Tone: core.ToneClosed, Markdown: live.Text, Footer: live.Footer,
	}); err != nil {
		xlog.For("engine").Warn("活卡片关闭失败 ( 内容都在 )", "message", live.Message, "error", err)
	}
}

// uploadImages 图片先进聊天，每张只传一次 ( 按条目 id )，
// 重试的帖子不会把同一张传两遍。同一张连败 PostTries 次后，
// 房间得到一行"没发出及原因"，一张图绝不拖住它后面的所有内容。
func (e *Engine) uploadImages(items []core.Item) error {
	for i := range items {
		it := &items[i]
		if it.Role != core.RoleImage || it.Key != "" || it.Err != "" || it.DataB64 == "" {
			continue
		}

		e.mu.Lock()
		key, uploaded := e.uploaded[it.ID]
		e.mu.Unlock()

		if !uploaded {
			md, err := e.Chat.Image(e.ctx, decodeB64(it.DataB64))
			if err != nil {
				e.mu.Lock()
				e.uploadFail[it.ID]++
				failures := e.uploadFail[it.ID]
				e.mu.Unlock()

				if failures < PostTries {
					return err
				}
				it.Err = err.Error()
				xlog.For("engine").Error("图片多次发送失败，跳过", "id", it.ID, "tries", failures, "error", err)
				continue
			}

			key = md
			e.mu.Lock()
			e.uploaded[it.ID] = key
			if len(e.uploaded) > 500 {
				for k := range e.uploaded {
					delete(e.uploaded, k)
					break
				}
			}
			e.mu.Unlock()
		}
		it.Key = key
	}
	return nil
}

// postItems live 模式一切进活卡片 ( 终答留给 turn 结束 )；
// 否则终答之前的进活卡片后关闭，终答起作为新消息 + 响铃。
func (e *Engine) postItems(session string, record hccli.Terminal, k string, cur state.Cursor, items []core.Item, reader readers.Reader, file string, live bool) error {
	answerAt := -1
	for i, it := range items {
		if it.End {
			answerAt = i
			break
		}
	}

	take := items
	if live && answerAt >= 0 {
		take = items[:answerAt]
	}
	if len(take) == 0 && (cur.Live == nil || live) {
		return nil
	}
	if err := e.uploadImages(take); err != nil {
		return err
	}
	footer := e.footerFor(session, record, reader.App(), metaOf(take, cur))

	var shown []core.Item
	for _, it := range take {
		if it.Role != core.RoleMeta {
			shown = append(shown, it)
		}
	}
	if len(shown) > 0 {
		e.noteUnanswered(k, false)
	}
	openLive := cur.Live
	shownAt := -1
	for i, it := range shown {
		if it.End {
			shownAt = i
			break
		}
	}

	var head []core.Item
	switch {
	case live:
		head = shown
	case shownAt >= 0:
		head = shown[:shownAt]
	case openLive != nil:
		head = shown
	}
	tail := []core.Item{}
	if !live {
		tail = shown[len(head):]
	}
	if headText := e.formatItems(k, head); headText != "" {
		openLive = e.addLive(session, record, openLive, headText, footer)
	}
	if !live && openLive != nil {
		// 模型名可能此刻才被说出。
		openLive.Footer = footer
		e.closeLive(openLive, "")
		openLive = nil
	}

	if tailText := e.formatItems(k, tail); tailText != "" {
		msg, err := e.postTo(session, record, tailText, footer)
		if err != nil {
			return err
		}
		if err := e.Chat.Ring(e.ctx, k, msg, "✅ done"); err != nil {
			xlog.For("engine").Warn("响铃失败", "k", k, "error", err)
		}
	}

	next := core.Advance(cur, take)
	next.Reader, next.File, next.Live = reader.App(), file, openLive
	if len(shown) > 0 {
		last := shown[len(shown)-1]
		next.Ended = last.Role == core.RoleAI && last.End
	}

	e.saveCursor(k, next)
	if len(take) > 0 {
		xlog.For("engine").Info("条目发出", "session", session, "address", record.Address(), "items", len(take), "reader", reader.App(), "live", live)
	}
	return nil
}

// metaOf 条目说出的 footer 数据 ( 新条目覆盖游标存量 )。
func metaOf(items []core.Item, cur state.Cursor) map[string]any {
	meta := map[string]any{}
	if cur.Meta != nil {
		for k, v := range cur.Meta {
			meta[k] = v
		}
	}

	for _, it := range items {
		for _, field := range []struct {
			key string
			get func(core.Item) any
		}{
			{"model", func(i core.Item) any { return i.Model }},
			{"provider", func(i core.Item) any { return i.Provider }},
			{"context", func(i core.Item) any {
				if i.Context == nil {
					return nil
				}
				return map[string]any{"used": i.Context.Used, "size": i.Context.Size}
			}},
			{"limits", func(i core.Item) any {
				if i.Limits == nil {
					return nil
				}
				return i.Limits
			}},
		} {
			if v := field.get(it); v != nil {
				meta[field.key] = v
			}
		}
	}
	return meta
}

// branchNow 终端目录的 git 分支 ( 15s 缓存 )。
func (e *Engine) branchNow(session string, record hccli.Terminal) string {
	cwd := foregroundCwd(record)
	if cwd == "" {
		return ""
	}
	k := e.keyOf(session, record.Address())
	e.mu.Lock()
	if b, ok := e.branches[k]; ok && time.Since(b.at) < 15*time.Second {
		e.mu.Unlock()
		return b.name
	}
	e.mu.Unlock()
	out, _ := e.HC.HomeShell(e.ctx, session, hccli.MachineOf(record.Address()),
		"cd "+shellQ(cwd)+" 2>/dev/null && { git branch --show-current 2>/dev/null | grep . || git rev-parse --short HEAD 2>/dev/null; }")
	name := strings.TrimSpace(out)
	e.mu.Lock()
	e.branches[k] = branchAt{name: name, at: time.Now()}
	e.mu.Unlock()
	return name
}

func foregroundCwd(record hccli.Terminal) string {
	fg, _ := record["foreground"].(map[string]any)
	if fg == nil {
		return ""
	}
	cwd, _ := fg["cwd"].(string)
	return cwd
}

func shellQ(s string) string { return "'" + strings.ReplaceAll(s, "'", `'''`) + "'" }

// footerFor 帖子下面那行小字：harness、模型 ( 或 screen )、分支、上下文、限额 ( reader 自报的补上 )；
// 不知道的一律略去。
func (e *Engine) footerFor(session string, record hccli.Terminal, app string, meta map[string]any) string {
	short := app
	reader := e.Readers.ByApp(app)
	if reader != nil {
		if titled, ok := reader.(interface{ Short() string }); ok {
			if s := titled.Short(); s != "" {
				short = s
			}
		}
	}

	model, _ := meta["model"].(string)
	branch := e.branchNow(session, record)

	var context *core.ContextUse
	if raw, ok := meta["context"].(map[string]any); ok {
		used, _ := raw["used"].(float64)
		size, _ := raw["size"].(float64)
		if used > 0 {
			context = &core.ContextUse{Used: int64(used), Size: int64(size)}
		}
	}

	var limits []core.Limit
	if raw, ok := meta["limits"].([]core.Limit); ok {
		limits = raw
	} else if raw, ok := meta["limits"].([]any); ok {
		for _, l := range raw {
			if lm, ok := l.(map[string]any); ok {
				name, _ := lm["name"].(string)
				used, _ := lm["used"].(float64)
				if name != "" {
					limits = append(limits, core.Limit{Name: name, Used: used})
				}
			}
		}
	}

	if metered, ok := reader.(readers.Metered); ok && reader != nil && (context == nil || context.Size == 0 || limits == nil) {
		if info, err := metered.Meter(e.ctx, session, record.Address(), meta); err == nil {
			// 只有窗口没有用量的情形：留给下一个带用量的条目补全。
			if context != nil && context.Size == 0 {
				context.Size = info.ContextSize
			}
			if limits == nil {
				limits = info.Limits
			}
		}
	}
	return core.FooterWords(short, model, branch, false, context, limits)
}

// cursorOf / saveCursor：每终端的游标在 state 里 ( reader、日志文件随它保存，程序退出后还用它发完遗言 )。
func (e *Engine) cursorOf(k string) state.Cursor {
	cur, ok := e.read().Cursors[k]
	if !ok {
		cur = core.StartCursorTime(e.StartedAt)
		cur.Seen = [][2]string{}
	}
	return cur
}

func (e *Engine) saveCursor(k string, cur state.Cursor) {
	st := e.read()
	if st.Cursors == nil {
		st.Cursors = map[string]state.Cursor{}
	}
	if cur.Seen == nil {
		cur.Seen = [][2]string{}
	}
	st.Cursors[k] = cur
	e.write(st)
}

// postRead 经 reader 发出终端自上一帖以来所说的话。
// waitForEnd 时先等 turn 的终答 ( hc 报告结束早于 agent 落盘 )。
// 返回 false 表示终端不可读。
func (e *Engine) postRead(session, address string, record hccli.Terminal, reader readers.Reader, waitForEnd, live bool) error {
	k := e.keyOf(session, address)
	cur := e.cursorOf(k)
	tries := 1
	if waitForEnd {
		tries = EndTries
	}
	var found *core.Reading
	for attempt := 0; attempt < tries; attempt++ {
		reading, err := reader.After(e.ctx, session, address, cur)
		if err != nil {
			return err
		}

		found = reading
		if reading == nil {
			// 不可读：交给调用方兜底。
			return nil
		}

		ended := false
		for _, it := range reading.Items {
			if it.End {
				ended = true
				break
			}
		}
		if ended || (len(reading.Items) == 0 && cur.Ended) {
			break
		}
		if attempt+1 < tries {
			e.sleep(EndWait)
		}
	}
	if found == nil {
		return nil
	}
	return e.postItems(session, record, k, cur, found.Items, reader, found.File, live)
}

// postTurn turn 结束。
// 等终答、发正文；你发过消息而记录一片空白( agent 把它当自己的命令吃掉了 ) 时发屏幕兜底。
func (e *Engine) postTurn(session, address string, record hccli.Terminal, reader readers.Reader) {
	k := e.keyOf(session, address)
	if err := e.postRead(session, address, record, reader, true, false); err == nil {
		if !e.isUnanswered(k) {
			return
		}
		xlog.For("engine").Warn("turn 结束但记录里没有新内容，发屏幕兜底", "session", session, "address", address)
	} else {
		xlog.For("engine").Warn("读取记录失败，发屏幕兜底", "session", session, "address", address, "error", err)
	}

	e.noteUnanswered(k, false)
	msg, err := e.postTo(session, record, e.screen(session, address, TurnScreens), "")
	if err != nil {
		xlog.For("engine").Error("屏幕兜底发送失败", "error", err)
		return
	}

	if err := e.Chat.Ring(e.ctx, k, msg, "✅ done"); err != nil {
		xlog.For("engine").Warn("响铃失败", "k", k, "error", err)
	}
}

// postEnd 程序没了。
// 先把它日志里剩下的遗言发完，再说它结束了；
// 同一个 tab 里新起来的终端此刻接管房间。
func (e *Engine) postEnd(session, address string, record hccli.Terminal, reason string) error {
	k := e.keyOf(session, address)
	cur := e.cursorOf(k)
	if reader := e.Readers.ByApp(cur.Reader); reader != nil && cur.File != "" {
		items, err := reader.Rest(e.ctx, session, address, cur)
		if err != nil {
			xlog.For("engine").Warn("遗言读取失败", "session", session, "address", address, "error", err)
		} else if len(items) > 0 {
			if err := e.postItems(session, record, k, cur, items, reader, cur.File, false); err != nil {
				xlog.For("engine").Warn("遗言发送失败", "error", err)
			}
		}
	}
	if _, err := e.postTo(session, record, fmt.Sprintf("🔚 **Terminal ended**%s", reasonPart(reason)), ""); err != nil {
		return err
	}
	e.markDone(k, 0)
	e.endRoom(k)

	// 同 tab 的新终端：现在把房间交给它。
	tab := e.read().Chats[k].Tab
	if tab != "" {
		if v := e.viewOf(session); v != nil {
			for _, t := range v.All() {
				other := t
				if other.Live() && other.Address() != address && other.Label("herdr.tab") == tab {
					e.later(session, other.Address(), "room", func() error {
						_, err := e.ensureRoom(session, other)
						return err
					})
				}
			}
		}
	}
	return nil
}

func reasonPart(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

// screen 终端屏幕最后几行，作为代码块。
func (e *Engine) screen(session, address string, lines int) string {
	text, err := e.HC.ScreenTail(e.ctx, session, address, lines)
	if err != nil {
		xlog.For("engine").Warn("读屏失败", "session", session, "address", address, "error", err)
		return ""
	}
	tidy := hccli.TidyScreen(text, lines)
	if tidy == "" {
		tidy = "(blank)"
	}
	return "```\n" + tidy + "\n```"
}

// markWorking / markDone 你从房间发的东西标上"工作中 / 完成"的回执，
// 等待中的回执存进 state，重启后照样补标。
func (e *Engine) markWorking(k string, message core.Message) {
	token, err := e.Chat.MarkWorking(e.ctx, message)
	if err != nil {
		xlog.For("engine").Warn("回执 ( 工作中 ) 失败", "message", message, "error", err)
		return
	}
	st := e.read()
	if st.Reacts == nil {
		st.Reacts = map[string][]state.React{}
	}
	st.Reacts[k] = append(st.Reacts[k], state.React{Message: string(message), Reaction: token, At: nowMs()})
	e.write(st)
}

// markDone 补标完成；
// olderThan 只标记至少这么久之前发进去的 ( sweep 用：刚发进去的可能还没开始一个 turn )。
func (e *Engine) markDone(k string, olderThan time.Duration) {
	st := e.read()
	pending := st.Reacts[k]
	if len(pending) == 0 {
		return
	}

	var waiting []state.React
	cut := nowMs() - olderThan.Milliseconds()
	for _, r := range pending {
		if r.At <= cut {
			waiting = append(waiting, r)
		}
	}
	if len(waiting) == 0 {
		return
	}

	var left []state.React
	for _, r := range pending {
		keep := true
		for _, w := range waiting {
			if w.Message == r.Message {
				keep = false
				break
			}
		}
		if keep {
			left = append(left, r)
		}
	}

	if len(left) > 0 {
		st.Reacts[k] = left
	} else {
		delete(st.Reacts, k)
	}
	e.write(st)
	for _, r := range waiting {
		if err := e.Chat.MarkDone(e.ctx, core.Message(r.Message), r.Reaction); err != nil {
			xlog.For("engine").Warn("回执 ( 完成 ) 失败", "message", r.Message, "error", err)
		}
	}
}

// decodeB64 把 reader 给出的图片数据解码；坏数据当空图处理 ( 房间里会看到未发送的原因 )。
func decodeB64(s string) []byte {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return raw
}

func nowMs() int64 { return time.Now().UnixMilli() }

// branchAt 是一条分支缓存。
type branchAt struct {
	name string
	at   time.Time
}
