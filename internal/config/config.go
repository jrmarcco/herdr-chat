// Package config 映射 herdrchat 的设置：
// 一个 profile 放在哪里 ( PlacesOf )、
// config.env 的格式 ( ParseEnv、SetConfigValue )，
// 以及一次运行需要的值 ( Load )。
//
// 从环境算出一个 profile 的目录 ( 移植自原版 placesOf ):
//
//	~/.config/herdrchat/<profile>/config.env   设置 ( XDG_CONFIG_HOME 可移动 )
//	~/.local/state/herdrchat/<profile>/        bot 写下的东西 ( XDG_STATE_HOME 可移动 )
//
// HERDRCHAT_HOME 则把一个 profile 放进你指定的一个目录：
// config.env 与 state/ 都在其中。
// 当目录不是惯常位置时，profile id 会带上一个短标签 —— 在别的目录里试验的安装，
// 永远不会停掉、替换或删除正式实例的服务与 hc 终端。
package config

import (
	"crypto/sha1" //nolint:gosec // G505: 目录标签哈希，非密码学用途。
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Places 是一个 profile 的落点。ID 是系统看到它的名字 ( hc 终端、服务 )；
// Terminal 是它使用的 hc 终端名。
type Places struct {
	Profile   string
	ID        string
	ConfigDir string
	StateDir  string
	Terminal  string
}

var profileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// PlacesOf 从 env ( 可为 nil 表示全未设置 ) 与 home 目录算出一个 profile 的目录。
// 纯函数：测试不会碰你的 home。
func PlacesOf(env map[string]string, home string) (Places, error) {
	if env == nil {
		env = map[string]string{}
	}
	profile := env["HERDRCHAT_PROFILE"]
	if profile == "" {
		profile = "default"
	}
	if !profileRe.MatchString(profile) {
		return Places{}, fmt.Errorf("profile %q: use letters, digits, - and _ only", profile)
	}

	// 空值视为未设置，与 shell 的语义一致。
	get := func(key string) string { return env[key] }
	var configDir, stateDir string
	if herdrHome := get("HERDRCHAT_HOME"); herdrHome != "" {
		configDir = herdrHome
		stateDir = filepath.Join(herdrHome, "state")
	} else {
		configRoot := filepath.Join(home, ".config")
		if v := get("XDG_CONFIG_HOME"); v != "" {
			configRoot = v
		}
		stateRoot := filepath.Join(home, ".local", "state")
		if v := get("XDG_STATE_HOME"); v != "" {
			stateRoot = v
		}
		configDir = filepath.Join(configRoot, "herdrchat", profile)
		stateDir = filepath.Join(stateRoot, "herdrchat", profile)
	}

	// 挪动过的安装绝不与惯常位置共享终端或服务名。
	usual := filepath.Join(home, ".config", "herdrchat", profile)
	usualState := filepath.Join(home, ".local", "state", "herdrchat", profile)
	moved := filepath.Clean(configDir) != filepath.Clean(usual) || filepath.Clean(stateDir) != filepath.Clean(usualState)
	id := profile
	if moved {
		//nolint:gosec // G401: 目录标签哈希，非安全用途。
		sum := sha1.Sum([]byte(filepath.Clean(configDir) + "\x00" + filepath.Clean(stateDir)))
		id = profile + "-" + hex.EncodeToString(sum[:])[:6]
	}
	terminal := "herdrchat-" + id
	if id == "default" {
		terminal = "herdrchat"
	}
	return Places{Profile: profile, ID: id, ConfigDir: configDir, StateDir: stateDir, Terminal: terminal}, nil
}

// MakeDirs 创建 config 与 state 目录，私有 ( mode 700 )。
// 在 bot 启动时调用，而非包加载时 ( 测试不得碰你的 home )。
func MakeDirs(p Places) error {
	for _, dir := range []string{p.ConfigDir, p.StateDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

var envLineRe = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*$`)

// stripQuotes 剥掉一层两侧成对的引号
// ( RE2 不支持反向引用，shell 风格的 `^(['"])(.*)\1$` 只好手工实现 )。
func stripQuotes(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// ParseEnv 把一个 config.env 读进 map。文件不存在则返回空 map。
func ParseEnv(file string) (map[string]string, error) {
	env := map[string]string{}
	text, err := os.ReadFile(file) //nolint:gosec // G304: 路径来自用户自己的 config.env 布局，非不可信输入
	if err != nil {
		if os.IsNotExist(err) {
			return env, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(text), "\n") {
		m := envLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		env[m[1]] = stripQuotes(m[2])
	}
	return env, nil
}

// SetConfigValue 在 config.env 中设置一个键，
// 其余每一行 ( 连同注释 ) 原样保留：替换该键所在行，或在末尾追加。文件保持私有。
func SetConfigValue(file, key, value string) error {
	var lines []string
	//nolint:gosec // G304: 路径来自用户自己的 config.env 布局，非不可信输入。
	if text, err := os.ReadFile(file); err == nil {
		lines = strings.Split(string(text), "\n")
	} else if !os.IsNotExist(err) {
		return err
	}
	keyRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	at := -1
	for i, l := range lines {
		if keyRe.MatchString(l) {
			at = i
			break
		}
	}
	if at >= 0 {
		lines[at] = key + "=" + value
	} else {
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = lines[:n-1]
		}
		lines = append(lines, key+"="+value, "")
	}

	//nolint:gosec // G703: 写入目标即用户自己的 config.env，非不可信输入。
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return err
	}
	return os.Chmod(file, 0o600)
}

// Spaces 是一组 space：全部、没有、或点名的那些。
type Spaces struct {
	All  bool
	List []string
}

// SpacesOf 读取一个 spaces 设置。未设置 ( set=false ) 表示取 fallback；
// `all` 是全部，空串或 `none` 是没有，其余为逗号分隔的 space 列表。
func SpacesOf(value string, set bool, fallback string) Spaces {
	v := strings.TrimSpace(fallback)
	if set {
		v = strings.TrimSpace(value)
	}
	if v == "all" {
		return Spaces{All: true}
	}
	if v == "" || v == "none" {
		return Spaces{}
	}
	var list []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			list = append(list, s)
		}
	}
	return Spaces{List: list}
}

// PartsOf 判断逗号分隔列表点名了 known 中的哪些；空串或 `none` 表示没有，
// 未设置 ( set=false ) 时取 fallback。未知的名字是错误。
func PartsOf(value string, set bool, fallback string, known []string, key string) (map[string]bool, error) {
	v := strings.TrimSpace(fallback)
	if set {
		v = strings.TrimSpace(value)
	}
	var named []string
	if v != "" && v != "none" {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				named = append(named, s)
			}
		}
	}
	var unknown []string
	for _, n := range named {
		ok := false
		for _, k := range known {
			if k == n {
				ok = true
				break
			}
		}
		if !ok {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("%s: %s is not one of %s", key, strings.Join(unknown, ", "), strings.Join(known, ", "))
	}
	out := map[string]bool{}
	for _, k := range known {
		out[k] = false
		for _, n := range named {
			if n == k {
				out[k] = true
				break
			}
		}
	}
	return out, nil
}

// OnOff 读取一个开/关设置 ( 也接受 true/yes/1 与 false/no/0 )；
// 其余任何值都表示 fallback。
func OnOff(value string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "yes", "1":
		return true
	case "off", "false", "no", "0":
		return false
	}
	return fallback
}

// DefaultSession 是 hc 自己的 session，空的 HERDR_SESSIONS 即指它。
const DefaultSession = "default"

// Config 是一次运行需要的全部，来自 config.env 与环境变量。
type Config struct {
	Profile     string
	ConfigFile  string
	Chat        string
	Env         map[string]string
	Sessions    []string
	SessionsSet bool
	AppID       string
	AppSecret   string
	OwnerOpenID string
	Muted       Spaces
	Silent      Spaces
	BotTags     bool
	UserTags    bool
	History     map[string]bool
}

// Load 把 config.env 与环境变量合并 ( 环境变量优先，同 shell )，
// 并检查一个 chat app 启动不可或缺的键 ( requires )。
func Load(file string, env map[string]string, requires []string) (Config, error) {
	fromFile, err := ParseEnv(file)
	if err != nil {
		return Config{}, err
	}
	merged := map[string]string{}
	for k, v := range fromFile {
		merged[k] = v
	}
	for k, v := range env {
		merged[k] = v
	}
	get := func(key string) (string, bool) { v, ok := merged[key]; return v, ok }
	var sessions []string
	for _, s := range strings.Split(merged["HERDR_SESSIONS"], ",") {
		if s = strings.TrimSpace(s); s != "" {
			sessions = append(sessions, s)
		}
	}
	sessionsSet := len(sessions) > 0
	if !sessionsSet {
		sessions = []string{DefaultSession}
	}
	var missing []string
	for _, k := range requires {
		if v, _ := get(k); v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		plural := "is"
		fill := "it"
		if len(missing) > 1 {
			plural, fill = "are", "them"
		}
		return Config{}, fmt.Errorf("%s %s empty in %s: fill %s in (the comment above each says where it comes from), then start again",
			strings.Join(missing, ", "), plural, file, fill)
	}
	muted, _ := get("HERDR_MUTED_SPACES")
	silent, _ := get("HERDR_SILENT_SPACES")
	history, _ := get("HERDR_HISTORY")
	parts, err := PartsOf(history, true, "file,folded", []string{"file", "folded"}, "HERDR_HISTORY")
	if err != nil {
		return Config{}, err
	}
	return Config{
		Profile:     "",
		ConfigFile:  file,
		Chat:        orDefault(merged["HERDRCHAT_CHAT"], "feishu"),
		Env:         merged,
		Sessions:    sessions,
		SessionsSet: sessionsSet,
		AppID:       merged["FEISHU_APP_ID"],
		AppSecret:   merged["FEISHU_APP_SECRET"],
		OwnerOpenID: merged["FEISHU_OWNER_OPEN_ID"],
		Muted:       SpacesOf(muted, true, "all"),
		Silent:      SpacesOf(silent, true, ""),
		BotTags:     OnOff(merged["HERDR_BOT_TAGS"], true),
		UserTags:    OnOff(merged["HERDR_USER_TAGS"], true),
		History:     parts,
	}, nil
}

// ChatOf 在检查各键之前，说出 config.env 配的是哪个 chat app。
func ChatOf(file string, env map[string]string) string {
	fromFile, err := ParseEnv(file)
	if err != nil {
		fromFile = map[string]string{}
	}
	if v := env["HERDRCHAT_CHAT"]; v != "" {
		return v
	}
	return orDefault(fromFile["HERDRCHAT_CHAT"], "feishu")
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
