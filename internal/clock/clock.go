// Package clock 是全项目读取时间和等待的唯一入口(决策记录 0003)。
// 生产代码使用 Real,测试使用 Fake 并手动推进时间。
package clock

import (
	"sort"
	"sync"
	"time"
)

// Clock 提供当前时间和定时等待。
type Clock interface {
	// Now 返回当前的墙上时间。
	Now() time.Time
	// After 返回一个通道,经过 d 之后收到当时的时间。
	After(d time.Duration) <-chan time.Time
	NewTimer(d time.Duration) Timer
}

// Timer 是可停止的单次等待。
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type realTimer struct{ timer *time.Timer }

func (t realTimer) C() <-chan time.Time     { return t.timer.C }
func (t realTimer) Stop() bool              { return t.timer.Stop() }
func (Real) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

// Real 是系统时钟。
type Real struct{}

func (Real) Now() time.Time                         { return time.Now() }
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Fake 是由测试控制的时钟:时间只在调用 Advance 或 Set 时前进。
type Fake struct {
	mu      sync.Mutex
	changed *sync.Cond // 等待者数量变化时广播
	now     time.Time
	waiters []*waiter
}

type waiter struct {
	at     time.Time
	ch     chan time.Time
	f      *Fake
	active bool
}

// NewFake 返回一个停在 start 的假时钟。
func NewFake(start time.Time) *Fake {
	f := &Fake{now: start}
	f.changed = sync.NewCond(&f.mu)
	return f
}

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) After(d time.Duration) <-chan time.Time { return f.NewTimer(d).C() }

func (w *waiter) C() <-chan time.Time { return w.ch }
func (w *waiter) Stop() bool {
	w.f.mu.Lock()
	defer w.f.mu.Unlock()
	if !w.active {
		return false
	}
	w.active = false
	for i, entry := range w.f.waiters {
		if entry == w {
			w.f.waiters = append(w.f.waiters[:i], w.f.waiters[i+1:]...)
			w.f.changed.Broadcast()
			break
		}
	}
	return true
}

func (f *Fake) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	w := &waiter{at: f.now.Add(d), ch: ch, f: f, active: d > 0}
	if d <= 0 {
		ch <- f.now
		return w
	}
	f.waiters = append(f.waiters, w)
	f.changed.Broadcast()
	return w
}

// Advance 把时间向前推进 d,并唤醒到期的等待者,按到期时间先后。
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set(f.now.Add(d))
}

// Set 把时间跳到 t,用于模拟睡眠唤醒或改时间。t 早于当前时间时不唤醒任何等待者。
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set(t)
}

// set 要求调用者已持有锁。等待者收到的是被唤醒时的当前时间,与真实定时器一致。
func (f *Fake) set(t time.Time) {
	f.now = t
	sort.SliceStable(f.waiters, func(i, j int) bool { return f.waiters[i].at.Before(f.waiters[j].at) })
	rest := f.waiters[:0]
	for _, w := range f.waiters {
		if w.at.After(t) {
			rest = append(rest, w)
			continue
		}
		w.active = false
		w.ch <- t
	}
	f.waiters = rest
	f.changed.Broadcast()
}

// Waiters 返回尚未到期的等待者数量,测试用它确认被测代码已经开始等待。
func (f *Fake) Waiters() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiters)
}

// BlockUntilWaiters 阻塞到至少有 n 个尚未到期的等待者。测试用它等被测代码进入等待,
// 再推进时间,不需要轮询。
func (f *Fake) BlockUntilWaiters(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.waiters) < n {
		f.changed.Wait()
	}
}
