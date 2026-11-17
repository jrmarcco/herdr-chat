package engine

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/readers"
	"github.com/jrmarcco/herdr-chat/internal/state"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// 房间生命周期：
// 一个房间照 herdr-app 展示终端的方式命名，
// 归属 tab ( 按 herdr.tab 标签 id ) 而非恰好跑在里面的终端，终端被替换后，新的接管 tab 的房间。

// keyOf 终端在 state 里的键，"<session>|tN"；
// 别的机器上的终端是 "@<machine>|<machine>/tN"，
// 机器名取跟随它的那个视角的叫法。
func (e *Engine) keyOf(session, address string) string {
	scope := session
	if machine := hccli.MachineOf(address); machine != "" {
		scope = machine
		return "@" + scope + "|" + address
	}
	return scope + "|" + address
}

// ScopeOfKey / AddressOfKey 拆开一个键。
func ScopeOfKey(k string) string {
	k = strings.TrimPrefix(k, "@")
	return k[:strings.Index(k, "|")]
}

func AddressOfKey(k string) string {
	return k[strings.Index(k, "|")+1:]
}

// shownWhere live 终端才谈得上房间；where 是 herdr-app 的 "<space> / <tab>"。
func shownWhere(record hccli.Terminal) (space, tab string) {
	if record == nil || !record.Live() {
		return "", ""
	}
	where := record.Where()
	s, t, ok := hccli.SplitWhere(where)
	if !ok {
		return "", ""
	}
	return s, t
}

var (
	digitsOnly    = regexp.MustCompile(`^\d+$`)
	leadingGlyphs = regexp.MustCompile(`^[^\p{L}\p{N}]+`)
)

// tabName herdr-app 只给了编号 ( "2" ) 的 tab 什么也没说，
// 用终端标题顶上 ( Claude Code 会把会话名写进去 )，再退到 hc 名；标题开头的状态符号剥掉，
// spinner 永远不该给房间改名。
// 带更多内容的标题 ( Codex 的 "<thread> | <folder>" )由 reader 的 TitleOf 挑出名字那段。
func (e *Engine) tabName(tab string, record hccli.Terminal) string {
	if !digitsOnly.MatchString(tab) {
		return tab
	}
	title := record.Title()
	if e != nil && e.Readers != nil {
		if reader := e.agentOf(record); reader != nil {
			if titled, ok := reader.(readers.Titled); ok {
				title = titled.TitleOf(title)
			}
		}
	}
	title = strings.Join(strings.Fields(leadingGlyphs.ReplaceAllString(title, "")), " ")
	name := title
	if name == "" {
		if n, _ := record["name"].(string); n != "" {
			name = n
		}
	}
	if name == "" {
		return tab
	}
	runes := []rune(name)
	if len(runes) > TitleMax {
		return string(runes[:TitleMax-1]) + "…"
	}
	return name
}

func (e *Engine) roomName(space, tab string, record hccli.Terminal) string {
	return space + " / " + e.tabName(tab, record)
}

// putRoom 写下一个键的房间 ( 读-改-写由调用方完成，这里只落 )。
func (e *Engine) putRoom(k string, room state.Room) {
	st := e.read()
	st.Chats[k] = room
	e.write(st)
}

// terminalOfRoom 反查一个房间对应的终端。
func (e *Engine) terminalOfRoom(room core.Room) (session, address string, ok bool) {
	for k, v := range e.read().Chats {
		if v.Chat == string(room) {
			return v.Session, AddressOfKey(k), true
		}
	}
	return "", "", false
}

