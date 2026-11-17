package cli

import (
	"context"

	"github.com/jrmarcco/herdr-chat/internal/config"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
)

// start 先停干净 ( 双 bot 双答 )，再按安装形态起。
// 有服务走服务，没有就在第一个 session 的一个 hc 终端里放个循环；
// 然后等日志说连上了。
func (a *App) cmdStart([]string) int {
	pl, cfg, ok := a.ready()
	if !ok {
		return 1
	}
	if !a.checkSessions(cfg) {
		return 1
	}

	ctx := context.Background()
	if stopped := a.stopEverywhere(ctx, pl, cfg); len(stopped) > 0 {
		a.printf("stopped %s first\n", joinWords(stopped))
	}

	offset := a.logOffset(pl)
	if svc := a.serviceOf(pl); svc != nil && svc.Installed() {
		if err := svc.Start(ctx); err != nil {
			a.errf("herdrchat: the service did not start: %v\n", err)
			return 1
		}
		a.printf("starting the service (%s)…\n", svc.File)
		return a.waitConnected(pl, cfg.Chat, offset)
	}

	if _, err := a.HC.RunTerminal(ctx, cfg.Sessions[0], hccli.RunOpts{
		Name: pl.Terminal, Cwd: a.Home, Argv: []string{"sh", "-c", a.loopScript(pl)},
	}); err != nil {
		a.errf("herdrchat: could not start the hc terminal: %v\n", err)
		return 1
	}

	a.printf("starting in hc terminal %q (session %s)…\n", pl.Terminal, cfg.Sessions[0])
	return a.waitConnected(pl, cfg.Chat, offset)
}

// stop 从所有形态停下。
func (a *App) cmdStop([]string) int {
	pl, ok := a.places()
	if !ok {
		return 2
	}
	cfg, err := a.loadConfig(pl)
	if err != nil {
		// 键不全也照样停。
		cfg = config.Config{Sessions: []string{config.DefaultSession}}
	}
	stopped := a.stopEverywhere(context.Background(), pl, cfg)
	if len(stopped) == 0 {
		a.println("it was not running")
		return 0
	}
	a.printf("stopped %s.\n", joinWords(stopped))
	return 0
}

func joinWords(words []string) string {
	out := ""
	for i, w := range words {
		if i > 0 {
			out += " and "
		}
		out += w
	}
	return out
}
