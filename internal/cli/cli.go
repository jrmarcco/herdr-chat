// Package cli 是 herdrchat 的每个子命令：setup、pair、init、
// start、stop、status、logs、install、uninstall、hooks、run、version。
//
// 依赖都从 App 注入 ( env、home、终端、时钟、hc 适配器、服务建造 )，测试
// 在临时 home 与假注入上跑，绝不碰真实 home 与真实的 hc。
package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/config"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/service"
	"github.com/jrmarcco/herdr-chat/internal/shell"
)

// usage 是给终端看的说明文案。
const usage = `herdrchat — see and answer every hc terminal from Feishu

usage: herdrchat <command> [options]

commands:
  setup      set a profile up: the chat app, its keys, the sessions, the first start
  pair       hand the bot to its owner with a 6-digit code
  init       write a starter config.env
  start      start the bot (a service, or a terminal that keeps it up)
  stop       stop it
  status     say whether it runs, and what is wrong if anything
  logs       its log, --errors for the problems, --day for another day
  install    keep it running as a system service
  uninstall  remove the service
  hooks      show, install or remove the PermissionRequest hook
  update     bring in the newest herdrchat, and restart the bot onto it
  run        run the bot in the foreground
  version    say which herdrchat this is

options:
  --profile <name>   another setup beside the default (default: default)`

// 等待与节拍。
const (
	ConnectWait  = 60 * time.Second
	ConnectTick  = 500 * time.Millisecond
	PairPollWait = 2 * time.Second
)

const (
	chatFeishu     = "feishu"
	keyAppID       = "FEISHU_APP_ID"
	keyAppSecret   = "FEISHU_APP_SECRET"         //nolint:gosec // G101: 键的名字不是敏感信息，值从不进代码。
	keyEncryptKey  = "FEISHU_ENCRYPT_KEY"        //nolint:gosec // G101: 键的名字不是敏感信息，值从不进代码。
	keyVerifyToken = "FEISHU_VERIFICATION_TOKEN" //nolint:gosec // G101: 键的名字不是敏感信息，值从不进代码。
	levelError     = "ERROR"
)

// App 是一次 CLI 运行的全部环境。
type App struct {
	Env map[string]string

	Home string

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	TTY bool // stdin 是终端 ( setup/hooks 要问问题 )

	Bin      string
	Platform string
	Version  string

	HC *hccli.CLI

	Now   func() time.Time
	Sleep func(time.Duration)

	// SvcOf 建这个平台上的服务 ( 测试注入 )；nil 用 service.Of。
	SvcOf func(spec service.Spec) *service.Service
	// CheckKeys 即时校验飞书密钥 ( 测试注入 )；nil 走真实的 tenant_access_token。
	CheckKeys func(ctx context.Context, appID, appSecret string) error
	// Stty 改终端回显 ( 测试注入 )；nil 用真的 stty。
	Stty func(arg string) bool
	// ConnectWait 覆盖等 "connected" 的时长 ( 测试注入；0 用默认 60s )。
	ConnectWait time.Duration
	// Latest 问最新 release ( 测试注入；nil 走 GitHub API )。
	Latest func(ctx context.Context) (Release, error)
	// Download 下载一个 URL ( 测试注入；nil 走真实的 HTTP )。
	Download func(ctx context.Context, url string) ([]byte, error)

	scanner *bufio.Scanner
}

// Run 分发一个子命令，返回退出码。
func (a *App) Run(args []string) int {
	args = a.takeProfile(args)
	if len(args) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version":
		a.println("herdrchat " + a.Version)
		return 0
	case "--help", "-h", "help":
		a.println(usage)
		return 0
	case "run":
		return a.cmdRun(rest)
	case "setup":
		return a.cmdSetup(rest)
	case "pair":
		return a.cmdPair(rest)
	case "init":
		return a.cmdInit(rest)
	case "start":
		return a.cmdStart(rest)
	case "stop":
		return a.cmdStop(rest)
	case "status":
		return a.cmdStatus(rest)
	case "logs":
		return a.cmdLogs(rest)
	case "install":
		return a.cmdInstall(rest)
	case "uninstall":
		return a.cmdUninstall(rest)
	case "hooks":
		return a.cmdHooks(rest)
	case "update":
		return a.cmdUpdate(rest)
	default:
		a.errf("herdrchat: unknown command %q\n\n%s\n", cmd, usage)
		return 2
	}
}

