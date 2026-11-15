package cli

import (
	"bytes"
	"os"
	"strings"

	"github.com/jrmarcco/herdr-chat"
	"github.com/jrmarcco/herdr-chat/internal/config"
)

// configExample 是起手的 config.env：正本在仓库根的 config.env.example，
// 由根包 embed.go 嵌入（go:embed 够不到包目录外的文件）。
var configExample = herdrchat.ConfigEnvExample

// init 写一个起手的 config.env ( 已有就不动 )。
func (a *App) cmdInit(args []string) int {
	chat := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--chat" && i+1 < len(args):
			chat = args[i+1]
			i++
		case args[i] == "--chat" || strings.HasPrefix(args[i], "--chat="):
			if v, ok := strings.CutPrefix(args[i], "--chat="); ok {
				chat = v
			} else {
				a.errf("herdrchat init: --chat takes a name\n")
				return 2
			}
		default:
			a.errf("herdrchat init: unknown option %q\n", args[i])
			return 2
		}
	}

	if chat != "" && chat != chatFeishu {
		a.errf("herdrchat init: this build talks to Feishu only (--chat %s)\n", chatFeishu)
		return 2
	}

	pl, ok := a.places()
	if !ok {
		return 2
	}

	file := a.configFile(pl)
	if err := os.MkdirAll(pl.ConfigDir, 0o700); err != nil {
		a.errf("herdrchat: %v\n", err)
		return 1
	}
	if _, err := os.Stat(file); err == nil {
		a.printf("already there: %s\n", file)
		return 0
	}
	if err := os.WriteFile(file, bytes.Clone(configExample), 0o600); err != nil {
		a.errf("herdrchat: %v\n", err)
		return 1
	}
	if chat == "feishu" {
		_ = config.SetConfigValue(file, "HERDRCHAT_CHAT", chatFeishu)
	}

	a.printf("wrote %s\n", file)
	a.printf("fill in FEISHU_APP_ID and FEISHU_APP_SECRET (the app's 凭证与基础信息 page),\n")
	a.printf("then `herdrchat start`. `ls ~/.local/state/herdr-core` lists your hc sessions.\n")
	return 0
}
