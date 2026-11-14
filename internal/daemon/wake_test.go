//go:build darwin || linux

package daemon

import (
	"strings"
	"testing"
	"time"

	"bugu/internal/clock"
	"bugu/internal/task"
)

// S4 决策记录 0016:dogfood 时发现的情况。08:00 的任务,电脑在睡眠;08:10 短暂的后台唤醒
// 时网络还没恢复,马上又睡;08:40 真正醒来。补跑不在 08:10 发生,而是在 08:40 醒来、
// 连续醒着一会儿之后发生,并且只跑一次。按时的触发不等。
func TestS4CatchupWaitsUntilAwake(t *testing.T) {
	f := clock.NewFake(at(11, 7, 59))
	d := start(t, t.TempDir(), f)
	create(t, d, command("news", "0 8 * * *", "echo 新闻"))
	tick := func(to time.Time) {
		f.Set(to)
		d.engine.tick()
		d.Wait()
	}
	tick(at(11, 7, 59).Add(15 * time.Second)) // 一直醒着

	// 睡眠跨过 08:00;08:10 短暂唤醒 30 秒。
	tick(at(11, 8, 10))
	tick(at(11, 8, 10).Add(15 * time.Second))
	tick(at(11, 8, 10).Add(30 * time.Second))
	if n := len(runs(t, d, "news")); n != 0 {
		t.Fatalf("短暂唤醒时就补跑了: %d 次", n)
	}

	// 08:40 真正醒来:前两分钟不跑,醒满之后补跑一次。
	tick(at(11, 8, 40))
	tick(at(11, 8, 41))
	if n := len(runs(t, d, "news")); n != 0 {
		t.Fatalf("刚醒来就补跑了: %d 次", n)
	}
	settle(d, f)
	list := runs(t, d, "news")
	if len(list) != 1 || list[0].Status != task.Succeeded || !list[0].ScheduledFor.Equal(at(11, 8, 0)) || !strings.Contains(list[0].Note, "刚醒来") {
		t.Fatalf("runs=%+v", list)
	}
	settle(d, f)
	if n := len(runs(t, d, "news")); n != 1 {
		t.Fatalf("补跑了不止一次: %d", n)
	}

	// 第二天一直醒着:08:00 按时触发,不等。
	for at := at(11, 9, 0); at.Before(time.Date(2026, 11, 12, 8, 0, 10, 0, time.UTC)); at = at.Add(30 * time.Second) {
		f.Set(at)
		d.engine.tick()
	}
	tick(time.Date(2026, 11, 12, 8, 0, 10, 0, time.UTC))
	if list := runs(t, d, "news"); len(list) != 2 || list[0].Note != "" {
		t.Fatalf("按时的触发: %+v", list[0])
	}
}

// S4:设置为不补跑的任务,醒来时立即记下"错过",不需要等。
func TestS4SkipIsRecordedImmediately(t *testing.T) {
	f := clock.NewFake(at(11, 7, 59))
	d := start(t, t.TempDir(), f)
	tk := command("news", "0 8 * * *", "true")
	tk.OnMissed = task.MissedSkip
	create(t, d, tk)
	f.Set(at(11, 9, 0))
	d.engine.tick()
	if list := runs(t, d, "news"); len(list) != 1 || list[0].Status != task.Missed {
		t.Fatalf("runs=%+v", list)
	}
}