// adoptTabRoom 房间归属 tab。
// 同 scope 里另一个键已带着同一个 tab id：
// 旧终端还活着 → 等它 ( WAIT，返回 ok=false 且 adopted=false )；
// 否则把旧房间 ( 连同置顶与回执映射 ) 改挂到新键，旧终端的遗言由它的 postEnd 先发。
// 返回 (房间, 是否接管成功)；( 空, false ) 表示需要新建。
func (e *Engine) adoptTabRoom(k, tab string) (state.Room, bool, bool) {
	st := e.read()
	var oldKey string
	var old state.Room
	found := false
	for ok, room := range st.Chats {
		if ok != k && room.Tab == tab && ScopeOfKey(ok) == ScopeOfKey(k) {
			oldKey, old, found = ok, room, true
			break
		}
	}
	if !found {
		return state.Room{}, false, false
	}

	// 旧终端仍 live：它的 end 会把房间交过来，现在不建。
	if v := e.viewOf(old.Session); v != nil {
		if t, ok := v.Get(AddressOfKey(oldKey)); ok && t.Live() {
			return state.Room{}, false, true // WAIT
		}
	}

	st = e.read()
	room, still := st.Chats[oldKey]
	if !still {
		return state.Room{}, false, false
	}

	delete(st.Chats, oldKey)
	st.Chats[k] = room

	if st.Pinned != nil {
		if p, ok := st.Pinned[oldKey]; ok {
			if st.Pinned == nil {
				st.Pinned = map[string]string{}
			}
			st.Pinned[k] = p
			delete(st.Pinned, oldKey)
		}
	}
	if st.Reacts != nil {
		if r, ok := st.Reacts[oldKey]; ok {
			if st.Reacts == nil {
				st.Reacts = map[string][]state.React{}
			}
			st.Reacts[k] = r
			delete(st.Reacts, oldKey)
		}
	}
	if st.Cursors != nil {
		delete(st.Cursors, oldKey)
	}

	e.write(st)
	xlog.For("engine").Info("房间跟随它 tab 的新终端", "room", room.Name, "chat", room.Chat, "address", AddressOfKey(k))
	_, err := e.Chat.Post(e.ctx, core.Room(room.Chat),
		fmt.Sprintf("↪ This tab runs a new terminal now (%s); it carries on here.", AddressOfKey(k)), "")
	if err != nil {
		xlog.For("engine").Warn("接管通知发送失败", "error", err)
	}
	return room, true, false
}

func (e *Engine) viewOf(session string) *hccli.View {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.views[session]
}

// ensureRoom 确保一个终端的房间存在且跟得上。
// 已存在的房间无条件放行，终端已结束时的遗言与结束语也要发进去；
// live + where + 跟随权 的门只拦 "新建"。
// 新房间做历史回填。返回房间 id；
// WAIT ( 旧终端还活着 ) 返回空。
func (e *Engine) ensureRoom(session string, record hccli.Terminal) (core.Room, error) {
	k := e.keyOf(session, record.Address())
	if known, ok := e.read().Chats[k]; ok {
		return e.refreshRoom(session, record, k, known)
	}
	space, tabName := shownWhere(record)
	if space == "" || !e.Chat.Ready() || !e.handles(session, record.Address()) {
		return "", nil
	}
	name := e.roomName(space, tabName, record)
	tab := record.Label("herdr.tab")
	if tab != "" {
		known, adopted, wait := e.adoptTabRoom(k, tab)
		if wait {
			return "", nil
		}
		if adopted {
			return e.refreshRoom(session, record, k, known)
		}
	}

	room, err := e.Chat.OpenRoom(e.ctx, core.RoomLook{
		Name:  name,
		Intro: fmt.Sprintf("hc terminal %s (hc session %q). Type here to talk to it.", record.Address(), session),
	})
	if err != nil {
		return "", err
	}

	st := e.read()
	st.Chats[k] = state.Room{Chat: string(room), Name: name, Session: session, Tab: tab}
	e.write(st)
	xlog.For("engine").Info("创建房间", "name", name, "chat", room)
	if err := e.Chat.PlaceRoom(e.ctx, k, core.Place{Space: space, Session: session}); err != nil {
		return room, err
	}
	e.backfill(session, record, k, room, name, e.History)
	return room, nil
}

