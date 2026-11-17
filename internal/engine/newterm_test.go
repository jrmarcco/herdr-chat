package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/fake"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
)

// /new 的三个入口与开终端卡的行为测试。

// lastRun 是 FakeHC 记下的最后一次 hc run。
func (r *rig) lastRun() (session string, opts map[string]string) {
	runs := r.hc.RunsOf()
	if len(runs) == 0 {
		return "", nil
	}
	last := runs[len(runs)-1]
	return last.Session, map[string]string{
		keyCwd:  last.Opts.Cwd,
		"where": last.Opts.Labels[keyWhere],
		"argv":  strings.Join(last.Opts.Argv, " "),
	}
}

func TestNewBareSendsTheCard(t *testing.T) {
	r := harness(t)
	r.start(t)
	r.direct("/new")

	r.waitFor(t, "开终端卡", func() bool { return r.countCalls(callPostDialog) == 1 })
	if len(r.hc.RunsOf()) != 0 {
		t.Fatal("a bare /new must not start a terminal by itself")
	}
	// 记录里的卡以标题辨认 ( Dialog 本体不进记录；形状由 feishu 的卡测试锁 )。
	for _, c := range r.chat.Calls() {
		if c.Name == callPostDialog {
			if title, _ := c.Args[1].(string); title != "🆕 New terminal" {
				t.Fatalf("card title = %q", title)
			}
		}
	}
}

