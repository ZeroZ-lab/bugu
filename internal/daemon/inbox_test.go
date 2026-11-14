//go:build darwin || linux

package daemon

import (
	"runtime"
	"strings"
	"testing"

	"bugu/internal/api"
	"bugu/internal/clock"
	"bugu/internal/task"
)

// G3 G9: 收件箱里是有结果要看的运行:有输出的成功、失败;无内容和没有输出的成功不进来。
// 读过之后不再算未读,失败单独计数供菜单栏显示。
func TestG3G9InboxAndUnread(t *testing.T) {
	f := clock.NewFake(epoch)
	d := start(t, t.TempDir(), f)
	report := command("report", "0 2 * * *", "echo '# 3 条新闻'; echo 详情")
	quiet := command("quiet", "0 2 * * *", "true")
	silent := command("silent", "0 2 * * *", "echo NOTHING_NEW")
	silent.SilentIf = "NOTHING_NEW"
	broken := command("broken", "0 2 * * *", "echo 磁盘满了 >&2; exit 1")
	for _, tk := range []task.Task{report, quiet, silent, broken} {
		create(t, d, tk)
	}
	advance(d, f, at(11, 2, 0))

	in, err := d.engine.Inbox(false)
	if err != nil || in.Unread != 2 || in.UnreadFailed != 1 || len(in.Runs) != 2 {
		t.Fatalf("inbox=%+v err=%v", in, err)
	}
	byTask := map[string]task.Run{}
	for _, r := range in.Runs {
		byTask[r.Task] = r
	}
	if byTask["report"].Summary != "3 条新闻 详情" || byTask["broken"].Summary != "磁盘满了" {
		t.Fatalf("摘要: %+v", in.Runs)
	}

	if r, err := d.engine.MarkRead(byTask["report"].ID); err != nil || r.ReadAt.IsZero() {
		t.Fatalf("mark read: %+v %v", r, err)
	}
	if in, _ := d.engine.Inbox(false); in.Unread != 1 || in.Runs[0].Task != "broken" {
		t.Fatalf("读过一条之后: %+v", in)
	}
	if all, _ := d.engine.Inbox(true); len(all.Runs) != 2 {
		t.Fatalf("全部: %+v", all)
	}
	// 不在收件箱里的运行标为已读也不改动。
	quietRun := runs(t, d, "quiet")[0]
	if r, err := d.engine.MarkRead(quietRun.ID); err != nil || !r.ReadAt.IsZero() {
		t.Fatalf("quiet: %+v %v", r, err)
	}

	advance(d, f, at(12, 2, 0))
	if in, _ := d.engine.Inbox(false); in.Unread != 3 {
		t.Fatalf("第二天: %+v", in)
	}
	if n, err := d.engine.MarkAllRead(); err != nil || n != 3 {
		t.Fatalf("全部已读: %d %v", n, err)
	}
	if in, _ := d.engine.Inbox(false); in.Unread != 0 || in.UnreadFailed != 0 || len(in.Runs) != 0 {
		t.Fatalf("全部已读之后: %+v", in)
	}
}

// G1: 任务列表带着每个任务最近一次已经结束的运行。别的任务跑得再频繁,也不会把它挤掉。
func TestG1TaskListCarriesLastRun(t *testing.T) {
	f := clock.NewFake(epoch)
	d := start(t, t.TempDir(), f)
	create(t, d, command("weekly", "0 2 * * 1", "echo 周报"))
	create(t, d, command("often", "*/15 * * * *", "true"))
	create(t, d, command("never", "0 2 * * *", "true"))
	if _, err := d.engine.RunNow("weekly", ""); err != nil {
		t.Fatal(err)
	}
	d.Wait()
	for range 20 {
		if _, err := d.engine.RunNow("often", ""); err != nil {
			t.Fatal(err)
		}
		d.Wait()
	}
	infos, err := d.engine.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	last := map[string]*task.Run{}
	for _, i := range infos {
		last[i.Name] = i.LastRun
	}
	if r := last["weekly"]; r == nil || r.Status != task.Succeeded || r.Summary != "周报" {
		t.Fatalf("weekly: %+v", r)
	}
	if r := last["often"]; r == nil || r.ID != runs(t, d, "often")[0].ID {
		t.Fatalf("often: %+v", r)
	}
	if last["never"] != nil {
		t.Fatalf("never: %+v", last["never"])
	}
	if one, err := d.engine.Task("weekly"); err != nil || one.LastRun == nil || one.LastRun.ID != last["weekly"].ID {
		t.Fatalf("单个任务: %+v %v", one, err)
	}
}

// G6: 运行中的运行详情能看到正在执行的那一步已有的输出,单步骤的放在整次运行的输出里,
// 多步骤的放在那一步里;结束后与存档一致。
func TestG6LogsWhileRunning(t *testing.T) {
	f := clock.NewFake(epoch)
	d := start(t, t.TempDir(), f)
	for _, multi := range []bool{false, true} {
		g1, release1 := fifo(t)
		g2, release2 := fifo(t)
		body := "echo started; cat " + g1 + "; cat " + g2 + "; echo finished"
		tk := command("single", "0 2 * * *", body)
		if multi {
			tk = command("multi", "0 2 * * *", "echo first")
			tk.Steps = append(tk.Steps, task.Step{Type: task.StepScript, Script: task.Script{Command: body}})
		}
		create(t, d, tk)
		r, err := d.engine.RunNow(tk.Name, "")
		if err != nil {
			t.Fatal(err)
		}
		release1() // 返回时 started 已经写进管道,第二个 cat 让这一步停在运行中
		current := func(l api.Logs) string {
			if multi {
				if len(l.Steps) != 2 || l.Steps[0].Stdout != "first\n" {
					return "?"
				}
				return l.Steps[1].Stdout
			}
			return l.Stdout
		}
		var logs api.Logs
		for range 1000 { // 采集输出的协程可能还没读到:按次数重试,不按时间等
			if logs, err = d.engine.Logs(r.ID); err == nil && current(logs) == "started\n" {
				break
			}
			runtime.Gosched()
		}
		if current(logs) != "started\n" {
			t.Fatalf("%s 运行中: %+v %v", tk.Name, logs, err)
		}
		release2()
		d.Wait()
		if logs, err = d.engine.Logs(r.ID); err != nil || current(logs) != "started\nfinished\n" {
			t.Fatalf("%s 结束后: %+v %v", tk.Name, logs, err)
		}
	}
}

func TestG6LiveTailKeepsTheEnd(t *testing.T) {
	short := "一行\n"
	if liveTail(short) != short {
		t.Fatal("短的输出应原样返回")
	}
	long := strings.Repeat("前面的内容\n", liveMax/10) + "最后一行\n"
	got := liveTail(long)
	head, rest, _ := strings.Cut(got, "\n")
	if !strings.HasPrefix(head, "[bugu:") || !strings.HasSuffix(got, "最后一行\n") || len(rest) > liveMax || !strings.HasPrefix(rest, "前面的内容\n") {
		t.Fatalf("head=%q len=%d", head, len(rest))
	}
}
