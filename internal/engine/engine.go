package engine

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/asks"
	"github.com/jrmarcco/herdr-chat/internal/chats"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/readers"
	"github.com/jrmarcco/herdr-chat/internal/state"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// 引擎：编排房间、发帖、入口与总览，把 hc 的事件流接到聊天侧。
//
// 并发规则：
// 一个终端 key 一条串行 lane，它对房间、游标、卡片的一切按序发生，不同终端互不等待；
// 所有 session 的首个事件快照与首次 cores 读取完成之前，不派发任何 lane，
// 远端机器的终端才能始终由同一个 session 认领。
// 定时器与等待的节拍。测试用 Quick 把等待压到毫秒级。

const (
	LiveEvery   = 5 * time.Second
	SweepEvery  = 60 * time.Second
	PostTries   = 5
	EndTries    = 20
	EndWait     = 750 * time.Millisecond
	SweepDone   = 30 * time.Second
	ScreenLines = 40
	TurnScreens = 150
	TitleMax    = 40
	FileBytes   = 20 << 20
	ShellEcho   = 1500 * time.Millisecond
	StopDrain   = 5 * time.Second // 优雅退出的排空上限
)

// hc 的状态词。
const (
	stateBlocked = "blocked" // 需要人
	stateWorking = "working" // 干着活
)

// 点击值：Esc 与多选的 Next。
const (
	pickEsc = "esc"
	pickTab = "tab"
)

// 卡片标题常量 ( 与原版逐字一致，聊天里认得它们 )。
const (
	LiveTitle   = "⌛️ Working…"
	FullTitle   = "⌛️ Working… (continued below)"
	ClosedTitle = "✅ Worked"
	EndedReply  = "Not delivered: this terminal has ended. `/new` starts one in its place."
	NeedsTitle  = "⚠️ Needs you"
	DoneTitle   = "✔ Answered."
)

// Engine 是一次运行的全部状态。字段在 Start 前填好 ( 依赖注入，测试全用 fake )。
type Engine struct {
	mu sync.Mutex // 保护跨 lane 共享的内存状态

	Chat     chats.Chat
	HC       core.HC
	Readers  *readers.Registry
	Store    state.Store
	Sessions []string
	History  map[string]bool // 回填的形态：file / folded
	Inbox    string          // 收到的图片落地的目录
	// DefaultSpace 是 /new 无兄弟 space 可承继时的 space 名 ( 装配处注入；空 = dev )。
	DefaultSpace string
	HomeDir      string            // 本机 home ( asks 协议的文件都在这；测试注入 )
	Env          map[string]string // 进程环境 ( XDG 目录定位用；装配处注入 )
	StartedAt    time.Time

	// Version 是本进程的版本；Latest 问最新 release ( 装配处注入；nil 即不 // 做新版提示 )。
	Version string
	Latest  func(ctx context.Context) (string, error)

	// SetOwner 记下配对成功的 owner ( 写进 config.env；装配处注入 )。
	SetOwner func(ownerID string) error
	// SetSessions 改写跟随的 HERDR_SESSIONS ( /sessions add|drop；装配处注入 )。
	SetSessions func([]string) error
	// Restart 在配对成功后让进程让位给新 owner 的实例
	// ( 装配处注入：退出，由服务或终端循环拉起 )。
	Restart func()

	// ManualTimers 不起 live/sweep/asks 定时器，测试自己 tick。
	// Quick 模式下 10ms 的定时器与断言抢时序，是旧测试抖动的根 ( 测试用 )。
	ManualTimers bool

	// Quick 把重试退避、终答等待与定时器间隔压到毫秒级 ( 测试用 )。
	Quick bool
	// Sleep 可替换的等待函数 ( 测试注入 )。
	Sleep func(time.Duration)

	ctx    context.Context
	cancel context.CancelFunc
	lanes  *core.Lanes
	asks   *asks.Asks
	asking map[string]bool

	// postedDialog: 终端 key -> 最后发过的对话框名 ( 一题一卡 )；
	// dialogCards: 消息 -> 它展示的对话框 ( 点击按消息找回 )。
	postedDialog map[string]string
	dialogCards  map[core.Message]core.Dialog
	lastAsk      map[string]string   // 终端 key -> 上次 herdrchat.ask 的请求 id
	branches     map[string]branchAt // 终端 key -> git 分支 ( 15s 缓存 )
	commands     *CommandCatalog     // 斜杠命令目录 ( 懒建 )

	views       map[string]*hccli.View
	topology    *core.CoreViews
	snapSeen    map[string]bool
	firstSnap   chan struct{}
	fromChat    map[string][]string // 终端 key -> 你最近发过的文本 ( 近 20 条 )
	unanswered  map[string]bool     // 你发过消息、其记录尚无回应的终端
	uploaded    map[string]string   // 图片条目 id -> 已上传的 markdown
	uploadFail  map[string]int
	liveBusy    map[string]bool
	since       map[string]time.Time
	lastState   map[string]string
	overviewDue *time.Timer
	timerStop   []chan struct{}
	stopping    bool

	inFlight atomic.Int64
}

