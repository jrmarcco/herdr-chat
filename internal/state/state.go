// Package state 保存 bot 写下的东西，让重启能在停下的地方继续：每个 profile
// 一个 state.json，形状与 Node 版逐字节兼容 ( spec §4.3 )，老用户放入新二进制
// 即可无损接管。
//
// 读-合-写的纪律属于引擎的 lane ( 一个终端一条串行 lane，不会有并发写同一键 )；
// 本包只负责整文件读写，原子落盘 ( tmp + rename )，且绝不静默丢弃损坏文件
// ( optimization D4 )：损坏的文件被保留为 state.json.corrupt-<ts> 并如实上报，
// 让丢失被听见。
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrCorrupt 表示 state 文件无法读取；损坏的文件已被另行保留
// ( 调用方应告知 owner：房间将被重建 )。
var ErrCorrupt = errors.New("state.json is damaged")

// Store 读写整个 state ( spec §3.3 )。引擎在每次跨 lane 步骤后重新读取并
// 合并，写者的串行由 lane 保证。
type Store interface {
	Read() (State, error)
	Write(State) error
}

// State 是 bot 持久化的全部。字段名与 JSON 形状严格跟随 Node 版；
// 原本是毫秒的数字仍是毫秒，ISO 字符串仍是字符串。
type State struct {
	Chats          map[string]Room         `json:"chats"`
	Cursors        map[string]Cursor       `json:"cursors,omitempty"`
	OpenCards      map[string]OpenCard     `json:"openCards,omitempty"`
	Reacts         map[string][]React      `json:"reacts,omitempty"`
	Waiting        map[string][]WaitingMsg `json:"waiting,omitempty"`
	Pinned         map[string]string       `json:"pinned,omitempty"`
	Overview       *OverviewRef            `json:"overview,omitempty"`
	Buttons        map[string]Button       `json:"buttons,omitempty"`
	Spaces         map[string]SpaceIcon    `json:"spaces,omitempty"`
	FeishuRead     map[string]int64        `json:"feishuRead,omitempty"`
	TelegramOffset int64                   `json:"telegramOffset,omitempty"`
	Down           *Down                   `json:"down,omitempty"`
	Pairing        *Pairing                `json:"pairing,omitempty"`
	Tags           map[string]string       `json:"tags,omitempty"` // scope -> 租户标签 id ( 含 "ended" )
}

// Room 是一个 tab 的镜像：对应哪个聊天房间、叫什么名字。后几个字段是该
// 群的装饰 ( 飞书的租户标签与 /auth 后的静音/你的标签 )。
type Room struct {
	Chat    string `json:"chat"`
	Name    string `json:"name"`
	Session string `json:"session,omitempty"`
	Tab     string `json:"tab,omitempty"`
	Ended   bool   `json:"ended,omitempty"`

	Tag        string `json:"tag,omitempty"`        // 已绑的租户标签 id
	EndedTag   bool   `json:"endedTag,omitempty"`   // 已绑 "ended" 标签
	EndedTagID string `json:"endedTagId,omitempty"` // ended 标签的 id
	Muted      bool   `json:"muted,omitempty"`      // ( /auth ) 已静音
	Label      string `json:"label,omitempty"`      // ( /auth ) 已放进你的标签
	Avatar     string `json:"avatar,omitempty"`     // 群头像的 image_key
}

// Cursor 记录一个终端的 reader 已发到哪：读到了哪里 ( at，一个恒落后最新
// 条目 LAG 的 ISO 时间戳 )、lag 窗口内见过哪些条目 id，以及它会继续编辑的
// 活卡片。
type Cursor struct {
	At     string         `json:"at"`
	Seen   [][2]string    `json:"seen"`
	Reader string         `json:"reader,omitempty"`
	File   string         `json:"file,omitempty"`
	Meta   map[string]any `json:"meta,omitempty"`
	Ended  bool           `json:"ended,omitempty"`
	Live   *LiveCard      `json:"live,omitempty"`
}

// LiveCard 记录工作中的终端持续编辑的那条消息，连同其文本与 footer，
// 让重启后继续编辑同一张卡而不是新开一张。
type LiveCard struct {
	Message string `json:"message"`
	Text    string `json:"text"`
	Footer  string `json:"footer"`
}

