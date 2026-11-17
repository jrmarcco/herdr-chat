package hccli

import (
	"bufio"
	"context"
	"encoding/json"
	"maps"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// Terminal 是 hc 报告的一个终端记录
// ( 原始 JSON 对象：address、status{state,app}、labels{where,herdr.space,herdr.tab,herdrchat.ask}、title、pid、foreground、turn… )。
// 字段的含义始终归 hc；
// 下面的访问器只读引擎跟踪的那几个。
type Terminal map[string]any

func (t Terminal) str(path ...string) string {
	var cur any = map[string]any(t)
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[key]
	}
	s, _ := cur.(string)
	return s
}

// Address 是终端地址 ( tN，或 machine/tN )。
func (t Terminal) Address() string { return t.str("address") }

// State 是程序的状态词：working、blocked、done、idle、error…
func (t Terminal) State() string { return t.str("status", "state") }

// App 是程序在其 status 里的自称 ( "claude-code"、"codex"… )。
func (t Terminal) App() string { return t.str("status", "app") }

// Labels 是终端的标签；Where 是 herdr-app 的 "<space> / <tab>"。
func (t Terminal) Labels() map[string]string {
	out := map[string]string{}
	if m, ok := t["labels"].(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

func (t Terminal) Label(key string) string { return t.str("labels", key) }

// Where 即 labels.where，herdr-app 给每个终端的 "<space> / <tab>"。
func (t Terminal) Where() string { return t.Label("where") }

// Title 是终端标题 ( agent 会把自己的会话名写进去 )。
func (t Terminal) Title() string { return t.str("title") }

// TurnEvent 在程序状态每次离开 working 时触发；State 是它停驻的词：
// done/idle/error ( turn 结束 ) 或 blocked ( 需要人 )。
type TurnEvent struct {
	Address string
	State   string
	Record  Terminal
}

// ChangeEvent 是事件流携带的其余事件。
type ChangeEvent struct {
	Type    string
	Address string
	Record  Terminal
	Data    map[string]any
}

// EventHandlers 是引擎想从一个 session 的事件流里听到的东西。
type EventHandlers struct {
	OnTurn     func(TurnEvent)
	OnChange   func(ChangeEvent)
	OnSnapshot func([]Terminal)
}

// EventsRestartDelay 是事件流进程结束后、再次拉起前的等待时长。
const EventsRestartDelay = 3 * time.Second

// eventsMaxLine 是扫描器的单行上限：终端很多时首行快照可达 MB 级
// ( 显式设限：无上限的快照行会吃内存 )。
const eventsMaxLine = 10 << 20

// eventsQuiet 是静默看门狗的窗口：超过它没有任何一行 ( 心跳 ~17s 一条 )，
// 视为这条订阅连接已被核心遗忘，杀掉重连。
const eventsQuiet = 45 * time.Second

// View 是一个 session 的终端实况集合，由其事件流保持更新。并发安全。
type View struct {
	mu        sync.Mutex
	terminals map[string]Terminal
	snapshot  chan struct{}
}

// NewView 建一个尚未收到首个快照的空 view。
func NewView() *View {
	return &View{terminals: map[string]Terminal{}, snapshot: make(chan struct{})}
}

// SnapshotDone 在首个快照被应用后关闭；每个 session 的快照都到齐之前，
// 引擎不派发任何 lane。
func (v *View) SnapshotDone() <-chan struct{} { return v.snapshot }

// MarkSnapshotDone 手动关闭首快照闸门，供没有真实进程可依赖的 fake 使用。
// 重复调用无副作用。
func (v *View) MarkSnapshotDone() {
	select {
	case <-v.snapshot:
	default:
		close(v.snapshot)
	}
}

// Get fetches one terminal's current record.
func (v *View) Get(address string) (Terminal, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	t, ok := v.terminals[address]
	return t, ok
}

// All 列出事件流当前知道的每个终端。
func (v *View) All() []Terminal {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]Terminal, 0, len(v.terminals))
	for _, t := range v.terminals {
		out = append(out, t)
	}
	return out
}

