package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripKeepsNodeShapes(t *testing.T) {
	// Node 版写出的 JSON ( 连同精确的字段类型：毫秒整数、ISO 字符串 ) 必须能
	// 无损读入并写回。
	const nodeJSON = `{
  "chats": {"default|t3": {"chat": "oc_1", "name": "api / claude", "session": "default", "tab": "tb7", "ended": false}},
  "cursors": {"default|t3": {"at": "2026-11-14T10:00:00.000Z", "seen": [["a", "2026-11-14T09:59:00.000Z"]], "reader": "claude-code", "file": "~/.claude/projects/p/s.jsonl", "meta": {"model": "opus"}, "live": {"message": "m1", "text": "working", "footer": "claude · main"}}},
  "reacts": {"default|t3": [{"message": "m2", "reaction": "THINKING", "at": 1731580000000}]},
  "waiting": {"default|t3": [{"where": "api / claude", "message": "m3", "session": "default", "address": "t3", "text": "go on"}]},
  "pinned": {"default|t3": "m4"},
  "overview": {"message": "m5"},
  "buttons": {"b1": {"value": {"pick": "1"}, "labels": {"1": "Yes"}}},
  "spaces": {"api": {"icon": 7322096}},
  "feishuRead": {"oc_1": 1731580000000},
  "telegramOffset": 12345,
  "down": {"since": 1731580000000, "said": 1731580000000, "why": "boom"},
  "pairing": {"code": "123456", "until": 1731580900000, "tries": 1}
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(nodeJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &FileStore{Path: path}
	st, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.Chats["default|t3"].Name != "api / claude" || st.Chats["default|t3"].Chat != "oc_1" {
		t.Errorf("room = %+v", st.Chats["default|t3"])
	}
	cur := st.Cursors["default|t3"]
	if cur.At != "2026-11-14T10:00:00.000Z" || cur.Seen[0][0] != "a" || cur.Live.Message != "m1" {
		t.Errorf("cursor = %+v", cur)
	}
	if st.Reacts["default|t3"][0].At != 1731580000000 {
		t.Errorf("reacts at must stay milliseconds: %+v", st.Reacts)
	}
	if st.Pairing.Code != "123456" || st.Down.Why != "boom" {
		t.Errorf("pairing/down = %+v %+v", st.Pairing, st.Down)
	}
	// 写回后确认没有任何字段变形。
	if writeErr := store.Write(st); writeErr != nil {
		t.Fatal(writeErr)
	}
	back, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(back)
	want, _ := json.Marshal(st)
	if string(again) != string(want) {
		t.Errorf("round trip changed the state")
	}
}

func TestMissingFileIsEmpty(t *testing.T) {
	st, err := (&FileStore{Path: filepath.Join(t.TempDir(), "none.json")}).Read()
	if err != nil || st.Chats == nil || len(st.Chats) != 0 {
		t.Errorf("missing file: %+v %v", st, err)
	}
}

func TestCorruptFileIsKeptAsideAndReported(t *testing.T) {
	// optimization D4：损坏的 state.json 绝不静默重置——房间会无声无息地丢。
	// 它被备份并如实上报。
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"chats": { broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := (&FileStore{Path: path}).Read()
	if !os.IsNotExist(err) && err == nil {
		t.Fatalf("corrupt file must error, got %+v", st)
	}
	if !strings.Contains(err.Error(), ErrCorrupt.Error()) || !strings.Contains(err.Error(), "corrupt-") {
		t.Errorf("err = %v, want %v with a backup name", err, ErrCorrupt)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "state.json.corrupt-") {
		t.Errorf("backup file = %v", entries)
	}
	if len(st.Chats) != 0 {
		t.Errorf("state after corruption starts empty")
	}
}

func TestWriteIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store := &FileStore{Path: path}
	if err := store.Write(Empty()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file must be renamed away")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 600", info.Mode().Perm())
	}
	// nil 的 Seen 序列化为 [] ( Node 形状 )，而不是 null。
	st := Empty()
	st.Cursors = map[string]Cursor{"k": {}}
	if err := store.Write(st); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(path) //nolint:gosec // G304: 测试临时目录内的文件
	if !strings.Contains(string(text), `"seen": []`) {
		t.Errorf("seen must marshal as []: %s", text)
	}
}
