package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 菜单解析行为测试：
// 权限对话框、多选与勾选、防陈旧 key、预览框 ( beside )、resize 折行稳定。
func rule(width int) string { return strings.Repeat("─", width) }

func permissionDialog(cmd string) string {
	return strings.Join([]string{
		"⏺ Running a command.",
		rule(60),
		" Bash command",
		" " + cmd,
		strings.Repeat("╌", 60),
		" Do you want to proceed?",
		" ❯ 1. Yes",
		"   2. Yes, and always allow access to cardtest from this project",
		"   3. No",
		"",
		" Esc to cancel · Tab to amend",
		"[hc] incomplete: older output may be missing",
	}, "\n")
}

func optionLabels(m *Menu) []string {
	var out []string
	for _, o := range m.Options {
		out = append(out, o.Label)
	}
	return out
}

func optionCheckmarks(m *Menu) []*bool {
	out := make([]*bool, len(m.Options))
	for i, o := range m.Options {
		out[i] = o.Checked
	}
	return out
}

func TestMenuReadsPermissionDialog(t *testing.T) {
	m := MenuOf(permissionDialog("touch a.txt"))
	if m == nil {
		t.Fatal("未读出菜单")
	}
	if m.Text != "Bash command\ntouch a.txt\nDo you want to proceed?" {
		t.Errorf("text = %q", m.Text)
	}
	want := []string{"Yes", "Yes, and always allow access to cardtest from this project", "No"}
	if fmt.Sprint(optionLabels(m)) != fmt.Sprint(want) {
		t.Errorf("options = %v", optionLabels(m))
	}
	if m.Multi {
		t.Errorf("不是多选")
	}
	if m.Free != nil {
		t.Errorf("没有自由输入项")
	}
}

func TestMenuAlikeDialogsDifferentKeys(t *testing.T) {
	if MenuOf(permissionDialog("touch a.txt")).Key == MenuOf(permissionDialog("rm -rf build")).Key {
		t.Errorf("长得像的两个对话框必须有不同的 key")
	}
	first := MenuOf(permissionDialog("touch a.txt"))
	second := MenuOf(permissionDialog("touch a.txt"))
	if first.Key != second.Key {
		t.Errorf("同一对话框的 key 稳定")
	}
}

func colorsDialog(red, blue bool, cursor int) string {
	c := func(on bool, mark string) string {
		if on {
			return "[" + mark + "]"
		}
		return "[ ]"
	}
	c1 := " "
	if cursor != 1 {
		c1 = "❯"
	}
	return strings.Join([]string{
		"Steps:",
		"1. Plan the work",
		"2. Do it",
		rule(60),
		"←  ☐ Colors  ☐ Size  ✔ Submit  →",
		"",
		"Pick colors",
		"",
		c1 + " 1. " + c(red, "✔") + " Red",
		"         Red",
		"  2. [ ] Green",
		"  3. " + c(blue, "✔") + " Blue",
		"  4. [ ] Type something",
		"     Next",
		rule(60),
		"  5. Chat about this",
		"",
		"Enter to select · Tab/Arrow keys to navigate · Esc to cancel",
	}, "\n")
}

func TestMenuReadsMultiSelect(t *testing.T) {
	m := MenuOf(colorsDialog(true, false, 1))
	if m.Text != "←  ☐ Colors  ☐ Size  ✔ Submit  →\nPick colors" {
		t.Errorf("text = %q", m.Text)
	}
	want := []string{"Red", "Green", "Blue", "Type something", "Chat about this"}
	if fmt.Sprint(optionLabels(m)) != fmt.Sprint(want) {
		t.Errorf("options = %v", optionLabels(m))
	}
	for i, checked := range optionCheckmarks(m) {
		var want *bool
		switch i {
		case 0:
			v := true
			want = &v
		case 1, 2, 3:
			v := false
			want = &v
		case 4:
			want = nil
		}
		if want == nil && checked != nil || want != nil && (checked == nil || *checked != *want) {
			t.Errorf("option %d checked = %v want %v", i+1, checked, want)
		}
	}
	if !m.Multi {
		t.Errorf("是多选")
	}
	if m.Free == nil || m.Free.N != 4 {
		t.Errorf("free = %+v, want 第 4 项", m.Free)
	}
}

func TestMenuKeyStableAcrossToggleAndCursor(t *testing.T) {
	if MenuOf(colorsDialog(false, false, 1)).Key != MenuOf(colorsDialog(true, true, 3)).Key {
		t.Errorf("勾选或光标移动后仍是同一个对话框")
	}
}

func TestMenuNoNumberedMenuNoDialog(t *testing.T) {
	if MenuOf("Login required. Open the link below.\n> ") != nil {
		t.Errorf("没有编号菜单就没有对话框")
	}
	if MenuOf("2. a\n3. b") != nil {
		t.Errorf("编号不连续不算菜单")
	}
}

