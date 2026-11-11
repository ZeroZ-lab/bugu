//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"bugu/internal/daemon"
)

// bugu 以命令行的方式调用一次,返回退出码、标准输出和标准错误。
func bugu(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := Main("test", args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// ok 调用一次并要求成功,返回标准输出。
func ok(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errOut := bugu(args...)
	if code != 0 {
		t.Fatalf("bugu %s: 退出码 %d\n%s%s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

// fails 调用一次并要求以指定的退出码失败,且标准错误包含 hint。
func fails(t *testing.T, want int, hint string, args ...string) {
	t.Helper()
	code, out, errOut := bugu(args...)
	if code != want || !strings.Contains(errOut, hint) {
		t.Errorf("bugu %s: 退出码 %d(期望 %d),标准错误 %q 应包含 %q\n%s", strings.Join(args, " "), code, want, errOut, hint, out)
	}
}

// backend 在临时数据目录上启动一个后台,并让命令行指向它。
func backend(t *testing.T) *daemon.Daemon {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("BUGU_HOME", home)
	// 评估间隔设得极长,后台在测试期间不会自行触发任何任务,结果不取决于测试在几点运行。
	d, err := daemon.Start(daemon.Options{Home: home, Listen: "127.0.0.1:0", Tick: 1 << 62})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

var runID = regexp.MustCompile(`运行编号 (\S+)`)

// run 立即运行任务并等它结束,返回运行编号。
func run(t *testing.T, d *daemon.Daemon, name string) string {
	t.Helper()
	m := runID.FindStringSubmatch(ok(t, "run", name))
	if m == nil {
		t.Fatal("输出里没有运行编号")
	}
	d.Wait()
	return m[1]
}

// 场景四在命令行下的完整流程:建一个每晚 2 点的备份任务,查看、试跑、看记录和输出;
// 脚本失败时记录为失败。对应 7.7 的 add、list、show、run、runs、logs。
func TestScenario4FromTheCommandLine(t *testing.T) {
	d := backend(t)
	if out := ok(t, "list"); !strings.Contains(out, "还没有任务") {
		t.Fatalf("list=%q", out)
	}

	out := ok(t, "add", "backup", "--schedule", "0 2 * * *", "--command", "echo backing up $TARGET; echo warn >&2",
		"--env", "TARGET=/data", "--timeout", "30m")
	if !strings.Contains(out, "已创建任务 backup") || !strings.Contains(out, " 02:00") {
		t.Fatalf("add=%q", out)
	}
	ok(t, "add", "broken", "--command", "exit 3", "--schedule", "0 2 * * *") // 位置参数和选项的顺序不限

	list := ok(t, "list")
	for _, want := range []string{"NAME", "backup", "broken", "enabled", "0 2 * * *", " 02:00"} {
		if !strings.Contains(list, want) {
			t.Errorf("list 缺少 %q:\n%s", want, list)
		}
	}
	show := ok(t, "show", "backup")
	for _, want := range []string{`"name": "backup"`, `"cron": "0 2 * * *"`, `"timeout": "30m0s"`, `"TARGET": "/data"`} {
		if !strings.Contains(show, want) {
			t.Errorf("show 缺少 %s:\n%s", want, show)
		}
	}

	good, bad := run(t, d, "backup"), run(t, d, "broken")
	code, stdout, stderr := bugu("logs", good)
	if code != 0 || stdout != "backing up /data\n" || stderr != "warn\n" {
		t.Fatalf("logs: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, _, stderr := bugu("logs", bad); !strings.Contains(stderr, "没有输出") {
		t.Fatalf("logs=%q", stderr)
	}

	runs := ok(t, "runs")
	lines := strings.Split(strings.TrimSpace(runs), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], bad) || !strings.HasPrefix(lines[2], good) {
		t.Fatalf("runs 应最新的在前:\n%s", runs)
	}
	if f := strings.Fields(lines[1]); f[1] != "broken" || f[2] != "failed" || f[3] != "3" || f[4] != "manual" {
		t.Errorf("失败的运行: %q", lines[1])
	}
	if f := strings.Fields(lines[2]); f[1] != "backup" || f[2] != "succeeded" || f[3] != "0" {
		t.Errorf("成功的运行: %q", lines[2])
	}
	if only := ok(t, "runs", "backup", "-n", "1"); strings.Contains(only, "broken") || !strings.Contains(only, good) {
		t.Errorf("按任务筛选:\n%s", only)
	}
}

// 7.7: edit 只改给出的部分;pause、resume、rm;show 的输出可以原样交给 add --file。
func TestEditPauseResumeRemoveAndFile(t *testing.T) {
	d := backend(t)
	ok(t, "add", "backup", "--schedule", "0 2 * * *", "--command", "echo v1", "--env", "A=1")

	ok(t, "edit", "backup", "--command", "echo v2 $A $B", "--env", "B=2")
	show := ok(t, "show", "backup")
	if !strings.Contains(show, `"cron": "0 2 * * *"`) || !strings.Contains(show, `"A": "1"`) {
		t.Fatalf("edit 改动了没给出的部分:\n%s", show)
	}
	if _, stdout, _ := bugu("logs", run(t, d, "backup")); stdout != "v2 1 2\n" {
		t.Fatalf("修改后的输出 = %q", stdout)
	}
	if out := ok(t, "edit", "backup", "--schedule", "30 4 * * *"); !strings.Contains(out, " 04:30") {
		t.Fatalf("edit=%q", out)
	}

	ok(t, "pause", "backup")
	if list := ok(t, "list"); !strings.Contains(list, "paused") {
		t.Fatalf("list=\n%s", list)
	}
	if out := ok(t, "resume", "backup"); !strings.Contains(out, " 04:30") {
		t.Fatalf("resume=%q", out)
	}

	file := filepath.Join(t.TempDir(), "task.json")
	definition := strings.Replace(ok(t, "show", "backup"), `"name": "backup"`, `"name": "copy"`, 1)
	if err := os.WriteFile(file, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	ok(t, "add", "--file", file)
	if _, stdout, _ := bugu("logs", run(t, d, "copy")); stdout != "v2 1 2\n" {
		t.Fatalf("从文件创建的任务输出 = %q", stdout)
	}

	if out := ok(t, "rm", "backup"); !strings.Contains(out, "已删除") {
		t.Fatalf("rm=%q", out)
	}
	if list := ok(t, "list"); strings.Contains(list, "backup") || !strings.Contains(list, "copy") {
		t.Fatalf("list=\n%s", list)
	}
	if runs := ok(t, "runs", "backup"); !strings.Contains(runs, "succeeded") {
		t.Fatalf("删除任务后运行记录应保留:\n%s", runs)
	}
}

// 7.7: 出错时退出码非零,说明具体到能据此修正。用法错误的退出码是 2,其余是 1。
func TestErrorsAreSpecific(t *testing.T) {
	backend(t)
	ok(t, "add", "backup", "--schedule", "0 2 * * *", "--command", "true")

	fails(t, 2, "缺少任务名", "add", "--schedule", "0 2 * * *", "--command", "true")
	fails(t, 2, "缺少触发方式", "add", "x", "--command", "true")
	fails(t, 2, "缺少要执行的内容", "add", "x", "--schedule", "0 2 * * *")
	fails(t, 2, "只能给一个", "add", "x", "--schedule", "0 2 * * *", "--command", "true", "--path", "/bin/true")
	fails(t, 2, "只能给一个", "add", "x", "--schedule", "0 2 * * *", "--at", "2030-01-01 02:00", "--command", "true")
	fails(t, 2, "名=值", "add", "x", "--schedule", "0 2 * * *", "--command", "true", "--env", "NOVALUE")
	fails(t, 2, "--timeout", "add", "x", "--schedule", "0 2 * * *", "--command", "true", "--timeout", "soon")
	fails(t, 2, "--at", "add", "x", "--at", "tomorrow", "--command", "true")
	fails(t, 2, "not defined", "add", "x", "--bogus")
	fails(t, 2, "任务名", "show")
	fails(t, 2, "没有给出要修改的内容", "edit", "backup")

	fails(t, 1, "第 1 段", "add", "x", "--schedule", "61 2 * * *", "--command", "true")
	fails(t, 1, "任务名", "add", "bad name", "--schedule", "0 2 * * *", "--command", "true")
	fails(t, 1, "已经过去", "add", "x", "--at", "2020-01-01 02:00", "--command", "true")
	fails(t, 1, "定义不同", "add", "backup", "--schedule", "0 3 * * *", "--command", "true")
	for _, cmd := range []string{"show", "rm", "run", "pause", "resume"} {
		fails(t, 1, `没有名为 "nope" 的任务`, cmd, "nope")
	}
	fails(t, 1, `没有名为 "nope" 的任务`, "edit", "nope", "--command", "true")
	fails(t, 1, "没有编号为", "logs", "nope")

	// C8: 重复提交同一个定义是安全的。
	ok(t, "add", "backup", "--schedule", "0 2 * * *", "--command", "true")
}

// 后台没有运行时,命令给出明确的提示而不是连接错误。
func TestCommandsNeedARunningDaemon(t *testing.T) {
	t.Setenv("BUGU_HOME", t.TempDir())
	for _, args := range [][]string{{"list"}, {"add", "x", "--schedule", "0 2 * * *", "--command", "true"}, {"runs"}} {
		fails(t, 1, "后台没有在运行,请先执行 bugu serve", args...)
	}
}

// 5.1: bugu serve 只运行后台。启动后命令行能连上;收到停止信号后收尾退出,命令行不再能连上。
func TestServeRunsUntilStopped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BUGU_HOME", home)
	ctx, stop := context.WithCancel(context.Background())
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, &env{stdout: pw, stderr: pw, home: home}, "127.0.0.1:0")
		pw.Close()
	}()
	// 启动完成后才会打印这一行,读到它就说明后台已经可用。
	line := make([]byte, 256)
	n, err := pr.Read(line)
	if err != nil || !strings.Contains(string(line[:n]), "后台已启动,监听 http://127.0.0.1:") {
		t.Fatalf("启动信息 = %q err=%v", line[:n], err)
	}
	ok(t, "add", "backup", "--schedule", "0 2 * * *", "--command", "true")

	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	fails(t, 1, "后台没有在运行", "list")
}
