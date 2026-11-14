// Package readers 是 agent 一侧的插件缝：每个 agent ( Claude Code、Codex、
// OpenCode ) 一个 reader，从 agent 自己的记录里读它说过的话，做到不丢一字、
// 房间在 agent 工作时实时可见。hc 仍是骨架 ( 哪些终端存在、它们的状态、向
// 它们输入 )；reader 补上的是对话。
package readers

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/state"
)

// ErrNoHook 是 Dialoger 在无法从这台机器的 agent 本身得知 ( 未装 hook ) 时
// 返回的错误：引擎改为立刻读屏。
var ErrNoHook = core.ErrHookAbsent

// Reader 是每个 agent reader 实现的契约 ( spec §3.2 )。After 返回游标之后的
// 新内容 ( Reading 为 nil：该终端不可读 )；Rest 返回程序消失后游标文件里
// 剩下的内容 ( 它的遗言 )。
type Reader interface {
	// App 是 agent 在其 hc status 里的名字 ( status.app )。
	App() string
	// Processes 是它的进程在 hc foreground 里呈现的名字集合。
	Processes() []string
	After(ctx context.Context, session, address string, cur state.Cursor) (*core.Reading, error)
	Rest(ctx context.Context, session, address string, cur state.Cursor) ([]core.Item, error)
}

// 可选能力各自成接口，一个 app 可以有其中几项而没有其余 ( optimization B2 )。
type Dialoger interface {
	// Dialog 从 agent 本身 ( 它的 API，或 herdrchat 的 hook ) 得知终端在等
	// 什么；没有等什么返回 nil，在那台机器上无从得知返回 ErrNoHook——
	// 引擎立刻改为读屏。
	Dialog(ctx context.Context, session, address string) (*core.AgentDialog, error)
}

type Answerer interface {
	// Answer 回答 Dialog 展示的问题；agent 收下了返回 true。
	Answer(ctx context.Context, session, address string, shown core.Dialog, r core.Reply) (bool, error)
}

type Titled interface {
	// TitleOf 从终端标题里清出一个房间名。
	TitleOf(title string) string
}

type Metered interface {
	// Meter 给 footer 提供 agent 记录里没有的数据。
	Meter(ctx context.Context, session, address string, meta map[string]any) (core.MeterInfo, error)
}

type StrayAsker interface {
	// StrayAsk 在 hook 跑到终端之外时，说出属于这个终端的那条 ask ( hook
	// 请求 )；没有匹配的返回 ""。
	StrayAsk(ctx context.Context, session, address string) (string, error)
}

type IdleAsker interface {
	// AsksIdle 表示它的对话框可以是 idle：在 turn 结束时被问。
	AsksIdle() bool
}

// Registry 收纳已安装的 reader，每个 app 一个。
type Registry struct {
	byApp map[string]Reader
	apps  []string
}

// NewRegistry 建一个空注册表。
func NewRegistry() *Registry { return &Registry{byApp: map[string]Reader{}} }

// Register 安装一个 reader；同一 app 装第二个是错误 ( CheckReader 是调用方
// 的第一步 )。
func (r *Registry) Register(rd Reader) error {
	if err := CheckReader(rd); err != nil {
		return err
	}
	if _, dup := r.byApp[rd.App()]; dup {
		return fmt.Errorf("two readers for %s", rd.App())
	}
	r.byApp[rd.App()] = rd
	r.apps = append(r.apps, rd.App())
	sort.Strings(r.apps)
	return nil
}

// ByApp 找到 hc status.app 指名的 reader，找不到返回 nil。
func (r *Registry) ByApp(app string) Reader { return r.byApp[app] }

// Apps 列出已安装 reader 的 app 名。
func (r *Registry) Apps() []string { return append([]string(nil), r.apps...) }

// CheckReader 在启动时说出一个 reader 缺了什么就跑不起来。
func CheckReader(rd Reader) error {
	if rd == nil {
		return errors.New("no reader")
	}
	if rd.App() == "" {
		return errors.New("a reader without an app name") // reader 没有名字
	}
	if len(rd.Processes()) == 0 {
		return fmt.Errorf("%s: no process names to match its foreground", rd.App())
	}
	return nil
}

// AgentOf 为一个终端的 hc 记录挑 reader：status.app 必须指名一个，且它跑的
// 程序 ( hc 的 foreground：进程名或 argv[0] 的 basename ) 必须属于该 reader——
// hc 的状态可能在 agent 退出后残留，一个 shell 不能被当成它的 agent。
// 前台未知时选择相信 ( 宁可话多的 shell，不要哑掉的 agent )。
func AgentOf(r *Registry, app, foreground string) Reader {
	rd := r.ByApp(app)
	if rd == nil {
		return nil
	}
	if foreground == "" {
		return rd
	}
	base := filepath.Base(strings.Fields(foreground)[0])
	for _, name := range rd.Processes() {
		if foreground == name || base == name {
			return rd
		}
	}
	return nil
}

// Shorter 是带短名的 reader ( footer 里用它代替 app 全名 )。
type Shorter interface {
	Short() string
}

// InstallClaude / InstallCodex / InstallOpenCode 由各子包提供；装配处
// ( cmd / 引擎测试 ) 挑选安装，core 不依赖任何具体 reader。
