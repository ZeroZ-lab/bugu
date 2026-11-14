//go:build darwin || linux

package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"bugu/internal/task"
)

var epoch = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newID(t *testing.T, s *Store, now time.Time) string {
	t.Helper()
	id, err := s.NewRunID(now)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// 运行编号严格递增:同一秒内的多次运行、重新打开数据目录、时钟回拨都不打乱"最新的在前"。
func TestRunIDsAlwaysIncrease(t *testing.T) {
	s := open(t)
	var ids []string
	add := func(s *Store, now time.Time) {
		id := newID(t, s, now)
		if err := s.CreateRun(task.Run{ID: id, Task: "a", Status: task.Succeeded}, backup("a")); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	add(s, epoch)
	add(s, epoch)
	add(s, epoch.Add(500*time.Millisecond))
	reopened, err := Open(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	add(reopened, epoch)                 // 重启后仍在同一秒
	add(reopened, epoch.Add(-time.Hour)) // 时钟被拨回
	add(reopened, epoch.Add(time.Second))
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("编号没有递增: %v", ids)
		}
	}
	runs, err := reopened.ListRuns("", 0)
	if err != nil || len(runs) != len(ids) || runs[0].ID != ids[len(ids)-1] || runs[len(runs)-1].ID != ids[0] {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
}

func backup(name string) task.Task {
	return task.Task{
		Name:    name,
		Trigger: task.Trigger{Cron: "0 2 * * *"},
		Steps:   []task.Step{{Type: task.StepScript, Script: task.Script{Command: "true"}}},
	}
}

// T1: 一个任务一个文件,保存后能读回、列出和删除。
func TestT1TasksSaveListDelete(t *testing.T) {
	s := open(t)
	for _, name := range []string{"b", "a"} {
		if err := s.SaveTask(backup(name)); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListTasks()
	if err != nil || len(list) != 2 || list[0].Name != "a" || list[1].Name != "b" {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	if got, err := s.LoadTask("a"); err != nil || got.Trigger.Cron != "0 2 * * *" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if err := s.DeleteTask("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadTask("a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后仍能读到: %v", err)
	}
	if err := s.DeleteTask("a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除应报不存在: %v", err)
	}
}

// 任务名和运行编号来自接口请求,不能被用来访问数据目录以外的文件。
func TestNamesCannotEscapeDataDirectory(t *testing.T) {
	s := open(t)
	if err := s.SaveTask(backup("../evil")); err == nil {
		t.Fatal("带路径的任务名应被拒绝")
	}
	if _, err := s.LoadTask("../state"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
	if _, err := s.LoadRun("../../tasks"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
	if _, _, err := s.LoadOutput(".."); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

// O1/O11: 运行存档包含元数据、完整输出和当时的配置快照;之后修改任务不影响快照。
func TestO1O11RunArchiveAndSnapshot(t *testing.T) {
	s := open(t)
	original := backup("backup")
	if err := s.SaveTask(original); err != nil {
		t.Fatal(err)
	}
	r := task.Run{ID: newID(t, s, epoch), Task: "backup", Status: task.Running, Trigger: task.BySchedule, CreatedAt: epoch}
	if err := s.CreateRun(r, original); err != nil {
		t.Fatal(err)
	}
	if out, errOut, err := s.LoadOutput(r.ID); err != nil || out != "" || errOut != "" {
		t.Fatalf("运行中应返回空输出: %q %q %v", out, errOut, err)
	}

	edited := original
	edited.Steps = []task.Step{{Type: task.StepScript, Script: task.Script{Command: "false"}}}
	if err := s.SaveTask(edited); err != nil {
		t.Fatal(err)
	}

	code := 7
	r.Status, r.ExitCode, r.Command = task.Failed, &code, []string{"/bin/sh", "-c", "true"}
	r.StartedAt, r.FinishedAt = epoch, epoch.Add(time.Minute)
	r.Stdout, r.Stderr = "out\n", "err\n"
	if err := s.FinishRun(r); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoadRun(r.ID)
	if err != nil || got.Status != task.Failed || got.ExitCode == nil || *got.ExitCode != 7 ||
		!got.FinishedAt.Equal(r.FinishedAt) || len(got.Command) != 3 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	out, errOut, err := s.LoadOutput(r.ID)
	if err != nil || out != "out\n" || errOut != "err\n" {
		t.Fatalf("out=%q errOut=%q err=%v", out, errOut, err)
	}
	snap, err := s.Snapshot(r.ID)
	if err != nil || snap.Steps[0].Script.Command != "true" {
		t.Fatalf("快照被后来的修改影响: %+v %v", snap, err)
	}
}

func TestListRunsNewestFirstWithFilterAndLimit(t *testing.T) {
	s := open(t)
	var ids []string
	for i, name := range []string{"a", "b", "a", "a"} {
		at := epoch.Add(time.Duration(i) * time.Hour)
		r := task.Run{ID: newID(t, s, at), Task: name, Status: task.Succeeded, CreatedAt: at}
		if err := s.CreateRun(r, backup(name)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	all, err := s.ListRuns("", 0)
	if err != nil || len(all) != 4 || all[0].ID != ids[3] || all[3].ID != ids[0] {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	a, err := s.ListRuns("a", 2)
	if err != nil || len(a) != 2 || a[0].ID != ids[3] || a[1].ID != ids[2] {
		t.Fatalf("a=%+v err=%v", a, err)
	}
}

// 运行记录的内存索引与磁盘一致:建立之后的新建、结束、改写都能查到,
// 重新打开数据目录时从磁盘重建;不是运行编号的目录和没写完元数据的目录被跳过。
func TestRunIndexFollowsWrites(t *testing.T) {
	s := open(t)
	early := task.Run{ID: newID(t, s, epoch), Task: "a", Status: task.Succeeded, Summary: "旧的"}
	if err := s.CreateRun(early, backup("a")); err != nil {
		t.Fatal(err)
	}
	for _, junk := range []string{"notes", newID(t, s, epoch)} { // 后者模拟建了目录就退出
		if err := os.Mkdir(filepath.Join(s.dir, "runs", junk), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	entries := func(s *Store) []RunEntry {
		t.Helper()
		var list []RunEntry
		if err := s.EachRun(func(e RunEntry) bool { list = append(list, e); return true }); err != nil {
			t.Fatal(err)
		}
		return list
	}
	if got := entries(s); len(got) != 1 || !got[0].InInbox || !got[0].Unread {
		t.Fatalf("建立索引: %+v", got)
	}

	late := task.Run{ID: newID(t, s, epoch.Add(time.Hour)), Task: "b", Status: task.Running, StartedAt: epoch}
	if err := s.CreateRun(late, backup("b")); err != nil {
		t.Fatal(err)
	}
	if got := entries(s); len(got) != 2 || got[0].ID != late.ID || got[0].Status != task.Running || got[0].InInbox {
		t.Fatalf("新建之后: %+v", got)
	}
	late.Status, late.Error = task.Failed, "坏了"
	if err := s.FinishRun(late); err != nil {
		t.Fatal(err)
	}
	early.ReadAt = epoch
	if err := s.UpdateRun(early); err != nil {
		t.Fatal(err)
	}
	want := []RunEntry{
		{ID: late.ID, Task: "b", Status: task.Failed, StartedAt: epoch, InInbox: true, Unread: true},
		{ID: early.ID, Task: "a", Status: task.Succeeded, InInbox: true},
	}
	if got := entries(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("结束和标为已读之后: %+v", got)
	}
	reopened, err := Open(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := entries(reopened); !reflect.DeepEqual(got, want) {
		t.Fatalf("重新打开: %+v", got)
	}
}

// O3: 后台异常退出后,停留在运行中的记录在下次启动时标为中断;已结束的不受影响。
func TestO3InterruptUnfinishedRuns(t *testing.T) {
	s := open(t)
	running := task.Run{ID: newID(t, s, epoch), Task: "a", Status: task.Running, CreatedAt: epoch}
	done := task.Run{ID: newID(t, s, epoch.Add(time.Hour)), Task: "a", Status: task.Succeeded, CreatedAt: epoch}
	for _, r := range []task.Run{running, done} {
		if err := s.CreateRun(r, backup("a")); err != nil {
			t.Fatal(err)
		}
	}
	restart := epoch.Add(2 * time.Hour)
	if n, err := s.InterruptUnfinished(restart); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got, _ := s.LoadRun(running.ID)
	if got.Status != task.Interrupted || !got.FinishedAt.Equal(restart) || got.Error == "" {
		t.Fatalf("got=%+v", got)
	}
	if got, _ := s.LoadRun(done.ID); got.Status != task.Succeeded {
		t.Fatalf("已结束的记录被改动: %+v", got)
	}
}

// S7: 调度进度落盘,重新打开后仍在。
func TestS7StateSurvivesReopen(t *testing.T) {
	s := open(t)
	if state, err := s.LoadState(); err != nil || len(state) != 0 {
		t.Fatalf("state=%v err=%v", state, err)
	}
	if err := s.SaveState(map[string]time.Time{"backup": epoch}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := reopened.LoadState()
	if err != nil || !state["backup"].Equal(epoch) {
		t.Fatalf("state=%v err=%v", state, err)
	}
}

// C2: 凭证首次使用时生成,之后保持不变,文件和数据目录只有当前用户可读。
func TestC2SecretIsGeneratedOncePrivately(t *testing.T) {
	s := open(t)
	first, err := s.Secret("token")
	if err != nil || len(first) != 64 {
		t.Fatalf("first=%q err=%v", first, err)
	}
	if second, err := s.Secret("token"); err != nil || second != first {
		t.Fatalf("第二次读取得到不同的值: %v", err)
	}
	for path, want := range map[string]os.FileMode{filepath.Join(s.dir, "token"): 0o600, s.dir: 0o700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s 权限 = %v,期望 %v (%v)", path, info.Mode().Perm(), want, err)
		}
	}
	other, err := open(t).Secret("token")
	if err != nil || other == first {
		t.Fatalf("不同数据目录生成了相同的凭证: %v", err)
	}
}

// 5.3: 同一个数据目录只能被一个后台占用,释放后可再次占用。
func TestLockIsExclusive(t *testing.T) {
	s := open(t)
	unlock, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("第二次加锁应失败: %v", err)
	}
	unlock()
	again, err := s.Lock()
	if err != nil {
		t.Fatalf("释放后应能再次加锁: %v", err)
	}
	again()
}
