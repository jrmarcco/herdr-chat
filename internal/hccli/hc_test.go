package hccli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 纯函数部分：env 剥离、`--` 规则与屏幕整理。
func TestSplitWhere(t *testing.T) {
	if s, tab, ok := SplitWhere("alpha / 2"); !ok || s != "alpha" || tab != "2" {
		t.Errorf("alpha / 2 = %q %q %v", s, tab, ok)
	}
	if s, tab, ok := SplitWhere("a / b / c"); !ok || s != "a" || tab != "b / c" {
		t.Errorf("a / b / c = %q %q %v (the tab keeps a later separator)", s, tab, ok)
	}
	if _, _, ok := SplitWhere(""); ok {
		t.Errorf("no separator: not a where")
	}
}

func TestMachineOf(t *testing.T) {
	if got := MachineOf("t37"); got != "" {
		t.Errorf("t37 = %q, want no machine", got)
	}
	if got := MachineOf("box/t4t"); got != "box" {
		t.Errorf("box/t4t = %q", got)
	}
}

func TestTidyScreen(t *testing.T) {
	mess := "  hi  \n\n\n  \nthere\n\n\n  end  \n\n"
	if got := TidyScreen(mess, -1); got != "  hi\n\nthere\n\n  end" {
		// 注意：行首空格保留 ( 对话框是带着它们画出来的 )。
		t.Errorf("tidy = %q", got)
	}
	if got := TidyScreen("a\nb\nc", 2); got != "b\nc" {
		t.Errorf("last 2 = %q", got)
	}
	if got := TidyScreen("", -1); got != "" {
		t.Errorf("empty = %q", got)
	}
}

func TestPlainErrorAndIsUnavailable(t *testing.T) {
	// hc 自己的 JSON 错误，落在包装层的错误消息里时。
	err := errorOf(`hc send t1 -- hi: hc: {"error":{"message":"machine box is not connected","kind":"unavailable"}}`)
	if got := PlainError(err); got != "machine box is not connected" {
		t.Errorf("plainError = %q", got)
	}
	if !IsUnavailable(err) {
		t.Errorf("kind unavailable must be seen")
	}
	fin := errorOf(`hc send t1 -- hi: {"error":{"message":"the terminal is gone","kind":"finished"}}`)
	if got := PlainError(fin); got != "this terminal has ended." {
		t.Errorf("finished = %q", got)
	}
	// 没有 hc 的 JSON 时，剥掉 "hc <args>: " 包装，其余保留。
	if got := PlainError(errorOf("hc get t1: not found")); got != "not found" {
		t.Errorf("plain = %q", got)
	}
	if got := PlainError(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
}

type strErr string

func (e strErr) Error() string { return string(e) }

func errorOf(s string) error { return strErr(s) }

// `--` 规则：你输入的内容绝不会被当成选项，
// 即使以 "-" 开头。
func TestSendPutsTextAfterDashes(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "args")
	stub := filepath.Join(dir, "hc")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + quote(out) + "\n"

	//nolint:gosec // G306: 测试 stub 脚本需要可执行位。
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &CLI{Bin: stub, Env: []string{"HC_TERMINAL_ID=t0", "HC_TERMINAL_SOCKET=/x", "HC_SCOPE=s"}}
	if err := c.Send(context.Background(), "s", "t1", "- a list item", false); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, out); strings.Join(got, "|") != "send|t1|--|- a list item" {
		t.Errorf("args = %v", got)
	}
	if err := c.Send(context.Background(), "s", "t1", "--force", true); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, out); strings.Join(got, "|") != "send|--force|t1|--|--force" {
		t.Errorf("force args = %v", got)
	}

	// bot 自身终端的变量绝不抵达 hc，HC_SESSION 指名 session。
	if err := c.Send(context.Background(), "my-session", "t1", "hi", false); err != nil {
		t.Fatal(err)
	}
	envScript := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + quote(out) + "\nenv | grep -E '^(HC_SESSION|HC_TERMINAL)' | sort > " + quote(filepath.Join(dir, "env")) + "\n"
	//nolint:gosec // G306: 测试 stub 脚本需要可执行位。
	if err := os.WriteFile(stub, []byte(envScript), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = c.Send(context.Background(), "my-session", "t1", "hi", false)
	env := readLines(t, filepath.Join(dir, "env"))
	if strings.Join(env, "|") != "HC_SESSION=my-session" {
		t.Errorf("env = %v, want only HC_SESSION=my-session", env)
	}
}

