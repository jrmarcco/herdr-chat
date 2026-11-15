package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/state"
)

const (
	snapLive   = "t1.snap"
	snapGone   = "t2.snap"
	snapOrphan = "t9.snap"
)

// /new 的 --cwd 规则：只认 $HOME 之下的相对路径，$HOME 本身与绝对路径都拒。

func (r *rig) newOut(args string) string {
	return r.e.newTerminal(strings.Fields(args))
}

func TestNewCwdRules(t *testing.T) {
	r := harness(t)
	r.start(t)
	home := r.e.HomeDir

	// 相对目录：落在 $HOME/<dir>。
	out := r.newOut("--cwd Projects/x default api/n1")
	if len(r.hc.Runs) != 1 || r.hc.Runs[0].Opts.Cwd != filepath.Join(home, "Projects", "x") {
		t.Fatalf("runs = %+v (out %q)", r.hc.Runs, out)
	}

	// $HOME 本身（显式、`.`、空段）与绝对路径、~、越出 home 的 ..，全拒。
	r.hc.ClearRuns()
	for _, bad := range []string{
		"--cwd . default api/n2",
		"--cwd ./ default api/n2",
		"--cwd /tmp default api/n2",
		"--cwd ~ default api/n2",
		"--cwd ../out default api/n2",
		"default api/n2", // 无 --cwd 且无可沿用的兄弟
	} {
		if got := r.newOut(bad); got == "" || !containsAny(got, "cannot work in $HOME", "--cwd") {
			t.Fatalf("%q: got %q", bad, got)
		}
	}
	if len(r.hc.Runs) != 0 { // 拒绝之后一次也没起
		t.Fatalf("runs after rejections = %+v", r.hc.Runs)
	}

	// 深层的 .. 只要在 home 内就收。
	r.hc.ClearRuns()
	if out := r.newOut("--cwd Projects/x/../y default api/n3"); len(r.hc.Runs) != 1 || r.hc.Runs[0].Opts.Cwd != filepath.Join(home, "Projects", "y") {
		t.Fatalf("in-home .. rejected: runs=%+v out=%q", r.hc.Runs, out)
	}

	// 兄弟终端的目录沿用：在 home 内的沿用，落在 $HOME 的不学。
	r.hc.ClearRuns()
	r.hc.Terminals["t1"]["foreground"] = map[string]any{"cwd": filepath.Join(home, "work")}
	if out := r.newOut("default api/n4"); len(r.hc.Runs) != 1 || r.hc.Runs[0].Opts.Cwd != filepath.Join(home, "work") {
		t.Fatalf("sibling reuse: runs=%+v out=%q", r.hc.Runs, out)
	}
	r.hc.ClearRuns()
	r.hc.Terminals["t1"]["foreground"] = map[string]any{"cwd": home}
	if out := r.newOut("default api/n5"); len(r.hc.Runs) != 0 || !containsAny(out, "cannot work in $HOME") {
		t.Fatalf("sibling in $HOME must not be reused: runs=%+v out=%q", r.hc.Runs, out)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) && (s == sub || len(s) > len(sub) && contains(s, sub)) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// /clean：ended 的群解散并从 state 摘除；无主 .snap 删掉；活终端一动不动。
func TestCleanEnded(t *testing.T) {
	r := harness(t)
	r.start(t)

	// t1 活着，房间在；t2 已结束（ended），房间在 state；screens 里有 t1/t2/t9 三份快照。
	st, _ := r.store.Read()
	if st.Cursors == nil {
		st.Cursors = map[string]state.Cursor{}
	}
	st.Chats["default|t2"] = state.Room{Chat: "room2", Name: "api / gone", Session: sessDefault, Tab: "tb2", Ended: true}
	st.Cursors["default|t2"] = state.Cursor{At: "2026-01-01T00:00:00.000Z"}
	if err := r.store.Write(st); err != nil {
		t.Fatal(err)
	}
	screens := filepath.Join(r.e.HomeDir, ".local", "state", "herdr-core", sessDefault, "screens")
	if err := os.MkdirAll(screens, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{snapLive, snapGone, snapOrphan, "notes.txt"} {
		if err := os.WriteFile(filepath.Join(screens, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 第一步 `/clean`：发确认卡 ( Yes / Cancel )，不动任何东西。
	r.direct("/clean")
	r.waitFor(t, "确认卡", func() bool { return r.countCalls("PostDialog") >= 1 })
	if r.countCalls("DeleteRoom") != 0 {
		t.Fatal("plan step must not delete anything")
	}
	for _, name := range []string{snapGone, snapOrphan} {
		if _, err := os.Stat(filepath.Join(screens, name)); err != nil {
			t.Fatalf("%s deleted during plan: %v", name, err)
		}
	}
	// 卡片内容带清单。
	for _, c := range r.chat.Calls() {
		if c.Name == "PostDialog" && len(c.Args) >= 2 {
			if d, ok := c.Args[1].(core.Dialog); ok && d.Title == "🧹 Clean up?" {
				if !contains(d.Markdown, "api / gone") || !contains(d.Markdown, "2 stale screen") {
					t.Fatalf("card markdown = %q", d.Markdown)
				}
			}
		}
	}

	// 点 Yes 按钮：真删。
	result, err := r.chat.Handlers.OnAction(r.click("1", "Yes, delete them", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result.Toast, "✔ Cleaned.") {
		t.Fatalf("toast = %q", result.Toast)
	}
	r.waitFor(t, "解散 ended 群", func() bool { return r.countCalls("DeleteRoom") == 1 })

	// 群与游标摘除；t1 的房间不动。
	st, _ = r.store.Read()
	if _, gone := st.Chats["default|t2"]; gone {
		t.Fatal("ended room still in state")
	}
	if _, gone := st.Cursors["default|t2"]; gone {
		t.Fatal("cursor of ended room still in state")
	}
	if _, kept := st.Chats["default|t1"]; !kept {
		t.Fatal("live room must stay")
	}
	for _, c := range r.chat.Calls() {
		if c.Name == "DeleteRoom" {
			if len(c.Args) < 1 || c.Args[0] != "default|t2" {
				t.Fatalf("DeleteRoom arg = %v", c.Args)
			}
		}
	}

	// 快照：活着的 t1 留下，t2/t9 删掉，无关文件不碰。
	for name, want := range map[string]bool{snapLive: true, snapGone: false, snapOrphan: false, "notes.txt": true} {
		_, serr := os.Stat(filepath.Join(screens, name))
		if want && serr != nil {
			t.Fatalf("%s must stay: %v", name, serr)
		}
		if !want && serr == nil {
			t.Fatalf("%s must be removed", name)
		}
	}

	// 点 Cancel：不删。
	st2, _ := r.store.Read()
	st2.Chats["default|t3"] = state.Room{Chat: "room3", Name: "api / cancel me", Session: sessDefault, Ended: true}
	if werr := r.store.Write(st2); werr != nil {
		t.Fatal(werr)
	}
	r.direct("/clean")
	r.waitFor(t, "又一张确认卡", func() bool { return r.countCalls("PostDialog") >= 2 })
	cancelRes, cancelErr := r.chat.Handlers.OnAction(r.click("2", "Cancel", ""))
	if cancelErr != nil || !contains(cancelRes.Toast, "Cancelled") {
		t.Fatalf("cancel: %v toast=%q", err, result.Toast)
	}
	if r.countCalls("DeleteRoom") != 1 {
		t.Fatalf("cancel must not delete: %d", r.countCalls("DeleteRoom"))
	}
	st3, _ := r.store.Read()
	if _, kept := st3.Chats["default|t3"]; !kept {
		t.Fatal("cancel must keep the room")
	}

	// 再来一次：没东西可清。
	st2b, _ := r.store.Read()
	st2b.Chats["default|t3"] = state.Room{Chat: "room3", Name: "x", Session: sessDefault}
	if err := r.store.Write(st2b); err != nil {
		t.Fatal(err)
	}
	r.direct("/clean")
	if got := r.lastCleanReply(); !contains(got, "Nothing to clean") {
		t.Fatalf("second clean plan = %q", got)
	}
}

func (r *rig) lastCleanReply() string {
	return r.lastReplyOf("", "clean", "Removed", "Deleted", "Cleanup", "Nothing")
}

// lastReplyOf 找最近一条含任一关键词的回复。
func (r *rig) lastReplyOf(_ string, words ...string) string {
	calls := r.chat.Calls()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Name == callReply && len(calls[i].Args) >= 2 {
			md, _ := calls[i].Args[1].(string)
			for _, w := range words {
				if contains(md, w) {
					return md
				}
			}
		}
	}
	return ""
}
