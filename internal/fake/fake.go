// Package fake 是引擎行为测试运行的测试三件套 ( spec §13，对应原版
// test/fakes.mjs )：记录每次调用的 chat、按剧本应答的 hc、按吩咐说话的
// reader。没有 socket、没有网络、除临时目录外没有磁盘。
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sync"

	"github.com/jrmarcco/herdr-chat/internal/chats"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/readers"
	"github.com/jrmarcco/herdr-chat/internal/state"
)

// Call 是 fake 被要求做的一件事，按方法名命名。
type Call struct {
	Name string
	Args []any
}

// FakeChat 记录每次调用并按剧本应答。M1 阶段返回零值即可；引擎的测试会
// 逐步长出各自要断言的东西。
type FakeChat struct {
	mu      sync.Mutex
	calls   []Call
	ready   bool
	nextNum int

	// LiveBytesVal / FoldedBytesVal 覆盖默认字节上限 ( 活卡片切卡等测试用 )。
	LiveBytesVal   int
	FoldedBytesVal int
	// FailPost 让前 N 次 Post / PostCard 失败 ( deliver 重试的测试用 )。
	FailPost int

	lastMsg core.Message

	Handlers chats.Handlers
}

// NewFakeChat 建一个 owner 已知 ( Ready 为 true ) 的实例——这才是有意义的
// 状态；测试需要时再翻转它。
func NewFakeChat() *FakeChat { return &FakeChat{ready: true} }

func (f *FakeChat) record(name string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Name: name, Args: args})
}

// Calls 按顺序列出目前记录到的一切。
func (f *FakeChat) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Counts 统计每个方法名各被调用了多少次。
func (f *FakeChat) Counts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for _, c := range f.calls {
		out[c.Name]++
	}
	return out
}

func (f *FakeChat) fresh() core.Message {
	f.nextNum++
	msg := core.Message(fmt.Sprintf("m%d", f.nextNum))
	f.mu.Lock()
	f.lastMsg = msg
	f.mu.Unlock()
	return msg
}

// LastPosted 是最近发出的一条消息的 id ( 点击测试用它找回卡片 )。
func (f *FakeChat) LastPosted() core.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastMsg
}

// --- 元信息 ---

func (f *FakeChat) Name() string     { return "fake" }
func (f *FakeChat) RoomWord() string { return "room" }
func (f *FakeChat) Ready() bool      { return f.ready }
func (f *FakeChat) SetReady(r bool)  { f.ready = r } // 测试翻转 owner 已知状态
func (f *FakeChat) Help() []string   { return nil }
func (f *FakeChat) LiveBytes() int {
	if f.LiveBytesVal > 0 {
		return f.LiveBytesVal
	}
	return 20000
}

func (f *FakeChat) FoldedBytes() int {
	if f.FoldedBytesVal > 0 {
		return f.FoldedBytesVal
	}
	return 20000
}

func (f *FakeChat) Split(markdown string) []string {
	// One piece: the fake has no platform limit to respect.
	return []string{markdown}
}

// --- 启动 ---

func (f *FakeChat) Start(ctx context.Context, h chats.Handlers) error {
	f.record("Start")
	f.Handlers = h
	if h.OnReady != nil {
		h.OnReady()
	}
	return nil
}

func (f *FakeChat) Command(cmd string, args []string, where core.Where) (bool, error) {
	f.record("Command", cmd, args)
	return false, nil
}

// --- 房间 ---

func (f *FakeChat) OpenRoom(ctx context.Context, look core.RoomLook) (core.Room, error) {
	f.record("OpenRoom", look.Name)
	f.nextNum++
	return core.Room(fmt.Sprintf("room%d", f.nextNum)), nil
}

func (f *FakeChat) RenameRoom(ctx context.Context, room core.Room, name string) error {
	f.record("RenameRoom", string(room), name)
	return nil
}

func (f *FakeChat) PlaceRoom(ctx context.Context, k string, place core.Place) error {
	f.record("PlaceRoom", k, place.Space)
	return nil
}

func (f *FakeChat) EndRoom(ctx context.Context, k string) error { f.record("EndRoom", k); return nil }

