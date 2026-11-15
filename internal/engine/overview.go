package engine

import (
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
	"github.com/jrmarcco/herdr-chat/internal/xlog"
)

// 直聊总览：终端一有变化就重排；短时间内的变化合并为一次更新。

// overviewWait 是总览更新的去抖窗口。
func (e *Engine) overviewWait() time.Duration {
	if e.Quick {
		return time.Millisecond
	}
	return 2 * time.Second
}

// scheduleOverview 请求一次总览刷新；已有的排队请求不重复。
func (e *Engine) scheduleOverview() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.overviewDue != nil || e.stopping {
		return
	}
	e.overviewDue = time.AfterFunc(e.overviewWait(), func() {
		e.mu.Lock()
		e.overviewDue = nil
		e.mu.Unlock()
		e.refreshOverview(false)
	})
}

// refreshOverview 把当前每个终端一行排出来，交给 chat 原地编辑 ( fresh=true 时发一条新的，/ls 用 )。
func (e *Engine) refreshOverview(fresh bool) {
	if e.ctx == nil || e.ctx.Err() != nil {
		return
	}
	rows := e.overviewRows
	if err := e.Chat.ShowOverview(e.ctx, rows(), core.OverviewOpts{Fresh: fresh}); err != nil {
		xlog.For("engine").Warn("总览更新失败", "error", err)
	}
}

func (e *Engine) overviewRows() []core.OverviewRow {
	var rows []core.OverviewRow
	for _, session := range e.Sessions {
		if v := e.viewOf(session); v != nil {
			for _, record := range v.All() {
				space, tabName_ := shownWhere(record)
				if space == "" || !e.handles(session, record.Address()) {
					continue
				}
				k := e.keyOf(session, record.Address())
				address := record.Address()
				if session != e.Sessions[0] && hccli.MachineOf(address) == "" {
					address = session + " " + address
				}
				since := ""
				e.mu.Lock()
				if t, ok := e.since[k]; ok {
					since = t.UTC().Format("15:04")
				}
				e.mu.Unlock()
				rows = append(rows, core.OverviewRow{
					Name:    e.roomName(space, tabName_, record),
					Address: address,
					State:   record.State(),
					Kind:    statusKind(record),
					Program: foregroundName(record),
					Since:   since,
					Room:    core.Room(e.read().Chats[k].Chat),
				})
			}
		}
	}
	return rows
}

func statusKind(record hccli.Terminal) string {
	status, _ := record["status"].(map[string]any)
	if status == nil {
		return ""
	}
	kind, _ := status["kind"].(string)
	return kind
}