func TestMenuKeyStableAsQuestionTabsGetMarks(t *testing.T) {
	draw := func(tabs, mark string) string {
		return strings.Join([]string{
			strings.Repeat("─", 22),
			" ← " + tabs + " →",
			"",
			"Which sizes?",
			"",
			"❯ 1. [" + mark + "] Small",
			"  2. [ ] Large",
			"  3. Type something.",
		}, "\n")
	}
	before := MenuOf(draw("☐ Colors  ☐ Sizes  ✔ Submit", " "))
	after := MenuOf(draw("☒ Colors  ☐ Sizes  ✔ Submit", "✔"))
	if before == nil || after == nil {
		t.Fatal("两屏都该有菜单")
	}
	if before.Key != after.Key {
		t.Errorf("问题标签页的标记变化不该换身份")
	}
	if after.Options[0].Checked == nil || !*after.Options[0].Checked {
		t.Errorf("选项勾选应读到 true")
	}
	other := MenuOf(strings.Replace(draw("☐ Colors  ☐ Sizes  ✔ Submit", " "), "Which sizes?", "Which colors?", 1))
	if before.Key == other.Key {
		t.Errorf("换了问题就是另一个对话框")
	}
}

func TestMenuBesidePreviewIsNeitherLabelNorKey(t *testing.T) {
	draw := func(cursor int, preview string) string {
		c1, c2 := "❯", " "
		if cursor == 2 {
			c1, c2 = " ", "❯"
		}
		return strings.Join([]string{
			strings.Repeat("─", 46),
			" ☐ Release",
			"│ The tests passed. Post the updates?",
			c1 + " 1. Post them    ┌──────────────────────────────┐",
			"    (Recommended)                 │ " + preview,
			c2 + " 2. Not yet                       └──────────────────────────────",
			"                                  Notes: press n to add notes",
		}, "\n")
	}
	a := MenuOf(draw(1, "Staging test done: all six cases"))
	b := MenuOf(draw(2, "No preview available"))
	if a == nil || b == nil {
		t.Fatal("两屏都该有菜单")
	}
	if fmt.Sprint(optionLabels(a)) != fmt.Sprint([]string{"Post them", "Not yet"}) {
		t.Errorf("labels = %v ( 预览框不算 )", optionLabels(a))
	}
	if a.Key != b.Key {
		t.Errorf("移动光标只换预览，不换对话框")
	}
	if !strings.HasPrefix(a.Text, "☐ Release\nThe tests passed. Post the updates?") {
		t.Errorf("text = %q", a.Text)
	}
}

func TestMenuBesideTakesArrowsAndKnowsCursor(t *testing.T) {
	screen := strings.Join([]string{
		strings.Repeat("─", 20),
		" ☐ Release",
		"│ Post it?",
		"  1. Post it    ┌──────────────┐",
		"    (Recommended)                 │ 1. a numbered preview line",
		"❯ 2. Not yet                       └──────────────",
		"Enter to select · ↑/↓ to navigate",
	}, "\n")
	m := MenuOf(screen)
	if m == nil {
		t.Fatal("该有菜单")
	}
	if !m.Beside {
		t.Errorf("带预览框的问题走方向键")
	}
	if m.Cursor != 2 {
		t.Errorf("cursor = %d, want 2", m.Cursor)
	}
	plain := MenuOf(strings.Join([]string{strings.Repeat("─", 10), "Proceed?", "❯ 1. Yes", "  2. No"}, "\n"))
	if plain.Beside || plain.Cursor != 1 {
		t.Errorf("普通菜单 beside=false cursor=1")
	}
}

func TestMenuKeyStableAcrossResizeRewrap(t *testing.T) {
	wide := strings.Repeat("─", 40) + "\nShall I run the long command that cleans the build folder now?\n❯ 1. Yes\n  2. No\n"
	narrow := strings.Repeat("─", 20) + "\nShall I run the long command that\ncleans the build folder now?\n❯ 1. Yes\n  2. No\n"
	if MenuOf(strings.Replace(wide, strings.Repeat("─", 40), strings.Repeat("─", 20), 1)).Key != MenuOf(narrow).Key {
		t.Errorf("同一段话换宽度折行，仍是同一个对话框")
	}
	if MenuOf(narrow).Key == MenuOf(strings.Replace(narrow, "build", "cache", 1)).Key {
		t.Errorf("换了词就是另一个对话框")
	}
}

// 语料库：
// testdata/screens 下的真实抓屏，逐个解析有编号菜单的给出选项与稳定 key，
// 没有的安静返回 nil；绝不 panic。
func TestScreenCorpus(t *testing.T) {
	entries, err := filepath.Glob("../../testdata/screens/*.txt")
	if err != nil || len(entries) < 20 {
		t.Fatalf("语料不足 20 份: %v (%d)", err, len(entries))
	}
	menus := 0
	for _, file := range entries {
		//nolint:gosec // G304: 测试语料目录内的固定文件。
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		screen := string(text)
		m := MenuOf(screen)
		if m == nil {
			continue
		}
		menus++
		if len(m.Options) == 0 || m.Key == "" {
			t.Errorf("%s: 读出了菜单但没有选项或 key", file)
		}
		again := MenuOf(screen)
		if again == nil || again.Key != m.Key {
			t.Errorf("%s: 同一屏幕两次解析 key 不稳定", file)
		}
		for i, o := range m.Options {
			if o.N != i+1 {
				t.Errorf("%s: 编号不连续: %v", file, m.Options)
				break
			}
		}
	}
	t.Logf("语料 %d 份，其中 %d 份带编号菜单", len(entries), menus)
}