// takeProfile 摘出全局 profile，其余原样返回。
func (a *App) takeProfile(args []string) []string {
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--profile" || arg == "-p":
			if i+1 < len(args) {
				a.Env["HERDRCHAT_PROFILE"] = args[i+1]
				i++
			}
		case strings.HasPrefix(arg, "--profile="):
			a.Env["HERDRCHAT_PROFILE"] = strings.TrimPrefix(arg, "--profile=")
		default:
			rest = append(rest, arg)
		}
	}
	return rest
}

func (a *App) println(s ...any) {
	// EPIPE 由 PipeSafe 收场；写不进就写不进。
	_, _ = fmt.Fprintln(a.Stdout, s...)
}

func (a *App) printf(format string, s ...any) {
	_, _ = fmt.Fprintf(a.Stdout, format, s...)
}

func (a *App) errf(format string, s ...any) {
	_, _ = fmt.Fprintf(a.Stderr, format, s...)
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *App) sleep(d time.Duration) {
	if a.Sleep != nil {
		a.Sleep(d)
		return
	}
	time.Sleep(d)
}

// ask 在终端上问一句，读一行回答。
func (a *App) ask(prompt string) string {
	_, _ = fmt.Fprint(a.Stdout, prompt)
	if a.scanner == nil {
		a.scanner = bufio.NewScanner(a.Stdin)
	}
	if a.scanner.Scan() {
		return strings.TrimSpace(a.scanner.Text())
	}
	return ""
}

// places 算这个 profile 的落点；名字不合法直接报。
func (a *App) places() (config.Places, bool) {
	pl, err := config.PlacesOf(a.Env, a.Home)
	if err != nil {
		a.errf("herdrchat: %v\n", err)
		return config.Places{}, false
	}
	return pl, true
}

// configFile 是 profile 的 config.env 路径。
func (a *App) configFile(pl config.Places) string {
	return filepath.Join(pl.ConfigDir, "config.env")
}

// chatOf 说 config 配的是哪个 chat app；本构建只说飞书。
func (a *App) chatOf(pl config.Places) string {
	return config.ChatOf(a.configFile(pl), a.Env)
}

// loadConfig 读 config 并检查该 chat 的必填键。
func (a *App) loadConfig(pl config.Places) (config.Config, error) {
	file := a.configFile(pl)
	chat := a.chatOf(pl)
	if chat != chatFeishu {
		return config.Config{}, fmt.Errorf("%s: this build talks to Feishu only (HERDRCHAT_CHAT=%s)", file, chat)
	}
	return config.Load(file, a.Env, []string{keyAppID, keyAppSecret})
}

// ready 是要起 bot 的命令共用的第一步：目录、config、必填键。
func (a *App) ready() (config.Places, config.Config, bool) {
	pl, ok := a.places()
	if !ok {
		return pl, config.Config{}, false
	}
	if err := config.MakeDirs(pl); err != nil {
		a.errf("herdrchat: %v\n", err)
		return pl, config.Config{}, false
	}
	cfg, err := a.loadConfig(pl)
	if err != nil {
		a.errf("herdrchat: %v\n", err)
		return pl, cfg, false
	}
	return pl, cfg, true
}

// --- 日志文件 ---

// logDir 是 bot 的日志目录 ( logs/ )。
func (a *App) logDir(pl config.Places) string {
	return pl.StateDir + "/logs"
}

// logDay 是今天 ( 本地日期 )。
func (a *App) logDay() string {
	return a.now().Format("2006-01-02")
}

