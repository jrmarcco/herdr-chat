package engine

import (
	"context"
	"errors"
	"testing"
)

// 新版提示：有新版给 owner 一张卡，
// 同一个版本只提示一次；不是发布构建、问不出来、不比现在新，都安安静静。
func (r *rig) toldUpdate() (string, bool) {
	for _, c := range r.chat.Calls() {
		if c.Name == "TellOwner" && len(c.Args) >= 1 {
			if title, _ := c.Args[0].(string); title != "" {
				return title, true
			}
		}
	}
	return "", false
}

func TestANewerReleaseTellsTheOwnerOnce(t *testing.T) {
	r := harness(t)
	r.start(t)
	verNow := "1.0.0"
	r.e.Version = verNow
	r.e.Latest = func(context.Context) (string, error) { return "1.1.0", nil }

	r.e.updateTick()
	title, ok := r.toldUpdate()
	if !ok || title != "📦 herdrchat 1.1.0 is out" {
		t.Fatalf("told = %q %v", title, ok)
	}
	st, _ := r.store.Read()
	if st.UpdateNoticed != "1.1.0" {
		t.Fatalf("updateNoticed = %q", st.UpdateNoticed)
	}

	// 同一个版本不再提示；更大的版本会再提示。
	r.e.updateTick()
	if n := r.countCalls("TellOwner"); n != 1 {
		t.Fatalf("TellOwner called %d times for one version", n)
	}

	r.e.Latest = func(context.Context) (string, error) { return "1.2.0", nil }
	r.e.updateTick()
	if n := r.countCalls("TellOwner"); n != 2 {
		t.Fatalf("TellOwner called %d times (a newer one should tell again)", n)
	}

	st, _ = r.store.Read()
	if st.UpdateNoticed != "1.2.0" {
		t.Fatalf("updateNoticed = %q", st.UpdateNoticed)
	}
}

func TestNoNoticeWhenNotNewerOrNotAReleaseOrNotAsked(t *testing.T) {
	r := harness(t)
	r.start(t)
	verNow := "1.0.0"
	r.e.Version = verNow

	r.e.Latest = func(context.Context) (string, error) { return verNow, nil }  // 同版 r.e.updateTick
	r.e.Latest = func(context.Context) (string, error) { return "0.9.0", nil } // 更旧 r.e.updateTick
	r.e.Latest = func(context.Context) (string, error) { return "", errors.New("no network") }
	r.e.updateTick()
	if _, ok := r.toldUpdate(); ok {
		t.Fatal("told about an update that is not one")
	}

	// dev 构建 ( 没有版本可说 )：不查。
	r.e.Version = "dev"
	r.e.Latest = func(context.Context) (string, error) { return "9.9.9", nil }
	r.e.updateTick()
	if _, ok := r.toldUpdate(); ok {
		t.Fatal("a dev build tells about releases it cannot compare")
	}
	if st, _ := r.store.Read(); st.UpdateNoticed != "" {
		t.Fatalf("updateNoticed = %q", st.UpdateNoticed)
	}
}
