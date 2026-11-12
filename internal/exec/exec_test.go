//go:build darwin || linux

package exec

import (
	"bufio"
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"bugu/internal/clock"
	"bugu/internal/task"
)

var agent string

func TestMain(m *testing.M) {
	agent = os.Getenv("BUGU_TEST_FAKEAGENT")
	if agent != "" {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "bugu-exec-test-")
	if err != nil {
		panic(err)
	}
	agent = filepath.Join(dir, "fakeagent")
	cmd := osexec.Command("go", "build", "-o", agent, "../fakeagent")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		panic(fmt.Sprintf("build fixture: %v: %s", err, out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

var epoch = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

func script(path string) task.Task {
	return task.Task{Steps: []task.Step{{Type: task.StepScript, Script: task.Script{Path: path}}}}
}

func TestT1T3RejectUnsupportedTasks(t *testing.T) {
	for _, input := range []task.Task{
		{}, {Steps: []task.Step{{Type: "text"}}},
		{Steps: []task.Step{{Type: task.StepScript}, {Type: task.StepScript}}},
		{Steps: []task.Step{{Type: task.StepScript}}},
		{Steps: []task.Step{{Type: task.StepScript, Script: task.Script{Path: agent, Command: "false"}}}},
	} {
		r, err := (Executor{Clock: clock.NewFake(epoch)}).Run(context.Background(), input, Info{})
		if err == nil || r.Status != task.Failed || r.ExitCode != nil || !r.StartedAt.IsZero() {
			t.Fatalf("result=%+v error=%v", r, err)
		}
	}
}

func TestT3E2O1CommandCwdEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUGU_EXEC_TEST", "inherited")
	input := task.Task{Cwd: dir, Env: map[string]string{"BUGU_EXEC_TEST": "overridden", "BUGU_ADDED": "value"}, Steps: []task.Step{{Type: task.StepScript, Script: task.Script{Command: "printf '%s|%s|%s' \"$PWD\" \"$BUGU_EXEC_TEST\" \"$BUGU_ADDED\"; printf error >&2; exit 7"}}}}
	f := clock.NewFake(epoch)
	r, err := (Executor{Clock: f}).Run(context.Background(), input, Info{})
	if err == nil || r.Status != task.Failed || r.ExitCode == nil || *r.ExitCode != 7 {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	physicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(r.Stdout, "|")
	actualDir := ""
	if len(parts) == 3 {
		actualDir, err = filepath.EvalSymlinks(parts[0])
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(parts) != 3 || actualDir != physicalDir || parts[1] != "overridden" || parts[2] != "value" || r.Stderr != "error" {
		t.Fatalf("result=%+v", r)
	}
	if !reflect.DeepEqual(r.Command, []string{"/bin/sh", "-c", input.Steps[0].Script.Command}) || !r.StartedAt.Equal(epoch) || !r.FinishedAt.Equal(epoch) {
		t.Fatalf("metadata=%+v", r)
	}
}

func TestT3O1PathWithSpacesAndLargeCompleteOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "script with spaces")
	if err := os.Symlink(agent, path); err != nil {
		t.Fatal(err)
	}
	input := script(path)
	input.Env = map[string]string{"FAKEAGENT_OUTPUT": strings.Repeat("o", 1023), "FAKEAGENT_STDERR": strings.Repeat("e", 1023), "FAKEAGENT_REPEAT": "256"}
	input.Timeout = task.Duration(time.Hour)
	f := clock.NewFake(epoch)
	r, err := (Executor{Clock: f}).Run(context.Background(), input, Info{})
	if err != nil || r.Status != task.Succeeded || r.ExitCode == nil || *r.ExitCode != 0 {
		t.Fatalf("status=%s err=%v", r.Status, err)
	}
	if r.Stdout != strings.Repeat(input.Env["FAKEAGENT_OUTPUT"]+"\n", 256) || r.Stderr != strings.Repeat(input.Env["FAKEAGENT_STDERR"]+"\n", 256) {
		t.Fatal("output truncated")
	}
	if !reflect.DeepEqual(r.Command, []string{path}) || f.Waiters() != 0 {
		t.Fatalf("command=%v timers=%d", r.Command, f.Waiters())
	}
}

func TestE5ClosedInputAndO1StartFailure(t *testing.T) {
	input := script(agent)
	input.Env = map[string]string{"FAKEAGENT_ASK": "1"}
	r, err := (Executor{Clock: clock.NewFake(epoch)}).Run(context.Background(), input, Info{})
	if err == nil || r.ExitCode == nil || *r.ExitCode != 3 || !strings.Contains(r.Stderr, "无法确认") {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	input = script(filepath.Join(t.TempDir(), "missing"))
	r, err = (Executor{Clock: clock.NewFake(epoch)}).Run(context.Background(), input, Info{})
	if err == nil || r.ExitCode != nil || r.Status != task.Failed {
		t.Fatalf("result=%+v err=%v", r, err)
	}
}

type outcome struct {
	run task.Run
	err error
}

// ready 使用 FIFO 事件握手，不轮询 PID 或文件，不以真实耗时判定通过。
func ready(t *testing.T) (string, <-chan string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ready")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	ch := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(f).ReadString('\n'); ch <- line }()
	return path, ch
}

func TestE3O3TimeoutCancelAndParentExitCleanDescendants(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel", "parent_exit"} {
		t.Run(mode, func(t *testing.T) {
			path, readyCh := ready(t)
			input := script(agent)
			input.Env = map[string]string{"FAKEAGENT_HANG": "1", "FAKEAGENT_CHILD": "1", "FAKEAGENT_READY": path}
			if mode == "parent_exit" {
				input.Env["FAKEAGENT_PARENT_EXIT"] = "1"
			}
			input.Timeout = task.Duration(time.Hour)
			f := clock.NewFake(epoch)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan outcome, 1)
			go func() { r, err := (Executor{Clock: f}).Run(ctx, input, Info{}); done <- outcome{r, err} }()
			var got outcome
			received := false
			t.Cleanup(func() {
				cancel()
				if !received {
					<-done
				}
			})
			select {
			case line := <-readyCh:
				var parent, child int
				if _, err := fmt.Sscan(line, &parent, &child); err != nil || parent == 0 || child == 0 {
					t.Fatalf("bad ready: %q", line)
				}
			case got = <-done:
				received = true
				if mode != "parent_exit" {
					t.Fatalf("exited before handshake: %+v %v", got.run, got.err)
				}
				line := <-readyCh
				var parent, child int
				if _, err := fmt.Sscan(line, &parent, &child); err != nil || parent == 0 || child == 0 {
					t.Fatalf("bad ready: %q", line)
				}
			}
			switch mode {
			case "timeout":
				f.Advance(time.Hour)
			case "cancel":
				cancel()
			}
			if !received {
				got = <-done
				received = true
			}
			want := map[string]task.Status{"timeout": task.TimedOut, "cancel": task.Canceled, "parent_exit": task.Succeeded}[mode]
			if got.run.Status != want || (got.err == nil) != (mode == "parent_exit") {
				t.Fatalf("status=%s err=%v", got.run.Status, got.err)
			}
			// 假时钟没有推进到宽限期，所以返回只可能是两条输出管道都读到了 EOF，
			// 即持有管道的子进程已被杀掉。进程组清理失效时这里会一直等待而不是通过。
			if strings.Contains(got.run.Stderr, "deadlock") || !strings.Contains(got.run.Stdout, "fakeagent: child") || f.Waiters() != 0 {
				t.Fatalf("output=%q timers=%d", got.run.Stdout, f.Waiters())
			}
			if mode == "timeout" && !got.run.FinishedAt.Equal(epoch.Add(time.Hour)) {
				t.Fatalf("finished=%v", got.run.FinishedAt)
			}
		})
	}
}

func TestE3O3AlreadyCanceledDoesNotStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := (Executor{Clock: clock.NewFake(epoch)}).Run(ctx, script(agent), Info{})
	if err != context.Canceled || r.Status != task.Canceled || !r.StartedAt.IsZero() {
		t.Fatalf("result=%+v err=%v", r, err)
	}
}

