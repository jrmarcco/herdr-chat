// Package opencode 是 OpenCode 的 reader ( hc status app "opencode" )，
// 走它自己的API：
// CLI `opencode api <operation>` 问的是 OpenCode 的后台服务，它自己登录。
// 这里不读它的数据库、不读它的屏——OpenCode 升级只要 API 还在，这就还能用。
//
//	哪个会话：
//	该终端目录下的会话 ( session.list )，活跃的 ( session.active ) 优先，否则更新最近的。
//
//	条目 ( session.message.list )：
//	you 是 user 消息；
//	ai 是 assistant 消息的 text 部件 ( 后面跟着一条 "idle" 消息的那条标 end，turn 结束；reasoning 不取 )；
//	tool 是 assistant 的 tool 部件 ( shell、write、edit、question… ) 一行。
//
//	它的对话框：
//	一个 form ( session.form.list：各问题带选项、也许自己的话 )
//	或
//	一条权限请求 ( session.permission.list：once / always / reject )。
//	一次一题，每题都有答案后整个 form 送出。Esc 取消 form、或拒绝请求。
package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/state"
)

// App / Processes：reader 契约。
const (
	paramSession = "sessionID"
	App          = "opencode"
	Short        = "opencode"
)

// Processes 是它的进程名。
var Processes = []string{"opencode"}

// Reader 走 OpenCode 的 API；依赖注入。
type Reader struct {
	HC    core.HC
	Shell func(ctx context.Context, session, machine, script string) (string, error)
	Cache *core.Meter
	// Fetch 给 GLM 限额用 ( 测试注入 )。
	Fetch func(ctx context.Context, url string, headers map[string]string) (int, []byte, error)

	mu      sync.Mutex
	partial map[string]map[string]any // form id -> {field: value}
}

// New 装配一个能直接用的 reader：shell 走 hc，meter 缓存自带，
// GLM 限额用真实的 HTTP。
func New(hc core.HC) *Reader {
	return &Reader{
		HC: hc, Shell: hc.HomeShell, Cache: core.NewMeter(), Fetch: httpFetch,
		partial: map[string]map[string]any{},
	}
}

// fetchClient 给限额探测用；走环境里的代理设置。
var fetchClient = &http.Client{Timeout: 15 * time.Second}

