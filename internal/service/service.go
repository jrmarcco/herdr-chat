// Package service 让 bot 成为系统的一项服务：
// macOS 上是 launchd agent，
// Linux 上是 systemd user unit。
// 它登录即起、崩溃自回，不依赖任何 hc core。
//
// 服务跑的是二进制自己的绝对路径 ( `<bin> run` )；
// 二进制被挪走由 BinGone 指出 ( status 提示重新 install )。
//
// 服务只带它需要的变量 ( hc 的路径、PATH、profile、目录 )，
// agent 的变量 ( CLAUDECODE、CLAUDE_CODE_* ) 一个也不跟进来。
package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 服务的两种形态。
const (
	KindLaunchd = "launchd"
	KindSystemd = "systemd"
)

// 重启节流。
const (
	throttleSeconds = 10 // launchd ThrottleInterval
	restartSeconds  = 5  // systemd RestartSec
	callTimeout     = 30 * time.Second
)

// envKeys 是服务带得进去的变量白名单。
var envKeys = []string{
	"HERDRCHAT_HC", "HERDRCHAT_PROFILE", "HERDRCHAT_HOME", "PATH",
	"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME",
}

// EnvFor 从进程环境里挑出服务该带的变量 ( 空值不带 )，并钉死 profile。
func EnvFor(env map[string]string, profile string) map[string]string {
	out := map[string]string{}
	for _, k := range envKeys {
		if v := env[k]; v != "" {
			out[k] = v
		}
	}
	out["HERDRCHAT_PROFILE"] = profile
	return out
}

// Spec 是一项服务要跑的东西。
type Spec struct {
	ID   string            // profile 的系统名
	Bin  string            // herdrchat 二进制的绝对路径
	Args []string          // 它的参数 ( 通常是 ["run"] )
	Env  map[string]string // 挑过的环境变量
	Out  string            // 日志打开之前的输出落在这 ( service.out.log )
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func xmlText(s string) string { return xmlEscaper.Replace(s) }

// LaunchdPlist 生成 launchd agent 的 plist：全路径、登录即起、崩溃自回。
// 骨架在 launchd.plist.tmpl；这里只准备数据（转义由模板的 xml 函数做）。
func LaunchdPlist(label string, s Spec) string {
	env := make([]kv, 0, len(s.Env))
	for _, k := range sortedKeys(s.Env) {
		env = append(env, kv{Key: k, Value: s.Env[k]})
	}
	return render("launchd.plist.tmpl", plistData{
		Label: label, Args: append([]string{s.Bin}, s.Args...),
		Throttle: throttleSeconds, Env: env, Out: s.Out,
	})
}

// unitQuote 按 systemd 的规矩给一个词加引号：反斜杠与引号转义，% 是它的
// 说明符前缀，要写成 %%。
func unitQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "%", "%%")
	return `"` + s + `"`
}

// SystemdUnit 生成 systemd user unit：总是重启，路径与变量都加引号。
// 骨架在 systemd.unit.tmpl；引号与 $ 转义在这里做齐，模板只管排布。
func SystemdUnit(s Spec) string {
	words := make([]string, 0, len(s.Args)+1)
	for _, arg := range append([]string{s.Bin}, s.Args...) {
		// ExecStart 里 $ 会被当成变量展开。
		words = append(words, strings.ReplaceAll(unitQuote(arg), "$", "$$"))
	}
	env := make([]string, 0, len(s.Env))
	for _, k := range sortedKeys(s.Env) {
		env = append(env, unitQuote(k+"="+s.Env[k]))
	}
	return render("systemd.unit.tmpl", unitData{
		ID: s.ID, ExecStart: strings.Join(words, " "),
		Env: env, Out: s.Out, Restart: restartSeconds,
	})
}

// Runner 跑一条系统命令 ( launchctl / systemctl )，返回它的输出。测试替换它。
type Runner func(ctx context.Context, name string, args ...string) (string, error)

func realRunner(parent context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, callTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // G204: 命令是固定的 launchctl/systemctl，参数由本包拼出
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), text)
	}
	return text, nil
}

// Service 是一个 profile 在这台机器上的服务。
type Service struct {
	Kind  string // launchd | systemd
	File  string // 服务文件
	Label string // launchd 的 Label / systemd 的 unit 名
	Spec  Spec
	// Run 跑 launchctl / systemctl ( 测试注入 )。
	Run Runner
	// UID 是 launchd 的 gui 域 ( 测试注入 )。
	UID int
}

