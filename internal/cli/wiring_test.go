package cli

import (
	"path/filepath"
	"testing"

	"github.com/jrmarcco/herdr-chat/internal/asks"
	"github.com/jrmarcco/herdr-chat/internal/chats"
	"github.com/jrmarcco/herdr-chat/internal/chats/feishu"
	"github.com/jrmarcco/herdr-chat/internal/config"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/engine"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/readers"
	"github.com/jrmarcco/herdr-chat/internal/readers/claude"
	"github.com/jrmarcco/herdr-chat/internal/readers/codex"
	"github.com/jrmarcco/herdr-chat/internal/readers/opencode"
	"github.com/jrmarcco/herdr-chat/internal/state"
)

// 引擎用的每个 hc 调用都接到真实的 hc 上，
// run 装配的每一块都是能跑的那一块 ( Go 里接口满足是编译期的，这里再把装配本身过一遍 )。
// 真实 hc 适配器满足引擎要的全部 ( 引擎包里有同一断言，这里就近再说一遍 )。
var _ core.HC = (*hccli.CLI)(nil)

func TestTheRealPartsWireUp(t *testing.T) {
	home := t.TempDir()
	hc := hccli.New()

	hc.Env = append(hc.Env, "HOME="+home)
	shared := asks.New(hc, home)

	reg := readers.NewRegistry()
	for _, rd := range []readers.Reader{claude.New(hc, shared), codex.New(hc, shared), opencode.New(hc)} {
		if err := readers.CheckReader(rd); err != nil {
			t.Fatalf("%s: %v", rd.App(), err)
		}
		if err := reg.Register(rd); err != nil {
			t.Fatal(err)
		}
	}
	if got := reg.Apps(); len(got) != 3 {
		t.Fatalf("apps = %v, want the three readers", got)
	}

	store := &state.FileStore{Path: filepath.Join(t.TempDir(), "state.json")}
	chat := feishu.New("cli_app_id", "cli_secret", "", "default", store, t.TempDir())
	if err := chats.CheckChat(chat); err != nil {
		t.Fatal(err)
	}

	// 引擎装配 ( 不 Start：不碰网络与 hc )。
	e := engine.New(chat, hc, reg, store, []string{config.DefaultSession})
	e.History = map[string]bool{"file": true, "folded": true}
	e.Inbox = filepath.Join(t.TempDir(), "inbox")
	e.SetAsks(shared)
	e.SetOwner = func(ownerID string) error {
		return config.SetConfigValue(filepath.Join(t.TempDir(), "config.env"), chat.OwnerKey(), ownerID)
	}
	if e.Chat.Ready() {
		t.Fatal("no owner yet: the engine must know")
	}
	if _, err := core.NewPairingCode(); err != nil {
		t.Fatal(err)
	}
}
