package core

import (
	"strings"

	"github.com/jrmarcco/herdr-chat/internal/hccli"
)

// 一个终端可能被多种视角看到：
// 它自己所在的 hc session，以及每个通过 machine 连到其 core 的被跟随 session ( 无论叫它什么名字 )。
//
// 一个终端是什么由 hc 说了算、绝不由名字说了算：
// 它跑在哪个 core 上 ( session 自己的 core 用 `hc server status`，
// 连到的 core 用 `hc machine ls` ) 加上它在这个 core 上的 tN。

// CoreInfo 是一个 session 的 core 拓扑：自己的 core id 与它连到的 core。
type CoreInfo struct {
	Self  string
	Links []hccli.MachineWithID
}

// CoreViews 按 core id 归一同一终端的多个视角，并回答谁跟随它。
type CoreViews struct {
	sessions []string
	cores    map[string]CoreInfo
}

// CoreViewsOf 建一个拓扑视图；sessions 的顺序即配置顺序 ( owner 回退时用 )。
func CoreViewsOf(sessions []string, cores map[string]CoreInfo) *CoreViews {
	return &CoreViews{sessions: sessions, cores: cores}
}

// coreOf 说一个视角的终端跑在哪个 core 上；
// 说不出 ( session 未知、机器未知，或机器后面还套机器 ) 返回 ""。
func (v *CoreViews) coreOf(session, address string) string {
	info, ok := v.cores[session]
	if !ok {
		return ""
	}
	machine := hccli.MachineOf(address)
	if strings.Contains(machine, "/") {
		return ""
	}
	if machine == "" {
		return info.Self
	}
	for _, l := range info.Links {
		if l.Name == machine {
			return l.ID
		}
	}
	return ""
}

// Identity 是一个终端跨视角稳定的身份 "<core id>|tN"；说不出的视角返回 ""。
func (v *CoreViews) Identity(session, address string) string {
	core := v.coreOf(session, address)
	if core == "" {
		return ""
	}
	return core + "|" + address[strings.LastIndex(address, "/")+1:]
}

// OwnerOf 说哪个 session 跟随一个 core 上的终端：
// 它自己的 session 优先 ( 直接视角 )，否则配置顺序里第一个有活跃连接的 session；
// machine 为 "" 表示直接跟随。
// 找不到返回 ok=false。
func (v *CoreViews) OwnerOf(core string) (session, machine string, ok bool) {
	for _, s := range v.sessions {
		if v.cores[s].Self == core {
			return s, "", true
		}
	}
	for _, s := range v.sessions {
		for _, l := range v.cores[s].Links {
			if l.ID == core && l.State == "connected" {
				return s, l.Name, true
			}
		}
	}
	return "", "", false
}

// Owns 判断一个视角是否就是那个跟随者；
// 拓扑说不出时 known=false，由调用方回退 ( 本机地址视为自己的；机器地址归第一个看到它 live 的 session )。
func (v *CoreViews) Owns(session, address string) (owned, known bool) {
	core := v.coreOf(session, address)
	if core == "" {
		return false, false
	}
	ownerSession, ownerMachine, ok := v.OwnerOf(core)
	if !ok {
		return false, true
	}
	return ownerSession == session && ownerMachine == hccli.MachineOf(address), true
}

// Reachable 判断一个视角够不够得到它的终端：视角途经的机器未连接时为 false
// ( 该终端挂起，绝不判死 )。
func (v *CoreViews) Reachable(session, address string) bool {
	machine := hccli.MachineOf(address)
	if machine == "" {
		return true
	}
	name := machine
	if i := strings.Index(name, "/"); i >= 0 {
		name = name[:i]
	}
	for _, l := range v.cores[session].Links {
		if l.Name == name {
			return l.State == "connected"
		}
	}
	return true
}
