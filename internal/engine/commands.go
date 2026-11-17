package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
)

// 斜杠命令目录：枚举这台
// 机器上该 agent 真实存在的命令，用户与项目的自定义命令、
// 技能让 "/" 在聊天里可发现，而不是背下来。
// 本机直接读文件；远端机器的目录 setup 通路补上。
// 各版本内置命令表也按 claude --version 校准。

// Command 是目录里的一条命令。
type Command struct {
	Name     string
	Desc     string
	ArgsHint string
	Source   string // user | project | skill
}

// catalogEvery 是命令目录的重读间隔。
const catalogEvery = 10 * time.Minute

// CommandCatalog 是命令目录的缓存。
type CommandCatalog struct {
	mu    sync.Mutex
	cache map[string]catalogEntry
	home  string
}

type catalogEntry struct {
	list []Command
	at   time.Time
}

// NewCommandCatalog 建一个目录缓存 ( home 是本机 home，测试可注入 )。
func NewCommandCatalog(home string) *CommandCatalog {
	return &CommandCatalog{cache: map[string]catalogEntry{}, home: home}
}

// CommandsOf 给出该 agent 在这个目录下的斜杠命令 ( 本机；远端机器暂无 )。
func (c *CommandCatalog) CommandsOf(agent, cwd, machine string) []Command {
	if machine != "" {
		return nil // 远端机器的命令目录走 hc exec，随 M7 的 setup 通路补上
	}
	key := agent + ":" + cwd
	c.mu.Lock()
	if hit, ok := c.cache[key]; ok && time.Since(hit.at) < catalogEvery {
		c.mu.Unlock()
		return hit.list
	}
	c.mu.Unlock()
	var list []Command
	switch agent {
	case "claude-code":
		list = append(list, commandFiles(c.home+"/.claude/commands", "user")...)
		list = append(list, commandFiles(cwd+"/.claude/commands", "project")...)
		list = append(list, skillsOf(c.home+"/.claude/skills")...)
		list = append(list, skillsOf(cwd+"/.claude/skills")...)
	case "codex":
		list = append(list, commandFiles(c.home+"/.codex/prompts", "user")...)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	c.mu.Lock()
	c.cache[key] = catalogEntry{list: list, at: time.Now()}
	c.mu.Unlock()
	return list
}

// readDir/readFile 带着各自唯一的 gosec 说明：目录与文件名按 agent 的固定
// 布局拼出，非外部输入。
func readDir(dir string) []os.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return entries
}

func readFile(file string) []byte {
	text, err := os.ReadFile(file) //nolint:gosec // G304: 路径按 agent 的固定布局拼出
	if err != nil {
		return nil
	}
	return text
}

// commandFiles 读一个命令目录：每个 .md 文件是一条命令，front matter 里的
// description 与 argument-hint 一起带来。
func commandFiles(dir, source string) []Command {
	entries := readDir(dir)
	if entries == nil {
		return nil
	}
	var out []Command
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		cmd := Command{Name: strings.TrimSuffix(entry.Name(), ".md"), Source: source}
		if text := readFile(filepath.Join(dir, entry.Name())); text != nil {
			cmd.Desc, cmd.ArgsHint = frontMatter(string(text))
		}
		out = append(out, cmd)
	}
	return out
}

// skillsOf 读一个技能目录：SKILL.md 的 description。
func skillsOf(dir string) []Command {
	entries := readDir(dir)
	if entries == nil {
		return nil
	}
	var out []Command
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		text := readFile(filepath.Join(dir, entry.Name(), "SKILL.md"))
		if text == nil {
			continue
		}
		desc, _ := frontMatter(string(text))
		out = append(out, Command{Name: entry.Name(), Desc: desc, Source: "skill"})
	}
	return out
}

var fmField = regexp.MustCompile(`(?m)^([\w-]+):\s*(.*)$`)

// frontMatter 取 YAML 头里的 description 与 argument-hint ( 极简解析，
// 不引 YAML 库 )。
func frontMatter(text string) (desc, args string) {
	if !strings.HasPrefix(text, "---") {
		return "", ""
	}
	rest := text[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", ""
	}
	for _, m := range fmField.FindAllStringSubmatch(rest[:end], -1) {
		switch m[1] {
		case "description":
			desc = strings.TrimSpace(m[2])
		case "argument-hint":
			args = strings.TrimSpace(m[2])
		}
	}
	return desc, args
}

// commandsCard 把目录排成一张卡。
func commandsCard(list []Command) core.Look {
	var lines []string
	for _, c := range list {
		line := "/" + c.Name
		if c.ArgsHint != "" {
			line += " " + c.ArgsHint
		}
		if c.Desc != "" {
			line += " — " + c.Desc
		}
		lines = append(lines, line)
	}
	body := strings.Join(lines, "\n")
	if body == "" {
		body = "No commands of its own on this machine; what it has built in still works."
	}
	return core.Look{Title: "⌘ Commands", Tone: core.ToneGood, Markdown: body}
}

// onCommands 是房间里 /commands 的入口：发一张目录卡。
func (e *Engine) onCommands(session string, record hccli.Terminal) error {
	agent := ""
	if reader := e.agentOf(record); reader != nil {
		agent = reader.App()
	}
	cwd := foregroundCwd(record)
	if cwd == "" {
		cwd = e.HomeDir
	}
	e.mu.Lock()
	catalog := e.commands
	e.mu.Unlock()
	if catalog == nil {
		catalog = NewCommandCatalog(e.HomeDir)
		e.mu.Lock()
		e.commands = catalog
		e.mu.Unlock()
	}
	look := commandsCard(catalog.CommandsOf(agent, cwd, hccli.MachineOf(record.Address())))
	room, err := e.ensureRoom(session, record)
	if err != nil || room == "" {
		return err
	}
	_, err = e.Chat.PostCard(e.ctx, room, look)
	return err
}
