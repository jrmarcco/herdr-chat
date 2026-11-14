package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/asks"

	"github.com/jrmarcco/herdr-chat/internal/core"
)

// 对话框三通路的行为测试 ( 对应原版 engine.test.mjs 的对话框部分 )：
// 屏幕通路发卡与点击按键、防陈旧卡片、hook 通路 ( agent 自报 )、多选勾选
// 与 Next、离开 blocked 关卡。

// 一屏 Claude 风格的权限对话框。
func permissionScreen(cmd string) string {
	rule := strings.Repeat("─", 60)
	return strings.Join([]string{
		"⏺ Running a command.",
		rule,
		" Bash command",
		" " + cmd,
		strings.Repeat("╌", 60),
		" Do you want to proceed?",
		" ❯ 1. Yes",
		"   2. No",
		"",
		" Esc to cancel · Tab to amend",
	}, "\n")
}

func (r *rig) setBlocked() {
	r.hc.Change(sessDefault, keyStatus, "t1", map[string]any{
		keyStatus: map[string]any{keyState: stateBlocked, keyApp: appFake},
	})
}

func (r *rig) setIdleStatus() {
	r.hc.Change(sessDefault, keyStatus, "t1", map[string]any{
		keyStatus: map[string]any{keyState: "idle", keyApp: appFake},
	})
}

// dialogCard 找到已发出的对话框卡片值 ( FakeChat 记录的 dialogValue )。
func (r *rig) dialogCardValue() map[string]any {
	for _, c := range r.chat.Calls() {
		if c.Name == "PostDialog" && len(c.Args) >= 3 {
			if v, ok := c.Args[2].(map[string]any); ok {
				return v
			}
		}
	}
	return nil
}

// click：按已发出的卡构造一次点击——消息 id 来自 openCards，值来自卡片的
// dialogValue，再叠上 pick/label/ticks/自由输入。
func (r *rig) click(pick, label, answer string) core.Click {
	st, _ := r.store.Read()
	oc, ok := st.OpenCards["default|t1"]
	base := r.dialogCardValue()
	if base == nil {
		base = map[string]any{}
	}
	if !ok {
		// 卡片已被答掉：退回已发出的卡的值 ( 你手里的旧卡就是这么点的 )。
		v := map[string]any{}
		for k, val := range base {
			v[k] = val
		}
		v["pick"] = pick
		if label != "" {
			v["label"] = label
		}
		return core.Click{Message: r.chat.LastPosted(), Value: v, Answer: answer}
	}
	v := map[string]any{}
	for k, val := range base {
		v[k] = val
	}
	v["pick"] = pick
	if label != "" {
		v["label"] = label
	}
	return core.Click{Message: core.Message(oc.Message), Value: v, Answer: answer}
}

// clickValue 用给定的值 ( 刷新后的卡 ) 构造点击。
func clickValue(value core.DialogValue, pick, label, answer string) core.Click {
	v := map[string]any{}
	if m, ok := value.(map[string]any); ok {
		for k, val := range m {
			v[k] = val
		}
	}
	v["pick"] = pick
	if label != "" {
		v["label"] = label
	}
	return core.Click{Value: v, Answer: answer}
}

