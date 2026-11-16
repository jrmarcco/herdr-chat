package feishu

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/state"
)

// 飞书卡片行为测试：
// chunk 的围栏切分、header 的emoji 剥离与色调、menuCard 的按钮/表单、
// doneCard、折叠卡、tagName、ticksOf、messageParts。
const (
	menuKey = "k1"
	optYes  = "Yes"
	optNo   = "No"
)

func TestChunkSplitsOnLines(t *testing.T) {
	// 一段短文原样一段。
	if got := chunk("hello\nworld", 6000); len(got) != 1 || got[0] != "hello\nworld" {
		t.Errorf("chunk = %q", got)
	}
	// 超长按行界切分。
	var lines []string
	for range 100 {
		lines = append(lines, strings.Repeat("x", 10))
	}
	parts := chunk(strings.Join(lines, "\n"), 100)
	if len(parts) < 10 {
		t.Errorf("parts = %d", len(parts))
	}
	for i, p := range parts {
		if len(p) > 101 {
			t.Errorf("part %d len = %d", i, len(p))
		}
	}
}

func TestChunkClosesAndReopensFence(t *testing.T) {
	// 一个代码围栏被切断：前段闭合，后段重开。
	inFence := "```go\n" + strings.Repeat("a\n", 100) + "```"
	parts := chunk(inFence, 100)
	if len(parts) < 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	if !strings.HasSuffix(parts[0], "```") {
		t.Errorf("第一段该闭围栏: %q…", tail(parts[0], 20))
	}
	if !strings.HasPrefix(parts[1], "```") {
		t.Errorf("第二段该重开围栏: %q…", head(parts[1], 20))
	}
	// 拼回检查：中段的围栏重开行 + 前段的围栏闭合行抵消，其余行按序都在。
	var allLines []string
	for _, p := range parts {
		allLines = append(allLines, strings.Split(p, "\n")...)
	}
	// 去掉闭合 ( "```" ) 与重开 ( "```go" ) 相邻的对，其余应与原文的行一致。
	var kept []string
	for i := 0; i < len(allLines); i++ {
		if allLines[i] == "```" && i+1 < len(allLines) && strings.HasPrefix(allLines[i+1], "```") {
			// 闭合与重开相抵。
			i++
			continue
		}
		kept = append(kept, allLines[i])
	}
	if strings.Join(kept, "\n") != inFence {
		t.Errorf("拼回不等于原文\nkept = %q…", head(strings.Join(kept, "\n"), 60))
	}
}

func tail(s string, n int) string {
	return s[max(0, len(s)-n):]
}

func head(s string, n int) string {
	return s[:min(n, len(s))]
}

func TestHeaderStripsEmojiAndUsesTone(t *testing.T) {
	h := cardHeader("⌛️ Working…", core.ToneWorking)
	header, _ := h["title"].(map[string]any)
	if header[kContent] != "Working…" {
		t.Errorf("title = %q ( emoji 剥掉 )", header["content"])
	}
	if h["template"] != "blue" {
		t.Errorf("working = blue")
	}
	h = cardHeader("⚠️ Needs you", core.ToneNeeds)
	if h["template"] != "orange" {
		t.Errorf("needs = orange")
	}
	icon, _ := h["icon"].(map[string]any)
	if icon["token"] != "warning_outlined" {
		t.Errorf("icon = %v", icon)
	}
}

func TestMenuCardShortOptionsSideBySide(t *testing.T) {
	menu := &core.Menu{
		Key: menuKey, Framed: true,
		Options: []core.MenuOption{
			{N: 1, Label: optYes},
			{N: 2, Label: optNo},
		},
	}
	value := map[string]any{"s": "default", "a": "t1", "k": menuKey}
	card := MenuCard("⚠️ Needs you", "Proceed?", menu, value, "")
	body, _ := card[kBody].(map[string]any)
	elements, _ := body[kElements].([]any)
	// 第二个元素是并排按钮 ( column_set flow )。
	row, ok := elements[1].(map[string]any)
	if !ok || row["tag"] != "column_set" || row["flex_mode"] != "flow" {
		t.Fatalf("elements[1] = %+v ( 短选项应并排 )", elements[1])
	}
	columns, _ := row["columns"].([]any)
	if len(columns) != 2 {
		t.Errorf("columns = %d", len(columns))
	}
	// 第三个元素是 Esc 按钮 ( 也并排 )。
	escRow, _ := elements[2].(map[string]any)
	escCols, _ := escRow["columns"].([]any)
	firstCol, _ := escCols[0].(map[string]any)
	colEls, _ := firstCol["elements"].([]any)
	escBtn, _ := colEls[0].(map[string]any)
	if escBtn["type"] != "danger_text" {
		t.Errorf("esc = %v", escBtn["type"])
	}
}

