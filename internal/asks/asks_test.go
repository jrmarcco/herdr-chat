package asks

// 这里的测试锁的是 asks 与 ask.sh 之间的协议本身：
// 正本脚本 ( internal/hooks 内嵌 ) 真跑一个来回——req 解析进 Ask、ans 原样
// 从脚本嘴里出来；再加内存集的封顶与 Clip 的 rune 边界。

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/fake"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/hooks"
)

// TestAskShSpeaksTheProtocol 让内嵌的 ask.sh 真跑一个来回 ( codex、无 hc
// 终端：整条路不需要 hc )：请求文件解析进 Ask，答案文件被原样打印，收场不留文件。
// 脚本与 Go 各写一遍协议 ( 字段名、id 形状、目录、标签 )——形状漂移在这里现形。
func TestAskShSpeaksTheProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ask.sh 是 POSIX sh")
	}
	// 正本脚本得带着 asks 这边的目录与标签字样：谁改了谁，这里先叫。
	if script := string(hooks.AskSh); !strings.Contains(script, AsksDir) || !strings.Contains(script, AskLabel) {
		t.Fatal("the embedded ask.sh no longer carries AsksDir/AskLabel")
	}

	dir := t.TempDir()
	//nolint:gosec // G306: 测试要跑的脚本正本。
	if err := os.WriteFile(filepath.Join(dir, "ask.sh"), hooks.AskSh, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", filepath.Join(dir, "ask.sh"), "codex", "10") //nolint:gosec // G204: 要测的就是这个脚本
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	cmd.Stdin = strings.NewReader(`{"tool_name":"Bash","input":{"command":"make test"}}`)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// 请求文件出现；id 是 <秒>-<pid> ( ".json.tmp" 不算 )。
	reqDir := filepath.Join(home, AsksDir, "req")
	id := ""
	for deadline := time.Now().Add(8 * time.Second); id == "" && time.Now().Before(deadline); {
		if entries, err := os.ReadDir(reqDir); err == nil {
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".json") {
					id = strings.TrimSuffix(e.Name(), ".json")
				}
			}
		}
		if id == "" {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if id == "" {
		_ = cmd.Process.Kill()
		t.Fatal("the hook never wrote its request")
	}
	if !askIDRe.MatchString(id) {
		_ = cmd.Process.Kill()
		t.Fatalf("request id %q is not <seconds>-<pid>", id)
	}

	// 脚本写出的形状得解析进 Ask —— "at" 是不带引号的数字这类漂移在这里现形。
	raw, err := os.ReadFile(filepath.Join(reqDir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var ask Ask
	if err := json.Unmarshal(raw, &ask); err != nil {
		t.Fatalf("the hook's request does not parse into Ask: %v\n%s", err, raw)
	}
	if ask.Agent != "codex" || ask.Terminal != "" || len(ask.Request) == 0 {
		t.Fatalf("ask = %+v", ask)
	}
	if delta := time.Now().Unix() - ask.At; delta < -60 || delta > 60 {
		t.Errorf("ask.At = %d，不是这一刻", ask.At)
	}

	// 原子写回决定 ( Asks.write 的形状 )：脚本把它原样打印，然后收场。
	decision := DecisionJSON(map[string]any{"behavior": "allow"})
	ansPath := filepath.Join(home, AsksDir, "ans", id+".json")
	if err := os.WriteFile(ansPath+".tmp", []byte(decision), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ansPath+".tmp", ansPath); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("hook 以 %v 收场", err)
		}
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("答案写回后 hook 没有收场")
	}
	if got := strings.TrimSpace(stdout.String()); got != decision {
		t.Errorf("hook 打印了 %q，该是原样的决定", got)
	}
	for _, left := range []string{filepath.Join(reqDir, id+".json"), ansPath} {
		if _, err := os.Stat(left); err == nil {
			t.Errorf("%s 没被清走", left)
		}
	}
}

// TestPartialsForgotten 部分答案与已答完一样封顶：
// 最旧的先忘，免得只答了一半就放弃的问答永远留在内存里。
func TestPartialsForgotten(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{filepath.Join(home, AsksDir, "req"), filepath.Join(home, AsksDir, "ans")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	hc := fake.NewFakeHC()
	hc.HomeDirSet = home
	a := New(hc, home)

	pid := strconv.Itoa(os.Getpid()) // kill -0 认测试自己的进程：自然活着
	base := time.Now().Add(-time.Hour).Unix()
	askID := func(i int) string { return strconv.FormatInt(base+int64(i), 10) + "-" + pid }

	decidePartial := func(ask *Ask, shown core.Dialog, reply core.Reply, partial map[string]string) *Decision {
		return &Decision{Partial: map[string]string{"q": reply.Pick}}
	}
	for i := 0; i < forgetAt+50; i++ {
		id := askID(i)
		raw, err := json.Marshal(map[string]any{
			"agent": "codex", "terminal": "", "at": base + int64(i),
			"request": map[string]any{"q": 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, AsksDir, "req", id+".json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		hc.Terminals["t1"] = hccli.Terminal{"labels": map[string]any{AskLabel: id}}
		shown := core.Dialog{Menu: &core.Menu{Own: &Ref{ID: id}}}
		if !a.AnswerAsk(context.Background(), "default", "t1", "codex", &shown, core.Reply{Pick: "1"}, decidePartial) {
			t.Fatalf("第 %d 条部分答案没有被记下", i)
		}
	}

	// 最旧的 50 条先忘，最近的 forgetAt 条都在。
	for i := 0; i < 50; i++ {
		if a.PartialOf(askID(i)) != nil {
			t.Errorf("第 %d 条该被忘掉", i)
		}
	}
	for _, i := range []int{50, 150, forgetAt + 49} {
		if a.PartialOf(askID(i)) == nil {
			t.Errorf("第 %d 条该还在", i)
		}
	}
}

// TestClipCutsOnRuneBoundary 字符上限落在多字节字符中间时退到边界，
// 不把半个字留在卡片上。
func TestClipCutsOnRuneBoundary(t *testing.T) {
	got := Clip(strings.Repeat("中", 100), 100, 0)
	if !utf8.ValidString(got) {
		t.Fatalf("切出了无效 UTF-8: %q", got)
	}
	if n := strings.Count(got, "中"); n != 33 { // 100 字节退到 99：33 个完整的字
		t.Errorf("完整的字 = %d，该是 33", n)
	}
	if !strings.HasSuffix(got, "… (the rest is in the terminal)") {
		t.Errorf("裁过就该说明还有下文: %q", got[max(0, len(got)-40):])
	}
}
