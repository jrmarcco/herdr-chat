package core

import "sync"

// Lanes 是引擎的并发规则：
// 每个终端 key 一条串行 FIFO 车道，
// 一个终端对它的房间、游标、卡片所做的一切都按序发生，
// 而不同终端之间互不等待。
// 错误由调用方持有 ( 引擎把 ErrMachineDown 变成挂起的房间、其余记日志 )；
// 车道绝不吞错。
//
// 每个 key 一把互斥锁恰是一条串行车道 Do。
// 自己不排队，只是在该key 先前的工作完成前不返回。
type Lanes struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewLanes 建一组空车道。
func NewLanes() *Lanes {
	return &Lanes{locks: map[string]*sync.Mutex{}}
}

func (l *Lanes) lane(key string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, ok := l.locks[key]
	if !ok {
		m = &sync.Mutex{}
		l.locks[key] = m
	}
	return m
}

// Do 把 fn 作为该 key 的下一步运行 ( 在它之前排入的每一步之后 )，
// 并把 fn 的错误原样交回。
func (l *Lanes) Do(key string, fn func() error) error {
	m := l.lane(key)
	m.Lock()
	defer m.Unlock()
	return fn()
}

// Go 是不等待的 Do：
// fn 是该 key 的下一步，在自己的 goroutine 上运行，
// 错误就地丢弃 ( 适用于失败已在 fn 内部自行上报的步骤 )。
func (l *Lanes) Go(key string, fn func() error) {
	m := l.lane(key)
	go func() {
		m.Lock()
		defer m.Unlock()
		_ = fn()
	}()
}
