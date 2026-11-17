package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/jrmarcco/herdr-chat/internal/chats"
	"github.com/jrmarcco/herdr-chat/internal/config"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/state"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// Chat 是 herdrchat 的飞书侧：
// 一个 hc 终端一个群，直聊是总览与命令；
// 经 /auth 后以你行事 ( 静音、你的标签、置顶、@ )。
//
// 事件走 larkws 长连接：
// 消息 ( im.message.receive_v1 )、
// 卡片点击( card.action.trigger，3 秒内应答 )、
// bot 菜单 ( application.bot.menu_v6 )。
//
// 飞书不保证补发停机期间的事件，所以自己看：
// 每个群记下已处理到的时刻 ( state.feishuRead )，
// 每次连接后用 message.list 补收漏掉的。

const (
	kType         = "type"
	kToast        = "toast"
	idChat        = "chat_id"
	idOpen        = "open_id"
	chatP2P       = "p2p"
	toastError    = "error"
	keyContent    = "content"
	youCheckEvery = 60 * time.Second
	overviewTries = 3
	workingEmoji  = "THINKING"
	doneEmoji     = "OK"
)

// Chat 实现 chats.Chat。
type Chat struct {
	mu sync.Mutex

	AppID       string
	AppSecret   string
	OwnerOpenID string // 空：还没配对，不建任何房间
	Profile     string

	// EncryptKey / VerificationToken 是应用「事件与回调 → 加密策略」页的两个值，可选。
	// 长连接本身在建连时已鉴权，不填照常工作；填了，带密文的事件 ( encrypt 字段 )
	// 由 SDK 的分发器自动解密 ( AES-256-CBC，密钥为 Encrypt Key 的 SHA-256 )。
	EncryptKey        string
	VerificationToken string

	// Store 是 state.json。
	Store state.Store

	// Muted/Silent 是 HERDR_MUTED_SPACES / HERDR_SILENT_SPACES。
	Muted  config.Spaces
	Silent config.Spaces

	// BotTags / UserTags 是 HERDR_BOT_TAGS / HERDR_USER_TAGS。
	BotTags, UserTags bool
	// StateDir 存 user-token.json。
	StateDir string
	// SetupNewLark 留给测试注入 SDK 客户端；nil 用官方 SDK。
	SetupNewLark func(appID, appSecret string) LarkAPI

	client   LarkAPI
	userAuth *UserAuth

	handlers  chats.Handlers
	seenOrder []string

	seen   map[string]bool
	queues map[string]*serialQueue

	// tagDupAt 记一个 scope 的租户标签重名 ( 402 ) 何时试过：
	// 冷却内不再撞；控制台删掉孤儿标签后，冷却一过再建即成。
	tagDupAt map[string]time.Time

	youChecked time.Time
	youAllowed bool
}

// LarkAPI 是 Chat 用到的 SDK 面 ( 测试可替换；生产是 *lark.Client 的包装 )。
type LarkAPI interface {
	// SendMessage 发一条消息 ( receiveIDType: chat_id / open_id )，返回 message_id。
	SendMessage(ctx context.Context, receiveIDType, receiveID, msgType, content string) (string, error)
	// PatchMessage 编辑一条消息 ( 卡片 )。
	PatchMessage(ctx context.Context, messageID, content string) error
	// ReplyMessage 回复一条消息。
	ReplyMessage(ctx context.Context, messageID, msgType, content string) (string, error)
	// RecallMessage 撤回。
	RecallMessage(ctx context.Context, messageID string) error
	// DeleteChat 解散群 ( bot 是群主 )。
	DeleteChat(ctx context.Context, chatID string) error
	// ListMessages 补收：某群从 since 秒起的消息。
	ListMessages(ctx context.Context, chatID string, sinceSec int64) ([]ListedMessage, error)
	// CreateChat 建群 ( 拉 owner 入群 )。
	CreateChat(ctx context.Context, name, description string) (string, error)
	// RenameChat / SetChatAvatar 改名/头像。
	RenameChat(ctx context.Context, chatID, name string) error
	SetChatAvatar(ctx context.Context, chatID, imageKey string) error
	// UploadImage 上传图片 ( type: message / avatar )。
	UploadImage(ctx context.Context, data []byte, imageType string) (string, error)
	// UploadFile 上传文件 ( file_type "stream" )，返回 file_key。
	UploadFile(ctx context.Context, name string, data []byte) (string, error)
	// DownloadImage 下载消息里的图片。
	DownloadImage(ctx context.Context, messageID, fileKey string) ([]byte, error)
	// AddReaction / DeleteReaction 表情回执。
	AddReaction(ctx context.Context, messageID, emojiType string) (string, error)
	DeleteReaction(ctx context.Context, messageID, reactionID string) error
	// CreateTag / BindTag / UnbindTag 租户标签。
	CreateTag(ctx context.Context, name string) (string, error)
	BindTag(ctx context.Context, tagID, chatID string) error
	UnbindTag(ctx context.Context, tagID, chatID string) error
}

