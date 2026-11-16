package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jrmarcco/herdr-chat/internal/config"
)

// setup 里可选的加密策略两键：回车跳过不写、回车保留已存、给了值才写。
func TestSetupAsksTheEventKeysOptionally(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.env")

	starter := func(t *testing.T, saved string) {
		t.Helper()
		text := "HERDRCHAT_CHAT=feishu\nFEISHU_APP_ID=x\nFEISHU_APP_SECRET=y\n" + saved
		if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(t *testing.T, input string) *bytes.Buffer {
		t.Helper()
		var out bytes.Buffer
		a := &App{
			Env:    map[string]string{},
			Home:   t.TempDir(),
			Stdin:  strings.NewReader(input),
			Stdout: &out,
			TTY:    true,
			Stty:   func(string) bool { return true },
		}
		env, err := config.ParseEnv(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.askEventKeys(file, env); err != nil {
			t.Fatalf("askEventKeys: %v", err)
		}
		return &out
	}
	values := func(t *testing.T) (enc, token string) {
		t.Helper()
		env, err := config.ParseEnv(file)
		if err != nil {
			t.Fatal(err)
		}
		return env[keyEncryptKey], env[keyVerifyToken]
	}

	t.Run("enter skips, nothing is written", func(t *testing.T) {
		starter(t, "FEISHU_ENCRYPT_KEY=\nFEISHU_VERIFICATION_TOKEN=\n")
		out := ask(t, "\n\n")
		if enc, token := values(t); enc != "" || token != "" {
			t.Fatalf("keys = %q %q, want both empty", enc, token)
		}
		if !strings.Contains(out.String(), "[skip]") {
			t.Fatalf("prompt %q does not say it is optional", out.String())
		}
	})

	t.Run("enter keeps what is saved", func(t *testing.T) {
		starter(t, "FEISHU_ENCRYPT_KEY=old-enc\nFEISHU_VERIFICATION_TOKEN=old-token\n")
		out := ask(t, "\n\n")
		if enc, token := values(t); enc != "old-enc" || token != "old-token" {
			t.Fatalf("keys = %q %q, want the saved ones", enc, token)
		}
		if !strings.Contains(out.String(), "[kept]") {
			t.Fatalf("prompt %q does not say a value is kept", out.String())
		}
	})

	t.Run("answers are written", func(t *testing.T) {
		starter(t, "FEISHU_ENCRYPT_KEY=\nFEISHU_VERIFICATION_TOKEN=\n")
		ask(t, "new-enc\nnew-token\n")
		if enc, token := values(t); enc != "new-enc" || token != "new-token" {
			t.Fatalf("keys = %q %q, want the answers", enc, token)
		}
	})

	t.Run("an answer replaces the saved one", func(t *testing.T) {
		starter(t, "FEISHU_ENCRYPT_KEY=old-enc\nFEISHU_VERIFICATION_TOKEN=old-token\n")
		ask(t, "new-enc\n\n")
		if enc, token := values(t); enc != "new-enc" || token != "old-token" {
			t.Fatalf("keys = %q %q, want one replaced one kept", enc, token)
		}
	})
}
