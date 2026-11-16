// Package asks 实现 hook 的 asks 协议。
// ask.sh 逐字节复用 ( 正本内嵌在 internal/hooks )，
// 由 `herdrchat hooks install` 装进 Claude Code 与Codex 的设置。
//
// hook 把请求留在 agent 所在机器的 ~/.local/state/herdrchat/asks/req/<id>.json，
// 形如 {agent, terminal, at, request} ( request 是 agent 给 hook 的原话 )，
// 并在等待期间给它的终端打上 herdrchat.ask=<id> 标签。
//
// 答案写进 ans/<id>.json：hook 把它作为自己的决定打印；
// 空文件即无决定 ( agent 改在终端里问 )。
// id 是 "<秒>-<hook 的 pid>"：
//
// hook 已死的请求不再算数。
package asks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/shell"
)

const (
	// AsksDir 是 asks 协议的目录 ( 相对 home )。
	AsksDir = ".local/state/herdrchat/asks"
	// HookFile 是 hook 脚本的落点 ( 相对 home )。
	HookFile = ".local/share/herdrchat/hooks/ask.sh"
	// AskLabel 是 hook 给等待中的终端打的标签。
	AskLabel = "herdrchat.ask"

	hookCheckEvery = 60 * time.Second
	// 内存集的上限：partial/finished 各至多 200 条，最旧的先忘。
	forgetAt = 200
)

var askIDRe = regexp.MustCompile(`^(\d+)-(\d+)$`)

// AskOf 读终端标签上等待中的请求 id；形状不对返回 ""。
func AskOf(record hccli.Terminal) string {
	if record == nil {
		return ""
	}
	id := record.Label(AskLabel)
	if !askIDRe.MatchString(id) {
		return ""
	}
	return id
}

// Ask 是一条打开的请求。
type Ask struct {
	ID       string
	Agent    string
	Terminal string

	At      int64 // 请求落盘的 unix 秒 ( hook 写的是不带引号的数字 )
	Request json.RawMessage
}

// Asks 是 hook 协议在引擎侧的全部状态：
// hook 在场缓存、每请求的部分答案、已答完的请求。
// 远端机器的读写走 hc exec，本机直接文件与 sh。
type Asks struct {
	mu sync.Mutex

	hc      core.HC
	homeDir string // 本机分支的 home ( 测试注入 )

	hookSeen  map[string]hookSeenAt
	partials  map[string]map[string]string
	partOrder []string // partials 的插入序 ( forgetAt 封顶，最旧的先忘；答完的懒着删 )
	finished  map[string]bool
	finOrder  []string
}

type hookSeenAt struct {
	at    time.Time
	there bool
}

// New 建一份 asks 状态。
func New(hc core.HC, homeDir string) *Asks {
	return &Asks{
		hc:      hc,
		homeDir: homeDir,

		hookSeen: map[string]hookSeenAt{},
		partials: map[string]map[string]string{},
		finished: map[string]bool{},
	}
}

// hookThere 问 hook 在不在那台机器 ( 一分钟缓存 )：
// 不在就没有请求来，对话框立刻改为读屏。
func (a *Asks) hookThere(ctx context.Context, session, machine string) bool {
	a.mu.Lock()
	seen, ok := a.hookSeen[machine]
	a.mu.Unlock()

	if ok && time.Since(seen.at) < hookCheckEvery {
		return seen.there
	}

	out, _ := a.hc.HomeShell(ctx, session, machine, "[ -f "+shell.Quote(HookFile)+" ] && echo yes")
	there := strings.TrimSpace(out) == "yes"

	a.mu.Lock()
	a.hookSeen[machine] = hookSeenAt{
		at:    time.Now(),
		there: there,
	}
	a.mu.Unlock()

	return there
}

// PartialOf 给出一条请求到目前为止答了什么
// ( Claude 的 AskUserQuestion 一次问几题：一张卡一题，全部有答案才作决定 )。
func (a *Asks) PartialOf(id string) map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.partials[id]
}

func (a *Asks) isFinished(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.finished[id]
}

// OpenAsk 是 agent 的终端正在等的请求；nil 没等；
// false ( known=false ) 表示 hook 不在那台机器上。
func (a *Asks) OpenAsk(ctx context.Context, session, address, agent string) (ask *Ask, known bool) {
	machine := hccli.MachineOf(address)
	term, err := a.hc.GetTerminal(ctx, session, address)
	if err != nil {
		term = nil
	}

	id := AskOf(term)
	if id == "" || a.isFinished(id) {
		return nil, a.hookThere(ctx, session, machine)
	}

	pid := askIDRe.FindStringSubmatch(id)[2]
	out, _ := a.hc.HomeShell(ctx, session, machine, "kill -0 "+pid+" 2>/dev/null && cat "+shell.Quote(AsksDir+"/req/"+id+".json"))

	var parsed Ask
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, true
	}
	if parsed.Agent != agent || len(parsed.Request) == 0 {
		return nil, true
	}

	parsed.ID = id
	return &parsed, true
}

// AskOpen 判断请求 id 的 hook 是否仍在等：请求文件还在、pid 还活着。
func (a *Asks) AskOpen(ctx context.Context, session, machine, id string) bool {
	if !askIDRe.MatchString(id) {
		return false
	}
	pid := askIDRe.FindStringSubmatch(id)[2]
	out, _ := a.hc.HomeShell(
		ctx,
		session,
		machine,
		"[ -f "+shell.Quote(AsksDir+"/req/"+id+".json")+" ] && kill -0 "+pid+" 2>/dev/null && echo open",
	)
	return strings.TrimSpace(out) == "open"
}

