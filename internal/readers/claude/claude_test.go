package claude

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
)

const (
	kUUID         = "uuid"
	kName         = "name"
	kInput        = "input"
	kOrigin       = "origin"
	kKind         = "kind"
	kThinking     = "thinking"
	kUser         = "user"
	kDesc         = "description"
	stopEnd       = "end_turn"
	thinkWord     = "thinking"
	narrationWord = "narration"
)

// 重复字面量收拢为常量 ( goconst )。
const (
	kTimestamp = "timestamp"
	kContent   = "content"
	kType      = "type"
	kStop      = "stop_reason"
	kToolUse   = "tool_use"
	kToolRes   = "tool_result"
	kAssistant = "assistant"
	kText      = "text"
	kModel     = "model"
	kHuman     = "human"
	kMessage   = "message"
	kSig       = "signature"
	kPrompt    = "prompt"
	kAttach    = "attachment"
	kModelX    = "claude-model-x"
	msgStaging = "also staging"
)

// 转录自原版 test/readers.test.mjs：顺序与过滤、narration、工具行、你的回答、
// 图片、模型与上下文、窗口与限额；加上 Task 子代理卡 ( FINAL_PLAN §2.B )。

func at(seconds int) string {
	return time.Date(2026, 11, 12, 0, 0, seconds, 0, time.UTC).Format("2006-01-02T15:04:05.000Z")
}

func entry(t *testing.T, o map[string]any) string {
	t.Helper()
	t.Helper()
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func human(t *testing.T, seconds int, text, uuid string) string {
	t.Helper()
	return entry(t, map[string]any{
		kType: kUser, kUUID: uuid, kTimestamp: at(seconds),
		kOrigin:  map[string]any{kKind: kHuman},
		kMessage: map[string]any{kContent: text},
	})
}

func aiSay(t *testing.T, seconds int, text, uuid string, end bool) string {
	t.Helper()
	stop := "tool_use"
	if end {
		stop = "end_turn"
	}
	return entry(t, map[string]any{
		kType: kAssistant, kUUID: uuid, kTimestamp: at(seconds),
		kMessage: map[string]any{kStop: stop, kContent: []any{map[string]any{kType: kText, kText: text}}},
	})
}

func conversationAfter(t *testing.T, lines []string, from string) ([]core.Item, bool) {
	t.Helper()
	items, complete := core.ItemsAfter(lines, core.StartCursor(from), EntryItems)
	return items, complete
}

func rolesTexts(items []core.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Role+": "+it.Text)
	}
	return out
}