// ListedMessage 是补收列表里的一条。
type ListedMessage struct {
	MessageID  string
	ChatID     string
	MsgType    string
	Content    string
	CreateTime int64 // 毫秒
	SenderID   string
}

// serialQueue 串行执行一个 chat 的消息 ( 3 秒时限内先登记再处理 )。
// next 是"前一条做完"的信号 ( 做完即 close；nil = 队列已排空 )。
//
// 注意不是传函数的通道：
// 向它发送只会抱着 chat 的锁死在第一条消息上 ( 教训记在这 )。
type serialQueue struct {
	mu   sync.Mutex
	next chan struct{}
}

func (q *serialQueue) run(fn func()) {
	// 每条消息装上自己的完成信号、等前一条的：
	// 谁装的谁关，换手也不会关错别人的 ( 曾经这里关"此刻的 q.next"，前一条的信号从此没人发 )。
	q.mu.Lock()
	prev := q.next // 前一条的完成信号；nil = 队列空，第一跳不等
	done := make(chan struct{})
	q.next = done
	q.mu.Unlock()
	go func() {
		if prev != nil {
			<-prev
		}
		fn()
		close(done)
	}()
	<-done
}

// New 建一个飞书 Chat。
func New(appID, appSecret, ownerOpenID, profile string, store state.Store, stateDir string) *Chat {
	return &Chat{
		AppID: appID, AppSecret: appSecret, OwnerOpenID: ownerOpenID, Profile: profile,
		Store: store, StateDir: stateDir, BotTags: true, UserTags: true,
		Muted:  config.Spaces{All: true},
		seen:   map[string]bool{},
		queues: map[string]*serialQueue{},
	}
}

// --- 契约：元信息 ---
func (c *Chat) Name() string     { return "feishu" }
func (c *Chat) RoomWord() string { return "group" }
func (c *Chat) Ready() bool      { return c.OwnerOpenID != "" }
func (c *Chat) OwnerKey() string { return OwnerKey }

func (c *Chat) Help() []string {
	return []string{"`/auth` let the bot act as you for chat-list settings (mute, labels, pin)"}
}

func (c *Chat) LiveBytes() int   { return LiveBytes }
func (c *Chat) FoldedBytes() int { return FoldedBytes }

func (c *Chat) Split(markdown string) []string { return chunk(markdown, ChunkSize) }

// Requires 是 config.env 的必填键。
var Requires = []string{"FEISHU_APP_ID", "FEISHU_APP_SECRET"}

// OwnerKey 是 config.env 里记 owner 的键 ( 配对成功时写入 )。
const OwnerKey = "FEISHU_OWNER_OPEN_ID"

// --- 契约：启动 ---
func (c *Chat) Start(ctx context.Context, h chats.Handlers) error {
	c.mu.Lock()
	c.handlers = h
	c.mu.Unlock()

	if c.SetupNewLark != nil {
		c.client = c.SetupNewLark(c.AppID, c.AppSecret)
	} else {
		c.client = &sdkClient{cli: lark.NewClient(c.AppID, c.AppSecret), owner: c.OwnerOpenID}
	}
	c.userAuth = NewUserAuth(c.AppID, c.AppSecret, c.StateDir+"/user-token.json")

	if c.OwnerOpenID == "" {
		xlog.For("feishu").Info("owner 未设置：配对之前不建任何房间")
	}

	// 事件分发 ( 长连接 )。加密策略配了 Encrypt Key 时，密文事件在这里被解密
	// ( 未配置则两个值为空串，行为与从前一致 )。
	// 三个入口都留一行探针日志：事件到没到，一眼可见。
	d := dispatcher.NewEventDispatcher(c.VerificationToken, c.EncryptKey).
		OnP2MessageReceiveV1(func(_ context.Context, event *larkim.P2MessageReceiveV1) error {
			xlog.For("feishu").Info("事件：收到消息")
			c.takeMessage(ctx, messageReceiveToMap(event))
			return nil
		}).OnP2ChatAccessEventBotP2pChatEnteredV1(func(_ context.Context, event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error {
		xlog.For("feishu").Info("事件：有人进入机器人会话")
		return nil
	}).OnP2CardActionTrigger(func(_ context.Context, card *larkcallback.CardActionTriggerEvent) (*larkcallback.CardActionTriggerResponse, error) {
		xlog.For("feishu").Info("事件：卡片点击")
		res := c.onCardAction(cardActionToMap(card))
		var toast *larkcallback.Toast
		if t, ok := res["toast"].(map[string]any); ok {
			toast = &larkcallback.Toast{Type: str(t["type"]), Content: str(t["content"])}
		}
		resp := &larkcallback.CardActionTriggerResponse{Toast: toast}
		if cardJSON, ok := res["card"].(map[string]any); ok {
			resp.Card = &larkcallback.Card{Type: "raw", Data: cardJSON}
		}
		return resp, nil
	})

	wsClient := larkws.NewClient(c.AppID, c.AppSecret,
		larkws.WithEventHandler(d),
		larkws.WithOnReady(func() {
			xlog.For("feishu").Info("connected")
			if h.OnReady != nil {
				h.OnReady()
			}
			go c.catchUp(ctx)
		}),
		larkws.WithOnReconnected(func() {
			xlog.For("feishu").Info("reconnected")
			go c.catchUp(ctx)
		}),
	)
	go func() {
		if err := wsClient.Start(ctx); err != nil {
			xlog.For("feishu").Error("长连接结束", "error", err)
		}
	}()

	// 每 60s 检查 /auth 是否还可用。
	go func() {
		t := time.NewTicker(youCheckEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.asYou()
			}
		}
	}()
	c.asYou()
	return nil
}

