package feishu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// 以你的身份行事 ( user_access_token )，为那几样只有你能改的聊天列表设置：群静音、
// 你自己的标签 ( feed group )、置顶。你在直聊里 `/auth` 授权一次 ( OAuth 设备流：
// 无需公网地址——bot 给出链接与 code，你在飞书里确认，bot 后台轮询 token )。
// token 到期前自动刷新，存 state 目录的 user-token.json ( 0600 )，绝不入日志。
// 这里发出的调用仅限本文件列出的那些。

const (
	groupNormal = "normal"
	kClientID   = "client_id"

	accountsHost = "https://accounts.feishu.cn"
	openHost     = "https://open.feishu.cn"
	// earlyRefresh 在到期前这么久就刷新。
	earlyRefresh = 5 * time.Minute
)

// UserScopes 是 /auth 请求的权限 ( 飞书控制台的 scope JSON 里就是它们 )。
var UserScopes = []string{
	"im:chat.user_setting:write",
	"im:feed_group_v1:read",
	"im:feed_group_v1:write",
	"im:feed.shortcut:write",
	"offline_access",
}

// Device 是设备授权的第一步产物。
type Device struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                int    `json:"interval"`
	ExpiresIn               int    `json:"expires_in"`
}

// UserAuth 是以你行事的全部状态 ( token 的存取与刷新 )。
type UserAuth struct {
	AppID     string
	AppSecret string
	// File 是 token 的落点 ( user-token.json )。
	File string

	// HTTP 可替换 ( 测试注入 )。
	HTTP func(req *http.Request) (*http.Response, error)

	mu sync.Mutex
}

// NewUserAuth 建一份。
func NewUserAuth(appID, appSecret, file string) *UserAuth {
	return &UserAuth{AppID: appID, AppSecret: appSecret, File: file}
}

func (u *UserAuth) roundTrip(_ context.Context, req *http.Request) (*http.Response, error) {
	if u.HTTP != nil {
		return u.HTTP(req)
	}
	return http.DefaultClient.Do(req)
}

type tokenFile struct {
	Access       string `json:"access"`
	AccessUntil  int64  `json:"accessUntil"`
	Refresh      string `json:"refresh"`
	RefreshUntil int64  `json:"refreshUntil"`
	Scope        string `json:"scope"`
}

func (u *UserAuth) load() *tokenFile {
	raw, err := readFile(u.File)
	if err != nil {
		return nil
	}
	var t tokenFile
	if json.Unmarshal(raw, &t) != nil {
		return nil
	}
	return &t
}

// readFile/writeFile 是 os 的薄封装 ( 测试可换 )。
var (
	readFile  = os.ReadFile
	writeFile = os.WriteFile
)

func (u *UserAuth) save(t *tokenFile) {
	raw, err := json.Marshal(t)
	if err != nil {
		return
	}
	_ = writeFile(u.File+".tmp", raw, 0o600)
}

