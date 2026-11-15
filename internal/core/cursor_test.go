package core

import (
	"fmt"
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/state"
)

// 游标行为测试：切行跳过与稳定 id、同时间戳晚写不丢、
// 晚写旧时间戳只发一次、tail 不够触发全量读、seen 只在 lag 之外遗忘。
// 最简单的一种日志：每条一个条目，带它的文本。
func at(seconds int) string {
	return time.Date(2026, 11, 12, 0, 0, seconds, 0, time.UTC).Format(isoLayout)
}

func line(seconds int, text, uuid string) string {
	return fmt.Sprintf(`{"uuid":%q,"timestamp":%q,"text":%q}`, uuid, at(seconds), text)
}

func parseText(e map[string]any) []Item {
	text, _ := e["text"].(string)
	return []Item{{Role: RoleAI, Text: text}}
}

func afterLines(t *testing.T, lines []string, cur state.Cursor) ([]Item, bool) {
	t.Helper()
	return ItemsAfter(lines, cur, parseText)
}

func TestCutLineSkippedAndIdsUnique(t *testing.T) {
	items, _ := afterLines(t, []string{`{"cut`, line(1, "a", "u1"), line(2, "b", "u2"), `{"half`}, StartCursor(at(0)))
	if len(items) != 2 || items[0].Text != "a" || items[1].Text != "b" {
		t.Fatalf("items = %+v", items)
	}
	if items[0].ID == items[1].ID {
		t.Errorf("每个条目必须有自己的 id")
	}
}

func TestSameTimestampLateWriteNotLost(t *testing.T) {
	// 同一时间戳的两条：晚写的那条不能丢。
	first := []string{line(10, "go", "u1"), line(11, "Part one.", "a1")}
	cur := StartCursor(at(0))
	items, _ := afterLines(t, first, cur)
	cur = Advance(cur, items)
	later := append(append([]string{}, first...), line(11, "Part two, same second.", "a2"))
	items, _ = afterLines(t, later, cur)
	if len(items) != 1 || items[0].Text != "Part two, same second." {
		t.Errorf("items = %+v", items)
	}
}

func TestLateWriteWithOlderTimestampPostedOnce(t *testing.T) {
	// 晚写但时间戳较旧的条目仍会发出，且只发一次。
	cur := StartCursor(at(0))
	written := []string{line(10, "go", "u1"), line(30, "Newer answer.", "a2")}
	items, _ := afterLines(t, written, cur)
	cur = Advance(cur, items)
	withLate := append(append([]string{}, written...), line(20, "Older text, written last.", "a1"))
	items, _ = afterLines(t, withLate, cur)
	if len(items) != 1 || items[0].Text != "Older text, written last." {
		t.Fatalf("items = %+v", items)
	}
	cur = Advance(cur, items)
	if again, _ := afterLines(t, withLate, cur); len(again) != 0 {
		t.Errorf("同一条目绝不重发: %+v", again)
	}
}

func TestTailShortOfCursorAsksForWholeFile(t *testing.T) {
	// tail 够不到游标之前时，complete = false，调用方须整文件重读。
	cur := StartCursor(at(5))
	if _, complete := afterLines(t, []string{line(20, "x", "a1")}, cur); complete {
		t.Errorf("tail 不够必须报 incomplete")
	}
	if _, complete := afterLines(t, []string{line(5, "old", "u0"), line(20, "x", "a1")}, cur); !complete {
		t.Errorf("tail 够到游标之前才算 complete")
	}
}

func TestSeenForgottenPastLag(t *testing.T) {
	// seen 只在条目老过 lag 之后遗忘。
	t0 := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	iso := func(ms int) string { return t0.Add(time.Duration(ms) * time.Millisecond).Format(isoLayout) }
	cur := StartCursor(iso(0))
	old := Item{ID: "old", At: t0.Add(1000 * time.Millisecond)}
	cur = Advance(cur, []Item{old})
	fresh := Item{ID: "new", At: t0.Add(1000*time.Millisecond + CursorLag + 5000*time.Millisecond)}
	cur = Advance(cur, []Item{fresh})
	seenNew, seenOld := false, false
	for _, pair := range cur.Seen {
		if pair[0] == "new" {
			seenNew = true
		}
		if pair[0] == "old" {
			seenOld = true
		}
	}
	if !seenNew || seenOld {
		t.Errorf("seen = %+v: new 须保留、old 须遗忘", cur.Seen)
	}
	if cur.At != iso(6000) {
		t.Errorf("at = %s, want %s", cur.At, iso(6000))
	}
}
