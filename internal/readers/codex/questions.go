package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/asks"
	"github.com/jrmarcco/herdr-chat/internal/core"
)

// 它的提问，从它自己的日志里读 ( 原文照抄的两种形态 )：
//
//	request_user_input ( Plan 模式 )：
//	一次 function_call，arguments 是问题 ( [{id, header, question, options: [{label, description}]}] )；
// 	它等着 ( blocked、kind question ) 直到对应的 function_call_output 落盘。
//	Codex 一次画一题 ( "Question i/n" )：数字键选择并进下一题，最后一题按下即全部送出；
//	"None of the above" ( 移动光标、tab、输入、Enter ) 是你自己的话。
// 	卡是屏幕正画的那一题，也用那些键回答。
//
//	request_user_input_async：
// 	一条带 questions 的 AgentMessage；
// 	没有谁在等 ( turn 继续、或已结束 )，你的回答就是你发的下一条消息。
//  Codex 不干活时它的卡常驻；点卡即发那条消息 ( 多题：每题有答案后一次发出 )。
// 	你接下来说的任何话都算对它的回答。

// questionsTail 是查找提问要看的日志尾部行数。
const (
	kindQuestion = "question"

	// questionsTail 是查找提问要看的日志尾部行数。
	questionsTail = 400
)

// questionScreen 是读屏找 "Question i/n" 的行数。
const questionScreen = 40

// Questions 是日志里未答的两种提问，各自可为空。
type Questions struct {
	Plan  *Questioned // Plan 模式 ( 在等 )
	Later *Questioned // 异步 ( 不挡工作 )
}

// Questioned 是一组提问：call_id 或 item id，与问题列表。
type Questioned struct {
	ID        string
	Questions []Question
}

// Question 是 Codex 的一条提问。
type Question struct {
	Header      string
	Question    string
	Title       string
	Options     []Option
	Description string
}

// Option 是一条选项。
type Option struct {
	Label       string
	Description string
}

var flatRe = regexp.MustCompile(`\s+`)

func flat(s string) string { return strings.TrimSpace(flatRe.ReplaceAllString(s, " ")) }

// QuestionsIn 从日志行里找未答的提问。
func QuestionsIn(lines []string) Questions {
	var out Questions
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			// 尾部被切断的首行。
			continue
		}

		etype, _ := entry["type"].(string)
		payload, _ := entry["payload"].(map[string]any)
		if payload == nil {
			payload = map[string]any{}
		}
		ptype, _ := payload["type"].(string)

		switch {
		case etype == "response_item" && ptype == "function_call" && payload["name"] == "request_user_input":
			var args struct {
				Questions []struct {
					Header   string `json:"header"`
					Question string `json:"question"`
					Options  []struct {
						Label       string `json:"label"`
						Description string `json:"description"`
					} `json:"options"`
				} `json:"questions"`
			}
			raw, _ := payload["arguments"].(string)
			if json.Unmarshal([]byte(raw), &args) != nil {
				out.Plan = nil
				continue
			}
			var questions []Question
			for _, q := range args.Questions {
				if q.Question == "" {
					continue
				}
				pq := Question{Header: q.Header, Question: q.Question}
				for _, o := range q.Options {
					pq.Options = append(pq.Options, Option{Label: o.Label, Description: o.Description})
				}
				questions = append(questions, pq)
			}
			if len(questions) > 0 {
				id, _ := payload["call_id"].(string)
				if id == "" {
					id, _ = payload["id"].(string)
				}
				out.Plan = &Questioned{ID: id, Questions: questions}
			} else {
				out.Plan = nil
			}
		case etype == "response_item" && ptype == "function_call_output":
			if out.Plan != nil && payload["call_id"] == out.Plan.ID {
				out.Plan = nil
			}
		case etype == eventMsg && (ptype == "task_complete" || ptype == "turn_aborted"):
			// 它的 turn 结束：没有谁在等。
			out.Plan = nil
		case etype == eventMsg && ptype == "item_completed":
			item, _ := payload["item"].(map[string]any)
			if item == nil {
				continue
			}
			var questions []Question
			if qs, ok := item["questions"].([]any); ok {
				for _, q := range qs {
					if qm, ok := q.(map[string]any); ok {
						if title, _ := qm["title"].(string); title != "" {
							question := Question{Title: title}
							if opts, ok := qm["options"].([]any); ok {
								for _, o := range opts {
									if om, ok := o.(map[string]any); ok {
										l, _ := om["label"].(string)
										d, _ := om["description"].(string)
										question.Options = append(question.Options, Option{Label: l, Description: d})
									}
								}
							}
							questions = append(questions, question)
						}
					}
				}
			}
			switch {
			case item["type"] == "AgentMessage" && len(questions) > 0:
				id, _ := item["id"].(string)
				out.Later = &Questioned{ID: id, Questions: questions}
			case item["type"] == "UserMessage":
				out.Later = nil
			}
		}
	}
	return out
}

