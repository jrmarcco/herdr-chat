package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// hook 安装器的行为测试 ( hook 协议本身与各 agent 的 askDialog/decide 已在 engine / readers 的测试里 )。
const mineJSON = `{"model":"x","hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"my-own.sh"}]}],"Stop":[{"hooks":[{"type":"command","command":"stop.sh"}]}]}}`

func readJSON(t *testing.T, file string) map[string]any {
	t.Helper()
	//nolint:gosec // G304: 测试自己写的临时文件。
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return m
}

func mustWrite(t *testing.T, file, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// firstHook 取 PermissionRequest[i] 的第一条 hook。
func firstHook(t *testing.T, m map[string]any, i int) map[string]any {
	t.Helper()
	list := permissionRequest(m)
	if i >= len(list) {
		t.Fatalf("PermissionRequest has %d entries, want one at %d", len(list), i)
	}
	inner, _ := list[i].(map[string]any)["hooks"].([]any)
	if len(inner) == 0 {
		t.Fatalf("entry %d has no hooks", i)
	}
	hook, ok := inner[0].(map[string]any)
	if !ok {
		t.Fatalf("entry %d's hook is not an object", i)
	}
	return hook
}

func TestAskShIsTheHookItself(t *testing.T) {
	// ask.sh 的正本就是这里的内嵌副本。
	// 这里锁住它仍是我们认的那个 hook：协议的。
	// 路径、agent 校验与用法行，一个都不能少。
	script := string(AskSh)
	for _, marker := range []string{
		"#!/bin/sh",
		"herdrchat's PermissionRequest hook for Claude Code and Codex",
		"$HOME/.local/state/herdrchat/asks",
		`case "$agent" in claude|codex) ;; *) exit 0 ;; esac`,
		`hc label "$t" "herdrchat.ask=$id"`,
		`req="$d/req/$id.json"`,
		`ans="$d/ans/$id.json"`,
		"trap done_ TERM INT HUP",
	} {
		if !strings.Contains(script, marker) {
			t.Fatalf("the embedded ask.sh lost its marker %q", marker)
		}
	}
	if !bytes.HasSuffix(AskSh, []byte("done_\n")) {
		t.Fatal("the embedded ask.sh lost its tail")
	}
}

func TestPlacesFollowTheAgentsOwnVariables(t *testing.T) {
	p := PlacesOf(nil, "/h")
	if p.Claude != "/h/.claude/settings.json" || p.Codex != "/h/.codex/hooks.json" || p.Hook != "/h/.local/share/herdrchat/hooks/ask.sh" {
		t.Fatalf("usual places: %+v", p)
	}
	p = PlacesOf(map[string]string{"CLAUDE_CONFIG_DIR": "/c", "CODEX_HOME": "/x"}, "/h")
	if p.Claude != "/c/settings.json" || p.Codex != "/x/hooks.json" {
		t.Fatalf("moved places: %+v", p)
	}
}

func TestInstallBesideWhatIsThereOnceWithABackupAndUninstallTakesOnlyItOut(t *testing.T) {
	home := t.TempDir()
	p := PlacesOf(nil, home)
	mustWrite(t, p.Claude, mineJSON)

	var mine map[string]any
	if err := json.Unmarshal([]byte(mineJSON), &mine); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := InstallHooks([]string{agentClaude, agentCodex}, p); err != nil {
			t.Fatal(err)
		}
	}

	after := readJSON(t, p.Claude)
	if after["model"] != "x" {
		t.Fatalf("model = %v, yours is kept", after["model"])
	}
	if n := len(permissionRequest(after)); n != 2 {
		t.Fatalf("PermissionRequest has %d entries, want yours and herdrchat's once", n)
	}

	hook := firstHook(t, after, 1)
	if cmd, _ := hook["command"].(string); !regexp.MustCompile(`herdrchat/hooks/ask\.sh' claude 1790$`).MatchString(cmd) {
		t.Fatalf("command = %q", cmd)
	}
	if hook["timeout"] != float64(1800) {
		t.Fatalf("timeout = %v, want 1800", hook["timeout"])
	}
	if backup := readJSON(t, p.Claude+".herdrchat-backup"); !reflect.DeepEqual(backup, mine) {
		t.Fatalf("the backup is not what was there first: %v", backup)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Fatal("an agent that is not set up here is left alone")
	}

	info, err := os.Stat(p.Hook)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("hook mode = %o, want 700", info.Mode().Perm())
	}

	want := []Status{{Agent: agentClaude, There: true, In: true}, {Agent: agentCodex}}
	if got := StatusOf(p); !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %+v, want %+v", got, want)
	}

	if err := UninstallHooks(p); err != nil {
		t.Fatal(err)
	}
	if got := readJSON(t, p.Claude); !reflect.DeepEqual(got, mine) {
		t.Fatalf("after uninstall = %v, want what was there first", got)
	}
	if _, err := os.Stat(p.Hook); !os.IsNotExist(err) {
		t.Fatal("the hook is still there after uninstall")
	}
}

func TestCodexGetsAStatusLineAndAFileThatIsNotJSONIsNeverTouched(t *testing.T) {
	home := t.TempDir()

	p := PlacesOf(nil, home)
	if err := os.MkdirAll(filepath.Dir(p.Codex), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks([]string{agentCodex}, p); err != nil {
		t.Fatal(err)
	}

	hook := firstHook(t, readJSON(t, p.Codex), 0)
	if cmd, _ := hook["command"].(string); !strings.HasSuffix(cmd, " codex 590") {
		t.Fatalf("command = %q", cmd)
	}
	if hook["timeout"] != float64(600) {
		t.Fatalf("timeout = %v, want 600", hook["timeout"])
	}
	if msg, _ := hook["statusMessage"].(string); !strings.Contains(msg, "herdrchat") {
		t.Fatalf("statusMessage = %q", msg)
	}
	if info, err := os.Stat(p.Codex); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("a new settings file is private: %v %v", info, err)
	}

	const broken = "{ not json"
	mustWrite(t, p.Claude, broken)

	err := InstallHooks([]string{agentClaude, agentCodex}, p)
	if err == nil || !strings.Contains(err.Error(), "not a JSON object") {
		t.Fatalf("err = %v, want it to say the file is not a JSON object", err)
	}
	if raw, _ := os.ReadFile(p.Claude); string(raw) != broken {
		t.Fatalf("the broken file was touched: %q", raw)
	}
	if _, err := os.Stat(p.Claude + ".herdrchat-backup"); !os.IsNotExist(err) {
		t.Fatal("a file that was never changed needs no backup")
	}
}

// 新装的条目在卸载后不留痕：没有 PermissionRequest 的文件回到原样；mode 保持。
func TestUninstallLeavesNoTraceAndKeepsTheMode(t *testing.T) {
	home := t.TempDir()
	p := PlacesOf(nil, home)
	mustWrite(t, p.Claude, `{"model":"x"}`)

	//nolint:gosec // G302: 这个文件要宽一点才测得出"保持原 mode"。
	if err := os.Chmod(p.Claude, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks([]string{agentClaude}, p); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(p.Claude); info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %o, the file keeps its own", info.Mode().Perm())
	}
	if err := UninstallHooks(p); err != nil {
		t.Fatal(err)
	}
	if got := readJSON(t, p.Claude); !reflect.DeepEqual(got, map[string]any{"model": "x"}) {
		t.Fatalf("after uninstall = %v", got)
	}

	// 再卸一遍：什么都没有，也不出错。
	if err := UninstallHooks(p); err != nil {
		t.Fatalf("uninstalling twice: %v", err)
	}
}
