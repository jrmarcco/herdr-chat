package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
)

const (
	kText    = "text"
	kTs      = "timestamp"
	kItem    = "item"
	itemDone = "item_completed"
	kPhase   = "phase"
	kPayload = "payload"
	kContent = "content"
	kType    = "type"
	agentMsg = "AgentMessage"
	userMsg  = "UserMessage"
	cmdExec  = "CommandExecution"
	fileChg  = "FileChange"
	sayYes   = "yes"
)

func entry(t *testing.T, o map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func at(seconds int) string {
	return time.Date(2026, 11, 12, 0, 0, seconds, 0, time.UTC).Format("2006-01-02T15:04:05.000Z")
}

func itemsOf(t *testing.T, lines []string) []core.Item {
	t.Helper()
	items, _ := core.ItemsAfter(lines, core.StartCursor(at(0)), EntryItems)
	return items
}

func TestEntryItemsShapes(t *testing.T) {
	lines := []string{
		entry(t, map[string]any{kType: "turn_context", kTs: at(1), kPayload: map[string]any{"model": "gpt-5.3"}}),
		entry(t, map[string]any{kType: eventMsg, kTs: at(2), kPayload: map[string]any{kType: itemDone, kItem: map[string]any{
			kType: "UserMessage", kContent: []any{map[string]any{kType: "text", "text": "ship it"}},
		}}}),
		entry(t, map[string]any{kType: eventMsg, kTs: at(3), kPayload: map[string]any{kType: itemDone, kItem: map[string]any{
			kType: agentMsg, kPhase: "commentary", kContent: []any{map[string]any{kType: kText, kText: "working"}},
		}}}),
		entry(t, map[string]any{kType: eventMsg, kTs: at(4), kPayload: map[string]any{kType: itemDone, kItem: map[string]any{
			kType: agentMsg, kPhase: "final_answer", kContent: []any{map[string]any{kType: kText, kText: "done"}},
		}}}),
		entry(t, map[string]any{kType: eventMsg, kTs: at(5), kPayload: map[string]any{kType: itemDone, kItem: map[string]any{
			kType: agentMsg, kPhase: "final_answer", "delivery": "async", kContent: []any{map[string]any{kType: kText, kText: "noted"}},
		}}}),
		entry(t, map[string]any{kType: eventMsg, kTs: at(6), kPayload: map[string]any{kType: itemDone, kItem: map[string]any{
			kType: "CommandExecution", "command": []any{"/bin/zsh", "-lc", "npm test"},
		}}}),
		entry(t, map[string]any{kType: eventMsg, kTs: at(7), kPayload: map[string]any{kType: itemDone, kItem: map[string]any{
			kType: "FileChange", "changes": map[string]any{
				"/pro/a.go": map[string]any{kType: "added"},
				"/pro/b.go": map[string]any{kType: "modified"},
			},
		}}}),
		entry(t, map[string]any{kType: eventMsg, kTs: at(8), kPayload: map[string]any{kType: itemDone, kItem: map[string]any{
			kType: "Reasoning",
		}}}),
	}
	items := itemsOf(t, lines)
	var shape []string
	for _, it := range items {
		end := ""
		if it.End {
			end = "*"
		}
		shape = append(shape, it.Role+end+":"+it.Text)
	}
	want := []string{
		"meta:", // 模型只喂 footer
		"you:ship it",
		"ai:working",
		"ai*:done",              // final_answer 标 end
		"ai:noted",              // 异步的终答不带 end
		"tool:▸ exec: npm test", // -lc 后即命令本身
		"tool:▸ edit: added a.go, modified b.go",
		// reasoning 不见
	}
	if fmt.Sprint(shape) != fmt.Sprint(want) {
		t.Errorf("shape = %v", shape)
	}
	if items[0].Model != "gpt-5.3" {
		t.Errorf("turn_context 的模型进 meta")
	}
}

func TestMeterOfTokenCount(t *testing.T) {
	items := itemsOf(t, []string{
		entry(t, map[string]any{kType: eventMsg, kTs: at(1), kPayload: map[string]any{
			kType: "token_count",
			"info": map[string]any{
				"last_token_usage":     map[string]any{"total_tokens": 234480},
				"model_context_window": 400000,
			},
			"rate_limits": map[string]any{
				"primary":   map[string]any{"used_percent": 57.4, "window_minutes": 10080},
				"secondary": map[string]any{"used_percent": 2.1, "window_minutes": 300},
			},
		}}),
	})
	if len(items) != 1 || items[0].Role != core.RoleMeta {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Context == nil || items[0].Context.Used != 234480 || items[0].Context.Size != 400000 {
		t.Errorf("context = %+v", items[0].Context)
	}
	if len(items[0].Limits) != 2 || items[0].Limits[0].Name != "5-hour" || items[0].Limits[1].Name != "7-day" {
		t.Errorf("limits = %+v ( 按窗口时长升序 )", items[0].Limits)
	}
}

func TestSecondsOfEtime(t *testing.T) {
	cases := map[string]int64{"0:05": 5, "01:02:03": 3723, "2-03:04:05": 183845}
	for etime, want := range cases {
		if got := Seconds(etime); got != want {
			t.Errorf("Seconds(%q) = %d, want %d", etime, got, want)
		}
	}
}

func TestPickLog(t *testing.T) {
	started := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	mk := func(file, cwd, ts string) string {
		return file + "\t" + `"cwd":"` + cwd + `"` + "\t" + `"timestamp":"` + ts + `"`
	}
	list := strings.Join([]string{
		mk(".codex/sessions/2026/11/11/rollout-old.jsonl", "/other", "2026-11-11T08:00:00.000Z"),
		mk(".codex/sessions/2026/11/12/rollout-a.jsonl", "/pro", "2026-11-12T09:59:58.000Z"), // 早 2 秒：在容差内
		mk(".codex/sessions/2026/11/12/rollout-b.jsonl", "/pro", "2026-11-12T10:05:00.000Z"), // 更晚开始
		mk(".codex/sessions/2026/11/12/rollout-c.jsonl", "/pro", "2026-11-09T00:00:00.000Z"), // resumed：太早
		"broken\tline",
	}, "\n")
	if got := PickLog(list, "/pro", started); got != ".codex/sessions/2026/11/12/rollout-a.jsonl" {
		t.Errorf("pickLog = %q ( 启动后最早的那个 )", got)
	}
	if got := PickLog("nothing here", "/pro", started); got != "" {
		t.Errorf("没有匹配 = 空")
	}
}

func TestTitleOf(t *testing.T) {
	cases := map[string]string{
		"codex | /pro":   "codex",
		"⠋ codex | /pro": "codex",
		"[ . ] Action Required | fix the login | /pro": "fix the login",
		"": "",
	}
	for title, want := range cases {
		if got := TitleOf(title); got != want {
			t.Errorf("TitleOf(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestRequestWords(t *testing.T) {
	if got := RequestWords("Bash", map[string]any{"description": "Run tests", "command": []any{"zsh", "-lc", "npm test"}}); got != "Run tests\n$ npm test" {
		t.Errorf("Bash = %q", got)
	}
	if got := RequestWords("mcp__github__issue", map[string]any{"title": "x"}); !strings.Contains(got, "github: issue") {
		t.Errorf("mcp = %q", got)
	}
}

func planLine(t *testing.T, questions string, id string) string {
	t.Helper()
	return entry(t, map[string]any{
		kType: "response_item", kTs: at(10),
		kPayload: map[string]any{
			kType: "function_call", "name": "request_user_input", "call_id": id,
			"arguments": `{"questions":[` + questions + `]}`,
		},
	})
}

func TestQuestionsInPlanAndLater(t *testing.T) {
	q1 := `{"header":"Colors","question":"Which color?","options":[{"label":"Red","description":"warm"},{"label":"Blue"}]}`
	q2 := `{"header":"Size","question":"Which size?","options":[{"label":"S"}]}`
	lines := []string{
		planLine(t, q1+","+q2, "call-1"),
		entry(t, map[string]any{kType: "response_item", kTs: at(11), kPayload: map[string]any{kType: "function_call_output", "call_id": "call-1"}}),
	}
	found := QuestionsIn(lines)
	if found.Plan != nil {
		t.Errorf("function_call_output 之后 Plan 不再等")
	}
	found = QuestionsIn([]string{planLine(t, q1+","+q2, "call-1")})
	if found.Plan == nil || len(found.Plan.Questions) != 2 || found.Plan.ID != "call-1" {
		t.Fatalf("plan = %+v", found.Plan)
	}
	dialog := PlanDialog(found.Plan, 0)
	if dialog == nil || dialog.Key != "call-1#0" || len(dialog.Options) != 3 || dialog.Free != 3 {
		t.Fatalf("planDialog = %+v", dialog)
	}
	if !strings.Contains(dialog.Text, "Colors") || !strings.Contains(dialog.Text, "Red: warm") || !strings.Contains(dialog.Text, "(question 1 of 2)") {
		t.Errorf("text = %q", dialog.Text)
	}
	// task_complete 收走 Plan。
	found = QuestionsIn(append([]string{planLine(t, q1, "call-2")},
		entry(t, map[string]any{kType: eventMsg, kTs: at(12), kPayload: map[string]any{kType: "task_complete"}})))
	if found.Plan != nil {
		t.Errorf("turn 结束没有谁在等")
	}
	// 异步提问：AgentMessage 带 questions；UserMessage 清掉。
	later := entry(t, map[string]any{kType: eventMsg, kTs: at(13), kPayload: map[string]any{kType: itemDone, kItem: map[string]any{
		kType: agentMsg, "id": "it-9", kContent: []any{}, "questions": []any{
			map[string]any{"title": "Ship it?", "options": []any{map[string]any{"label": "Yes"}}},
		},
	}}})
	found = QuestionsIn([]string{later})
	if found.Later == nil || found.Later.ID != "it-9" || len(found.Later.Questions) != 1 {
		t.Fatalf("later = %+v", found.Later)
	}
	dialog = LaterDialog(found.Later, nil)
	if dialog == nil || !dialog.Idle || dialog.Free != 2 || !strings.Contains(dialog.Text, "Ship it?") {
		t.Errorf("laterDialog = %+v", dialog)
	}
	if LaterDialog(found.Later, map[int]string{0: sayYes}) != nil {
		t.Errorf("都答过了就没有卡")
	}
}

func TestQuestionOnScreen(t *testing.T) {
	screen := strings.Join([]string{
		"Answer a few questions",
		"  Question 2/3",
		"› 1. Red",
		"  2. Blue",
	}, "\n")
	on := QuestionOnScreen(screen)
	if on == nil || on.Index != 1 || on.Count != 3 || on.Cursor != 1 || !strings.Contains(on.Words, "Red") {
		t.Errorf("on = %+v", on)
	}
	if QuestionOnScreen("nothing") != nil {
		t.Errorf("没有题就没有")
	}
}

func TestLaterMessage(t *testing.T) {
	if got := LaterMessage([]string{"Ship it?"}, []string{"yes"}); got != "yes" {
		t.Errorf("single = %q", got)
	}
	if got := LaterMessage([]string{"A?", "B?"}, []string{"1", "2"}); got != "A? 1\nB? 2" {
		t.Errorf("multi = %q", got)
	}
}
