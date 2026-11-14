package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/jrmarcco/herdr-chat/internal/core"
)

const (
	stateBlocked = "blocked"
	stateWorking = "working"
)

// 总览卡片 ( 移植自原版 overview.mjs )：一行一个终端，状态彩签、群链接、
// 从何时起；等着你的排最前。

var stateWords = map[string]struct {
	Color string
	Word  string
	Rank  int
}{
	stateBlocked: {"orange", "needs you", 0},
	"error":      {"red", "error", 1},
	stateWorking: {"blue", stateWorking, 2},
	"done":       {"green", "done", 3},
	"idle":       {"neutral", "idle", 4},
}

const otherRank = 5

func chatLink(chat string) string {
	return "https://applink.feishu.cn/client/chat/open?openChatId=" + chat
}

// overviewText 排总览的文字。
func overviewText(rows []core.OverviewRow) string {
	sorted := make([]core.OverviewRow, len(rows))
	copy(sorted, rows)
	// 先按状态 rank，再按名字。
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			si, sj := stateWords[sorted[i].State], stateWords[sorted[j].State]
			if si.Rank == 0 && sorted[i].State == "" {
				si.Rank = otherRank
			}
			if sj.Rank == 0 && sorted[j].State == "" {
				sj.Rank = otherRank
			}
			if si.Rank > sj.Rank || (si.Rank == sj.Rank && sorted[i].Name > sorted[j].Name) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	if len(sorted) == 0 {
		return "No terminal is shown in herdr-app."
	}
	var lines []string
	for _, r := range sorted {
		s, ok := stateWords[r.State]
		if !ok {
			s = struct {
				Color string
				Word  string
				Rank  int
			}{"neutral", "", otherRank}
		}
		name := escapeOverview(r.Name)
		if r.Room != "" {
			name = "[" + name + "](" + chatLink(string(r.Room)) + ")"
		}
		word := s.Word
		if s.Rank == otherRank {
			if r.Program != "" {
				word = r.Program
			} else {
				word = "no status"
			}
		} else if r.State == stateBlocked && r.Kind != "" {
			word += " (" + r.Kind + ")"
		}
		line := "<text_tag color='" + s.Color + "'>" + tagSafe(word) + "</text_tag> **" + name + "**"
		if r.Since != "" {
			line += " · since " + r.Since
		}
		line += " · `" + r.Address + "`"
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// escapeOverview 把 markdown 记号写成 HTML 实体 ( 链接文本里飞书显示反斜杠转义 )。
func escapeOverview(text string) string {
	return footerEsc.ReplaceAllStringFunc(text, func(c string) string {
		return fmt.Sprintf("&#%d;", c[0])
	})
}

// tagSafe 只去会终结 text_tag 的字符。
func tagSafe(text string) string {
	return strings.NewReplacer("<", "", ">", "").Replace(text)
}

// overviewCard 是总览的卡：等待的橙色、只有工作的蓝色、都不错绿色。
func overviewCard(rows []core.OverviewRow, now time.Time) map[string]any {
	waiting, working := 0, 0
	for _, r := range rows {
		switch r.State {
		case stateBlocked:
			waiting++
		case stateWorking:
			working++
		}
	}
	tone := core.ToneGood
	if working > 0 {
		tone = core.ToneWorking
	}
	if waiting > 0 {
		tone = core.ToneNeeds
	}
	title := fmt.Sprintf("herdrchat · %d terminals", len(rows))
	if waiting > 0 {
		title = fmt.Sprintf("%d needs you · %s", waiting, title)
	}
	return map[string]any{
		kSchema:  kSchemaV,
		"config": map[string]any{"update_multi": true},
		"header": cardHeader(title, tone),
		"body": map[string]any{"elements": []any{
			map[string]any{"tag": "markdown", kContent: overviewText(rows)},
			map[string]any{"tag": "markdown", kContent: "Updated " + now.Format("15:04"), kTextSize: kNotation},
		}},
	}
}

// sdkClient 把官方 SDK 包成 LarkAPI。
type sdkClient struct {
	cli *lark.Client
}

func (s *sdkClient) SendMessage(ctx context.Context, receiveIDType, receiveID, msgType, content string) (string, error) {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(receiveID).
			MsgType(msgType).
			Content(content).
			Build()).
		Build()
	resp, err := s.cli.Im.Message.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("feishu message.create %d: %s", resp.Code, resp.Msg)
	}
	return *resp.Data.MessageId, nil
}