func (f *FakeChat) ReviveRoom(ctx context.Context, k string) error {
	f.record("ReviveRoom", k)
	return nil
}

// --- 发帖 ---

func (f *FakeChat) Post(ctx context.Context, room core.Room, markdown, footer string) (core.Message, error) {
	if err := f.maybeFail("Post"); err != nil {
		return "", err
	}
	f.record("Post", string(room), markdown)
	return f.fresh(), nil
}

// maybeFail 按剧本让前几次发帖失败。
func (f *FakeChat) maybeFail(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailPost > 0 {
		f.FailPost--
		return fmt.Errorf("%s failed (script)", name)
	}
	return nil
}

func (f *FakeChat) Reply(ctx context.Context, where core.Where, markdown string) (core.Message, error) {
	f.record("Reply", string(where.Room), markdown)
	return f.fresh(), nil
}

func (f *FakeChat) PostCard(ctx context.Context, room core.Room, look core.Look) (core.Message, error) {
	if err := f.maybeFail("PostCard"); err != nil {
		return "", err
	}
	f.record("PostCard", string(room), look.Title, look.Markdown)
	return f.fresh(), nil
}

func (f *FakeChat) EditCard(ctx context.Context, msg core.Message, look core.Look) error {
	f.record("EditCard", string(msg), look.Title, look.Markdown)
	return nil
}

func (f *FakeChat) PostDialog(ctx context.Context, room core.Room, d core.Dialog, v core.DialogValue) (core.Message, error) {
	f.record("PostDialog", string(room), d.Title, v)
	return f.fresh(), nil
}

func (f *FakeChat) CloseDialog(ctx context.Context, msg core.Message, d core.Dialog, done string) error {
	f.record("CloseDialog", string(msg), done)
	return nil
}

// --- 提醒 ---

func (f *FakeChat) Pin(ctx context.Context, k string, room core.Room, msg core.Message) error {
	f.record("Pin", k, string(room), string(msg))
	return nil
}
func (f *FakeChat) Unpin(ctx context.Context, k string) error { f.record("Unpin", k); return nil }
func (f *FakeChat) Pinned() []string                          { return nil }
func (f *FakeChat) Ring(ctx context.Context, k string, msg core.Message, text string) error {
	f.record("Ring", k, text)
	return nil
}

func (f *FakeChat) MarkWorking(ctx context.Context, msg core.Message) (string, error) {
	f.record("MarkWorking", string(msg))
	return "token", nil
}

func (f *FakeChat) MarkDone(ctx context.Context, msg core.Message, token string) error {
	f.record("MarkDone", string(msg), token)
	return nil
}

// --- 媒体 ---

func (f *FakeChat) Image(ctx context.Context, buf []byte) (string, error) {
	f.record("Image", len(buf))
	return "![image](fake)", nil
}

func (f *FakeChat) PostFile(ctx context.Context, room core.Room, fp core.FilePost) (core.Message, error) {
	f.record("PostFile", string(room), fp.Name)
	return f.fresh(), nil
}

func (f *FakeChat) PostFolded(ctx context.Context, room core.Room, fp core.FoldedPost) (core.Message, error) {
	f.record("PostFolded", string(room), fp.Title)
	return f.fresh(), nil
}

func (f *FakeChat) Download(ctx context.Context, msg core.Message, ref, file string) error {
	f.record("Download", ref, file)
	return nil
}

// --- owner ---

func (f *FakeChat) TellOwner(ctx context.Context, look core.Look) error {
	f.record("TellOwner", look.Title)
	return nil
}

func (f *FakeChat) ShowOverview(ctx context.Context, rows []core.OverviewRow, opts core.OverviewOpts) error {
	f.record("ShowOverview", len(rows), opts.Fresh)
	return nil
}

// 编译期保证：这个 fake 是一个 chat app。
var _ chats.Chat = (*FakeChat)(nil)

// FakeHC 按剧本应答并记录发来的东西。它保存事件 handlers，测试就能像
// session 的事件流那样驱动事件。
type FakeHC struct {
	mu        sync.Mutex
	Terminals map[string]hccli.Terminal // by address, current records
	Screens   map[string]string         // by address
	// HomeDirSet 让 HomeShell 的本机分支在测试目录里跑；Execs/ExecFn 是
	// `hc exec --on` 的记录与剧本。
	HomeDirSet string
	Execs      []ExecCall
	ExecFn     func(session, machine, script string) (string, error)
	Sent       []SentText
	Keyed      []SentKeys
	Labeled    []SetLabel

	sessions map[string]hccli.EventHandlers
	views    map[string]*hccli.View
}

