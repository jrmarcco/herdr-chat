package core

import (
	"crypto/sha1" //nolint:gosec // G505: 特征串哈希，非密码学用途
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/state"
)

// 游标：
// 一个 agent 对话日志 ( .jsonl ) 里已经发过什么，让 reader 只返回新的。
// 每个读日志的 reader 共用。
//
// 游标是 {at, seen}：
// seen 列出最近 LagMS 内已发条目的 [id, at]，
// at 恒落后最新已发条目 LagMS。
// 一条条目算新，当且仅当它在 at 之后且未被 seen，
// 晚写但时间戳较旧 ( 在 lag 窗口内 ) 的条目仍会被拾起，
// 且任何条目不会被发两次。

// CursorLag 是 at 恒定滞后的时长 ( 原 LAG_MS )。
const CursorLag = 10 * time.Minute

// isoLayout 与 hc/agent 日志的时间戳格式一致 ( 毫秒、UTC )。
const isoLayout = "2006-01-02T15:04:05.000Z"

// StartCursor 建一个从 at 开始的游标；首启传启动时刻，绝不补发历史。
func StartCursor(at string) state.Cursor {
	return state.Cursor{At: at, Seen: [][2]string{}}
}

// StartCursorTime 是 StartCursor 的 time 形态。
func StartCursorTime(at time.Time) state.Cursor {
	return StartCursor(at.UTC().Format(isoLayout))
}

// after 判断一条 [at, id] 对该游标是否是新条目。
func after(cur state.Cursor, at, id string) bool {
	// ISO 同格式，字符串比较即时间比较。
	if at <= cur.At {
		return false
	}
	for _, seen := range cur.Seen {
		if seen[0] == id {
			return false
		}
	}
	return true
}

// IDOf 为一条日志条目生成稳定 id：
// 有 uuid 用 uuid；
// 否则把条目的特征 ( type|timestamp|operation|content|payload 特征 ) 哈希成 sha1。
// 同一毫秒的两条不同条目绝不能共享 id，否则第二条会被当成已发。
func IDOf(entry map[string]any) string {
	if id, ok := entry["uuid"].(string); ok && id != "" {
		return id
	}
	payload := ""
	if p, ok := entry["payload"]; ok && p != nil {
		feat := ""
		if m, ok := p.(map[string]any); ok {
			if im, ok := m["item"].(map[string]any); ok {
				feat, _ = im["id"].(string)
			}
			if feat == "" {
				feat, _ = m["id"].(string)
			}
			if feat == "" {
				feat, _ = m["call_id"].(string)
			}
		}
		if feat == "" {
			if raw, err := json.Marshal(p); err == nil {
				feat = string(raw)
				if len(feat) > 4000 {
					feat = feat[:4000]
				}
			}
		}
		payload = "|" + feat
	}
	sum := sha1.Sum([]byte(fmt.Sprint(entry["type"], "|", entry["timestamp"], "|", entry["operation"], "|", entry["content"]) + payload)) //nolint:gosec // G401: 特征串哈希，非安全用途
	return hex.EncodeToString(sum[:])
}

// ItemsAfter 用 entryItems ( reader 自己的解析器 ) 从日志行里取出游标之后的条目，按序返回；
// 每条带上条目的时间戳 at 与稳定 id ( 同条目多项时加 #i 后缀 )。
//
// complete 说明这些行是否够读到游标之前：
// 当最老的可读条目已晚于游标，更旧的未发条目可能在这些行之外，调用方须整文件重读。
func ItemsAfter(lines []string, cur state.Cursor, entryItems func(map[string]any) []Item) (items []Item, complete bool) {
	oldest := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // 被切断的首行、写了一半的尾行
		}
		ts, _ := entry["timestamp"].(string)
		if ts == "" {
			continue
		}
		if oldest == "" || ts < oldest {
			oldest = ts
		}
		id := IDOf(entry)
		if !after(cur, ts, id) {
			continue
		}
		at, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			continue
		}
		for i, it := range entryItems(entry) {
			it.At, it.ID = at, id
			if i > 0 {
				it.ID = fmt.Sprintf("%s#%d", id, i)
			}
			items = append(items, it)
		}
	}
	return items, oldest != "" && oldest <= cur.At
}

// Advance 得出条目发出之后的游标：
// at 推进到 max(旧 at, 最新条目 - LagMS)，
// seen 只保留仍晚于新 at 的 lag。
// 窗口一过，id 即可遗忘。
func Advance(cur state.Cursor, items []Item) state.Cursor {
	seen := make([][2]string, 0, len(cur.Seen)+len(items))
	seen = append(seen, cur.Seen...)
	newest := cur.At
	for _, it := range items {
		iso := it.At.UTC().Format(isoLayout)
		seen = append(seen, [2]string{it.ID, iso})
		if iso > newest {
			newest = iso
		}
	}
	if t, err := time.Parse(time.RFC3339, newest); err == nil {
		lagged := t.Add(-CursorLag).UTC().Format(isoLayout)
		if lagged > cur.At {
			cur.At = lagged
		}
	}
	kept := seen[:0]
	for _, pair := range seen {
		if pair[1] > cur.At {
			kept = append(kept, pair)
		}
	}
	cur.Seen = kept
	return cur
}
