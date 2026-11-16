package config

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var idTagRe = regexp.MustCompile(`^work-[0-9a-f]{6}$`)

const (
	spaceAll     = "all"
	partFile     = "file"
	partFolded   = "folded"
	bothParts    = "file,folded"
	keyBotToken  = "TELEGRAM_BOT_TOKEN" //nolint:gosec // G101: 配置键名而非凭证值。
	envProfile   = "HERDRCHAT_PROFILE"
	envHerdrHome = "HERDRCHAT_HOME"
	profileWork  = "work"
)

// 表驱动用例：
// spaces、开关、places、parts 与 setConfigValue。配对测试随 pairing 一起移植。
func TestSpacesOf(t *testing.T) {
	cases := []struct {
		value    string
		set      bool
		fallback string
		want     Spaces
	}{
		{"", false, spaceAll, Spaces{All: true}},
		{"", false, "", Spaces{}},
		{spaceAll, true, "", Spaces{All: true}},
		{"", true, spaceAll, Spaces{}},
		{"none", true, spaceAll, Spaces{}},
		{" alpha, beta ", true, spaceAll, Spaces{List: []string{"alpha", "beta"}}},
	}
	for _, c := range cases {
		if got := SpacesOf(c.value, c.set, c.fallback); got.All != c.want.All || strings.Join(got.List, ",") != strings.Join(c.want.List, ",") {
			t.Errorf("SpacesOf(%q, %v, %q) = %+v, want %+v", c.value, c.set, c.fallback, got, c.want)
		}
	}
}

func TestOnOff(t *testing.T) {
	cases := []struct {
		value    string
		fallback bool
		want     bool
	}{
		{"", true, true},
		{"", false, false},
		{"off", true, false},
		{" ON ", false, true},
		{"no", true, false},
		{"maybe", true, true},
	}
	for _, c := range cases {
		if got := OnOff(c.value, c.fallback); got != c.want {
			t.Errorf("OnOff(%q, %v) = %v, want %v", c.value, c.fallback, got, c.want)
		}
	}
}

func TestPlaces(t *testing.T) {
	// 每个 profile 在 XDG 下各一对目录，或自选的一个目录。
	base, err := PlacesOf(nil, "/h")
	if err != nil {
		t.Fatal(err)
	}
	want := Places{
		Profile:   DefaultSession,
		ID:        DefaultSession,
		ConfigDir: "/h/.config/herdrchat/default",
		StateDir:  "/h/.local/state/herdrchat/default",
		Terminal:  "herdrchat",
	}
	if base != want {
		t.Errorf("base = %+v, want %+v", base, want)
	}
	work, err := PlacesOf(map[string]string{envProfile: profileWork}, "/h")
	if err != nil {
		t.Fatal(err)
	}
	if work.ID != "work" || work.Terminal != "herdrchat-work" {
		t.Errorf("work = %+v", work)
	}
	xdg, err := PlacesOf(map[string]string{"XDG_CONFIG_HOME": "/c", "XDG_STATE_HOME": "/s"}, "/h")
	if err != nil {
		t.Fatal(err)
	}
	if xdg.ConfigDir != "/c/herdrchat/default" || xdg.StateDir != "/s/herdrchat/default" {
		t.Errorf("xdg = %+v", xdg)
	}

	// XDG 移动过的目录同样带短标签。
	if !strings.HasPrefix(xdg.Terminal, "herdrchat-default-") || len(xdg.Terminal) != len("herdrchat-default-")+6 {
		t.Errorf("xdg.Terminal = %q, want herdrchat-default-<6 hex>", xdg.Terminal)
	}
	moved, err := PlacesOf(map[string]string{envHerdrHome: "/my/hc", envProfile: profileWork}, "/h")
	if err != nil {
		t.Fatal(err)
	}
	if moved.ConfigDir != "/my/hc" || moved.StateDir != "/my/hc/state" {
		t.Errorf("moved = %+v", moved)
	}

	// 挪动过的安装绝不与惯常位置共享终端或服务名。
	if !idTagRe.MatchString(moved.ID) {
		t.Errorf("moved.ID = %q, want work-<6 hex>", moved.ID)
	}
	if moved.Terminal != "herdrchat-"+moved.ID {
		t.Errorf("moved.Terminal = %q", moved.Terminal)
	}
	other, _ := PlacesOf(map[string]string{envHerdrHome: "/other"}, "/h")
	if other.ID == moved.ID {
		t.Errorf("different HERDRCHAT_HOME must give different ids")
	}
	empty, err := PlacesOf(map[string]string{envHerdrHome: "", XDG_KEY: ""}, "/h")
	if err != nil {
		t.Fatal(err)
	}
	if empty.ConfigDir != "/h/.config/herdrchat/default" {
		t.Errorf("empty-value env keys mean unset: %+v", empty)
	}
	if _, err := PlacesOf(map[string]string{envProfile: "../x"}, "/h"); err == nil || !strings.Contains(err.Error(), "letters, digits") {
		t.Errorf("bad profile: err = %v, want letters, digits", err)
	}
}

const XDG_KEY = "XDG_CONFIG_HOME"

