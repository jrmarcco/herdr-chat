// Package core 是引擎，以及它与 chat app、reader 共享的东西：领域类型
// ( types.go )、错误分类学 ( errors.go )、让一个终端的副作用保序的 lane
// ( lanes.go )。
package core

import "errors"

// bot 的错误分类学：
// 一小组哨兵错误，引擎、CLI 与 skill( /herdrchat fix ) 可以按它分支，
// 每个都有人在聊天里读得懂的话。
// 其余错误保持原样；
// UserWords 兜底取其文本。
var (
	// ErrTranscriptChanged: agent 开了新会话 ( /clear、resume )；旧文件剩余 // 内容先发完，再跟随新的。
	ErrTranscriptChanged = errors.New("the agent started a new conversation; following that one")
	// ErrTranscriptUnreadable: agent 自己的记录读不出来 ( 缺失、无法解析、 // 或截断到无法挽救 )。
	ErrTranscriptUnreadable = errors.New("the agent's own record cannot be read")
	// ErrSessionAmbiguous: 同一目录下的两个 agent 会话无法区分。
	ErrSessionAmbiguous = errors.New("two agent sessions look alike here; it cannot be told which one this terminal runs")
	// ErrLogMatchAmbiguous: 日志文件是启发式匹配的、可能存在更优候选； // 在 footer 里显示为 ⚠，不致命。
	ErrLogMatchAmbiguous = errors.New("⚠ log match ambiguous")
	// ErrMachineDown: hc 够不到终端所在的机器；房间挂起等待，绝不判死。
	ErrMachineDown = errors.New("hc cannot reach this terminal's machine; its room waits")
	// ErrTerminalFinished: 终端已结束；无法再向它发送任何东西。
	ErrTerminalFinished = errors.New("this terminal has ended")
	// ErrHookAbsent: 机器上没有 herdrchat 的 hook，对话框改为读屏。
	ErrHookAbsent = errors.New("herdrchat's hook is not on this machine; reading the screen instead")
	// ErrNoReader: 该 agent 没有 reader；展示的就是它的屏幕。
	ErrNoReader = errors.New("this agent's conversation cannot be read; showing its screen")
	// ErrBotNotRunning: 配对或启动步骤需要 bot 先跑起来。
	ErrBotNotRunning = errors.New("the bot is not running yet")
	// ErrStaleCard: 你点的卡片已不是屏幕上的那个对话框。
	ErrStaleCard = errors.New("the dialog has moved on; the card was refreshed")
	// ErrChatNotReady: owner 尚未可知；不能建任何房间。
	ErrChatNotReady = errors.New("the bot does not know its owner yet")
)

// UserWords 把错误说成人在聊天里读得懂的话：
// 分类学认识它就用分类学的话，否则用错误本身的文本。
func UserWords(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
