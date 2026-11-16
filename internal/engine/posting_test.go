package engine

import (
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/core"
	"github.com/jrmarcco/herdr-chat/internal/hccli"
)

// deliver 的重试只留给会变的局面：
// 无错也无房 ( 未展示的终端、tab 被占着 ) 时立刻收场，
// 不占着车道空等——一次 sleep 都不该有。
func TestDeliverStopsWhenNoRoomAndNoError(t *testing.T) {
	r := harness(t)
	slept := false
	r.e.Sleep = func(time.Duration) { slept = true }

	record := hccli.Terminal{keyAddress: "t9"} // 没有 where：不该有房间
	msg, err := r.e.deliver(sessDefault, record, func(core.Room) (core.Message, error) {
		t.Fatal("nothing should be sent without a room")
		return "", nil
	})
	if msg != "" || err != nil {
		t.Fatalf("deliver = %q, %v; want nothing sent", msg, err)
	}
	if slept {
		t.Fatal("deliver waited on a room that cannot appear")
	}
}