// 真实 hc 适配器满足引擎对 hc 的全部要求 ( fake 的等价断言在 internal/fake )。
var _ core.HC = (*hccli.CLI)(nil)

// New 装配一个引擎；Start 之前还需填好依赖字段。
func New(chat chats.Chat, hc core.HC, reg *readers.Registry, store state.Store, sessions []string) *Engine {
	e := &Engine{
		Chat: chat, HC: hc, Readers: reg, Store: store, Sessions: sessions,
		History:      map[string]bool{"file": true, "folded": true},
		StartedAt:    time.Now(),
		lanes:        core.NewLanes(),
		asking:       map[string]bool{},
		postedDialog: map[string]string{},
		dialogCards:  map[core.Message]core.Dialog{},
		lastAsk:      map[string]string{},
		branches:     map[string]branchAt{},
		views:        map[string]*hccli.View{},
		snapSeen:     map[string]bool{},
		firstSnap:    make(chan struct{}),
		fromChat:     map[string][]string{},
		unanswered:   map[string]bool{},
		uploaded:     map[string]string{},
		uploadFail:   map[string]int{},
		liveBusy:     map[string]bool{},
		since:        map[string]time.Time{},
		lastState:    map[string]string{},
	}
	if e.HomeDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			e.HomeDir = home
		} else {
			e.HomeDir = "."
		}
	}
	e.asks = asks.New(hc, e.HomeDir)
	e.Sleep = time.Sleep
	return e
}

func (e *Engine) sleep(d time.Duration) {
	if e.Quick {
		d = time.Millisecond
	}
	e.Sleep(d)
}

func (e *Engine) every(d time.Duration) time.Duration {
	if e.Quick {
		// 1ms 在并行测试下竞态面太大；10ms 依然够快。
		return 10 * time.Millisecond
	}
	return d
}

// read/write：每次都过 Store，lane 的串行保证同一终端不会写穿彼此
// ( 与原版 "每次跨 await 重读 state.json" 同一纪律 )。
func (e *Engine) read() state.State {
	st, err := e.Store.Read()
	if err != nil {
		xlog.For("engine").Warn("state 读取失败", "error", err)
		return state.Empty()
	}
	return st
}

func (e *Engine) write(st state.State) {
	if err := e.Store.Write(st); err != nil {
		xlog.For("engine").Error("state 写入失败", "error", err)
	}
}

