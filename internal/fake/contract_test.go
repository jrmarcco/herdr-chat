package fake_test

import (
	"context"
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/chats"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/fake"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/readers"
)

// 契约检查 ( checkChat / checkReader )：
// 跑不起来的 chat 或 reader 要在建任何房间之前的启动时刻就说出来。

const (
	appFakeAgent = "fake-agent"
	appClaude    = "claude-code"
	procClaude   = "claude"
)

type badChat struct{ *fake.FakeChat }

func (badChat) LiveBytes() int { return 0 }

func TestCheckChat(t *testing.T) {
	if err := chats.CheckChat(fake.NewFakeChat()); err != nil {
		t.Errorf("the fake chat must pass: %v", err)
	}
	if err := chats.CheckChat(badChat{fake.NewFakeChat()}); err == nil {
		t.Errorf("a chat with no live-card bytes must fail")
	}
	if err := chats.CheckChat(nil); err == nil {
		t.Errorf("no chat at all must fail")
	}
}

type emptyReader struct{ *fake.FakeReader }

func (emptyReader) Processes() []string { return nil }

type dupReader struct{ *fake.FakeReader }

func TestCheckReaderAndRegistry(t *testing.T) {
	reg := readers.NewRegistry()
	if err := reg.Register(fake.NewFakeReader()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Register(dupReader{fake.NewFakeReader()}); err == nil || err.Error() != "two readers for fake-agent" {
		t.Errorf("duplicate app: err = %v", err)
	}
	if err := reg.Register(emptyReader{fake.NewFakeReader()}); err == nil {
		t.Errorf("a reader with no processes must fail CheckReader")
	}
	if reg.ByApp(appFakeAgent) == nil || reg.ByApp("nope") != nil {
		t.Errorf("ByApp lookup broken")
	}
	if apps := reg.Apps(); len(apps) != 1 || apps[0] != appFakeAgent {
		t.Errorf("Apps = %v", apps)
	}
}

func TestAgentOf(t *testing.T) {
	reg := readers.NewRegistry()
	rd := fake.NewFakeReader()

	rd.AppName = appClaude
	rd.ProcNames = []string{procClaude}
	if err := reg.Register(rd); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		app, foreground string
		want            bool
		why             string
	}{
		{appClaude, procClaude, true, "its own name"},
		{appClaude, "/usr/local/bin/claude", true, "a path's basename"},
		{appClaude, "/bin/zsh", false, "hc's status can linger: a shell is not its agent"},
		{appClaude, "", true, "an unknown foreground is trusted (better chatty than silent)"},
		{"codex", "codex", false, "an app with no reader"},
		{"", procClaude, false, "no app in the status"},
	}
	for _, c := range cases {
		got := readers.AgentOf(reg, c.app, c.foreground)
		if (got != nil) != c.want {
			t.Errorf("AgentOf(%q, %q): have reader = %v, want %v (%s)", c.app, c.foreground, got != nil, c.want, c.why)
		}
	}
}

func TestFakeChatStartFiresReady(t *testing.T) {
	chat := fake.NewFakeChat()

	ready := make(chan struct{})
	err := chat.Start(context.Background(), chats.Handlers{
		OnReady: func() { close(ready) },
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("Start must say the owner is known")
	}
	// 走一遍契约里的发帖流程，让各类型形状保持诚实。
	msg, err := chat.Post(context.Background(), "room1", "hello", "claude · main")
	if err != nil || msg == "" {
		t.Fatalf("Post = %q %v", msg, err)
	}
	if _, err := chat.PostDialog(context.Background(), "room1", core.Dialog{Title: "⚠️ Needs you"}, "k1"); err != nil {
		t.Fatal(err)
	}
	if counts := chat.Counts(); counts["Post"] != 1 || counts["PostDialog"] != 1 {
		t.Errorf("counts = %v", counts)
	}
}

func TestFakeHCSnapshotAndSends(t *testing.T) {
	hc := fake.NewFakeHC()
	hc.Terminals["t1"] = hccli.Terminal{
		"address": "t1",
		"status":  map[string]any{"state": "working", "app": "fake-agent"},
		"labels":  map[string]any{"where": "api / claude"},
	}

	view := hccli.NewView()
	hc.FollowEvents(context.Background(), "default", hccli.EventHandlers{
		OnTurn: func(hccli.TurnEvent) { t.Log("turn seen") },
	}, view)

	select {
	case <-hc.SnapshotDone():
	case <-time.After(time.Second):
		t.Fatal("no first snapshot")
	}
	if rec, ok := view.Get("t1"); !ok || rec.State() != "working" || rec.App() != "fake-agent" || rec.Where() != "api / claude" {
		t.Errorf("record = %v ok=%v", rec, ok)
	}
	if err := hc.Send(context.Background(), "default", "t1", "hello", false); err != nil {
		t.Fatal(err)
	}
	if len(hc.Sent) != 1 || hc.Sent[0].Text != "hello" || hc.Sent[0].Session != "default" {
		t.Errorf("sent = %+v", hc.Sent)
	}
	hc.Turn("default", "t1", "blocked")
}
