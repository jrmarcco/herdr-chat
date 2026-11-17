package core

import (
	"crypto/sha1" //nolint:gosec // G505: 对话框身份哈希，非密码学用途
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jrmarcco/herdr-chat/internal/hccli"
)

// 屏幕菜单解析：
// 程序被挡在屏幕上画的编号菜单 Claude Code 的授权与提问、
// Codex 的审批从屏幕文字里读出选项，让聊天里的按钮代替按键。
//
// 屏幕上最后一段 1、2、3… 连续编号就是菜单，选项行之间的行 ( 描述 ) 跳过；
// 顶边框 ( 一行 ─ ) 之上是对话框的正文。
// 身份 key 从顶边框到最后一个选项做哈希，剔除光标符、
// 勾选标记与预览框，同一个对话框换一种折行 ( 终端 resize ) 还是同一个，
// 旧卡片答不了长得像的新对话框。
var (
	menuOption     = regexp.MustCompile(`^\s*(?:[❯›]\s*)?(\d{1,2})\.\s+(?:\[(.)\]\s+)?(.*\S)\s*$`)
	menuCursor     = regexp.MustCompile(`[❯›]`)
	menuRule       = regexp.MustCompile(`^[\s─╌━-]*$`)
	menuBorder     = regexp.MustCompile(`^\s*─{10,}`)
	menuFree       = regexp.MustCompile(`^Type something`)
	menuBeside     = regexp.MustCompile(`\s{3,}[┌└│├┤┐┘╭╰─┬┴┼║╔╚].*$`)
	menuLeftBorder = regexp.MustCompile(`^(\s*)│\s?`)
	menuItem       = regexp.MustCompile(`^•\s`)
	// 勾选标记：算 key 时剔除 ( 同一对话框的标记会随作答变化 )。
	menuMarks   = regexp.MustCompile(`[☐☑☒✔✓✗✘■□●○◉◯]`)
	menuNoBox   = regexp.MustCompile(`^(\s*\d{1,2}\.\s+)\[.\]`)
	menuAskIDRe = regexp.MustCompile(`^(\d+)-(\d+)$`)
)

// menuTextLines 是无顶边框时向上取的行数。
const menuTextLines = 15

// ownPart 拿到一个选项行的"自己那部分"：
// 预览框 ( beside 模式下画在选项右侧 ) 既不算选项文字也不算 key；
// 多问题对话框的左框线也拿掉。
func ownPart(line string) string {
	line = menuBeside.ReplaceAllString(line, "")
	return menuLeftBorder.ReplaceAllString(line, "$1")
}

// topOf 找对话框的顶：
// 选项区上方最近的顶边框；没有则最近的 Codex 条目行 ( 也是已知边缘 )；
// 再没有就取选项上方 menuTextLines 行 ( framed=false，不知是不是对话框 )。
func topOf(lines []string, first int) (top int, framed bool) {
	top = first
	for top > 0 && !menuBorder.MatchString(lines[top]) {
		top--
	}
	if menuBorder.MatchString(lines[top]) {
		return top, true
	}
	for i := first - 1; i >= 0; i-- {
		if menuItem.MatchString(lines[i]) {
			return i, true
		}
	}
	if first-menuTextLines > 0 {
		return first - menuTextLines, false
	}
	return 0, false
}

// keyOf 算对话框的身份：
// 从顶到最后选项，去光标符、去选项前导勾选框、去勾选标记，
// 以空格连接 ( 不用换行：同一对话框换个宽度折行不同，仍是同一个 )。
func keyOf(lines []string, top, last int) string {
	var parts []string
	for _, l := range lines[top : last+1] {
		l = menuCursor.ReplaceAllString(l, "")
		l = menuNoBox.ReplaceAllString(l, "$1")
		l = menuMarks.ReplaceAllString(l, "")
		parts = append(parts, l)
	}
	body := strings.Join(parts, " ")
	body = strings.Join(strings.Fields(body), " ")

	//nolint:gosec // G401: 对话框身份哈希，非安全用途。
	sum := sha1.Sum([]byte(body))
	return hex.EncodeToString(sum[:])[:12]
}

// MenuOf 解析一屏文字。没有编号菜单 ( 或编号不连续 ) 返回 nil。
func MenuOf(screenText string) *Menu {
	raw := strings.Split(screenText, "\n")
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = ownPart(l)
	}
	type run struct {
		first   int
		last    int
		options []MenuOption
	}
	var current *run
	var menu *run
	for i, line := range lines {
		m := menuOption.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if n == 1 {
			current = &run{first: i}
		} else if current == nil || n != len(current.options)+1 {
			current = nil
			continue
		}
		opt := MenuOption{N: n, Label: m[3]}
		if m[2] != "" {
			// [x] / [ ]：勾选框。
			checked := m[2] != " "
			opt.Checked = &checked
		}
		current.options = append(current.options, opt)
		current.last = i
		menu = current
	}
	if menu == nil {
		return nil
	}
	top, framed := topOf(lines, menu.first)
	out := &Menu{Key: keyOf(lines, top, menu.last), Framed: framed}
	out.Text = dialogText(lines, top, menu.first)
	out.Options = menu.options
	for _, o := range out.Options {
		if o.Checked != nil {
			out.Multi = true
		}
		if menuFree.MatchString(o.Label) {
			free := o
			out.Free = &free
		}
	}

	// 带预览框的问题不吃数字键 ( 它的按键说明是 "↑/↓ to navigate" )：
	// 选项从光标 ( ❯ ) 出发用方向键走到，再 Enter。
	out.Beside = false
	for _, l := range raw[menu.first : menu.last+1] {
		if menuBeside.MatchString(l) {
			out.Beside = true
			break
		}
	}
	out.Cursor = 1
	for _, o := range out.Options {
		re := regexp.MustCompile(`^\s*[❯›]\s*` + fmt.Sprint(o.N) + `\.`)
		for _, l := range raw[menu.first : menu.last+1] {
			if menuCursor.MatchString(l) && re.MatchString(l) {
				out.Cursor = o.N
			}
		}
	}
	return out
}

