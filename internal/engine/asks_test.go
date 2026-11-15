package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jrmarcco/herdr-chat/internal/asks"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/fake"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
)

// hook 的 asks 协议：req/ans 文件、herdrchat.ask 标签、部分答案与已答完的遗忘。
// fake 的 HomeShell 本机分支真的跑 sh，文件协议由此用真实文件系统验证。

// askTestEnv 备好一个 home：hook 在场、一条活着的请求 ( pid 用测试自己的进程，kill -0 自然活着 )。
func askTestEnv(t *testing.T) (*asks.Asks, *fake.FakeHC, string, string) {
	t.Helper()
	home := t.TempDir()
	for _, dir := range []string{
		filepath.Join(home, asks.AsksDir, "req"),
		filepath.Join(home, asks.AsksDir, "ans"),
		filepath.Dir(filepath.Join(home, asks.HookFile)),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	//nolint:gosec // G306: 测试 stub 脚本需要可执行位。
	if err := os.WriteFile(filepath.Join(home, asks.HookFile), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	hc := fake.NewFakeHC()
	hc.HomeDirSet = home
	asks := asks.New(hc, home)
	id := "1731500000-" + strconv.Itoa(os.Getpid())
	return asks, hc, home, id
}

func writeReq(t *testing.T, home, id string, body map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, asks.AsksDir, "req", id+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readAns(t *testing.T, home, id string) string {
	t.Helper()
	//nolint:gosec // G304: 测试目录内的文件。
	raw, err := os.ReadFile(filepath.Join(home, asks.AsksDir, "ans", id+".json"))
	if err != nil {
		return ""
	}
	return string(raw)
}

func TestAskOfLabel(t *testing.T) {
	record := hccli.Terminal{keyLabels: map[string]any{asks.AskLabel: "1731500000-42"}}
	if asks.AskOf(record) != "1731500000-42" {
		t.Errorf("标签读出失败")
	}
	bad := hccli.Terminal{keyLabels: map[string]any{asks.AskLabel: "nonsense"}}
	if asks.AskOf(bad) != "" || asks.AskOf(nil) != "" {
		t.Errorf("形状不对或缺失的标签不算")
	}
}

func TestOpenAskReadsRequest(t *testing.T) {
	a, hc, home, id := askTestEnv(t)
	writeReq(t, home, id, map[string]any{
		keyAgent: appFake, keyAddress: addrT1,
		keyRequest: map[string]any{"tool": "Bash", "command": "make test"},
	})
	hc.Terminals["t1"] = hccli.Terminal{keyAddress: addrT1, keyLabels: map[string]any{asks.AskLabel: id}}
	ask, known := a.OpenAsk(context.Background(), "default", "t1", appFake)
	if !known || ask == nil {
		t.Fatalf("ask = %v known=%v", ask, known)
	}
	if ask.ID != id || ask.Agent != appFake {
		t.Errorf("ask = %+v", ask)
	}
	// 别的 agent 的请求不算。
	if ask, _ := a.OpenAsk(context.Background(), "default", "t1", "other"); ask != nil {
		t.Errorf("不是这个 agent 的请求不该读出")
	}
}

func dialogForAsk(id string) core.Dialog {
	menu := &core.Menu{Key: id, Framed: true, Options: []core.MenuOption{{N: 1, Label: optYes}, {N: 2, Label: optNo}}}
	menu.Own = &asks.Ref{ID: id}
	return core.Dialog{Menu: menu, Title: NeedsTitle, DoneTitle: DoneTitle}
}

func TestAnswerAskWritesDecisionAndFinishes(t *testing.T) {
	a, hc, home, id := askTestEnv(t)
	writeReq(t, home, id, map[string]any{keyAgent: appFake, keyRequest: map[string]any{"q": 1}})
	hc.Terminals["t1"] = hccli.Terminal{keyAddress: addrT1, keyLabels: map[string]any{asks.AskLabel: id}}
	shown := dialogForAsk(id)

	decideAll := func(ask *asks.Ask, d core.Dialog, r core.Reply, partial map[string]string) *asks.Decision {
		return &asks.Decision{Decision: asks.DecisionJSON(map[string]any{"allow": true})}
	}
	if !a.AnswerAsk(context.Background(), "default", "t1", appFake, &shown, core.Reply{Pick: "1"}, decideAll) {
		t.Fatal("答案该被送出")
	}
	ans := readAns(t, home, id)
	if !strings.Contains(ans, `"allow":true`) {
		t.Errorf("ans 文件内容 = %q", ans)
	}
	// 已答完：不再算打开的请求。
	if ask, _ := a.OpenAsk(context.Background(), "default", "t1", appFake); ask != nil {
		t.Errorf("答完的请求不该再打开")
	}
	// decide 说"不是它的答案"：不写不记。
	shownOther := dialogForAsk("9999999999-1")
	if a.AnswerAsk(context.Background(), "default", "t1", appFake, &shownOther, core.Reply{Pick: "1"}, decideAll) {
		t.Errorf("别的请求的卡不该被答")
	}
}

func TestAnswerAskKeepsPartials(t *testing.T) {
	a, hc, home, id := askTestEnv(t)
	writeReq(t, home, id, map[string]any{keyAgent: appFake, keyRequest: map[string]any{"q": 2}})
	hc.Terminals["t1"] = hccli.Terminal{keyAddress: addrT1, keyLabels: map[string]any{asks.AskLabel: id}}
	shown := dialogForAsk(id)

	decidePartial := func(ask *asks.Ask, d core.Dialog, r core.Reply, partial map[string]string) *asks.Decision {
		if len(partial) == 0 {
			return &asks.Decision{Partial: map[string]string{"Colors?": r.Pick}}
		}
		merged := map[string]string{}
		for k, v := range partial {
			merged[k] = v
		}
		merged["Sizes?"] = r.Pick
		return &asks.Decision{Decision: asks.DecisionJSON(map[string]any{"answers": merged})}
	}
	if !a.AnswerAsk(context.Background(), "default", "t1", appFake, &shown, core.Reply{Pick: "red"}, decidePartial) {
		t.Fatal("部分答案该被记下")
	}
	if readAns(t, home, id) != "" {
		t.Errorf("还有题没答，不该写决定")
	}
	// 第二题答完：决定里两题都有。
	if !a.AnswerAsk(context.Background(), "default", "t1", appFake, &shown, core.Reply{Pick: "large"}, decidePartial) {
		t.Fatal("全齐该送出")
	}
	ans := readAns(t, home, id)
	if !strings.Contains(ans, "red") || !strings.Contains(ans, "large") {
		t.Errorf("决定应带上两题: %q", ans)
	}
}

func TestStrayAskFindsRequestWithoutTerminal(t *testing.T) {
	a2, _, home, id := askTestEnv(t)
	writeReq(t, home, id, map[string]any{keyAgent: appFake, keyRequest: map[string]any{"q": 1}})
	stray := a2.StrayAsk(context.Background(), "default", "", appFake)
	if stray == nil || stray.ID != id {
		t.Fatalf("stray = %+v", stray)
	}
	// 指名了终端的不算流浪。
	writeReq(t, home, "1731600000-"+strconv.Itoa(os.Getpid()), map[string]any{keyAgent: appFake, keyAddress: "t9", keyRequest: map[string]any{}})
	if stray = a2.StrayAsk(context.Background(), "default", "", appFake); stray != nil && stray.Terminal != "" {
		t.Errorf("指名终端的请求不该被认领")
	}
}

func TestDecisionJSONShape(t *testing.T) {
	out := asks.DecisionJSON(map[string]any{"allow": true, "updatedInput": map[string]any{"x": 1}})
	var parsed struct {
		HookSpecificOutput struct {
			HookEventName string         `json:"hookEventName"`
			Decision      map[string]any `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.HookSpecificOutput.HookEventName != "PermissionRequest" {
		t.Errorf("hookEventName = %q", parsed.HookSpecificOutput.HookEventName)
	}
	if parsed.HookSpecificOutput.Decision["allow"] != true {
		t.Errorf("decision = %v", parsed.HookSpecificOutput.Decision)
	}
}

func TestClip(t *testing.T) {
	long := make([]string, 40)
	for i := range long {
		long[i] = "line"
	}
	clipped := asks.Clip(strings.Join(long, "\n"), 0, 30)
	if !strings.Contains(clipped, "the rest is in the terminal") || len(clipped) > 200 {
		t.Errorf("clip = %q", clipped[:min(60, len(clipped))])
	}
	if got := asks.Clip("short", 0, 0); got != "short" {
		t.Errorf("短文原样: %q", got)
	}
}
