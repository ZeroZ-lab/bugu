package clock

import (
	"testing"
	"time"
)

var start = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

func TestFakeAfterFiresOnAdvance(t *testing.T) {
	f := NewFake(start)
	ch := f.After(10 * time.Minute)

	f.Advance(9 * time.Minute)
	select {
	case <-ch:
		t.Fatal("未到期就被唤醒")
	default:
	}
	if f.Waiters() != 1 {
		t.Fatalf("等待者 = %d,期望 1", f.Waiters())
	}

	f.Advance(time.Minute)
	select {
	case got := <-ch:
		if want := start.Add(10 * time.Minute); !got.Equal(want) {
			t.Errorf("收到 %v,期望 %v", got, want)
		}
	default:
		t.Fatal("到期后没有被唤醒")
	}
	if f.Waiters() != 0 {
		t.Errorf("等待者 = %d,期望 0", f.Waiters())
	}
}

// 模拟笔记本睡眠:时间一次跳过多个等待者,全部都应被唤醒。
func TestFakeSetJumpWakesAll(t *testing.T) {
	f := NewFake(start)
	a, b := f.After(time.Hour), f.After(3*time.Hour)
	f.Set(start.Add(24 * time.Hour))
	for name, ch := range map[string]<-chan time.Time{"a": a, "b": b} {
		select {
		case <-ch:
		default:
			t.Errorf("%s 没有被唤醒", name)
		}
	}
}

func TestFakeAfterNonPositiveFiresAtOnce(t *testing.T) {
	f := NewFake(start)
	select {
	case <-f.After(0):
	default:
		t.Fatal("时长为 0 应立即到期")
	}
}

// E3: 执行提前结束时可释放总时长上限的等待资源。
func TestE3StoppedTimerDoesNotFire(t *testing.T) {
	f := NewFake(start)
	timer := f.NewTimer(time.Hour)
	if !timer.Stop() || timer.Stop() || f.Waiters() != 0 {
		t.Fatal("停止后仍保留等待或重复停止成功")
	}
	f.Advance(2 * time.Hour)
	select {
	case <-timer.C():
		t.Fatal("已停止的定时器仍触发")
	default:
	}
	expired := f.NewTimer(time.Hour)
	f.Advance(time.Hour)
	<-expired.C()
	if expired.Stop() {
		t.Fatal("已触发的定时器停止返回成功")
	}
}

// 被测代码在另一个 goroutine 里开始等待后,BlockUntilWaiters 才返回。
func TestFakeBlockUntilWaiters(t *testing.T) {
	f := NewFake(start)
	fired := make(chan time.Time, 1)
	go func() { fired <- <-f.After(time.Minute) }()
	f.BlockUntilWaiters(1)
	f.Advance(time.Minute)
	if got := <-fired; !got.Equal(start.Add(time.Minute)) {
		t.Fatalf("收到 %v", got)
	}
}