// dialogText 是选项区上方的对话框正文：去掉横线、去掉首尾空白。
func dialogText(lines []string, top, first int) string {
	var kept []string
	for _, l := range lines[top:first] {
		if menuRule.MatchString(l) {
			continue
		}
		kept = append(kept, strings.TrimSpace(l))
	}

	// 正文里的空行保留 ( 对话框正文本身可以有空行；只滤 RULE，join 时保留 )。
	return strings.Join(kept, "\n")
}

// AskID 校验 herdrchat.ask 标签的形状 "<秒>-<pid>"。
func AskID(id string) bool { return menuAskIDRe.MatchString(id) }

// 屏幕自己声明的确认路：Enter 前进、Esc 取消。
var (
	enterHintRe    = regexp.MustCompile(`(?i)enter[^\n]{0,30}(continue|confirm|proceed)`)
	escHintRe      = regexp.MustCompile(`(?i)esc[^\n]{0,20}cancel`)
	cursorChoiceRe = regexp.MustCompile(`(?m)^\s*[❯›]\s*\S`)
	arrowNavRe     = regexp.MustCompile(`↑|↓`)
)

// confirmTailLines 是合成判定只看的屏尾行数 ( 提示行坐在底部 )。
const confirmTailLines = 8

// ConfirmMenuOf 把「Enter to continue · Esc to cancel」形状的无编号屏合成按钮：
// 屏幕自己声明了 Enter 前进、Esc 取消，按钮忠实转述这两条路：
// 点击按它声明的键，绝不按数字。
// 取消由卡自带的 Esc 按钮走，故只此一项。
//
// 光标选择 ( ❯ 停在某个选项上 ) 或方向键导航的屏不吃 Enter，
// 那要按位置走，Enter 会确认光标所指，无论指什么一律不合成。
// 没有这形状返回 nil。
func ConfirmMenuOf(screenText string) *Menu {
	lines := strings.Split(screenText, "\n")
	if n := len(lines); n > confirmTailLines {
		lines = lines[n-confirmTailLines:]
	}
	tail := strings.Join(lines, "\n")
	if !enterHintRe.MatchString(tail) || !escHintRe.MatchString(tail) {
		return nil
	}
	if cursorChoiceRe.MatchString(screenText) || arrowNavRe.MatchString(screenText) {
		return nil
	}

	// 身份哈希盖住卡片正文那一屏 ( 看见什么，核对的就是什么 )。
	shown := hccli.TidyScreen(screenText, menuTextLines)
	//nolint:gosec // G401: 对话框身份哈希，非安全用途。
	sum := sha1.Sum([]byte(shown))
	return &Menu{
		Key: "cfm" + hex.EncodeToString(sum[:])[:9],
		// 与编号菜单同一纪律：两次读到同一个 key 才采信。
		Framed:  true,
		Options: []MenuOption{{N: 1, Label: "Continue (Enter)"}},
		Keys:    map[int][]string{1: {"enter"}},
	}
}

// claude 的文件夹信任确认屏：问句、Yes 选项行与光标行。
var (
	trustAskRe  = regexp.MustCompile(`(?i)is this a project (?:you created|or one that you trust)|do you trust`)
	trustYesRe  = regexp.MustCompile(`(?im)^\s*(?:[❯›]\s*)?yes\b[^\n]*trust`)
	trustLineRe = regexp.MustCompile(`(?m)^\s*[❯›]\s*\S`)
)

// TrustAnswerKeys：屏幕若是 claude 的文件夹信任确认 ( ❯ 停在 No, exit 上、
// Yes … trust 在选项里 )，给出把光标走到 Yes 再确认的按键序列；
// 不是这个屏、或认不出选项，返回 false——绝不盲按。
func TrustAnswerKeys(screen string) ([]string, bool) {
	if !trustAskRe.MatchString(screen) {
		return nil, false
	}
	lines := strings.Split(screen, "\n")
	cursor, yes := -1, -1
	for i, l := range lines {
		if cursor < 0 && trustLineRe.MatchString(l) {
			cursor = i
		}
		if yes < 0 && trustYesRe.MatchString(l) {
			yes = i
		}
	}
	if cursor < 0 || yes < 0 {
		return nil, false
	}
	var keys []string
	for moves := yes - cursor; moves != 0; {
		if moves > 0 {
			keys = append(keys, "down")
			moves--
			continue
		}
		keys = append(keys, "up")
		moves++
	}
	return append(keys, "enter"), true
}

// MenuWithConfirm：一屏的菜单。编号菜单优先；
// 没有编号、但屏幕自己声明了 Enter/Esc 两条路的，合成转述按钮。
func MenuWithConfirm(screenText string) *Menu {
	if m := MenuOf(screenText); m != nil {
		return m
	}
	return ConfirmMenuOf(screenText)
}
