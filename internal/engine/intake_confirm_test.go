package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/state"
)

// 发送前的查屏 ( 信任确认这一类 hc 不报 blocked 的对话框 )：
// 在 herdr-app 里由人确认，这里绝不盲发 —— 盲发的回车恰好确认 "No, exit"，终端当场退出。

const (
	termTrust = "t9"
	callReply = "Reply"

	stateLive = "live"
	tabTrust  = "tb-trust"
	msgM9     = "m9"
)

const trustScreen = " Accessing workspace:\n\n /home/x\n\n Quick safety check: Is this a project you created or one that you trust?\n\n Claude Code'll be able to read, edit, and execute files here.\n\n ❯ No, exit\n   Yes, I trust this folder\n\n Enter to confirm · Esc to cancel\n"

func TestNothingTypedIntoAWaitingConfirm(t *testing.T) {
	r := harness(t)
	r.start(t)

	// 一个还没有注册成 agent 的终端 ( 新生的 claude 停在目录信任上 )——走正式的事件通路进来，视图里才有它。
	r.hc.Change(sessDefault, "created", termTrust, map[string]any{
		keyAddress: termTrust, keyState: stateLive,
		keyLabels: map[string]any{keyWhere: whereAPI, keySpace: spaceID, keyTabLabel: tabTrust},
	})

	// 房间先注册进 state ( onMessage 按房间反查终端 )。
	st, _ := r.store.Read()
	st.Chats["default|t9"] = state.Room{Chat: "room-t9", Name: "api / trust", Session: sessDefault, Tab: tabTrust}
	if err := r.store.Write(st); err != nil {
		t.Fatal(err)
	}
	r.hc.SetScreen(termTrust, trustScreen)
	r.chat.Handlers.OnMessage(core.Incoming{
		Room: core.Room("room-t9"), Where: core.Where{Room: core.Room("room-t9"), Message: msgM9},
		Message: msgM9, Text: "hi",
	})

	r.waitFor(t, "指路回复", func() bool {
		for _, c := range r.chat.Calls() {
			if c.Name == callReply && len(c.Args) >= 2 {
				if md, _ := c.Args[1].(string); strings.Contains(md, "nothing was typed") {
					return true
				}
			}
		}
		return false
	})

	for _, sent := range r.hc.Sents() {
		if sent.Address == termTrust {
			t.Fatalf("盲发进确认框: %+v", sent)
		}
	}

	// 正常空闲屏幕 ( 光标行是空的 )：照发。
	r.hc.SetScreen(termTrust, "❯\n\n  ⏸ manual mode on")
	r.chat.Handlers.OnMessage(core.Incoming{
		Room: core.Room("room-t9"), Where: core.Where{Room: core.Room("room-t9"), Message: "m10"},
		Message: "m10", Text: "hello",
	})
	r.waitFor(t, "正常送达", func() bool {
		for _, sent := range r.hc.Sents() {
			if sent.Address == termTrust && sent.Text == "hello" {
				return true
			}
		}
		return false
	})
}

// 语料全量过一遍判据：三份信任屏都要拦，其余不带确认提示的都不拦。
func TestScreenWaitsConfirmOverTheCorpus(t *testing.T) {
	files, err := os.ReadDir(filepath.Join("..", "..", "testdata", "screens"))
	if err != nil {
		t.Fatal(err)
	}
	// 全是"盲发会出事"的等待型对话框：三份信任屏 + 问题/审批/选择器。
	wantWaits := map[string]bool{
		"trust.txt": true, "codex-trust.txt": true, "agy-trust.txt": true,
		"agy-question.txt": true, "claude-effort-slider.txt": true,
		"codex-approval.txt": true, "codex-effort-picker.txt": true, "codex-model-picker.txt": true,
	}

	checked := 0
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".txt") {
			continue
		}
		//nolint:gosec // G304: 语料目录自读。
		raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "screens", name))
		if err != nil {
			t.Fatal(err)
		}
		got := screenWaitsConfirm(string(raw))
		if want, ok := wantWaits[name]; ok {
			checked++
			if got != want {
				t.Errorf("%s: waits = %v, want %v", name, got, want)
			}
		} else if got {
			t.Errorf("%s: 意外命中 ( 宁漏勿拦 )", name)
		}
	}
	if checked != len(wantWaits) {
		t.Fatalf("语料缺信任屏: %d/%d", checked, len(wantWaits))
	}
}

// /enter：只按回车、不注入文字 —— "Enter to continue · Esc to cancel" 类告知框的确认钥匙，与 /esc 对称。
func TestEnterPressesEnterAndNothingElse(t *testing.T) {
	r := harness(t)
	r.start(t)

	r.hc.Change(sessDefault, "created", termTrust, map[string]any{
		keyAddress: termTrust, keyState: stateLive,
		keyLabels: map[string]any{keyWhere: whereAPI, keySpace: spaceID, keyTabLabel: tabTrust},
	})
	st, _ := r.store.Read()
	st.Chats["default|t9"] = state.Room{Chat: "room-t9", Name: "api / trust", Session: sessDefault, Tab: tabTrust}
	if err := r.store.Write(st); err != nil {
		t.Fatal(err)
	}
	r.hc.SetScreen(termTrust, trustScreen)

	r.chat.Handlers.OnMessage(core.Incoming{
		Room: core.Room("room-t9"), Where: core.Where{Room: core.Room("room-t9"), Message: msgM9},
		Message: msgM9, Text: "/enter",
	})

	r.waitFor(t, "Enter pressed 回复", func() bool {
		for _, c := range r.chat.Calls() {
			if c.Name == callReply && len(c.Args) >= 2 {
				if md, _ := c.Args[1].(string); strings.Contains(md, "Enter pressed.") {
					return true
				}
			}
		}
		return false
	})

	pressed := false
	for _, k := range r.hc.KeysPressed() {
		if k.Address == termTrust {
			pressed = true
			if len(k.Keys) != 1 || k.Keys[0] != "enter" {
				t.Fatalf("keys = %v, want exactly enter", k.Keys)
			}
		}
	}
	if !pressed {
		t.Fatal("enter was never pressed")
	}
	for _, sent := range r.hc.Sents() {
		if sent.Address == termTrust {
			t.Fatalf("/enter must not type text: %+v", sent)
		}
	}
}
