package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jrmarcco/herdr-chat/internal/asks"
	"github.com/jrmarcco/herdr-chat/internal/core"
)

const (
	behaviorKey = "behavior"
	allowWord   = "allow"
	denyWord    = "deny"
	optNoLabel  = "No"
)

// 它的对话框，走 herdrchat 的 hook ( asks 包 )：
// hook 运行期间 Claude 问你的一次工具授权 ( Yes；Claude 给了规则时还有 "不再问"；No )、
// 它的提问 ( AskUserQuestion：一题一卡、多选框、自己的话，答案合在一起回去 )、它的计划 ( ExitPlanMode )。
// Esc 会打断 Claude，和在它自己的提示里按 Esc 一样。没有 hook ( 或 hook 没看见的对话框 ) 照旧读屏。

// homeRel 把绝对路径说成人话：工作目录内的相对、其余 ~/。
func homeRel(file, cwd string) string {
	f := strings.TrimSpace(file)
	if cwd != "" && strings.HasPrefix(f, cwd+"/") {
		return f[len(cwd)+1:]
	}
	return regexp.MustCompile(`^/(Users|home)/[^/]+/`).ReplaceAllString(f, "~/")
}

// diffOf 把一次修改排成 diff 的样子。
func diffOf(from, to string) string {
	var lines []string
	for _, l := range strings.Split(from, "\n") {
		lines = append(lines, "- "+l)
	}
	for _, l := range strings.Split(to, "\n") {
		lines = append(lines, "+ "+l)
	}
	return strings.Join(lines, "\n")
}

// ToolWords 把一个工具要做的事说成卡上的话。
func ToolWords(tool string, input map[string]any, cwd string) string {
	str := func(key string) string {
		v, _ := input[key].(string)
		return v
	}
	switch tool {
	case "Bash":
		parts := []string{}
		if d := str("description"); d != "" {
			parts = append(parts, d)
		}
		if c := str("command"); c != "" {
			parts = append(parts, "$ "+c)
		}
		return strings.Join(parts, "\n")
	case "Write":
		return "Write " + homeRel(str("file_path"), cwd) + "\n" + str("content")
	case "Edit":
		return "Edit " + homeRel(str("file_path"), cwd) + "\n" + diffOf(str("old_string"), str("new_string"))
	case "MultiEdit":
		var diffs []string
		if edits, ok := input["edits"].([]any); ok {
			for _, e := range edits {
				em, _ := e.(map[string]any)
				if em == nil {
					continue
				}
				o, _ := em["old_string"].(string)
				n, _ := em["new_string"].(string)
				diffs = append(diffs, diffOf(o, n))
			}
		}
		return "Edit " + homeRel(str("file_path"), cwd) + "\n" + strings.Join(diffs, "\n")
	case "NotebookEdit":
		return "Edit " + homeRel(str("notebook_path"), cwd) + "\n" + str("new_source")
	case "Read":
		return "Read " + homeRel(str("file_path"), cwd)
	case "Glob", "Grep":
		out := "Search for " + str("pattern")
		if p := str("path"); p != "" {
			out += " in " + homeRel(p, cwd)
		}
		return out
	case "WebFetch":
		out := "Fetch " + str("url")
		if p := str("prompt"); p != "" {
			out += "\n" + p
		}
		return out
	case "WebSearch":
		return "Search the web: " + str("query")
	default:
		mcp := regexp.MustCompile(`^mcp__(.+?)__(.+)$`).FindStringSubmatch(tool)
		name := "Use " + tool
		if tool == "" {
			name = "Use a tool"
		}
		if mcp != nil {
			name = mcp[1] + ": " + mcp[2]
		}
		var jsonPart string
		if len(input) > 0 {
			raw, err := json.MarshalIndent(input, "", " ")
			if err == nil {
				jsonPart = string(raw)
			}
		}
		parts := []string{name}
		if jsonPart != "" {
			parts = append(parts, jsonPart)
		}
		return strings.Join(parts, "\n")
	}
}

func optionLabel(o any) string {
	switch v := o.(type) {
	case string:
		return v
	case map[string]any:
		l, _ := v["label"].(string)
		return l
	}
	return ""
}

