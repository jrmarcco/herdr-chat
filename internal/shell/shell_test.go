package shell

import "testing"

// TestQuote 锁住转义契约：拼进 sh -c 的东西，三种危险的引号形状都得活下来。
func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":            `''`,
		"plain":       `'plain'`,
		"it's":        `'it'\''s'`,
		`a'b'c`:       `'a'\''b'\''c'`,
		`'; rm -rf /`: `''\''; rm -rf /'`,
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}
