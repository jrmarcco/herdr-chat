package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/config"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/service"
)

// CLI 行为测试：
// --profile 校验、usage、EPIPE，
// 加上 init、logs、hooks、pair、start 的错误路径。

// 全部在临时 home 上跑，hc 与服务都是注入的，绝不碰真实的 home 与真实的 hc。
const (
	cmdStatus = "status"
	cmdInit   = "init"
	cmdHooks  = "hooks"
	cmdStart  = "start"
	cmdPair   = "pair"
	flagDay   = "--day"
)

func newApp(t *testing.T) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	home := t.TempDir()
	var out, errOut bytes.Buffer
	a := &App{
		Env:      map[string]string{"HOME": home},
		Home:     home,
		Stdin:    strings.NewReader(""),
		Stdout:   &out,
		Stderr:   &errOut,
		Bin:      filepath.Join(home, "bin", "herdrchat"),
		Platform: "linux",
		Version:  "test",
		HC:       &hccli.CLI{Bin: "/bin/false", Env: []string{}},
	}
	t.Cleanup(func() {
		_ = os.MkdirAll(filepath.Dir(a.Bin), 0o700)
		//nolint:gosec // G306: 服务要可执行文件，写进测试临时目录。
		_ = os.WriteFile(a.Bin, []byte("#!/bin/sh\n"), 0o755)
	})
	return a, &out, &errOut
}