func (s *sdkClient) PatchMessage(ctx context.Context, messageID, content string) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().Content(content).Build()).
		Build()
	resp, err := s.cli.Im.Message.Patch(ctx, req)
	if err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("feishu message.patch %d: %s", resp.Code, resp.Msg)
	}
	return nil
}

func (s *sdkClient) ReplyMessage(ctx context.Context, messageID, msgType, content string) (string, error) {
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType(msgType).
			Content(content).
			Build()).
		Build()
	resp, err := s.cli.Im.Message.Reply(ctx, req)
	if err != nil {
		return "", err
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("feishu message.reply %d: %s", resp.Code, resp.Msg)
	}
	return *resp.Data.MessageId, nil
}

func (s *sdkClient) RecallMessage(ctx context.Context, messageID string) error {
	req := larkim.NewDeleteMessageReqBuilder().MessageId(messageID).Build()
	resp, err := s.cli.Im.Message.Delete(ctx, req)
	if err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("feishu message.delete %d: %s", resp.Code, resp.Msg)
	}
	return nil
}

func (s *sdkClient) ListMessages(ctx context.Context, chatID string, sinceSec int64) ([]ListedMessage, error) {
	req := larkim.NewListMessageReqBuilder().
		ContainerIdType("chat").
		ContainerId(chatID).
		StartTime(fmt.Sprint(sinceSec)).
		SortType("ByCreateTimeAsc").
		PageSize(50).
		Build()
	resp, err := s.cli.Im.Message.List(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("feishu message.list %d: %s", resp.Code, resp.Msg)
	}
	var out []ListedMessage
	for _, item := range resp.Data.Items {
		senderID := ""
		if item.Sender != nil && item.Sender.Id != nil {
			senderID = *item.Sender.Id
		}
		content := "{}"
		if item.Body != nil && item.Body.Content != nil {
			content = *item.Body.Content
		}
		msgType := ""
		if item.MsgType != nil {
			msgType = *item.MsgType
		}
		created := int64(0)
		if item.CreateTime != nil {
			created, _ = strconv.ParseInt(*item.CreateTime, 10, 64)
		}
		out = append(out, ListedMessage{
			MessageID: *item.MessageId, ChatID: chatID, MsgType: msgType,
			Content: content, CreateTime: created, SenderID: senderID,
		})
	}
	return out, nil
}

// bytesReader 把 []byte 变 SDK 要的 io.Reader。
func bytesReader(data []byte) io.Reader { return bytes.NewReader(data) }

// ioReadAll 读完一个响应文件。
func ioReadAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

// messageReceiveToMap 把 SDK 的事件结构变回引擎用的 map。
func messageReceiveToMap(event *larkim.P2MessageReceiveV1) map[string]any {
	if event == nil || event.Event == nil {
		return map[string]any{}
	}
	raw, _ := json.Marshal(event.Event)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return map[string]any{
		"message": m["message"],
		"sender":  m["sender"],
	}
}

func (s *sdkClient) CreateChat(ctx context.Context, name, description string) (string, error) {
	ownerID := "" // 由 Chat 注入；这里读不到，走 s.ownerOpenID
	_ = ownerID
	return "", fmt.Errorf("CreateChat 需要 owner：走 Chat.OpenRoom")
}

func (s *sdkClient) RenameChat(ctx context.Context, chatID, name string) error {
	req := larkim.NewUpdateChatReqBuilder().
		ChatId(chatID).
		Body(larkim.NewUpdateChatReqBodyBuilder().Name(name).Build()).
		Build()
	resp, err := s.cli.Im.Chat.Update(ctx, req)
	if err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("feishu chat.update %d: %s", resp.Code, resp.Msg)
	}
	return nil
}