func TestConversationKeepsYourPromptsAnswersInOrder(t *testing.T) {
	lines := []string{
		`{"cut`,
		human(t, 1, "before the start", "u1"),
		human(t, 3, "deploy it", "u2"),
		entry(t, map[string]any{kType: kUser, kUUID: "m", kTimestamp: at(4), "isMeta": true, kMessage: map[string]any{kContent: "[Image: source]"}}),
		entry(t, map[string]any{kType: kAssistant, kUUID: "a1", kTimestamp: at(5), kMessage: map[string]any{kContent: []any{
			map[string]any{kType: kText, kText: "Checking."},
			map[string]any{kType: kToolUse, "id": "x"},
		}}}),
		entry(t, map[string]any{kType: kUser, kUUID: "r", kTimestamp: at(6), kMessage: map[string]any{kContent: []any{
			map[string]any{kType: kToolRes, "tool_use_id": "x", kContent: "ok"},
		}}}),
		entry(t, map[string]any{kType: "queue-operation", "operation": "enqueue", kTimestamp: at(7), "content": "also staging"}),
		entry(t, map[string]any{kType: "queue-operation", "operation": "remove", "reason": "absorbed_mid_turn", kTimestamp: at(7), "content": "also staging"}),
		entry(t, map[string]any{kType: kAttach, kUUID: "q1", kTimestamp: at(7), kAttach: map[string]any{
			kType: "queued_command", kPrompt: msgStaging, kOrigin: map[string]any{kKind: kHuman},
		}}),
		entry(t, map[string]any{kType: kAttach, kUUID: "q2", kTimestamp: at(7), kAttach: map[string]any{
			kType: "queued_command", kPrompt: "<task-notification>done</task-notification>", kOrigin: map[string]any{kKind: "task-notification"},
		}}),
		entry(t, map[string]any{kType: kUser, kUUID: "n", kTimestamp: at(7), kOrigin: map[string]any{kKind: "task-notification"}, kMessage: map[string]any{kContent: "<task-notification>done</task-notification>"}}),
		entry(t, map[string]any{kType: kAssistant, kUUID: "s", kTimestamp: at(8), "isSidechain": true, kMessage: map[string]any{kContent: []any{map[string]any{kType: kText, kText: "subagent noise"}}}}),
		entry(t, map[string]any{kType: kUser, kUUID: "i", kTimestamp: at(8), kMessage: map[string]any{kContent: "<system-reminder>injected</system-reminder>"}}),
		aiSay(t, 9, "Done.", "a2", true),
	}
	items, complete := conversationAfter(t, lines, at(2))
	want := []string{
		"you: deploy it",
		"ai: Checking.",
		"tool: ▸ tool",
		"you: also staging",
		"ai: Done.",
	}
	if fmt.Sprint(rolesTexts(items)) != fmt.Sprint(want) {
		t.Errorf("items = %v", rolesTexts(items))
	}
	for i, it := range items {
		wantEnd := i == len(items)-1
		if it.End != wantEnd {
			t.Errorf("item %d end = %v", i, it.End)
		}
	}
	ids := map[string]bool{}
	for _, it := range items {
		if ids[it.ID] {
			t.Errorf("id 重复: %s", it.ID)
		}
		ids[it.ID] = true
	}
	if !complete {
		t.Errorf("tail 够到游标之前，complete")
	}
}

func TestNarrationCountsPlainThinkingDoesNot(t *testing.T) {
	sig := func(word string) string {
		return base64.StdEncoding.EncodeToString([]byte("\x08\x04" + word + "\x12xyz"))
	}
	lines := []string{
		entry(t, map[string]any{kType: kAssistant, kUUID: "t1", kTimestamp: at(10), kMessage: map[string]any{kStop: kToolUse, kContent: []any{
			map[string]any{kType: kThinking, kThinking: "private reasoning", kSig: sig(thinkWord)},
		}}}),
		entry(t, map[string]any{kType: kAssistant, kUUID: "n1", kTimestamp: at(10), kMessage: map[string]any{kStop: kToolUse, kContent: []any{
			map[string]any{kType: kThinking, kThinking: "Found the cause; fixing it.", kSig: sig(narrationWord)},
		}}}),
		entry(t, map[string]any{kType: kAssistant, kUUID: "e1", kTimestamp: at(11), kMessage: map[string]any{kStop: kToolUse, kContent: []any{
			map[string]any{kType: kThinking, kThinking: "", kSig: sig(narrationWord)},
		}}}),
	}
	items, _ := conversationAfter(t, lines, at(0))
	if len(items) != 1 || items[0].Text != "Found the cause; fixing it." {
		t.Errorf("items = %+v", items)
	}
}

func TestToolCallsOneLineEach(t *testing.T) {
	lines := []string{
		entry(t, map[string]any{kType: kAssistant, kUUID: "b1", kTimestamp: at(10), kMessage: map[string]any{kStop: kToolUse, kContent: []any{
			map[string]any{kType: kToolUse, kName: "Bash", kInput: map[string]any{"command": "npm test", kDesc: "Run the unit tests"}},
		}}}),
		entry(t, map[string]any{kType: kAssistant, kUUID: "r1", kTimestamp: at(11), kMessage: map[string]any{kStop: kToolUse, kContent: []any{
			map[string]any{kType: kToolUse, kName: "Read", kInput: map[string]any{"file_path": "/tmp/a.js"}},
		}}}),
	}
	items, _ := conversationAfter(t, lines, at(0))
	want := []string{"▸ Bash: Run the unit tests", "▸ Read: /tmp/a.js"}
	var texts []string
	for _, it := range items {
		texts = append(texts, it.Text)
	}
	if fmt.Sprint(texts) != fmt.Sprint(want) {
		t.Errorf("lines = %v", texts)
	}
}

