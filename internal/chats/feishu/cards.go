// Package feishu 是 herdrchat 的飞书侧：卡片 schema 2.0 的模板与切分
// ( 移植自原版 lib/chats/feishu/client.mjs 的卡片部分 )。
//
// 卡片 JSON 是飞书 schema "2.0"：header 是色调图标标题，body 是 markdown 与
// 按钮元素。footer 的 markdown 记号写成 HTML 实体 ( 反斜杠转义飞书会原样显示 )。
package feishu

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jrmarcco/herdr-chat/internal/core"
)

// 卡片 JSON 的键与固定值 ( goconst 收拢 )。
const (
	kSchemaV  = "2.0"
	kSchema   = "schema"
	kConfig   = "config"
	kHeader   = "header"
	kBody     = "body"
	kElements = "elements"
	kContent  = "content"
	kTag      = "tag"
	kText     = "text"
	kName     = "name"
	kColor    = "color"
	kUpdate   = "update_multi"
	kPlain    = "plain_text"
	kDefault  = "default"
	iconDone  = "done_outlined"
	grey      = "grey"
	kNotation = "notation"
	kTextSize = "text_size"
	kMarkdown = "markdown"
)

// ChunkSize 是一张卡装的字节数 ( 卡片上限 30KB，留出 JSON 转义的余量 )。
const ChunkSize = 6000

// LiveBytes / FoldedBytes 是引擎契约里的可编辑卡与折叠帖容量。
const (
	LiveBytes   = 20000
	FoldedBytes = 20000
)

// chunk 把一段文本按行界切成几段；被切断的代码围栏闭合并在下一段重开。
func chunk(text string, size int) []string {
	var parts []string
	var current []string
	length := 0
	var fence string
	haveFence := false
	flush := func() {
		if len(current) == 0 {
			return
		}
		body := strings.Join(current, "\n")
		if haveFence {
			body += "\n```"
		}
		parts = append(parts, body)
		if haveFence {
			current = []string{fence}
			length = len(fence)
		} else {
			current = nil
			length = 0
		}
	}
	for _, raw := range strings.Split(text, "\n") {
		line := raw
		for {
			piece := line
			if len(piece) > size {
				piece = line[:size]
			}
			if length+len(piece)+1 > size && len(current) > 0 {
				flush()
			}
			current = append(current, piece)
			length += len(piece) + 1
			if strings.HasPrefix(strings.TrimLeft(piece, " \t"), "```") {
				if haveFence {
					haveFence = false
				} else {
					fence = strings.TrimSpace(piece)
					haveFence = true
				}
			}
			if len(line) <= size {
				break
			}
			line = line[size:]
		}
	}
	if len(current) > 0 {
		parts = append(parts, strings.Join(current, "\n"))
	}
	if len(parts) == 0 {
		parts = []string{""}
	}
	return parts
}