// SentText 是 fake 接下的一次 `hc send`。
type SentText struct {
	Session, Address, Text string
	Force                  bool
}

// SentKeys 是 fake 接下的一次 `hc keys` 按键。
type SentKeys struct {
	Session, Address string
	Keys             []string
}

// SetLabel 是 fake 接下的一次 `hc label`。
type SetLabel struct {
	Session, Address string
	Labels           map[string]string
}

// NewFakeHC 建一个备好了可见终端的 fake。
func NewFakeHC() *FakeHC {
	return &FakeHC{
		Terminals: map[string]hccli.Terminal{},
		Screens:   map[string]string{},
		sessions:  map[string]hccli.EventHandlers{},
		views:     map[string]*hccli.View{},
	}
}

func (f *FakeHC) FollowEvents(ctx context.Context, session string, h hccli.EventHandlers, view *hccli.View) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[session] = h
	f.views[session] = view
	// A first snapshot of everything already there, as the real stream does.
	var list []hccli.Terminal
	for _, t := range f.Terminals {
		list = append(list, t)
	}
	if view != nil {
		view.ApplySnapshot(list)
		view.MarkSnapshotDone()
	}
	go func() {
		if h.OnSnapshot != nil {
			h.OnSnapshot(list)
		}
	}()
}

// SnapshotDone 等待每个被跟随 session 的首个快照到齐。
func (f *FakeHC) SnapshotDone() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	done := make(chan struct{})
	go func() {
		for _, v := range f.views {
			<-v.SnapshotDone()
		}
		close(done)
	}()
	return done
}

// cloneTerm 深拷贝一条终端记录：fake 改的永远是自己的副本，与引擎正在读的
// 记录 ( 含嵌套的 status、labels ) 不共享任何 map。
func cloneTerm(t hccli.Terminal) hccli.Terminal {
	raw, err := json.Marshal(t)
	if err != nil {
		return hccli.Terminal{}
	}
	var out hccli.Terminal
	if json.Unmarshal(raw, &out) != nil {
		return hccli.Terminal{}
	}
	return out
}

// Turn 像事件流那样为一个地址触发 turn 事件，并同步状态到当前记录
// ( 在副本上改，不惊动正在被读的记录 )。
func (f *FakeHC) Turn(session, address, stateWord string) {
	f.mu.Lock()
	h := f.sessions[session]
	record := cloneTerm(f.Terminals[address])
	if status, ok := record["status"].(map[string]any); ok {
		status["state"] = stateWord
	}
	f.Terminals[address] = record
	view := f.views[session]
	f.mu.Unlock()
	if view != nil {
		view.Upsert(record)
	}
	if h.OnTurn != nil {
		h.OnTurn(hccli.TurnEvent{Address: address, State: stateWord, Record: record})
	}
}

// Change 触发其余任意事件——先像真实事件流的 apply 那样应用到 fake 的终端
// 记录上，让引擎看到的记录是最新的。
func (f *FakeHC) Change(session, typ, address string, data map[string]any) {
	f.mu.Lock()
	record := cloneTerm(f.Terminals[address])
	switch typ {
	case "created", "restarted":
		record = hccli.Terminal(data)
	case "status":
		for k, v := range data {
			record[k] = v
		}
	case "labels":
		if r, ok := data["record"].(map[string]any); ok {
			record = hccli.Terminal(r)
		} else {
			record["labels"] = data["labels"]
		}
	case "finished":
		record["state"] = "finished"
		record["foreground"] = nil
	}
	if address != "" {
		f.Terminals[address] = record
	}
	h := f.sessions[session]
	view := f.views[session]
	f.mu.Unlock()
	if view != nil {
		if typ == "removed" {
			view.Delete(address)
		} else if record != nil {
			view.Upsert(record)
		}
	}
	if h.OnChange != nil {
		h.OnChange(hccli.ChangeEvent{Type: typ, Address: address, Record: record, Data: data})
	}
}