// OnScreen 是屏幕正画的那题：第几题、共几题、光标 ( › ) 在哪、下方一行词。
type OnScreen struct {
	Index  int
	Count  int
	Cursor int
	Words  string
}

var (
	questionLine = regexp.MustCompile(`^\s*Question (\d+)/(\d+)`)
	cursorLine   = regexp.MustCompile(`^\s*›\s*(\d+)\.`)
)

// QuestionOnScreen 读屏幕找 "Question i/n"。
func QuestionOnScreen(text string) *OnScreen {
	lines := strings.Split(text, "\n")
	at := -1
	for i := len(lines) - 1; i >= 0 && at < 0; i-- {
		if questionLine.MatchString(lines[i]) {
			at = i
		}
	}

	if at < 0 {
		return nil
	}

	m := questionLine.FindStringSubmatch(lines[at])
	idx, _ := strconv.Atoi(m[1])
	count, _ := strconv.Atoi(m[2])
	cursor := 1
	for _, l := range lines[at+1:] {
		if c := cursorLine.FindStringSubmatch(l); c != nil {
			cursor, _ = strconv.Atoi(c[1])
			break
		}
	}
	return &OnScreen{Index: idx - 1, Count: count, Cursor: cursor, Words: flat(strings.Join(lines[at+1:], " "))}
}

// PlanDialog 是 Plan 模式第 index 题的卡。
func PlanDialog(plan *Questioned, index int) *core.AgentDialog {
	if plan == nil || index < 0 || index >= len(plan.Questions) {
		return nil
	}

	q := plan.Questions[index]
	dialog := &core.AgentDialog{
		Key: plan.ID + "#" + fmt.Sprint(index), Kind: kindQuestion, Title: "Codex asks",
		Ref: map[string]any{idKey: plan.ID, "index": index, "count": len(plan.Questions), "question": q.Question},
	}

	var text []string
	for _, o := range q.Options {
		dialog.Options = append(dialog.Options, core.AgentOption{N: len(dialog.Options) + 1, Label: o.Label})
		if o.Description != "" {
			text = append(text, o.Label+": "+o.Description)
		}
	}

	dialog.Options = append(dialog.Options, core.AgentOption{N: len(dialog.Options) + 1, Label: "None of the above (your own words)"})
	dialog.Free = len(dialog.Options)
	for _, part := range []string{q.Header, q.Question, strings.Join(text, "\n")} {
		if part != "" {
			dialog.Text = strings.TrimSpace(dialog.Text + "\n" + part)
		}
	}

	if len(plan.Questions) > 1 {
		dialog.Text += fmt.Sprintf("\n(question %d of %d)", index+1, len(plan.Questions))
	}
	return dialog
}

// LaterDialog 是第一条还没答的异步提问的卡 ( partial：{index: 答案} )。
func LaterDialog(later *Questioned, partial map[int]string) *core.AgentDialog {
	if later == nil {
		return nil
	}
	index := -1
	for j := range later.Questions {
		if _, done := partial[j]; !done {
			index = j
			break
		}
	}
	if index < 0 {
		return nil
	}
	q := later.Questions[index]
	dialog := &core.AgentDialog{
		Key: later.ID + "#" + fmt.Sprint(index), Kind: kindQuestion, Title: "Codex asks", Idle: true,
		Ref: map[string]any{"id": later.ID, "index": index, "titles": titlesOf(later)},
	}
	for _, o := range q.Options {
		dialog.Options = append(dialog.Options, core.AgentOption{N: len(dialog.Options) + 1, Label: o.Label})
	}
	dialog.Options = append(dialog.Options, core.AgentOption{N: len(dialog.Options) + 1, Label: "Type your own answer"})
	dialog.Free = len(dialog.Options)
	dialog.Text = q.Title
	if len(later.Questions) > 1 {
		dialog.Text += fmt.Sprintf("\n(question %d of %d)", index+1, len(later.Questions))
	}
	return dialog
}

