package cli

import (
	"context"
	"strings"

	"github.com/jrmarcco/herdr-chat/internal/hooks"
)

// hooks 看 / 装 / 卸 PermissionRequest hook。
// 非 TTY 且没有 --yes 时装与卸都拒绝；装之前打印要动的文件。
func (a *App) cmdHooks(args []string) int {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}

	yes := false
	var agents []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--yes", "-y":
			yes = true
		case "claude", "codex":
			agents = append(agents, args[i])
		default:
			a.errf("herdrchat hooks: unknown option %q\n", args[i])
			return 2
		}
	}

	p := hooks.PlacesOf(a.Env, a.Home)
	switch sub {
	case "", "status", "show":
		for _, st := range hooks.StatusOf(p) {
			switch {
			case st.In:
				a.printf("%s: herdrchat's hook is in (%s)\n", st.Agent, hookFileOf(p, st.Agent))
			case st.There:
				a.printf("%s: set up here, the hook is not in (herdrchat hooks install %s)\n", st.Agent, st.Agent)
			default:
				a.printf("%s: not set up on this machine\n", st.Agent)
			}
		}
		return 0
	case "install":
		if len(agents) == 0 {
			agents = hooks.Agents
		}
		var files []string
		for _, agent := range agents {
			for _, st := range hooks.StatusOf(p) {
				if st.Agent == agent && st.There {
					files = append(files, hookFileOf(p, agent))
				}
			}
		}
		if len(files) == 0 {
			a.println("neither Claude Code nor Codex is set up on this machine; nothing to do")
			return 0
		}
		a.printf("this puts herdrchat's hook in:\n")
		for _, f := range files {
			a.printf("  %s\n", f)
		}
		if !a.confirm(yes, "Put it in? [y/N] ") {
			return 0
		}
		if err := hooks.InstallHooks(agents, p); err != nil {
			a.errf("herdrchat hooks: %v\n", err)
			return 1
		}
		a.printf("in. The hook itself lives at %s (never edited; it is herdrchat's own).\n", p.Hook)
		return 0
	case "uninstall", "remove":
		if !a.confirm(yes, "Take it out? [y/N] ") {
			return 0
		}
		if err := hooks.UninstallHooks(p); err != nil {
			a.errf("herdrchat hooks: %v\n", err)
			return 1
		}
		a.println("out")
		return 0
	default:
		a.errf("herdrchat hooks: unknown subcommand %q (show, install, uninstall)\n", sub)
		return 2
	}
}

// confirm：--yes 直接做；否则要一个终端与一个 y。
func (a *App) confirm(yes bool, prompt string) bool {
	if yes {
		return true
	}
	if !a.TTY {
		a.errf("herdrchat: not a terminal: pass --yes to do it without asking\n")
		return false
	}
	answer := strings.ToLower(a.ask(prompt))
	if answer != "y" && answer != "yes" {
		a.println("nothing changed")
		return false
	}
	return true
}

func hookFileOf(p hooks.Places, agent string) string {
	if agent == "codex" {
		return p.Codex
	}
	return p.Claude
}

// checkFeishuKeys 即时校验一对飞书密钥 ( setup 用；App.CheckKeys 可注入 )。
func (a *App) checkFeishuKeys(ctx context.Context, appID, appSecret string) error {
	if a.CheckKeys != nil {
		return a.CheckKeys(ctx, appID, appSecret)
	}
	return feishuKeysWork(ctx, appID, appSecret)
}