func TestPartsOf(t *testing.T) {
	known := []string{partFile, partFolded}
	cases := []struct {
		value    string
		set      bool
		fallback string
		want     map[string]bool
	}{
		{"", false, bothParts, map[string]bool{partFile: true, partFolded: true}},
		{partFolded, true, bothParts, map[string]bool{partFile: false, partFolded: true}},
		{"", true, bothParts, map[string]bool{partFile: false, partFolded: false}},
		{"none", true, bothParts, map[string]bool{partFile: false, partFolded: false}},
	}
	for _, c := range cases {
		got, err := PartsOf(c.value, c.set, c.fallback, known, "HERDR_HISTORY")
		if err != nil {
			t.Fatalf("PartsOf(%q): %v", c.value, err)
		}
		if !sameParts(got, c.want) {
			t.Errorf("PartsOf(%q, %v, %q) = %v, want %v", c.value, c.set, c.fallback, got, c.want)
		}
	}
	if _, err := PartsOf("files", true, bothParts, known, "HERDR_HISTORY"); err == nil || !strings.Contains(err.Error(), "HERDR_HISTORY: files is not one of file, folded") {
		t.Errorf("unknown part: err = %v", err)
	}
}

func TestSetConfigValue(t *testing.T) {
	dir := t.TempDir
	file := filepath.Join(dir(), "config.env")
	if err := os.WriteFile(file, []byte("# a comment\n"+keyBotToken+"=\nHERDR_SESSIONS=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetConfigValue(file, keyBotToken, "1:abc"); err != nil {
		t.Fatal(err)
	}
	if err := SetConfigValue(file, "TELEGRAM_OWNER_ID", "42"); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // G304: 测试临时目录内的文件。
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	want := "# a comment\nTELEGRAM_BOT_TOKEN=1:abc\nHERDR_SESSIONS=\nTELEGRAM_OWNER_ID=42\n"
	if string(text) != want {
		t.Errorf("file = %q, want %q", text, want)
	}

	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 600", info.Mode().Perm())
	}
}

func TestParseEnvQuotes(t *testing.T) {
	dir := t.TempDir
	file := filepath.Join(dir(), "config.env")
	if err := os.WriteFile(file, []byte("A=plain\nB = spaced \nC=\"quoted\"\nD='single'\n# comment\nno good line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := ParseEnv(file)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"A": "plain", "B": "spaced", "C": "quoted", "D": "single"} {
		if env[k] != want {
			t.Errorf("env[%q] = %q, want %q", k, env[k], want)
		}
	}
	if _, ok := env["no"]; ok {
		t.Errorf("malformed line must be skipped")
	}
	missing, err := ParseEnv(filepath.Join(dir(), "absent.env"))
	if err != nil || missing == nil || len(missing) != 0 {
		t.Errorf("missing file: %v %v", missing, err)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir
	file := filepath.Join(dir(), "config.env")
	if err := os.WriteFile(file, []byte(keyBotToken+"=1:abc\nHERDR_SESSIONS=a, b\nHERDR_HISTORY=folded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file, map[string]string{"HERDRCHAT_CHAT": "telegram"}, []string{keyBotToken})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Chat != "telegram" || !sameStrings(cfg.Sessions, []string{"a", "b"}) || !cfg.SessionsSet {
		t.Errorf("cfg = %+v", cfg)
	}
	if !cfg.History["folded"] || cfg.History["file"] {
		t.Errorf("history = %v", cfg.History)
	}

	// 空的 HERDR_SESSIONS 即 hc 自己的 session。
	if writeErr := os.WriteFile(file, []byte(keyBotToken+"=x\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	cfg, err = Load(file, nil, []string{keyBotToken})
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(cfg.Sessions, []string{DefaultSession}) || cfg.SessionsSet {
		t.Errorf("sessions = %v set=%v", cfg.Sessions, cfg.SessionsSet)
	}

	// 缺失的必填键要说出缺什么、在哪补。
	if writeErr := os.WriteFile(file, []byte("\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	_, err = Load(file, nil, []string{keyBotToken, "TELEGRAM_OWNER_ID"})
	if err == nil || !strings.Contains(err.Error(), keyBotToken+", TELEGRAM_OWNER_ID are empty") {
		t.Errorf("missing keys: err = %v", err)
	}
}

func sameParts(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// 加密策略的两个可选键：文件与环境各来一个，环境优先；不配置即空串。
func TestLoadEncryptPolicy(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.env")
	if err := os.WriteFile(file, []byte("FEISHU_ENCRYPT_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file, map[string]string{"FEISHU_VERIFICATION_TOKEN": "from-env", "FEISHU_ENCRYPT_KEY": "env-wins"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EncryptKey != "env-wins" || cfg.VerificationToken != "from-env" {
		t.Errorf("encrypt policy keys: %+v", cfg)
	}

	cfg, err = Load(file, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EncryptKey != "from-file" || cfg.VerificationToken != "" {
		t.Errorf("encrypt policy defaults: %+v", cfg)
	}
}

func sameStrings(a, b []string) bool { return slices.Equal(a, b) }

// HERDRCHAT_SPACE：/new 的默认 space 名；未设是 dev。
func TestLoadSpace(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.env")
	if err := os.WriteFile(file, []byte(keyBotToken+"=1:abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file, nil, []string{keyBotToken})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Space != "dev" {
		t.Fatalf("default space = %q, want dev", cfg.Space)
	}
	cfg, err = Load(file, map[string]string{"HERDRCHAT_SPACE": profileWork}, []string{keyBotToken})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Space != profileWork {
		t.Fatalf("space = %q, want %s", cfg.Space, profileWork)
	}
}
