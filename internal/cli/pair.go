package cli

import (
	"context"
	"path/filepath"

	"github.com/jrmarcco/herdr-chat/internal/config"
	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/state"
)

// pair 把 bot 交给它的 owner。
// 生成 6 位码写进 state，主人在飞书里把它发给bot；
// bot 认下之后重启为 owner 的实例，这里的等待以连上新实例收尾。
func (a *App) cmdPair([]string) int {
	pl, cfg, ok := a.ready()
	if !ok {
		return 1
	}
	if cfg.OwnerOpenID != "" {
		a.println("the owner is already set (FEISHU_OWNER_OPEN_ID in " + a.configFile(pl) + ")")
		return 0
	}
	if runs := a.botRuns(context.Background(), pl, cfg); len(runs) == 0 {
		a.errf("herdrchat: the bot is not running: `herdrchat start` first\n")
		return 1
	}

	code, err := core.NewPairingCode()
	if err != nil {
		a.errf("herdrchat: %v\n", err)
		return 1
	}

	store := &state.FileStore{Path: filepath.Join(pl.StateDir, "state.json")}
	st, err := store.Read()
	if err != nil {
		a.errf("herdrchat: %v\n", err)
		return 1
	}

	st.Pairing = &state.Pairing{Code: code, Until: a.now().Add(core.PairWindow).UnixMilli()}
	if err := store.Write(st); err != nil {
		a.errf("herdrchat: %v\n", err)
		return 1
	}

	a.println("In Feishu, search this app's name, open its bot, and send it this code:")
	a.println()
	a.println("    " + code)
	a.println()
	a.printf("It works for %d minutes; a wrong code can be tried %d times.\n",
		int(core.PairWindow.Minutes()), core.PairTries)
	a.printf("Waiting for the owner…\n")

	deadline := a.now().Add(core.PairWindow)
	for {
		a.sleep(PairPollWait)
		env, _ := config.ParseEnv(a.configFile(pl))
		if env[feishuOwnerKey] != "" {
			a.println("✅ paired. The bot starts again with its owner set.")
			return a.waitConnected(pl, cfg.Chat, a.logOffset(pl))
		}
		if !a.now().Before(deadline) {
			a.errf("herdrchat: no answer in %d minutes: the code is gone. `herdrchat pair` shows a new one.\n",
				int(core.PairWindow.Minutes()))
			return 1
		}
	}
}

// configOf 给不需要 config 的命令一个能用的形状 ( stopEverywhere 只用 sessions )。
func configOf(a *App, pl config.Places) config.Config {
	cfg, err := a.loadConfig(pl)
	if err != nil {
		return config.Config{Sessions: []string{config.DefaultSession}}
	}
	return cfg
}
