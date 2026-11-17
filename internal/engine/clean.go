package engine

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// /clean 把已结束终端的残留清掉：
//   - 房间：state 里标了 ended 的群解散 ( DeleteRoom )，连同游标/回执/暂存
//     一起从 state 摘除；之后同 tab 再起终端会建新群；
//   - 屏幕快照：本机各 session 的 screens/<地址>.snap 里，地址已不在事件流
//     里的删掉 ( 远端机器的快照不在这台机器上，清不到 )。
//
// 终端还活着的房间绝不动；ended 又被复活的 ( 新终端接管 ) 群不在清理之列。

// cleanKey 是 /clean 确认卡的对话框 key ( onAction 据此拦截，不走终端 )。
const cleanKey = "clean-confirm"

// cleanEnded 清理已结束终端的残留。两步：先 `/clean` 发确认卡，
// 点按钮动手解散群聊不可逆，绝不一步就干。`/clean yes` 是按钮不可用时的文字兜底。
func (e *Engine) cleanEnded(args []string, where core.Where) string {
	if e.ctx == nil || e.ctx.Err() != nil {
		return "The bot is stopping: not now."
	}
	if len(args) > 0 && args[0] == "yes" {
		return e.doClean()
	}
	return e.planClean(where)
}

// planClean 只看不动：发一张确认卡 ( Yes / Cancel 按钮 )。
func (e *Engine) planClean(where core.Where) string {
	rooms, snaps := e.surveyClean()
	if rooms == 0 && snaps == 0 {
		return "Nothing to clean: no ended terminal's group, no stale screen file."
	}
	var lines []string
	if rooms > 0 {
		lines = append(lines, "- delete "+itoa(rooms)+" ended "+groupWord(rooms)+" ("+endedNames(e)+")")
	}
	if snaps > 0 {
		lines = append(lines, "- remove "+itoa(snaps)+" stale screen "+fileWord(snaps))
	}
	text := "Cleanup would:\n" + strings.Join(lines, "\n") + "\n\nGroups cannot be undone."

	dialog := core.Dialog{
		Title:     "🧹 Clean up?",
		Markdown:  text,
		DoneTitle: DoneTitle,
		Menu: &core.Menu{
			Key: cleanKey,
			Options: []core.MenuOption{
				{N: 1, Label: "Yes, delete them"},
				{N: 2, Label: "Cancel"},
			},
		},
	}
	if _, err := e.Chat.PostDialog(e.ctx, where.Room, dialog, map[string]any{"k": cleanKey}); err != nil {
		xlog.For("engine").Warn("清理确认卡发送失败", "error", err)
		return text + "\n\nSend `/clean yes` to go ahead."
	}
	return "" // 卡已发，不再回文字
}

// doClean 真正执行清理，回复总结。
func (e *Engine) doClean() string {
	rooms := e.cleanRooms()
	snaps := e.cleanSnaps()

	lines := []string{}
	switch {
	case rooms.deleted > 0 && snaps > 0:
		lines = append(lines, rooms.report())
		lines = append(lines, fmtSnaps(snaps))
	case rooms.deleted > 0:
		lines = append(lines, rooms.report())
	case snaps > 0:
		lines = append(lines, fmtSnaps(snaps))
	default:
		lines = append(lines, "Nothing to clean: no ended terminal's group, no stale screen file.")
	}
	for _, problem := range rooms.problems {
		lines = append(lines, "⚠ "+problem)
	}
	return strings.Join(lines, "\n")
}

// surveyClean 数一数会删的群与快照 ( 不动任何东西 )。
func (e *Engine) surveyClean() (rooms, snaps int) {
	st := e.read()
	for _, room := range st.Chats {
		if room.Ended {
			rooms++
		}
	}
	snaps = e.countSnaps()
	return rooms, snaps
}

// endedNames 列出会删的群名 ( 至多 8 个，多了加 … )。
func endedNames(e *Engine) string {
	st := e.read()
	var names []string
	for _, room := range st.Chats {
		if room.Ended {
			names = append(names, room.Name)
		}
	}
	if len(names) > 8 {
		names = append(names[:8], "…")
	}
	return strings.Join(names, ", ")
}

// wordFile 收拢 goconst：'file' 在历史形态键里也出现。
const wordFile = "file"

func groupWord(n int) string {
	if n == 1 {
		return "group"
	}
	return "groups"
}

func fileWord(n int) string {
	if n == 1 {
		return wordFile
	}
	return "files"
}

func fmtSnaps(n int) string {
	if n == 1 {
		return "Removed 1 stale screen file."
	}
	return "Removed " + itoa(n) + " stale screen files."
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// roomsCleaned 是一次房间清理的结果。
type roomsCleaned struct {
	deleted  int
	names    []string
	problems []string
}

func (r roomsCleaned) report() string {
	plural := "s"
	if r.deleted == 1 {
		plural = ""
	}
	return "Deleted " + itoa(r.deleted) + " ended group" + plural + ": " + strings.Join(r.names, ", ") + "."
}

// cleanRooms 解散已结束终端的群并从 state 摘除。
func (e *Engine) cleanRooms() roomsCleaned {
	var out roomsCleaned
	st := e.read()
	for k, room := range st.Chats {
		if !room.Ended {
			continue
		}
		if err := e.Chat.DeleteRoom(k); err != nil {
			out.problems = append(out.problems, room.Name+": "+hccli.PlainError(err))
			continue
		}
		out.deleted++
		out.names = append(out.names, room.Name)
		delete(st.Chats, k)
		delete(st.Cursors, k)
		delete(st.OpenCards, k)
		delete(st.Reacts, k)
		delete(st.Waiting, k)
		delete(st.Pinned, k)
		xlog.For("engine").Info("清理已结束终端的群", "k", k, "name", room.Name)
	}
	if out.deleted > 0 || len(out.problems) > 0 {
		e.write(st)
		e.scheduleOverview()
	}
	return out
}

// countSnaps 只数不删：本机各 session 里已无终端的屏幕快照个数。
func (e *Engine) countSnaps() int {
	n := 0
	for _, session := range e.Sessions {
		dir := hccli.ScreensDir(e.Env, e.HomeDir, session)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		view := e.viewOf(session)
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".snap") {
				continue
			}
			address := strings.TrimSuffix(name, ".snap")
			if view != nil {
				if record, ok := view.Get(address); ok && record.Live() {
					continue
				}
			}
			n++
		}
	}
	return n
}

// cleanSnaps 删本机各 session 里已无终端的屏幕快照，返回删掉的个数。
func (e *Engine) cleanSnaps() int {
	removed := 0
	for _, session := range e.Sessions {
		dir := hccli.ScreensDir(e.Env, e.HomeDir, session)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // 目录不在：这个 session 没有快照
		}
		view := e.viewOf(session)
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".snap") {
				continue
			}
			address := strings.TrimSuffix(name, ".snap")
			if view != nil {
				if record, ok := view.Get(address); ok && record.Live() {
					continue // 终端还在：快照是它的
				}
			}
			if err := os.Remove(filepath.Join(dir, name)); err == nil {
				removed++
			}
		}
	}
	return removed
}
