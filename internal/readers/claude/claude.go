// Package claude 是 Claude Code 的 reader ( hc status app "claude-code" )：
// 读它自己的 transcript。
//
//	定位：终端前台 pid 在 ~/.claude/sessions/<pid>.json 里指名会话
//	( {sessionId, cwd} )；transcript 在
//	~/.claude/projects/<cwd 的非字母数字全替换为 '-'>/<sessionId>.jsonl。
//
// 条目：you 是你敲的话 ( 人的输入、turn 中途排进去的消息、你对提问的回答 )；
// ai 是它的文本 ( end 标记 turn 的最后一条回答，stop_reason "end_turn" )，
// 工作中说的有些话只存成 "narration" 块 ( 签名里带 narration 的 thinking 块 )，
// 那些算文本，普通 thinking 不算；tool 是一次工具调用一行；image 是你贴的图
// 或工具给它看的图。footer 的数据 ( 模型、上下文用量 ) 随回答带来；窗口与
// 限额由 Claude Code 自答 ( claude -p /context、/usage，不产生模型调用 )。
package claude

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/asks"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/state"
)

// App / Short / Processes 是 reader 契约的三件套。
const (
	typeUser = "user"
	App      = "claude-code"
	Short    = "claude"
)

// Processes 是它的进程在 hc foreground 里的名字。
var Processes = []string{"claude"}

// Reader 读 Claude Code 的 transcript；依赖注入，测试全用 fake。
type Reader struct {
	HC    core.HC
	Files core.HomeFiles
	Asks  *asks.Asks
	Cache *core.Meter
	// Shell 跑 meter 的 claude -p ( hccli.CLI.HomeShell 同一形态 )。
	Shell func(ctx context.Context, session, machine, script string) (string, error)
}

// App / Short / Processes：reader 契约 ( App 是 hc status 里的名，Short 是
// footer 里的短名 )。
func (r *Reader) App() string         { return App }
func (r *Reader) Short() string       { return Short }
func (r *Reader) Processes() []string { return Processes }

