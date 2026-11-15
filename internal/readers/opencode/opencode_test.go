package opencode

import (
	"fmt"
	"testing"

	"github.com/jrmarcco/herdr-chat/internal/core"
)

const (
	modelGLM  = "glm-4.7"
	provZhipu = "zhipuai"
	kID       = "id"
	kAsst     = "assistant"
	kType     = "type"
	kContent  = "content"
	kCreated  = "created"
	kTime     = "time"
	kDir      = "directory"
	kInput    = "input"
	kKey      = "key"
	kText     = "text"
	kUpdated  = "updated"
	kTitle    = "title"
	sessOld   = "s-old"
	kProvider = "providerID"
)

func itemsOfLines(t *testing.T, lines []string) []core.Item {
	t.Helper()
	items, _ := core.ItemsAfter(lines, core.StartCursor("0000-00-00T00:00:00.000Z"), entryItems)
	return items
}

func TestLinesOfMarksEndAtIdle(t *testing.T) {
	messages := []any{
		map[string]any{kID: "u1", kType: "user", kText: "ship it", kTime: map[string]any{kCreated: 1000}},
		map[string]any{
			kID: "a1", kType: kAsst, "model": map[string]any{kID: modelGLM, kProvider: provZhipu},
			"tokens": map[string]any{"input": 100, "cache": map[string]any{"read": 20, "write": 5}},
			kContent: []any{
				map[string]any{kType: kText, kText: "working on it"},
				map[string]any{kType: "tool", "name": "shell", kInput: map[string]any{"command": "npm test"}},
			}, kTime: map[string]any{kCreated: 2000},
		},
		map[string]any{kID: "a2", kType: kAsst, kContent: []any{
			map[string]any{kType: kText, kText: "done"},
		}, kTime: map[string]any{kCreated: 3000}},
		map[string]any{kID: "i1", kType: "idle", kTime: map[string]any{kCreated: 4000}},
	}
	items := itemsOfLines(t, LinesOf(messages))
	var shape []string
	for _, it := range items {
		end := ""
		if it.End {
			end = "*"
		}
		shape = append(shape, it.Role+end+":"+it.Text)
	}
	want := []string{
		"you:ship it",
		"ai:working on it",
		"tool:▸ shell: npm test",
		"ai*:done", // idle 之前最后一条 ai 标 end
	}
	if fmt.Sprint(shape) != fmt.Sprint(want) {
		t.Errorf("shape = %v", shape)
	}
	if items[1].Model != modelGLM || items[1].Provider != provZhipu || items[1].Context == nil || items[1].Context.Used != 125 {
		t.Errorf("meta = %+v", items[1])
	}
}

func TestQuestionToolLine(t *testing.T) {
	items := itemsOfLines(t, LinesOf([]any{map[string]any{
		kID: "a1", kType: kAsst, kTime: map[string]any{kCreated: 1},
		kContent: []any{map[string]any{
			kType: "tool", "name": "question",
			kInput: map[string]any{"questions": []any{map[string]any{"question": "Ship it?"}}},
		}},
	}}))
	if len(items) != 1 || items[0].Text != "▸ question: Ship it?" {
		t.Errorf("items = %+v", items)
	}
}

func TestPickSession(t *testing.T) {
	list := []any{
		map[string]any{kID: "s-other", kDir: "/elsewhere", kUpdated: 900},
		map[string]any{kID: sessOld, kDir: "/pro", kUpdated: 100},
		map[string]any{kID: "s-new", kDir: "/pro", kUpdated: 500},
	}
	if got := PickSession(list, nil, "/pro"); got != "s-new" {
		t.Errorf("无活跃：更新最近的, got %q", got)
	}
	active := map[string]any{"s-old": true}
	if got := PickSession(list, active, "/pro"); got != "s-old" {
		t.Errorf("活跃优先, got %q", got)
	}
	if got := PickSession(nil, nil, "/pro"); got != "" {
		t.Errorf("没有会话 = 空")
	}
}

