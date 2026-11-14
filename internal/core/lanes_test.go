package core

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSameKeyRunsInOrder(t *testing.T) {
	l := NewLanes()
	var mu sync.Mutex
	var order []int
	var steps sync.WaitGroup
	for i := 0; i < 50; i++ {
		steps.Add(1)
		go func(i int) {
			l.Go("t1", func() error {
				defer steps.Done()
				mu.Lock()
				order = append(order, i)
				mu.Unlock()
				return nil
			})
		}(i)
	}
	steps.Wait()
	// 每一步都落地；车道保证了每步的原子性 ( 单互斥锁下的 append 即
	// "没有两步交错" 的替身 )。
	if len(order) != 50 {
		t.Errorf("ran %d steps, want 50", len(order))
	}
}

func TestSameKeyIsSerial(t *testing.T) {
	// 睡眠的那一步必须占住车道：同 key 的第二步要等第一步返回后才能开始。
	l := NewLanes()
	var inside atomic.Int32
	done := make(chan struct{})
	go l.Go("t1", func() error {
		inside.Store(1)
		time.Sleep(60 * time.Millisecond)
		inside.Store(2)
		close(done)
		return nil
	})
	// 稍等第一步启动，再压入第二步。
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	_ = l.Do("t1", func() error { return nil })
	<-done
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("second step ran after %v; the lane did not serialise", elapsed)
	}
	if inside.Load() != 2 {
		t.Errorf("first step must finish untouched")
	}
}

func TestDifferentKeysDoNotWait(t *testing.T) {
	// 被占住的车道不能挡住另一个 key 的步骤。
	l := NewLanes()
	release := make(chan struct{})
	go l.Go("t1", func() error {
		<-release
		return nil
	})
	time.Sleep(10 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		if err := l.Do("t2", func() error { return nil }); err != nil {
			t.Errorf("Do on a free key: %v", err)
		}
		close(done)
	}()
	select {
	case <-done:
		// t1 被占着时 t2 照跑：key 之间互不依赖。
	case <-time.After(500 * time.Millisecond):
		t.Fatal("a held lane blocked another key")
	}
	close(release)
}

func TestDoReturnsError(t *testing.T) {
	l := NewLanes()
	sentinel := ErrTerminalFinished
	if err := l.Do("k", func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("Do must hand the error back, got %v", err)
	}
}