// textOf 取一条消息里的文本 ( 带 tool_result 的不算 )。
func textOf(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		hasResult := false
		for _, b := range c {
			m, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "tool_result" {
				hasResult = true
			}
			if m["type"] == "text" {
				if t, _ := m["text"].(string); t != "" {
					parts = append(parts, t)
				}
			}
		}
		if hasResult {
			return ""
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// isNarration：带词的 thinking 块、签名里写着 narration——它工作时说的话
// 只落盘在这种块里。
func isNarration(block map[string]any) bool {
	if block["type"] != "thinking" {
		return false
	}
	thinking, _ := block["thinking"].(string)
	sig, _ := block["signature"].(string)
	if strings.TrimSpace(thinking) == "" || sig == "" {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), "narration")
}

// toolLine 把一次工具调用说成一行：叫什么、干什么 ( 截 120 字 )。
func toolLine(block map[string]any) string {
	input, _ := block["input"].(map[string]any)
	what := ""
	for _, key := range []string{"description", "command", "file_path", "pattern", "url", "prompt"} {
		if v, _ := input[key].(string); v != "" {
			what = v
			break
		}
	}
	name, _ := block["name"].(string)
	if name == "" {
		name = "tool"
	}
	short := strings.Join(strings.Fields(what), " ")
	if len(short) > 120 {
		short = short[:117] + "..."
	}
	if short == "" {
		return "▸ " + name
	}
	return "▸ " + name + ": " + short
}

// taskCard ( FINAL_PLAN §2.B )：Claude 把活儿交给子代理 ( Task 工具 ) 时，
// 会话里单独成卡的一笔委托——交待了什么。
func taskCard(block map[string]any) *core.Card {
	if block["name"] != "Task" {
		return nil
	}
	input, _ := block["input"].(map[string]any)
	title, _ := input["description"].(string)
	if title == "" {
		if p, _ := input["prompt"].(string); p != "" {
			title = p
		}
	}
	if len([]rune(title)) > 80 {
		title = string([]rune(title)[:79]) + "…"
	}
	if title == "" {
		return nil
	}
	return &core.Card{Kind: "subagent", Title: title, State: "started"}
}

// imagesOf 取一条消息里的图：你贴的 ( 顶层块 ) 与工具给它看的
// ( tool_result 里：截图、读的图文件 )。
func imagesOf(content any, human bool) []core.Item {
	blocks, ok := content.([]any)
	if !ok {
		return nil
	}
	var out []core.Item
	add := func(b map[string]any, from string) {
		if b["type"] != "image" {
			return
		}
		source, _ := b["source"].(map[string]any)
		if source == nil || source["type"] != "base64" {
			return
		}
		data, _ := source["data"].(string)
		media, _ := source["media_type"].(string)
		if data != "" {
			out = append(out, core.Item{Role: core.RoleImage, From: from, Media: media, DataB64: data})
		}
	}
	for _, b := range blocks {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if human {
			add(m, core.RoleYou)
		}
		if m["type"] == "tool_result" {
			if inner, ok := m["content"].([]any); ok {
				for _, x := range inner {
					if xm, ok := x.(map[string]any); ok {
						add(xm, core.RoleAI)
					}
				}
			}
		}
	}
	return out
}

// EntryItems 是一条 transcript 条目里的东西，按块序 ( 一条可同时有文本与工具 )。
func EntryItems(entry map[string]any) []core.Item {
	if truthy(entry["isSidechain"]) {
		return nil
	}
	etype, _ := entry["type"].(string)
	message, _ := entry["message"].(map[string]any)
	isMeta := truthy(entry["isMeta"])
	origin, _ := entry["origin"].(map[string]any)
	originKind, _ := origin["kind"].(string)

	if etype == typeUser && !isMeta && originKind == "human" {
		t := strings.TrimSpace(textOf(message["content"]))
		var out []core.Item
		if t != "" {
			out = append(out, core.Item{Role: core.RoleYou, Text: t})
		}
		return append(out, imagesOf(message["content"], true)...)
	}
	if etype == typeUser && !isMeta {
		if shown := imagesOf(message["content"], false); len(shown) > 0 {
			return shown
		}
	}
	if etype == typeUser {
		if result, ok := entry["toolUseResult"].(map[string]any); ok {
			if answers, ok := result["answers"].(map[string]any); ok && len(answers) > 0 {
				keys := make([]string, 0, len(answers))
				for k := range answers {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				var lines []string
				for _, q := range keys {
					lines = append(lines, fmt.Sprintf("%v → %v", q, answers[q]))
				}
				if t := strings.Join(lines, "\n"); t != "" {
					return []core.Item{{Role: core.RoleYou, Text: t}}
				}
			}
		}
		return nil
	}
	if etype == "attachment" {
		attachment, _ := entry["attachment"].(map[string]any)
		if attachment == nil || attachment["type"] != "queued_command" {
			return nil
		}
		if o, _ := attachment["origin"].(map[string]any); o != nil && o["kind"] == "human" {
			if t := strings.TrimSpace(textOf(attachment["prompt"])); t != "" {
				return []core.Item{{Role: core.RoleYou, Text: t}}
			}
		}
		return nil
	}
	if etype != "assistant" {
		return nil
	}
	content, ok := message["content"].([]any)
	if !ok {
		return nil
	}
	end := message["stop_reason"] == "end_turn"
	model, _ := message["model"].(string)
	if strings.HasPrefix(model, "<") { // "<synthetic>"：Claude Code 自己编的回复
		model = ""
	}
	usage, _ := message["usage"].(map[string]any)
	var used int64
	if usage != nil {
		used = num(usage["input_tokens"]) + num(usage["cache_read_input_tokens"]) + num(usage["cache_creation_input_tokens"])
	}
	var context *core.ContextUse
	if model != "" && used > 0 {
		context = &core.ContextUse{Used: used}
	}
	var out []core.Item
	for _, b := range content {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		said := ""
		if block["type"] == "text" {
			said, _ = block["text"].(string)
		} else if isNarration(block) {
			said, _ = block["thinking"].(string)
		}
		if strings.TrimSpace(said) != "" {
			out = append(out, core.Item{Role: core.RoleAI, Text: strings.TrimSpace(said), End: end, Model: model, Context: context})
		} else if block["type"] == "tool_use" {
			out = append(out, core.Item{Role: core.RoleTool, Text: toolLine(block), Model: model, Context: context})
			if card := taskCard(block); card != nil {
				out = append(out, core.Item{Role: core.RoleCard, Card: card})
			}
		}
	}
	// 只有 thinking：没什么可展示，但上下文多满现在知道了。
	if len(out) == 0 && context != nil {
		out = append(out, core.Item{Role: core.RoleMeta, Model: model, Context: context})
	}
	return out
}

func truthy(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// App/Processes/After/Rest：reader 契约。

// After 给出游标之后的新内容；终端里没有可读的 Claude 时 Reading 为 nil。
// 会话换了 transcript ( /clear、resume ) 时，旧文件里剩的先发。
func (r *Reader) After(ctx context.Context, session, address string, cur state.Cursor) (*core.Reading, error) {
	machine := hccli.MachineOf(address)
	term, err := r.HC.GetTerminal(ctx, session, address)
	if err != nil {
		// 终端读不到 ( 已结束等 )：没有可读的 Claude，不是错误。
		return nil, nil //nolint:nilerr // 读不到即不可读，是结论
	}
	pid := foregroundPid(term)
	if pid == 0 {
		return nil, nil
	}
	metaText, err := r.Files.HomeFile(ctx, session, machine, fmt.Sprintf(".claude/sessions/%d.json", pid), 0)
	if err != nil {
		return nil, nil //nolint:nilerr // 会话文件不在即不可读
	}
	var meta struct {
		SessionID string `json:"sessionId"`
		Cwd       string `json:"cwd"`
	}
	if json.Unmarshal([]byte(metaText), &meta) != nil || meta.SessionID == "" || meta.Cwd == "" {
		return nil, nil //nolint:nilerr // 会话文件读不出即不可读
	}
	file := fmt.Sprintf(".claude/projects/%s/%s.jsonl", nonAlphaNum(meta.Cwd), meta.SessionID)
	var items []core.Item
	if cur.File != "" && cur.File != file {
		items = append(items, core.LogAfter(ctx, r.Files, session, machine, cur.File, cur, EntryItems)...)
	}
	items = append(items, core.LogAfter(ctx, r.Files, session, machine, file, cur, EntryItems)...)
	return &core.Reading{Items: items, File: file}, nil
}

// Rest 给出程序没了之后游标文件里还剩下的 ( 它的遗言 )。
func (r *Reader) Rest(ctx context.Context, session, address string, cur state.Cursor) ([]core.Item, error) {
	if cur.File == "" {
		return nil, nil
	}
	return core.LogAfter(ctx, r.Files, session, hccli.MachineOf(address), cur.File, cur, EntryItems), nil
}

func foregroundPid(term hccli.Terminal) int64 {
	fg, _ := term["foreground"].(map[string]any)
	if fg == nil {
		return 0
	}
	return num(fg["pid"])
}

var nonAlpha = regexp.MustCompile(`[^A-Za-z0-9]`)

func nonAlphaNum(s string) string { return nonAlpha.ReplaceAllString(s, "-") }

// ---------- 上下文窗口与账号限额：Claude Code 自答 ----------

// meter 的缓存时长与首等上限。
const (
	limitsTTL = 5 * time.Minute
	windowTTL = 24 * time.Hour
	firstWait = 10 * time.Second
)

// ContextWindowIn 从 /context 的输出读窗口："**Tokens:** 13.8k / 1m (1%)" -> 1000000。
func ContextWindowIn(text string) int64 {
	m := regexp.MustCompile(`(?i)Tokens:\**\s*[\d.]+[km]?\s*/\s*([\d.]+)\s*([km]?)`).FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	base := parseFloat(m[1])
	switch strings.ToLower(m[2]) {
	case "k":
		return int64(base * 1e3)
	case "m":
		return int64(base * 1e6)
	}
	return int64(base)
}

// LimitsIn 从 /usage 的输出读限额；它一条都没说 ( API key 没有限额 ) 时为空。
func LimitsIn(text string) []core.Limit {
	var limits []core.Limit
	for _, pair := range []struct {
		re   *regexp.Regexp
		name string
	}{
		{regexp.MustCompile(`(?m)^Current session:\s*([\d.]+)% used`), "5-hour"},
		{regexp.MustCompile(`(?m)^Current week \(all models\):\s*([\d.]+)% used`), "7-day"},
	} {
		if m := pair.re.FindStringSubmatch(text); m != nil {
			limits = append(limits, core.Limit{Name: pair.name, Used: parseFloat(m[1])})
		}
	}
	return limits
}

// Meter 给 footer 拿窗口与限额 ( reader 契约的可选能力 Metered )。
func (r *Reader) Meter(ctx context.Context, session, address string, meta map[string]any) (core.MeterInfo, error) {
	machine := hccli.MachineOf(address)
	model, _ := meta["model"].(string)
	info := core.MeterInfo{}
	if model != "" {
		v := r.Cache.KnownSoon("claude|"+machine+"|window|"+model, windowTTL, func() (any, error) {
			out, err := r.Shell(ctx, session, machine, fmt.Sprintf("claude -p /context --model '%s' --no-session-persistence 2>/dev/null", model))
			if err != nil {
				return nil, err
			}
			w := ContextWindowIn(out)
			if w == 0 {
				return nil, nil // 不缓存读不出的
			}
			return w, nil
		}, firstWait)
		if w, ok := v.(int64); ok {
			info.ContextSize = w
		}
	}
	v := r.Cache.KnownSoon("claude|"+machine+"|limits", limitsTTL, func() (any, error) {
		out, err := r.Shell(ctx, session, machine, "claude -p /usage --no-session-persistence 2>/dev/null")
		if err != nil {
			return nil, err
		}
		limits := LimitsIn(out)
		if limits == nil {
			return nil, nil
		}
		return limits, nil
	}, firstWait)
	if limits, ok := v.([]core.Limit); ok {
		info.Limits = limits
	}
	return info, nil
}
