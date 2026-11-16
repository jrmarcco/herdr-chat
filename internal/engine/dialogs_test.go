package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/asks"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/readers"
)

// 对话框三通路的行为测试：
// 屏幕通路发卡与点击按键、防陈旧卡片、hook 通路 ( agent 自报 )、
// 多选勾选与 Next、离开 blocked 关卡。
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

// click 按已发出的卡构造一次点击——消息 id 来自 openCards，
// 值来自卡片的dialogValue，再叠上 pick/label/ticks/自由输入。
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
	// 答完要随回调响应整卡换上关卡的样 ( 按钮随之消失——PATCH 换不掉 )。
	if result.Card == nil || result.Card.Tone != core.ToneDone {
		t.Errorf("answered card = %+v, want the done look", result.Card)
	}
	if len(r.reader.Answers()) != 1 || r.reader.Answers()[0].Pick != "1" {
		t.Errorf("answer = %+v", r.reader.Answers())
	}

	// 答完卡片要收场：去按钮、写结论 ( 曾经只回 toast，卡永远停在提问的样子 )。
	if got := r.countCalls("CloseDialog"); got != 1 {
		t.Errorf("answered card was not closed: CloseDialog called %d times", got)
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
	r2.ManualTimers = true
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

// turn 结束是提问卡的确定性收口：
// 即使 hc 的 status 事件不来 ( 离开 blocked 的关卡扑空 )，
// 终答发完也得把卡关掉——点完只回 toast 的话，卡永远橙着。
func TestTurnDoneClosesTheDialogCard(t *testing.T) {
	r := harness(t)
	r.start(t)

	r.hc.SetScreen("t1", permissionScreen("make test"))
	r.setBlocked()
	r.waitFor(t, "对话框卡片", func() bool { return r.countCalls("PostDialog") == 1 })
	if got := r.countCalls("CloseDialog"); got != 0 {
		t.Fatalf("开局不该有关卡: %d", got)
	}

	// turn 结束 ( done )：读屏发终答之外，开着的卡要关。
	r.hc.Turn(sessDefault, "t1", "done")
	r.waitFor(t, "turn 完成关卡", func() bool { return r.countCalls("CloseDialog") >= 1 })
	st, _ := r.store.Read()
	if _, still := st.OpenCards["default|t1"]; still {
		t.Fatalf("openCards 还留着: %+v", st.OpenCards)
	}
}

const kindDialog = "dialog"

// 没装 hook 的机器：ErrNoHook 问一次就走，
// 不把 DRAW_TRIES 花在必无答案的探测上 ( sweep 每分钟都会重来 )。
func TestPostBlockedAsksTheAgentOnceWhenNoHook(t *testing.T) {
	r := harness(t)
	r.reader.DialogErr = readers.ErrNoHook
	r.hc.SetScreen(addrT1, " It waits for a yes or a no.")
	// status 照生产的样子带上 kind/msg ( claude 总是说 "dialog open" )。
	r.hc.Change(sessDefault, keyStatus, addrT1, map[string]any{
		keyStatus: map[string]any{keyState: stateBlocked, keyApp: appFake, "kind": kindDialog, "msg": "dialog open"},
	})

	if err := r.e.postBlocked(sessDefault, addrT1, r.hc.Terminals[addrT1], false, false); err != nil {
		t.Fatalf("postBlocked: %v", err)
	}
	if got := r.reader.DialogCalls(); got != 1 {
		t.Fatalf("Dialog asked %d times, want 1", got)
	}
	if r.countCalls("Post") != 1 {
		t.Fatalf("posts = %d, want the no-menu notice", r.countCalls("Post"))
	}
}

// 无菜单的屏两次读到一致即采信，不再读满 DRAW_TRIES 次
// ( blocked 期间 sweep 每分钟重来，读满是纯空转 )。
func TestPostBlockedSettlesOnAStableNoMenuScreen(t *testing.T) {
	r := harness(t)
	r.reader.DialogErr = readers.ErrNoHook
	r.hc.SetScreen(addrT1, " It waits for a yes or a no.")
	r.setBlocked()

	if err := r.e.postBlocked(sessDefault, addrT1, r.hc.Terminals[addrT1], false, false); err != nil {
		t.Fatalf("postBlocked: %v", err)
	}
	if got := r.hc.ScreenReads(); got != 2 {
		t.Fatalf("screen read %d times, want 2 (a look and its confirm)", got)
	}
}

// forgetDialog 真的忘记：发卡失败后，同名的对话框还能再发。
func TestForgetDialogForgetsTheClaim(t *testing.T) {
	r := harness(t)
	k := r.e.keyOf(sessDefault, addrT1)
	d := core.Dialog{Menu: &core.Menu{Key: "abc"}}
	if !r.e.claimDialog(k, d, nil) {
		t.Fatal("the first claim must win")
	}
	if r.e.claimDialog(k, d, nil) {
		t.Fatal("the second claim must lose")
	}
	r.e.forgetDialog(k, dialogName(d, nil))
	if !r.e.claimDialog(k, d, nil) {
		t.Fatal("after forgetting, the claim must win again")
	}
}

// status 没说 kind/msg 的无菜单对话框：名字有兜底，
// 不再与 postedDialog 的零值 ( 从未发过 ) 撞车——第一张卡发得出去。
func TestPostBlockedNamesANamelessDialog(t *testing.T) {
	r := harness(t)
	r.reader.DialogErr = readers.ErrNoHook
	r.hc.SetScreen(addrT1, " It waits for a yes or a no.")
	r.setBlocked() // 无 kind/msg：曾经连名字都是空的

	if err := r.e.postBlocked(sessDefault, addrT1, r.hc.Terminals[addrT1], false, false); err != nil {
		t.Fatalf("postBlocked: %v", err)
	}
	if r.countCalls("Post") != 1 {
		t.Fatalf("posts = %d, want the notice even without kind/msg", r.countCalls("Post"))
	}
	if got := r.e.postedDialog[r.e.keyOf(sessDefault, addrT1)]; got != kindDialog {
		t.Fatalf("claimed name = %q, want the fallback", got)
	}
}

// 「Enter to continue · Esc to cancel」形状的告知：合成按钮卡——
// 走 PostDialog ( 不是文字 )、进 openCards；点击按 enter，绝不按数字。
func TestConfirmNoticeBecomesAButtonCard(t *testing.T) {
	r := harness(t)
	r.start(t)

	r.hc.SetScreen(addrT1, strings.Join([]string{
		" We're changing auto mode.",
		"",
		" Nothing breaks: auto mode keeps working.",
		"",
		" Enter to continue · Esc to cancel",
	}, "\n"))
	r.setBlocked()
	r.waitFor(t, "合成按钮卡", func() bool { return r.countCalls("PostDialog") == 1 })
	if r.countCalls("Post") != 0 {
		t.Fatalf("posts = %d, want the button card instead of text", r.countCalls("Post"))
	}
	st, _ := r.store.Read()
	if _, ok := st.OpenCards["default|t1"]; !ok {
		t.Fatal("the synthesized card must be an open card")
	}

	// 点 1. Continue → 复核同 key → 按 enter。
	result, err := r.chat.Handlers.OnAction(r.click("1", "Continue (Enter)", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Toast, "✔ 1. Continue (Enter)") {
		t.Fatalf("toast = %q", result.Toast)
	}
	pressed := false
	for _, k := range r.hc.KeysPressed() {
		if k.Address != addrT1 {
			continue
		}
		if len(k.Keys) == 1 && k.Keys[0] == "enter" {
			pressed = true
		}
		if len(k.Keys) == 1 && k.Keys[0] == "1" {
			t.Fatal("a synthesized card must never press digits")
		}
	}
	if !pressed {
		t.Fatalf("enter was not pressed: %+v", r.hc.KeysPressed())
	}
}

// 光标选择 ( ❯ ) 的确认屏绝不合成按钮——Enter 跟光标走，按什么全看它指着哪。
func TestCursorChoiceStaysATextCard(t *testing.T) {
	r := harness(t)
	r.reader.DialogErr = readers.ErrNoHook
	r.hc.SetScreen(addrT1, trustScreen) // intake_confirm_test.go 的真实信任屏
	r.hc.Change(sessDefault, keyStatus, addrT1, map[string]any{
		keyStatus: map[string]any{keyState: stateBlocked, keyApp: appFake, "kind": kindDialog, "msg": "trust"},
	})

	if err := r.e.postBlocked(sessDefault, addrT1, r.hc.Terminals[addrT1], false, false); err != nil {
		t.Fatalf("postBlocked: %v", err)
	}
	if r.countCalls("PostDialog") != 0 {
		t.Fatalf("PostDialog = %d, a cursor choice must not get buttons", r.countCalls("PostDialog"))
	}
	if r.countCalls("Post") != 1 {
		t.Fatalf("posts = %d, want the plain screen notice", r.countCalls("Post"))
	}
}