// logFile 是某天的日志文件。
func (a *App) logFile(pl config.Places, day string) string {
	return a.logDir(pl) + "/" + day + ".log"
}

// logSince 读某天日志里 offset 之后追加的部分。
func (a *App) logSince(pl config.Places, day string, offset int64) []string {
	raw, err := os.ReadFile(a.logFile(pl, day))
	if err != nil || int64(len(raw)) <= offset {
		return nil
	}
	text := string(raw[offset:])
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// logOffset 是此刻日志的大小 ( 只看此后追加的行 )。
func (a *App) logOffset(pl config.Places) int64 {
	info, err := os.Stat(a.logFile(pl, a.logDay()))
	if err != nil {
		return 0
	}
	return info.Size()
}

// connectedLine 认一条 "连上了" 的日志行 ( xlog 的形状：时间 级别 组件 消息 )。
func connectedLine(line, chat string) bool {
	fields := strings.Fields(line)
	return len(fields) >= 4 && fields[2] == chat && strings.HasPrefix(fields[3], "connected")
}

// waitConnected 等今天的日志里出现 "<chat> connected"；等不到就说最近 5 条
// ERROR、退出码 1。
func (a *App) connectWait() time.Duration {
	if a.ConnectWait > 0 {
		return a.ConnectWait
	}
	return ConnectWait
}

func (a *App) waitConnected(pl config.Places, chat string, offset int64) int {
	deadline := a.now().Add(a.connectWait())
	for {
		for _, line := range a.logSince(pl, a.logDay(), offset) {
			if connectedLine(line, chat) {
				return 0
			}
		}
		if !a.now().Before(deadline) {
			break
		}
		a.sleep(ConnectTick)
	}

	a.errf("herdrchat: the bot did not reach %s within %s. What went wrong:\n", chat, a.connectWait())

	var errors []string
	for _, line := range a.logSince(pl, a.logDay(), offset) {
		if f := strings.Fields(line); len(f) >= 2 && f[1] == levelError {
			errors = append(errors, line)
		}
	}
	if len(errors) == 0 {
		a.errf("  (no ERROR in the log; is %s's console set up — long connection events and card callbacks?)\n", chat)
	} else {
		for _, line := range errors[max(0, len(errors)-5):] {
			a.errf("  %s\n", line)
		}
	}
	return 1
}

// --- 服务与 hc 终端 ---

// serviceSpec 是这个 profile 的服务要跑的东西。
func (a *App) serviceSpec(pl config.Places) service.Spec {
	return service.Spec{
		ID:   pl.ID,
		Bin:  a.Bin,
		Args: []string{"run"},
		Env:  service.EnvFor(a.Env, pl.Profile),
		Out:  a.logDir(pl) + "/service.out.log",
	}
}

// serviceOf 建这个平台上的服务 ( nil = 这个平台没有 )。
func (a *App) serviceOf(pl config.Places) *service.Service {
	if a.SvcOf != nil {
		return a.SvcOf(a.serviceSpec(pl))
	}
	return service.Of(a.Platform, a.Home, a.Env, a.serviceSpec(pl))
}

// sessionsHere 列本机存在的 hc session。
func (a *App) sessionsHere() []string {
	return hccli.SessionsHere(a.Env, a.Home)
}

// allSessions 是可能与本 profile 有关的所有 session：配置的 ∪ 本机存在的。
func (a *App) allSessions(cfg config.Config) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range slices.Concat(cfg.Sessions, a.sessionsHere()) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}

// checkSessions：配置里的每个 session 必须真的在本机 ( 不存在的 session 不
// 起 bot，免得一个手误凭空造出一个 core )。
func (a *App) checkSessions(cfg config.Config) bool {
	here := map[string]bool{}
	for _, s := range a.sessionsHere() {
		here[s] = true
	}

	var missing []string
	for _, s := range cfg.Sessions {
		if !here[s] {
			missing = append(missing, s)
		}
	}
	if len(missing) == 0 {
		return true
	}
	a.errf(
		"herdrchat: session %s is not on this machine (ls ~/.local/state/herdr-core says: %s)\n",
		strings.Join(missing, ", "),
		strings.Join(a.sessionsHere(), ", "),
	)
	return false
}