func titlesOf(later *Questioned) []string {
	out := make([]string, len(later.Questions))
	for i, q := range later.Questions {
		out[i] = q.Title
	}
	return out
}

// LaterMessage 是回答异步提问的那条消息：单题即答案本身，多题每题一行。
func LaterMessage(titles []string, answers []string) string {
	if len(titles) == 1 && len(answers) == 1 {
		return answers[0]
	}
	var lines []string
	for i, t := range titles {
		if i < len(answers) {
			lines = append(lines, t+" "+answers[i])
		}
	}
	return strings.Join(lines, "\n")
}

// questionsOf 读该终端日志尾部，给出其中的提问。
func (r *Reader) questionsOf(ctx context.Context, session, address string) (*Questions, hccliTerminal, bool) {
	term, err := r.HC.GetTerminal(ctx, session, address)
	if err != nil {
		return nil, nil, false
	}
	fg, _ := term["foreground"].(map[string]any)
	if fg == nil || num(fg["pid"]) == 0 || fgCwd(fg) == "" {
		return nil, term, false
	}
	file := r.logOf(ctx, session, address, fg)
	if file == "" {
		return nil, term, false
	}
	text, err := r.Files.HomeFile(ctx, session, machineOfAddr(address), file, questionsTail)
	if err != nil {
		return nil, term, false
	}
	found := QuestionsIn(strings.Split(text, "\n"))
	return &found, term, true
}

type hccliTerminal = map[string]any

func machineOfAddr(address string) string {
	if i := strings.LastIndex(address, "/"); i >= 0 {
		return address[:i]
	}
	return ""
}

// Dialog Codex 现在问的是什么，hook 的请求优先，其次日志里的提问；
// 都没有返回 nil；
// Plan 的题还没画出来也返回 nil ( 等它画 )。
func (r *Reader) Dialog(ctx context.Context, session, address string) (*core.AgentDialog, error) {
	ask, known := r.Asks.OpenAsk(ctx, session, address, App)
	if !known {
		return nil, core.ErrHookAbsent
	}
	if ask != nil {
		return AskDialog(ask), nil
	}

	found, term, ok := r.questionsOf(ctx, session, address)
	if !ok {
		return nil, nil
	}

	if found.Plan != nil {
		screenText, _ := r.HC.ScreenTail(ctx, session, address, questionScreen)
		on := QuestionOnScreen(screenText)
		if on == nil || on.Count != len(found.Plan.Questions) {
			return nil, nil
		}
		dialog := PlanDialog(found.Plan, on.Index)
		if dialog == nil {
			return nil, nil
		}
		ref, _ := dialog.Ref.(map[string]any)
		question, _ := ref["question"].(string)
		head := flat(question)
		if len(head) > 40 {
			head = head[:40]
		}
		if !strings.Contains(on.Words, head) {
			return nil, nil
		}
		return dialog, nil
	}
	if found.Later != nil {
		r.mu.Lock()
		done := r.laterDone[found.Later.ID]
		partial := r.laterPart[found.Later.ID]
		r.mu.Unlock()
		stateWord := terminalState(term)
		if !done && stateWord != "working" && stateWord != stateBlockedWord {
			if dialog := LaterDialog(found.Later, partial); dialog != nil {
				return dialog, nil
			}
		}
	}

	return nil, nil
}

func terminalState(term map[string]any) string {
	status, _ := term["status"].(map[string]any)
	if status == nil {
		return ""
	}
	s, _ := status["state"].(string)
	return s
}

const stateBlockedWord = "blocked"