// RemoveTerminal 拿掉一个终端 ( 从记录与每个 view，像 removed 事件那样 )。
func (f *FakeHC) RemoveTerminal(address string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.Terminals, address)
	for _, v := range f.views {
		v.Delete(address)
	}
}

// SetTerminal 整体替换一个终端的记录 ( 存入副本，并同步到每个 view )。
func (f *FakeHC) SetTerminal(address string, record hccli.Terminal) {
	f.mu.Lock()
	record = cloneTerm(record)
	record["address"] = address
	f.Terminals[address] = record
	views := make([]*hccli.View, 0, len(f.views))
	for _, v := range f.views {
		views = append(views, v)
	}
	f.mu.Unlock()
	for _, v := range views {
		v.Upsert(record)
	}
}

func (f *FakeHC) Cores(ctx context.Context, session string) (string, []hccli.MachineWithID, error) {
	return "core-1", nil, nil
}

func (f *FakeHC) Machines(ctx context.Context, session string) ([]hccli.Machine, error) {
	return nil, nil
}

func (f *FakeHC) GetTerminal(ctx context.Context, session, address string) (hccli.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.Terminals[address]
	if !ok {
		return nil, fmt.Errorf("hc get %s: no such terminal", address)
	}
	return t, nil
}

func (f *FakeHC) ScreenTail(ctx context.Context, session, address string, lines int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	screen, ok := f.Screens[address]
	if !ok {
		return "", nil
	}
	return hccli.TidyScreen(screen, lines), nil
}

func (f *FakeHC) Send(ctx context.Context, session, address, text string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Sent = append(f.Sent, SentText{Session: session, Address: address, Text: text, Force: force})
	return nil
}

func (f *FakeHC) Keys(ctx context.Context, session, address string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Keyed = append(f.Keyed, SentKeys{Session: session, Address: address, Keys: append([]string(nil), keys...)})
	return nil
}

func (f *FakeHC) PutFile(ctx context.Context, session, address, localFile string) (string, error) {
	return "/remote/" + path.Base(localFile), nil
}

func (f *FakeHC) RunTerminal(ctx context.Context, session string, o hccli.RunOpts) (json.RawMessage, error) {
	return json.RawMessage("{}"), nil
}

func (f *FakeHC) Kill(ctx context.Context, session, address string) error { return nil }

func (f *FakeHC) Label(ctx context.Context, session, address string, labels map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Labeled = append(f.Labeled, SetLabel{Session: session, Address: address, Labels: labels})
	return nil
}

// 编译期保证：这个 fake 就是引擎的 hc。
var _ core.HC = (*FakeHC)(nil)

// FakeReader 在被问及它服务的终端时，说出测试吩咐的话。
type FakeReader struct {
	AppName   string
	ProcNames []string
	Reading   *core.Reading

	mu sync.Mutex
	// afterFn / restFn 覆盖默认行为 ( 脚本化的增量与遗言 )；测试随时换剧本、
	// 引擎随时读，用锁同步。
	afterFn      func(ctx context.Context, session, address string, cur state.Cursor) (*core.Reading, error)
	restFn       func(ctx context.Context, session, address string, cur state.Cursor) ([]core.Item, error)
	DialogNow    *core.AgentDialog
	Answered     []core.Reply
	MeterInfo    core.MeterInfo
	CleanedTitle string
}

