package opencode

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jrmarcco/herdr-chat/internal/core"
)

// refPermission 是 ref 的类型词。
const refPermission = "permission"

// 它的对话框，走它自己的 API：一个 form 或一条权限请求 ( 见包注释 )。

// 权限请求的三选项。
var permissionOptions = []core.AgentOption{
	{N: 1, Label: "Allow once"},
	{N: 2, Label: "Allow always"},
	{N: 3, Label: "Reject"},
}

// DialogOf 把 forms 与 permissions 排成现在等着的卡；answered 是各 form 已答
// 的字段 ( {field: value} )。
func DialogOf(forms, permissions any, answered map[string]map[string]any) *core.AgentDialog {
	if form := firstOf(forms); form != nil {
		done := answered[idOf(form)]
		fields, _ := form["fields"].([]any)
		var field map[string]any
		left := 0
		for _, f := range fields {
			fm, ok := f.(map[string]any)
			if !ok {
				continue
			}
			key, _ := fm["key"].(string)
			if done != nil {
				if _, has := done[key]; has {
					continue
				}
			}
			left++
			if field == nil {
				field = fm
			}
		}
		if field != nil {
			key, _ := field["key"].(string)
			ftype, _ := field["type"].(string)
			multi := regexp.MustCompile(`(?i)multi|array`).MatchString(ftype) || field["multiple"] == true
			dialog := &core.AgentDialog{
				Key: idOf(form) + "#" + key, Kind: "question", Multi: multi,
				Ref: map[string]any{"type": "form", "id": idOf(form), "field": key, "fields": fieldKeys(fields)},
			}
			if title, _ := form["title"].(string); title != "" {
				dialog.Title = title
			}
			if kind, _ := metadata(form); kind != "" {
				dialog.Kind = kind
			}
			if options, ok := field["options"].([]any); ok {
				for _, o := range options {
					om, _ := o.(map[string]any)
					label := ""
					value := any(nil)
					if om != nil {
						label, _ = om["label"].(string)
						if label == "" {
							label = fmt.Sprint(om["value"])
						}
						value = om["value"]
					}
					opt := core.AgentOption{N: len(dialog.Options) + 1, Label: label}
					if multi {
						off := false
						opt.Checked = &off
					}
					dialog.Options = append(dialog.Options, opt)
					_ = value
				}
			}
			if field["custom"] == true {
				dialog.Options = append(dialog.Options, core.AgentOption{N: len(dialog.Options) + 1, Label: "Type your own answer"})
				dialog.Free = len(dialog.Options)
			}
			var text []string
			for _, part := range []string{str(field["title"]), str(field["description"])} {
				if part != "" {
					text = append(text, part)
				}
			}
			if left > 1 {
				text = append(text, fmt.Sprintf("(question %d of %d)", len(fields)-left+1, len(fields)))
			}
			dialog.Text = strings.Join(text, "\n")
			return dialog
		}
	}
	if request := firstOf(permissions); request != nil {
		what := []string{str(request["message"]), str(request["action"])}
		if resources, ok := request["resources"].([]any); ok {
			for _, res := range resources {
				if s, ok := res.(string); ok {
					what = append(what, s)
				}
			}
		}
		return &core.AgentDialog{
			Key: "permission#" + idOf(request), Kind: "permission", Title: "Permission",
			Text: strings.Join(nonEmpty(what), "\n"), Options: permissionOptions,
			Ref: map[string]any{"type": "permission", "id": idOf(request)},
		}
	}
	return nil
}

func firstOf(list any) map[string]any {
	items, _ := list.([]any)
	if len(items) == 0 {
		return nil
	}
	m, _ := items[0].(map[string]any)
	return m
}

func idOf(m map[string]any) string {
	id, _ := m["id"].(string)
	return id
}

func metadata(form map[string]any) (string, bool) {
	meta, _ := form["metadata"].(map[string]any)
	if meta == nil {
		return "", false
	}
	kind, _ := meta["kind"].(string)
	return kind, true
}

