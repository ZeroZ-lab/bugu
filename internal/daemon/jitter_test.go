//go:build darwin || linux

package daemon

import (
	"fmt"
	"testing"
	"time"

	"bugu/internal/clock"
	"bugu/internal/schedule"
	"bugu/internal/task"
)

// nameWithOffset 找一个任务名,使它在原定时间点 at 的偏移满足 ok。偏移由任务名决定(决策记录 0018)。
func nameWithOffset(t *testing.T, tr task.Trigger, at time.Time, ok func(time.Duration) bool) (string, time.Duration) {
	t.Helper()
	for i := range 1000 {
		name := fmt.Sprintf("jitter-%d", i)
		if off := schedule.Offset(tr, name, at); ok(off) {
			return name, off
		}
	}
	t.Fatal("找不到合适的任务名")
	return "", 0
}

// S12 E7: 设置了随机偏移的任务在偏移后的时间触发;运行记录的原定时间是原来的时间点;
// 提前到前一天跑的 00:00 任务,{date} 仍是原定的那一天;任务列表的下次运行是实际时间。
func TestS12JitteredRunUsesScheduledDate(t *testing.T) {
	f := clock.NewFake(at(11, 12, 0))
	d := start(t, t.TempDir(), f)
	tr := task.Trigger{Cron: "0 0 * * *", Jitter: task.Duration(10 * time.Minute)}
	midnight := at(12, 0, 0)
	name, off := nameWithOffset(t, tr, midnight, func(o time.Duration) bool { return o < -time.Minute })
	tk := command(name, tr.Cron, "echo {date} {time}")
	tk.Trigger = tr
	create(t, d, tk)
	fireAt := midnight.Add(off)
	if info, _ := d.engine.Task(name); !info.NextRun.Equal(fireAt) {
		t.Fatalf("下次运行 %v,应为 %v", info.NextRun, fireAt)
	}

	advance(d, f, fireAt.Add(-time.Second))
	if n := len(runs(t, d, name)); n != 0 {
		t.Fatalf("实际时间之前就触发了: %d 次", n)
	}
	advance(d, f, fireAt)
	list := runs(t, d, name)
	if len(list) != 1 || !list[0].ScheduledFor.Equal(midnight) || list[0].Status != task.Succeeded {
		t.Fatalf("runs=%+v", list)
	}
	if logs, _ := d.engine.Logs(list[0].ID); logs.Stdout != "2026-11-12 00:00:00\n" {
		t.Fatalf("模板时间应去掉偏移: %q", logs.Stdout)
	}
	advance(d, f, midnight.Add(15*time.Minute))
	if n := len(runs(t, d, name)); n != 1 {
		t.Fatalf("同一个时间点触发了 %d 次", n)
	}
}

// S12 S7: 刚按时跑过之后加上随机偏移,同一个时间点因为偏移被推后,也不再跑第二遍。
func TestS12AddingJitterDoesNotRerun(t *testing.T) {
	f := clock.NewFake(at(11, 8, 0))
	d := start(t, t.TempDir(), f)
	tr := task.Trigger{Cron: "0 9 * * *", Jitter: task.Duration(10 * time.Minute)}
	nine := at(11, 9, 0)
	name, _ := nameWithOffset(t, tr, nine, func(o time.Duration) bool { return o > 2*time.Minute })
	tk := command(name, "0 9 * * *", "true")
	create(t, d, tk)
	advance(d, f, nine)
	if n := len(runs(t, d, name)); n != 1 {
		t.Fatalf("09:00 应触发一次: %d", n)
	}

	f.Set(nine.Add(time.Minute))
	tk.Trigger = tr
	if _, err := d.engine.UpdateTask(name, tk); err != nil {
		t.Fatal(err)
	}
	advance(d, f, nine.Add(15*time.Minute))
	if n := len(runs(t, d, name)); n != 1 {
		t.Fatalf("加上偏移后又跑了一遍: %d 次", n)
	}
	next := at(12, 9, 0)
	if info, _ := d.engine.Task(name); !info.NextRun.Equal(next.Add(schedule.Offset(tr, name, next))) {
		t.Fatalf("下次运行 %v", info.NextRun)
	}
}
