package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/fake"
	"github.com/jrmarcco/herdr-chat/internal/readers"
	"github.com/jrmarcco/herdr-chat/internal/state"
)

// 引擎核心场景的行为测试，全部跑在 Fake 三件套上：
// 快照建房间与回填、活卡片与终答、切卡、deliver 重试、遗言与接管、endGone、入口命令。

// term 构造一条终端记录 ( live、带 where 与 tab 标签、agent 空闲中 )。
func term(address string) map[string]any {
	return map[string]any{
		keyAddress: address,
		keyState:   "live",
		keyStatus:  map[string]any{keyState: "idle", keyApp: appFake},
		keyLabels: map[string]any{
			keyWhere:    whereAPI,
			keySpace:    spaceID,
			keyTabLabel: tabKey,
		},
	}
}

type rig struct {
	e      *Engine
	chat   *fake.FakeChat
	hc     *fake.FakeHC
	reader *fake.FakeReader
	store  *state.MemStore
}

func harness(t *testing.T) *rig {
	t.Helper()
	chat := fake.NewFakeChat()
	hc := fake.NewFakeHC()

	hc.Terminals["t1"] = term("t1")
	reader := fake.NewFakeReader()
	reg := readers.NewRegistry()
	if err := reg.Register(reader); err != nil {
		t.Fatal(err)
	}

	store := &state.MemStore{}
	e := New(chat, hc, reg, store, []string{sessDefault})
	home := t.TempDir()
	e.HomeDir = home
	e.SetAsksHome(home)
	e.Quick = true
	e.ManualTimers = true
	e.StartedAt = time.Now().Add(-time.Hour) // 回填条目不算 "未来"
	reader.SetAfterFn(func(ctx context.Context, session, address string, cur state.Cursor) (*core.Reading, error) {
		return &core.Reading{File: logFile}, nil
	})

	t.Cleanup(e.Stop)
	return &rig{e: e, chat: chat, hc: hc, reader: reader, store: store}
}

func (r *rig) start(t *testing.T) {
	t.Helper()
	if err := r.e.Start(context.Background(), func() {}); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, "建房间", func() bool { return r.countCalls("OpenRoom") == 1 })
}

func (r *rig) waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s ( calls=%v )", what, r.chat.Counts())
}

func (r *rig) countCalls(name string) int {
	return r.chat.Counts()[name]
}

