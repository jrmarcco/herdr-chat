package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/config"
)

// setup 一问一答把一个 profile 立起来：
// hc 在场、密钥即校验、挑 session、start、飞书控制台两步、pair、问装不装服务。
func (a *App) cmdSetup([]string) int {
	if !a.TTY {
		a.errf("herdrchat setup asks questions: run it in a terminal\n")
		return 1
	}

	pl, ok := a.places()
	if !ok {
		return 2
	}
	ctx := context.Background()

	// 1. hc 在场。
	if out, err := a.HC.Run(ctx, "default", []string{"--version"}, 10*time.Second); err != nil {
		a.errf("herdrchat: hc is not here: %v (herdr runs it; https://herdr.example installs it)\n", err)
		return 1
	} else {
		a.printf("hc %s\n", strings.TrimSpace(out))
	}

	// 2. 本构建只说飞书。
	file := a.configFile(pl)
	if err := os.MkdirAll(pl.ConfigDir, 0o700); err != nil {
		a.errf("herdrchat: %v\n", err)
		return 1
	}
	if _, err := os.Stat(file); err != nil {
		_ = os.WriteFile(file, bytes.Clone(configExample), 0o600)
		a.printf("wrote a starter %s\n", file)
	}
	env, _ := config.ParseEnv(file)

	// 3. 密钥，即时校验 ( 密钥无回显；回车保留已存的 )。
	a.println("Feishu: the app's App ID and App Secret (凭证与基础信息 page).")
	appID := a.ask("App ID [" + env["FEISHU_APP_ID"] + "]: ")
	if appID == "" {
		appID = env["FEISHU_APP_ID"]
	}
	secret := a.secret("App Secret [kept]: ")
	if secret == "" {
		secret = env["FEISHU_APP_SECRET"]
	}
	for {
		if appID != "" && secret != "" {
			if err := a.checkFeishuKeys(ctx, appID, secret); err == nil {
				break
			} else {
				a.printf("those keys do not work: %v\n", err)
			}
		} else {
			a.println("both are needed (the Feishu developer console has them).")
		}
		appID = a.ask("App ID: ")
		secret = a.secret("App Secret: ")
	}
	for k, v := range map[string]string{keyAppID: appID, keyAppSecret: secret} {
		if err := config.SetConfigValue(file, k, v); err != nil {
			a.errf("herdrchat: %v\n", err)
			return 1
		}
	}

	// 可选的加密策略两键：回车跳过。
	if err := a.askEventKeys(file, env); err != nil {
		a.errf("herdrchat: %v\n", err)
		return 1
	}

	// 4. sessions：列出本机的，逗号分隔；
	// 回车全 default。
	here := a.sessionsHere()
	if len(here) == 0 {
		a.errf("herdrchat: no hc session on this machine (`hc server start` makes the default one)\n")
		return 1
	}
	a.printf("hc sessions here: %s\n", strings.Join(here, ", "))
	sessions := a.ask("Sessions to mirror, comma-separated [Enter for default]: ")
	if sessions != "" {
		for s := range strings.SplitSeq(sessions, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if slices.Contains(here, s) {
				a.errf("herdrchat: session %s is not one of %s\n", s, strings.Join(here, ", "))
				return 1
			}
		}
		if err := config.SetConfigValue(file, "HERDR_SESSIONS", sessions); err != nil {
			a.errf("herdrchat: %v\n", err)
			return 1
		}
	}

	// 5. 起。
	if code := a.cmdStart(nil); code != 0 {
		return code
	}

	// 6. 飞书控制台两步 + 发布。
	a.println()
	a.println("In the Feishu developer console (https://open.feishu.cn/app):")
	a.println("  5. Events and callbacks (事件与回调): long connection mode (长连接),")
	a.println("     add im.message.receive_v1; callbacks too: add card.action.trigger.")
	a.println("  6. Publish a version (发布版本); if it needs review, have an admin approve it.")
	a.ask("Press Enter when both are done… ")

	// 7. 配对。
	if code := a.cmdPair(nil); code != 0 {
		return code
	}

	// 8. 服务。
	if answer := strings.ToLower(a.ask("Keep it running as a service (starts at login, back after a crash)? [Y/n] ")); answer == "" || answer == "y" || answer == "yes" {
		return a.cmdInstall(nil)
	}
	a.println("then `herdrchat start` runs it in an hc terminal.")
	return 0
}

// askEventKeys 问可选的加密策略两键 ( 控制台 事件与回调 -> 加密策略 页 )：
// 回车跳过，已有值的回车保留。只在给了值时写。
// 空着不写，starter 里那两行空键原样留着。
func (a *App) askEventKeys(file string, env map[string]string) error {
	a.println("Optional: the app's Encrypt Key and Verification Token (事件与回调 -> 加密策略 page).")
	a.println("The long connection works without them; fill them only if that page shows them.")

	ask := func(name, saved string) string {
		hint := "skip"
		if saved != "" {
			hint = "kept"
		}
		v := a.secret(name + " [" + hint + "]: ")
		if v == "" {
			return saved
		}
		return v
	}
	for k, v := range map[string]string{
		keyEncryptKey:  ask("Encrypt Key", env[keyEncryptKey]),
		keyVerifyToken: ask("Verification Token", env[keyVerifyToken]),
	} {
		if v == "" {
			continue
		}
		if err := config.SetConfigValue(file, k, v); err != nil {
			return err
		}
	}
	return nil
}

// secret 问一个不回显的回答 ( stty -echo；不是终端就退而求其次照读 )。
func (a *App) secret(prompt string) string {
	if !a.TTY {
		return a.ask(prompt)
	}
	if !a.stty("-echo") {
		return a.ask(prompt) // 不是能改的终端：照读，别拦着
	}
	defer a.stty("echo")
	answer := a.ask(prompt)
	a.println("")
	return answer
}

// stty 在 stdin 的终端上改回显 ( stty 不在就说明改不了，算失败 )。
func (a *App) stty(arg string) bool {
	if a.Stty != nil {
		return a.Stty(arg)
	}
	tty, ok := a.Stdin.(*os.File)
	if !ok {
		return false
	}

	//nolint:gosec // G204: 固定的 stty 与参数。
	cmd := exec.CommandContext(context.Background(), "stty", arg)
	cmd.Stdin = tty
	return cmd.Run() == nil
}

// feishuKeysWork 用 tenant_access_token/internal 即时校验一对密钥。
func feishuKeysWork(ctx context.Context, appID, appSecret string) error {
	body, _ := json.Marshal(map[string]string{"app_id": appID, "app_secret": appSecret})

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal",
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w (is the network up?)", err)
	}

	//nolint:errcheck // 只读一次的探测。
	defer resp.Body.Close()
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.Code != 0 {
		return fmt.Errorf("feishu says %d: %s", out.Code, out.Msg)
	}
	return nil
}
