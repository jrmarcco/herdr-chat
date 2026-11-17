// Package hccli 是对 hc CLI 的薄封装：
// 每次调用都指名 session ( HC_SESSION )，绝不落在默认 core 上；
// bot 自己跑在 hc 终端里，其环境中的 HC_TERMINAL_ID、
// HC_SCOPE 与 HC_TERMINAL_SOCKET 会被剔掉。
// 不剔的话，bot 输入的内容会被 hc 包上 <hc from="tN"> 标签、当成另一个 agent 的留言，
// 而不是像你亲手敲进去一样抵达。
//
// 每次调用都带 deadline；
// 输出上限 16MB。
package hccli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 各类调用的超时。
const (
	TimeoutDefault = 15 * time.Second
	TimeoutRun     = 30 * time.Second
	TimeoutSend    = 60 * time.Second
	TimeoutPut     = 60 * time.Second
	MaxOutput      = 16 << 20

	// jsonFlag 是 hc 的 JSON 输出开关，凡解析应答的调用都带。
	jsonFlag = "--json"

	// kindFinished 是 hc 表示终端已结束的错误 kind，也是 events "finished"
	// 记录携带的状态词。
	kindFinished = "finished"
)

// hcTerminalEnv 是 bot 自己终端的环境变量，每次调用都剔除。
var hcTerminalEnv = map[string]bool{
	"HC_TERMINAL_ID":     true,
	"HC_SCOPE":           true,
	"HC_TERMINAL_SOCKET": true,
}

// CLI 运行 hc。New 基于进程环境创建；测试则基于 stub 脚本与自带环境创建。
type CLI struct {
	// Bin 是 hc 的路径：先看本字段，再看环境里的 HERDRCHAT_HC，最后是裸的。
	// "hc" ( 跑 bot 的 core 所在环境 PATH 里未必有 hc，跑不起 hc 的 bot 什么也看不见 )。
	Bin string
	// Env 是基础环境 ( New 时的 os.Environ )。
	Env []string
}

// New 基于进程环境创建 CLI。
func New() *CLI {
	return &CLI{Env: os.Environ()}
}

func (c *CLI) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	for _, kv := range c.Env {
		if v, ok := strings.CutPrefix(kv, "HERDRCHAT_HC="); ok && v != "" {
			return v
		}
	}
	return "hc"
}

// envFor 构造一次调用的环境：
// 去掉 bot 自身终端变量后的基础环境，加上 HC_SESSION=<session>。
func (c *CLI) envFor(session string) []string {
	env := make([]string, 0, len(c.Env)+1)
	for _, kv := range c.Env {
		key, _, _ := strings.Cut(kv, "=")
		if hcTerminalEnv[key] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HC_SESSION="+session)
}

// Run 运行一条 hc 命令并返回 stdout。
// 错误携带命令与 hc 自己的话：`hc <args>: <stderr || stdout || 原因>`。
func (c *CLI) Run(parent context.Context, session string, args []string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = TimeoutDefault
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	//nolint:gosec // G204: hc 路径与参数来自本机配置和引擎自身，非不可信输入。
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Env = c.envFor(session)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("hc %s: timed out after %s", strings.Join(args, " "), timeout)
	}
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			detail = err.Error()
		}
		return stdout.String(), fmt.Errorf("hc %s: %s", strings.Join(args, " "), detail)
	}
	if stdout.Len() > MaxOutput {
		return "", fmt.Errorf("hc %s: output past %dMB", strings.Join(args, " "), MaxOutput>>20)
	}
	return stdout.String(), nil
}

