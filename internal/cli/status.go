package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/config"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/hooks"
)

// status 查看 profile 在哪、每个 session 的 core 活没活、bot 以什么形态在跑、
// 必填键齐不齐 ( 不打秘密 )、/auth 的 token、今天的 ERROR/WARN。
func (a *App) cmdStatus([]string) int {
	pl, ok := a.places()
	if !ok {
		return 2
	}
	a.printf("profile   %s\n", pl.Profile)
	a.printf("settings  %s\n", a.configFile(pl))
	a.printf("state     %s\n", pl.StateDir)

	file := a.configFile(pl)
	env, _ := config.ParseEnv(file)
	for k, v := range a.Env {
		env[k] = v
	}
	a.printf("chat      feishu:")
	for _, key := range []string{keyAppID, keyAppSecret} {
		if env[key] != "" {
			a.printf(" %s set", key)
		} else {
			a.printf(" %s MISSING", key)
		}
	}
	if env[feishuOwnerKey] != "" {
		a.printf(" owner set\n")
	} else {
		a.printf(" owner missing (herdrchat pair)\n")
	}

	here := map[string]bool{}
	for _, s := range a.sessionsHere() {
		here[s] = true
	}
	cfg, cfgErr := a.loadConfig(pl)
	sessions := cfg.Sessions
	if cfgErr != nil {
		sessions = []string{config.DefaultSession}
	}

	ctx := context.Background()
	a.printf("sessions ")
	for i, s := range sessions {
		if i > 0 {
			a.printf("         ")
		}
		switch {
		case !here[s]:
			a.printf("%s: not on this machine\n", s)
		case hccli.CoreUp(ctx, a.Env, a.Home, s, time.Second):
			a.printf("%s: core up\n", s)
		default:
			a.printf("%s: core down (hc server start %s)\n", s, s)
		}
	}

	runs := a.botRuns(ctx, pl, cfg)
	switch len(runs) {
	case 0:
		a.printf("runs      not running (herdrchat start)\n")
	case 1:
		a.printf("runs      as %s\n", runs[0].Where)
	default:
		a.printf("runs      ⚠ twice: %s\n", joinRuns(runs))
	}
	if svc := a.serviceOf(pl); svc != nil && svc.Installed() {
		if gone := svc.BinGone(); gone != "" {
			a.printf("runs      ⚠ the service's binary is gone (%s): `herdrchat install` again\n", gone)
		}
	}

	if token, err := os.ReadFile(filepath.Join(pl.StateDir, "user-token.json")); err == nil {
		var t struct {
			RefreshUntil int64 `json:"refreshUntil"`
		}
		if json.Unmarshal(token, &t) == nil && t.RefreshUntil > 0 {
			a.printf("/auth     allowed until %s\n", time.UnixMilli(t.RefreshUntil).Format("2006-01-02 15:04"))
		} else {
			a.printf("/auth     set up\n")
		}
	} else {
		a.printf("/auth     not set up (send /auth to the bot)\n")
	}

	status := hooks.StatusOf(hooks.PlacesOf(a.Env, a.Home))
	words := make([]string, 0, len(status))
	for _, st := range status {
		switch {
		case st.In:
			words = append(words, st.Agent+": in")
		case st.There:
			words = append(words, st.Agent+": not in (herdrchat hooks install)")
		default:
			words = append(words, st.Agent+": not set up here")
		}
	}
	a.printf("hook      %s\n", strings.Join(words, "; "))

	errCount, warnCount := a.logCount(pl, a.logDay())
	if errCount < 0 {
		a.printf("log       no log for today yet\n")
	} else {
		a.printf("log       today: %d ERROR, %d WARN (herdrchat logs --errors)\n", errCount, warnCount)
	}
	if cfgErr != nil {
		a.errf("settings   %v\n", cfgErr)
	}
	return 0
}

// feishuOwnerKey 与 chats/feishu.OwnerKey 一致 ( status 不起 chat 实例 )。
const feishuOwnerKey = "FEISHU_OWNER_OPEN_ID"

func joinRuns(runs []oneRun) string {
	words := make([]string, 0, len(runs))
	for _, r := range runs {
		words = append(words, r.Where)
	}
	return strings.Join(words, " and ")
}

// logCount 数某天日志里的 ERROR 与 WARN。
func (a *App) logCount(pl config.Places, day string) (int, int) {
	raw, err := os.ReadFile(a.logFile(pl, day))
	if err != nil {
		return -1, 0
	}
	errCount, warnCount := 0, 0
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[1] {
		case levelError:
			errCount++
		case "WARN":
			warnCount++
		}
	}
	return errCount, warnCount
}