func TestYourAnswersToItsQuestionsAreYours(t *testing.T) {
	line := entry(t, map[string]any{
		kType: kUser, kUUID: "q1", kTimestamp: at(10),
		"message":       map[string]any{kContent: []any{map[string]any{kType: kToolRes, kContent: "answered"}}},
		"toolUseResult": map[string]any{"answers": map[string]any{"Pick colors": "Red, Blue", "Pick a size": "Medium"}},
	})
	items, _ := conversationAfter(t, []string{line}, at(0))
	if len(items) != 1 || items[0].Role != core.RoleYou {
		t.Fatalf("items = %+v", items)
	}
	// Go 的 map 无 JSON 插入序：两行都在即可。
	for _, part := range []string{"Pick colors → Red, Blue", "Pick a size → Medium"} {
		if !strings.Contains(items[0].Text, part) {
			t.Errorf("答案缺 %q: %q", part, items[0].Text)
		}
	}
}

func TestImagesYoursAndItsNeverMetaOrSubagent(t *testing.T) {
	img := func(data string) map[string]any {
		return map[string]any{kType: "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": data}}
	}
	lines := []string{
		entry(t, map[string]any{kType: kUser, kUUID: "p", kTimestamp: at(10), kOrigin: map[string]any{kKind: kHuman}, kMessage: map[string]any{kContent: []any{
			map[string]any{kType: kText, kText: "look"}, img("AAA"),
		}}}),
		entry(t, map[string]any{kType: kUser, kUUID: "s", kTimestamp: at(11), kMessage: map[string]any{kContent: []any{
			map[string]any{kType: kToolRes, "tool_use_id": "x", kContent: []any{img("BBB")}},
		}}}),
		entry(t, map[string]any{kType: kUser, kUUID: "m", kTimestamp: at(12), "isMeta": true, kMessage: map[string]any{kContent: []any{img("CCC")}}}),
		entry(t, map[string]any{kType: kUser, kUUID: "c", kTimestamp: at(13), "isSidechain": true, kMessage: map[string]any{kContent: []any{
			map[string]any{kType: kToolRes, kContent: []any{img("DDD")}},
		}}}),
	}
	items, _ := conversationAfter(t, lines, at(0))
	var shape []string
	for _, it := range items {
		shown := it.Text
		if it.DataB64 != "" {
			shown = it.DataB64
		}
		shape = append(shape, it.Role+"/"+it.From+"/"+shown)
	}
	want := []string{"you//look", "image/you/AAA", "image/ai/BBB"}
	if fmt.Sprint(shape) != fmt.Sprint(want) {
		t.Errorf("items = %v", shape)
	}
}

func TestEachAnswerNamesItsModel(t *testing.T) {
	lines := []string{
		entry(t, map[string]any{kType: kAssistant, kUUID: "a", kTimestamp: at(1), kMessage: map[string]any{"model": kModelX, kStop: kToolUse, kContent: []any{
			map[string]any{kType: kText, kText: "Looking."},
			map[string]any{kType: kToolUse, "id": "x"},
		}}}),
		entry(t, map[string]any{kType: kAssistant, kUUID: "b", kTimestamp: at(2), kMessage: map[string]any{"model": "<synthetic>", kStop: stopEnd, kContent: []any{
			map[string]any{kType: kText, kText: "No response requested."},
		}}}),
	}
	items, _ := conversationAfter(t, lines, at(0))
	var models []string
	for _, it := range items {
		models = append(models, it.Model)
	}
	// 第一条两个 item 都带模型；synthetic 不带。
	if models[0] != "claude-model-x" || models[1] != "claude-model-x" || models[2] != "" {
		t.Errorf("models = %v", models)
	}
}

