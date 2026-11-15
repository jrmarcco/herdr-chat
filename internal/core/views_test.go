package core

import (
	"testing"

	"github.com/jrmarcco/herdr-chat/internal/hccli"
)

// 视图归一行为测试：
// 同一终端的多个视角归一、跟随者归属、顺序无关、断连移交、拓扑未知的回退。
const (
	sessMain = "main"
	coreA    = "core-a"
	coreB    = "core-b"
)

func link(name, id string, stateWord ...string) hccli.MachineWithID {
	state := "connected"
	if len(stateWord) > 0 {
		state = stateWord[0]
	}
	return hccli.MachineWithID{Name: name, ID: id, State: state}
}

// 三台 session 在一台电脑上："main" 以机器形式够到另外两台，三者连着同一台 server。
func testCores() map[string]CoreInfo {
	return map[string]CoreInfo{
		"main": {Self: "core-main", Links: []hccli.MachineWithID{link("box-a", coreA), link("box-b", coreB), link("server", "core-srv")}},
		"a":    {Self: coreA, Links: []hccli.MachineWithID{link("server", "core-srv")}},
		"b":    {Self: coreB, Links: []hccli.MachineWithID{link("server", "core-srv")}},
	}
}

func ownsOf(t *testing.T, v *CoreViews, session, address string, want bool) {
	t.Helper()

	got, known := v.Owns(session, address)
	if !known || got != want {
		t.Errorf("Owns(%s, %s) = %v (known=%v), want %v", session, address, got, known, want)
	}
}

func TestViewIdentityAndOwnership(t *testing.T) {
	v := CoreViewsOf([]string{sessMain, "a", "b"}, testCores())

	// 从别的 session 看到的终端，只在其自己的 session 被跟随。
	if v.Identity("a", "t37") != v.Identity(sessMain, "box-a/t37") {
		t.Errorf("同一终端的身份必须跨视角一致")
	}
	ownsOf(t, v, "a", "t37", true)
	ownsOf(t, v, sessMain, "box-a/t37", false)
	ownsOf(t, v, "b", "t1", true)
	ownsOf(t, v, sessMain, "box-b/t1", false)
	ownsOf(t, v, sessMain, "t4", true)
}

func TestMachineFollowedOnceByFirstSession(t *testing.T) {
	// 同一台机器无论各 session 叫它什么名字，只被第一个 session 跟随一次。
	named := map[string]CoreInfo{
		"a": {Self: coreA, Links: []hccli.MachineWithID{link("server", "core-srv")}},
		"b": {Self: coreB, Links: []hccli.MachineWithID{link("agent", "core-srv")}},
	}
	v := CoreViewsOf([]string{"a", "b"}, named)
	if v.Identity("a", "server/t5") != v.Identity("b", "agent/t5") {
		t.Errorf("别名不影响身份")
	}
	ownsOf(t, v, "a", "server/t5", true)
	ownsOf(t, v, "b", "agent/t5", false)
}

func TestSessionOrderDoesNotMatterForOwn(t *testing.T) {
	// HERDR_SESSIONS 的顺序不影响一个自身被跟随的 session。
	v := CoreViewsOf([]string{sessMain, "a", "b"}, testCores())
	w := CoreViewsOf([]string{"a", "b", sessMain}, testCores())

	for _, x := range []*CoreViews{v, w} {
		ownsOf(t, x, "a", "t37", true)
		ownsOf(t, x, "main", "box-a/t37", false)
	}
	ownsOf(t, v, sessMain, "server/t5", true)
	ownsOf(t, w, "a", "server/t5", true)
}

func TestDroppedConnectionHandsOver(t *testing.T) {
	// 断掉的连接把它的终端交给下一个连着该 core 的 session。
	dropped := map[string]CoreInfo{
		"a": {Self: coreA, Links: []hccli.MachineWithID{link("server", "core-srv", "disconnected")}},
		"b": {Self: coreB, Links: []hccli.MachineWithID{link("server", "core-srv")}},
	}
	v := CoreViewsOf([]string{"a", "b"}, dropped)
	ownsOf(t, v, "a", "server/t5", false)
	ownsOf(t, v, "b", "server/t5", true)
}

func TestFollowedThroughAMachine(t *testing.T) {
	// 只以机器形式被跟随的 session，就在那台机器上跟随。
	v := CoreViewsOf([]string{sessMain}, testCores())
	ownsOf(t, v, sessMain, "box-a/t37", true)
}

func TestUnknownTopologyFallsBackToCaller(t *testing.T) {
	// 拓扑说不出的事留给调用方回退。
	v := CoreViewsOf([]string{sessMain, "x"}, testCores())
	if owned, known := v.Owns("x", "t1"); known {
		t.Errorf("x 的 cores 未知：必须交给调用方回退 ( got %v )", owned)
	}
	if _, known := v.Owns(sessMain, "nowhere/t1"); known {
		t.Errorf("不存在的机器：必须交回调用方")
	}
	if _, known := v.Owns(sessMain, "server/inner/t1"); known {
		t.Errorf("机器后面的机器：必须交回调用方")
	}
	if v.Identity("x", "t1") != "" {
		t.Errorf("未知视角没有身份")
	}
	if !v.Reachable(sessMain, "box-a/t1") {
		t.Errorf("连接中的机器可达")
	}
	dropped := map[string]CoreInfo{
		"a": {Self: coreA, Links: []hccli.MachineWithID{link("server", "core-srv", "disconnected")}},
	}
	if CoreViewsOf([]string{"a"}, dropped).Reachable("a", "server/t1") {
		t.Errorf("断连的机器不可达 ( 房间挂起而非判死 )")
	}
}
