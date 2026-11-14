package core

import (
	"time"
)

// 引擎、chat app 与 reader 共享的领域类型 ( spec §3 )。Room 与 Message 是
// chat app 自己的 id，原样保存：core 绝不往里看。

// Room 是 chat app 的房间 id ( 飞书群的 chat_id、Telegram topic 的 thread id )。
type Room string

// Message 是 chat app 的消息 id。
type Message string

// Where 指明引擎要回应的东西在哪：房间 ( 空：直聊 ) 与要回应的那条消息。
type Where struct {
	Room    Room
	Message Message
}

// Tone 是卡片的样子；chat app 把它映射成自己的配色。
type Tone string

const (
	ToneWorking Tone = "working"
	ToneClosed  Tone = "closed"
	ToneNeeds   Tone = "needs"
	ToneDone    Tone = "done"
	ToneGood    Tone = "good"
	ToneBad     Tone = "bad"
)

// Look 是一张卡片：标题、色调、markdown 正文与一行 footer。
type Look struct {
	Title    string
	Tone     Tone
	Markdown string
	Footer   string
}

// MenuOption 是对话框按屏幕上画的那样给出的一个编号选项。
// Checked 三态：nil 非多选项；true/false 是勾选框的实/虚 ( 多选题 )。
type MenuOption struct {
	N       int
	Label   string
	Checked *bool
}

// Menu 是从屏幕上读出的对话框编号菜单 ( spec §6.5.1 )：最后一段 1、2、3…
// 连续编号的选项，上方是对框自己的话。Key 是对话框的身份 ( 一个哈希 )：旧
// 卡片永远答不了长得像的新对话框。Own 在 agent 自己上报对话框时 ( hook 通路 )
// 携带它的原始引用，让点击沿原路回去而不是穿过屏幕。
type Menu struct {
	Key     string
	Options []MenuOption
	Multi   bool
	Free    *MenuOption // "Type something" 选项 ( 自由输入 )
	Text    string
	Framed  bool
	Beside  bool
	Cursor  int
	Own     any
}

// Dialog 是终端在等什么：有菜单就带菜单、周围的话，以及卡片关闭时说的话。
type Dialog struct {
	Menu      *Menu
	Title     string
	Markdown  string
	DoneTitle string
	Footer    string
}

// DialogValue 是对话框卡片发出后携带的值，让点击知道自己回答的是哪个对话
// 框 ( 菜单的 key，或 agent 的原始引用 )。chat app 对它保持不透明。
type DialogValue any

// Item roles.
const (
	RoleYou   = "you"
	RoleAI    = "ai"
	RoleTool  = "tool"
	RoleImage = "image"
	RoleMeta  = "meta"
	RoleCard  = "card" // FINAL_PLAN §2.B: a subagent / workflow card
)

// Card 是会话里单独成卡的一次委托：子代理或工作流、被交代了什么、
// 现在怎样。
type Card struct {
	Kind    string // "subagent" | "workflow" | "phase"
	Title   string
	State   string // "started" | "done" | "failed"
	Summary string
}

// ContextUse 是模型上下文用了多少。
type ContextUse struct {
	Used int64
	Size int64
}

// Limit 是一项账号限额 ( 窗口名与已用百分比 )。
type Limit struct {
	Name string
	Used float64
}

// Item 是 reader 从 agent 自己的记录里取出的一条：你说的、它答的、一行
// 工具、一张图，或只喂给 footer 的 meta。ID 稳定 ( 游标去重用 )；
// End 标记 turn 的最后一条回答。
type Item struct {
	Role     string
	Text     string
	At       time.Time
	ID       string
	End      bool
	From     string // image: "you" | "ai"
	Media    string
	DataB64  string
	Key      string // image upload result
	Err      string
	Model    string
	Provider string
	Context  *ContextUse
	Limits   []Limit
	Card     *Card
}

// Reading 是 reader 找到的新内容：条目，以及它读的日志文件 ( 存进游标 )。
type Reading struct {
	Items []Item
	File  string
}

// Reply 是对一个对话框的回答：选中的选项、自由输入、多选的勾选，
// 或 Esc。
type Reply struct {
	Pick  string
	Text  string
	Ticks []string
	Esc   bool
}

// AgentDialog 是 agent 自己上报的提问 ( 它的 API，或 herdrchat 的 hook )：
// 原生形状，引擎的 agentDialog 再把它包成发卡的 Dialog。
type AgentDialog struct {
	Key     string
	Kind    string
	Title   string
	Text    string
	Options []AgentOption
	Multi   bool
	Free    int  // 自由输入项的编号 ( 0：没有 )
	Idle    bool // 不挡工作的提问，由你的下一条消息回答
	Ref     any  // reader 的原始引用，answer 沿原路回去
}

// AgentOption 是 agent 上报的一个选项。
type AgentOption struct {
	N       int
	Label   string
	Checked *bool
}

// MeterInfo 是 reader 从 agent 本身得知的 footer 数据。
type MeterInfo struct {
	ContextSize int64
	Limits      []Limit
}

// Incoming 是来自聊天侧的一条消息 ( Room 为空：直聊 )。
type Incoming struct {
	Room    Room
	Where   Where
	Message Message
	Text    string
	Images  [][]byte
}

// Click 是卡片上的一次按钮点击：卡片的值、所在消息，以及卡片带输入框时
// 自由输入的文本。
type Click struct {
	Value   DialogValue
	Message Message
	Answer  string
}

// ActionResult 是点击立刻给出的回应 ( 飞书要求 3 秒内应答 )：一条 toast，
// 也许还有卡片的下一副样子；Value 是刷新后卡片携带的值 ( 下一次点击凭它
// 找回对话框 )。
type ActionResult struct {
	Toast string
	Card  *Look
	Value DialogValue
}

// RoomLook 是新房间的名字与开场白。
type RoomLook struct {
	Name  string
	Intro string
}

// Place 说明房间属于哪个 space ( 用于它的装饰：头像、标签、静音 )。
type Place struct {
	Space   string
	Session string
	Machine string
}

// FilePost 是发进房间的一个文件 ( 回填的全部历史 )。
type FilePost struct {
	Name    string
	Data    []byte
	Caption string
}

// FoldedPost 是房间的折叠历史帖。
type FoldedPost struct {
	Title    string
	Markdown string
}

// OverviewRow 是直聊总览里的一行。
type OverviewRow struct {
	Name    string
	Address string
	State   string
	Kind    string
	Program string
	Since   string
	Room    Room
}

// OverviewOpts 说明总览如何展示。
type OverviewOpts struct {
	Fresh bool // a new message, not an edit of the old one
}