// SetAfterFn 换增量剧本 ( 锁内写，与引擎的并发读同步 )。
func (r *FakeReader) SetAfterFn(fn func(ctx context.Context, session, address string, cur state.Cursor) (*core.Reading, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.afterFn = fn
}

// SetRestFn 换遗言剧本。
func (r *FakeReader) SetRestFn(fn func(ctx context.Context, session, address string, cur state.Cursor) ([]core.Item, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.restFn = fn
}

func NewFakeReader() *FakeReader {
	return &FakeReader{AppName: "fake-agent", ProcNames: []string{"fake"}}
}

func (r *FakeReader) App() string         { return r.AppName }
func (r *FakeReader) Processes() []string { return r.ProcNames }

func (r *FakeReader) After(ctx context.Context, session, address string, cur state.Cursor) (*core.Reading, error) {
	r.mu.Lock()
	fn := r.afterFn
	r.mu.Unlock()
	if fn != nil {
		return fn(ctx, session, address, cur)
	}
	return r.Reading, nil
}

func (r *FakeReader) Rest(ctx context.Context, session, address string, cur state.Cursor) ([]core.Item, error) {
	r.mu.Lock()
	fn := r.restFn
	r.mu.Unlock()
	if fn != nil {
		return fn(ctx, session, address, cur)
	}
	return nil, nil
}

func (r *FakeReader) Dialog(ctx context.Context, session, address string) (*core.AgentDialog, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.DialogNow, nil
}

// SetDialogNow 换它现在等着的提问 ( 锁内写，与引擎并发读同步 )。
func (r *FakeReader) SetDialogNow(d *core.AgentDialog) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.DialogNow = d
}

func (r *FakeReader) Answer(ctx context.Context, session, address string, shown core.Dialog, rep core.Reply) (bool, error) {
	r.Answered = append(r.Answered, rep)
	return true, nil
}

func (r *FakeReader) TitleOf(title string) string { return r.CleanedTitle }

func (r *FakeReader) Meter(ctx context.Context, session, address string, meta map[string]any) (core.MeterInfo, error) {
	return r.MeterInfo, nil
}

// 编译期保证：这个 fake 是带全部可选能力的 reader。
var (
	_ readers.Reader   = (*FakeReader)(nil)
	_ readers.Dialoger = (*FakeReader)(nil)
	_ readers.Answerer = (*FakeReader)(nil)
	_ readers.Titled   = (*FakeReader)(nil)
	_ readers.Metered  = (*FakeReader)(nil)
)

// SetScreen 换一个终端的屏幕 ( 锁内写，测试与引擎并发安全 )。
func (f *FakeHC) SetScreen(address, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Screens[address] = text
}

// ClearSents / ClearKeys 清空发送与按键记录 ( 锁内 )。
func (f *FakeHC) ClearSents() { f.mu.Lock(); defer f.mu.Unlock(); f.Sent = nil }
func (f *FakeHC) ClearKeys()  { f.mu.Lock(); defer f.mu.Unlock(); f.Keyed = nil }

// Sents 列出收到的 `hc send` ( 锁内拷贝，测试可安全读 )。
func (f *FakeHC) Sents() []SentText {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SentText(nil), f.Sent...)
}

// KeysPressed 列出收到的 `hc keys`。
func (f *FakeHC) KeysPressed() []SentKeys {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SentKeys(nil), f.Keyed...)
}

// HomeShell 在 fake 的 HomeDir ( 默认 os.UserHomeDir，测试注入临时目录 ) 下
// 真的跑 sh：hook 的 req/ans 文件协议由此用真实文件系统测试。
func (f *FakeHC) HomeShell(ctx context.Context, session, machine, script string) (string, error) {
	if machine != "" {
		f.mu.Lock()
		f.Execs = append(f.Execs, ExecCall{Session: session, Machine: machine, Script: script})
		fn := f.ExecFn
		f.mu.Unlock()
		if fn != nil {
			return fn(session, machine, script)
		}
		return "", nil
	}
	dir := f.homeDir()
	cmd := exec.CommandContext(ctx, "sh", "-c", script) //nolint:gosec // G204: 测试脚本由测试自己拼
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return "", nil
	}
	return string(out), nil
}

func (f *FakeHC) homeDir() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.HomeDirSet != "" {
		return f.HomeDirSet
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

// ExecCall 是一次 `hc exec --on` ( HomeShell 的远端分支 )。
type ExecCall struct {
	Session, Machine, Script string
}

// ExecCalls 列出收到的 exec 调用 ( 锁内拷贝 )。
func (f *FakeHC) ExecCalls() []ExecCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ExecCall(nil), f.Execs...)
}

// ClearAnswered 清空已收到的回答 ( 锁内 )。
func (r *FakeReader) ClearAnswered() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Answered = nil
}

// Answers 列出已收到的回答 ( 锁内拷贝 )。
func (r *FakeReader) Answers() []core.Reply {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.Reply(nil), r.Answered...)
}