// keysOf 列一个 map 的键 ( 探针用 )。
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// takeMessage 收到一条消息 ( 去重、按 chat 排队 )。
func (c *Chat) takeMessage(ctx context.Context, data map[string]any) {
	msg, _ := data["message"].(map[string]any)
	if msg == nil {
		xlog.For("feishu").Info("探针：message 字段缺失", "keys", keysOf(data))
		return
	}
	messageID, _ := msg["message_id"].(string)
	if messageID == "" || c.alreadySeen(messageID) {
		xlog.For("feishu").Info("探针：message_id 为空或已见过", "message_id", messageID)
		return
	}
	chatID, _ := msg["chat_id"].(string)
	c.enqueue(chatID, func() {
		c.handleMessage(ctx, data)
		c.markRead(chatID, nowMs(msg["create_time"]))
	})
}

func (c *Chat) alreadySeen(messageID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[messageID] {
		return true
	}
	c.seen[messageID] = true
	c.seenOrder = append(c.seenOrder, messageID)
	if len(c.seenOrder) > 2000 {
		delete(c.seen, c.seenOrder[0])
		c.seenOrder = c.seenOrder[1:]
	}
	return false
}

func (c *Chat) enqueue(chatID string, fn func()) {
	c.mu.Lock()
	q, ok := c.queues[chatID]
	if !ok {
		// next 为 nil：无前驱，run 里第一跳就不等。
		q = &serialQueue{}
		c.queues[chatID] = q
		c.mu.Unlock()
		q.run(fn)
		return
	}
	c.mu.Unlock()
	q.run(fn)
}

func nowMs(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		ms, _ := strconv.ParseInt(n, 10, 64)
		return ms
	}
	return time.Now().UnixMilli()
}

// handleMessage 把一条飞书消息变成引擎的形状。
func (c *Chat) handleMessage(ctx context.Context, data map[string]any) {
	msg, _ := data["message"].(map[string]any)
	sender, _ := data["sender"].(map[string]any)
	senderID, _ := sender["sender_id"].(map[string]any)
	openID, _ := senderID["open_id"].(string)
	chatID, _ := msg["chat_id"].(string)
	messageType, _ := msg["message_type"].(string)
	content, _ := msg[keyContent].(string)

	if c.OwnerOpenID == "" {
		// 配对：直聊里的第一条非斜杠消息。
		if chatType, _ := msg["chat_type"].(string); chatType != chatP2P {
			xlog.For("feishu").Info("探针：非 p2p 聊天，配对分支不收", "chat_type", chatType)
			return
		}
		text, _, _ := MessageParts(messageType, content)
		xlog.For("feishu").Info("探针：配对消息进来", "message_type", messageType, "text_len", len(text))
		if strings.HasPrefix(strings.TrimSpace(text), "/") {
			return
		}
		// 配对由引擎的 tryPairing 处理 ( 直聊发过来的 6 位码 )。
		c.mu.Lock()
		h := c.handlers
		c.mu.Unlock()
		if h.OnMessage != nil {
			h.OnMessage(core.Incoming{
				Where:   core.Where{Room: core.Room(chatID), Message: core.Message(str(msg["message_id"]))},
				Message: core.Message(str(msg["message_id"])), Text: text, Sender: openID,
			})
		}
		return
	}
	if openID != c.OwnerOpenID {
		xlog.For("feishu").Warn("忽略非 owner 的消息", "from", openID)
		return
	}
	text, images, ok := MessageParts(messageType, content)
	if !ok {
		_, _ = c.Reply(ctx, core.Where{Room: core.Room(chatID)}, fmt.Sprintf(
			"Not delivered: only text and images are passed on, not this (%s). Copy its text into a message.", messageType))
		return
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "@_user_1", ""))
	for i := 2; i < 10; i++ {
		text = strings.TrimSpace(strings.ReplaceAll(text, fmt.Sprintf("@_user_%d", i), ""))
	}
	if text == "" && len(images) == 0 {
		return
	}
	chatType, _ := msg["chat_type"].(string)
	var room core.Room
	if chatType != chatP2P {
		room = core.Room(chatID)
	}
	c.mu.Lock()
	h := c.handlers
	c.mu.Unlock()
	if h.OnMessage != nil {
		h.OnMessage(core.Incoming{
			Room: room, Where: core.Where{Room: core.Room(chatID), Message: core.Message(str(msg["message_id"]))},
			Message: core.Message(str(msg["message_id"])), Text: text, Images: imageRefsToBytes(images),
			Sender: openID,
		})
	}
}