func TestWindowOf(t *testing.T) {
	models := []any{
		map[string]any{kID: modelGLM, kProvider: provZhipu, "limit": map[string]any{"context": 200000}},
		map[string]any{kID: modelGLM, kProvider: "other", "limit": map[string]any{"context": 1}},
	}
	if got := WindowOf(models, modelGLM, provZhipu); got != 200000 {
		t.Errorf("带 provider = %d", got)
	}
	if got := WindowOf(models, modelGLM, ""); got != 200000 {
		t.Errorf("不带 provider 命中第一个 = %d", got)
	}
	if got := WindowOf(models, "none", ""); got != 0 {
		t.Errorf("没有命中 = 0")
	}
}

func TestZhipuLimits(t *testing.T) {
	if ZhipuHost("zai") != "api.z.ai" || ZhipuHost("zhipuai-pro") != "open.bigmodel.cn" || ZhipuHost("openai") != "" {
		t.Errorf("host 判定")
	}
	credentials := []any{
		map[string]any{"integrationID": provZhipu, "active": true, "value": map[string]any{kType: "key", kKey: "sk-zp"}},
		map[string]any{"integrationID": "other", "value": map[string]any{kType: "key", kKey: "sk-x"}},
	}
	if got := KeyOf(credentials, provZhipu); got != "sk-zp" {
		t.Errorf("key = %q", got)
	}
	reply := []byte(`{"success":true,"data":{"limits":[
		{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":44},
		{"type":"TOKENS_LIMIT","unit":6,"number":7,"percentage":57},
		{"type":"TIME_LIMIT","unit":5,"number":1,"percentage":30}
	]}}`)
	limits := ZhipuLimitsIn(reply)
	if len(limits) != 2 || limits[0].Name != "5-hour" || limits[0].Used != 44 || limits[1].Name != "7-day" || limits[1].Used != 57 {
		t.Errorf("limits = %+v ( TIME_LIMIT 不取、按小时升序 )", limits)
	}
	if ZhipuLimitsIn([]byte(`{"success":false}`)) != nil || ZhipuLimitsIn([]byte(`nope`)) != nil {
		t.Errorf("不如预期就不给")
	}
}

func TestDialogOfFormAndPermission(t *testing.T) {
	forms := []any{map[string]any{
		kID: "f1", "title": "Deploy",
		"fields": []any{
			map[string]any{
				kKey: "env", "type": "select", kTitle: "Which env?",
				"options": []any{map[string]any{"label": "staging"}, map[string]any{"label": "prod"}},
			},
			map[string]any{kKey: "note", kType: kText, kTitle: "Any notes?", "custom": true},
		},
	}}
	dialog := DialogOf(forms, nil, nil)
	if dialog == nil || dialog.Key != "f1#env" || len(dialog.Options) != 2 || !stringsContains(dialog.Text, "Which env?") || !stringsContains(dialog.Text, "(question 1 of 2)") {
		t.Fatalf("form dialog = %+v", dialog)
	}
	// 第一题已答：第二题带自由输入。
	answered := map[string]map[string]any{"f1": {"env": "staging"}}
	dialog = DialogOf(forms, nil, answered)
	if dialog == nil || dialog.Key != "f1#note" || dialog.Free != len(dialog.Options) || dialog.Free == 0 {
		t.Fatalf("second = %+v", dialog)
	}
	// 权限请求。
	permissions := []any{map[string]any{kID: "p1", "message": "Run npm test", "action": "bash"}}
	dialog = DialogOf(nil, permissions, nil)
	if dialog == nil || dialog.Kind != "permission" || len(dialog.Options) != 3 || dialog.Options[2].Label != "Reject" {
		t.Fatalf("permission = %+v", dialog)
	}
	if DialogOf(nil, nil, nil) != nil {
		t.Errorf("没有等待 = nil")
	}
}

func TestTitleOfOC(t *testing.T) {
	if got := TitleOf("OC | fix the login"); got != "fix the login" {
		t.Errorf("title = %q", got)
	}
}

func stringsContains(s, part string) bool {
	return len(part) == 0 || len(s) >= len(part) && (s == part || indexOf(s, part) >= 0)
}

func indexOf(s, part string) int {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return i
		}
	}
	return -1
}
