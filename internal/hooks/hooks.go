// Package hooks 把 herdrchat 的 PermissionRequest hook 放进 Claude Code 与 Codex 的设置里，
// 并把它自带的 ask.sh 落到它们找得到的地方。
//
// 规矩：
//   - ask.sh 逐字节复用 ( 正本即本包内嵌的 ask.sh；出处与归属见 THIRD-PARTY/ )；
//   - 只增删命令里含 herdrchat/hooks/ask.sh 标记的条目，其余一概不动；
//   - 首次改动前备份 <file>.herdrchat-backup；
//   - 解析失败的文件绝不写；
//   - 原子写，保持原 mode ( 新文件 0600 )；
//   - 没装过的 agent ( 它的目录不存在 ) 不碰。
package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	_ "embed"
)

//go:embed ask.sh
var AskSh []byte

// Marker 是认领"这条是我写的"的标记：hook 的固定路径里带着它。
const Marker = "herdrchat/hooks/ask.sh"

// 各 agent 的 hook 等待与超时。
const (
	claudeWait    = 1790
	claudeTimeout = 1800
	codexWait     = 590
	codexTimeout  = 600
)

// agent 名。
const (
	agentClaude = "claude"
	agentCodex  = "codex"
)

// Agents 是装得了 hook 的 agent，顺序即展示顺序。
var Agents = []string{agentClaude, agentCodex}

// Places 是 hook 相关文件的落点。
type Places struct {
	Claude string // settings.json
	Codex  string // hooks.json
	Hook   string // ask.sh 的落点
}

// PlacesOf 从 env ( 可为 nil ) 与 home 目录算出各文件的位置。
func PlacesOf(env map[string]string, home string) Places {
	if env == nil {
		env = map[string]string{}
	}
	claudeDir := filepath.Join(home, ".claude")
	if v := env["CLAUDE_CONFIG_DIR"]; v != "" {
		claudeDir = v
	}
	codexDir := filepath.Join(home, ".codex")
	if v := env["CODEX_HOME"]; v != "" {
		codexDir = v
	}
	share := filepath.Join(home, ".local", "share")
	if v := env["XDG_DATA_HOME"]; v != "" {
		share = v
	}
	return Places{
		Claude: filepath.Join(claudeDir, "settings.json"),
		Codex:  filepath.Join(codexDir, "hooks.json"),
		Hook:   filepath.Join(share, "herdrchat", "hooks", "ask.sh"),
	}
}

// fileOf 一个 agent 的设置文件与它所在的目录。
func fileOf(p Places, agent string) string {
	if agent == agentCodex {
		return p.Codex
	}
	return p.Claude
}

func waitOf(agent string) int {
	if agent == agentCodex {
		return codexWait
	}
	return claudeWait
}

func timeoutOf(agent string) int {
	if agent == agentCodex {
		return codexTimeout
	}
	return claudeTimeout
}

// entry 是写进 PermissionRequest 的那一条。
func entry(p Places, agent string) map[string]any {
	hook := map[string]any{
		"type":    "command",
		"command": fmt.Sprintf("sh '%s' %s %d", p.Hook, agent, waitOf(agent)),
		"timeout": timeoutOf(agent),
	}
	if agent == agentCodex {
		hook["statusMessage"] = "herdrchat is asking your chat for the answer"
	}
	return map[string]any{"hooks": []any{hook}}
}

// isMine 判断一条 PermissionRequest 条目是否 herdrchat 写的。
func isMine(item any) bool {
	m, ok := item.(map[string]any)
	if !ok {
		return false
	}
	list, ok := m["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range list {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, Marker) {
			return true
		}
	}
	return false
}

// readObject 读一个 JSON 设置文件；不存在返回 (nil, nil, false)；
// 解析出来不是 JSON 对象是错误 ( 调用方绝不写回 )。
func readObject(file string) (map[string]any, []byte, bool, error) {
	raw, err := os.ReadFile(file) //nolint:gosec // G304: 路径由 XDG 布局拼出，非不可信输入
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, raw, true, fmt.Errorf("%s: not a JSON object", file)
	}
	return m, raw, true, nil
}