// imageRefsToBytes 把飞书的 image_key 变成引擎期望的图片引用标记 ( 下载在 Download 里按 ref 落地 )。
func imageRefsToBytes(refs []string) [][]byte {
	out := make([][]byte, 0, len(refs))
	for _, r := range refs {
		out = append(out, []byte(r))
	}
	return out
}

// markRead 记下该群已处理到的时刻 ( 补收的水位 )。
func (c *Chat) markRead(chatID string, at int64) {
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	st, _ := c.Store.Read()
	if st.FeishuRead == nil {
		st.FeishuRead = map[string]int64{}
	}
	if st.FeishuRead[chatID] >= at {
		return
	}
	st.FeishuRead[chatID] = at
	_ = c.Store.Write(st)
}

// catchUp 补收停机期间漏掉的 owner 消息。
func (c *Chat) catchUp(ctx context.Context) {
	if c.OwnerOpenID == "" || c.client == nil {
		return
	}
	st, _ := c.Store.Read()
	chatsSet := map[string]bool{}
	for _, room := range st.Chats {
		if !room.Ended {
			chatsSet[room.Chat] = true
		}
	}
	for chatID := range st.FeishuRead {
		chatsSet[chatID] = true
	}
	for chatID := range chatsSet {
		since := st.FeishuRead[chatID]
		if since == 0 {
			c.markRead(chatID, time.Now().UnixMilli())
			continue
		}
		missed, err := c.client.ListMessages(ctx, chatID, since/1000)
		if err != nil {
			xlog.For("feishu").Warn("补收失败", "chat", chatID, "error", err)
			continue
		}
		for _, m := range missed {
			if m.CreateTime <= since || m.SenderID != c.OwnerOpenID || c.alreadySeen(m.MessageID) {
				continue
			}
			c.enqueue(chatID, func() {
				c.handleMessage(ctx, map[string]any{
					"message": map[string]any{
						"message_id": m.MessageID, "chat_id": chatID,
						"chat_type": chatTypeOf(st, chatID), "message_type": m.MsgType,
						keyContent: m.Content, "create_time": m.CreateTime,
					},
					"sender": map[string]any{"sender_id": map[string]any{"open_id": m.SenderID}},
				})
				c.markRead(chatID, m.CreateTime)
			})
		}
	}
}

func chatTypeOf(st state.State, chatID string) string {
	for _, room := range st.Chats {
		if room.Chat == chatID {
			return "group"
		}
	}
	return chatP2P
}

