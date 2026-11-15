package core

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// 帖子 footer 里 harness 与模型之外的话：
// git 分支、上下文占了多少、账号限额要花一条命令才能知道的数 ( 模型的上下文窗口、账号的 5 小时/7 天限额 ) 按"上次已知"立即给出：
// 超过时长就在后台再问一次，发帖只在第一次等它几秒 ( KnownSoon )，之后从不等。

// Meter 是"上次已知"的缓存：键 -> 值、读到的时刻、是否正在问。
type Meter struct {
	mu   sync.Mutex
	seen map[string]meterEntry
}

type meterEntry struct {
	value  any
	at     time.Time
	asking bool
}

// NewMeter 建一份空缓存。
func NewMeter() *Meter { return &Meter{seen: map[string]meterEntry{}} }

// ForgetAll 清掉全部读数 ( 测试用 )。
func (m *Meter) ForgetAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = map[string]meterEntry{}
}

// LastKnown 给出键上次已知的值 ( 从未问过为零值 )，
// 超过 maxAge 就在后台再问一次；
// 问失败保留旧值，同样隔 maxAge 才再试。
// ask 在锁外跑。
func (m *Meter) LastKnown(key string, maxAge time.Duration, ask func() (any, error)) any {
	m.mu.Lock()
	entry, ok := m.seen[key]
	if !ok {
		entry = meterEntry{at: time.Time{}}
	}
	if !entry.asking && time.Since(entry.at) > maxAge {
		entry.asking = true
		m.seen[key] = entry
		m.mu.Unlock()
		go func() {
			value, err := ask()
			m.mu.Lock()
			defer m.mu.Unlock()
			current := m.seen[key]
			current.at, current.asking = time.Now(), false
			if err == nil && value != nil {
				current.value = value
			}
			m.seen[key] = current
		}()
		return entry.value
	}
	m.mu.Unlock()
	return entry.value
}

// KnownSoon 同 LastKnown，但第一次 ( 还一无所知 ) 会等读数最多 waitMs：
// 重启后的第一帖也能带上它，只等这一次、只等几秒。
func (m *Meter) KnownSoon(key string, maxAge time.Duration, ask func() (any, error), wait time.Duration) any {
	value := m.LastKnown(key, maxAge, ask)
	m.mu.Lock()
	asking := m.seen[key].asking
	known := m.seen[key].value != nil
	m.mu.Unlock()
	if known || !asking {
		return value
	}
	deadline := time.After(wait)
	for {
		select {
		case <-deadline:
			m.mu.Lock()
			v := m.seen[key].value
			m.mu.Unlock()
			return v
		case <-time.After(2 * time.Millisecond):
			m.mu.Lock()
			entry := m.seen[key]
			m.mu.Unlock()
			if !entry.asking {
				return entry.value
			}
		}
	}
}

// Tokens 把 token 数说成人话：234480 -> "234k"，1404175 -> "1.4M"。
func Tokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1e6:
		return fmt.Sprintf("%dk", int64(math.Round(float64(n)/1000)))
	default:
		s := fmt.Sprintf("%.1fM", float64(n)/1e6)
		if len(s) > 3 && s[len(s)-3:] == ".0M" {
			s = s[:len(s)-3] + "M"
		}
		return s
	}
}

// WindowName 从窗口分钟数得出限额名：300 -> "5-hour"，10080 -> "7-day"。
func WindowName(minutes int64) string {
	switch {
	case minutes%1440 == 0:
		return fmt.Sprintf("%d-day", minutes/1440)
	case minutes%60 == 0:
		return fmt.Sprintf("%d-hour", minutes/60)
	default:
		return fmt.Sprintf("%d-minute", minutes)
	}
}

// FooterWords 排出 footer 的一行：
// "claude · claude-opus-5-5 · main · context 23% (234k) · 5-hour 2% · 7-day 57%"。
// 不知道的一律略去。
func FooterWords(app, model, branch string, screen bool, context *ContextUse, limits []Limit) string {
	shown := model
	if screen {
		shown = "screen"
	}
	var parts []string
	for _, s := range []string{app, shown, branch} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if context != nil && context.Used > 0 {
		if context.Size > 0 {
			parts = append(parts, fmt.Sprintf("context %d%% (%s)", int64(math.Round(float64(context.Used)*100/float64(context.Size))), Tokens(context.Used)))
		} else {
			parts = append(parts, "context "+Tokens(context.Used))
		}
	}
	for _, l := range limits {
		if !math.IsNaN(l.Used) && l.Used >= 0 {
			parts = append(parts, fmt.Sprintf("%s %d%%", l.Name, int64(math.Round(l.Used))))
		}
	}
	return joinParts(parts)
}

func joinParts(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " · "
		}
		out += p
	}
	return out
}
