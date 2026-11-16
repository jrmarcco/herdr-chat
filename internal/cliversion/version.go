// Package cliversion 只放一个变量与一个比较：
// goreleaser 构建时用-X github.com/jrmarcco/herdr-chat/internal/cliversion.Version={{.Version}}
// 把版本写进来 ( `go install`/`go build` 的本地构建保持 "dev" )。
//
// 单独成包 ldflags 的 -X 才有稳定的落点，main 与 cli/engine 共用它。
package cliversion

import "strings"

// Version 是这个二进制的版本；"dev" 表示不是一次发布构建。
var Version = "dev"

// Dev 说这个二进制是不是一次发布构建。
func Dev() bool { return DevVersion(Version) }

// DevVersion 按一个版本串判断它是不是发布构建
// ( 空串、dev、devel 都不是；App.Version、引擎的 Version 都用它，别只看包里的变量 )。
// 带预发布后缀 ( "-"，含 Go 模块伪版本 vX.Y.Z-<time>-<hash>——本地构建的
// Main.Version 正是它 ) 的也不是：否则 v1.0.4-0.x 会被当成 1.0.4 本身，
// `update` 就把真发布拒之门外了 ( 教训记在这 )。
func DevVersion(v string) bool {
	return v == "dev" || v == "" || v == "(devel)" || strings.Contains(v, "-")
}

// Newer 说 release 标签 a ( 如 "v1.2.3" ) 是否比 b 新：
// 按点分段数值比较；
// 前缀 v 剥掉，段里取前导数字；
// 段数不同按缺 0 补；数字全平时正式版胜过预发布 ( 1.0.4 > 1.0.4-0.任何——
// 本地构建的伪版本正是它 )，两个预发布之间不排 ( 哈希无意义 )。
// 解析不出的比较一律当"不新"——宁可少提示，不瞎升级。
func Newer(a, b string) bool {
	as, preA, okA := parts(a)
	bs, preB, okB := parts(b)
	if !okA || !okB {
		return false
	}
	for i := range max(len(as), len(bs)) {
		av, bv := 0, 0
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av != bv {
			return av > bv
		}
	}
	return !preA && preB // 同版：正式版比预发布新
}

// parts 把 "v1.2.3-rc1" 剥成 [1 2 3] 与"是预发布"。
// 第一个 "-" 起 ( -rc1、伪版本的 -0.<时间>-<hash> ) 全是预发布后缀，整段切掉。
func parts(tag string) ([]int, bool, bool) {
	tag = trimV(tag)
	pre := false
	if i := strings.Index(tag, "-"); i >= 0 {
		tag, pre = tag[:i], true
	}
	if tag == "" {
		return nil, pre, false
	}
	var out []int
	for _, seg := range splitDots(tag) {
		n, ok := leadingNumber(seg)
		if !ok {
			return nil, pre, false
		}
		out = append(out, n)
	}
	return out, pre, len(out) > 0
}

func trimV(tag string) string {
	if len(tag) > 1 && (tag[0] == 'v' || tag[0] == 'V') {
		return tag[1:]
	}
	return tag
}

func splitDots(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// leadingNumber 取一段的前导数字 ( "3-rc1" 的 3 )；
// 一段没有数字打头就解析不出。
func leadingNumber(seg string) (int, bool) {
	end := 0
	for end < len(seg) && seg[end] >= '0' && seg[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n := 0
	for _, c := range []byte(seg[:end]) {
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			return 1 << 30, true
		}
	}
	return n, true
}
