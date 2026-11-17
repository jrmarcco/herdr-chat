// Package codex 是 Codex 的 reader ( hc status app "codex" )：读它自己的
// rollout 日志。
//
//	定位：Codex 把每个会话写进 ~/.codex/sessions/YYYY/MM/DD/rollout-<时间>-<id>.jsonl，
//	首行 ( session_meta ) 指名它的 cwd。它不保持文件打开、pid 也不落在日志外的
//	任何地方，于是日志这样找：该终端的 codex 启动以来写过的、cwd 相符的那个；
//	有两个时取会话开始最接近启动的那个 ( resume 的会话开始得更早，此时取最新
//	文件 )。每个 codex 进程找一次。
//
//	条目："item_completed" 事件，Codex 自己展示的那样：you 是 UserMessage；ai 是
//	AgentMessage ( phase "commentary" 干活中、"final_answer" 结尾标 end；异步说的
//	那句也是终答，但 turn 还会继续 )，异步的提问是一行工具；tool 是
//	CommandExecution / FileChange 及其余非 reasoning 条目；meta 只喂 footer
//	( turn_context 的模型、token_count 的上下文与限额 )。
package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/asks"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/state"
)

// App / Short / Processes：reader 契约。
const (
	eventMsg  = "event_msg"
	idKey     = "id"
	allowWord = "allow"
	denyWord  = "deny"
	App       = "codex"
	Short     = "codex"
)

// Processes 是它的进程名。
var Processes = []string{"codex"}

// Reader 读 Codex 的 rollout 日志；依赖注入。
type Reader struct {
	HC    core.HC
	Files core.HomeFiles
	Asks  *asks.Asks
	Shell func(ctx context.Context, session, machine, script string) (string, error)

	mu        sync.Mutex
	found     map[string]string // session|address|pid -> 日志 ( 相对 home )
	laterDone map[string]bool
	laterPart map[string]map[int]string
}

// New 装配一个能直接用的 reader：文件通路与 shell 走 hc，asks 与引擎共享
// 同一个实例；提问状态机用的几张表在这里建好 ( 空表上写会炸 )。
func New(hc core.HC, shared *asks.Asks) *Reader {
	return &Reader{
		HC: hc, Files: hc, Asks: shared, Shell: hc.HomeShell,
		found:     map[string]string{},
		laterDone: map[string]bool{},
		laterPart: map[string]map[int]string{},
	}
}