func (s *sdkClient) SetChatAvatar(ctx context.Context, chatID, imageKey string) error {
	req := larkim.NewUpdateChatReqBuilder().
		ChatId(chatID).
		Body(larkim.NewUpdateChatReqBodyBuilder().Avatar(imageKey).Build()).
		Build()
	resp, err := s.cli.Im.Chat.Update(ctx, req)
	if err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("feishu chat.update avatar %d: %s", resp.Code, resp.Msg)
	}
	return nil
}

func (s *sdkClient) UploadImage(ctx context.Context, data []byte, imageType string) (string, error) {
	req := larkim.NewCreateImageReqBuilder().
		Body(larkim.NewCreateImageReqBodyBuilder().
			ImageType(imageType).
			Image(bytesReader(data)).
			Build()).
		Build()
	resp, err := s.cli.Im.Image.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("feishu image.create %d: %s", resp.Code, resp.Msg)
	}
	return *resp.Data.ImageKey, nil
}

func (s *sdkClient) UploadFile(ctx context.Context, name string, data []byte) (string, error) {
	req := larkim.NewCreateFileReqBuilder().
		Body(larkim.NewCreateFileReqBodyBuilder().
			FileType("stream").
			FileName(name).
			File(bytesReader(data)).
			Build()).
		Build()
	resp, err := s.cli.Im.File.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("feishu file.create %d: %s", resp.Code, resp.Msg)
	}
	return *resp.Data.FileKey, nil
}

func (s *sdkClient) DownloadImage(ctx context.Context, messageID, fileKey string) ([]byte, error) {
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(fileKey).
		Type("image").
		Build()
	resp, err := s.cli.Im.MessageResource.Get(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("feishu resource.get %d: %s", resp.Code, resp.Msg)
	}
	return ioReadAll(resp.File)
}

func (s *sdkClient) AddReaction(ctx context.Context, messageID, emojiType string) (string, error) {
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().
			ReactionType(larkim.NewEmojiBuilder().EmojiType(emojiType).Build()).
			Build()).
		Build()
	resp, err := s.cli.Im.MessageReaction.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("feishu reaction.create %d: %s", resp.Code, resp.Msg)
	}
	return *resp.Data.ReactionId, nil
}

func (s *sdkClient) DeleteReaction(ctx context.Context, messageID, reactionID string) error {
	req := larkim.NewDeleteMessageReactionReqBuilder().
		MessageId(messageID).
		ReactionId(reactionID).
		Build()
	resp, err := s.cli.Im.MessageReaction.Delete(ctx, req)
	if err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("feishu reaction.delete %d: %s", resp.Code, resp.Msg)
	}
	return nil
}

func (s *sdkClient) CreateTag(ctx context.Context, name string) (string, error) {
	// im.v2.tag.create 走原生请求 ( SDK 的 v2 资源面 )。
	return "", fmt.Errorf("tag 走原生请求 ( M5 收尾时接 )")
}

func (s *sdkClient) BindTag(ctx context.Context, tagID, chatID string) error {
	return fmt.Errorf("tag 走原生请求 ( M5 收尾时接 )")
}

func (s *sdkClient) UnbindTag(ctx context.Context, tagID, chatID string) error {
	return fmt.Errorf("tag 走原生请求 ( M5 收尾时接 )")
}

// cardActionToMap 把 SDK 的卡片回调结构变回引擎用的 map。
func cardActionToMap(card *larkcallback.CardActionTriggerEvent) map[string]any {
	if card == nil || card.Event == nil {
		return map[string]any{}
	}
	return map[string]any{
		"operator": map[string]any{"open_id": card.Event.Operator.OpenID},
		"action": map[string]any{
			"value":      card.Event.Action.Value,
			"form_value": card.Event.Action.FormValue,
		},
		"context": map[string]any{"open_message_id": card.Event.Context.OpenMessageID},
	}
}
