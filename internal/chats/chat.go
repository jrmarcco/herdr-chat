// Package chats 是两条插件缝中 chat app 一侧：
// 每个 chat app ( 本构建只有飞书 ) 为 core 实现的契约 —— core 绝不 import 任何app，
// app 也绝不 import 引擎。
package chats

import (
	"context"
	"fmt"
	"strings"

	"github.com/jrmarcco/herdr-chat/internal/core"
)

// Handlers 是 chat app 递向引擎一侧的东西：
// 来自你聊天的消息与按钮点击，以及 owner 已知的通报。
type Handlers struct {
	OnMessage func(core.Incoming)
	OnAction  func(core.Click) (core.ActionResult, error)
	OnReady   func()
}

// chat 契约的 Go 形态：
// app 赋值给 Chat 的那一刻，成员齐全就是编译器的活；
// 运行期仍可能出错的 ( 空名字、零字节上限 ) 由 CheckChat 在启动时、建任何房间之前指出。
type Chat interface {
	// 元信息
	Name() string     // "feishu"
	Ready() bool      // owner known; unknown, no room is made
	OwnerKey() string // the config.env key that names the owner
	Help() []string   // its own direct-chat command lines
	RoomWord() string // "group" | "topic"
	LiveBytes() int   // what one editable card holds
	FoldedBytes() int
	Split(markdown string) []string // by the platform's limits, code-fence aware

	// 启动
	Start(ctx context.Context, h Handlers) error
	Command(cmd string, args []string, where core.Where) (bool, error) // its own direct-chat commands

	// 房间
	OpenRoom(ctx context.Context, look core.RoomLook) (core.Room, error)
	RenameRoom(ctx context.Context, room core.Room, name string) error
	PlaceRoom(ctx context.Context, k string, place core.Place) error
	DeleteRoom(k string) error // 彻底删掉一个房间 ( /clean：解散群 )
	EndRoom(ctx context.Context, k string) error
	ReviveRoom(ctx context.Context, k string) error

	// 发帖
	Post(ctx context.Context, room core.Room, markdown, footer string) (core.Message, error)
	Reply(ctx context.Context, where core.Where, markdown string) (core.Message, error)
	PostCard(ctx context.Context, room core.Room, look core.Look) (core.Message, error)
	EditCard(ctx context.Context, msg core.Message, look core.Look) error
	PostDialog(ctx context.Context, room core.Room, d core.Dialog, v core.DialogValue) (core.Message, error)
	CloseDialog(ctx context.Context, msg core.Message, d core.Dialog, done string) error

	// 提醒
	Pin(ctx context.Context, k string, room core.Room, msg core.Message) error
	Unpin(ctx context.Context, k string) error
	Pinned() []string
	Ring(ctx context.Context, k string, msg core.Message, text string) error
	MarkWorking(ctx context.Context, msg core.Message) (token string, err error)
	MarkDone(ctx context.Context, msg core.Message, token string) error

	// 媒体
	Image(ctx context.Context, buf []byte) (markdown string, err error)
	PostFile(ctx context.Context, room core.Room, f core.FilePost) (core.Message, error)
	PostFolded(ctx context.Context, room core.Room, f core.FoldedPost) (core.Message, error)
	Download(ctx context.Context, msg core.Message, ref, file string) error

	// owner
	TellOwner(ctx context.Context, look core.Look) error
	ShowOverview(ctx context.Context, rows []core.OverviewRow, opts core.OverviewOpts) error
}

// CheckChat 在启动时说出编译期无法得知的问题：运行期合理性检查。
func CheckChat(c Chat) error {
	if c == nil {
		return fmt.Errorf("no chat app")
	}
	var problems []string
	if c.Name() == "" {
		problems = append(problems, "no name")
	}
	if c.OwnerKey() == "" {
		problems = append(problems, "no config key for its owner")
	}
	if c.RoomWord() == "" {
		problems = append(problems, "no word for a room")
	}
	if c.LiveBytes() <= 0 {
		problems = append(problems, "a live card must hold some bytes")
	}
	if c.FoldedBytes() < 0 {
		problems = append(problems, "a folded post cannot hold negative bytes")
	}
	if problems != nil {
		return fmt.Errorf("%s: %s", c.Name(), strings.Join(problems, "; "))
	}
	return nil
}