func TestMenuCardLongOptionsOnePerLine(t *testing.T) {
	menu := &core.Menu{
		Key: "k2", Framed: true,
		Options: []core.MenuOption{
			{N: 1, Label: optYes},
			{N: 2, Label: "Yes, and always allow access to this project from now on"},
			{N: 3, Label: "No"},
		},
	}
	card := MenuCard("title", "text", menu, map[string]any{}, "")
	body, _ := card[kBody].(map[string]any)
	elements, _ := body[kElements].([]any)
	// 长选项 ( >14 字符 ) 各自一行，不是 column_set。
	for i := 1; i <= 3; i++ {
		if el, ok := elements[i].(map[string]any); ok && el["tag"] == "column_set" {
			t.Errorf("长选项不该并排 ( element %d )", i)
		}
	}
}

func TestMenuCardMultiBoxesAndNext(t *testing.T) {
	off := false
	on := true
	menu := &core.Menu{
		Key: "k3", Multi: true,
		Options: []core.MenuOption{
			{N: 1, Label: "Red", Checked: &off},
			{N: 2, Label: "Green", Checked: &on},
			{N: 3, Label: "Blue", Checked: &off},
			{N: 4, Label: "Type something"},
		},
		Free: &core.MenuOption{N: 4, Label: "Type something"},
	}
	card := MenuCard("title", "text", menu, map[string]any{}, "")
	body, _ := card[kBody].(map[string]any)
	elements, _ := body[kElements].([]any)
	// 找 form "boxes" ( 3 个 checker + Next ) 与 form "free" ( input + Send )。
	var boxForm, freeForm map[string]any
	for _, e := range elements {
		if el, ok := e.(map[string]any); ok && el["tag"] == "form" {
			switch el["name"] {
			case "boxes":
				boxForm = el
			case "free":
				freeForm = el
			}
		}
	}
	if boxForm == nil || freeForm == nil {
		t.Fatalf("缺表单: boxes=%v free=%v", boxForm != nil, freeForm != nil)
	}
	inner, _ := boxForm[kElements].([]any)
	// 3 个 checker + Next 按钮。
	checkers := 0
	for _, e := range inner {
		if el, ok := e.(map[string]any); ok && el["tag"] == "checker" {
			checkers++
		}
	}
	if checkers != 3 {
		t.Errorf("checkers = %d", checkers)
	}
	last, _ := inner[len(inner)-1].(map[string]any)
	if last["form_action_type"] != "submit" {
		t.Errorf("Next 该是 submit")
	}
	// free 表单有 input 与 Send。
	freeEls, _ := freeForm[kElements].([]any)
	first, _ := freeEls[0].(map[string]any)
	if first[kTag] != "input" {
		t.Errorf("free[0] = %+v", freeEls[0])
	}
	second, _ := freeEls[1].(map[string]any)
	if second["form_action_type"] != "submit" {
		t.Errorf("Send 该是 submit")
	}
}

func TestFooterIsEntityEscaped(t *testing.T) {
	card := PlainCard("text", "⌛️ Working…", core.ToneWorking, "claude · main · context 23% <&>")
	body, _ := card[kBody].(map[string]any)
	elements, _ := body[kElements].([]any)
	footer, _ := elements[1].(map[string]any)
	content, _ := footer[kContent].(string)
	if !strings.Contains(content, "&#60;") || !strings.Contains(content, "&#38;") {
		t.Errorf("footer = %q ( < & 该成实体 )", content)
	}
	if footer["text_size"] != "notation" {
		t.Errorf("text_size = %v", footer["text_size"])
	}
}

func TestDoneCardStripsLeadingCheck(t *testing.T) {
	card := DoneCard("✔ Answered.", "the dialog text", "✔ You picked Yes.", "footer")
	body, _ := card[kBody].(map[string]any)
	elements, _ := body[kElements].([]any)
	done, _ := elements[1].(map[string]any)
	c, _ := done[kContent].(string)
	if c != "You picked Yes." {
		t.Errorf("done = %q ( 去掉领头的 ✔ )", c)
	}
}

func TestFoldedCardIsCollapsiblePanel(t *testing.T) {
	card := FoldedCard("📜 Before this room", "the history")
	body, _ := card[kBody].(map[string]any)
	elements, _ := body[kElements].([]any)
	panel, _ := elements[0].(map[string]any)
	if panel["tag"] != "collapsible_panel" || panel["expanded"] != false {
		t.Errorf("panel = %+v", panel)
	}
}