func TestT3E2RelativeFilePathAndHomeExpansion(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, "local-script")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf local"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	for _, scriptPath := range []string{"local-script", "~/work/local-script"} {
		input := script(scriptPath)
		input.Cwd = "~/work"
		r, err := (Executor{Clock: clock.NewFake(epoch)}).Run(context.Background(), input, Info{})
		if err != nil || r.Stdout != "local" || !reflect.DeepEqual(r.Command, []string{path}) {
			t.Fatalf("result=%+v err=%v", r, err)
		}
	}
}

func TestE2InvalidEnvironmentFailsBeforeStarting(t *testing.T) {
	for key, value := range map[string]string{"": "x", "A=B": "x", "NUL\x00": "x", "VALUE": "a\x00b"} {
		input := script(agent)
		input.Env = map[string]string{key: value}
		r, err := (Executor{Clock: clock.NewFake(epoch)}).Run(context.Background(), input, Info{})
		if err == nil || !r.StartedAt.IsZero() || r.ExitCode != nil {
			t.Fatalf("result=%+v err=%v", r, err)
		}
	}
}

// E3/O1/O3: 脱离进程组的后代仍持有输出管道时，采集在宽限期后结束，
// 状态、退出码和结束时间保持父进程的真实结果。
func TestE3O1O3EscapedDescendantEndsCollectionAfterGrace(t *testing.T) {
	path, readyCh := ready(t)
	input := script(agent)
	input.Env = map[string]string{"FAKEAGENT_HANG": "1", "FAKEAGENT_CHILD": "1", "FAKEAGENT_CHILD_DETACH": "1", "FAKEAGENT_PARENT_EXIT": "1", "FAKEAGENT_READY": path}
	f := clock.NewFake(epoch)
	done := make(chan outcome, 1)
	go func() {
		r, err := (Executor{Clock: f}).Run(context.Background(), input, Info{})
		done <- outcome{r, err}
	}()
	var parent, child int
	if _, err := fmt.Sscan(<-readyCh, &parent, &child); err != nil || parent == 0 || child == 0 {
		t.Fatalf("bad ready: parent=%d child=%d err=%v", parent, child, err)
	}
	// 子进程不在被清理的进程组里，由测试负责收尾。
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	// 没有总时长上限，唯一的等待者就是宽限期定时器：执行器已回收父进程并在等输出。
	f.BlockUntilWaiters(1)
	select {
	case got := <-done:
		t.Fatalf("宽限期未到就返回: %+v %v", got.run, got.err)
	default:
	}
	f.Advance(outputGrace)
	got := <-done
	if got.err != nil || got.run.Status != task.Succeeded || got.run.ExitCode == nil || *got.run.ExitCode != 0 {
		t.Fatalf("result=%+v err=%v", got.run, got.err)
	}
	if !strings.Contains(got.run.Stdout, "fakeagent: child") || !got.run.FinishedAt.Equal(epoch) || f.Waiters() != 0 {
		t.Fatalf("stdout=%q finished=%v timers=%d", got.run.Stdout, got.run.FinishedAt, f.Waiters())
	}
}