func TestScreenDialogCardAndClick(t *testing.T) {
	r := harness(t)
	r.start(t)

	// blocked → 读屏发卡 ( 置顶 + 响铃 )，卡片进 state.openCards。
	r.hc.SetScreen("t1", permissionScreen("make test"))
	r.setBlocked()
	r.waitFor(t, "对话框卡片", func() bool { return r.countCalls("PostDialog") == 1 })
	r.waitFor(t, "置顶", func() bool { return r.countCalls("Pin") >= 1 })
	r.waitFor(t, "响铃", func() bool { return r.countCalls("Ring") >= 1 })
	st, _ := r.store.Read()
	if _, ok := st.OpenCards["default|t1"]; !ok {
		t.Fatalf("openCards 缺卡片: %+v", st.OpenCards)
	}
	value := r.dialogCardValue()
	if value == nil || value["k"] == "" || value["s"] != sessDefault {
		t.Fatalf("卡片值 = %v", value)
	}

	// 点 1. Yes → 复核通过 → 按键 "1"。
	result, err := r.chat.Handlers.OnAction(r.click("1", optYes, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Toast, "✔ 1. Yes") {
		t.Errorf("toast = %q", result.Toast)
	}
	pressed := false
	for _, k := range r.hc.KeysPressed() {
		if len(k.Keys) == 1 && k.Keys[0] == "1" {
			pressed = true
		}
	}
	if !pressed {
		t.Errorf("数字键没有按下去: %+v", r.hc.KeysPressed())
	}
}

func TestStaleCardRefreshesNotPresses(t *testing.T) {
	r := harness(t)
	r.start(t)

	r.hc.SetScreen("t1", permissionScreen("make test"))
	r.setBlocked()
	r.waitFor(t, "对话框卡片", func() bool { return r.countCalls("PostDialog") == 1 })

	// 屏幕换成了另一个命令的对话框：旧卡点击不按键，卡片原地刷新。
	r.hc.SetScreen("t1", permissionScreen("rm -rf build"))
	result, err := r.chat.Handlers.OnAction(r.click("1", optYes, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.hc.KeysPressed()) != 0 {
		t.Errorf("旧卡绝不按键: %+v", r.hc.KeysPressed())
	}
	if !strings.Contains(result.Toast, "changed before your tap") {
		t.Errorf("toast = %q", result.Toast)
	}
	if result.Card == nil {
		t.Errorf("卡片该刷新为当前对话框")
	}
	// 再点一次 ( 用刷新后的卡值 )：这次按下去。
	result, err = r.chat.Handlers.OnAction(clickValue(result.Value, "1", "Yes", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.hc.KeysPressed()) == 0 {
		t.Errorf("刷新后的卡该能答: %+v", r.hc.KeysPressed())
	}
	_ = result
}

func TestFreeAnswerAndEsc(t *testing.T) {
	r := harness(t)
	r.start(t)

	r.hc.SetScreen("t1", strings.Join([]string{
		strings.Repeat("─", 40),
		"What should the app be called?",
		"❯ 1. herdrchat",
		"  2. Type something.",
	}, "\n"))
	r.setBlocked()
	r.waitFor(t, "对话框卡片", func() bool { return r.countCalls("PostDialog") == 1 })

	// 不带文字点自由输入项：提醒先输入。
	result, err := r.chat.Handlers.OnAction(r.click("2", "Type something.", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Toast, "Type your answer first") {
		t.Errorf("toast = %q", result.Toast)
	}
	// 带文字：先选中那一项，再把话输进去。
	r.hc.ClearSents()
	if _, err := r.chat.Handlers.OnAction(r.click("2", "Type something.", "shipit")); err != nil {
		t.Fatal(err)
	}
	typed := false
	for _, s := range r.hc.Sents() {
		if s.Text == "shipit" && s.Force {
			typed = true
		}
	}
	if !typed {
		t.Errorf("自由输入没有送达: %+v", r.hc.Sents())
	}

	// Esc。
	r.hc.ClearKeys()
	if _, err := r.chat.Handlers.OnAction(r.click("esc", "", "")); err != nil {
		t.Fatal(err)
	}
	escPressed := false
	for _, k := range r.hc.KeysPressed() {
		if len(k.Keys) == 1 && k.Keys[0] == "esc" {
			escPressed = true
		}
	}
	if !escPressed {
		t.Errorf("Esc 没有按下去")
	}
}

func TestLeavingBlockedClosesCard(t *testing.T) {
	r := harness(t)
	r.start(t)

	r.hc.SetScreen("t1", permissionScreen("make test"))
	r.setBlocked()
	r.waitFor(t, "对话框卡片", func() bool { return r.countCalls("PostDialog") == 1 })

	// 在终端里答掉 ( 状态离开 blocked )：卡片去按钮、写上结论。
	r.hc.SetScreen("t1", "")
	r.setIdleStatus()
	r.waitFor(t, "关卡", func() bool { return r.countCalls("CloseDialog") >= 1 })
	closed := false
	for _, c := range r.chat.Calls() {
		if c.Name == "CloseDialog" && len(c.Args) >= 2 {
			if done, _ := c.Args[1].(string); strings.Contains(done, "Answered") {
				closed = true
			}
		}
	}
	if !closed {
		t.Errorf("关卡结论该是 %q: %+v", DoneTitle, r.chat.Calls())
	}
	st, _ := r.store.Read()
	if _, still := st.OpenCards["default|t1"]; still {
		t.Errorf("openCards 该清掉")
	}
}

func TestAgentDialogThroughItsAPI(t *testing.T) {
	r := harness(t)
	r.start(t)

	// agent 自己上报 ( 它的 API / hook 的等价物 )：不读屏，点击沿 API 回答。
	asked := &core.AgentDialog{
		Key: "ask-1", Kind: "permission", Text: "Run make test?",
		Options: []core.AgentOption{{N: 1, Label: "Yes"}, {N: 2, Label: optNo}},
	}
	r.reader.SetDialogNow(asked)
	r.reader.ClearAnswered()
	// herdrchat.ask 标签出现 → 发卡 ( own 通路 )。
	r.hc.Change(sessDefault, "labels", "t1", map[string]any{
		keyLabels: map[string]any{
			keyWhere: whereAPI, keySpace: spaceID, keyTabLabel: tabKey,
			asks.AskLabel: "1731500000-424242",
		},
	})
	r.waitFor(t, "agent 上报的卡", func() bool { return r.countCalls("PostDialog") == 1 })
	if value := r.dialogCardValue(); value == nil || value["k"] != "ask-1" {
		t.Fatalf("卡片值 = %v", value)
	}

	// 点击 Yes：reader.Answer 收到 pick。
	result, err := r.chat.Handlers.OnAction(r.click("1", optYes, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Toast, "✔") {
		t.Errorf("toast = %q", result.Toast)
	}
	if len(r.reader.Answers()) != 1 || r.reader.Answers()[0].Pick != "1" {
		t.Errorf("answer = %+v", r.reader.Answers())
	}
	// 屏幕通路绝没被碰 ( 没按过键 )。
	if len(r.hc.KeysPressed()) != 0 {
		t.Errorf("agent 上报的卡不碰终端按键: %+v", r.hc.KeysPressed())
	}

	// 它问的换了：点击提醒、卡片刷新，不回答。
	r.reader.SetDialogNow(&core.AgentDialog{
		Key: "ask-2", Kind: "permission", Text: "Run rm -rf?",
		Options: []core.AgentOption{{N: 1, Label: "Yes"}, {N: 2, Label: optNo}},
	})
	result, err = r.chat.Handlers.OnAction(r.click("1", optYes, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.reader.Answers()) != 1 {
		t.Errorf("换问题后不该回答: %+v", r.reader.Answers())
	}
	if !strings.Contains(result.Toast, "something else") {
		t.Errorf("toast = %q", result.Toast)
	}
}

func TestMultiSelectTicksThenNext(t *testing.T) {
	r := harness(t)
	r.start(t)

	on, off := true, false
	asked := &core.AgentDialog{
		Key: "colors", Kind: "question", Text: "Pick colors", Multi: true,
		Options: []core.AgentOption{
			{N: 1, Label: "Red", Checked: &off},
			{N: 2, Label: "Green", Checked: &off},
			{N: 3, Label: "Blue", Checked: &on},
		},
	}
	r.reader.SetDialogNow(asked)
	r.hc.Change(sessDefault, "labels", "t1", map[string]any{
		keyLabels: map[string]any{
			keyWhere: whereAPI, keySpace: spaceID, keyTabLabel: tabKey,
			asks.AskLabel: "1731500000-424243",
		},
	})
	r.waitFor(t, "多选卡", func() bool { return r.countCalls("PostDialog") == 1 })

	// 卡上翻勾 ( 本地，立即回应 )。
	result, err := r.chat.Handlers.OnAction(r.click("1", "Red", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Toast, "Red") {
		t.Errorf("toast = %q", result.Toast)
	}
	if len(r.reader.Answers()) != 0 {
		t.Errorf("翻勾不该回答: %+v", r.reader.Answers())
	}

	// Next 带上勾选状态：一次回答。
	click := r.click("tab", "", "")
	if m, ok := click.Value.(map[string]any); ok {
		m["ticks"] = map[string]any{"1": true, "3": true}
	}
	if _, err := r.chat.Handlers.OnAction(click); err != nil {
		t.Fatal(err)
	}
	if len(r.reader.Answers()) != 1 {
		t.Fatalf("Next 该回答: %+v", r.reader.Answers())
	}
	if fmt.Sprint(r.reader.Answers()[0].Ticks) != "[1 3]" {
		t.Errorf("ticks = %v", r.reader.Answers()[0].Ticks)
	}
}

func TestRestartRestoresOpenCard(t *testing.T) {
	r := harness(t)
	r.start(t)

	r.hc.SetScreen("t1", permissionScreen("make test"))
	r.setBlocked()
	r.waitFor(t, "对话框卡片", func() bool { return r.countCalls("PostDialog") == 1 })
	st, _ := r.store.Read()
	if _, ok := st.OpenCards["default|t1"]; !ok {
		t.Fatal("openCards 缺卡片")
	}

	// 重启：卡片登记回来 ( postedDialog 恢复，同一对话框不会重发 )。
	r.e.Stop()
	r2 := New(r.chat, r.hc, r.e.Readers, r.store, []string{sessDefault})
	r2.Quick = true
	r2.StartedAt = time.Now().Add(-time.Hour)
	t.Cleanup(r2.Stop)
	if err := r2.Start(context.Background(), func() {}); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, "重启完成", func() bool { return r.countCalls("Start") >= 2 })
	// 同一屏 blocked 仍在：sweep 补卡时 postedDialog 已随 openCards 恢复 → 不重发。
	// 稳定采样：计数在两轮间隔后仍是 1 ( 高负载下不等固定时长 )。
	steadyAt := 0
	for i := 0; i < 100; i++ {
		if got := r.countCalls("PostDialog"); got == steadyAt {
			if got > 1 {
				t.Errorf("同一对话框重启后不重发 ( got %d )", got)
				return
			}
			if i > 5 {
				return
			}
		} else {
			steadyAt = got
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 等待中的卡可以关闭 ( closeCard 找得到 )。
	r.setIdleStatus()
	r.waitFor(t, "关卡", func() bool { return r.countCalls("CloseDialog") >= 1 })
}