func TestContextComesWithEachAnswer(t *testing.T) {
	lines := []string{
		entry(t, map[string]any{kType: kAssistant, kUUID: "a", kTimestamp: at(1), kMessage: map[string]any{
			kModel: "m", kStop: stopEnd,
			"usage":  map[string]any{"input_tokens": 2, "cache_read_input_tokens": 225208, "cache_creation_input_tokens": 1608, "output_tokens": 234},
			kContent: []any{map[string]any{kType: kText, kText: "Hi."}},
		}}),
		entry(t, map[string]any{kType: kAssistant, kUUID: "b", kTimestamp: at(2), kMessage: map[string]any{
			kModel: "m", kStop: kToolUse,
			"usage":  map[string]any{"input_tokens": 5, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0},
			kContent: []any{map[string]any{kType: kThinking, kThinking: "private", "signature": "x"}},
		}}),
	}
	items, _ := conversationAfter(t, lines, at(0))
	if len(items) != 2 || items[0].Role != core.RoleAI || items[0].Context == nil || items[0].Context.Used != 226818 {
		t.Fatalf("items = %+v", items)
	}
	if items[1].Role != core.RoleMeta || items[1].Context.Used != 5 { // 纯 thinking 也报上下文
		t.Errorf("meta = %+v", items[1])
	}
}

func TestContextWindowAndLimitsFromClaudeItself(t *testing.T) {
	if got := ContextWindowIn("## Context Usage\n\n**Model:** claude-opus-5-5  \n**Tokens:** 13.8k / 1m (1%)"); got != 1000000 {
		t.Errorf("window = %d", got)
	}
	if got := ContextWindowIn("**Tokens:** 5k / 200k (2%)"); got != 200000 {
		t.Errorf("window = %d", got)
	}
	if got := ContextWindowIn("something else"); got != 0 {
		t.Errorf("读不出就是 0")
	}
	usage := "You are currently using your subscription\n\nCurrent session: 2% used · resets Nov 14 at 6:09pm (Asia/Singapore)\nCurrent week (all models): 57% used · resets Nov 15\nCurrent week (Fable): 4% used"
	limits := LimitsIn(usage)
	if len(limits) != 2 || limits[0].Name != "5-hour" || limits[0].Used != 2 || limits[1].Name != "7-day" || limits[1].Used != 57 {
		t.Errorf("limits = %+v", limits)
	}
	if LimitsIn("You are using an API key") != nil {
		t.Errorf("没有限额就一条都不给，绝不编 0%%")
	}
}

func TestTaskToolCallBecomesSubagentCard(t *testing.T) {
	// FINAL_PLAN §2.B：把活儿交给子代理，会话里单独成卡。
	lines := []string{
		entry(t, map[string]any{kType: kAssistant, kUUID: "t", kTimestamp: at(10), kMessage: map[string]any{kStop: kToolUse, kContent: []any{
			map[string]any{kType: kToolUse, kName: "Task", kInput: map[string]any{
				kDesc: "Search the codebase", "prompt": "find every caller of Foo",
			}},
		}}}),
	}
	items, _ := conversationAfter(t, lines, at(0))
	if len(items) != 2 {
		t.Fatalf("items = %+v ( 要有工具行 + 卡 )", items)
	}
	if items[0].Role != core.RoleTool || items[0].Text != "▸ Task: Search the codebase" {
		t.Errorf("tool = %+v", items[0])
	}
	card := items[1]
	if card.Role != core.RoleCard || card.Card == nil || card.Card.Kind != "subagent" ||
		card.Card.Title != "Search the codebase" || card.Card.State != "started" {
		t.Errorf("card item = %+v", card)
	}
}

func TestToolWordsForCard(t *testing.T) {
	if got := ToolWords("Bash", map[string]any{"description": "Run tests", "command": "npm test"}, ""); got != "Run tests\n$ npm test" {
		t.Errorf("Bash = %q", got)
	}
	if got := ToolWords("Read", map[string]any{"file_path": "/Users/me/proj/a.go"}, "/Users/me/proj"); got != "Read a.go" {
		t.Errorf("Read = %q", got)
	}
	if got := ToolWords("mcp__github__create_issue", map[string]any{"title": "x"}, ""); !strings.Contains(got, "github: create_issue") {
		t.Errorf("mcp = %q", got)
	}
}