// Answer 回答一张卡，异步提问发那条消息；Plan 的题按数字键；其余走 hook。
func (r *Reader) Answer(ctx context.Context, session, address string, shown core.AgentDialog, reply core.Reply) (bool, error) {
	if shown.Idle {
		return r.answerLater(ctx, session, address, shown, reply)
	}
	ref, _ := shown.Ref.(map[string]any)
	if _, hasCount := ref["count"]; hasCount {
		return r.answerPlan(ctx, session, address, shown, ref, reply)
	}
	id, _ := ref["id"].(string)
	dialog := core.Dialog{Menu: &core.Menu{Own: &asks.Ref{ID: id, Raw: &shown}}}
	ok := r.Asks.AnswerAsk(ctx, session, address, App, &dialog, reply, func(ask *asks.Ask, d core.Dialog, rep core.Reply, _ map[string]string) *asks.Decision {
		return Decide(ask, &shown, rep)
	})
	return ok, nil
}

func (r *Reader) answerPlan(ctx context.Context, session, address string, shown core.AgentDialog, ref map[string]any, reply core.Reply) (bool, error) {
	if reply.Esc {
		return true, r.HC.Keys(ctx, session, address, []string{"esc"})
	}

	var option *core.AgentOption
	for i, o := range shown.Options {
		if fmt.Sprint(o.N) == reply.Pick {
			option = &shown.Options[i]
			break
		}
	}

	if option == nil {
		return false, nil
	}
	if option.N != shown.Free {
		return true, r.HC.Keys(ctx, session, address, []string{fmt.Sprint(option.N)})
	}
	text := strings.TrimSpace(reply.Text)
	if text == "" {
		return false, nil
	}
	screenText, _ := r.HC.ScreenTail(ctx, session, address, questionScreen)
	on := QuestionOnScreen(screenText)
	idx, _ := ref["index"].(int)
	if on == nil || on.Index != idx {
		return false, nil
	}

	moves := option.N - on.Cursor
	if moves != 0 {
		keys := make([]string, 0, abs(moves))
		for i := 0; i < abs(moves); i++ {
			if moves > 0 {
				keys = append(keys, "down")
			} else {
				keys = append(keys, "up")
			}
		}
		if err := r.HC.Keys(ctx, session, address, keys); err != nil {
			return false, err
		}
	}
	time.Sleep(200 * time.Millisecond)
	if err := r.HC.Keys(ctx, session, address, []string{"tab"}); err != nil {
		return false, err
	}
	time.Sleep(300 * time.Millisecond)
	return true, r.HC.Send(ctx, session, address, text, true)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (r *Reader) answerLater(ctx context.Context, session, address string, shown core.AgentDialog, reply core.Reply) (bool, error) {
	ref, _ := shown.Ref.(map[string]any)
	id, _ := ref["id"].(string)
	index, _ := ref["index"].(int)
	titles, _ := ref["titles"].([]string)
	if rawTitles, ok := ref["titles"].([]any); ok {
		titles = nil
		for _, t := range rawTitles {
			if s, ok := t.(string); ok {
				titles = append(titles, s)
			}
		}
	}
	if reply.Esc {
		r.mu.Lock()
		r.laterDone[id] = true
		r.mu.Unlock()
		return true, nil
	}

	var option *core.AgentOption
	for i, o := range shown.Options {
		if fmt.Sprint(o.N) == reply.Pick {
			option = &shown.Options[i]
			break
		}
	}
	said := ""
	if option != nil && option.N == shown.Free {
		said = strings.TrimSpace(reply.Text)
	} else if option != nil {
		said = option.Label
	}
	if said == "" {
		return false, nil
	}

	r.mu.Lock()
	partial := map[int]string{}
	for k, v := range r.laterPart[id] {
		partial[k] = v
	}
	partial[index] = said
	if len(partial) < len(titles) {
		r.laterPart[id] = partial
		r.mu.Unlock()
		return true, nil
	}

	r.mu.Unlock()
	answers := make([]string, len(titles))

	for i := range titles {
		answers[i] = partial[i]
	}
	if err := r.HC.Send(ctx, session, address, LaterMessage(titles, answers), false); err != nil {
		return false, err
	}
	r.mu.Lock()
	delete(r.laterPart, id)
	r.laterDone[id] = true
	r.mu.Unlock()
	return true, nil
}