// AsksHere 判断本机是否有请求在等：看一眼目录，不起 shell 可以每几秒问一次。
func (a *Asks) AsksHere() bool {
	entries, err := os.ReadDir(filepath.Join(a.homeDir, AsksDir, "req"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			return true
		}
	}
	return false
}

// StrayAsk 是机器上没指名终端的、属于 agent 的请求
// ( Codex 经共享后台服务把hook 跑在终端之外时如此 )；
// nil 没有匹配的。
func (a *Asks) StrayAsk(ctx context.Context, session, machine, agent string) *Ask {
	script := "cd " + shell.Quote(AsksDir+"/req") + " 2>/dev/null && for f in *.json; do [ -f \"$f\" ] || continue; id=\"${f%.json}\"; kill -0 \"${id#*-}\" 2>/dev/null || continue; printf '%s\\t' \"$id\"; tr -d '\\n' < \"$f\"; echo; done"
	out, _ := a.hc.HomeShell(ctx, session, machine, script)
	for line := range strings.SplitSeq(out, "\n") {
		before, after, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}

		id := before
		if !askIDRe.MatchString(id) || a.isFinished(id) {
			continue
		}

		var parsed Ask
		if err := json.Unmarshal([]byte(after), &parsed); err != nil {
			// 写了一半：下一眼再看。
			continue
		}

		if parsed.Agent == agent && parsed.Terminal == "" && len(parsed.Request) != 0 {
			parsed.ID = id
			return &parsed
		}
	}
	return nil
}

// write 把请求 id 的决定落盘 ( hook 会打印它 )；空串即无决定。
func (a *Asks) write(ctx context.Context, session, machine, id, decision string) bool {
	if !askIDRe.MatchString(id) {
		return false
	}

	req := AsksDir + "/req/" + id + ".json"
	ans := AsksDir + "/ans/" + id + ".json"
	script := "[ -f " + shell.Quote(req) + " ] && printf '%s' " + shell.Quote(decision) +
		" > " + shell.Quote(ans+".tmp") + " && mv " + shell.Quote(ans+".tmp") + " " + shell.Quote(ans) + " && echo written"
	out, _ := a.hc.HomeShell(ctx, session, machine, script)
	return strings.TrimSpace(out) == "written"
}

// Decide 给出一条请求对一次点击的决定：
// {Decision} ( hook 的输出，空串为无 )、
// {Partial} ( 多题之一已答：记下，先不发 ) 或 nil ( 不是它的答案 )。
type Decide func(ask *Ask, shown core.Dialog, reply core.Reply, partial map[string]string) *Decision

// Decision 是 Decide 的产出。
type Decision struct {
	Decision string
	Partial  map[string]string
}

// AnswerAsk 回答 shown ( 由请求 shown.Ref 做出的卡 ) 的问题。
// true 表示送出了 ( 或记下了部分答案 )。
func (a *Asks) AnswerAsk(ctx context.Context, session, address, agent string, shown *core.Dialog, reply core.Reply, decide Decide) bool {
	ask, known := a.OpenAsk(ctx, session, address, agent)
	if !known || ask == nil {
		return false
	}

	ref, _ := shown.Menu.Own.(*Ref)
	if ref == nil || ask.ID != ref.ID {
		return false
	}

	out := decide(ask, *shown, reply, a.PartialOf(ask.ID))
	if out == nil {
		return false
	}
	if out.Partial != nil {
		a.mu.Lock()
		if _, seen := a.partials[ask.ID]; !seen {
			a.partOrder = append(a.partOrder, ask.ID)
			if len(a.partOrder) > forgetAt {
				delete(a.partials, a.partOrder[0])
				a.partOrder = a.partOrder[1:]
			}
		}
		a.partials[ask.ID] = out.Partial
		a.mu.Unlock()
		return true
	}
	if !a.write(ctx, session, hccli.MachineOf(address), ask.ID, out.Decision) {
		return false
	}

	a.mu.Lock()
	delete(a.partials, ask.ID)
	a.finished[ask.ID] = true
	a.finOrder = append(a.finOrder, ask.ID)
	if len(a.finOrder) > forgetAt {
		delete(a.finished, a.finOrder[0])
		a.finOrder = a.finOrder[1:]
	}
	a.mu.Unlock()
	return true
}

// Ref 是 agentDialog 放进 Menu.Own 的引用：回答沿 hook 原路回去。
type Ref struct {
	ID  string
	Raw *core.AgentDialog
}

// DecisionJSON 是 hook 输出的决定格式：
// allow ( {updatedInput, updatedPermissions} )
// 或
// deny ( {message, interrupt} )，
//
// Claude Code 与 Codex 的 PermissionRequest 都这么收。
func DecisionJSON(decision map[string]any) string {
	out, err := json.Marshal(
		map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName": "PermissionRequest",
				"decision":      decision,
			},
		})
	if err != nil {
		return ""
	}
	return string(out)
}

// Clip 把一段话裁到 max 字符 lines 行内说给卡片听，裁过就说明还有下文。
func Clip(text string, maxChars int, lines int) string {
	const defaultMax, defaultLines = 1500, 30
	if maxChars <= 0 {
		maxChars = defaultMax
	}
	if lines <= 0 {
		lines = defaultLines
	}
	text = strings.ReplaceAll(text, "\r", "")
	all := strings.Split(text, "\n")
	out := strings.Join(all[:min(lines, len(all))], "\n")
	cut := len(all) > lines
	if len(out) > maxChars {
		// 退到 rune 边界再切：别把一个多字节字符切成两半，那在卡片上是乱码。
		for maxChars > 0 && !utf8.RuneStart(out[maxChars]) {
			maxChars--
		}
		out = out[:maxChars]
		cut = true
	}
	if cut {
		return strings.TrimRight(out, " \t\n") + "\n… (the rest is in the terminal)"
	}
	return out
}