func TestEventsFollowSnapshotTurnAndDedupe(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "hc")
	script := "#!/bin/sh\ncat <<'JSONL'\n" +
		`{"seq":5,"terminals":[{"address":"t1","status":{"state":"working","app":"claude-code"},"labels":{"where":"api / claude"},"title":"api"}]}` + "\n" +
		`{"method":"event","params":{"seq":6,"type":"turn","terminal":"t1","data":{"state":"blocked","turn":2}}}` + "\n" +
		`{"method":"event","params":{"seq":6,"type":"status","terminal":"t1","data":{"status":{"state":"blocked"}}}}` + "\n" + // same seq: dropped
		`{"method":"event","params":{"seq":7,"type":"labels","terminal":"t1","data":{"labels":{"where":"api / claude","herdrchat.ask":"1731500000-99"}}}}` + "\n" +
		`{"method":"heartbeat","params":{"seq":8}}` + "\n" +
		"JSONL\nsleep 30\n"

	//nolint:gosec // G306: 测试 stub 脚本需要可执行位。
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var turns []TurnEvent
	var changes []ChangeEvent
	var snapshots int

	view := NewView()
	c := &CLI{Bin: stub, Env: nil}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.FollowEvents(ctx, "s", EventHandlers{
		OnTurn: func(e TurnEvent) {
			mu.Lock()
			turns = append(turns, e)
			mu.Unlock()
		},
		OnChange: func(e ChangeEvent) {
			mu.Lock()
			changes = append(changes, e)
			mu.Unlock()
		},
		OnSnapshot: func([]Terminal) {
			mu.Lock()
			snapshots++
			mu.Unlock()
		},
	}, view)

	select {
	case <-view.SnapshotDone():
	case <-time.After(5 * time.Second):
		t.Fatal("no first snapshot")
	}

	waitFor(t, time.Second, "a turn event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(turns) == 1 && turns[0].State == "blocked" && turns[0].Address == "t1"
	})
	waitFor(t, time.Second, "the labels change (seq 7) but not the status (seq 6 was seen)", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(changes) == 1 && changes[0].Type == "labels"
	})

	mu.Lock()
	defer mu.Unlock()

	if snapshots != 1 {
		t.Errorf("snapshots = %d, want 1", snapshots)
	}
	rec, ok := view.Get("t1")
	if !ok || rec.State() != "working" || rec.App() != "claude-code" || rec.Where() != "api / claude" {
		t.Errorf("record = %v", rec)
	}

	// turn 事件设置了 record.turn；labels 事件 ( 更新的 seq ) 保留了记录。
	// 并加上 herdrchat.ask。
	if rec.Label("herdrchat.ask") != "1731500000-99" {
		t.Errorf("herdrchat.ask = %q", rec.Label("herdrchat.ask"))
	}
	if turn, _ := rec["turn"].(float64); turn != 2 {
		t.Errorf("record.turn = %v, want 2", rec["turn"])
	}
}

