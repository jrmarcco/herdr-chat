package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
)

// /sessions 三件套与 /new 卡 session 下拉的行为测试。

// makeSession 在临时 home 里造一个本机 hc session 目录 ( core.json 在即算 )。
func makeSession(t *testing.T, home, name string) {
	t.Helper()
	dir := filepath.Join(home, ".local", "state", "herdr-core", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "core.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// replies：直聊里已发出的文字回复 ( 按出现顺序 )。
func (r *rig) replies() []string {
	var out []string
	for _, c := range r.chat.Calls() {
		if c.Name == callReply && len(c.Args) >= 2 {
			if md, _ := c.Args[1].(string); md != "" {
				out = append(out, md)
			}
		}
	}
	return out
}

func TestSessionsListsLocalCores(t *testing.T) {
	r := harness(t)
	r.start(t)
	makeSession(t, r.e.HomeDir, "default")
	makeSession(t, r.e.HomeDir, sessWork)
	r.direct("/sessions")

	r.waitFor(t, "session 一览", func() bool {
		for _, md := range r.replies() {
			if !strings.Contains(md, "sessions on this machine") {
				continue
			}
			// 跟随的 default 带终端数；未跟随的 work 给出 add 指引。
			if !strings.Contains(md, "`default`") || !strings.Contains(md, "followed") {
				t.Fatalf("list = %q", md)
			}
			if !strings.Contains(md, "`work`") || !strings.Contains(md, "`/sessions add work`") {
				t.Fatalf("list = %q", md)
			}
			return true
		}
		return false
	})
}

func TestSessionsAddAndDrop(t *testing.T) {
	r := harness(t)
	r.start(t)
	makeSession(t, r.e.HomeDir, "default")
	makeSession(t, r.e.HomeDir, sessWork)

	var saved []string
	restarted := make(chan struct{}, 4)
	r.e.SetSessions = func(list []string) error {
		saved = list
		return nil
	}
	r.e.Restart = func() { restarted <- struct{}{} }

	// 不存在的 session 不收 ( 绝不凭空造 core )。
	r.direct("/sessions add nope")
	r.waitFor(t, "拒绝不存在的", func() bool {
		for _, md := range r.replies() {
			if strings.Contains(md, "not on this machine") {
				return true
			}
		}
		return false
	})

	// add：本机在的纳入，写入后重启生效。
	r.direct("/sessions add work")
	r.waitFor(t, "add 生效", func() bool { return len(saved) == 2 && len(restarted) > 0 })
	if saved[0] != sessDefault || saved[1] != sessWork {
		t.Fatalf("saved = %v", saved)
	}

	// 再 add：幂等。
	r.direct("/sessions add work")
	r.waitFor(t, "幂等", func() bool {
		for _, md := range r.replies() {
			if strings.Contains(md, "already following") {
				return true
			}
		}
		return false
	})

	// 跟随起来后 drop：干净的移出 ( add 已把进程内范围同步成两台 )。
	saved = nil
	r.direct("/sessions drop work")
	r.waitFor(t, "drop 生效", func() bool { return len(saved) == 1 && saved[0] == sessDefault })

	// 没跟随的 drop：直说。
	r.direct("/sessions drop work")
	r.waitFor(t, "未跟随的 drop", func() bool {
		for _, md := range r.replies() {
			if strings.Contains(md, "not following") {
				return true
			}
		}
		return false
	})

	// 最后一个不能 drop。
	r.direct("/sessions drop default")
	r.waitFor(t, "最后一个拒 drop", func() bool {
		for _, md := range r.replies() {
			if strings.Contains(md, "at least one session") {
				return true
			}
		}
		return false
	})
}

func TestSessionsDropRefusesLiveTerminals(t *testing.T) {
	r := harness(t)
	r.start(t)
	r.e.Sessions = []string{sessDefault, sessWork}
	r.e.SetSessions = func([]string) error { return nil }

	// work 的 view 里挂一台活终端：drop 要先拒。
	v := hccli.NewView()
	v.Upsert(term("t5"))
	r.e.views[sessWork] = v
	r.direct("/sessions drop work")
	r.waitFor(t, "活终端拒 drop", func() bool {
		for _, md := range r.replies() {
			if strings.Contains(md, "still has a live terminal") {
				return true
			}
		}
		return false
	})
}

func TestNewCardSessionDropdown(t *testing.T) {
	r := harness(t)
	r.start(t)

	// 单 session：只有 agent 下拉 ( 一项的下拉是噪音 )。
	r.direct("/new")
	r.waitFor(t, "卡", func() bool { return r.countCalls(callPostDialog) == 1 })
	d := r.postedDialog()
	if len(d.Selects) != 1 || d.Selects[0].Name != fieldAgent {
		t.Fatalf("selects = %+v, want agent only", d.Selects)
	}
	if !strings.Contains(d.Markdown, "Session: "+sessDefault+".") {
		t.Fatalf("markdown = %q, want the session named", d.Markdown)
	}

	// 多 session：多一个 session 下拉。
	r.e.Sessions = []string{sessDefault, sessWork}
	r.direct("/new")
	r.waitFor(t, "第二张卡", func() bool { return r.countCalls(callPostDialog) == 2 })
	d = r.postedDialog()
	if len(d.Selects) != 2 || d.Selects[1].Name != fieldSession || len(d.Selects[1].Options) != 2 {
		t.Fatalf("selects = %+v, want agent + session", d.Selects)
	}
	if strings.Contains(d.Markdown, "Session:") {
		t.Fatalf("markdown = %q, the dropdown already says it", d.Markdown)
	}

	// 提交带上 session：终端开在那个 session 上。
	r.hc.RemoveTerminal("t1")
	result, err := r.chat.Handlers.OnAction(core.Click{
		Value:   map[string]any{"k": newKey, valPick: "1"},
		Message: directMsg,
		Answer:  folderFoo,
		Form:    map[string]string{fieldAgent: agentClaude, "session": sessWork},
	})
	if err != nil || !strings.Contains(result.Toast, "✔") {
		t.Fatalf("submit: %v %+v", err, result)
	}
	r.waitFor(t, "开在 work", func() bool {
		runs := r.hc.RunsOf()
		return len(runs) == 1 && runs[0].Session == sessWork
	})

	// 未知 session：不 start，toast 指路。
	r.hc.ClearRuns()
	result, err = r.chat.Handlers.OnAction(core.Click{
		Value:   map[string]any{"k": newKey, valPick: "1"},
		Message: directMsg,
		Answer:  folderFoo,
		Form:    map[string]string{"session": "elsewhere"},
	})
	if err != nil || !strings.Contains(result.Toast, "unknown session") {
		t.Fatalf("unknown session: %v %+v", err, result)
	}
	if len(r.hc.RunsOf()) != 0 {
		t.Fatal("an unknown session must start nothing")
	}
}

// postedDialog：最近一张已发出卡的 Dialog ( FakeChat 记录的末位参数 )。
func (r *rig) postedDialog() core.Dialog {
	calls := r.chat.Calls()
	for i := len(calls) - 1; i >= 0; i-- {
		c := calls[i]
		if c.Name == callPostDialog && len(c.Args) >= 4 {
			if d, ok := c.Args[3].(core.Dialog); ok {
				return d
			}
		}
	}
	return core.Dialog{}
}