func (r *rig) postsContaining(part string) []fake.Call {
	var out []fake.Call
	for _, c := range r.chat.Calls() {
		if len(c.Args) < 2 {
			continue
		}
		// Post: Args=[room, markdown]；
		// PostCard/EditCard: Args=[.., title, markdown]。
		for _, arg := range c.Args[1:] {
			if md, _ := arg.(string); strings.Contains(md, part) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// say 换 reader 的增量剧本
// ( 按游标契约过滤：只有游标之后、未 seen 的才算新，这本来就是 reader 的职责，剧本要演得像 )。
func (r *rig) say(items ...core.Item) {
	r.reader.SetAfterFn(func(ctx context.Context, session, address string, cur state.Cursor) (*core.Reading, error) {
		until, _ := time.Parse(time.RFC3339, cur.At)
		seen := map[string]bool{}
		for _, pair := range cur.Seen {
			seen[pair[0]] = true
		}
		var fresh []core.Item
		for _, it := range items {
			if it.At.After(until) && !seen[it.ID] {
				fresh = append(fresh, it)
			}
		}
		return &core.Reading{File: logFile, Items: fresh}, nil
	})
}

// setWorking 把终端状态置为 working ( 经事件通路，view 随之更新 )。
func (r *rig) setWorking() {
	r.hc.Change("default", "status", "t1", map[string]any{
		keyStatus: map[string]any{keyState: stateWorking, keyApp: appFake},
	})
}

func (r *rig) roomMessage(text string) {
	st, _ := r.store.Read()
	room := st.Chats["default|t1"].Chat
	r.chat.Handlers.OnMessage(core.Incoming{
		Room: core.Room(room), Where: core.Where{Room: core.Room(room), Message: "m0"},
		Message: "m0", Text: text,
	})
}

func (r *rig) direct(text string) {
	r.chat.Handlers.OnMessage(core.Incoming{Where: core.Where{Message: directMsg}, Message: directMsg, Text: text})
}

func TestSnapshotMakesRoomWithBackfill(t *testing.T) {
	r := harness(t)
	r.reader.SetAfterFn(func(ctx context.Context, session, address string, cur state.Cursor) (*core.Reading, error) {
		if cur.At == "0000-00-00T00:00:00.000Z" {
			// 回填：从头读。
			return &core.Reading{File: logFile, Items: []core.Item{
				{Role: core.RoleYou, Text: "fix it", At: r.e.StartedAt.Add(-time.Minute), ID: "u1"},
				{Role: core.RoleAI, Text: "done", At: r.e.StartedAt.Add(-30 * time.Second), ID: idA1},
			}}, nil
		}
		return &core.Reading{File: logFile}, nil
	})

	r.start(t)
	r.waitFor(t, "回填文件 + 折叠帖", func() bool {
		return r.countCalls("PostFile") == 1 && r.countCalls("PostFolded") == 1
	})

	st, _ := r.store.Read()
	room, ok := st.Chats["default|t1"]
	if !ok || room.Chat == "" || room.Tab != tabKey || room.Name != "api / claude" {
		t.Errorf("room = %+v", room)
	}
}

func TestLiveCardThenFinalAnswer(t *testing.T) {
	r := harness(t)
	r.start(t)

	// working → liveTick 把中间内容发进活卡片。
	r.say(core.Item{Role: core.RoleAI, Text: "thinking aloud", At: time.Now(), ID: idA1})
	r.setWorking()
	r.e.liveTick()
	r.waitFor(t, "活卡片", func() bool { return len(r.postsContaining("thinking aloud")) > 0 })

	st, _ := r.store.Read()
	if st.Cursors["default|t1"].Live == nil {
		t.Fatalf("游标缺活卡片: %+v", st.Cursors)
	}

	// turn 结束 → 终答新消息 + 响铃 + 活卡片关闭。
	r.say(core.Item{Role: core.RoleAI, Text: "the answer", At: time.Now(), ID: idA2, End: true})
	r.hc.Turn(sessDefault, "t1", "done")
	r.waitFor(t, "终答", func() bool { return len(r.postsContaining("the answer")) > 0 })
	r.waitFor(t, "响铃", func() bool { return r.countCalls("Ring") >= 1 })
	closed := false
	for _, c := range r.chat.Calls() {
		if c.Name == callEdit && len(c.Args) >= 2 && c.Args[1] == ClosedTitle {
			closed = true
		}
	}
	if !closed {
		t.Errorf("活卡片未关闭为 %s", ClosedTitle)
	}
}

func TestLiveCardOverflowSplits(t *testing.T) {
	r := harness(t)
	r.chat.LiveBytesVal = 30 // 很小的卡片：一段就满
	r.start(t)

	r.say(core.Item{Role: core.RoleAI, Text: "0123456789abcdefghij", At: time.Now(), ID: idA1})
	r.setWorking()
	r.e.liveTick()
	r.waitFor(t, "第一张活卡片", func() bool { return r.countCalls("PostCard") >= 1 })

	// 第二段：装不下 → 封卡另开。
	r.say(core.Item{Role: core.RoleAI, Text: "second part here", At: time.Now(), ID: idA2})
	r.e.liveTick()
	r.waitFor(t, "切卡 ( continued below + 新卡 )", func() bool {
		full, fresh := 0, 0
		for _, c := range r.chat.Calls() {
			if c.Name == callEdit && len(c.Args) >= 2 && c.Args[1] == FullTitle {
				full++
			}
			if c.Name == "PostCard" && len(c.Args) >= 2 && c.Args[1] == LiveTitle {
				fresh++
			}
		}
		return full >= 1 && fresh >= 2
	})
}

func TestDeliverRetriesPost(t *testing.T) {
	r := harness(t)
	// 前两次失败。
	r.chat.FailPost = 2
	r.start(t)
	r.say(core.Item{Role: core.RoleAI, Text: "after retries", At: time.Now(), ID: idA1, End: true})
	r.hc.Turn(sessDefault, "t1", "done")
	r.waitFor(t, "重试后终答送达", func() bool { return len(r.postsContaining("after retries")) > 0 })
	if r.chat.FailPost != 0 {
		t.Errorf("失败的次数没有用尽: %d", r.chat.FailPost)
	}
}

func TestPostEndLastWordsAndAdoption(t *testing.T) {
	r := harness(t)
	r.reader.SetRestFn(func(ctx context.Context, session, address string, cur state.Cursor) ([]core.Item, error) {
		return []core.Item{{Role: core.RoleAI, Text: "last words", At: time.Now(), ID: "z1"}}, nil
	})
	r.start(t)

	// 先有一轮正常对话，游标里才会有 reader 与日志文件 ( 遗言读的是它 )。
	r.say(core.Item{Role: core.RoleAI, Text: "some work", At: time.Now(), ID: idA1, End: true})
	r.hc.Turn(sessDefault, "t1", "done")
	r.waitFor(t, "终答 ( 建立游标 )", func() bool { return len(r.postsContaining("some work")) > 0 })

	// 终端结束 → 遗言 + 结束语 + 房间标记 ended。
	r.hc.Change("default", "finished", "t1", map[string]any{"reason": "exit"})
	r.waitFor(t, "遗言", func() bool { return len(r.postsContaining("last words")) > 0 })
	r.waitFor(t, "结束语", func() bool { return len(r.postsContaining("Terminal ended")) > 0 })
	r.waitFor(t, "房间标记结束", func() bool {
		st, _ := r.store.Read()
		return st.Chats["default|t1"].Ended
	})

	// 同 tab 的新终端起来 → 接管旧房间，不新建。
	before := r.countCalls("OpenRoom")
	r.hc.SetTerminal("t2", term("t2"))
	r.hc.Change(sessDefault, keyLabels, "t2", map[string]any{
		"labels": map[string]any{"where": "api / claude", "herdr.space": spaceID, "herdr.tab": tabKey},
	})

	r.waitFor(t, "接管通知", func() bool { return len(r.postsContaining("new terminal now (t2)")) > 0 })
	st, _ := r.store.Read()
	if _, old := st.Chats["default|t1"]; old {
		t.Errorf("旧键应已移除")
	}
	if room, ok := st.Chats["default|t2"]; !ok || room.Tab != tabKey {
		t.Errorf("新键应接管房间: %+v", st.Chats)
	}
	if after := r.countCalls("OpenRoom"); after != before {
		t.Errorf("接管不该新建房间 ( before=%d after=%d )", before, after)
	}
}

func TestEndGoneMarksRoomsOfMissingTerminals(t *testing.T) {
	r := harness(t)
	r.start(t)
	r.hc.RemoveTerminal("t1")
	r.e.endGone()

	r.waitFor(t, "endGone 标记结束", func() bool {
		st, _ := r.store.Read()
		return st.Chats["default|t1"].Ended
	})
}

func TestRoomMessageTypesIntoTerminal(t *testing.T) {
	r := harness(t)
	r.start(t)

	r.roomMessage("hello there")
	r.waitFor(t, "消息送入终端", func() bool {
		for _, sent := range r.hc.Sents() {
			if sent.Text == "hello there" {
				return true
			}
		}
		return false
	})
	r.waitFor(t, "回执 ( 工作中 )", func() bool { return r.countCalls("MarkWorking") >= 1 })

	// 你说过的话不再被 reader 重复发出来；ai 的回应作为终答出现。
	r.say(
		core.Item{Role: core.RoleYou, Text: "hello there", At: time.Now(), ID: "u1"},
		core.Item{Role: core.RoleAI, Text: "hi from agent", At: time.Now(), ID: idA1, End: true},
	)

	r.hc.Turn(sessDefault, "t1", "done")
	r.waitFor(t, "ai 的回应", func() bool { return len(r.postsContaining("hi from agent")) > 0 })
	for _, c := range r.chat.Calls() {
		if c.Name == "Post" && len(c.Args) >= 2 {
			if md, _ := c.Args[1].(string); strings.Contains(md, "hello there") {
				t.Errorf("你从聊天发过的话不该再出现: %q", md)
			}
		}
	}
}

func TestDirectCommands(t *testing.T) {
	r := harness(t)
	r.start(t)

	// /ls → fresh 总览。
	r.direct("/ls")
	r.waitFor(t, "fresh 总览", func() bool {
		for _, c := range r.chat.Calls() {
			if c.Name == "ShowOverview" && len(c.Args) >= 2 {
				if fresh, _ := c.Args[1].(bool); fresh {
					return true
				}
			}
		}
		return false
	})

	// 未知命令 → 帮助。
	r.direct("anything")
	r.waitFor(t, "帮助文本", func() bool {
		for _, c := range r.chat.Calls() {
			if c.Name == callReply && len(c.Args) >= 2 {
				if md, _ := c.Args[1].(string); strings.Contains(md, "/new <session>") {
					return true
				}
			}
		}
		return false
	})
}

func TestRestartContinuesSameLiveCard(t *testing.T) {
	r := harness(t)
	r.start(t)

	// 一段工作中的内容 → 活卡片。
	r.say(core.Item{Role: core.RoleAI, Text: "part one", At: time.Now(), ID: idA1})
	r.setWorking()
	r.e.liveTick()
	r.waitFor(t, "活卡片", func() bool { return r.countCalls("PostCard") >= 1 })
	st, _ := r.store.Read()
	live := st.Cursors["default|t1"].Live
	if live == nil {
		t.Fatal("游标缺活卡片")
	}

	// 重启 ( 换一个引擎，同一个 store )：新的增量续编同一张卡。
	r.e.Stop()
	r2 := New(r.chat, r.hc, r.e.Readers, r.store, []string{sessDefault})
	r2.Quick = true
	r2.ManualTimers = true
	r2.StartedAt = time.Now().Add(-time.Hour)
	t.Cleanup(r2.Stop)
	r.say(core.Item{Role: core.RoleAI, Text: "part two", At: time.Now(), ID: idA2})
	if err := r2.Start(context.Background(), func() {}); err != nil {
		t.Fatal(err)
	}

	r.setWorking()
	r2.liveTick()
	deadline := time.Now().Add(3 * time.Second)
	edited := false
	for time.Now().Before(deadline) {
		for _, c := range r.chat.Calls() {
			if c.Name == callEdit && len(c.Args) >= 1 && c.Args[0] == live.Message {
				edited = true
			}
		}
		if edited {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !edited {
		t.Fatalf("重启后应续编同一张活卡片 %s", live.Message)
	}
}