func TestEventsRestartAfterEnding(t *testing.T) {
	// 事件流立刻结束；
	// 3 秒后重启，第二轮的快照到达 ( "missed" 也会重放一份 )。
	dir := t.TempDir()
	stub := filepath.Join(dir, "hc")
	script := "#!/bin/sh\nif [ -f " + quote(filepath.Join(dir, "ran")) + " ]; then echo '{\"seq\":1,\"terminals\":[]}'; sleep 30; exit 0; fi\ntouch " + quote(filepath.Join(dir, "ran")) + "\nexit 1\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil { //nolint:gosec // G306: 测试 stub 脚本需要可执行位
		t.Fatal(err)
	}
	view := NewView()
	c := &CLI{Bin: stub, Env: nil}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.FollowEvents(ctx, "s", EventHandlers{}, view)
	select {
	case <-view.SnapshotDone():
	case <-time.After(10 * time.Second): // 3s restart + slack t.Fatal("the stream was not restarted after it ended")
	}
}

func TestProbeCapabilitiesOnStub(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "hc")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'hc 0.9.2'; exit 0; fi\nif [ \"$2\" = \"attach\" ]; then echo 'usage: hc terminal attach <address>'; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil { //nolint:gosec // G306: 测试 stub 脚本需要可执行位
		t.Fatal(err)
	}
	c := &CLI{Bin: stub}
	caps := c.ProbeCapabilities(context.Background())
	if caps.HCVersion != "0.9.2" || !caps.HasTerminalAttach {
		t.Errorf("caps = %+v", caps)
	}
	// 没有 hc 的机器什么都答不上来：按最低能力对待，绝不是错误。
	missing := &CLI{Bin: filepath.Join(dir, "nope")}
	caps = missing.ProbeCapabilities(context.Background())
	if caps.HCVersion != "" || caps.HasTerminalAttach {
		t.Errorf("caps on failure = %+v, want zero values", caps)
	}
}

// --- 辅助函数 ------------------------------------------------------------
func quote(p string) string {
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

func readLines(t *testing.T, file string) []string {
	t.Helper()
	//nolint:gosec // G304: 测试临时目录内的文件。
	text, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(text), "\n"), "\n")
}

func waitFor(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// 静默看门狗：流上长时间一行都没有 ( 心跳 ~17s 一条 ) 时杀掉子进程，
// eventsOnce 返回、由 FollowEvents 的重启循环重连。
func TestEventsQuietWatchdogKillsStuckChild(t *testing.T) {
	dir := t.TempDir()
	// stub hc：打印一行快照，然后永远沉默 ( 不退出、不再输出 )。
	stub := filepath.Join(dir, "hc")
	script := "#!/bin/sh\nif [ \"$1\" = events ]; then echo x >> \"$0.runs\"; echo '{\"seq\":1,\"terminals\":[]}'; sleep 300; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil { //nolint:gosec // G306: 测试 stub 需要可执行位
		t.Fatal(err)
	}
	c := &CLI{Bin: stub, Env: nil}
	view := NewView()
	h := EventHandlers{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.FollowEvents(ctx, "default", h, view); close(done) }()
	// 看门狗窗口 + 重启延迟以内必须循环回来 ( 若无看门狗会挂到 sleep 300 )。
	deadline := time.After(eventsQuiet + EventsRestartDelay + 15*time.Second)
	tick := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("watchdog did not recycle the silent stream in time")
		case <-tick:
			tick = time.After(2 * time.Second)
		}
		// 复活过的证据：view 被第二次快照刷新过 ( stub 每次启动都发同一快照，
		// 重启计数拿不到；用进程存在性+完成信号都不够直接——这里改为：看
		// stub 是否被再次拉起 )。
		if runs := spawnCount(t, filepath.Join(dir, "hc.runs")); runs >= 2 {
			cancel()
			return
		}
	}
}

// spawnCount 数 stub 被执行的次数 ( 每次执行追加一个 x )。
func spawnCount(t *testing.T, file string) int {
	t.Helper()
	data, err := os.ReadFile(file) //nolint:gosec // G304: 测试自己写的计数文件
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range data {
		if c == 'x' {
			n++
		}
	}
	return n
}
