package cliversion

import "testing"

// v123 是比较表的基准标签。
const v123 = "v1.2.3"

func TestNewerComparesTagsLikeAReleasePage(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v1.2.4", v123, true},
		{"1.10.0", "1.9.9", true}, // 数值比，不按字典序
		{"v2.0.0", "v1.99.99", true},
		{v123, "v1.2.4", false},
		{v123, "1.2.3", false},  // 相同即不新
		{"1.2", "1.2.0", false}, // 缺的段按 0
		{"1.2.1", "1.2", true},
		{"v1.2.3-rc1", v123, false}, // 同段即同版：预发布不当新版
		{"latest", v123, false},     // 解析不出：不瞎升级
		{v123, "dev", false},        // dev 自己当最新 ( 升不升由 update 命令另说 )
		{"", "v1.0.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestDevKnowsItself(t *testing.T) {
	old := Version
	defer func() { Version = old }()

	Version = "dev"
	if !Dev() {
		t.Fatal("dev is dev")
	}
	Version = "1.2.3"
	if Dev() {
		t.Fatal("1.2.3 is a release")
	}
	Version = "(devel)"
	if !Dev() {
		t.Fatal("(devel) is not a release")
	}
}