func TestNewCardSubmitStartsTheTerminal(t *testing.T) {
	r := harness(t)
	// 下拉里的 agent 来自已装的 reader：再装一个 codex，选它。
	codex := fake.NewFakeReader()
	codex.AppName = cmdCodex
	if err := r.e.Readers.Register(codex); err != nil {
		t.Fatal(err)
	}
	r.start(t)
	r.direct("/new")
	r.waitFor(t, "开终端卡", func() bool { return r.countCalls(callPostDialog) == 1 })

	result, err := r.chat.Handlers.OnAction(core.Click{
		Value:   map[string]any{"k": newKey, valPick: "1"},
		Message: directMsg,
		Answer:  " Projects/foo ",
		Form:    map[string]string{fieldAgent: "codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Toast, "✔ Started `codex`") {
		t.Fatalf("toast = %q", result.Toast)
	}
	home := r.e.HomeDir
	session, opts := r.lastRun()
	if session != sessDefault || opts == nil {
		t.Fatalf("run = %v %v, want one terminal started", session, opts)
	}
	if want := home + "/Projects/foo"; opts["cwd"] != want {
		t.Fatalf("cwd = %q, want %q", opts["cwd"], want)
	}
	if opts["argv"] != "/bin/zsh -lc exec codex" {
		t.Fatalf("argv = %q", opts["argv"])
	}
	// tab 名取目录名；space 承自最近一台 ( t1 的 api )。
	if opts["where"] != "api / foo" {
		t.Fatalf("where = %q, want api / foo", opts["where"])
	}
}

func TestNewCardRejectsBadSubmissions(t *testing.T) {
	r := harness(t)
	r.start(t)

	// 空目录。
	result, err := r.chat.Handlers.OnAction(core.Click{
		Value: map[string]any{"k": newKey, valPick: "1"}, Answer: "  ",
	})
	if err != nil || result.Toast != "Type the folder first." {
		t.Fatalf("empty folder: %v %q", err, result.Toast)
	}
	// 绝对路径：只回 toast，卡不动 ( 输入还在 )。
	result, err = r.chat.Handlers.OnAction(core.Click{
		Value: map[string]any{"k": newKey, valPick: "1"}, Answer: "/etc",
	})
	if err != nil || !strings.Contains(result.Toast, "under your home") || result.Card != nil {
		t.Fatalf("absolute folder: %v %+v", err, result)
	}
	if len(r.hc.RunsOf()) != 0 {
		t.Fatal("nothing should have started")
	}
	// Esc：取消。
	result, err = r.chat.Handlers.OnAction(core.Click{
		Value: map[string]any{"k": newKey, valPick: pickEsc},
	})
	if err != nil || !strings.Contains(result.Toast, "Cancelled") {
		t.Fatalf("esc: %v %q", err, result.Toast)
	}
}

func TestNewFolderShortcut(t *testing.T) {
	r := harness(t)
	r.start(t)
	r.direct("/new Projects/github.com/jit codex")

	r.waitFor(t, "快捷开终端", func() bool { return len(r.hc.RunsOf()) == 1 })
	_, opts := r.lastRun()
	if opts["cwd"] != r.e.HomeDir+"/Projects/github.com/jit" {
		t.Fatalf("cwd = %q", opts["cwd"])
	}
	if !strings.Contains(opts["argv"], "codex") {
		t.Fatalf("argv = %q", opts["argv"])
	}
	// tab 名取目录 basename；与 t1 的 tab ( claude ) 不同 space 名下无冲突。
	if opts["where"] != "api / jit" {
		t.Fatalf("where = %q", opts["where"])
	}
}

func TestNewTabNameDedupes(t *testing.T) {
	r := harness(t)
	r.start(t)
	// t1 的 tab 名是 claude ( where "api / claude" )：同名目录要排到 -2。
	r.direct("/new Projects/claude")

	r.waitFor(t, "开终端", func() bool { return len(r.hc.RunsOf()) == 1 })
	_, opts := r.lastRun()
	if opts["where"] != "api / claude-2" {
		t.Fatalf("where = %q, want the deduped tab name", opts["where"])
	}
}

func TestNewDefaultSpaceWhenNoSibling(t *testing.T) {
	r := harness(t)
	r.start(t)
	// 没有 where 终端时用默认 space ( HERDRCHAT_SPACE；未注入 dev )。
	r.hc.RemoveTerminal("t1")
	r.direct("/new Projects/foo")

	r.waitFor(t, "开终端", func() bool { return len(r.hc.RunsOf()) == 1 })
	_, opts := r.lastRun()
	if opts["where"] != "dev / foo" {
		t.Fatalf("where = %q, want the default space", opts["where"])
	}

	// 注入的默认 space 生效。
	r.e.DefaultSpace = sessWork
	r.direct("/new Projects/bar")
	r.waitFor(t, "第二台", func() bool { return len(r.hc.RunsOf()) == 2 })
	_, opts = r.lastRun()
	if opts["where"] != "work / bar" {
		t.Fatalf("where = %q, want the configured space", opts["where"])
	}
}

func TestNewOldSyntaxInheritsTheFolder(t *testing.T) {
	r := harness(t)
	r.start(t)

	// 兄弟终端带着 home 下的 cwd：旧语法的目录承自它。
	rec := term("t1")
	rec["foreground"] = map[string]any{"name": "fake", keyCwd: r.e.HomeDir + "/Projects/api"}
	r.hc.SetTerminal("t1", hccli.Terminal(rec))
	r.direct("/new default api/named")

	r.waitFor(t, "旧语法开终端", func() bool { return len(r.hc.RunsOf()) == 1 })
	_, opts := r.lastRun()
	if opts["cwd"] != r.e.HomeDir+"/Projects/api" {
		t.Fatalf("cwd = %q, want the sibling's", opts["cwd"])
	}
	if opts["where"] != "api / named" {
		t.Fatalf("where = %q", opts["where"])
	}

	// 没有兄弟可承：指路快捷形态。
	r2 := harness(t)
	r2.start(t)
	r2.hc.RemoveTerminal("t1")
	r2.direct("/new default new-space/tab")
	r2.waitFor(t, "回复指路", func() bool {
		for _, c := range r2.chat.Calls() {
			if c.Name == callReply && len(c.Args) >= 2 {
				if md, _ := c.Args[1].(string); strings.Contains(md, "/new <folder>") {
					return true
				}
			}
		}
		return false
	})
}

// place 点名的两个命令形态与卡上的 place 输入。

// folderFoo / agentClaude 是这两个测试文件反复用的字面量。
const (
	folderFoo   = "Projects/foo"
	agentClaude = cmdClaude
)

func TestNewWithAnExplicitPlace(t *testing.T) {
	r := harness(t)
	r.start(t)

	// /new <folder> <space>/<tab>：session 默认。
	r.direct("/new Projects/foo work/jit9")
	r.waitFor(t, "place 形态开终端", func() bool { return len(r.hc.RunsOf()) == 1 })
	_, opts := r.lastRun()
	if opts["where"] != "work / jit9" {
		t.Fatalf("where = %q, want work / jit9", opts["where"])
	}

	// /new <folder> <session> <space>/<tab> [command]：完整形态。
	r.direct("/new Projects/bar default dev/full codex")
	r.waitFor(t, "完整形态", func() bool { return len(r.hc.RunsOf()) == 2 })
	_, opts = r.lastRun()
	if opts["where"] != "dev / full" || !strings.Contains(opts["argv"], "codex") {
		t.Fatalf("where=%q argv=%q", opts["where"], opts["argv"])
	}

	// place 形状不对：如实报错、一个也不开。
	r.direct("/new Projects/baz dev/")
	for _, run := range r.hc.RunsOf() {
		if strings.Contains(run.Opts.Cwd, "baz") {
			t.Fatal("a malformed place must start nothing")
		}
	}
}

// 卡上填 place：去处照填；填坏只回 toast，卡不动。
func TestNewCardPlaceField(t *testing.T) {
	r := harness(t)
	r.start(t)
	r.direct("/new")
	r.waitFor(t, "开终端卡", func() bool { return r.countCalls(callPostDialog) == 1 })

	result, err := r.chat.Handlers.OnAction(core.Click{
		Value:  map[string]any{"k": newKey, valPick: "1"},
		Form:   map[string]string{"agent": agentClaude, "place": "work / from-card"},
		Answer: folderFoo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Toast, "✔ Started") {
		t.Fatalf("toast = %q", result.Toast)
	}
	_, opts := r.lastRun()
	if opts["where"] != "work / from-card" {
		t.Fatalf("where = %q, want the card's place", opts["where"])
	}

	// 坏 place：toast 报错，不起终端。
	result, err = r.chat.Handlers.OnAction(core.Click{
		Value:  map[string]any{"k": newKey, valPick: "1"},
		Form:   map[string]string{"agent": agentClaude, "place": "no-slash"},
		Answer: folderFoo,
	})
	if err != nil || !strings.Contains(result.Toast, "<space>/<tab>") || result.Card != nil {
		t.Fatalf("bad place: %v %+v", err, result)
	}
}

// /new 起跑后的自动代答：claude 在新目录停在信任确认上——替你选 Yes；
// 别的 agent 不出手。
func TestAutoTrustAnswersClaudesFolderQuestion(t *testing.T) {
	r := harness(t)
	r.start(t)
	r.hc.SetScreen("t99", trustScreen) // 起跑即停在信任确认 ( RunTerminal 的假地址是 t99 )

	if _, ok := r.e.startTerminal(startRequest{folder: folderFoo}); !ok {
		t.Fatal("startTerminal failed")
	}
	r.waitFor(t, "代答按键", func() bool {
		for _, k := range r.hc.KeysPressed() {
			if k.Address == "t99" && len(k.Keys) == 2 && k.Keys[0] == "down" && k.Keys[1] == keyEnter {
				return true
			}
		}
		return false
	})

	// codex 起跑：信任屏在也不代答。
	r.hc.ClearKeysPressed()
	r.hc.SetScreen("t99", trustScreen)
	if _, ok := r.e.startTerminal(startRequest{folder: folderFoo, command: "codex"}); !ok {
		t.Fatal("startTerminal (codex) failed")
	}

	// 给 lane 一点时间证明它没出手。
	time.Sleep(100 * time.Millisecond)
	for _, k := range r.hc.KeysPressed() {
		if k.Address == "t99" {
			t.Fatalf("codex must not be auto-answered: %+v", k)
		}
	}
}