// writeObject 原子写回一个设置文件：保持原 mode ( 新文件 0600 )。
func writeObject(file string, m map[string]any, existed bool) error {
	text, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if existed {
		if info, err := os.Stat(file); err == nil {
			mode = info.Mode()
		}
	}
	tmp := file + ".herdrchat-tmp"
	if err := os.WriteFile(tmp, append(text, '\n'), mode); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// editObject 改一个 agent 的设置文件：fn 拿到根对象，返回是否要写回。
// 目录不存在 ( agent 没装 ) 与解析失败都原样放过。
func editObject(p Places, agent string, fn func(m map[string]any) bool) error {
	file := fileOf(p, agent)
	if info, err := os.Stat(filepath.Dir(file)); err != nil || !info.IsDir() {
		//nolint:nilerr // 目录不在：这个 agent 在这台机器上没有设置，原样放过。
		return nil
	}
	m, raw, existed, err := readObject(file)
	if err != nil {
		return err
	}
	if m == nil {
		m = map[string]any{}
	}
	if !fn(m) {
		return nil
	}
	if existed {
		// 首次改动前留一份原样 ( 只留第一份：备份的是你自己的样子 )。
		backup := file + ".herdrchat-backup"
		if _, err := os.Stat(backup); os.IsNotExist(err) {
			if werr := os.WriteFile(backup, raw, 0o600); werr != nil {
				return werr
			}
		}
	}
	return writeObject(file, m, existed)
}

// permissionRequest 读出 hooks.PermissionRequest 数组 ( 没有即空；不改 m )。
func permissionRequest(m map[string]any) []any {
	hooksObj, _ := m["hooks"].(map[string]any)
	list, _ := hooksObj["PermissionRequest"].([]any)
	return list
}

// others 是数组里不属于 herdrchat 的条目。
func others(list []any) []any {
	kept := make([]any, 0, len(list)+1)
	for _, item := range list {
		if !isMine(item) {
			kept = append(kept, item)
		}
	}
	return kept
}

// setPermissionRequest 写回 hooks.PermissionRequest；
// 空了就把键摘掉，连同空的 hooks 对象一起 ( 卸载后文件回到没见过 herdrchat 的样子 )。
func setPermissionRequest(m map[string]any, list []any) {
	hooksObj, ok := m["hooks"].(map[string]any)
	if len(list) == 0 {
		if ok {
			delete(hooksObj, "PermissionRequest")
			if len(hooksObj) == 0 {
				delete(m, "hooks")
			}
		}
		return
	}
	if !ok {
		hooksObj = map[string]any{}
		m["hooks"] = hooksObj
	}
	hooksObj["PermissionRequest"] = list
}

// InstallHooks 放好 ask.sh 本体 ( mode 0700 )，
// 并把每个点名的 agent 的条目放进它的设置 ( 已在则先摘旧再放新：装两遍仍只有一条 )。
func InstallHooks(agents []string, p Places) error {
	if err := os.MkdirAll(filepath.Dir(p.Hook), 0o700); err != nil {
		return err
	}

	//nolint:gosec // G306: hook 是要被 sh 执行的脚本，0700 是它该有的 mode。
	if err := os.WriteFile(p.Hook, AskSh, 0o700); err != nil {
		return err
	}

	//nolint:gosec // G302: 同上，已存在的旧文件也要可执行。
	if err := os.Chmod(p.Hook, 0o700); err != nil {
		return err
	}

	for _, agent := range agents {
		err := editObject(p, agent, func(m map[string]any) bool {
			setPermissionRequest(m, append(others(permissionRequest(m)), entry(p, agent)))
			return true
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// UninstallHooks 从两个 agent 的设置里摘掉 herdrchat 的条目，并移走 ask.sh。
// 读不懂的设置文件原样留着，只把这件事报出来 ( ask.sh 照样移走 )。
func UninstallHooks(p Places) error {
	var first error
	for _, agent := range Agents {
		err := editObject(p, agent, func(m map[string]any) bool {
			list := permissionRequest(m)
			kept := others(list)
			if len(kept) == len(list) {
				// 没有我的条目：文件不动。
				return false
			}
			setPermissionRequest(m, kept)
			return true
		})

		if err != nil && first == nil {
			first = err
		}
	}

	if err := os.Remove(p.Hook); err != nil && !os.IsNotExist(err) && first == nil {
		first = err
	}
	return first
}

// Status 是一个 agent 的 hook 状态。
type Status struct {
	Agent string
	There bool // 这个 agent 在这台机器上 ( 它的设置目录在 )
	In    bool // herdrchat 的条目在它的设置里
}

// StatusOf 说每个 agent 的 hook 在不在。
func StatusOf(p Places) []Status {
	out := make([]Status, 0, len(Agents))
	for _, agent := range Agents {
		file := fileOf(p, agent)
		there := false
		if info, err := os.Stat(filepath.Dir(file)); err == nil && info.IsDir() {
			there = true
		}
		in := false
		if m, _, _, err := readObject(file); err == nil && m != nil {
			if slices.ContainsFunc(permissionRequest(m), isMine) {
				in = true
			}
		}
		out = append(out, Status{Agent: agent, There: there, In: in})
	}
	return out
}
