// Package shell 收拢拼给 shell 跑的零碎。
// 单引号转义一处定义：cli、asks 与 opencode 拼本机/远端脚本时共用。
package shell

import "strings"

// Quote 把一段 shell 文本安全地包进单引号 ( 内部的单引号按 '\” 转义 )。
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