// StartDeviceFlow 走第一步：给你一个链接与 code。
func (u *UserAuth) StartDeviceFlow(ctx context.Context) (*Device, error) {
	form := url.Values{
		kClientID: {u.AppID},
		"scope":   {strings.Join(UserScopes, " ")},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, accountsHost+"/oauth/v1/device_authorization", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+basicAuth(u.AppID, u.AppSecret))
	res, err := u.roundTrip(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	var device Device
	if err := json.NewDecoder(res.Body).Decode(&device); err != nil {
		return nil, err
	}
	if device.DeviceCode == "" {
		return nil, fmt.Errorf("device authorization: no device_code")
	}
	return &device, nil
}

// tokenReply 是 oauth token 端点的回复。
type tokenReply struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int    `json:"refresh_token_expires_in"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
}

// WaitForToken 轮询直到你确认 ( 或 code 过期 )。
func (u *UserAuth) WaitForToken(ctx context.Context, device *Device) (*tokenFile, error) {
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	until := time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)
	if device.ExpiresIn == 0 {
		until = time.Now().Add(4 * time.Minute)
	}
	for time.Now().Before(until) {
		time.Sleep(interval)
		form := url.Values{
			"grant_type":    {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code":   {device.DeviceCode},
			kClientID:       {u.AppID},
			"client_secret": {u.AppSecret},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, accountsHost+"/oauth/v3/token", strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := u.roundTrip(ctx, req)
		if err != nil {
			continue
		}
		var reply tokenReply
		_ = json.NewDecoder(res.Body).Decode(&reply)
		_ = res.Body.Close()
		if reply.AccessToken != "" {
			return u.keep(reply), nil
		}
		switch reply.Error {
		case "slow_down":
			interval += 5 * time.Second
		case "authorization_pending":
		default:
			return nil, fmt.Errorf("authorization: %s", reply.Error)
		}
	}
	return nil, fmt.Errorf("authorization: the code expired before it was confirmed")
}

// keep 把一次 token 回复存进文件。
func (u *UserAuth) keep(reply tokenReply) *tokenFile {
	now := time.Now().UnixMilli()
	old := u.load()
	if old == nil {
		old = &tokenFile{}
	}
	t := &tokenFile{
		Access:  reply.AccessToken,
		Refresh: reply.RefreshToken,
		Scope:   reply.Scope,
	}
	if t.Refresh == "" {
		t.Refresh = old.Refresh
	}
	t.AccessUntil = now + int64(reply.ExpiresIn)*1000
	if t.AccessUntil == now {
		t.AccessUntil = now + 7200*1000
	}
	if reply.RefreshToken != "" {
		t.RefreshUntil = now + int64(orInt(reply.RefreshExpiresIn, 7*86400))*1000
	} else {
		t.RefreshUntil = old.RefreshUntil
	}
	u.save(t)
	return t
}

func orInt(v, fallback int) int {
	if v != 0 {
		return v
	}
	return fallback
}

// Token 给出可用的 access token：没过期的直接用，快过期的刷新；没有或刷新不了
// 返回 ""。刷新被拒 ( token 撤销 ) 也返回 ""，网络不通则报错——片刻离线不该被
// 当成撤销的 /auth。
func (u *UserAuth) Token(ctx context.Context) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	t := u.load()
	if t == nil || t.Access == "" {
		return "", nil
	}
	now := time.Now().UnixMilli()
	if now < t.AccessUntil-earlyRefresh.Milliseconds() {
		return t.Access, nil
	}
	if t.Refresh == "" || now > t.RefreshUntil {
		return "", nil
	}
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": t.Refresh,
		kClientID:       u.AppID,
		"client_secret": u.AppSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, accountsHost+"/oauth/v3/token", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	res, err := u.roundTrip(ctx, req)
	if err != nil {
		return "", err // 网络不通：交给调用方按"未检查"处理
	}
	defer func() { _ = res.Body.Close() }()
	var reply tokenReply
	decodeErr := json.NewDecoder(res.Body).Decode(&reply)
	if decodeErr != nil || reply.AccessToken == "" {
		return "", nil
	}
	return u.keep(reply).Access, nil
}

// Allowed 是"现在可以以你行事吗"：没有 token 或刷新被拒才是 false；一次没打通
// 的调用报错，由调用方按"维持现状"处理。
func (u *UserAuth) Allowed(ctx context.Context) (bool, error) {
	token, err := u.Token(ctx)
	if err != nil {
		return false, err
	}
	return token != "", nil
}

// userCall 以你的身份调一次 open API。
func (u *UserAuth) userCall(ctx context.Context, method, apiPath string, body any) (map[string]any, error) {
	access, err := u.Token(ctx)
	if err != nil {
		return nil, err
	}
	if access == "" {
		return nil, nil
	}
	var reqBody *strings.Reader
	if body != nil {
		raw, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return nil, marshalErr
		}
		reqBody = strings.NewReader(string(raw))
	} else {
		reqBody = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, method, openHost+apiPath, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	res, err := u.roundTrip(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	var reply struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&reply); err != nil {
		return nil, err
	}
	if reply.Code != 0 {
		return nil, fmt.Errorf("%s %s %d: %s", method, apiPath, reply.Code, reply.Msg)
	}
	if reply.Data == nil {
		return map[string]any{}, nil
	}
	return reply.Data, nil
}

// SetMuted 静音/取消静音一批群。
func (u *UserAuth) SetMuted(ctx context.Context, chatIDs []string, muted bool) error {
	settings := make([]any, 0, len(chatIDs))
	for _, id := range chatIDs {
		settings = append(settings, map[string]any{"chat_id": id, "is_muted": muted})
	}
	_, err := u.userCall(ctx, http.MethodPost, "/open-apis/im/v1/chat_user_setting/batch_update",
		map[string]any{"chat_settings": settings})
	return err
}

// FeedGroup 是你的一个标签。
type FeedGroup struct {
	ID   string
	Name string
}

// FeedGroups 列出你的标签。
func (u *UserAuth) FeedGroups(ctx context.Context) ([]FeedGroup, error) {
	data, err := u.userCall(ctx, http.MethodGet, "/open-apis/im/v1/groups?page_size=50&page_token=", nil)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	items, _ := data["items"].([]any)
	var out []FeedGroup
	for _, item := range items {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		id, _ := m["feed_group_id"].(string)
		name, _ := m["name"].(string)
		out = append(out, FeedGroup{ID: id, Name: name})
	}
	return out, nil
}

// CreateFeedGroup 建一个标签。
func (u *UserAuth) CreateFeedGroup(ctx context.Context, name string) (string, error) {
	data, err := u.userCall(ctx, http.MethodPost, "/open-apis/im/v1/groups",
		map[string]any{"feed_group_creator": map[string]any{"name": name, kType: groupNormal}})
	if err != nil {
		return "", err
	}
	id, _ := data["feed_group_id"].(string)
	return id, nil
}

// AddToFeedGroup / RemoveFromFeedGroup 把群放进/拿出你的标签。
func (u *UserAuth) AddToFeedGroup(ctx context.Context, groupID string, chatIDs []string) error {
	_, err := u.userCall(ctx, http.MethodPost, "/open-apis/im/v1/groups/"+groupID+"/batch_add_item",
		map[string]any{"items": feedItems(chatIDs)})
	return err
}

func (u *UserAuth) RemoveFromFeedGroup(ctx context.Context, groupID string, chatIDs []string) error {
	_, err := u.userCall(ctx, http.MethodPost, "/open-apis/im/v1/groups/"+groupID+"/batch_remove_item",
		map[string]any{"items": feedItems(chatIDs)})
	return err
}

func feedItems(chatIDs []string) []any {
	out := make([]any, 0, len(chatIDs))
	for _, id := range chatIDs {
		out = append(out, map[string]any{"feed_id": id, "feed_type": "chat"})
	}
	return out
}

// Pin / Unpin 置顶/取消置顶一个群。
func (u *UserAuth) Pin(ctx context.Context, chatID string) error {
	_, err := u.userCall(ctx, http.MethodPost, "/open-apis/im/v2/feed_shortcuts",
		map[string]any{"is_header": true, "shortcuts": []any{map[string]any{"feed_card_id": chatID, kType: 1}}})
	return err
}

func (u *UserAuth) Unpin(ctx context.Context, chatID string) error {
	_, err := u.userCall(ctx, http.MethodPost, "/open-apis/im/v2/feed_shortcuts/remove",
		map[string]any{"shortcuts": []any{map[string]any{"feed_card_id": chatID, kType: 1}}})
	return err
}

func basicAuth(appID, appSecret string) string {
	return base64.StdEncoding.EncodeToString([]byte(appID + ":" + appSecret))
}