// Of 说出这个平台上该 profile 的服务；平台不支持时返回 nil。
func Of(platform, home string, env map[string]string, s Spec) *Service {
	switch platform {
	case "darwin":
		label := "app.herdrchat." + s.ID
		return &Service{
			Kind: KindLaunchd, Label: label, Spec: s, Run: realRunner, UID: os.Getuid(),
			File: filepath.Join(home, "Library", "LaunchAgents", label+".plist"),
		}
	case "linux":
		configRoot := filepath.Join(home, ".config")
		if v := env["XDG_CONFIG_HOME"]; v != "" {
			configRoot = v
		}
		unit := "herdrchat-" + s.ID + ".service"
		return &Service{
			Kind: KindSystemd, Label: unit, Spec: s, Run: realRunner, UID: os.Getuid(),
			File: filepath.Join(configRoot, "systemd", "user", unit),
		}
	}
	return nil
}

// Content 是服务文件该有的内容。
func (s *Service) Content() string {
	if s.Kind == KindLaunchd {
		return LaunchdPlist(s.Label, s.Spec)
	}
	return SystemdUnit(s.Spec)
}

// Installed：服务文件在不在。
func (s *Service) Installed() bool {
	_, err := os.Stat(s.File)
	return err == nil
}

// Write 写下服务文件 ( 每次重写：路径或变量变了就跟着变 )。
func (s *Service) Write() error {
	//nolint:gosec // G301: LaunchAgents / systemd user 目录是系统约定的 0755。
	if err := os.MkdirAll(filepath.Dir(s.File), 0o755); err != nil {
		return err
	}
	//nolint:gosec // G306: 服务文件由 launchd/systemd 读取，0644 是约定 mode ( 不含秘密 )。
	return os.WriteFile(s.File, []byte(s.Content()), 0o644)
}

// Remove 移走服务文件。
func (s *Service) Remove() error {
	if err := os.Remove(s.File); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// BinGone：服务文件里写的二进制已经不在了就返回它的路径 ( status 据此提示重新 install )；
// 还在返回 ""。
func (s *Service) BinGone() string {
	if _, err := os.Stat(s.Spec.Bin); err != nil {
		return s.Spec.Bin
	}
	return ""
}

func (s *Service) domain() string { return "gui/" + strconv.Itoa(s.UID) }

func (s *Service) target() string { return s.domain() + "/" + s.Label }

// Start ( 重 ) 起服务：launchd 先卸再装 ( 文件变了才生效 )，
// systemd 重读后 enable + restart。
func (s *Service) Start(ctx context.Context) error {
	if s.Kind == KindLaunchd {
		_, _ = s.Run(ctx, "launchctl", "bootout", s.target())
		_, err := s.Run(ctx, "launchctl", "bootstrap", s.domain(), s.File)
		return err
	}
	if _, err := s.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if _, err := s.Run(ctx, "systemctl", "--user", "enable", s.Label); err != nil {
		return err
	}
	_, err := s.Run(ctx, "systemctl", "--user", "restart", s.Label)
	return err
}

// Stop 停下服务 ( 文件留着：下次登录或 start 还会起来 )。
func (s *Service) Stop(ctx context.Context) error {
	if s.Kind == KindLaunchd {
		if !s.loaded(ctx) {
			return nil // 没装着：已经是停的
		}
		_, err := s.Run(ctx, "launchctl", "bootout", s.target())
		return err
	}
	_, err := s.Run(ctx, "systemctl", "--user", "stop", s.Label)
	return err
}

// Disable 停下并不再随登录启动 ( uninstall 用；之后调用方 Remove 文件 )。
func (s *Service) Disable(ctx context.Context) error {
	if s.Kind == KindLaunchd {
		return s.Stop(ctx)
	}
	_, err := s.Run(ctx, "systemctl", "--user", "disable", "--now", s.Label)
	return err
}

// Reload 在服务文件移走之后让 systemd 重读 ( launchd 无此步 )。
func (s *Service) Reload(ctx context.Context) {
	if s.Kind == KindSystemd {
		_, _ = s.Run(ctx, "systemctl", "--user", "daemon-reload")
	}
}

func (s *Service) loaded(ctx context.Context) bool {
	_, err := s.Run(ctx, "launchctl", "print", s.target())
	return err == nil
}

// Running 说服务此刻是否在跑，以及它的 pid ( 不在跑为 0 )。
func (s *Service) Running(ctx context.Context) (pid int, running bool) {
	if s.Kind == KindLaunchd {
		out, err := s.Run(ctx, "launchctl", "print", s.target())
		if err != nil {
			return 0, false
		}
		return pidIn(out, "pid = "), strings.Contains(out, "state = running")
	}
	out, err := s.Run(ctx, "systemctl", "--user", "show", s.Label, "--property=ActiveState,MainPID")
	if err != nil {
		return 0, false
	}
	return pidIn(out, "MainPID="), strings.Contains(out, "ActiveState=active")
}

// pidIn 从 "<prefix><digits>" 的一行里读出 pid。
func pidIn(text, prefix string) int {
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil {
				return n
			}
		}
	}
	return 0
}