// oneRun 是 bot 的一种在跑形态。
type oneRun struct {
	Where   string
	Service bool
}

// botRuns 说 bot 此刻在哪些形态里跑着 ( 服务、hc 终端 )。
func (a *App) botRuns(ctx context.Context, pl config.Places, cfg config.Config) []oneRun {
	var runs []oneRun
	if svc := a.serviceOf(pl); svc != nil && svc.Installed() {
		if _, running := svc.Running(ctx); running {
			runs = append(runs, oneRun{Where: "service (" + svc.Kind + ")", Service: true})
		}
	}
	for _, s := range a.allSessions(cfg) {
		if !hccli.CoreUp(ctx, a.Env, a.Home, s, time.Second) {
			continue
		}
		tr, err := a.HC.GetTerminal(ctx, s, pl.Terminal)
		if err == nil && tr.Live() {
			runs = append(runs, oneRun{Where: "hc terminal " + pl.Terminal + " (session " + s + ")"})
		}
	}
	return runs
}

// stopEverywhere 把 bot 从所有形态里停下 ( 双 bot 双答是最大忌讳 )，
// 返回停下了什么。
func (a *App) stopEverywhere(ctx context.Context, pl config.Places, cfg config.Config) []string {
	var stopped []string
	if svc := a.serviceOf(pl); svc != nil && svc.Installed() {
		_, running := svc.Running(ctx)
		if err := svc.Stop(ctx); err != nil {
			a.errf("herdrchat: the service did not stop: %v\n", err)
		} else if running {
			stopped = append(stopped, "the service")
		}
	}
	for _, s := range a.allSessions(cfg) {
		if !hccli.CoreUp(ctx, a.Env, a.Home, s, time.Second) {
			continue
		}
		tr, err := a.HC.GetTerminal(ctx, s, pl.Terminal)
		if err != nil || !tr.Live() {
			continue
		}
		if err := a.HC.Kill(ctx, s, pl.Terminal); err != nil {
			a.errf("herdrchat: %s in session %s did not stop: %v\n", pl.Terminal, s, err)
			continue
		}
		stopped = append(stopped, "hc terminal "+pl.Terminal+" (session "+s+")")
	}
	return stopped
}

// loopScript 是无服务时 bot 所在 hc 终端里跑的循环：退了 3 秒后再来。
func (a *App) loopScript(pl config.Places) string {
	env := service.EnvFor(a.Env, pl.Profile)
	// PATH 用终端自己的；其余变量才需要带进去。
	delete(env, "PATH")

	var assigns []string
	for _, k := range sortedKeysOf(env) {
		assigns = append(assigns, "export "+shell.Quote(k+"="+env[k]))
	}
	script := strings.Join(assigns, "\n")
	if script != "" {
		script += "\n"
	}
	// 只引二进制的路径：把 "run" 一起引进去，
	// sh 会去找一个名字里带空格的可执行文件
	// ( 127 not found —— 教训记在这 )。
	return script + "while true; do " + shell.Quote(a.Bin) + " run" + `; echo "herdrchat stopped ($?), starting again in 3 s"; sleep 3; done`
}

func sortedKeysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// PipeSafe 包一层输出：
// 管道断掉 ( EPIPE 之类 ) 时不再写、
// 叫 exit 安静收场 —— `herdrchat logs | head` 不该以一场报错误收场。
func PipeSafe(w io.Writer, exit func()) io.Writer {
	return pipeSafe{w: w, exit: exit}
}

type pipeSafe struct {
	w    io.Writer
	exit func()
}

func (p pipeSafe) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	if err != nil {
		p.exit()
		return n, err
	}
	return n, nil
}