// refreshRoom 让一个已有房间跟得上：
// 复活、改名、装饰。终端已结束时 where 读不出来，
// 改名与装饰跳过，房间照给 ( 遗言、结束语都发得进去 )。
func (e *Engine) refreshRoom(session string, record hccli.Terminal, k string, known state.Room) (core.Room, error) {
	if known.Ended {
		e.reviveRoom(k, known)
	}
	space, tabName := shownWhere(record)
	if space == "" {
		return core.Room(known.Chat), nil
	}
	name := e.roomName(space, tabName, record)
	tab := record.Label("herdr.tab")
	if known.Name != name || known.Session != session || (tab != "" && known.Tab != tab) {
		if known.Name != name {
			if err := e.Chat.RenameRoom(e.ctx, core.Room(known.Chat), name); err != nil {
				return "", err
			}
			xlog.For("engine").Info("房间改名", "chat", known.Chat, "name", name)
		}
		known.Name, known.Session = name, session
		if tab != "" {
			known.Tab = tab
		}
		e.putRoom(k, known)
	}
	if err := e.Chat.PlaceRoom(e.ctx, k, core.Place{Space: space, Session: session}); err != nil {
		return "", err
	}
	return core.Room(known.Chat), nil
}

// endRoom 终端没了，房间带着历史留下，
// 标 ended，同一位置的新终端得到的就不会是老房间。
func (e *Engine) endRoom(k string) {
	known, ok := e.read().Chats[k]
	if !ok || known.Ended {
		return
	}
	room := known
	room.Ended = true
	e.putRoom(k, room)
	xlog.For("engine").Info("房间标记结束", "name", known.Name, "chat", known.Chat)
	if err := e.Chat.EndRoom(e.ctx, k); err != nil {
		xlog.For("engine").Warn("标记房间结束失败", "k", k, "error", err)
	}
}

func (e *Engine) reviveRoom(k string, known state.Room) {
	room := known
	room.Ended = false
	e.putRoom(k, room)
	if err := e.Chat.ReviveRoom(e.ctx, k); err != nil {
		xlog.For("engine").Warn("复活房间失败", "k", k, "error", err)
		return
	}
	xlog.For("engine").Info("房间复活", "name", known.Name, "chat", known.Chat)
}

// endGone bot 停机期间结束的终端收不到 end 事件；
// 每个 session 的首快照都到齐后，任何 session 里都不 live 的房间判 ended。
// 别的机器上的终端只在那台机器连接着时才算真的没了，断连绝不会结束一个房间。
func (e *Engine) endGone() {
	connected := map[string]bool{}
	for _, session := range e.Sessions {
		machines, err := e.HC.Machines(e.ctx, session)
		if err != nil {
			continue
		}
		for _, m := range machines {
			if m.State == "connected" {
				connected[m.Name] = true
			}
		}
	}

	e.mu.Lock()
	topology := e.topology
	e.mu.Unlock()

	alive := map[string]bool{}
	for _, session := range e.Sessions {
		if v := e.viewOf(session); v != nil {
			for _, record := range v.All() {
				if record.Live() {
					id := record.Address()
					if topology != nil {
						if ident := topology.Identity(session, record.Address()); ident != "" {
							id = ident
						}
					}
					alive[id] = true
				}
			}
		}
	}

	for k, c := range e.read().Chats {
		address := AddressOfKey(k)
		machine := hccli.MachineOf(address)
		if c.Ended || !e.knownSession(c.Session) || (machine != "" && !connected[machine]) {
			continue
		}
		id := address
		if topology != nil {
			if ident := topology.Identity(c.Session, address); ident != "" {
				id = ident
			}
		}
		if alive[id] {
			continue
		}
		_ = e.lanes.Do(k, func() error { e.endRoom(k); return nil })
	}
}

func (e *Engine) knownSession(session string) bool {
	for _, s := range e.Sessions {
		if s == session {
			return true
		}
	}
	return false
}