// later 把一步工作排进一个终端的 lane：串行保序；错误按 lane 规则消化
// ( 机器不可达 → 记挂起；其余 ERROR 日志 )。
func (e *Engine) later(session, address, label string, fn func() error) {
	k := e.keyOf(session, address)
	e.lanes.Go(k, func() error {
		if e.ctx != nil {
			select {
			case <-e.ctx.Done():
				return nil // 正在退出：不再派发
			default:
			}
		}
		e.inFlight.Add(1)
		defer e.inFlight.Add(-1)
		if err := fn(); err != nil {
			if hccli.IsUnavailable(err) && hccli.MachineOf(address) != "" {
				e.machineDown(session, hccli.MachineOf(address))
				return nil
			}
			xlog.For("engine").Error("lane 步骤失败", "label", label, "session", session, "address", address, "error", err)
		}
		return nil
	})
}

// machineDown 记一台够不到的机器：
// 房间挂起 ( endGone 的 reachable 检查会放过它 )，提示只说一次。
func (e *Engine) machineDown(session, machine string) {
	xlog.For("engine").Warn("机器暂时不可达，房间挂起等待", "session", session, "machine", machine)
}

// Start 接线并起泵：cores 并行读、chat 启动、每个 session 跟随事件流；
// 全部首快照到齐后补一次 endGone 与暂存消息，然后是 live/sweep 定时器。
func (e *Engine) Start(parent context.Context, onReady func()) error {
	e.ctx, e.cancel = context.WithCancel(parent)
	log := xlog.For("engine")

	log.Info("herdrchat 启动", "chat", e.Chat.Name, "sessions", e.Sessions)
	if !e.Chat.Ready() {
		log.Info("owner 未设置：在它配对之前不建任何房间")
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); e.readCores() }()

	if err := e.Chat.Start(e.ctx, chats.Handlers{
		OnMessage: e.onMessage,
		OnAction:  e.onAction,
		OnReady:   onReady,
	}); err != nil {
		e.cancel()
		return err
	}

	snapWG := &sync.WaitGroup{}
	for _, session := range e.Sessions {
		snapWG.Add(1)
		go func() { <-e.firstSnap; snapWG.Done() }()
		e.watch(session)
	}
	go func() {
		wg.Wait()     // cores 到位 ( handles 需要拓扑 )
		snapWG.Wait() // 每个 session 的首快照到齐
		e.afterFirstSnapshot()
	}()

	e.restoreDialogs()
	if !e.ManualTimers {
		e.startTimer(e.every(LiveEvery), e.liveTick)
		e.startTimer(e.every(SweepEvery), e.sweepTick)
		e.startTimer(e.every(everyAsks), e.asksTick)
		e.startUpdateWatch()
	}
	return nil
}

// everyAsks 是扫本机 asks 目录的周期 ( 2s；目录 readdir，开销极小 )。
const everyAsks = 2 * time.Second

// asksTick：本机有请求在等、或有悬空标签时，扫一遍认领与回收。
func (e *Engine) asksTick() {
	if e.ctx == nil || e.ctx.Err() != nil {
		return
	}
	for _, session := range e.Sessions {
		if e.asks.AsksHere() || e.labelsOut(session) {
			e.claimAsks(session, false)
		}
	}
}

// labelsOut：本 session 是否有挂着 herdrchat.ask 标签的本机终端。
func (e *Engine) labelsOut(session string) bool {
	for _, tr := range e.readable(session) {
		if asks.AskOf(tr.record) != "" && hccli.MachineOf(tr.record.Address()) == "" {
			return true
		}
	}
	return false
}

// ownDialogOpen：该终端是否有一张 agent 上报的卡在等。
func (e *Engine) ownDialogOpen(k string) bool {
	oc, ok := e.openCardOf(k)
	if !ok {
		return false
	}
	var dialog core.Dialog
	if json.Unmarshal(oc.Dialog, &dialog) != nil {
		return false
	}
	return dialog.Menu != nil && dialog.Menu.Own != nil
}

