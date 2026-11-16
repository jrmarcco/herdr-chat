package engine

import (
	"strings"
	"testing"

	"github.com/jrmarcco/herdr-chat/internal/core"
)

// /kill 卡：裸命令出卡选终端，提交即结束；旧语法照旧。

func TestKillBareSendsTheCard(t *testing.T) {
	r := harness(t)
	r.start(t)
	r.direct("/kill")

	r.waitFor(t, "终端卡", func() bool { return r.countCalls(callPostDialog) == 1 })
	d := r.postedDialog()
	if len(d.Selects) != 1 || d.Selects[0].Name != fieldTerminal {
		t.Fatalf("selects = %+v", d.Selects)
	}
	opts := d.Selects[0].Options
	if len(opts) != 1 {
		t.Fatalf("options = %+v, want the one live terminal", opts)
	}
	if opts[0].Label != "api / claude (t1)" || opts[0].Value != sessDefault+"|t1" {
		t.Fatalf("option = %+v", opts[0])
	}
	if len(r.hc.Kills()) != 0 {
		t.Fatal("a bare /kill must not end anything by itself")
	}
}

func TestKillCardSubmitEndsTheTerminal(t *testing.T) {
	r := harness(t)
	r.start(t)
	r.direct("/kill")
	r.waitFor(t, "终端卡", func() bool { return r.countCalls(callPostDialog) == 1 })

	result, err := r.chat.Handlers.OnAction(core.Click{
		Value:   map[string]any{"k": killKey, valPick: "1"},
		Message: directMsg,
		Form:    map[string]string{fieldTerminal: sessDefault + "|t1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Toast, "✔ Ended `t1`") {
		t.Fatalf("toast = %q", result.Toast)
	}
	kills := r.hc.Kills()
	if len(kills) != 1 || kills[0].Session != sessDefault || kills[0].Address != "t1" {
		t.Fatalf("kills = %+v", kills)
	}
}

func TestKillCardRefusesWhatIsGone(t *testing.T) {
	r := harness(t)
	r.start(t)

	// 卡躺了一阵，终端已结束：如实告知，不动手。
	r.hc.RemoveTerminal("t1")
	result, err := r.chat.Handlers.OnAction(core.Click{
		Value:   map[string]any{"k": killKey, valPick: "1"},
		Message: directMsg,
		Form:    map[string]string{fieldTerminal: sessDefault + "|t1"},
	})
	if err != nil || !strings.Contains(result.Toast, "ended already") {
		t.Fatalf("dead: %v %q", err, result.Toast)
	}

	// 不认识的值：请重发。
	result, err = r.chat.Handlers.OnAction(core.Click{
		Value:   map[string]any{"k": killKey, valPick: "1"},
		Message: directMsg,
		Form:    map[string]string{fieldTerminal: "elsewhere"},
	})
	if err != nil || !strings.Contains(result.Toast, "unknown") {
		t.Fatalf("unknown: %v %q", err, result.Toast)
	}

	// Esc：取消。
	result, err = r.chat.Handlers.OnAction(core.Click{
		Value:   map[string]any{"k": killKey, valPick: pickEsc},
		Message: directMsg,
	})
	if err != nil || !strings.Contains(result.Toast, "Cancelled") {
		t.Fatalf("esc: %v %q", err, result.Toast)
	}
	if len(r.hc.Kills()) != 0 {
		t.Fatalf("kills = %+v, nothing should have ended", r.hc.Kills())
	}
}

func TestKillCardNothingLiveSaysSo(t *testing.T) {
	r := harness(t)
	r.start(t)
	r.hc.RemoveTerminal("t1")
	r.direct("/kill")
	r.waitFor(t, "无事可做", func() bool {
		for _, md := range r.replies() {
			if strings.Contains(md, "Nothing to end") {
				return true
			}
		}
		return false
	})
}

func TestKillOldSyntaxStillEnds(t *testing.T) {
	r := harness(t)
	r.start(t)
	r.direct("/kill default t1")
	r.waitFor(t, "旧语法", func() bool { return len(r.hc.Kills()) == 1 })
	r.direct("/kill default") // 少一个参数：指路新形态
	r.waitFor(t, "指路", func() bool {
		for _, md := range r.replies() {
			if strings.Contains(md, "a card asks") {
				return true
			}
		}
		return false
	})
}