// Upsert 放入或替换一条终端记录 ( created/restarted/status/labels 等 )。
func (v *View) Upsert(t Terminal) {
	if a := t.Address(); a != "" {
		v.mu.Lock()
		v.terminals[a] = t
		v.mu.Unlock()
	}
}

// Delete 拿掉一个终端 ( removed 事件 )；不在也不报错。
func (v *View) Delete(address string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.terminals, address)
}

// ApplySnapshot 用一份快照替换 view 中的全部，即事件流首行与每个 `missed` 携带的内容。
// 真实跟随器与 fake 共用它，两者以同一方式维护 view。
func (v *View) ApplySnapshot(list []Terminal) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.terminals = map[string]Terminal{}
	for _, t := range list {
		if a := t.Address(); a != "" {
			v.terminals[a] = t
		}
	}
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asTerminals(v any) []Terminal {
	list, _ := v.([]any)
	out := make([]Terminal, 0, len(list))
	for _, item := range list {
		if m := asMap(item); m != nil {
			out = append(out, Terminal(m))
		}
	}
	return out
}

// FollowEvents 为一个 session ( 本机与已连接机器 ) 跟随 `hc events --json` 直到 ctx 结束，并保持 view 更新。
//
// 格式：
// 首行是快照 {seq, terminals}；
// 之后是 JSON-RPC 通知：event {seq, type, terminal, data}、heartbeat {seq}、missed {seq, terminals} ( 一份新快照 )。
// 进程结束后 3 秒重启。
func (c *CLI) FollowEvents(ctx context.Context, session string, h EventHandlers, view *View) {
	if view == nil {
		view = NewView()
	}
	go func() {
		for ctx.Err() == nil {
			c.eventsOnce(ctx, session, h, view)
			if ctx.Err() != nil {
				return
			}
			xlog.For("hccli").Warn("事件流结束，稍后重启", "delay_s", EventsRestartDelay.Seconds())
			select {
			case <-ctx.Done():
				return
			case <-time.After(EventsRestartDelay):
			}
		}
	}()
}

