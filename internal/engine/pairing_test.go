package engine

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/state"
)

// 配对：没有 owner 的 bot 只认配对码。

// directMsg 与 engine_test 的 direct 共用一条消息 id。
const directMsg = "d0"

type pairRig struct {
	*rig

	mu sync.Mutex

	owner     string
	restarted int
}

func pairHarness(t *testing.T) *pairRig {
	t.Helper()

	p := &pairRig{rig: harness(t)}
	p.chat.SetReady(false)
	p.e.SetOwner = func(id string) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.owner = id
		return nil
	}
	p.e.Restart = func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.restarted++
	}
	if err := p.e.Start(t.Context(), func() {}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.e.Stop)
	return p
}

func (p *pairRig) say(text string) {
	p.chat.Handlers.OnMessage(core.Incoming{Where: core.Where{Message: directMsg}, Message: directMsg, Text: text, Sender: "ou_you"})
}

func (p *pairRig) setPairing(t *testing.T, pairing *state.Pairing) {
	t.Helper()
	st, _ := p.store.Read()
	st.Pairing = pairing
	if err := p.store.Write(st); err != nil {
		t.Fatal(err)
	}
}

// lastReply 是最近一条 Reply 的文字。
func (p *pairRig) lastReply() string {
	calls := p.chat.Calls()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Name == callReply && len(calls[i].Args) >= 2 {
			md, _ := calls[i].Args[1].(string)
			return md
		}
	}
	return ""
}

func (p *pairRig) ownerNow() (string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.owner, p.restarted
}

func TestTheRightCodeMakesYouTheOwnerAndTheBotStartsAgain(t *testing.T) {
	p := pairHarness(t)
	p.setPairing(t, &state.Pairing{Code: "042137", Until: time.Now().Add(time.Minute).UnixMilli()})

	p.say(" 042137 ")
	if owner, _ := p.ownerNow(); owner != "ou_you" {
		t.Fatalf("owner = %q, want the one who sent the code", owner)
	}
	if !strings.Contains(p.lastReply(), "owner now") {
		t.Fatalf("reply = %q", p.lastReply())
	}
	if st, _ := p.store.Read(); st.Pairing != nil {
		t.Fatal("a used code is gone")
	}
	p.waitFor(t, "重启", func() bool { _, n := p.ownerNow(); return n == 1 })
	if p.countCalls("OpenRoom") != 0 {
		t.Fatal("no room is made before the owner's own instance starts")
	}
}

func TestAWrongCodeCountsAndFiveVoidIt(t *testing.T) {
	p := pairHarness(t)
	p.setPairing(t, &state.Pairing{Code: "042137", Until: time.Now().Add(time.Minute).UnixMilli()})

	for i := 1; i < core.PairTries; i++ {
		p.say("000000")
		st, _ := p.store.Read()
		if st.Pairing == nil || st.Pairing.Tries != i {
			t.Fatalf("after %d wrong codes: %+v", i, st.Pairing)
		}
	}

	if !strings.Contains(p.lastReply(), "1 of 5 tries left") {
		t.Fatalf("reply = %q", p.lastReply())
	}
	p.say("000000")
	if st, _ := p.store.Read(); st.Pairing != nil {
		t.Fatal("the fifth wrong code voids the pairing")
	}
	p.say("042137")
	if owner, n := p.ownerNow(); owner != "" || n != 0 {
		t.Fatal("a void code makes no owner")
	}
}

func TestWithNoCodeOrAnOldOneItSaysHowAndRunsNothing(t *testing.T) {
	p := pairHarness(t)

	p.say("hello")
	reply := p.lastReply()

	if !strings.Contains(reply, "herdrchat pair") || !strings.Contains(reply, "FAKE_OWNER_ID=ou_you") {
		t.Fatalf("reply = %q, want how to pair and the line to fill in by hand", reply)
	}

	p.setPairing(t, &state.Pairing{Code: "042137", Until: time.Now().Add(-time.Second).UnixMilli()})
	p.say("042137")
	if owner, _ := p.ownerNow(); owner != "" {
		t.Fatal("a code past its time makes no owner")
	}
	if st, _ := p.store.Read(); st.Pairing != nil {
		t.Fatal("a code past its time is dropped")
	}

	// 没有 owner 的 bot 不替任何人起终端、杀终端。
	p.say("/kill default t1")
	p.say("/new default api/x")
	if n := len(p.hc.Sents()); n != 0 {
		t.Fatalf("%d things were typed for a stranger", n)
	}
	for _, c := range p.chat.Calls() {
		if c.Name == "Command" || c.Name == "ShowOverview" {
			t.Fatalf("%s ran for a stranger", c.Name)
		}
	}
}