// Stop 停泵并优雅退出：停定时器、断事件流、排空在飞的 lane ( ≤StopDrain )。
func (e *Engine) Stop() {
	e.mu.Lock()
	e.stopping = true
	stops := e.timerStop
	e.timerStop = nil
	if e.overviewDue != nil {
		e.overviewDue.Stop()
	}
	e.mu.Unlock()
	for _, stop := range stops {
		close(stop)
	}
	if e.cancel != nil {
		e.cancel()
	}
	deadline := time.Now().Add(StopDrain)
	for e.inFlight.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *Engine) startTimer(every time.Duration, fn func()) {
	stop := make(chan struct{})
	go func() {
		t := time.NewTimer(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if e.ctx != nil && e.ctx.Err() != nil {
					return
				}
				fn()
				t.Reset(every)
			}
		}
	}()
	e.mu.Lock()
	e.timerStop = append(e.timerStop, stop)
	e.mu.Unlock()
}

// afterFirstSnapshot：首快照与 cores 都到齐后的一次性收尾。
func (e *Engine) afterFirstSnapshot() {
	e.endGone()
	// 重启前暂存的消息：终端已不再等待的，现在送进去。
	st := e.read()
	for k := range st.Waiting {
		address := AddressOfKey(k)
		if !e.anyBlocked(address) {
			e.sendWaiting(k)
		}
	}
}

func (e *Engine) anyBlocked(address string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.Sessions {
		if v := e.views[s]; v != nil {
			if t, ok := v.Get(address); ok && t.State() == stateBlocked {
				return true
			}
		}
	}
	return false
}

// readCores 刷新拓扑：每个 session 的 core 与它连到的 core。
func (e *Engine) readCores() {
	cores := map[string]core.CoreInfo{}
	for _, session := range e.Sessions {
		self, links, err := e.HC.Cores(e.ctx, session)
		if err != nil {
			xlog.For("engine").Warn("读取 session 拓扑失败", "session", session, "error", err)
			continue
		}
		cores[session] = core.CoreInfo{Self: self, Links: links}
	}

	e.mu.Lock()
	e.topology = core.CoreViewsOf(e.Sessions, cores)
	e.mu.Unlock()
}

