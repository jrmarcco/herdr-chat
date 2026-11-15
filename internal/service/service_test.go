package service

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 服务文件生成与控制序列的行为测试。
const (
	specID   = "work"
	specProf = "HERDRCHAT_PROFILE"
	specPath = "PATH"
)

func base() Spec {
	return Spec{
		ID: specID, Bin: "/opt/herdrchat/bin/herdrchat", Args: []string{"run"},
		Env: map[string]string{specProf: specID, specPath: "/a:/b & <c>"},
		Out: "/state/logs/service.out.log",
	}
}

func mustMatch(t *testing.T, text, pattern string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(text) {
		t.Fatalf("no match for %s in:\n%s", pattern, text)
	}
}

func TestLaunchdAgentRunsByFullPathComesBackAndEscapes(t *testing.T) {
	plist := LaunchdPlist("app.herdrchat.work", base())
	mustMatch(t, plist, `<key>Label</key>\s*<string>app\.herdrchat\.work</string>`)
	mustMatch(t, plist, `<string>/opt/herdrchat/bin/herdrchat</string>\s*<string>run</string>`)
	mustMatch(t, plist, `<key>RunAtLoad</key>\s*<true/>`)
	mustMatch(t, plist, `<key>KeepAlive</key>\s*<true/>`)
	mustMatch(t, plist, `<key>ThrottleInterval</key>\s*<integer>10</integer>`)
	mustMatch(t, plist, `<string>/a:/b &amp; &lt;c&gt;</string>`)
	mustMatch(t, plist, `<key>StandardOutPath</key>\s*<string>/state/logs/service\.out\.log</string>`)
	if strings.Contains(plist, "CLAUDE") {
		t.Fatal("no agent's variables come along")
	}
}

func TestSystemdUnitRestartsAlwaysAndQuotes(t *testing.T) {
	unit := SystemdUnit(base())
	mustMatch(t, unit, `(?m)^ExecStart="/opt/herdrchat/bin/herdrchat" "run"$`)
	mustMatch(t, unit, `(?m)^Environment="PATH=/a:/b & <c>"$`)
	mustMatch(t, unit, `(?m)^Restart=always$`)
	mustMatch(t, unit, `(?m)^RestartSec=5$`)
	mustMatch(t, unit, `(?m)^WantedBy=default\.target$`)
	s := base()
	s.Bin, s.Env = `/we "ird/100%/bin`, map[string]string{"K": `a"b\c`}
	unit = SystemdUnit(s)
	mustMatch(t, unit, `(?m)^ExecStart="/we \\"ird/100%%/bin" "run"$`)
	mustMatch(t, unit, `(?m)^Environment="K=a\\"b\\\\c"$`)
}

func TestOnlyWhatTheServiceNeedsComesAlong(t *testing.T) {
	env := EnvFor(map[string]string{
		specPath: "/bin", "HERDRCHAT_HC": "/opt/hc", "XDG_STATE_HOME": "/s", specProf: "other",
		"CLAUDECODE": "1", "CLAUDE_CODE_ENTRYPOINT": "cli", "HOME": "/h", "XDG_CONFIG_HOME": "",
	}, "work")
	want := map[string]string{specPath: "/bin", "HERDRCHAT_HC": "/opt/hc", "XDG_STATE_HOME": "/s", specProf: "work"}
	if len(env) != len(want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
	for k, v := range want {
		if env[k] != v {
			t.Fatalf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
}

// a service names its file per profile and tells when the binary it runs is gone.
func TestServiceNamesItsFilePerProfileAndTellsWhenItsBinaryIsGone(t *testing.T) {
	home := t.TempDir()
	spec := base()
	spec.Bin = filepath.Join(home, "gone", "herdrchat")
	svc := Of("darwin", home, nil, spec)
	if svc.Kind != KindLaunchd {
		t.Fatalf("kind = %s", svc.Kind)
	}
	if want := filepath.Join(home, "Library", "LaunchAgents", "app.herdrchat.work.plist"); svc.File != want {
		t.Fatalf("file = %s, want %s", svc.File, want)
	}
	if svc.Installed() {
		t.Fatal("installed before it is written")
	}
	if err := svc.Write(); err != nil {
		t.Fatal(err)
	}
	if !svc.Installed() {
		t.Fatal("not installed after it is written")
	}
	if raw, _ := os.ReadFile(svc.File); string(raw) != svc.Content() {
		t.Fatal("the file is not the content")
	}
	if svc.BinGone() != spec.Bin {
		t.Fatalf("BinGone = %q, want the missing path", svc.BinGone())
	}
	if err := svc.Remove(); err != nil {
		t.Fatal(err)
	}
	if svc.Installed() {
		t.Fatal("installed after it is removed")
	}
	if err := svc.Remove(); err != nil {
		t.Fatalf("removing twice: %v", err)
	}

	linux := Of("linux", home, nil, spec)
	if want := filepath.Join(home, ".config", "systemd", "user", "herdrchat-work.service"); linux.File != want {
		t.Fatalf("file = %s, want %s", linux.File, want)
	}
	moved := Of("linux", home, map[string]string{"XDG_CONFIG_HOME": "/xdg"}, spec)
	if moved.File != "/xdg/systemd/user/herdrchat-work.service" {
		t.Fatalf("file = %s", moved.File)
	}
	if Of("windows", home, nil, spec) != nil {
		t.Fatal("no service on a platform without one")
	}
	exe, _ := os.Executable()
	spec.Bin = exe
	if Of("linux", home, nil, spec).BinGone() != "" {
		t.Fatal("a binary that is there is not gone")
	}
}

// 调用序列：launchd 先卸再装；systemd 重读、enable、restart。
func TestTheCallsEachSystemTakes(t *testing.T) {
	var calls []string
	reply := map[string]string{}
	run := func(_ context.Context, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		return reply[line], nil
	}
	ctx := context.Background()

	mac := Of("darwin", "/h", nil, base())
	mac.Run, mac.UID = run, 501
	if err := mac.Start(ctx); err != nil {
		t.Fatal(err)
	}
	reply["launchctl print gui/501/app.herdrchat.work"] = "state = running\n\tpid = 4242\n"
	if pid, running := mac.Running(ctx); !running || pid != 4242 {
		t.Fatalf("running = %v pid = %d", running, pid)
	}
	if err := mac.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"launchctl bootout gui/501/app.herdrchat.work",
		"launchctl bootstrap gui/501 /h/Library/LaunchAgents/app.herdrchat.work.plist",
		"launchctl print gui/501/app.herdrchat.work",
		"launchctl print gui/501/app.herdrchat.work",
		"launchctl bootout gui/501/app.herdrchat.work",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("launchd calls:\n%s", strings.Join(calls, "\n"))
	}

	calls = nil
	linux := Of("linux", "/h", nil, base())
	linux.Run = run
	if err := linux.Start(ctx); err != nil {
		t.Fatal(err)
	}
	reply["systemctl --user show herdrchat-work.service --property=ActiveState,MainPID"] = "ActiveState=active\nMainPID=77\n"
	if pid, running := linux.Running(ctx); !running || pid != 77 {
		t.Fatalf("running = %v pid = %d", running, pid)
	}
	if err := linux.Disable(ctx); err != nil {
		t.Fatal(err)
	}
	linux.Reload(ctx)
	want = []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable herdrchat-work.service",
		"systemctl --user restart herdrchat-work.service",
		"systemctl --user show herdrchat-work.service --property=ActiveState,MainPID",
		"systemctl --user disable --now herdrchat-work.service",
		"systemctl --user daemon-reload",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("systemd calls:\n%s", strings.Join(calls, "\n"))
	}
}