func TestTagNameCutsAtLastSeparator(t *testing.T) {
	if got := TagName("build-agent"); got != "build-agent" {
		t.Errorf("短的 = %q", got)
	}
	if got := TagName("build-agent-server-long"); got != "build-agent" {
		t.Errorf("长的 = %q ( 在最后的 - 截 )", got)
	}
	if got := TagName("averyveryverylongname"); got != "averyveryverylon" {
		t.Errorf("无分隔符 = %q ( 硬截 16 )", got)
	}
}

func TestTicksOfReadsBoxes(t *testing.T) {
	ticks := TicksOf(map[string]any{
		"box_1": true, "box_2": "false", "box_3": "true", "answer": "words",
	})
	if ticks == nil || !ticks["1"] || ticks["2"] || !ticks["3"] || len(ticks) != 3 {
		t.Errorf("ticks = %+v", ticks)
	}
	if TicksOf(map[string]any{"answer": "x"}) != nil {
		t.Errorf("一个框都没有 = nil")
	}
	if TicksOf(nil) != nil {
		t.Errorf("空表单 = nil")
	}
}

func TestMessageParts(t *testing.T) {
	text, images, ok := MessageParts("text", `{"text":"hello @_user_1"}`)
	if !ok || text != "hello @_user_1" || len(images) != 0 {
		t.Errorf("text: %q %v %v", text, images, ok)
	}
	_, images, ok = MessageParts("image", `{"image_key":"img_v2_abc"}`)
	if !ok || len(images) != 1 || images[0] != "img_v2_abc" {
		t.Errorf("image: %v %v", images, ok)
	}
	post := `{"title":"T","content":[[{"tag":"text","text":"hi "},{"tag":"a","text":"link","href":"https://x"},{"tag":"img","image_key":"k1"}]]}`
	text, images, ok = MessageParts("post", post)
	if !ok || text != "T\nhi link (https://x)" || len(images) != 1 || images[0] != "k1" {
		t.Errorf("post: %q %v %v", text, images, ok)
	}
	if _, _, ok := MessageParts("file", `{"file_key":"f"}`); ok {
		t.Errorf("文件类型不传")
	}
	if _, _, ok := MessageParts("text", "not json"); ok {
		t.Errorf("非 JSON 不传")
	}
}

func TestMenuCardJSONShape(t *testing.T) {
	// 整张卡序列化成 JSON 后有 schema 2.0、header、update_multi。
	menu := &core.Menu{Key: "k", Options: []core.MenuOption{{N: 1, Label: optYes}, {N: 2, Label: optNo}}}
	card := MenuCard("title", "text", menu, map[string]any{}, "")
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if json.Unmarshal(raw, &parsed) != nil {
		t.Fatal("bad json")
	}
	if parsed["schema"] != "2.0" {
		t.Errorf("schema = %v", parsed["schema"])
	}
	config, _ := parsed["config"].(map[string]any)
	if config["update_multi"] != true {
		t.Errorf("update_multi = %v", config["update_multi"])
	}
}

// 一个 chat 的第一条消息必须被处理：
// 曾经这里向无缓冲通道发送、抱着 chat 的锁死住，
// 之后所有消息全部卡死 ( 零日志 ) —— 回归锁。
func TestTheFirstMessageOfAChatIsHandled(t *testing.T) {
	c := New("a", "b", "", "t", &state.MemStore{}, t.TempDir())
	var mu sync.Mutex
	var handled []string
	var wg sync.WaitGroup
	for _, id := range []string{"m1", "m2", "m3"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			c.enqueue("chat1", func() {
				mu.Lock()
				defer mu.Unlock()
				handled = append(handled, id)
			})
		}(id)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("messages stuck: the first message of a chat deadlocked the queue")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(handled) != 3 {
		t.Fatalf("handled = %v, want all three", handled)
	}
}

// /new 的开终端卡：目录输入与 agent 下拉同一张表单 ( 按钮点击不带输入，
// 表单提交才带 )，Start 一次带齐两者；Esc 在表单外。
func TestNewCardAsksFolderAndAgentInOneForm(t *testing.T) {
	card := NewCard("🆕 New terminal", "Which folder?", "Working folder, e.g. Projects/foo", "",
		[]string{"claude", "codex"}, map[string]any{"k": "new-terminal"})
	body, _ := card[kBody].(map[string]any)
	elements, _ := body[kElements].([]any)
	form, _ := elements[1].(map[string]any)
	if form[kTag] != "form" {
		t.Fatalf("second element = %v, want the form", form)
	}
	raw, _ := json.Marshal(card)
	s := string(raw)
	for _, want := range []string{
		`"name":"answer"`, `"select_static"`, `"name":"agent"`,
		`"value":"codex"`, `"initial_option":"claude"`,
		`"form_action_type":"submit"`, `"esc"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("no %s in card", want)
		}
	}
}
