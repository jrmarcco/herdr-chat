package core

import (
	"crypto/sha1" //nolint:gosec // G505: 对话框身份哈希，非密码学用途
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 屏幕菜单解析：
// 程序被挡在屏幕上画的编号菜单 —— Claude Code 的授权与提问、
// Codex 的审批 —— 从屏幕文字里读出选项，让聊天里的按钮代替按键。
//
// 屏幕上最后一段 1、2、3… 连续编号就是菜单，选项行之间的行 ( 描述 ) 跳过；
// 顶边框 ( 一行 ─ ) 之上是对话框的正文。
// 身份 key 从顶边框到最后一个选项做哈希，剔除光标符、
// 勾选标记与预览框——同一个对话框换一种折行 ( 终端 resize ) 还是同一个，
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