// OpenCard 是仍在等回答的对话框卡片。
type OpenCard struct {
	Message string          `json:"message"`
	Dialog  json.RawMessage `json:"dialog,omitempty"`
}

// React 是一条消息上未完成的回执 ( 工作中 👀，完成 👌 )。
type React struct {
	Message  string `json:"message"`
	Reaction string `json:"reaction"`
	At       int64  `json:"at"`
}

// WaitingMsg 是对话框占着终端时你发来的消息；对话框一让开，就按顺序输入
// 进去，绝不丢弃。
type WaitingMsg struct {
	Where   string `json:"where"`
	Message string `json:"message"`
	Session string `json:"session"`
	Address string `json:"address"`
	Text    string `json:"text"`
}

// OverviewRef 记录直聊里总览消息的 id ( 原地编辑 )。
type OverviewRef struct {
	Message string `json:"message"`
}

// Button 是 Telegram 按钮的值，以短 id 保存 ( callback_data 只有 64 字节 )；
// Labels 携带选项文本，用于识别过期卡片。
type Button struct {
	Value  json.RawMessage   `json:"value"`
	Labels map[string]string `json:"labels,omitempty"`
}

// SpaceIcon 是 Telegram 给一个 space 的 topic 分配的图标颜色。
type SpaceIcon struct {
	Icon int64 `json:"icon"`
}

// Down 记住一次崩溃，让下次启动能说出停了多久。
type Down struct {
	Since int64  `json:"since"`
	Said  int64  `json:"said"`
	Why   string `json:"why"`
}

// Pairing 是把 bot 交给 owner 的 6 位配对码。
type Pairing struct {
	Code  string `json:"code"`
	Until int64  `json:"until"`
	Tries int    `json:"tries"`
}

// Empty 是全新的 state：还没有房间，其余字段皆缺省。
func Empty() State { return State{Chats: map[string]Room{}} }

// FileStore 把 state 放在一个 JSON 文件里，原子写入。不同终端的 lane 可能
// 并发写，内部用锁串行化。
type FileStore struct {
	Path string
	mu   sync.Mutex
}

// Read 读取 state；文件不存在视为空。无法解析的文件被挪到一边
// ( corrupt-<ts> ) 并以 ErrCorrupt 连同备份路径上报——静默重置会丢掉全部
// 房间映射。
func (s *FileStore) Read() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return Empty(), nil
		}
		return Empty(), err
	}
	st := Empty()
	if err := json.Unmarshal(text, &st); err != nil {
		backup := fmt.Sprintf("%s.corrupt-%d", s.Path, time.Now().Unix())
		if renameErr := os.Rename(s.Path, backup); renameErr != nil {
			return Empty(), fmt.Errorf("%w: and keeping it aside failed: %w", ErrCorrupt, renameErr)
		}
		return Empty(), fmt.Errorf("%w (kept as %s): %w", ErrCorrupt, backup, err)
	}
	if st.Chats == nil {
		st.Chats = map[string]Room{}
	}
	return st, nil
}

// Write 原子地写下整个 state，保持私有 ( 0600 )。
func (s *FileStore) Write(st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.Chats == nil {
		st.Chats = map[string]Room{}
	}
	if st.Cursors != nil {
		for k, c := range st.Cursors { // cursor 的 seen 列表须序列化为 [] 而非 null
			if c.Seen == nil {
				c.Seen = [][2]string{}
				st.Cursors[k] = c
			}
		}
	}
	text, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, append(text, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// MemStore 是内存中的 Store，供测试与 fake 使用。读与写都做深拷贝，
// 模拟 FileStore 的语义：读到的副本随手改不会落盘，必须走 Write。
type MemStore struct {
	mu sync.Mutex
	St State
}

func cloneState(st State) State {
	raw, err := json.Marshal(st)
	if err != nil {
		return Empty()
	}
	var out State
	if json.Unmarshal(raw, &out) != nil {
		return Empty()
	}
	return out
}

func (m *MemStore) Read() (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.St.Chats == nil {
		return Empty(), nil
	}
	return cloneState(m.St), nil
}

func (m *MemStore) Write(st State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.St = cloneState(st)
	return nil
}
