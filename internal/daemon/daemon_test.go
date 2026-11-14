//go:build darwin || linux

package daemon

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"bugu/internal/api"
	"bugu/internal/clock"
	"bugu/internal/notify"
	"bugu/internal/store"
	"bugu/internal/task"
)

// 2026-11-11 01:00 UTC,周三。
var epoch = time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC)

func at(day, hour, minute int) time.Time {
	return time.Date(2026, 11, day, hour, minute, 0, 0, time.UTC)
}

func start(t *testing.T, home string, f *clock.Fake) *Daemon {
	t.Helper()
	d, err := Start(Options{Home: home, Listen: "127.0.0.1:0", Clock: f})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func command(name, cron, cmd string) task.Task {
	return task.Task{
		Name:    name,
		Trigger: task.Trigger{Cron: cron},
		Steps:   []task.Step{{Type: task.StepScript, Script: task.Script{Command: cmd}}},
	}
}

func create(t *testing.T, d *Daemon, tk task.Task) {
	t.Helper()
	if _, err := d.engine.CreateTask(tk); err != nil {
		t.Fatal(err)
	}
}

// advance 把时钟拨到 to,评估一次并等触发的运行结束。
func advance(d *Daemon, f *clock.Fake, to time.Time) {
	f.Set(to)
	d.engine.tick()
	d.Wait()
}

// settle 让电脑醒来后连续醒着一会儿:时钟每 30 秒走一步并评估一次,醒满补跑要等的时间后,等运行结束。
func settle(d *Daemon, f *clock.Fake) {
	for range int(settleAfterWake / (30 * time.Second)) {
		f.Advance(30 * time.Second)
		d.engine.tick()
	}
	d.Wait()
}

func runs(t *testing.T, d *Daemon, name string) []task.Run {
	t.Helper()
	list, err := d.engine.Runs(name, 0)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func code(err error) string {
	var e *api.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// 场景四:每晚 2 点执行备份脚本,结果有记录可查;脚本失败时记录为失败并保留输出和退出码。
// S1/O1: 到点触发,存档包含状态、起止时间、命令、退出码和输出。
// S7: 同一个时间点不会触发第二次。
func TestScenario4S1O1S7NightlyScript(t *testing.T) {
	f := clock.NewFake(epoch)
	d := start(t, t.TempDir(), f)
	create(t, d, command("backup", "0 2 * * *", "echo backing up; echo warn >&2"))
	create(t, d, command("broken", "0 2 * * *", "echo oops >&2; exit 3"))

	if info, _ := d.engine.Task("backup"); !info.NextRun.Equal(at(11, 2, 0)) || info.Running {
		t.Fatalf("下次运行时间 = %v", info.NextRun)
	}
	advance(d, f, at(11, 1, 59))
	if n := len(runs(t, d, "")); n != 0 {
		t.Fatalf("时间没到就有 %d 次运行", n)
	}

	advance(d, f, at(11, 2, 0))
	ok := runs(t, d, "backup")
	if len(ok) != 1 {
		t.Fatalf("backup 运行了 %d 次", len(ok))
	}
	r := ok[0]
	if r.Status != task.Succeeded || r.Trigger != task.BySchedule || !r.ScheduledFor.Equal(at(11, 2, 0)) ||
		r.ExitCode == nil || *r.ExitCode != 0 || !r.StartedAt.Equal(at(11, 2, 0)) || !r.FinishedAt.Equal(at(11, 2, 0)) ||
		len(r.Command) != 3 || r.Error != "" {
		t.Fatalf("run=%+v", r)
	}
	if logs, err := d.engine.Logs(r.ID); err != nil || logs.Stdout != "backing up\n" || logs.Stderr != "warn\n" {
		t.Fatalf("logs=%+v err=%v", logs, err)
	}

	bad := runs(t, d, "broken")
	if len(bad) != 1 || bad[0].Status != task.Failed || bad[0].ExitCode == nil || *bad[0].ExitCode != 3 || bad[0].Error == "" {
		t.Fatalf("broken=%+v", bad)
	}
	if logs, _ := d.engine.Logs(bad[0].ID); logs.Stderr != "oops\n" {
		t.Fatalf("logs=%+v", logs)
	}

	advance(d, f, at(11, 2, 0).Add(30*time.Second))
	advance(d, f, at(11, 23, 0))
	if n := len(runs(t, d, "backup")); n != 1 {
		t.Fatalf("同一个时间点触发了 %d 次", n)
	}
	advance(d, f, at(12, 2, 0))
	if n := len(runs(t, d, "backup")); n != 2 {
		t.Fatalf("第二天应再运行一次,共 %d 次", n)
	}
}

// S3: 后台自己的循环按短周期醒来并触发,不需要外部推动。
func TestS3LoopFiresOnItsOwn(t *testing.T) {
	f := clock.NewFake(epoch)
	ticked := make(chan struct{}, 8)
	d, err := Start(Options{Home: t.TempDir(), Listen: "127.0.0.1:0", Clock: f, afterTick: func() { ticked <- struct{}{} }})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	create(t, d, command("backup", "0 2 * * *", "true"))
	f.BlockUntilWaiters(1) // 循环已经在等下一次评估
	f.Set(at(11, 2, 0))
	<-ticked
	d.Wait()
	if list := runs(t, d, "backup"); len(list) != 1 || list[0].Status != task.Succeeded {
		t.Fatalf("runs=%+v", list)
	}
}

// S7: 重启后不重复触发已处理过的时间点。
// S3/S4: 停机或睡眠跨过多个时间点,恢复后只补跑最近一次,并注明错过了几次。
func TestS3S4S7RestartAndSleep(t *testing.T) {
	home := t.TempDir()
	f := clock.NewFake(epoch)
	d := start(t, home, f)
	create(t, d, command("backup", "0 2 * * *", "true"))
	advance(d, f, at(11, 2, 0))
	d.Close()

	f.Set(at(11, 2, 5))
	d = start(t, home, f) // 启动时会评估一次
	d.Wait()
	if n := len(runs(t, d, "backup")); n != 1 {
		t.Fatalf("重启后重复触发,共 %d 次", n)
	}
	d.Close()

	// 停机三天:错过 12、13、14 日的 02:00。启动后连续醒着一会儿才补跑(决策记录 0016)。
	f.Set(at(14, 9, 0))
	d = start(t, home, f)
	d.Wait()
	if n := len(runs(t, d, "backup")); n != 1 {
		t.Fatalf("刚启动就补跑了,共 %d 次", n)
	}
	settle(d, f)
	list := runs(t, d, "backup")
	if len(list) != 2 || !list[0].ScheduledFor.Equal(at(14, 2, 0)) || !strings.Contains(list[0].Note, "错过 2 次") {
		t.Fatalf("runs=%+v", list)
	}

	// 不停机、只是睡眠(时钟直接跳变)也一样。
	advance(d, f, at(16, 9, 0))
	settle(d, f)
	list = runs(t, d, "backup")
	if len(list) != 3 || !list[0].ScheduledFor.Equal(at(16, 2, 0)) || !strings.Contains(list[0].Note, "错过 1 次") {
		t.Fatalf("runs=%+v", list)
	}
}

// S7: 调度进度文件坏了(断电时写到一半)后台照样启动:坏文件移开留作查看,
// 任务从启动时开始计时,不会因为没有进度就把过去的触发都补一遍,之后按时触发。
func TestS7BrokenStateDoesNotBlockStart(t *testing.T) {
	home := t.TempDir()
	f := clock.NewFake(epoch)
	d := start(t, home, f)
	create(t, d, command("backup", "0 2 * * *", "true"))
	d.Close()
	path := filepath.Join(home, "state.json")
	if err := os.WriteFile(path, []byte(`{"backup": "2026-11`), 0o600); err != nil {
		t.Fatal(err)
	}

	f.Set(at(14, 9, 0))
	d = start(t, home, f)
	settle(d, f)
	if n := len(runs(t, d, "backup")); n != 0 {
		t.Fatalf("没有进度时不应补跑: %d 次", n)
	}
	if b, err := os.ReadFile(path + ".broken"); err != nil || string(b) != `{"backup": "2026-11` {
		t.Fatalf("坏文件应留作查看: %q %v", b, err)
	}
	advance(d, f, at(15, 2, 0))
	if n := len(runs(t, d, "backup")); n != 1 {
		t.Fatalf("之后应按时触发: %d 次", n)
	}
}

// fifo 返回一个命名管道。读它的命令会一直阻塞,直到测试调用 release。
func fifo(t *testing.T) (path string, release func()) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "gate")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, func() {
		w, err := os.OpenFile(path, os.O_WRONLY, 0) // 阻塞到命令打开读端,所以不需要等待或轮询
		if err != nil {
			t.Error(err)
			return
		}
		w.Close()
	}
}

// S5: 上一次还在运行时,新的定时触发跳过并留下记录,不排队;手动触发得到明确的冲突错误。
func TestS5OverlappingTriggerIsSkipped(t *testing.T) {
	f := clock.NewFake(epoch)
	d := start(t, t.TempDir(), f)
	gate, release := fifo(t)
	create(t, d, command("slow", "0 * * * *", "cat "+gate+"; echo finished"))

	f.Set(at(11, 2, 0))
	d.engine.tick()
	if info, _ := d.engine.Task("slow"); !info.Running {
		t.Fatal("任务应显示为运行中")
	}
	f.Set(at(11, 3, 0))
	d.engine.tick()
	if _, err := d.engine.RunNow("slow", ""); code(err) != api.CodeConflict {
		t.Fatalf("运行中再手动触发: err=%v", err)
	}

	list := runs(t, d, "slow")
	if len(list) != 2 || list[0].Status != task.Skipped || !list[0].ScheduledFor.Equal(at(11, 3, 0)) ||
		!strings.Contains(list[0].Note, list[1].ID) || list[1].Status != task.Running {
		t.Fatalf("runs=%+v", list)
	}

	release()
	d.Wait()
	list = runs(t, d, "slow")
	if len(list) != 2 || list[1].Status != task.Succeeded {
		t.Fatalf("runs=%+v", list)
	}
	if logs, _ := d.engine.Logs(list[1].ID); logs.Stdout != "finished\n" {
		t.Fatalf("logs=%+v", logs)
	}
	if info, _ := d.engine.Task("slow"); info.Running {
		t.Fatal("结束后不应再显示为运行中")
	}
}

// E3: 超过总时长上限的运行被终止并记为超时。
func TestE3TimeoutIsRecorded(t *testing.T) {
	f := clock.NewFake(epoch)
	d := start(t, t.TempDir(), f)
	gate, _ := fifo(t)
	tk := command("stuck", "0 2 * * *", "cat "+gate)
	tk.Timeout = task.Duration(time.Hour)
	create(t, d, tk)

	f.Set(at(11, 2, 0))
	d.engine.tick()
	f.BlockUntilWaiters(2) // 后台循环的定时器,加上这次运行的超时定时器
	f.Set(at(11, 3, 0))
	d.Wait()
	list := runs(t, d, "stuck")
	if len(list) != 1 || list[0].Status != task.TimedOut || list[0].ExitCode != nil || !list[0].FinishedAt.Equal(at(11, 3, 0)) {
		t.Fatalf("runs=%+v", list)
	}
}

// O3: 后台关闭时仍在运行的任务被终止并记为中断;
// 上一个后台没来得及收尾的"运行中"记录,在下次启动时标为中断。
func TestO3InterruptedOnShutdownAndAfterCrash(t *testing.T) {
	home := t.TempDir()
	f := clock.NewFake(epoch)
	d := start(t, home, f)
	gate, _ := fifo(t)
	create(t, d, command("slow", "0 2 * * *", "cat "+gate))
	f.Set(at(11, 2, 0))
	d.engine.tick()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListRuns("slow", 0)
	if len(list) != 1 || list[0].Status != task.Interrupted || list[0].Error == "" {
		t.Fatalf("runs=%+v", list)
	}

	// 模拟崩溃:留下一条没有收尾的记录。
	id, err := st.NewRunID(at(11, 4, 0))
	if err != nil {
		t.Fatal(err)
	}
	crashed := task.Run{ID: id, Task: "slow", Status: task.Running, CreatedAt: at(11, 4, 0)}
	if err := st.CreateRun(crashed, task.Task{Name: "slow"}); err != nil {
		t.Fatal(err)
	}
	f.Set(at(11, 5, 0))
	d = start(t, home, f)
	got, err := d.engine.Run(crashed.ID)
	if err != nil || got.Status != task.Interrupted || !got.FinishedAt.Equal(at(11, 5, 0)) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

// S8: 暂停期间不触发,恢复后不补跑暂停期间的时间点;手动运行不受暂停影响。
func TestS8PauseResumeAndRunNow(t *testing.T) {
	f := clock.NewFake(epoch)
	d := start(t, t.TempDir(), f)
	create(t, d, command("backup", "0 2 * * *", "true"))

	if info, err := d.engine.SetPaused("backup", true); err != nil || !info.Paused || !info.NextRun.IsZero() {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	advance(d, f, at(11, 2, 0))
	if n := len(runs(t, d, "backup")); n != 0 {
		t.Fatalf("暂停期间运行了 %d 次", n)
	}

	r, err := d.engine.RunNow("backup", "")
	d.Wait()
	if err != nil || r.Trigger != task.ByManual || r.Status != task.Running {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	if got, _ := d.engine.Run(r.ID); got.Status != task.Succeeded {
		t.Fatalf("got=%+v", got)
	}

	f.Set(at(11, 2, 30))
	if info, _ := d.engine.SetPaused("backup", false); info.Paused || !info.NextRun.Equal(at(12, 2, 0)) {
		t.Fatalf("info=%+v", info)
	}
	advance(d, f, at(11, 3, 0))
	if n := len(runs(t, d, "backup")); n != 1 {
		t.Fatalf("恢复后补跑了暂停期间的时间点,共 %d 次", n)
	}
	advance(d, f, at(12, 2, 0))
	if n := len(runs(t, d, "backup")); n != 2 {
		t.Fatalf("恢复后的下一个时间点没有触发,共 %d 次", n)
	}
}

// C8: 重复提交同一个任务定义是安全的;带同一个幂等标识重试触发,只运行一次。
func TestC8CreateAndRunAreIdempotent(t *testing.T) {
	home := t.TempDir()
	f := clock.NewFake(epoch)
	d := start(t, home, f)
	tk := command("backup", "0 2 * * *", "true")
	create(t, d, tk)
	if _, err := d.engine.CreateTask(tk); err != nil {
		t.Fatalf("重复提交相同定义应成功: %v", err)
	}
	if _, err := d.engine.CreateTask(command("backup", "0 3 * * *", "true")); code(err) != api.CodeConflict {
		t.Fatalf("同名不同定义应冲突: %v", err)
	}

	first, err := d.engine.RunNow("backup", "key-1")
	d.Wait()
	if err != nil {
		t.Fatal(err)
	}
	again, err := d.engine.RunNow("backup", "key-1")
	if err != nil || again.ID != first.ID || again.Status != task.Succeeded {
		t.Fatalf("again=%+v err=%v", again, err)
	}
	d.Close()
	d = start(t, home, f) // 重启后仍然认得这个标识
	if again, err = d.engine.RunNow("backup", "key-1"); err != nil || again.ID != first.ID {
		t.Fatalf("again=%+v err=%v", again, err)
	}
	if n := len(runs(t, d, "backup")); n != 1 {
		t.Fatalf("共运行 %d 次", n)
	}
	other, _ := d.engine.RunNow("backup", "key-2")
	d.Wait()
	if other.ID == first.ID {
		t.Fatal("不同的标识应是不同的运行")
	}
}

// 7.7: 定义有误时在创建阶段就被拒绝,错误说明指出问题所在;不存在的对象返回 not_found。
func TestInvalidDefinitionsAndMissingObjects(t *testing.T) {
	f := clock.NewFake(epoch)
	d := start(t, t.TempDir(), f)
	good := command("backup", "0 2 * * *", "true")
	create(t, d, good)

	bad := map[string]task.Task{
		"任务名":   command("bad name", "0 2 * * *", "true"),
		"第 1 段": command("x", "61 2 * * *", "true"),
		"二选一":   {Name: "x", Steps: good.Steps},
		"一个步骤":  {Name: "x", Trigger: good.Trigger},
		"已经过去":  {Name: "x", Trigger: task.Trigger{At: epoch.Add(-time.Hour)}, Steps: good.Steps},
		"步骤类型":  {Name: "x", Trigger: good.Trigger, Steps: []task.Step{{Type: "js"}}},
	}
	for hint, tk := range bad {
		_, err := d.engine.CreateTask(tk)
		if code(err) != api.CodeInvalid || !strings.Contains(err.Error(), hint) {
			t.Errorf("%s: err=%v", hint, err)
		}
	}
	if list, _ := d.engine.Tasks(); len(list) != 1 {
		t.Fatalf("无效的定义被保存了: %+v", list)
	}

	if _, err := d.engine.UpdateTask("backup", command("other", "0 2 * * *", "true")); code(err) != api.CodeInvalid {
		t.Errorf("改名应被拒绝: %v", err)
	}
	if _, err := d.engine.UpdateTask("backup", command("backup", "bad", "true")); code(err) != api.CodeInvalid {
		t.Errorf("无效的修改应被拒绝: %v", err)
	}
	for name, err := range map[string]error{
		"Task":      second(d.engine.Task("nope")),
		"Update":    second(d.engine.UpdateTask("nope", command("nope", "0 2 * * *", "true"))),
		"Delete":    d.engine.DeleteTask("nope"),
		"SetPaused": second(d.engine.SetPaused("nope", true)),
		"RunNow":    second(d.engine.RunNow("nope", "")),
		"Run":       second(d.engine.Run("nope")),
		"Logs":      second(d.engine.Logs("20261111T020000-000000")),
	} {
		if code(err) != api.CodeNotFound {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }

// 修改触发方式后从现在重新计时;删除任务后不再触发,历史记录保留。
func TestUpdateAndDelete(t *testing.T) {
	f := clock.NewFake(epoch)
	d := start(t, t.TempDir(), f)
	create(t, d, command("backup", "0 2 * * *", "echo v1"))
	advance(d, f, at(11, 2, 0))

	f.Set(at(11, 4, 0))
	info, err := d.engine.UpdateTask("backup", command("backup", "0 3 * * *", "echo v2"))
	if err != nil || !info.NextRun.Equal(at(12, 3, 0)) {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	advance(d, f, at(11, 5, 0))
	if n := len(runs(t, d, "backup")); n != 1 {
		t.Fatalf("修改触发方式后补跑了修改之前的时间点,共 %d 次", n)
	}
	advance(d, f, at(12, 3, 0))
	list := runs(t, d, "backup")
	if len(list) != 2 {
		t.Fatalf("runs=%+v", list)
	}
	if logs, _ := d.engine.Logs(list[0].ID); logs.Stdout != "v2\n" {
		t.Fatalf("logs=%+v", logs)
	}

	if err := d.engine.DeleteTask("backup"); err != nil {
		t.Fatal(err)
	}
	advance(d, f, at(13, 3, 0))
	if n := len(runs(t, d, "backup")); n != 2 {
		t.Fatalf("删除后仍在触发或历史丢失,共 %d 次", n)
	}
}

func get(t *testing.T, url, token string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// C1/C2/C3: 运行中的后台拒绝不带令牌的请求;令牌首次启动时生成在仅当前用户可读的文件里;
// 默认只监听本机。5.3: 同一个数据目录不能启动第二个后台。
func TestC1C2C3AuthAndSingleInstance(t *testing.T) {
	home := t.TempDir()
	d := start(t, home, clock.NewFake(epoch))
	url := "http://" + d.Addr() + api.Prefix + "/tasks"

	raw, err := os.ReadFile(filepath.Join(home, api.TokenFile))
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(raw))
	if info, _ := os.Stat(filepath.Join(home, api.TokenFile)); info.Mode().Perm() != 0o600 {
		t.Fatalf("令牌文件权限 = %v", info.Mode().Perm())
	}
	if got := get(t, url, ""); got != http.StatusUnauthorized {
		t.Fatalf("不带令牌: %d", got)
	}
	if got := get(t, url, "wrong"+token); got != http.StatusUnauthorized {
		t.Fatalf("错误的令牌: %d", got)
	}
	if got := get(t, url, token); got != http.StatusOK {
		t.Fatalf("正确的令牌: %d", got)
	}

	host, _, err := net.SplitHostPort(DefaultListen)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		t.Fatalf("默认监听地址 %q 不是本机地址", DefaultListen)
	}

	endpoint, err := os.ReadFile(filepath.Join(home, api.EndpointFile))
	if err != nil || !strings.Contains(string(endpoint), d.Addr()) {
		t.Fatalf("endpoint=%s err=%v", endpoint, err)
	}
	if _, err := Start(Options{Home: home, Listen: "127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), "已有一个后台") {
		t.Fatalf("第二个后台应启动失败: %v", err)
	}
	d.Close()
	if _, err := os.Stat(filepath.Join(home, api.EndpointFile)); !os.IsNotExist(err) {
		t.Fatalf("关闭后应移除地址文件: %v", err)
	}
	again := start(t, home, clock.NewFake(epoch))
	if raw, _ := os.ReadFile(filepath.Join(home, api.TokenFile)); strings.TrimSpace(string(raw)) != token {
		t.Fatal("重启后令牌变了")
	}
	again.Close()
}

// N1/N2/N5: 运行结束后,后台按任务的通知设置把系统通知交给界面。
// M2 的完成标准:建一个定时脚本,到点后收到通知。
func TestN1N2ScheduledRunNotifies(t *testing.T) {
	f := clock.NewFake(epoch)
	var mu sync.Mutex // 同一时刻触发的几个任务各自在自己的 goroutine 里结束
	var got []notify.Message
	d, err := Start(Options{Home: t.TempDir(), Listen: "127.0.0.1:0", Clock: f, Notify: func(m notify.Message) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, m)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	system := func(when string) []task.Notification {
		return []task.Notification{{Channel: task.ChannelSystem, When: when}}
	}
	report := command("report", "0 2 * * *", "echo 3 new items")
	report.Notifications = system("")
	quiet := command("quiet", "0 2 * * *", "true")
	quiet.Notifications = system("")
	broken := command("broken", "0 2 * * *", "echo disk full >&2; exit 3")
	broken.Notifications = system(task.NotifyOnFailure)
	silent := command("silent", "0 2 * * *", "echo ignored; exit 1") // 没有设置通知
	for _, tk := range []task.Task{report, quiet, broken, silent} {
		create(t, d, tk)
	}

	advance(d, f, at(11, 2, 0)) // Wait 返回时全部运行已结束,通知都已交出
	if len(got) != 2 {
		t.Fatalf("通知 = %+v", got)
	}
	byTask := map[string]notify.Message{}
	for _, m := range got {
		byTask[m.Task] = m
	}
	if m := byTask["report"]; m.Title != "report 完成" || !strings.HasPrefix(m.Body, "3 new items\n") || m.RunID != runs(t, d, "report")[0].ID {
		t.Errorf("report: %+v", m)
	}
	if m := byTask["broken"]; m.Title != "broken 失败(退出码 3)" || !strings.HasPrefix(m.Body, "disk full\n") {
		t.Errorf("broken: %+v", m)
	}
}