// footerEscape 把 footer 的 markdown 记号写成 HTML 实体。
var footerEsc = regexp.MustCompile(`[\\\` + "`" + `*_\[\]#>~|<&]`)

func escapeFooter(footer string) string {
	return footerEsc.ReplaceAllStringFunc(footer, func(c string) string {
		return fmt.Sprintf("&#%d;", c[0])
	})
}

// footerElement 是卡片底部的一行小字。
func footerElement(footer string) map[string]any {
	if footer == "" {
		return nil
	}
	return map[string]any{
		kTag: kMarkdown, kContent: escapeFooter(footer), kTextSize: kNotation,
	}
}

func footerElements(footer string) []any {
	if el := footerElement(footer); el != nil {
		return []any{el}
	}
	return nil
}

// toneLook 是一种色调在飞书里的样子：模板色 + 标准图标。
type toneLook struct {
	Template string
	Icon     string
}

var tones = map[core.Tone]toneLook{
	core.ToneWorking: {"blue", "loading_outlined"},
	core.ToneClosed:  {grey, iconDone},
	core.ToneNeeds:   {"orange", "warning_outlined"},
	core.ToneDone:    {grey, iconDone},
	core.ToneGood:    {"green", "done_outlined"},
	core.ToneBad:     {"red", "warning_outlined"},
}

// leadingEmoji 剥掉标题领头的 emoji ( RE2 没有 Extended_Pictographic，用
// 符号块近似：杂项符号、表情符号块、变体选择符与零宽连接符 )。
var leadingEmoji = regexp.MustCompile(`^(?:[\x{2190}-\x{27BF}\x{2B00}-\x{2BFF}\x{FE0F}\x{200D}][\x{FE0F}\x{200D}]*\s*)+`)

// cardHeader 是卡片的头：标题 ( 去掉 core 领头的 emoji——图标已经说了 ) 与
// 色调模板色。
func cardHeader(title string, tone core.Tone) map[string]any {
	look, ok := tones[tone]
	if !ok {
		look = tones[core.ToneWorking]
	}
	plain := leadingEmoji.ReplaceAllString(title, "")
	if plain == "" {
		plain = title
	}
	return map[string]any{
		"title":    map[string]any{kTag: kPlain, kContent: plain},
		"template": look.Template,
		"icon":     map[string]any{kTag: "standard_icon", "token": look.Icon, kColor: look.Template},
	}
}

// cardBody 是卡片身体的 markdown 元素 + footer。
func cardBody(markdown, footer string) map[string]any {
	elements := []any{map[string]any{kTag: kMarkdown, kContent: markdown}}
	elements = append(elements, footerElements(footer)...)
	return map[string]any{kElements: elements}
}

// PlainCard 是一张发出后还可编辑的 markdown 卡 ( update_multi )。
func PlainCard(markdown, title string, tone core.Tone, footer string) map[string]any {
	return map[string]any{
		kSchema: kSchemaV,
		kConfig: map[string]any{kUpdate: true},
		kHeader: cardHeader(title, tone),
		kBody:   cardBody(markdown, footer),
	}
}

// buttonTextMax 是按钮文本的截断。
const buttonTextMax = 80

// button 是一个回调按钮 ( 点击发 card.action.trigger )。
func button(text string, value map[string]any, buttonType string) map[string]any {
	short := text
	runes := []rune(text)
	if len(runes) > buttonTextMax {
		short = string(runes[:buttonTextMax-1]) + "…"
	}
	return map[string]any{
		kTag: "button", kType: buttonType, "width": kDefault, "size": kDefault,
		kText:        map[string]any{kTag: kPlain, kContent: short},
		"hover_tips": map[string]any{kTag: kPlain, kContent: text},
		"behaviors":  []any{map[string]any{kType: "callback", "value": value}},
	}
}

// buttonRow 把按钮并排 ( flow 布局：窄屏自动换行 )。
func buttonRow(buttons []any) map[string]any {
	columns := make([]any, 0, len(buttons))
	for _, b := range buttons {
		columns = append(columns, map[string]any{kTag: "column", "width": "auto", kElements: []any{b}})
	}
	return map[string]any{
		kTag: "column_set", "flex_mode": "flow", "horizontal_spacing": "small",
		"columns": columns,
	}
}

// sideBySide 说几个短选项可以并排 ( 长选项独行，免得被截 )。
func sideBySide(labels []string) bool {
	if len(labels) > 4 {
		return false
	}
	for _, l := range labels {
		if len([]rune(l)) > 14 {
			return false
		}
	}
	return true
}

func optionsBlock(buttons []any, labels []string) []any {
	if sideBySide(labels) {
		return []any{buttonRow(buttons)}
	}
	return buttons
}

// boxPrefix 是多选框在表单里的名字前缀 ( form_value 里按 box_<n> 读回 )。
const boxPrefix = "box_"

// MenuCard 是提问的卡：屏幕正文、每选项一个按钮、自由输入的表单框。
// 按钮与框各送一个回调，值是 value 加 {pick, label} ( 或 pick: "tab"/"esc" )；
// 框里的文字以 form_value.answer 回来。多选的框是 form 里的 checker
// ( 无 behaviors：只在手机上打勾，不发调用 )，Next 提交整张表。
func MenuCard(title, markdown string, menu *core.Menu, value map[string]any, footer string) map[string]any {
	elements := []any{map[string]any{kTag: kMarkdown, kContent: markdown}}
	var boxes []any
	var choices []any
	var labels []string
	for _, o := range menu.Options {
		if menu.Free != nil && o.N == menu.Free.N {
			continue
		}
		if menu.Multi && o.Checked != nil {
			boxes = append(boxes, map[string]any{
				kTag: "checker", kName: fmt.Sprintf("%s%d", boxPrefix, o.N),
				"checked": *o.Checked,
				kText:     map[string]any{kTag: kPlain, kContent: fmt.Sprintf("%d. %s", o.N, o.Label)},
			})
			continue
		}
		buttonType := "default"
		if o.N == 1 && !menu.Multi {
			buttonType = "primary_filled"
		}
		v := withPick(value, fmt.Sprint(o.N), o.Label)
		choices = append(choices, button(fmt.Sprintf("%d. %s", o.N, o.Label), v, buttonType))
		labels = append(labels, fmt.Sprintf("%d. %s", o.N, o.Label))
	}
	if len(choices) > 0 {
		elements = append(elements, optionsBlock(choices, labels)...)
	}
	if menu.Multi {
		elements = append(elements, map[string]any{
			kTag: "form", kName: "boxes",
			kElements: append(boxes, formSubmit("Next ⇥", withPick(value, "tab", ""), "next")),
		})
	}
	if menu.Free != nil {
		elements = append(elements, map[string]any{
			kTag: "form", kName: "free",
			kElements: []any{
				map[string]any{
					kTag: "input", kName: "answer", "required": true,
					"placeholder": map[string]any{kTag: kPlain, kContent: menu.Free.Label},
				},
				formSubmit("Send", withPick(value, fmt.Sprint(menu.Free.N), menu.Free.Label), "send"),
			},
		})
	}
	elements = append(elements, buttonRow([]any{button("Esc (cancel)", withPick(value, pickEsc, ""), "danger_text")}))
	elements = append(elements, footerElements(footer)...)
	return map[string]any{
		kSchema: kSchemaV,
		kConfig: map[string]any{kUpdate: true},
		kHeader: cardHeader(title, core.ToneNeeds),
		kBody:   map[string]any{kElements: elements},
	}
}

const pickEsc = "esc"

// formSubmit 是表单里带提交行为的按钮。
func formSubmit(text string, value map[string]any, name string) map[string]any {
	b := button(text, value, "primary_filled")
	b["name"] = name
	b["form_action_type"] = "submit"
	return b
}

// withPick 把 pick 与 label 叠进卡片的值。
func withPick(value map[string]any, pick, label string) map[string]any {
	out := make(map[string]any, len(value)+2)
	for k, v := range value {
		out[k] = v
	}
	out["pick"] = pick
	if label != "" {
		out["label"] = label
	}
	return out
}

// OwnerCard 是发给 owner 的带按钮的卡。
func OwnerCard(title string, tone core.Tone, markdown string, footer string, buttons [][2]string) map[string]any {
	card := PlainCard(markdown, title, tone, footer)
	if len(buttons) > 0 {
		body, _ := card["body"].(map[string]any)
		if body != nil {
			elements, _ := body["elements"].([]any)
			var btns []any
			for i, b := range buttons {
				kind := "default"
				if i == 0 {
					kind = "primary_filled"
				}
				btns = append(btns, button(b[0], map[string]any{"owner": b[1]}, kind))
			}
			body["elements"] = append(elements, buttonRow(btns))
		}
	}
	return card
}

var doneLead = regexp.MustCompile(`^[✔✓]\x{FE0F}?\s*`)

// DoneCard 是对话框答完的卡：灰色、原文、结论小字 ( 图标就是那个勾 )。
func DoneCard(title, markdown, done, footer string) map[string]any {
	body := []any{
		map[string]any{kTag: kMarkdown, kContent: markdown},
		map[string]any{
			kTag: kMarkdown, kContent: doneLead.ReplaceAllString(done, ""), kTextSize: kNotation,
			"icon": map[string]any{kTag: "standard_icon", "token": iconDone, kColor: grey},
		},
	}
	body = append(body, footerElements(footer)...)
	return map[string]any{
		kSchema: kSchemaV,
		kConfig: map[string]any{kUpdate: true},
		kHeader: cardHeader(title, core.ToneDone),
		kBody:   map[string]any{kElements: body},
	}
}

// FoldedCard 是一段可展开的历史 ( collapsible_panel )。
func FoldedCard(title, markdown string) map[string]any {
	return map[string]any{
		kSchema: kSchemaV,
		kBody: map[string]any{kElements: []any{map[string]any{
			kTag: "collapsible_panel", "expanded": false,
			kHeader:   map[string]any{"title": map[string]any{kTag: kMarkdown, kContent: "**" + title + "**"}},
			"border":  map[string]any{kColor: grey, "corner_radius": "5px"},
			kElements: []any{map[string]any{kTag: kMarkdown, kContent: markdown}},
		}}},
	}
}

// TagMax 是租户标签名的上限 ( 飞书收 16 字符，再长的按最后的 -_. 截断 )。
const TagMax = 16

// TagName 清洗标签名：超长时在最后一个放得下的 "-" "_" "." 处截。
func TagName(name string) string {
	runes := []rune(name)
	if len(runes) <= TagMax {
		return name
	}
	cut := -1
	for i := 0; i < TagMax && i < len(runes); i++ {
		if runes[i] == '-' || runes[i] == '_' || runes[i] == '.' {
			cut = i
		}
	}
	if cut > 0 {
		return string(runes[:cut])
	}
	return string(runes[:TagMax])
}

// TicksOf 从提交的表单里读勾选框：{<n>: true|false}；一个框都没回来时 nil
// ( 什么都不知道，也什么都不该按 )。checker 的值 true 或 "true" 都算真。
func TicksOf(formValue map[string]any) map[string]bool {
	var out map[string]bool
	for name, v := range formValue {
		if !strings.HasPrefix(name, boxPrefix) {
			continue
		}
		n, err := strconv.Atoi(name[len(boxPrefix):])
		if err != nil {
			continue
		}
		on := v == true
		if s, ok := v.(string); ok {
			on = s == "true"
		}
		if out == nil {
			out = map[string]bool{}
		}
		out[fmt.Sprint(n)] = on
	}
	return out
}

// MessageParts 是收到的消息说的东西：文本与图片键，按序；其他类型返回 ok=false。
func MessageParts(messageType, content string) (text string, images []string, ok bool) {
	var c map[string]any
	if json.Unmarshal([]byte(content), &c) != nil || c == nil {
		return "", nil, false
	}
	switch messageType {
	case kText:
		t, _ := c["text"].(string)
		return t, nil, true
	case "image":
		key, _ := c["image_key"].(string)
		return "", []string{key}, true
	case "post":
		var lines []string
		if title, _ := c["title"].(string); title != "" {
			lines = append(lines, title)
		}
		paragraphs, _ := c["content"].([]any)
		for _, paragraph := range paragraphs {
			els, _ := paragraph.([]any)
			line := ""
			for _, e := range els {
				el, _ := e.(map[string]any)
				if el == nil {
					continue
				}
				if el["tag"] == "img" {
					if key, _ := el["image_key"].(string); key != "" {
						images = append(images, key)
					}
				} else if el["tag"] == "a" {
					href, _ := el["href"].(string)
					t, _ := el["text"].(string)
					line += t + " (" + href + ")"
				} else if t, isStr := el["text"].(string); isStr {
					line += t
				}
			}
			lines = append(lines, line)
		}
		return strings.TrimSpace(strings.Join(lines, "\n")), images, true
	}
	return "", nil, false
}