func fieldKeys(fields []any) []string {
	var out []string
	for _, f := range fields {
		if fm, ok := f.(map[string]any); ok {
			out = append(out, str(fm["key"]))
		}
	}
	return out
}

func nonEmpty(parts []string) []string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Dialog：现在等着的卡 ( reader 契约 )。
func (r *Reader) Dialog(ctx context.Context, session, address string) (*core.AgentDialog, error) {
	machine, cwd, id, ok := r.where(ctx, session, address)
	if !ok {
		return nil, nil
	}
	forms, _ := r.api(ctx, session, machine, cwd, "session.form.list", map[string]string{paramSession: id}, nil, false)
	permissions, _ := r.api(ctx, session, machine, cwd, "session.permission.list", map[string]string{paramSession: id}, nil, false)
	r.mu.Lock()
	partial := make(map[string]map[string]any, len(r.partial))
	for k, v := range r.partial {
		partial[k] = v
	}
	r.mu.Unlock()
	return DialogOf(forms, permissions, partial), nil
}

// Answer 回答一张卡；多题 form 每题有答案后一次送出。
func (r *Reader) Answer(ctx context.Context, session, address string, shown core.AgentDialog, reply core.Reply) (bool, error) {
	machine, cwd, id, ok := r.where(ctx, session, address)
	if !ok || shown.Ref == nil {
		return false, nil
	}
	params := map[string]string{paramSession: id}
	ref, _ := shown.Ref.(map[string]any)
	if ref["type"] == refPermission {
		decision := ""
		if reply.Esc {
			decision = "reject"
		} else {
			for _, o := range shown.Options {
				if fmt.Sprint(o.N) == reply.Pick {
					switch o.Label {
					case "Allow once":
						decision = "once"
					case "Allow always":
						decision = "always"
					case "Reject":
						decision = "reject"
					}
					break
				}
			}
		}
		if decision == "" {
			return false, nil
		}
		params["requestID"] = fmt.Sprint(ref["id"])
		_, ok := r.api(ctx, session, machine, cwd, "session.permission.reply", params, map[string]any{"decision": decision}, false)
		return ok, nil
	}
	formID, _ := ref["id"].(string)
	if reply.Esc {
		r.mu.Lock()
		delete(r.partial, formID)
		r.mu.Unlock()
		params["formID"] = formID
		_, ok := r.api(ctx, session, machine, cwd, "session.form.cancel", params, nil, false)
		return ok, nil
	}
	var value any
	hasValue := false
	if shown.Multi {
		list := []any{}
		ticked := map[int]bool{}
		for _, t := range reply.Ticks {
			n, _ := strconv.Atoi(t)
			ticked[n] = true
		}
		for _, o := range shown.Options {
			if ticked[o.N] {
				list = append(list, o.Label)
			}
		}
		value = list
		hasValue = true
	} else {
		var option *core.AgentOption
		for j := range shown.Options {
			if fmt.Sprint(shown.Options[j].N) == reply.Pick {
				option = &shown.Options[j]
				break
			}
		}
		if shown.Free != 0 && option != nil && option.N == shown.Free {
			if text := strings.TrimSpace(reply.Text); text != "" {
				value = text
				hasValue = true
			}
		} else if option != nil {
			value = option.Label
			hasValue = true
		}
	}
	if !hasValue || value == "" {
		return false, nil
	}
	r.mu.Lock()
	done := map[string]any{}
	for k, v := range r.partial[formID] {
		done[k] = v
	}
	field, _ := ref["field"].(string)
	done[field] = value
	fieldsDone := true
	if keys, ok := ref["fields"].([]string); ok {
		for _, k := range keys {
			if _, has := done[k]; !has {
				fieldsDone = false
				break
			}
		}
	}
	if !fieldsDone {
		r.partial[formID] = done
		r.mu.Unlock()
		return true, nil // 下一题接着问；都答了才送
	}
	delete(r.partial, formID)
	r.mu.Unlock()
	params["formID"] = formID
	_, okAPI := r.api(ctx, session, machine, cwd, "session.form.reply", params, map[string]any{"answer": done}, false)
	return okAPI, nil
}
