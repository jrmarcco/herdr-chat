package feishu

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/state"
)

// 租户标签重名 ( 402 拿不到 id )：装饰退位——
// 不绑标签、不报错、不拦房间的其他事；冷却一过再试，建成了就绑上。

// scopeDefault 是本测试反复用的 scope 名 ( hc 的 default session )。
const scopeDefault = "default"

// dupTagLark 只会建标签：dup 时一律重名，否则真建 ( 测试可切换 )。
type dupTagLark struct {
	LarkAPI
	dup     bool
	created int
	bound   int
}

func (l *dupTagLark) CreateTag(_ context.Context, name string) (string, error) {
	l.created++
	if l.dup {
		return "", &ErrDuplicateTag{Name: name}
	}
	return "tag_" + name, nil
}

func (l *dupTagLark) BindTag(_ context.Context, tagID, _ string) error {
	l.bound++
	if tagID == "" {
		return errors.New("bind with no tag id")
	}
	return nil
}

func TestPlaceRoomDegradesWhenTheTagNameIsTaken(t *testing.T) {
	store := &state.MemStore{}
	st, _ := store.Read()
	st.Chats["k"] = state.Room{Chat: "oc_1", Name: "dev / jit"}
	_ = store.Write(st)

	c := New("app", "secret", "ou_owner", scopeDefault, store, t.TempDir())
	lark := &dupTagLark{dup: true}
	c.client = lark
	ctx := context.Background()

	if err := c.PlaceRoom(ctx, "k", core.Place{Session: scopeDefault}); err != nil {
		t.Fatalf("PlaceRoom: %v (decoration must not block the room)", err)
	}
	if lark.created != 1 || lark.bound != 0 {
		t.Fatalf("created %d bound %d, want one try and no bind", lark.created, lark.bound)
	}

	// 冷却内不再撞。
	if err := c.PlaceRoom(ctx, "k", core.Place{Session: scopeDefault}); err != nil {
		t.Fatal(err)
	}
	if lark.created != 1 {
		t.Fatalf("created %d within the cooldown, want no retry", lark.created)
	}

	// 控制台删掉孤儿标签后：冷却一过，建成了就绑上、id 记进 state。
	lark.dup = false
	c.tagDupAt[scopeDefault] = time.Now().Add(-2 * tagDupCooldown)
	if err := c.PlaceRoom(ctx, "k", core.Place{Session: scopeDefault}); err != nil {
		t.Fatal(err)
	}
	if lark.created != 2 || lark.bound != 1 {
		t.Fatalf("created %d bound %d, want a fresh tag bound", lark.created, lark.bound)
	}
	env, _ := store.Read()
	if env.Tags[scopeDefault] != "tag_default" {
		t.Fatalf("tags = %v, want the id kept", env.Tags)
	}
}
