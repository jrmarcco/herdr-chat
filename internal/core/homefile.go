package core

import (
	"context"
	"strings"

	"github.com/jrmarcco/herdr-chat/internal/state"
)

// 终端所在机器 home 目录下文件的读取：本机直读、远端经 hc exec 的那套通路
// 在 hccli 里实现；游标算法在此 ( 移植自原版 lib/core/home.mjs 的 logAfter )。

// HomeFiles 是 reader 需要的文件通路：读终端所在机器 home 下的一个文件
// ( tail>0 时只取尾部那些行 )。*hccli.CLI 实现了它。
type HomeFiles interface {
	HomeFile(ctx context.Context, session, machine, rel string, tail int) (string, error)
}

// 日志先读的尾部行数；不够 ( 游标更早 ) 再整文件读。
const TailLines = 1500

// LogAfter 读日志里游标之后的条目：先读尾部，尾部够不到游标就整文件读
// ( entryItems 是 reader 自己的解析器 )。
func LogAfter(ctx context.Context, files HomeFiles, session, machine, rel string, cur state.Cursor, entryItems func(map[string]any) []Item) []Item {
	tail, err := files.HomeFile(ctx, session, machine, rel, TailLines)
	if err != nil {
		tail = ""
	}
	items, complete := ItemsAfter(strings.Split(tail, "\n"), cur, entryItems)
	if complete {
		return items
	}
	whole, err := files.HomeFile(ctx, session, machine, rel, 0)
	if err != nil {
		return items
	}
	items, _ = ItemsAfter(strings.Split(whole, "\n"), cur, entryItems)
	return items
}