// O2: 输出超过上限时保留开头和结尾,并说明省略了多少。
func TestO2LargeOutputKeepsHeadAndTail(t *testing.T) {
	input := script(agent)
	line := strings.Repeat("x", 99)
	input.Env = map[string]string{"FAKEAGENT_OUTPUT": line, "FAKEAGENT_REPEAT": "1000"}
	r, err := (Executor{Clock: clock.NewFake(epoch), MaxOutput: 1000}).Run(context.Background(), input, Info{})
	if err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat(line+"\n", 1000)
	if !strings.HasPrefix(r.Stdout, full[:500]) || !strings.HasSuffix(r.Stdout, full[len(full)-500:]) {
		t.Fatalf("开头或结尾没有保留: %q", r.Stdout)
	}
	// 结尾最多攒到一个上限,所以总长不超过上限的 1.5 倍加上说明行。
	if !strings.Contains(r.Stdout, "[bugu: 输出过大,此处省略 ") || len(r.Stdout) > 1700 {
		t.Fatalf("len=%d", len(r.Stdout))
	}
}

func TestO2CaptureAccountsForEveryByte(t *testing.T) {
	c := capture{limit: 10}
	total := 0
	for i := range 50 {
		n, _ := c.Write([]byte(strings.Repeat("a", i%7+1)))
		total += n
	}
	if got := len(c.head) + len(c.tail) + int(c.dropped); got != total {
		t.Fatalf("保留加省略 = %d,写入 = %d", got, total)
	}
}
