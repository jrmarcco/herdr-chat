package core

import (
	"context"
	"encoding/json"

	"github.com/jrmarcco/herdr-chat/internal/hccli"
)

// HC 是引擎对 hc 所做的一切，让整个 core 能在从不碰 socket 的 fake 上运行。
// *hccli.CLI 实现了它。
// 每个方法都带 context：没有 deadline 的 hc 调用不予合并。
type HC interface {
	FollowEvents(ctx context.Context, session string, h hccli.EventHandlers, view *hccli.View)
	Cores(ctx context.Context, session string) (self string, links []hccli.MachineWithID, err error)
	Machines(ctx context.Context, session string) ([]hccli.Machine, error)
	GetTerminal(ctx context.Context, session, address string) (hccli.Terminal, error)
	ScreenTail(ctx context.Context, session, address string, lines int) (string, error)
	Send(ctx context.Context, session, address, text string, force bool) error
	Keys(ctx context.Context, session, address string, keys []string) error
	PutFile(ctx context.Context, session, address, localFile string) (string, error)
	RunTerminal(ctx context.Context, session string, o hccli.RunOpts) (json.RawMessage, error)
	// HomeShell 在终端所在机器的 home 目录跑一条短 shell 命令 ( 本机直接 sh，远端 hc exec --on )；失败的输出为空。
	HomeShell(ctx context.Context, session, machine, script string) (string, error)
	// HomeFile 读终端所在机器 home 下的一个文件 ( tail>0 时只取尾部 )，readers的日志通路 ( core.HomeFiles )。
	HomeFile(ctx context.Context, session, machine, rel string, tail int) (string, error)
	Kill(ctx context.Context, session, address string) error
	Label(ctx context.Context, session, address string, labels map[string]string) error
}