// httpFetch 是 Fetch 的默认实现。
func httpFetch(ctx context.Context, url string, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := fetchClient.Do(req)
	if err != nil {
		return 0, nil, err
	}

	//nolint:errcheck // 只读一次的探测响应。
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func short(s string, maxLen int) string {
	one := strings.Join(strings.Fields(s), " ")
	if len(one) > maxLen {
		return one[:maxLen-3] + "..."
	}
	return one
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

const exitMark = "__herdrchat_exit="

// api 调一次 OpenCode 的 API ( 在 cwd 下跑，API 按那个目录的项目作答 )。
// whole=true 时给整个回复，否则给其 data 字段；调用失败返回 ok=false。
func (r *Reader) api(ctx context.Context, session, machine, cwd, operation string, params map[string]string, body any, whole bool) (any, bool) {
	args := operation
	for k, v := range params {
		args += fmt.Sprintf(" --param %s", shellQuote(k+"="+v))
	}
	if body != nil {
		raw, err := json.Marshal(body)
		if err == nil {
			args += " -d " + shellQuote(string(raw))
		}
	}

	out, err := r.Shell(ctx, session, machine,
		fmt.Sprintf("cd %s && opencode api %s 2>/dev/null; echo \"%s$?\"", shellQuote(cwd), args, exitMark))
	if err != nil {
		return nil, false
	}
	at := strings.LastIndex(out, exitMark)
	if at < 0 || strings.TrimSpace(out[at+len(exitMark):]) != "0" {
		return nil, false
	}
	text := strings.TrimSpace(out[:at])
	if text == "" {
		return map[string]any{}, true
	}

	var parsed any
	if json.Unmarshal([]byte(text), &parsed) != nil {
		return map[string]any{}, true
	}
	if !whole {
		if m, ok := parsed.(map[string]any); ok {
			if data, has := m["data"]; has {
				return data, true
			}
		}
	}
	return parsed, true
}

func toolLine(part map[string]any) string {
	input, _ := part["input"].(map[string]any)
	if state, ok := part["state"].(map[string]any); ok {
		if in, ok := state["input"].(map[string]any); ok {
			input = in
		}
	}
	if input == nil {
		input = map[string]any{}
	}
	name, _ := part["name"].(string)
	if name == "question" {
		if questions, ok := input["questions"].([]any); ok && len(questions) > 0 {
			if q, ok := questions[0].(map[string]any); ok {
				ask, _ := q["question"].(string)
				if ask == "" {
					ask, _ = q["title"].(string)
				}
				return "▸ question: " + short(ask, 120)
			}
		}
		return "▸ question:"
	}
	what := ""
	for _, key := range []string{"command", "filePath", "path", "pattern", "url", "description"} {
		if v, _ := input[key].(string); v != "" {
			what = v
			break
		}
	}
	if what == "" {
		return "▸ " + name
	}
	return "▸ " + name + ": " + short(what, 120)
}

// messageItems 一条消息的条目；ends：它后面跟着一条 "idle" 消息。
func messageItems(m map[string]any, ends bool) []core.Item {
	mtype, _ := m["type"].(string)
	if mtype == "user" {
		if t := strings.TrimSpace(str(m["text"])); t != "" {
			return []core.Item{{Role: core.RoleYou, Text: t}}
		}
		return nil
	}
	if mtype != "assistant" {
		return nil
	}
	model, _ := m["model"].(map[string]any)
	modelID, _ := model["id"].(string)
	provider, _ := model["providerID"].(string)
	var context *core.ContextUse
	if tokens, ok := m["tokens"].(map[string]any); ok {
		used := num(tokens["input"])
		if cache, ok := tokens["cache"].(map[string]any); ok {
			used += num(cache["read"]) + num(cache["write"])
		}
		if used > 0 {
			context = &core.ContextUse{Used: used}
		}
	}

	var out []core.Item
	if content, ok := m["content"].([]any); ok {
		for _, p := range content {
			part, ok := p.(map[string]any)
			if !ok {
				continue
			}
			switch part["type"] {
			case "text":
				if t := strings.TrimSpace(str(part["text"])); t != "" {
					out = append(out, core.Item{Role: core.RoleAI, Text: t, Model: modelID, Provider: provider, Context: context})
				}
			case "tool":
				out = append(out, core.Item{Role: core.RoleTool, Text: toolLine(part), Model: modelID, Provider: provider, Context: context})
			}
		}
	}

	// 绝不产出只喂 footer 的条目：OpenCode 原地改写一条消息 ( 先 reasoning 后 text，同一个 id )，
	// 早读出的条目会把 id 标成已发、它的 text 就再读不到。
	if ends {
		for i := len(out) - 1; i >= 0; i-- {
			if out[i].Role == core.RoleAI {
				out[i].End = true
				break
			}
		}
	}
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// LinesOf 把一个会话的消息排成游标读的日志行 ({timestamp, uuid, message, ends})。
func LinesOf(messages []any) []string {
	var lines []string
	for i, m := range messages {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		ends := false
		for j := i + 1; j < len(messages); j++ {
			nm, _ := messages[j].(map[string]any)
			if nm == nil {
				continue
			}
			if nm["type"] != "assistant" || hasText(nm) {
				ends = nm["type"] == "idle"
				break
			}
		}
		raw, err := json.Marshal(map[string]any{
			"timestamp": time.UnixMilli(createdOf(mm)).UTC().Format("2006-01-02T15:04:05.000Z"),
			"uuid":      mm["id"],
			"message":   mm,
			"ends":      ends,
		})
		if err == nil {
			lines = append(lines, string(raw))
		}
	}
	return lines
}

func hasText(m map[string]any) bool {
	content, ok := m["content"].([]any)
	if !ok {
		return false
	}
	for _, p := range content {
		if pm, ok := p.(map[string]any); ok && pm["type"] == "text" {
			return true
		}
	}
	return false
}

func createdOf(m map[string]any) int64 {
	timeObj, _ := m["time"].(map[string]any)
	if timeObj == nil {
		return 0
	}
	return num(timeObj["created"])
}

func entryItems(entry map[string]any) []core.Item {
	message, _ := entry["message"].(map[string]any)
	if message == nil {
		return nil
	}
	return messageItems(message, truthy(entry["ends"]))
}

func truthy(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// conversationAfter 按游标读行。
func conversationAfter(lines []string, cur state.Cursor) []core.Item {
	items, _ := core.ItemsAfter(lines, cur, entryItems)
	return items
}

// PickSession 挑该终端 OpenCode 的会话：
// 其目录的会话中，活跃的优先，否则更新最近的。
func PickSession(list any, active any, cwd string) string {
	sessions, _ := list.([]any)
	var mine []map[string]any
	for _, s := range sessions {
		sm, ok := s.(map[string]any)
		if !ok || sm["id"] == nil {
			continue
		}
		if dir, _ := sm["directory"].(string); dir != "" && dir != cwd {
			continue
		}
		mine = append(mine, sm)
	}
	var pool []map[string]any
	if activeMap, ok := active.(map[string]any); ok {
		for _, s := range mine {
			id, _ := s["id"].(string)
			if truthy(activeMap[id]) {
				pool = append(pool, s)
			}
		}
	}
	if len(pool) == 0 {
		pool = mine
	}
	sort.SliceStable(pool, func(i, j int) bool { return num(pool[i]["updated"]) > num(pool[j]["updated"]) })
	if len(pool) == 0 {
		return ""
	}
	id, _ := pool[0]["id"].(string)
	return id
}

// where 找该终端的 OpenCode 会话。
func (r *Reader) where(ctx context.Context, session, address string) (machine, cwd, id string, ok bool) {
	term, err := r.HC.GetTerminal(ctx, session, address)
	if err != nil {
		return "", "", "", false
	}
	fg, _ := term["foreground"].(map[string]any)
	if fg == nil {
		return "", "", "", false
	}
	cwd, _ = fg["cwd"].(string)
	if cwd == "" {
		return "", "", "", false
	}
	machine = hccli.MachineOf(address)
	list, _ := r.api(ctx, session, machine, cwd, "session.list", nil, nil, false)
	active, _ := r.api(ctx, session, machine, cwd, "session.active", nil, nil, false)
	id = PickSession(list, active, cwd)
	return machine, cwd, id, id != ""
}

const (
	pageSize  = 100
	pageLimit = 50
)

// messagesOf 拿一个会话的消息，旧在前，直到 since ( 更早即整个会话 )；
// API 一页一页给的是新在前。
func (r *Reader) messagesOf(ctx context.Context, session, machine, cwd, id, since string) []any {
	var all []any
	cursor := ""
	for i := 0; i < pageLimit; i++ {
		params := map[string]string{paramSession: id, "limit": fmt.Sprint(pageSize)}
		if cursor != "" {
			params["cursor"] = cursor
		} else {
			params["order"] = "desc"
		}
		page, ok := r.api(ctx, session, machine, cwd, "session.message.list", params, nil, true)
		if !ok {
			if len(all) > 0 {
				return reverseAll(all)
			}
			return nil
		}
		pageMap, _ := page.(map[string]any)
		if pageMap == nil {
			return reverseAll(all)
		}
		data, _ := pageMap["data"].([]any)
		all = append(all, data...)
		next, _ := pageMap["cursor"].(map[string]any)
		cursor, _ = next["next"].(string)
		if len(data) < pageSize || cursor == "" {
			break
		}
		if oldest := lastCreated(data); oldest != "" && oldest <= since {
			break
		}
	}
	return reverseAll(all)
}

func lastCreated(data []any) string {
	for i := len(data) - 1; i >= 0; i-- {
		if m, ok := data[i].(map[string]any); ok {
			created := createdOf(m)
			if created > 0 {
				return time.UnixMilli(created).UTC().Format("2006-01-02T15:04:05.000Z")
			}
		}
	}
	return ""
}

func reverseAll(all []any) []any {
	out := make([]any, len(all))
	for i, v := range all {
		out[len(all)-1-i] = v
	}
	return out
}

// After 给出游标之后的新内容；file 是会话 id。
func (r *Reader) After(ctx context.Context, session, address string, cur state.Cursor) (*core.Reading, error) {
	machine, cwd, id, ok := r.where(ctx, session, address)
	if !ok {
		return nil, nil
	}
	var items []core.Item
	if cur.File != "" && cur.File != id {
		if old := r.messagesOf(ctx, session, machine, cwd, cur.File, cur.At); old != nil {
			items = append(items, conversationAfter(LinesOf(old), cur)...)
		}
	}
	now := r.messagesOf(ctx, session, machine, cwd, id, cur.At)
	if now == nil {
		return nil, nil
	}
	items = append(items, conversationAfter(LinesOf(now), cur)...)
	return &core.Reading{Items: items, File: id}, nil
}

// Rest 给出遗言。
func (r *Reader) Rest(ctx context.Context, session, address string, cur state.Cursor) ([]core.Item, error) {
	if cur.File == "" {
		return nil, nil
	}
	machine := hccli.MachineOf(address)
	cwd := "."
	if term, err := r.HC.GetTerminal(ctx, session, address); err == nil {
		if fg, _ := term["foreground"].(map[string]any); fg != nil {
			if c, _ := fg["cwd"].(string); c != "" {
				cwd = c
			}
		}
	}
	old := r.messagesOf(ctx, session, machine, cwd, cur.File, cur.At)
	if old == nil {
		return nil, nil
	}
	return conversationAfter(LinesOf(old), cur), nil
}

func (r *Reader) App() string         { return App }
func (r *Reader) Processes() []string { return Processes }

// TitleOf 从终端标题 "OC | <会话名>" 里清出会话名。
func TitleOf(title string) string {
	return strings.TrimSpace(regexp.MustCompile(`^OC\s*\|\s*`).ReplaceAllString(title, ""))
}

// ---------- footer 的窗口与 GLM Coding Plan 的限额 ----------

// WindowOf 从 OpenCode 的 model.list 里找模型的上下文窗口 ( limit.context )。
func WindowOf(models any, model, provider string) int64 {
	list, _ := models.([]any)
	var hit map[string]any
	for _, m := range list {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		id, _ := mm["modelID"].(string)
		if id == "" {
			id, _ = mm["id"].(string)
		}
		if id != model {
			continue
		}
		p, _ := mm["providerID"].(string)
		if provider != "" && p == provider {
			hit = mm
			break
		}
		if hit == nil {
			hit = mm
		}
	}
	if hit == nil {
		return 0
	}
	limit, _ := hit["limit"].(map[string]any)
	if limit == nil {
		return 0
	}
	return num(limit["context"])
}

// ZhipuHost 给 provider 的用量接口主机 ( GLM Coding Plan / Z.ai )。
func ZhipuHost(provider string) string {
	if regexp.MustCompile(`^zai(-|$)`).MatchString(provider) {
		return "api.z.ai"
	}
	if regexp.MustCompile(`^zhipuai(-|$)`).MatchString(provider) {
		return "open.bigmodel.cn"
	}
	return ""
}

// KeyOf 从 credential.list 里找该 provider 的活跃 key。
func KeyOf(credentials any, provider string) string {
	list, _ := credentials.([]any)
	for _, c := range list {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if integration, _ := cm["integrationID"].(string); integration != provider {
			continue
		}
		if active, has := cm["active"]; has && !truthy(active) {
			continue
		}
		value, _ := cm["value"].(map[string]any)
		if value == nil || value["type"] != "key" {
			continue
		}
		if key, _ := value["key"].(string); key != "" {
			return key
		}
	}
	return ""
}

// ZhipuLimit 是一条限额 ( hours 只用于排序 )。
type ZhipuLimit struct {
	Name  string
	Used  float64
	Hours float64
}

// ZhipuLimitsIn 从智谱的用量回复读 TOKENS_LIMIT：
// unit 3 是小时、6 是周 ( number 见过 1 与 7 )；
// TIME_LIMIT 是网页工具的月限，不挡模型，不取。
func ZhipuLimitsIn(reply []byte) []core.Limit {
	var parsed struct {
		Success bool `json:"success"`
		Data    struct {
			Limits []struct {
				Type       string  `json:"type"`
				Unit       int64   `json:"unit"`
				Number     float64 `json:"number"`
				Percentage float64 `json:"percentage"`
			} `json:"limits"`
		} `json:"data"`
	}
	if json.Unmarshal(reply, &parsed) != nil || !parsed.Success {
		return nil
	}
	var out []ZhipuLimit
	for _, l := range parsed.Data.Limits {
		if l.Type != "TOKENS_LIMIT" {
			continue
		}
		var name string
		var hours float64
		switch {
		case l.Unit == 3 && l.Number > 0:
			name = fmt.Sprintf("%s-hour", trimFloat(l.Number))
			hours = l.Number
		case l.Unit == 6:
			name = "7-day"
			hours = 168
		default:
			continue
		}
		out = append(out, ZhipuLimit{Name: name, Used: l.Percentage, Hours: hours})
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hours < out[j].Hours })
	limits := make([]core.Limit, len(out))
	for i, l := range out {
		limits[i] = core.Limit{Name: l.Name, Used: l.Used}
	}
	return limits
}

func trimFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprint(int64(f))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", f), "0"), ".")
}

// zhipuLimits 问智谱自己的用量插件问的那个接口。
// key 只在这一次请求里持有，绝不落盘、绝不入日志，也只发给它本来就在用的主机。
// 任何一步不如预期都不给限额——footer 略去，绝不编 0%。
func (r *Reader) zhipuLimits(ctx context.Context, session, machine, cwd, provider string) []core.Limit {
	host := ZhipuHost(provider)
	if host == "" {
		return nil
	}
	credentials, ok := r.api(ctx, session, machine, cwd, "credential.list", nil, nil, false)
	if !ok {
		return nil
	}
	key := KeyOf(credentials, provider)
	if key == "" {
		return nil
	}
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	status, body, err := r.Fetch(reqCtx, "https://"+host+"/api/monitor/usage/quota/limit",
		map[string]string{"Authorization": key, "Accept-Language": "en-US,en"})
	if err != nil || status != http.StatusOK {
		return nil
	}
	return ZhipuLimitsIn(body)
}

// meter 的缓存时长与首等上限。
const (
	windowTTL = 24 * time.Hour
	limitsTTL = 5 * time.Minute
	firstWait = 10 * time.Second
)

// Meter 给 footer 拿窗口与 ( 智谱的 ) 限额。
func (r *Reader) Meter(ctx context.Context, session, address string, meta map[string]any) (core.MeterInfo, error) {
	model, _ := meta["model"].(string)
	if model == "" {
		return core.MeterInfo{}, nil
	}
	provider, _ := meta["provider"].(string)
	machine := hccli.MachineOf(address)
	term, err := r.HC.GetTerminal(ctx, session, address)
	if err != nil {
		//nolint:nilerr // 读不到终端就没有 footer 数据。
		return core.MeterInfo{}, nil
	}
	fg, _ := term["foreground"].(map[string]any)
	if fg == nil {
		return core.MeterInfo{}, nil
	}
	cwd, _ := fg["cwd"].(string)
	if cwd == "" {
		return core.MeterInfo{}, nil
	}
	info := core.MeterInfo{}
	v := r.Cache.KnownSoon("opencode|"+machine+"|window|"+provider+"/"+model, windowTTL, func() (any, error) {
		models, ok := r.api(ctx, session, machine, cwd, "model.list", nil, nil, false)
		if !ok {
			return nil, nil
		}
		if w := WindowOf(models, model, provider); w > 0 {
			return w, nil
		}
		return nil, nil
	}, firstWait)
	if w, ok := v.(int64); ok {
		info.ContextSize = w
	}
	if ZhipuHost(provider) != "" {
		v := r.Cache.KnownSoon("opencode|"+machine+"|limits|"+provider, limitsTTL, func() (any, error) {
			if limits := r.zhipuLimits(ctx, session, machine, cwd, provider); limits != nil {
				return limits, nil
			}
			return nil, nil
		}, firstWait)
		if limits, ok := v.([]core.Limit); ok {
			info.Limits = limits
		}
	}
	return info, nil
}

var _ io.Reader = io.Reader(nil)