func (c *CLI) eventsOnce(ctx context.Context, session string, h EventHandlers, view *View) {
	// hc 的路径来自本机配置 ( Bin / HERDRCHAT_HC / PATH )，是运维自己指定的，非不可信输入。
	//nolint:gosec // G204: hc 路径来自本机配置，非不可信输入。
	cmd := exec.CommandContext(ctx, c.bin(), "events", "--json")
	cmd.Env = c.envFor(session)

	// 独立进程组 + WaitDelay：
	// 杀掉时要连孙进程一起 ( 孤儿握着 stderr 管道会把Wait 卡成死等 )；
	// 进程退出后管道未关时 WaitDelay 强制回收。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 3 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		xlog.For("hccli").Warn("事件流 stdout 管道建立失败", "error", err)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		xlog.For("hccli").Warn("事件流 stderr 管道建立失败", "error", err)
		return
	}
	if err := cmd.Start(); err != nil {
		xlog.For("hccli").Warn("事件流进程启动失败", "error", err)
		return
	}
	var wg sync.WaitGroup
	// 事件流自己的抱怨，逐行记录。
	wg.Go(func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); line != "" {
				xlog.For("hccli").Warn("事件流 stderr", "text", line)
			}
		}
		if err := sc.Err(); err != nil {
			xlog.For("hccli").Warn("事件流 stderr 读取失败", "error", err)
		}
	})

	first := true
	var seq float64
	snapshot := func(list []Terminal, at float64) {
		view.ApplySnapshot(list)
		seq = at
		view.MarkSnapshotDone()
		if h.OnSnapshot != nil {
			h.OnSnapshot(view.All())
		}
	}
	apply := func(params map[string]any) {
		at, _ := params["seq"].(float64)
		// 已被更新的快照覆盖。
		if at <= seq {
			return
		}
		seq = at
		name, _ := params["terminal"].(string)
		typ, _ := params["type"].(string)
		data := asMap(params["data"])
		view.mu.Lock()
		record := view.terminals[name]
		switch typ {
		case "created", "restarted":
			view.terminals[name] = Terminal(data)
		case "removed":
			// 摘掉之前留好这份记录并按已结束盖印：
			// 回调 ( postEnd 的遗言 ) 还要用它，且它不再是 live，
			// 谁也不会再为它建房间。
			if record != nil {
				record["state"] = "finished"
				record["foreground"] = nil
			}
			delete(view.terminals, name)
		case kindFinished:
			maps.Copy(record, data)
			record["state"] = "finished"
			record["foreground"] = nil
			view.terminals[name] = record
		case "lost":
			record["pid"] = nil
			record["foreground"] = nil
			view.terminals[name] = record
		case "status":
			maps.Copy(record, data)
			view.terminals[name] = record
		case "labels":
			if r := asMap(data["record"]); r != nil {
				view.terminals[name] = Terminal(r)
			} else if record != nil {
				record["labels"] = asMap(data["labels"])
				view.terminals[name] = record
			}
		case "foreground":
			record["foreground"] = data
			view.terminals[name] = record
		case "turn":
			if record != nil {
				turn := asMap(params["data"])
				record["turn"] = turn["turn"]
				view.terminals[name] = record
			}
		default:
			// title、size、keep：直接并入。
			maps.Copy(record, data)
			if record != nil {
				view.terminals[name] = record
			}
		}
		current := view.terminals[name]
		if typ == "removed" {
			current = record // 摘掉之前的它：回调要拿着完整的遗言记录
		}
		view.mu.Unlock()
		if typ == "turn" {
			state, _ := data["state"].(string)
			if h.OnTurn != nil {
				h.OnTurn(TurnEvent{Address: name, State: state, Record: current})
			}
		} else if h.OnChange != nil {
			h.OnChange(ChangeEvent{Type: typ, Address: name, Record: current, Data: data})
		}
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), eventsMaxLine)

	// 静默看门狗：hc 的流每 ~17s 一条心跳，事件更密。
	// 长连订阅者偶发 "拿到快照后核心不再向它投递" ( 连接开着、进程活着、什么也收不到，手动新订阅者一切正常，见上游排查记录 )，
	// 进程不死循环也就不重启，引擎从此全聋。任何行 ( 事件/心跳/快照 ) 都算活着；
	// 静默超过 eventsQuiet 就杀掉子进程，让既有的重启循环带一条新连接回来。
	lastLine := time.Now()
	lineSeen := make(chan struct{}, 1)
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	go func() {
		t := time.NewTimer(eventsQuiet)
		defer t.Stop()
		for {
			select {
			case <-watchdogDone:
				return
			case <-lineSeen:
				lastLine = time.Now()
				t.Reset(eventsQuiet)
			case <-t.C:
				if time.Since(lastLine) >= eventsQuiet {
					xlog.For("hccli").Warn("事件流静默过久，杀掉重连", "session", session, "quiet", eventsQuiet.String())
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					return
				}
				t.Reset(eventsQuiet)
			}
		}
	}()
	notifyLine := func() {
		select {
		case lineSeen <- struct{}{}:
		default:
		}
	}

	for sc.Scan() {
		notifyLine()
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			xlog.For("hccli").Warn("事件流行不可解析", "line", truncate(line, 120))
			continue
		}
		method, _ := msg["method"].(string)
		params := asMap(msg["params"])
		if first {
			first = false
			snapshot(asTerminals(msg["terminals"]), numOr(msg["seq"], 0))
			view.mu.Lock()
			n := len(view.terminals)
			view.mu.Unlock()
			xlog.For("hccli").Info("开始跟随 hc 会话", "session", session, "terminals", n)
			continue
		}
		switch method {
		case "event":
			apply(params)
		case "missed":
			snapshot(asTerminals(params["terminals"]), numOr(params["seq"], 0))
		}
	}
	// 非正常结束 ( 行超长等 )：说一声，让 FollowEvents 的重启循环接手。
	if err := sc.Err(); err != nil {
		xlog.For("hccli").Warn("事件流读取失败，稍后重启", "session", session, "error", err)
	}
	wg.Wait()
	_ = cmd.Wait()
}

func numOr(v any, fallback float64) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return fallback
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Live 说终端记录是否处于 live 状态
// ( hc 的快照与 created/restarted 记录带它；finished/removed 之后不是 )。
func (t Terminal) Live() bool {
	return t.str("state") == "live"
}