// transcriptOf 把条目排成一份可读的文字。
func transcriptOf(items []core.Item) string {
	var parts []string
	for _, it := range items {
		switch it.Role {
		case core.RoleYou:
			parts = append(parts, youOrOthers(it.Text, " · "+clockOf(it.At)))
		case core.RoleAI, core.RoleTool:
			parts = append(parts, it.Text)
		case core.RoleImage:
			who := "it looked at"
			if it.From == core.RoleYou {
				who = "you pasted"
			}
			parts = append(parts, fmt.Sprintf("_(an image %s)_", who))
		}
	}
	return strings.Join(parts, "\n\n")
}

func clockOf(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") }

// backfill 新房间不空白，这个终端之前的整段对话一份文件，
// 最新一段一帖折叠；只取房间游标之前的内容 ( 其余照常发，不会来两遍 )；
// 静默、只此一次。
func (e *Engine) backfill(session string, record hccli.Terminal, k string, room core.Room, name string, parts map[string]bool) {
	reader := e.agentOf(record)
	if reader == nil || (!parts["file"] && !parts["folded"]) {
		return
	}

	until := e.cursorOf(k).At
	untilTime, _ := time.Parse(time.RFC3339, until)
	all, err := reader.After(e.ctx, session, record.Address(), core.StartCursor("0000-00-00T00:00:00.000Z"))
	if err != nil || all == nil {
		return
	}

	var items []core.Item
	for _, it := range all.Items {
		if it.Role != core.RoleMeta && !it.At.After(untilTime) {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		return
	}

	whole := transcriptOf(items)
	if len(whole) > FileBytes {
		whole = fmt.Sprintf("_(the oldest part is left out: the whole is over %d MB)_\n\n…%s", FileBytes/(1<<20), whole[len(whole)-FileBytes:])
	}
	day := items[0].At.UTC().Format("2006-01-02")
	file := fmt.Sprintf("# %s\n\nhc terminal %s (hc session %q), %s to %s UTC, %d items.\n\n%s\n",
		name, record.Address(), session,
		items[0].At.UTC().Format("2006-01-02 15:04"), items[len(items)-1].At.UTC().Format("2006-01-02 15:04"),
		len(items), whole)
	if parts["file"] {
		if _, err := e.Chat.PostFile(e.ctx, room, core.FilePost{
			Name:    sanitizeName(name) + "-" + day + ".md",
			Data:    []byte(file),
			Caption: fmt.Sprintf("The conversation before this room: %d items", len(items)),
		}); err != nil {
			xlog.For("engine").Warn("回填文件发送失败", "error", err)
		}
	}

	// 最新、装得下的那些整条，最旧的在前。
	var recent []core.Item
	bytes := 0
	for i := len(items) - 1; i >= 0; i-- {
		size := len(transcriptOf([]core.Item{items[i]})) + 2
		if bytes+size > e.Chat.FoldedBytes() {
			break
		}
		recent = append([]core.Item{items[i]}, recent...)
		bytes += size
	}
	if parts["folded"] && len(recent) > 0 {
		title := "📜 Before this room"
		if len(recent) < len(items) {
			note := fmt.Sprintf(" (the last %d of %d", len(recent), len(items))
			if parts["file"] {
				note += "; all of it is in the file"
			}
			title += note + ")"
		}
		if _, err := e.Chat.PostFolded(e.ctx, room, core.FoldedPost{Title: title, Markdown: transcriptOf(recent)}); err != nil {
			xlog.For("engine").Warn("回填折叠帖发送失败", "error", err)
		}
	}
	xlog.For("engine").Info("回填早前对话", "session", session, "address", record.Address(), "items", len(items), "shown", len(recent))
}

func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// whereShown：终端是否被 herdr-app 展示 ( 有 where 才有房间可谈 )。
func whereShown(record hccli.Terminal) bool {
	space, _ := shownWhere(record)
	return space != ""
}