func (a *App) writeConfig(t *testing.T, pl config.Places, text string) {
	t.Helper()
	if err := os.MkdirAll(pl.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.configFile(pl), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUsageUnknownAndVersion(t *testing.T) {
	a, out, errOut := newApp(t)

	if code := a.Run(nil); code != 2 || !strings.Contains(errOut.String(), "usage: herdrchat") {
		t.Fatalf("no command: code %d, stderr %q", code, errOut.String())
	}
	errOut.Reset()
	if code := a.Run([]string{"frobnicate"}); code != 2 || !strings.Contains(errOut.String(), `unknown command "frobnicate"`) {
		t.Fatalf("unknown: code %d, stderr %q", code, errOut.String())
	}
	if code := a.Run([]string{"--help"}); code != 0 || !strings.Contains(out.String(), "usage: herdrchat") {
		t.Fatalf("help: code %d, out %q", code, out.String())
	}
	out.Reset()
	if code := a.Run([]string{"version"}); code != 0 || out.String() != "herdrchat test\n" {
		t.Fatalf("version: code %d, out %q", code, out.String())
	}
}

func TestProfileNameIsValidated(t *testing.T) {
	a, _, errOut := newApp(t)
	code := a.Run([]string{"--profile", "bad name", cmdStatus})
	if code != 2 || !strings.Contains(errOut.String(), "use letters, digits") {
		t.Fatalf("code %d, stderr %q", code, errOut.String())
	}

	// --profile=<name> 的形式也认；合法名一路走到 status。
	a2, out, _ := newApp(t)
	a2.Env["HERDRCHAT_PROFILE"] = ""
	if code := a2.Run([]string{"--profile=work", "status"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(out.String(), "settings  "+filepath.Join(a2.Home, ".config", "herdrchat", "work", "config.env")) {
		t.Fatalf("out %q", out.String())
	}
}

func TestInitWritesAStarterConfig(t *testing.T) {
	a, out, _ := newApp(t)
	if code := a.Run([]string{cmdInit}); code != 0 {
		t.Fatalf("code %d", code)
	}
	pl, _ := config.PlacesOf(a.Env, a.Home)
	file := a.configFile(pl)
	//nolint:gosec // G304: 测试自己写的临时文件。
	raw, err := os.ReadFile(file)
	if err != nil || len(raw) == 0 {
		t.Fatalf("config not written: %v", err)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, only you read it", info.Mode().Perm())
	}
	original := string(raw)

	// 已有则不动。
	out.Reset()
	if code := a.Run([]string{cmdInit}); code != 0 || !strings.Contains(out.String(), "already there") {
		t.Fatalf("code %d out %q", code, out.String())
	}
	//nolint:gosec // G304: 同上。
	if again, _ := os.ReadFile(file); string(again) != original {
		t.Fatal("init overwrote an existing config")
	}

	// 本构建只说飞书。
	if code := a.Run([]string{cmdInit, "--chat", "telegram", "--profile", "tg"}); code != 2 {
		t.Fatalf("telegram: code %d", code)
	}
}

func TestLogsFiltersTailsAndPicksADay(t *testing.T) {
	a, _, _ := newApp(t)
	pl, _ := config.PlacesOf(a.Env, a.Home)
	dir := a.logDir(pl)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	today := time.Now().Format("2006-01-02")
	lines := "12:00:01.000 INFO  feishu     connected\n" +
		"12:00:05.000 WARN  engine    a retry recovered\n" +
		"12:00:06.000 ERROR feishu    send failed\n" +
		"12:00:07.000 INFO  engine    fine again\n"
	if err := os.WriteFile(a.logFile(pl, today), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.logFile(pl, "2026-01-31"), []byte("09:00:00.000 ERROR old      boom\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	a.Stdout = &out
	if code := a.cmdLogs(nil); code != 0 || strings.Count(out.String(), "\n") != 4 {
		t.Fatalf("all: code %d out %q", code, out.String())
	}

	out.Reset()
	if code := a.cmdLogs([]string{"--errors"}); code != 0 || !strings.Contains(out.String(), "ERROR") || strings.Contains(out.String(), "WARN") {
		t.Fatalf("errors: code %d out %q", code, out.String())
	}

	out.Reset()
	if code := a.cmdLogs([]string{"-n", "2"}); code != 0 || !strings.HasPrefix(out.String(), "12:00:06.000") {
		t.Fatalf("tail: code %d out %q", code, out.String())
	}

	out.Reset()
	if code := a.cmdLogs([]string{flagDay, "2026-01-31"}); code != 0 || !strings.Contains(out.String(), "boom") {
		t.Fatalf("day: code %d out %q", code, out.String())
	}
	if code := a.cmdLogs([]string{flagDay, "2026-13-99"}); code != 2 {
		t.Fatalf("bad day: code %d", code)
	}

	// 换个空 home：没有日志。
	a.Home = t.TempDir()
	if code := a.cmdLogs([]string{flagDay, "2020-01-01"}); code != 1 {
		t.Fatalf("missing day: code %d", code)
	}
}

func TestHooksShowInstallAndRefuseWithoutATerminal(t *testing.T) {
	a, out, errOut := newApp(t)

	// 空机器：两行"没装"。
	if code := a.Run([]string{cmdHooks}); code != 0 ||
		!strings.Contains(out.String(), "claude: not set up") || !strings.Contains(out.String(), "codex: not set up") {
		t.Fatalf("status: code %d out %q", code, out.String())
	}

	// 有 .claude：非 TTY 且无 --yes → 拒绝。
	if err := os.MkdirAll(filepath.Join(a.Home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}

	errOut.Reset()
	if code := a.Run([]string{cmdHooks, "install"}); code != 0 || !strings.Contains(errOut.String(), "not a terminal") {
		t.Fatalf("refuse: code %d err %q", code, errOut.String())
	}

	// --yes：装上、报告位置。
	out.Reset()
	if code := a.Run([]string{cmdHooks, "install", "--yes"}); code != 0 || !strings.Contains(out.String(), "puts herdrchat's hook in") {
		t.Fatalf("install: code %d out %q", code, out.String())
	}
	out.Reset()
	if code := a.Run([]string{cmdHooks}); code != 0 || !strings.Contains(out.String(), "claude: herdrchat's hook is in") {
		t.Fatalf("status after: code %d out %q", code, out.String())
	}

	// TTY 里问，答 n：什么都不动。
	a.TTY = true
	a.Stdin = strings.NewReader("n\n")
	a.scanner = nil
	out.Reset()
	if code := a.Run([]string{cmdHooks, "uninstall"}); code != 0 || !strings.Contains(out.String(), "nothing changed") {
		t.Fatalf("answer n: code %d out %q", code, out.String())
	}

	a.Stdin = strings.NewReader("y\n")
	a.scanner = nil
	out.Reset()
	if code := a.Run([]string{cmdHooks, "uninstall"}); code != 0 || !strings.Contains(out.String(), "out") {
		t.Fatalf("answer y: code %d out %q", code, out.String())
	}
}

func TestStartRefusesWithoutKeysOrSessions(t *testing.T) {
	a, _, errOut := newApp(t)
	pl, _ := config.PlacesOf(a.Env, a.Home)

	// 没有配置：缺键。
	if code := a.Run([]string{cmdStart}); code != 1 || !strings.Contains(errOut.String(), "FEISHU_APP_ID") {
		t.Fatalf("no keys: code %d err %q", code, errOut.String())
	}

	// 有键，session 不在本机。
	a.writeConfig(t, pl, "FEISHU_APP_ID=a\nFEISHU_APP_SECRET=b\nHERDR_SESSIONS=ghost\n")
	errOut.Reset()
	if code := a.Run([]string{cmdStart}); code != 1 || !strings.Contains(errOut.String(), "session ghost is not on this machine") {
		t.Fatalf("ghost session: code %d err %q", code, errOut.String())
	}

	// HERDRCHAT_CHAT=telegram：本构建只说飞书。
	a.writeConfig(t, pl, "HERDRCHAT_CHAT=telegram\nTELEGRAM_BOT_TOKEN=x\n")
	errOut.Reset()
	if code := a.Run([]string{cmdStart}); code != 1 || !strings.Contains(errOut.String(), "Feishu only") {
		t.Fatalf("telegram: code %d err %q", code, errOut.String())
	}
}

func TestPairRefusesWhenOwnerIsSetOrBotIsDown(t *testing.T) {
	a, out, _ := newApp(t)
	pl, _ := config.PlacesOf(a.Env, a.Home)
	a.writeConfig(t, pl, "FEISHU_APP_ID=a\nFEISHU_APP_SECRET=b\nFEISHU_OWNER_OPEN_ID=ou_x\n")
	if code := a.Run([]string{cmdPair}); code != 0 || !strings.Contains(out.String(), "already set") {
		t.Fatalf("owner set: code %d out %q", code, out.String())
	}

	a2, _, errOut2 := newApp(t)
	pl2, _ := config.PlacesOf(a2.Env, a2.Home)
	a2.writeConfig(t, pl2, "FEISHU_APP_ID=a\nFEISHU_APP_SECRET=b\n")
	if code := a2.Run([]string{cmdPair}); code != 1 || !strings.Contains(errOut2.String(), "not running") {
		t.Fatalf("bot down: code %d err %q", code, errOut2.String())
	}
}

// pair 的成功路径：
// 服务在跑 ( 注入的 systemctl 应答 )，主人把 owner 写进config，轮询看到、以"连上了"收尾。
func TestPairEndsWithTheOwnerWrittenAndTheBotConnected(t *testing.T) {
	a, out, _ := newApp(t)
	pl, _ := config.PlacesOf(a.Env, a.Home)
	a.writeConfig(t, pl, "FEISHU_APP_ID=a\nFEISHU_APP_SECRET=b\n")

	svcFile := filepath.Join(a.Home, "svc.service")
	if err := os.WriteFile(svcFile, []byte("[Unit]\n"), 0o644); err != nil { //nolint:gosec // G306: systemd 读的 unit 文件本来就是 0644
		t.Fatal(err)
	}
	a.SvcOf = func(spec service.Spec) *service.Service {
		return &service.Service{
			Kind: service.KindSystemd, File: svcFile,
			Label: "svc.service", Spec: spec, UID: 1000,
			Run: func(_ context.Context, _ string, _ ...string) (string, error) {
				return "ActiveState=active\nMainPID=5\n", nil
			},
		}
	}
	steps := 0
	a.Sleep = func(time.Duration) {
		steps++
		if steps == 1 {
			// bot 收到码、认下主人 ( 引擎那边的事，这里直接落 config )。
			if err := config.SetConfigValue(a.configFile(pl), "FEISHU_OWNER_OPEN_ID", "ou_you"); err != nil {
				t.Fatal(err)
			}
		}
		if steps == 2 {
			// 实例连上了。
			if err := os.MkdirAll(a.logDir(pl), 0o700); err != nil {
				t.Fatal(err)
			}
			day := a.logDay
			if err := os.WriteFile(a.logFile(pl, day()), []byte("12:00:00.000 INFO  feishu     connected\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if code := a.Run([]string{cmdPair}); code != 0 {
		t.Fatalf("code %d out %q", code, out.String())
	}
	if !strings.Contains(out.String(), "✅ paired") {
		t.Fatalf("out %q", out.String())
	}
	st, err := os.ReadFile(filepath.Join(pl.StateDir, "state.json"))
	if err != nil || !strings.Contains(string(st), `"pairing"`) {
		t.Fatalf("pairing was not written first: %v %s", err, st)
	}
	// 码是 6 位数字。
	out.Reset()
}

func TestStatusReportsPathsKeysAndHonesty(t *testing.T) {
	a, out, _ := newApp(t)
	pl, _ := config.PlacesOf(a.Env, a.Home)
	a.writeConfig(t, pl, "FEISHU_APP_ID=a\n")
	// default session 在本机 ( 有 core.json、core 不在跑 )。
	if err := os.MkdirAll(filepath.Join(a.Home, ".local", "state", "herdr-core", "default"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Home, ".local", "state", "herdr-core", "default", "core.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := a.Run([]string{"status"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	text := out.String()
	for _, want := range []string{
		"settings  " + a.configFile(pl),
		"FEISHU_APP_ID set", "FEISHU_APP_SECRET MISSING", "owner missing",
		"core down", "not running", "/auth     not set up", "hook",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("status lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "FEISHU_APP_SECRET=b") || strings.Contains(text, "=a") {
		t.Fatalf("status prints secrets:\n%s", text)
	}
}

func TestLoopScriptCarriesWhatItNeedsAndNothingElse(t *testing.T) {
	a, _, _ := newApp(t)
	a.Env["HERDRCHAT_HC"] = "/opt/hc"
	a.Env["CLAUDECODE"] = "1"
	a.Env["HERDRCHAT_PROFILE"] = "work"
	pl, _ := config.PlacesOf(a.Env, a.Home)
	script := a.loopScript(pl)
	for _, want := range []string{
		"export 'HERDRCHAT_HC=/opt/hc'", "export 'HERDRCHAT_PROFILE=work'",
		"while true; do '" + a.Bin + "' run", "sleep 3",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script lacks %q:\n%s", want, script)
		}
	}
	for _, banned := range []string{"CLAUDE", "PATH"} {
		if strings.Contains(script, banned) {
			t.Fatalf("script carries %s:\n%s", banned, script)
		}
	}
}

func TestWaitConnectedTimesOutWithTheErrorsItFound(t *testing.T) {
	a, _, errOut := newApp(t)
	pl, _ := config.PlacesOf(a.Env, a.Home)
	if err := os.MkdirAll(a.logDir(pl), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.logFile(pl, a.logDay()), []byte("12:00:01.000 ERROR feishu    no route\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.Sleep = func(time.Duration) {}
	a.ConnectWait = 10 * time.Millisecond

	// 立刻到点。
	if code := a.waitConnected(pl, "feishu", 0); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errOut.String(), "no route") {
		t.Fatalf("stderr %q", errOut.String())
	}
}

// EPIPE：管道断了就安静收场，不是一场报错。
func TestPipeSafeExitsQuietlyOnABrokenPipe(t *testing.T) {
	exited := false
	w := PipeSafe(brokenPipe{}, func() { exited = true })
	_, err := w.Write([]byte("hello"))
	if err == nil || !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("err = %v, want EPIPE", err)
	}
	if !exited {
		t.Fatal("the exit hook did not run")
	}
}

type brokenPipe struct{}

func (brokenPipe) Write([]byte) (int, error) { return 0, syscall.EPIPE }

func TestConfigExampleIsTheShippedOne(t *testing.T) {
	// 示例配置的正本就是这里的内嵌副本 ( `herdrchat init` 写出的即它 )。
	// 锁住它仍是一份能照着填的配置：每个键都在，注释说明在；
	// 本构建只说飞书，别的 app 的键不该再出现在模板里。
	text := string(configExample)
	for _, key := range []string{
		"HERDRCHAT_CHAT=", "HERDR_SESSIONS=", "FEISHU_APP_ID=", "FEISHU_APP_SECRET=",
		"FEISHU_OWNER_OPEN_ID=", "HERDR_MUTED_SPACES=", "HERDR_HISTORY=",
		"HERDR_BOT_TAGS=", "HERDR_USER_TAGS=",
	} {
		if !strings.Contains(text, key) {
			t.Fatalf("the embedded config.env.example lost %s", key)
		}
	}
	for _, banned := range []string{"TELEGRAM_", "--- Telegram"} {
		if strings.Contains(text, banned) {
			t.Fatalf("the embedded config.env.example still mentions %s", banned)
		}
	}
	if !strings.Contains(text, "herdrchat start") || !strings.Contains(text, "Feishu only") {
		t.Fatal("the embedded config.env.example lost its guidance comments")
	}
}