// onCardAction 处理卡片点击 ( 3 秒内应答 toast 与新卡 )。
func (c *Chat) onCardAction(data map[string]any) map[string]any {
	operator, _ := data["operator"].(map[string]any)
	openID, _ := operator["open_id"].(string)
	if openID != c.OwnerOpenID {
		return map[string]any{kToast: map[string]any{kType: toastError, kContent: "Only the owner can answer."}}
	}
	action, _ := data["action"].(map[string]any)
	value, _ := action["value"].(map[string]any)
	form, _ := action["form_value"].(map[string]any)
	var ticks map[string]bool
	if pick, _ := value["pick"].(string); pick == "tab" && form != nil {
		ticks = TicksOf(form)
		if ticks == nil {
			return map[string]any{kToast: map[string]any{kType: toastError, "content": "No ticked box came through, so nothing was sent. Tick at least one, or answer it in herdr-app."}}
		}
	}
	answer, _ := form["answer"].(string)
	fields := map[string]string{}
	for k, v := range form {
		if s, ok := v.(string); ok {
			fields[k] = s
		}
	}
	contextMap, _ := data["context"].(map[string]any)
	messageID, _ := contextMap["open_message_id"].(string)

	c.mu.Lock()
	h := c.handlers
	c.mu.Unlock()

	out, err := h.OnAction(core.Click{Value: core.DialogValue(value), Message: core.Message(messageID), Answer: answer, Form: fields})
	// ticks 已在 value 里 ( form_value 的 box_<n> 键 )，引擎由 value["ticks"] 读。
	_ = ticks
	if err != nil {
		return map[string]any{kToast: map[string]any{kType: toastError, "content": truncate(err.Error(), 200)}}
	}
	res := map[string]any{kToast: map[string]any{kType: "info", kContent: out.Toast}}

	// 回调响应里的新卡是唯一能换掉按钮/表单的通道 ( PATCH 只能改展示内容， // 交互组件动不了，飞书文档明说 )。
	// 原地刷新与关卡都靠它。
	if out.Card != nil {
		res["card"] = PlainCard(out.Card.Markdown, out.Card.Title, out.Card.Tone, out.Card.Footer)
	}
	return res
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// asYou 检查 /auth 是否还可用 ( 60s 缓存 )；打开时把所有群归位。
func (c *Chat) asYou() bool {
	c.mu.Lock()
	if time.Since(c.youChecked) < youCheckEvery {
		allowed := c.youAllowed
		c.mu.Unlock()
		return allowed
	}
	c.mu.Unlock()

	checkCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	allowed, err := c.userAuth.Allowed(checkCtx)
	cancel()
	if err != nil {
		xlog.For("feishu").Warn("以你行事：未检查 ( 维持现状 )", "error", err)
		c.mu.Lock()
		c.youChecked = time.Now().Add(-youCheckEvery + 10*time.Second)
		c.mu.Unlock()
		c.mu.Lock()
		return c.youAllowed
	}

	c.mu.Lock()
	was := c.youAllowed
	c.youAllowed = allowed
	c.youChecked = time.Now()
	c.mu.Unlock()

	if allowed && !was {
		xlog.For("feishu").Info("以你行事：开")
	} else if !allowed && was {
		xlog.For("feishu").Warn("以你行事：关")
	}
	return allowed
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// --- 契约：房间 ---

func (c *Chat) OpenRoom(ctx context.Context, look core.RoomLook) (core.Room, error) {
	chatID, err := c.client.CreateChat(ctx, truncate(look.Name, 60), look.Intro)
	if err != nil {
		return "", err
	}
	return core.Room(chatID), nil
}

func (c *Chat) RenameRoom(ctx context.Context, room core.Room, name string) error {
	return c.client.RenameChat(ctx, string(room), truncate(name, 60))
}

func (c *Chat) PlaceRoom(ctx context.Context, k string, place core.Place) error {
	// 静态装饰：租户标签 ( 250ms 串行队列 )。头像与用户标签随 /auth。
	if !c.BotTags {
		return nil
	}
	st, _ := c.Store.Read()
	room, ok := st.Chats[k]
	if !ok {
		return nil
	}
	scope := place.Session
	if place.Machine != "" {
		scope = place.Machine
	}
	tagID, err := c.tagOf(ctx, scope)
	if err != nil {
		return err
	}
	if tagID == "" {
		return nil // 名字被占而拿不到 id：不绑标签 ( tagOf 说过一声了 )
	}
	if room.Tag == tagID {
		return nil
	}
	if room.Tag != "" {
		_ = c.client.UnbindTag(ctx, room.Tag, room.Chat)
	}
	if err := c.client.BindTag(ctx, tagID, room.Chat); err != nil {
		return err
	}
	room.Tag = tagID
	st, _ = c.Store.Read()
	if _, ok := st.Chats[k]; ok {
		roomCopy := room
		st.Chats[k] = mergeRoom(st.Chats[k], roomCopy)
		_ = c.Store.Write(st)
	}
	return nil
}

// mergeRoom 把新字段合并进已有记录 ( Tag 等装饰字段不覆盖 Chat/Name )。
func mergeRoom(old, deco state.Room) state.Room {
	old.Tag = deco.Tag
	return old
}

// tagDupCooldown：标签重名后隔多久再试 ( 控制台删掉孤儿标签即自愈 )。
const tagDupCooldown = time.Hour

// tagOf 建/取一个 scope 的租户标签 ( id 存 state.tags )。
// 同名标签占了名字而拿不到 id ( ErrDuplicateTag ) 时装饰退位：
// 返回空 id、不报错、不绑标签，也绝不拦住房间的其他事；
// 冷却一过再试 ( tagDupAt )。
func (c *Chat) tagOf(ctx context.Context, scope string) (string, error) {
	st, _ := c.Store.Read()
	if st.Tags == nil {
		st.Tags = map[string]string{}
	}
	if id, ok := st.Tags[scope]; ok {
		return id, nil
	}
	c.mu.Lock()
	if c.tagDupAt == nil {
		c.tagDupAt = map[string]time.Time{}
	}
	if at, tried := c.tagDupAt[scope]; tried && time.Since(at) < tagDupCooldown {
		c.mu.Unlock()
		return "", nil
	}
	c.mu.Unlock()

	tagID, err := c.client.CreateTag(ctx, TagName(scope))
	if err != nil {
		var dup *ErrDuplicateTag
		if errors.As(err, &dup) {
			c.mu.Lock()
			c.tagDupAt[scope] = time.Now()
			c.mu.Unlock()
			xlog.For("feishu").Warn("租户标签重名，装饰退位 ( 删掉控制台里的同名标签即自愈 )",
				"scope", scope, "tag", dup.Name, "retry", tagDupCooldown.String())
			return "", nil
		}
		return "", err
	}
	c.mu.Lock()
	delete(c.tagDupAt, scope)
	c.mu.Unlock()
	st, _ = c.Store.Read()
	if st.Tags == nil {
		st.Tags = map[string]string{}
	}
	st.Tags[scope] = tagID
	_ = c.Store.Write(st)
	return tagID, nil
}

func (c *Chat) EndRoom(ctx context.Context, k string) error {
	if !c.BotTags {
		return nil
	}
	st, _ := c.Store.Read()
	room, ok := st.Chats[k]
	if !ok || room.EndedTag {
		return nil
	}
	tagID, err := c.tagOf(ctx, "ended")
	if err != nil {
		return err
	}
	if tagID == "" {
		return nil // 同上：装饰退位，房间照常结束
	}
	if err := c.client.BindTag(ctx, tagID, room.Chat); err != nil {
		return err
	}
	room.EndedTag = true
	st, _ = c.Store.Read()
	if _, still := st.Chats[k]; still {
		kept := st.Chats[k]
		kept.EndedTag = true
		st.Chats[k] = kept
		_ = c.Store.Write(st)
	}
	return nil
}

// DeleteRoom 彻底删掉一个房间 ( /clean )：解散群。state 由引擎摘除。
func (c *Chat) DeleteRoom(k string) error {
	st, _ := c.Store.Read()
	room, ok := st.Chats[k]
	if !ok {
		return nil // 已不在：幂等
	}
	if room.Tag != "" || room.EndedTagID != "" {
		// 标签随群解散失效，state.tags 里的 id 留着给下一个同 scope 群复用。
		_ = room.Tag
	}
	return c.client.DeleteChat(context.Background(), room.Chat)
}

func (c *Chat) ReviveRoom(ctx context.Context, k string) error {
	if !c.BotTags {
		return nil
	}
	st, _ := c.Store.Read()
	room, ok := st.Chats[k]
	if !ok || !room.EndedTag {
		return nil
	}
	if room.EndedTagID != "" {
		_ = c.client.UnbindTag(ctx, room.EndedTagID, room.Chat)
	} else if endedID, has := st.Tags["ended"]; has {
		_ = c.client.UnbindTag(ctx, endedID, room.Chat)
	}
	room.EndedTag = false
	st, _ = c.Store.Read()
	if _, still := st.Chats[k]; still {
		kept := st.Chats[k]
		kept.EndedTag = false
		st.Chats[k] = kept
		_ = c.Store.Write(st)
	}
	return nil
}

// --- 契约：发帖 ---

func (c *Chat) Post(ctx context.Context, room core.Room, markdown, footer string) (core.Message, error) {
	return c.sendMarkdown(ctx, string(room), markdown, footer)
}

func (c *Chat) Reply(ctx context.Context, where core.Where, markdown string) (core.Message, error) {
	if where.Message != "" {
		// 回复 ( 文本 )。
		parts := chunk(markdown, ChunkSize)
		var last core.Message
		for _, part := range parts {
			content, _ := json.Marshal(map[string]string{"text": part})
			id, err := c.client.ReplyMessage(ctx, string(where.Message), "text", string(content))
			if err != nil {
				return last, err
			}
			last = core.Message(id)
		}
		return last, nil
	}
	return c.sendMarkdown(ctx, string(where.Room), markdown, "")
}

func (c *Chat) sendMarkdown(ctx context.Context, chatID, text, footer string) (core.Message, error) {
	if text == "" {
		text = "(empty)"
	}
	idType, id := splitChatID(chatID)
	var last core.Message
	parts := chunk(text, ChunkSize)
	for i, part := range parts {
		foot := ""
		if i == len(parts)-1 {
			foot = footer
		}
		// 卡片 ( markdown )；被拒则纯文本。
		cardJSON := PlainCard(part, "", core.ToneWorking, foot)

		// 无标题的纯 markdown 卡。
		delete(cardJSON, "header")
		contentBytes, _ := json.Marshal(cardJSON)
		sentID, err := c.client.SendMessage(ctx, idType, id, "interactive", string(contentBytes))
		if err != nil {
			textContent, _ := json.Marshal(map[string]string{kText: part + foot})
			sentID, err = c.client.SendMessage(ctx, idType, id, "text", string(textContent))
			if err != nil {
				return last, err
			}
		}
		last = core.Message(sentID)
	}
	return last, nil
}

func splitChatID(chatID string) (idType, id string) {
	if rest, ok := strings.CutPrefix(chatID, "open_id:"); ok {
		return idOpen, rest
	}
	return idChat, chatID
}

func (c *Chat) PostCard(ctx context.Context, room core.Room, look core.Look) (core.Message, error) {
	cardJSON := PlainCard(look.Markdown, look.Title, look.Tone, look.Footer)
	contentBytes, _ := json.Marshal(cardJSON)
	id, err := c.client.SendMessage(ctx, idChat, string(room), "interactive", string(contentBytes))
	return core.Message(id), err
}

func (c *Chat) EditCard(ctx context.Context, msg core.Message, look core.Look) error {
	cardJSON := PlainCard(look.Markdown, look.Title, look.Tone, look.Footer)
	contentBytes, _ := json.Marshal(cardJSON)
	return c.client.PatchMessage(ctx, string(msg), string(contentBytes))
}

func (c *Chat) PostDialog(ctx context.Context, room core.Room, d core.Dialog, v core.DialogValue) (core.Message, error) {
	value, _ := v.(map[string]any)
	if value == nil {
		value = map[string]any{}
	}
	var cardJSON map[string]any
	switch {
	case len(d.Selects) > 0 && d.Menu != nil:
		placeholder := ""
		if d.Menu.Free != nil {
			placeholder = d.Menu.Free.Label
		}
		cardJSON = NewCard(d.Title, d.Markdown, placeholder, d.Footer, d.Submit, d.Selects, value)
	case d.Menu != nil:
		cardJSON = MenuCard(d.Title, d.Markdown, d.Menu, value, d.Footer)
	default:
		cardJSON = PlainCard(d.Markdown, d.Title, core.ToneNeeds, d.Footer)
	}
	contentBytes, _ := json.Marshal(cardJSON)
	id, err := c.client.SendMessage(ctx, idChat, string(room), "interactive", string(contentBytes))
	return core.Message(id), err
}

func (c *Chat) CloseDialog(ctx context.Context, msg core.Message, d core.Dialog, done string) (err error) {
	cardJSON := DoneCard(d.DoneTitle, d.Markdown, done, d.Footer)
	contentBytes, _ := json.Marshal(cardJSON)
	return c.client.PatchMessage(ctx, string(msg), string(contentBytes))
}

// --- 契约：提醒 ---

func (c *Chat) Pin(ctx context.Context, k string, room core.Room, msg core.Message) error {
	if !c.asYou() {
		return nil
	}
	if err := c.userAuth.Pin(ctx, string(room)); err != nil {
		return err
	}
	st, _ := c.Store.Read()
	if st.Pinned == nil {
		st.Pinned = map[string]string{}
	}
	st.Pinned[k] = string(room)
	return c.Store.Write(st)
}

func (c *Chat) Unpin(ctx context.Context, k string) error {
	st, _ := c.Store.Read()
	room := st.Pinned[k]
	if room == "" {
		return nil
	}
	_ = c.userAuth.Unpin(ctx, room)
	delete(st.Pinned, k)
	return c.Store.Write(st)
}

func (c *Chat) Pinned() []string {
	st, _ := c.Store.Read()
	var out []string
	for k := range st.Pinned {
		out = append(out, k)
	}
	return out
}

func (c *Chat) Ring(ctx context.Context, k string, msg core.Message, text string) error {
	// 静音的群里只有 @ 提及才响；未静音的群本来就响。
	// 完全静默的 space 连 @ 都不发。
	if !c.asYou() {
		return nil
	}
	st, _ := c.Store.Read()
	room, ok := st.Chats[k]
	if !ok || !room.Muted {
		return nil
	}
	space := spaceOfName(room.Name)
	if c.Silent.All || containsSpace(c.Silent.List, space) {
		return nil
	}
	content, _ := json.Marshal(map[string]string{
		"text": "<at user_id=\"" + c.OwnerOpenID + "\"></at> " + text,
	})
	_, err := c.client.ReplyMessage(ctx, string(msg), "text", string(content))
	return err
}

func spaceOfName(name string) string {
	parts := strings.SplitN(name, " / ", 2)
	return parts[0]
}

func containsSpace(list []string, space string) bool {
	for _, s := range list {
		if s == space {
			return true
		}
	}
	return false
}

func (c *Chat) MarkWorking(ctx context.Context, msg core.Message) (string, error) {
	return c.client.AddReaction(ctx, string(msg), workingEmoji)
}

func (c *Chat) MarkDone(ctx context.Context, msg core.Message, token string) error {
	if token != "" {
		_ = c.client.DeleteReaction(ctx, string(msg), token)
	}
	_, err := c.client.AddReaction(ctx, string(msg), doneEmoji)
	return err
}

// --- 契约：媒体 ---

func (c *Chat) Image(ctx context.Context, data []byte) (string, error) {
	key, err := c.client.UploadImage(ctx, data, "message")
	if err != nil {
		return "", err
	}
	return "![image](" + key + ")", nil
}

func (c *Chat) PostFile(ctx context.Context, room core.Room, f core.FilePost) (core.Message, error) {
	key, err := c.client.UploadFile(ctx, f.Name, f.Data)
	if err != nil {
		return "", err
	}
	content, _ := json.Marshal(map[string]string{"file_key": key})
	id, err := c.client.SendMessage(ctx, idChat, string(room), "file", string(content))
	return core.Message(id), err
}

func (c *Chat) PostFolded(ctx context.Context, room core.Room, f core.FoldedPost) (core.Message, error) {
	cardJSON := FoldedCard(f.Title, f.Markdown)
	contentBytes, _ := json.Marshal(cardJSON)
	id, err := c.client.SendMessage(ctx, idChat, string(room), "interactive", string(contentBytes))
	if err != nil {
		// 折叠卡被拒 → 普通帖 ( 绝不丢失 )。
		xlog.For("feishu").Warn("折叠卡被拒，改发普通帖", "error", err)
		return c.Post(ctx, room, "**"+f.Title+"**\n\n"+f.Markdown, "")
	}
	return core.Message(id), nil
}

func (c *Chat) Download(ctx context.Context, msg core.Message, ref, file string) error {
	// ref 是 image_key ( 引擎把 refs 传成 image_key 字符串 )。
	data, err := c.client.DownloadImage(ctx, string(msg), ref)
	if err != nil {
		return err
	}
	return writeFile(file, data, 0o600)
}

// --- 契约：owner ---

func (c *Chat) TellOwner(ctx context.Context, look core.Look) (err error) {
	if c.OwnerOpenID == "" || c.client == nil {
		return fmt.Errorf("tell owner: no owner, or the chat is not started yet")
	}
	cardJSON := PlainCard(look.Markdown, look.Title, look.Tone, look.Footer)
	contentBytes, _ := json.Marshal(cardJSON)
	_, err = c.client.SendMessage(ctx, idOpen, c.OwnerOpenID, "interactive", string(contentBytes))
	return err
}

func (c *Chat) ShowOverview(ctx context.Context, rows []core.OverviewRow, opts core.OverviewOpts) error {
	if c.OwnerOpenID == "" {
		return nil
	}
	st, _ := c.Store.Read()
	old := ""
	if st.Overview != nil {
		old = st.Overview.Message
	}
	cardJSON := overviewCard(rows, time.Now())
	contentBytes, _ := json.Marshal(cardJSON)
	if old != "" && !opts.Fresh {
		if err := c.client.PatchMessage(ctx, old, string(contentBytes)); err == nil {
			return nil
		} else {
			xlog.For("feishu").Warn("总览编辑失败，重试或新发", "error", err)
		}
		for attempt := 1; attempt < overviewTries; attempt++ {
			time.Sleep(time.Duration(1<<attempt) * time.Second)
			if err := c.client.PatchMessage(ctx, old, string(contentBytes)); err == nil {
				return nil
			}
		}
	}
	id, err := c.client.SendMessage(ctx, idOpen, c.OwnerOpenID, "interactive", string(contentBytes))
	if err != nil {
		return err
	}
	st, _ = c.Store.Read()
	st.Overview = &state.OverviewRef{Message: id}
	if writeErr := c.Store.Write(st); writeErr != nil {
		return writeErr
	}
	if old != "" {
		_ = c.client.RecallMessage(ctx, old)
	}
	return nil
}

// Command 是飞书自己的直聊命令：/auth 走设备流。
func (c *Chat) Command(cmd string, args []string, where core.Where) (bool, error) {
	ctx := context.Background()
	if cmd != "/auth" {
		return false, nil
	}
	device, err := c.userAuth.StartDeviceFlow(ctx)
	if err != nil {
		_, _ = c.Reply(ctx, where, "Not allowed: "+err.Error())
		return true, nil //nolint:nilerr // 授权失败已告知用户，命令本身处理成功
	}
	link := device.VerificationURIComplete
	if link == "" {
		link = device.VerificationURI
	}
	_, _ = c.Reply(ctx, where, fmt.Sprintf(
		"Open this link and allow it (the code is %s):\n\n%s\n\nIt lets the bot mute its groups for you, put them in your labels and pin the one that needs you. Nothing else.",
		device.UserCode, link))
	go func() {
		if _, err := c.userAuth.WaitForToken(ctx, device); err != nil {
			_, _ = c.Reply(ctx, where, "Not allowed: "+err.Error())
			return
		}
		_, _ = c.Reply(ctx, where, "✅ Allowed. Muting the groups and putting them in your labels now.")
		c.mu.Lock()
		c.youChecked = time.Time{}
		c.mu.Unlock()
	}()
	return true, nil
}

// 编译期：是 chat app。
var _ chats.Chat = (*Chat)(nil)

// bytesReader 给 json.Marshal 之后的 io 使用留个引用。
var _ io.Reader = bytes.NewReader(nil)