func textOf(content any) string {
	blocks, ok := content.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, c := range blocks {
		m, _ := c.(map[string]any)
		if m == nil {
			continue
		}
		if t, _ := m["type"].(string); strings.EqualFold(t, "text") {
			if s, _ := m["text"].(string); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func short(s string, maxLen int) string {
	one := strings.Join(strings.Fields(s), " ")
	if len(one) > maxLen {
		return one[:maxLen-3] + "..."
	}
	return one
}

// commandOf 把 argv 说成一条命令：shell 的 `-lc <命令>` 只显示命令本身。
func commandOf(item map[string]any) string {
	var argv []string
	switch list := item["command"].(type) {
	case []any:
		for _, a := range list {
			if s, ok := a.(string); ok {
				argv = append(argv, s)
			}
		}
	case string:
		argv = []string{list}
	}
	for i, a := range argv {
		if (a == "-lc" || a == "-c") && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return strings.Join(argv, " ")
}

// EntryItems 是一条 rollout 条目里的东西。
func EntryItems(entry map[string]any) []core.Item {
	etype, _ := entry["type"].(string)
	payload, _ := entry["payload"].(map[string]any)
	if payload == nil {
		payload = map[string]any{}
	}
	if etype == "turn_context" {
		if model, _ := payload["model"].(string); model != "" {
			return []core.Item{{Role: core.RoleMeta, Model: model}}
		}
		return nil
	}
	ptype, _ := payload["type"].(string)
	if etype == eventMsg && ptype == "token_count" {
		return meterOf(payload)
	}
	if etype != eventMsg || ptype != "item_completed" {
		return nil
	}
	item, _ := payload["item"].(map[string]any)
	if item == nil {
		item = map[string]any{}
	}
	switch item["type"] {
	case "UserMessage":
		if t := strings.TrimSpace(textOf(item["content"])); t != "" {
			return []core.Item{{Role: core.RoleYou, Text: t}}
		}
		return nil
	case "AgentMessage":
		var asked []string
		if questions, ok := item["questions"].([]any); ok {
			for _, q := range questions {
				if qm, ok := q.(map[string]any); ok {
					if title, _ := qm["title"].(string); title != "" {
						asked = append(asked, title)
					}
				}
			}
		}
		if len(asked) > 0 { // 异步提问：它的卡有全部问题
			return []core.Item{{Role: core.RoleTool, Text: "▸ asks you: " + short(strings.Join(asked, " · "), 120)}}
		}
		if t := strings.TrimSpace(textOf(item["content"])); t != "" {
			end := item["phase"] == "final_answer" && item["delivery"] != "async"
			return []core.Item{{Role: core.RoleAI, Text: t, End: end}}
		}
		return nil
	case "CommandExecution":
		return []core.Item{{Role: core.RoleTool, Text: "▸ exec: " + short(commandOf(item), 120)}}
	case "FileChange":
		var changes []string
		if files, ok := item["changes"].(map[string]any); ok {
			names := make([]string, 0, len(files))
			for file := range files {
				names = append(names, file)
			}
			sort.Strings(names) // map 遍历无序：排序保证稳定输出
			for _, file := range names {
				cm, _ := files[file].(map[string]any)
				kind := "change"
				if cm != nil {
					if t, _ := cm["type"].(string); t != "" {
						kind = t
					}
				}
				changes = append(changes, kind+" "+filepath.Base(file))
			}
		}
		joined := strings.Join(changes, ", ")
		if joined == "" {
			joined = "files"
		}
		return []core.Item{{Role: core.RoleTool, Text: "▸ edit: " + short(joined, 120)}}
	default:
		itype, _ := item["type"].(string)
		if itype == "" || strings.Contains(strings.ToLower(itype), "reasoning") {
			return nil
		}
		return []core.Item{{Role: core.RoleTool, Text: "▸ " + itype}}
	}
}

// meterOf 一条 token_count，
// 上下文多满 ( 上次请求的 token，占模型的窗口 ) 与回复说出的每条限额 ( 按窗口时长命名 )。
func meterOf(payload map[string]any) []core.Item {
	info, _ := payload["info"].(map[string]any)
	var context *core.ContextUse
	if info != nil {
		if usage, ok := info["last_token_usage"].(map[string]any); ok {
			used := num(usage["total_tokens"])
			if used > 0 {
				context = &core.ContextUse{Used: used}
				if w := num(info["model_context_window"]); w > 0 {
					context.Size = w
				}
			}
		}
	}
	var limits []core.Limit
	rate, _ := payload["rate_limits"].(map[string]any)
	if rate != nil {
		var windows []struct {
			minutes int64
			used    float64
		}
		for _, key := range []string{"primary", "secondary"} {
			if w, ok := rate[key].(map[string]any); ok {
				used, okU := w["used_percent"].(float64)
				minutes := num(w["window_minutes"])
				if okU && minutes > 0 {
					windows = append(windows, struct {
						minutes int64
						used    float64
					}{minutes, used})
				}
			}
		}
		sort.Slice(windows, func(i, j int) bool { return windows[i].minutes < windows[j].minutes })
		for _, w := range windows {
			limits = append(limits, core.Limit{Name: core.WindowName(w.minutes), Used: w.used})
		}
	}
	if context == nil && limits == nil {
		return nil
	}
	return []core.Item{{Role: core.RoleMeta, Context: context, Limits: limits}}
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

// Seconds 把 ps etime 的 "[[dd-]hh:]mm:ss" 说成秒。
func Seconds(etime string) int64 {
	t := strings.TrimSpace(etime)
	days := int64(0)
	if i := strings.Index(t, "-"); i >= 0 {
		days, _ = strconv.ParseInt(t[:i], 10, 64)
		t = t[i+1:]
	}
	parts := strings.Split(t, ":")
	for len(parts) < 3 {
		parts = append([]string{"0"}, parts...)
	}
	var h, m, s int64
	h, _ = strconv.ParseInt(parts[0], 10, 64)
	m, _ = strconv.ParseInt(parts[1], 10, 64)
	s, _ = strconv.ParseInt(parts[2], 10, 64)
	return days*86400 + h*3600 + m*60 + s
}

// PickLog 在列表 ( file\t"cwd":"…"\t"timestamp":"…" 每行 ) 里挑 cwd 相符、
// 会话开始最接近 started 的日志；resumed 的更早，此时取文件名最新者。
func PickLog(list, cwd string, started time.Time) string {
	var mine []struct {
		file  string
		began time.Time
	}
	for _, line := range strings.Split(list, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 3 || parts[0] == "" {
			continue
		}
		if parts[1] != `"cwd":"`+cwd+`"` {
			continue
		}
		ts := strings.TrimSuffix(strings.TrimPrefix(parts[2], `"timestamp":"`), `"`)
		began, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			began = time.Time{}
		}
		mine = append(mine, struct {
			file  string
			began time.Time
		}{parts[0], began})
	}
	if len(mine) == 0 {
		return ""
	}
	var fresh []struct {
		file  string
		began time.Time
	}
	for _, m := range mine {
		if !m.began.Before(started.Add(-5 * time.Second)) {
			fresh = append(fresh, m)
		}
	}
	pool := fresh
	if len(pool) == 0 {
		pool = mine
		sort.Slice(pool, func(i, j int) bool { return pool[j].file < pool[i].file }) // 文件名最新者
		return pool[0].file
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].began.Before(pool[j].began) })
	return pool[0].file
}

// listScript 列出近 minutes 分钟内写过的 rollout 日志与各自首行的 cwd/timestamp。
func listScript(minutes int64) string {
	return fmt.Sprintf(`find .codex/sessions -name 'rollout-*.jsonl' -mmin -%d 2>/dev/null | while read -r f; do printf '%%s\t%%s\t%%s\n' "$f" "$(head -n1 "$f" | grep -o '"cwd":"[^"]*"' | head -1)" "$(head -n1 "$f" | grep -o '"timestamp":"[^"]*"' | head -1)"; done`, minutes)
}

// logOf 找该终端的 codex 的日志 ( 每进程一次 )。
func (r *Reader) logOf(ctx context.Context, session, address string, fg map[string]any) string {
	key := fmt.Sprintf("%s|%s|%d", session, address, num(fg["pid"]))
	r.mu.Lock()
	cached, ok := r.found[key]
	r.mu.Unlock()
	if ok {
		return cached
	}
	machine := hccli.MachineOf(address)
	out, _ := r.Shell(ctx, session, machine, fmt.Sprintf("ps -o etime= -p %d", num(fg["pid"])))
	age := strings.TrimSpace(out)
	if age == "" {
		return ""
	}
	started := time.Now().Add(-time.Duration(Seconds(age)) * time.Second)
	minutes := int64(time.Since(started).Minutes()) + 2
	file := PickLog(mustShell(ctx, r.Shell, session, machine, listScript(minutes)), fgCwd(fg), started)
	if file != "" {
		r.mu.Lock()
		if r.found == nil {
			r.found = map[string]string{}
		}
		r.found[key] = file
		r.mu.Unlock()
	}
	return file
}

func fgCwd(fg map[string]any) string {
	cwd, _ := fg["cwd"].(string)
	return cwd
}

func mustShell(ctx context.Context, shell func(context.Context, string, string, string) (string, error), session, machine, script string) string {
	out, _ := shell(ctx, session, machine, script)
	return out
}

// After 给出游标之后的新内容；找不到该终端 codex 的日志时 Reading 为 nil。
// 同一终端换了新 codex ( 新进程 ) 读它自己的日志；旧日志里剩的先发。
func (r *Reader) After(ctx context.Context, session, address string, cur state.Cursor) (*core.Reading, error) {
	term, err := r.HC.GetTerminal(ctx, session, address)
	if err != nil {
		return nil, nil //nolint:nilerr // 读不到即不可读，是结论不是错误
	}
	fg, _ := term["foreground"].(map[string]any)
	if fg == nil || num(fg["pid"]) == 0 || fgCwd(fg) == "" {
		return nil, nil
	}
	file := r.logOf(ctx, session, address, fg)
	if file == "" {
		return nil, nil
	}
	machine := hccli.MachineOf(address)
	var items []core.Item
	if cur.File != "" && cur.File != file {
		items = append(items, core.LogAfter(ctx, r.Files, session, machine, cur.File, cur, EntryItems)...)
	}
	items = append(items, core.LogAfter(ctx, r.Files, session, machine, file, cur, EntryItems)...)
	return &core.Reading{Items: items, File: file}, nil
}

// Rest 给出遗言。
func (r *Reader) Rest(ctx context.Context, session, address string, cur state.Cursor) ([]core.Item, error) {
	if cur.File == "" {
		return nil, nil
	}
	return core.LogAfter(ctx, r.Files, session, hccli.MachineOf(address), cur.File, cur, EntryItems), nil
}

func (r *Reader) App() string         { return App }
func (r *Reader) Processes() []string { return Processes }

// TitleOf 从终端标题里清出房间名：Codex 的标题是 "<thread> | <folder>"，干活时
// 前头带 spinner、等你时带 "[ . ] Action Required | "；thread 直到 Codex 起名前
// 都叫 "codex"。
func TitleOf(title string) string {
	plain := regexp.MustCompile(`^[^\p{L}\p{N}\[]+`).ReplaceAllString(title, "")
	plain = regexp.MustCompile(`(?i)^\[[^\]]*\]\s*Action Required\s*\|\s*`).ReplaceAllString(plain, "")
	return strings.TrimSpace(strings.Split(plain, " | ")[0])
}

// ---------- 它的审批，走 herdrchat 的 hook ---------- // RequestWords 把请求要做的事说成卡上的话。
func RequestWords(tool string, input map[string]any) string {
	command := ""
	switch list := input["command"].(type) {
	case []any:
		command = commandOf(input)
	case string:
		command = list
	}
	desc, _ := input["description"].(string)
	switch tool {
	case "Bash":
		parts := []string{}
		if desc != "" {
			parts = append(parts, desc)
		}
		if command != "" {
			parts = append(parts, "$ "+command)
		}
		return strings.Join(parts, "\n")
	case "apply_patch":
		parts := []string{}
		if desc != "" {
			parts = append(parts, desc)
		}
		if command != "" {
			parts = append(parts, command)
		}
		return strings.Join(parts, "\n")
	}
	name := tool
	if name == "" {
		name = "a tool"
	}
	name = regexp.MustCompile(`^mcp__(.+?)__`).ReplaceAllString(name, "$1: ")
	var jsonPart string
	if len(input) > 0 {
		if raw, err := json.MarshalIndent(input, "", " "); err == nil {
			jsonPart = string(raw)
		}
	}
	parts := []string{name}
	if jsonPart != "" {
		parts = append(parts, jsonPart)
	}
	return strings.Join(parts, "\n")
}

// AskDialog 把 hook 的请求做成卡：Yes / No / Answer in the terminal，
// Codex 只在 hook 结束后才显示自己的提示，"在终端里答" 就是把决定交还给它 ( 空决定 )；
// Esc 也是 No ( Codex 自己的提示把 Esc 当 No )。
func AskDialog(ask *asks.Ask) *core.AgentDialog {
	req := map[string]any{}
	_ = json.Unmarshal(ask.Request, &req)
	input, _ := req["tool_input"].(map[string]any)
	if input == nil {
		input = map[string]any{}
	}
	toolName, _ := req["tool_name"].(string)
	return &core.AgentDialog{
		Key: ask.ID, Kind: "permission", Title: "Codex asks to",
		Text: asks.Clip(RequestWords(toolName, input), 0, 0),
		Options: []core.AgentOption{
			{N: 1, Label: "Yes"},
			{N: 2, Label: "No"},
			{N: 3, Label: "Answer in the terminal"},
		},
		Ref: map[string]any{idKey: ask.ID, "tool": toolName},
	}
}

const saidNoCodex = "The user said no, in herdrchat (their chat)."

// Decide：hook 的决定；"在终端里答" 是空决定。
func Decide(ask *asks.Ask, shown *core.AgentDialog, reply core.Reply) *asks.Decision {
	value := ""
	if reply.Esc {
		value = denyWord
	} else {
		var option *core.AgentOption
		for i, o := range shown.Options {
			if fmt.Sprint(o.N) == reply.Pick {
				option = &shown.Options[i]
				break
			}
		}
		if option != nil {
			switch option.Label {
			case "Yes":
				value = allowWord
			case "No":
				value = denyWord
			case "Answer in the terminal":
				value = "terminal"
			}
		}
	}
	switch value {
	case allowWord:
		return &asks.Decision{Decision: asks.DecisionJSON(map[string]any{"behavior": allowWord})}
	case denyWord:
		return &asks.Decision{Decision: asks.DecisionJSON(map[string]any{"behavior": denyWord, "message": saidNoCodex})}
	case "terminal":
		return &asks.Decision{Decision: ""}
	}
	return nil
}

// IsOf 一条没指名终端的请求 ( Codex 经共享后台服务 ) 是否属于该终端它的会话就是该终端日志的那个。
func IsOf(ask *asks.Ask, file string) bool {
	if file == "" {
		return false
	}
	req := map[string]any{}
	_ = json.Unmarshal(ask.Request, &req)
	if p, _ := req["transcript_path"].(string); p != "" && strings.HasSuffix(p, "/"+file) {
		return true
	}
	id, _ := req["session_id"].(string)
	return id != "" && strings.HasSuffix(filepath.Base(file), "-"+id+".jsonl")
}

// StrayAsk 认领属于该终端的流浪请求 ( reader 契约的 StrayAsker )。
func (r *Reader) StrayAsk(ctx context.Context, session, address string) (string, error) {
	term, err := r.HC.GetTerminal(ctx, session, address)
	if err != nil {
		return "", nil //nolint:nilerr // 读不到终端即无从认领
	}
	fg, _ := term["foreground"].(map[string]any)
	if fg == nil || num(fg["pid"]) == 0 || fgCwd(fg) == "" {
		return "", nil
	}
	file := r.logOf(ctx, session, address, fg)
	if file == "" {
		return "", nil
	}
	if ask := r.Asks.StrayAsk(ctx, session, hccli.MachineOf(address), App); ask != nil && IsOf(ask, file) {
		return ask.ID, nil
	}
	return "", nil
}

// AsksIdle 它有些提问不挡工作 ( 异步的 ) core 在 turn 结束时也问 dialog。
func (r *Reader) AsksIdle() bool { return true }