// 合成按钮：
// 屏幕没有编号、但自己声明了「Enter 前进 · Esc 取消」按钮忠实转述这两条路；
// 点击按它声明的键，绝不按数字。
func TestConfirmMenuSynthesizedFromTheScreenOwnWords(t *testing.T) {
	notice := " We're changing auto mode.\n\n Nothing breaks: auto mode keeps working.\n\n Enter to continue · Esc to cancel"
	m := ConfirmMenuOf(notice)
	if m == nil {
		t.Fatal("a screen that offers enter/esc must get buttons")
	}
	if len(m.Options) != 1 || m.Options[0].N != 1 {
		t.Fatalf("options = %+v, want the single continue", m.Options)
	}
	if got := m.Keys[1]; len(got) != 1 || got[0] != "enter" {
		t.Fatalf("keys[1] = %v, want exactly enter", got)
	}
	if m.Key != ConfirmMenuOf(notice).Key {
		t.Fatal("the same screen must keep the same key")
	}
	if MenuWithConfirm(notice) == nil || MenuWithConfirm(notice).Keys == nil {
		t.Fatal("MenuWithConfirm must hand back the synthesized menu")
	}
}

// Enter 跟光标走的屏 ( ❯ 选项、方向键导航 )、没有声明的屏，一律不合成。
func TestConfirmMenuRefusesWhatEnterWouldMisanswer(t *testing.T) {
	cases := map[string]string{
		"cursor choice":  " ❯ No, exit\n   Yes, I trust this folder\n\n Enter to confirm · Esc to cancel",
		"arrow navigate": " ↑/↓ Navigate · enter Confirm · esc Cancel",
		"no hints":       "just some text on a screen",
		"enter only":     " Press enter to continue",
	}
	for name, screen := range cases {
		if m := ConfirmMenuOf(screen); m != nil {
			t.Fatalf("%s: synthesized %+v, want none", name, m)
		}
	}
}

// 编号菜单永远优先，合成不与它混作。
func TestNumberedMenuWinsOverSynthesis(t *testing.T) {
	numbered := " Do you want to proceed?\n 1. Yes\n 2. No\n\n Esc to cancel · enter to confirm"
	m := MenuWithConfirm(numbered)
	if m == nil || m.Keys != nil {
		t.Fatalf("menu = %+v, want the numbered one without keys", m)
	}
}

// claude 的文件夹信任确认屏：给出走到 Yes 并确认的按键；别的屏一律不认。

const keyPressEnter = "enter"

func TestTrustAnswerKeys(t *testing.T) {
	safety := " Quick safety check: Is this a project you created or one that you trust?\n\n ❯ No, exit\n   Yes, I trust this folder\n\n Enter to confirm · Esc to cancel"
	keys, ok := TrustAnswerKeys(safety)
	if !ok || len(keys) != 2 || keys[0] != "down" || keys[1] != keyPressEnter {
		t.Fatalf("keys = %v %v, want down+enter", keys, ok)
	}
	files := " Do you trust the files in this folder?\n\n ❯ No, exit\n   Yes, I trust the files in this folder\n\n Enter to confirm · Esc to cancel"
	if keys, ok := TrustAnswerKeys(files); !ok || len(keys) != 2 {
		t.Fatalf("alt phrasing: %v %v", keys, ok)
	}
	// 光标已在 Yes 上：只按 enter。
	cursored := " Do you trust the files in this folder?\n\n   No, exit\n ❯ Yes, I trust the files in this folder\n\n Enter to confirm · Esc to cancel"
	if keys, ok := TrustAnswerKeys(cursored); !ok || len(keys) != 1 || keys[0] != keyPressEnter {
		t.Fatalf("cursored yes: %v %v", keys, ok)
	}
	// 别的屏：普通确认、菜单、空闲提示符——绝不认。
	for name, screen := range map[string]string{
		"notice":    " We're changing auto mode.\n\n Enter to continue · Esc to cancel",
		"menu":      " Proceed?\n 1. Yes\n 2. No\n\n Esc to cancel",
		"prompt":    " ❯ ",
		"no yes":    " Is this a project you created or one that you trust?\n\n ❯ No, exit\n   Exit anyway\n\n Enter to confirm",
		"no cursor": " Is this a project you created or one that you trust?\n\n   No, exit\n   Yes, I trust this folder\n\n Enter to confirm",
	} {
		if keys, ok := TrustAnswerKeys(screen); ok {
			t.Fatalf("%s: recognized as %v, want none", name, keys)
		}
	}
}