// jsonRun 运行一条 hc 命令并解析其 --json 输出。
func jsonRun[T any](c *CLI, ctx context.Context, session string, args []string, timeout time.Duration) (T, error) {
	var out T
	text, err := c.Run(ctx, session, args, timeout)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return out, fmt.Errorf("hc %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// Machine 是 session 连接的一台机器。
type Machine struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// Machines 列出 session 连接的机器；在线时 state 为 "connected"。
func (c *CLI) Machines(ctx context.Context, session string) ([]Machine, error) {
	out, err := jsonRun[struct {
		Machines []Machine `json:"machines"`
	}](c, ctx, session, []string{"ls", jsonFlag}, TimeoutDefault)
	return out.Machines, err
}

// Cores 给出 session 自己的 core id，以及它以机器形式够到的 core：
// 一条 link 的 id 就是它所达 core 的 id。
func (c *CLI) Cores(ctx context.Context, session string) (self string, links []MachineWithID, err error) {
	type statusOut struct {
		Core struct {
			ID string `json:"id"`
		} `json:"core"`
	}
	type lsOut struct {
		Machines []MachineWithID `json:"machines"`
	}
	st, err := jsonRun[statusOut](c, ctx, session, []string{"server", "status", jsonFlag}, TimeoutDefault)
	if err != nil {
		return "", nil, err
	}
	ls, err := jsonRun[lsOut](c, ctx, session, []string{"machine", "ls", jsonFlag}, TimeoutDefault)
	if err != nil {
		return "", nil, err
	}
	return st.Core.ID, ls.Machines, nil
}

// MachineWithID 是带所达 core id 的机器连接。
type MachineWithID struct {
	Name  string `json:"name"`
	ID    string `json:"id"`
	State string `json:"state"`
}

// GetTerminal 取一个终端的完整记录。
func (c *CLI) GetTerminal(ctx context.Context, session, address string) (Terminal, error) {
	return jsonRun[Terminal](c, ctx, session, []string{"get", address, jsonFlag}, TimeoutDefault)
}

// RunOpts 启动一个后台终端 ( hc run )，无父进程以便存活。
type RunOpts struct {
	Name   string
	Cwd    string
	Labels map[string]string
	Argv   []string
}

func (c *CLI) RunTerminal(ctx context.Context, session string, o RunOpts) (json.RawMessage, error) {
	args := []string{"run", "--bg", "--no-parent", "--json"}
	if o.Name != "" {
		args = append(args, "--name", o.Name)
	}
	args = append(args, "--cwd", o.Cwd)
	for k, v := range o.Labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, "--")
	args = append(args, o.Argv...)
	text, err := c.Run(ctx, session, args, TimeoutRun)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(text), nil
}

// Send 向终端输入文本。Force 表示程序被对话框挡住也照样输入 ( 即回答对话框 )。
// 先给 `--`：你输入的内容绝不会被当成选项，即使以 "-" 开头。
func (c *CLI) Send(ctx context.Context, session, address, text string, force bool) error {
	args := []string{"send"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, address, "--", text)
	_, err := c.Run(ctx, session, args, TimeoutSend)
	return err
}

// PutFile 把本地文件送到终端旁边、它所在的机器上；返回文件在那边落地的路径。
func (c *CLI) PutFile(ctx context.Context, session, address, localFile string) (string, error) {
	out, err := jsonRun[struct {
		Path string `json:"path"`
	}](c, ctx, session, []string{"put", address, localFile, jsonFlag}, TimeoutPut)
	return out.Path, err
}

// Label 给终端设置标签 ( 空值表示删除该标签 )。
func (c *CLI) Label(ctx context.Context, session, address string, labels map[string]string) error {
	args := []string{"label", address}
	for k, v := range labels {
		args = append(args, k+"="+v)
	}
	_, err := c.Run(ctx, session, args, TimeoutDefault)
	return err
}

// Keys 在终端里按键 ( "1"、"esc"、"tab"、"up"、"down"、"enter" )。
func (c *CLI) Keys(ctx context.Context, session, address string, keys []string) error {
	_, err := c.Run(ctx, session, append([]string{"keys", address}, keys...), TimeoutDefault)
	return err
}

// Kill 结束一个终端。
func (c *CLI) Kill(ctx context.Context, session, address string) error {
	_, err := c.Run(ctx, session, []string{"kill", address}, TimeoutDefault)
	return err
}

// ScreenTail 读取终端屏幕的最后几行 ( 对话框，以及无 agent 终端的回应 )，
// 按人读的习惯整理。
func (c *CLI) ScreenTail(ctx context.Context, session, address string, lines int) (string, error) {
	text, err := c.Run(ctx, session, []string{"read", address, "--lines", fmt.Sprint(lines)}, TimeoutDefault)
	if err != nil {
		return "", err
	}
	all := strings.Split(text, "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.TrimRight(strings.Join(all, "\n"), " \t\n"), nil
}

// SplitWhere 拆开 herdr-app 给它展示的每个终端打的 "<space> / <tab>" 标签；
// tab 保留其后可能出现的 " / "。
func SplitWhere(where string) (space, tab string, ok bool) {
	i := strings.Index(where, " / ")
	if i < 0 {
		return "", "", false
	}
	return where[:i], where[i+3:], true
}

// MachineOf 说出地址所在的机器 ( "box/t4t" 中的 "box" )；终端在本机时返回 ""。
func MachineOf(address string) string {
	i := strings.LastIndex(address, "/")
	if i < 0 {
		return ""
	}
	return address[:i]
}

// IsUnavailable 判断一个 hc 错误是否表示终端所在的机器够不到
// ( hc JSON 里的 "kind":"unavailable" )。
func IsUnavailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), `"kind":"unavailable"`)
}