// AskDialog 把 hook 的请求做成 agent 上报的提问：
// 现在问的是哪一题 ( partial：已答过的跳过 )；没有可问的返回 nil。
func AskDialog(ask *asks.Ask, partial map[string]string) *core.AgentDialog {
	req := map[string]any{}
	_ = json.Unmarshal(ask.Request, &req)
	input, _ := req["tool_input"].(map[string]any)
	if input == nil {
		input = map[string]any{}
	}
	toolName, _ := req["tool_name"].(string)
	ref := map[string]any{"id": ask.ID, "tool": toolName}

	if toolName == "AskUserQuestion" {
		var questions []map[string]any
		if raw, ok := req["tool_input"].(map[string]any); ok {
			if list, ok := raw["questions"].([]any); ok {
				for _, q := range list {
					if qm, ok := q.(map[string]any); ok && qm["question"] != nil {
						questions = append(questions, qm)
					}
				}
			}
		}
		idx := -1
		for i, q := range questions {
			question, _ := q["question"].(string)
			if _, done := partial[question]; !done {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil
		}
		q := questions[idx]
		multi := q["multiSelect"] == true
		dialog := &core.AgentDialog{
			Key: ask.ID + "#" + fmt.Sprint(idx), Kind: "question", Title: "Claude asks",
			Multi: multi, Ref: ref,
		}
		var about []string
		if options, ok := q["options"].([]any); ok {
			for _, o := range options {
				l := optionLabel(o)
				if l == "" {
					continue
				}
				checked := false
				opt := core.AgentOption{N: len(dialog.Options) + 1, Label: l}
				if multi {
					opt.Checked = &checked
				}
				dialog.Options = append(dialog.Options, opt)
				if om, ok := o.(map[string]any); ok {
					if d, _ := om["description"].(string); d != "" {
						about = append(about, l+": "+d)
					}
				}
			}
		}
		dialog.Text = strings.Join(about, "\n")
		free := core.AgentOption{N: len(dialog.Options) + 1, Label: "Type your own answer"}
		dialog.Options = append(dialog.Options, free)
		dialog.Free = free.N
		header, _ := q["header"].(string)
		question, _ := q["question"].(string)
		var text []string
		for _, part := range []string{header, question, strings.TrimSpace(dialog.Text)} {
			if part != "" {
				text = append(text, part)
			}
		}
		if len(questions) > 1 {
			text = append(text, fmt.Sprintf("(question %d of %d)", idx+1, len(questions)))
		}
		dialog.Text = strings.Join(text, "\n")
		ref["question"] = question
		return dialog
	}
	if toolName == "ExitPlanMode" {
		dialog := &core.AgentDialog{Key: ask.ID, Kind: "plan", Title: "Claude has a plan", Ref: ref}
		plan, _ := input["plan"].(string)
		dialog.Text = asks.Clip(plan, 3000, 60)
		dialog.Options = []core.AgentOption{
			{N: 1, Label: "Yes, start"},
			{N: 2, Label: "Yes, and accept its edits"},
			{N: 3, Label: "No, keep planning"},
		}
		return dialog
	}
	// 一次工具授权。
	dialog := &core.AgentDialog{Key: ask.ID, Kind: "permission", Title: "Claude asks to", Ref: ref}
	cwd, _ := req["cwd"].(string)
	dialog.Text = asks.Clip(ToolWords(toolName, input, cwd), 0, 0)
	dialog.Options = []core.AgentOption{{N: 1, Label: "Yes"}}
	if suggestions, ok := req["permission_suggestions"].([]any); ok && len(suggestions) > 0 {
		dialog.Options = append(dialog.Options, core.AgentOption{N: 2, Label: "Yes, and don't ask again"})
		dialog.Options = append(dialog.Options, core.AgentOption{N: 3, Label: optNoLabel})
	} else {
		dialog.Options = append(dialog.Options, core.AgentOption{N: 2, Label: optNoLabel})
	}
	return dialog
}

// 拒绝/打断的措辞 ( 原文照抄，Claude Code 的日志里认得它们 )。
const (
	saidNo = "The user said no, in herdrchat (their chat)."
	escMsg = "The user pressed Esc, in herdrchat (their chat)."
)

// Decide 是 hook 的决定：{Decision}、{Partial} ( 还有多题未答 ) 或 nil。
func Decide(ask *asks.Ask, shown *core.AgentDialog, reply core.Reply, partial map[string]string) *asks.Decision {
	req := map[string]any{}
	if json.Unmarshal(ask.Request, &req) != nil {
		return nil
	}
	if reply.Esc {
		return &asks.Decision{Decision: asks.DecisionJSON(map[string]any{
			behaviorKey: "deny", "message": escMsg, "interrupt": true,
		})}
	}
	var option *core.AgentOption
	for i, o := range shown.Options {
		if fmt.Sprint(o.N) == reply.Pick {
			option = &shown.Options[i]
			break
		}
	}
	if shown.Kind == "question" {
		var said string
		if option != nil && shown.Free != 0 && option.N == shown.Free {
			said = strings.TrimSpace(reply.Text)
		} else if shown.Multi {
			ticked := map[int]bool{}
			for _, t := range reply.Ticks {
				n, _ := strconv.Atoi(t)
				ticked[n] = true
			}
			var labels []string
			for _, o := range shown.Options {
				if o.N != shown.Free && ticked[o.N] {
					labels = append(labels, o.Label)
				}
			}
			said = strings.Join(labels, ", ")
		} else if option != nil {
			said = option.Label
		}
		if said == "" {
			return nil
		}
		ref, _ := shown.Ref.(map[string]any)
		question, _ := ref["question"].(string)
		answers := map[string]any{}
		for k, v := range partial {
			answers[k] = v
		}
		answers[question] = said
		// 还有没答的题：先记下，都不发。
		if input, ok := req["tool_input"].(map[string]any); ok {
			if questions, ok := input["questions"].([]any); ok {
				for _, q := range questions {
					qm, _ := q.(map[string]any)
					if qm == nil || qm["question"] == nil {
						continue
					}
					name, _ := qm["question"].(string)
					if _, done := answers[name]; !done {
						next := map[string]string{}
						for k, v := range partial {
							next[k] = v
						}
						next[question] = said
						return &asks.Decision{Partial: next}
					}
				}
			}
		}
		if input, ok := req["tool_input"].(map[string]any); ok {
			input["answers"] = answers
			return &asks.Decision{Decision: asks.DecisionJSON(map[string]any{
				behaviorKey: allowWord, "updatedInput": input,
			})}
		}
		return nil
	}
	if option == nil {
		return nil
	}
	deny := option.Label == optNoLabel
	if deny {
		return &asks.Decision{Decision: asks.DecisionJSON(map[string]any{
			behaviorKey: "deny", "message": saidNo,
		})}
	}
	if shown.Kind == "plan" {
		d := map[string]any{behaviorKey: allowWord}
		if input, ok := req["tool_input"].(map[string]any); ok {
			d["updatedInput"] = input
		} else {
			d["updatedInput"] = map[string]any{}
		}
		if option.Label == "Yes, and accept its edits" {
			d["updatedPermissions"] = []map[string]any{{
				"type": "setMode", "mode": "acceptEdits", "destination": "session",
			}}
		}
		return &asks.Decision{Decision: asks.DecisionJSON(d)}
	}
	if option.Label == "Yes, and don't ask again" {
		return &asks.Decision{Decision: asks.DecisionJSON(map[string]any{
			behaviorKey: "allow", "updatedPermissions": req["permission_suggestions"],
		})}
	}
	return &asks.Decision{Decision: asks.DecisionJSON(map[string]any{behaviorKey: "allow"})}
}

// Dialog：终端现在等的是 hook 的哪个请求 ( reader 契约的 Dialoger )。
func (r *Reader) Dialog(ctx context.Context, session, address string) (*core.AgentDialog, error) {
	ask, known := r.Asks.OpenAsk(ctx, session, address, App)
	if !known {
		return nil, core.ErrHookAbsent
	}
	if ask == nil {
		return nil, nil
	}
	return AskDialog(ask, r.Asks.PartialOf(ask.ID)), nil
}

// Answer：把一次点击写成 hook 的决定 ( reader 契约的 Answerer )。
func (r *Reader) Answer(ctx context.Context, session, address string, shown core.AgentDialog, reply core.Reply) (bool, error) {
	menu := &core.Menu{Own: &asks.Ref{ID: shown.Key, Raw: &shown}}
	dialog := core.Dialog{Menu: menu}
	ref, _ := shown.Ref.(map[string]any)
	if id, _ := ref["id"].(string); id != "" {
		menu.Own = &asks.Ref{ID: id, Raw: &shown}
	}
	ok := r.Asks.AnswerAsk(ctx, session, address, App, &dialog, reply, func(ask *asks.Ask, d core.Dialog, rep core.Reply, partial map[string]string) *asks.Decision {
		return Decide(ask, &shown, rep, partial)
	})
	return ok, nil
}