// handles：这个 session 是否就是该终端的跟随者 ( 拓扑未知时的回退：
// 本机地址是自己的；机器地址归第一个看到它 live 的 session )。
func (e *Engine) handles(session, address string) bool {
	e.mu.Lock()
	topology := e.topology
	e.mu.Unlock()
	if topology != nil {
		if owned, known := topology.Owns(session, address); known {
			return owned
		}
	}
	if hccli.MachineOf(address) == "" {
		return true
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	for _, s := range e.Sessions {
		if v := e.views[s]; v != nil {
			if t, ok := v.Get(address); ok && t.Live() {
				return s == session
			}
		}
	}
	return false
}

// watch 接线一个 session 的事件流。
func (e *Engine) watch(session string) {
	view := hccli.NewView()

	e.mu.Lock()
	e.views[session] = view
	e.mu.Unlock()

	e.HC.FollowEvents(e.ctx, session, hccli.EventHandlers{
		OnSnapshot: func(list []hccli.Terminal) {
			e.mu.Lock()
			if !e.snapSeen[session] {
				e.snapSeen[session] = true
				if len(e.snapSeen) == len(e.Sessions) {
					close(e.firstSnap) // 每个 session 的首快照都到齐了
				}
			}

			e.mu.Unlock()
			e.scheduleOverview()

			for _, record := range list {
				r := record
				if whereShown(r) {
					e.later(session, r.Address(), "room", func() error {
						_, err := e.ensureRoom(session, r)
						return err
					})
				}
			}
			go e.sweep(session) // 等 view 里已有本 session 的记录之后
		},
		OnChange: func(ev hccli.ChangeEvent) { e.onChange(session, ev) },
		OnTurn:   func(ev hccli.TurnEvent) { e.onTurn(session, ev) },
	}, view)
}

// onChange：房间触发、结束触发与状态簿记 ( 对话框的 blocked 分支属 M3 )。
func (e *Engine) onChange(session string, ev hccli.ChangeEvent) {
	e.scheduleOverview()
	if ev.Address == "" {
		return
	}
	record := ev.Record
	roomIfShown := func() {
		// snapshot / created|restarted|labels|status|title 且带 where 标签 → ensureRoom。
		// /new 的终端带着标签出生，没有后续 labels事件，created 与 status 不触发的话，房间永远等不来。
		if whereShown(record) {
			r := record
			e.later(session, ev.Address, "room", func() error {
				_, err := e.ensureRoom(session, r)
				return err
			})
		}
	}

	switch ev.Type {
	case "status":
		e.noteState(session, ev.Address, record)
		roomIfShown()
	case "created", "restarted", "title":
		roomIfShown()
	case "labels":
		// 标签事件两头兼顾：先让 hook 的卡片走起来，房间照常跟。
		e.onAsk(session, ev.Address, record)
		roomIfShown()
	case "finished", "removed":
		e.later(session, ev.Address, "end", func() error {
			return e.postEnd(session, ev.Address, record, reasonOf(ev))
		})
	}
}

// noteState 维护 since ( 状态何时变的，总览用 )；离开 blocked 时送出暂存消息。
func (e *Engine) noteState(session, address string, record hccli.Terminal) {
	k := e.keyOf(session, address)
	now := record.State()

	e.mu.Lock()
	was := e.lastState[k]
	e.lastState[k] = now
	if now != was {
		e.since[k] = time.Now()
	}
	e.mu.Unlock()

	if now != stateBlocked {
		if was == stateBlocked {
			e.later(session, address, "held", func() error {
				e.sendWaiting(k)
				return nil
			})
		}
		// 它的 hook 还在问 ( Codex 一边干活一边等 )：卡留着，消息也留着。
		if asks.AskOf(record) != "" && e.ownDialogOpen(k) {
			return
		}
		e.mu.Lock()
		delete(e.postedDialog, k)
		e.mu.Unlock()
		e.later(session, address, "dialog", func() error {
			e.closeCard(k, DoneTitle)
			return nil
		})
		return
	}

	// 从 working 直接进 blocked 的归 turn 事件 ( 先发它说的话 )；
	// 其余的 blocked ( 一个对话框直接进下一个 ) 在这里发卡。
	if was == stateWorking || !whereShown(record) || !e.handles(session, address) || e.agentOf(record) == nil {
		return
	}

	rec := record
	e.later(session, address, "dialog", func() error {
		// 卡片上手时对话框可能已答掉，postBlocked 里会再核一次状态。
		return e.postBlocked(session, address, rec, false, false)
	})
}

func reasonOf(ev hccli.ChangeEvent) string {
	if ev.Data == nil {
		return ""
	}
	reason, _ := ev.Data["reason"].(string)
	return reason
}

// onTurn 一个 turn 离开了 working。
// blocked ( 需要人 ) 属 M3 的对话框状态机，
// 这里先把它说过的话发出去；turn 结束则等终答、发正文、补回执。
func (e *Engine) onTurn(session string, ev hccli.TurnEvent) {
	e.scheduleOverview()
	record := ev.Record
	if !whereShown(record) || !e.handles(session, ev.Address) {
		return
	}

	reader := e.agentOf(record)
	if reader == nil {
		return // 房间只镜像 agent：shell 之类自己不会说话
	}

	stateWord := ev.State
	e.later(session, ev.Address, "turn", func() error {
		switch stateWord {
		case stateBlocked:
			// 停下来提问之前说过的话先发，然后是提问的卡。
			_ = e.postRead(session, ev.Address, record, reader, false, false)
			return e.postBlocked(session, ev.Address, record, false, false)
		default:
			e.postTurn(session, ev.Address, record, reader)
			e.markDone(e.keyOf(session, ev.Address), 0)
			// turn 到头：它开着的提问卡不再有人等，收掉。
			// 不依赖 hc 的 status 事件，"离开 blocked" 那条事件并不总会来，
			// turn 的 done 是确定性的收口点。
			e.closeCard(e.keyOf(session, ev.Address), DoneTitle)
			return nil
		}
	})
}

// agentOf 按 hc 记录挑 reader ( 前台进程名校验，防状态残留误判 )。
func (e *Engine) agentOf(record hccli.Terminal) readers.Reader {
	if record == nil {
		return nil
	}
	return readers.AgentOf(e.Readers, record.App(), foregroundName(record))
}

func foregroundName(record hccli.Terminal) string {
	fg, _ := record["foreground"].(map[string]any)
	if fg == nil {
		return ""
	}
	name, _ := fg["name"].(string)
	return name
}

// readable 列出一个 session 里"跟得住"的终端：live、被本 session 跟随、有 reader。
func (e *Engine) readable(session string) []struct {
	record hccli.Terminal
	reader readers.Reader
} {
	e.mu.Lock()
	view := e.views[session]
	e.mu.Unlock()
	var out []struct {
		record hccli.Terminal
		reader readers.Reader
	}
	if view == nil {
		return out
	}
	for _, record := range view.All() {
		if !record.Live() || !whereShown(record) || !e.handles(session, record.Address()) {
			continue
		}
		if reader := e.agentOf(record); reader != nil {
			out = append(out, struct {
				record hccli.Terminal
				reader readers.Reader
			}{record, reader})
		}
	}
	return out
}

// liveTick：工作中的可读终端每 LiveEvery 发一次增量进活卡片 ( 每终端同时仅一次 )。
func (e *Engine) liveTick() {
	for _, session := range e.Sessions {
		for _, tr := range e.readable(session) {
			if tr.record.State() != stateWorking {
				continue
			}
			k := e.keyOf(session, tr.record.Address())
			e.mu.Lock()
			if e.liveBusy[k] {
				e.mu.Unlock()
				continue
			}
			e.liveBusy[k] = true
			e.mu.Unlock()
			e.later(session, tr.record.Address(), "live", func() error {
				defer func() { e.mu.Lock(); delete(e.liveBusy, k); e.mu.Unlock() }()
				return e.postRead(session, tr.record.Address(), tr.record, tr.reader, false, true)
			})
		}
	}
}

// sweepTick：漏掉的 turn、漏发的终答、错过的事件，空闲可读终端补一遍。
func (e *Engine) sweepTick() {
	if e.ctx == nil || e.ctx.Err() != nil {
		return
	}
	e.readCores()
	for _, session := range e.Sessions {
		e.sweep(session)
	}
}

func (e *Engine) sweep(session string) {
	for _, tr := range e.readable(session) {
		record, reader := tr.record, tr.reader
		address := record.Address()
		k := e.keyOf(session, address)
		// 等着却没卡 ( bot 停机期间开始的等待、错过的事件 )：现在补；
		// 有卡不重发。
		if record.State() == stateBlocked && !e.openCardExists(k) {
			rec := record
			e.later(session, address, "dialog", func() error {
				return e.postBlocked(session, address, rec, false, false)
			})
			continue
		}

		if asks.AskOf(record) != "" {
			if !e.openCardExists(k) {
				rec := record
				e.later(session, address, "dialog", func() error {
					return e.postBlocked(session, address, rec, false, true)
				})
			}
			continue
		}

		if record.State() == stateWorking {
			continue
		}

		e.later(session, address, "sweep", func() error {
			if err := e.postRead(session, address, record, reader, false, false); err != nil {
				return err
			}
			e.markDone(e.keyOf(session, address), SweepDone)
			if idle, ok := reader.(readers.IdleAsker); ok && idle.AsksIdle() && !e.openCardExists(k) {
				e.postIdleAsk(session, address, record)
			}
			return nil
		})
	}
}

// SetAsks 换掉引擎用的 asks 实例与 readers 共享同一个
// ( partial 与 finished 的记忆必须是一份 )。
func (e *Engine) SetAsks(a *asks.Asks) { e.asks = a }

// SetAsksHome 把 asks 的本机 home 换到别处 ( 测试隔离：绝不碰真实 home )。
func (e *Engine) SetAsksHome(home string) { e.asks = asks.New(e.HC, home) }

// onAction 是聊天侧的按钮点击 ( 对话框状态机，见 dialogs.go )。
func (e *Engine) onAction(click core.Click) (core.ActionResult, error) {
	return e.onActionDialog(click)
}

// onAsk：agent 的 hook 问/答了 ( 终端 herdrchat.ask 标签的出现与消失，通路 A )。
// 标签出现，无论状态如何 ( Codex 不显示提示、一边干活一边等 ) 立刻发卡；
// 消失说明 hook 结束 ( 聊天里答的、终端里答的、或超时 )，关卡，终端还在等的再读屏发新卡。
func (e *Engine) onAsk(session, address string, record hccli.Terminal) {
	k := e.keyOf(session, address)
	id := asks.AskOf(record)

	e.mu.Lock()
	was := e.lastAsk[k]
	e.lastAsk[k] = id
	e.mu.Unlock()

	if id == was {
		return
	}
	if !whereShown(record) || !e.handles(session, address) || e.agentOf(record) == nil {
		return
	}
	if id != "" {
		rec := record
		e.later(session, address, "dialog", func() error {
			return e.postBlocked(session, address, rec, false, true)
		})
		return
	}

	e.later(session, address, "dialog", func() error {
		if e.ownDialogOpen(k) {
			e.closeCard(k, DoneTitle)
		}
		// 只有 hook 的那张被遗忘；期间从屏幕拿到卡的不动 ( Codex 自己的提示在 hook 结束后才出现 )，也不会重发。
		e.mu.Lock()
		if was != "" && (e.postedDialog[k] == was || strings.HasPrefix(e.postedDialog[k], was+"#")) {
			delete(e.postedDialog, k)
		}
		e.mu.Unlock()

		if term, err := e.HC.GetTerminal(e.ctx, session, address); err == nil {
			if term.State() == stateBlocked {
				rec := record
				return e.postBlocked(session, address, rec, false, false)
			}
			if term.State() != "" {
				e.sendWaiting(k)
			}
		}
		return nil
	})
}

// ClaimAsks：给没能标终端的 hook 请求找终端 ( reader 的 StrayAsk 认领、hc label 打标签 )；hook 已死的悬空标签撤掉。
// everywhere 时连远端机器也扫 ( sweep 周期 )，否则只看本机。
func (e *Engine) claimAsks(session string, everywhere bool) {
	e.mu.Lock()
	if e.asking[session] {
		e.mu.Unlock()
		return
	}
	e.asking[session] = true
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		delete(e.asking, session)
		e.mu.Unlock()
	}()

	for _, tr := range e.readable(session) {
		record, reader := tr.record, tr.reader
		machine := hccli.MachineOf(record.Address())
		if machine != "" && !everywhere {
			continue
		}
		if id := asks.AskOf(record); id != "" {
			if !e.asks.AskOpen(e.ctx, session, machine, id) {
				if err := e.HC.Label(e.ctx, session, record.Address(), map[string]string{asks.AskLabel: ""}); err == nil {
					xlog.For("engine").Info("hook 已不在，标签撤除", "session", session, "address", record.Address(), "id", id)
				}
			}
			continue
		}
		stray, ok := reader.(readers.StrayAsker)
		if !ok || !e.asks.AsksHere() && machine == "" {
			continue
		}
		id, err := stray.StrayAsk(e.ctx, session, record.Address())
		if err != nil || id == "" {
			continue
		}
		if err := e.HC.Label(e.ctx, session, record.Address(), map[string]string{asks.AskLabel: id}); err == nil {
			xlog.For("engine").Info("终端外的 hook 请求认领到位", "session", session, "address", record.Address(), "id", id)
		}
	}
}