// PlainError 把失败的 hc 调用说成人读得懂的话：hc 自己的 message，
// 不带命令行与 JSON 包装。
func PlainError(err error) string {
	if err == nil {
		return ""
	}
	raw := err.Error()
	if i := strings.LastIndex(raw, `{"error":`); i >= 0 {
		var wrapped struct {
			Error struct {
				Message string `json:"message"`
				Kind    string `json:"kind"`
			} `json:"error"`
		}
		if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(raw[i:])), &wrapped); jsonErr == nil {
			if wrapped.Error.Kind == kindFinished {
				return "this terminal has ended."
			}
			if wrapped.Error.Message != "" {
				return wrapped.Error.Message
			}
		}
	}

	if after, ok := strings.CutPrefix(raw, "hc "); ok {
		if j := strings.Index(after, ": "); j >= 0 {
			return after[j+2:]
		}
	}
	return raw
}

// TidyScreen 按人的习惯读屏幕：无行尾空格、首尾无空行、不连续两个空行；
// 给了 last 就只取最后那些行 ( 对话框坐在底部 )。
func TidyScreen(text string, last int) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" && (len(lines) == 0 || lines[len(lines)-1] == "") {
			continue
		}
		lines = append(lines, line)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if last >= 0 && len(lines) > last {
		lines = lines[len(lines)-last:]
	}
	return strings.Join(lines, "\n")
}

// ScreensDir 是一个 session 的屏幕快照目录 ( hc 写下的 <地址>.snap )。
func ScreensDir(env map[string]string, home, session string) string {
	return filepath.Join(StateRoot(env, home), session, "screens")
}

// StateRoot 是 hc 存各 session 状态的地方
// ( 拿不存在的 session 去问 hc 会把它启动：一次手误不能凭空造出一个 session )。
func StateRoot(env map[string]string, home string) string {
	if v := env["XDG_STATE_HOME"]; v != "" {
		return filepath.Join(v, "herdr-core")
	}
	return filepath.Join(home, ".local", "state", "herdr-core")
}

// SessionsHere 从 hc 自己的目录列出本机的 hc session。
func SessionsHere(env map[string]string, home string) []string {
	root := StateRoot(env, home)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), "core.json")); err == nil {
			names = append(names, e.Name())
		}
	}
	return names
}

// CoreUp 判断一个 session 的 hc core 是否在跑，且不惊动它：
// 它的 socket ( 记在 core.lock 首行 ) 能否接受连接。
func CoreUp(ctx context.Context, env map[string]string, home, session string, timeout time.Duration) bool {
	//nolint:gosec // G304: 路径由 XDG 布局拼出，非不可信输入。
	text, err := os.ReadFile(filepath.Join(StateRoot(env, home), session, "core.lock"))
	if err != nil {
		return false
	}

	socket := strings.TrimSpace(strings.SplitN(string(text), "\n", 2)[0])
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err == nil {
		// 探活连接，关闭失败不影响结论。
		_ = conn.Close()
		return true
	}
	// 探活失败即"不在跑"，是结论不是错误。
	return false
}

// HomeShell 在终端所在机器的 home 目录跑一条短 shell 命令：
// 本机直接 sh -c，远端经 `hc exec --on` ( 其 cwd 即那边的 home )。
// 失败时输出视为空 ( 与原版 homeShell 一致：读 hook 状态的探针不该炸 )。
func (c *CLI) HomeShell(ctx context.Context, session, machine, script string) (string, error) {
	if machine == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}

		//nolint:gosec // G204: 脚本由引擎拼出，非外部输入。
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Dir = home
		out, err := cmd.Output()
		if err != nil && len(out) == 0 {
			return "", nil
		}

		return string(out), nil
	}
	if text, err := c.Run(ctx, session, []string{"exec", "--on", machine, "sh", "-c", script}, 30*time.Second); err == nil {
		return text, nil
	}
	// 失败输出视为空是协议 ( 探针不该炸 )。
	return "", nil
}
