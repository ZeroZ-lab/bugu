//go:build darwin || linux

package exec

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"bugu/internal/clock"
	"bugu/internal/runner"
	"bugu/internal/task"
)

func sh(name, command string) task.Step {
	return task.Step{Name: name, Type: task.StepScript, Script: task.Script{Command: command}}
}

func text(name, s string) task.Step { return task.Step{Name: name, Type: task.StepText, Text: s} }

// T4 T5 T8: 文本步骤原样输出;上一步的输出经标准输入和 {prev} 交给下一步,
// 前面任一步的输出可用 {steps.<名字>.output} 引用;每一步的输出分别存档,整次运行的结果是最后一步的输出。
func TestT4T5T8StepsPassOutputForward(t *testing.T) {
	rn := runner.Runner{Name: "fake", Command: []string{agent}, PromptVia: runner.ViaArg}
	e := Executor{Clock: clock.NewFake(epoch), Runners: lookup(rn)}
	runDir := t.TempDir()
	input := task.Task{Name: "triage", Steps: []task.Step{
		sh("检查", "echo 'issue #1'; echo 'issue #2'"),
		text("要求", "按类型分类 {date}"),
		sh("", "cat; echo 上一步={prev}"),
		{Name: "分析", Type: task.StepAgent, Agent: task.Agent{Runner: "fake", Prompt: "{steps.要求.output}|{steps.检查.output}"}},
	}}
	r, err := e.Run(context.Background(), input, Info{RunDir: runDir})
	if err != nil || r.Status != task.Succeeded || len(r.Steps) != 4 {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if want := "fakeagent: 按类型分类 2026-11-11|issue #1\nissue #2\n\n"; r.Stdout != want {
		t.Fatalf("stdout=%q want %q", r.Stdout, want)
	}
	// 文本步骤后面的脚本从标准输入读到文本,{prev} 也是它。
	want := []string{"issue #1\nissue #2\n", "按类型分类 2026-11-11", "按类型分类 2026-11-11上一步=按类型分类 2026-11-11\n", r.Stdout}
	for i, w := range want {
		stdout, _, err := StepOutput(runDir, i)
		if err != nil || stdout != w || r.Steps[i].Status != task.Succeeded {
			t.Errorf("第 %d 步: 存档=%q 状态=%s err=%v,期望 %q", i+1, stdout, r.Steps[i].Status, err, w)
		}
	}
	if r.Steps[0].Name != "检查" || r.Steps[3].Type != task.StepAgent || r.Command[0] != agent {
		t.Fatalf("steps=%+v command=%v", r.Steps, r.Command)
	}
}

// T5: 命令里代入的步骤输出不会被 shell 解释,不论 {prev} 写在引号里还是外面。
func TestT5OutputInCommandIsNotInterpretedByShell(t *testing.T) {
	nasty := `a'b"c $(touch pwned); echo x` + "`id`"
	dir := t.TempDir()
	for _, command := range []string{`printf %s {prev}`, `printf %s "{prev}"`, `printf %s '{prev}'`, `printf %s "前缀 {prev} 后缀"`} {
		input := task.Task{Name: "x", Cwd: dir, Steps: []task.Step{text("", nasty), sh("", command)}}
		r, err := (Executor{Clock: clock.NewFake(epoch)}).Run(context.Background(), input, Info{})
		want := nasty
		if strings.Contains(command, "前缀") {
			want = "前缀 " + nasty + " 后缀"
		}
		if err != nil || r.Stdout != want {
			t.Errorf("%s: stdout=%q err=%v,期望 %q", command, r.Stdout, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("输出里的命令被执行了")
	}
}

// T5: AI 命令行步骤的提示词没有引用任何步骤输出时,上一步的输出附在提示词后面。
func TestT5AgentGetsPreviousOutputAppended(t *testing.T) {
	rn := runner.Runner{Name: "in", Command: []string{agent}, PromptVia: runner.ViaStdin}
	e := Executor{Clock: clock.NewFake(epoch), Runners: lookup(rn)}
	input := task.Task{Name: "x", Steps: []task.Step{sh("", "echo 三条新闻"), {Type: task.StepAgent, Agent: task.Agent{Runner: "in", Prompt: "总结一下"}}}}
	r, err := e.Run(context.Background(), input, Info{})
	if err != nil || r.Stdout != "fakeagent: 总结一下\n\n三条新闻\n" {
		t.Fatalf("stdout=%q err=%v", r.Stdout, err)
	}
}

// T6: 某一步失败即停止,后面的步骤不执行,运行记录标明停在哪一步。
func TestT6FailedStepStopsTheRun(t *testing.T) {
	dir := t.TempDir()
	input := task.Task{Name: "x", Cwd: dir, Steps: []task.Step{sh("取数", "echo ok"), sh("整理", "echo boom >&2; exit 4"), sh("发送", "touch sent")}}
	r, err := (Executor{Clock: clock.NewFake(epoch)}).Run(context.Background(), input, Info{})
	if err == nil || !strings.Contains(err.Error(), "第 2 步(整理)") || r.Status != task.Failed || *r.ExitCode != 4 {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	got := []task.Status{r.Steps[0].Status, r.Steps[1].Status, r.Steps[2].Status}
	if got[0] != task.Succeeded || got[1] != task.Failed || got[2] != task.Skipped || r.Steps[1].Error == "" {
		t.Fatalf("steps=%+v", r.Steps)
	}
	if r.Stderr != "[bugu: 第 2 步(整理)]\nboom\n" {
		t.Fatalf("stderr=%q", r.Stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "sent")); err == nil {
		t.Fatal("失败之后的步骤被执行了")
	}
}

// T7: 脚本以约定的退出码声明无事可做:后面的步骤不执行,运行成功但无内容。
func TestT7NothingToDoEndsEarly(t *testing.T) {
	dir := t.TempDir()
	input := task.Task{Name: "x", Cwd: dir, Steps: []task.Step{
		sh("检查", fmt.Sprintf("echo 没有新 issue; exit %d", task.ExitNothingToDo)),
		sh("分析", "touch ran"),
	}}
	r, err := (Executor{Clock: clock.NewFake(epoch)}).Run(context.Background(), input, Info{})
	if err != nil || r.Status != task.Succeeded || !r.NoContent || !r.Steps[0].NothingToDo || r.Steps[1].Status != task.Skipped {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Fatal("无事可做之后的步骤被执行了")
	}
	// 只有脚本步骤用这个约定,AI 命令行步骤以同样的退出码结束仍是失败。
	rn := runner.Runner{Name: "fake", Command: []string{agent}, PromptVia: runner.ViaArg}
	input = agentTask("fake", "hi")
	input.Env = map[string]string{"FAKEAGENT_EXIT": fmt.Sprint(task.ExitNothingToDo)}
	if r, err := (Executor{Clock: clock.NewFake(epoch), Runners: lookup(rn)}).Run(context.Background(), input, Info{}); err == nil || r.Status != task.Failed || r.NoContent {
		t.Fatalf("agent: result=%+v err=%v", r, err)
	}
}

// O4: 成功且结果包含静默标记时记为无内容;失败的运行不受影响。
func TestO4SilentMarker(t *testing.T) {
	for _, tt := range []struct {
		command   string
		status    task.Status
		noContent bool
	}{
		{"echo NOTHING_NEW", task.Succeeded, true},
		{"echo 两条新闻", task.Succeeded, false},
		{"echo NOTHING_NEW; exit 1", task.Failed, false},
	} {
		input := task.Task{Name: "x", SilentIf: "NOTHING_NEW", Steps: []task.Step{sh("", tt.command)}}
		r, _ := (Executor{Clock: clock.NewFake(epoch)}).Run(context.Background(), input, Info{})
		if r.Status != tt.status || r.NoContent != tt.noContent {
			t.Errorf("%s: status=%s noContent=%v", tt.command, r.Status, r.NoContent)
		}
	}
}

// 7.4: {state_dir} 和 {run_dir} 在命令里展开。
func TestStateDirVariable(t *testing.T) {
	state, runDir := t.TempDir(), t.TempDir()
	input := task.Task{Name: "x", Steps: []task.Step{sh("", "echo seen > {state_dir}/seen.txt; echo {run_dir}")}}
	r, err := (Executor{Clock: clock.NewFake(epoch)}).Run(context.Background(), input, Info{RunDir: runDir, StateDir: state})
	if err != nil || r.Stdout != runDir+"\n" {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if b, err := os.ReadFile(filepath.Join(state, "seen.txt")); err != nil || string(b) != "seen\n" {
		t.Fatalf("state=%q err=%v", b, err)
	}
}

// T5 T7 与 7.4: 定义有误的多步骤任务在保存时就被拒绝,说明指出是哪一步。
func TestT5ValidateStepReferences(t *testing.T) {
	for want, steps := range map[string][]task.Step{
		"至少要有一个步骤":    nil,
		"第一个步骤没有上一步":  {sh("", "echo {prev}")},
		"第 1 步(a):":   {sh("a", "echo {steps.b.output}"), sh("b", "true")},
		"不在这一步之前":     {sh("a", "echo {steps.a.output}"), sh("b", "true")},
		"重复":          {sh("a", "true"), sh("a", "true")},
		"不能包含花括号":     {sh("{a}", "true")},
		"文本步骤的内容不能为空": {text("", " ")},
		"第 2 步:":      {sh("", "true"), {Type: "js"}},
		"步骤的 timeout": {{Type: task.StepScript, Script: task.Script{Command: "true"}, Timeout: -1}},
	} {
		err := Validate(task.Task{Steps: steps}, nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err=%v", want, err)
		}
	}
	ok := []task.Step{sh("a", "true"), text("", "{steps.a.output} {prev} {\"json\": 1}")}
	if err := Validate(task.Task{Steps: ok}, nil); err != nil {
		t.Fatal(err)
	}
}

// T8 E3: 每一步可以有自己的超时;任务的总时长上限按剩余时间约束后面的步骤。
// 第一步在总时长过去 45 分钟时结束,第二步只剩 15 分钟。
func TestT8E3StepTimeoutWithinTaskBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ready")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	fifo, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer fifo.Close()
	lines := make(chan string, 2)
	go func() {
		in := bufio.NewReader(fifo)
		for {
			line, err := in.ReadString('\n')
			if err != nil {
				return
			}
			lines <- line
		}
	}()

	input := task.Task{Name: "x", Timeout: task.Duration(time.Hour),
		Env:   map[string]string{"FAKEAGENT_HANG": "1", "FAKEAGENT_READY": path},
		Steps: []task.Step{{Type: task.StepScript, Script: task.Script{Path: agent}}, {Type: task.StepScript, Script: task.Script{Path: agent}, Timeout: task.Duration(30 * time.Minute)}},
	}
	f := clock.NewFake(epoch)
	done := make(chan outcome, 1)
	go func() {
		r, err := (Executor{Clock: f}).Run(context.Background(), input, Info{})
		done <- outcome{r, err}
	}()

	var pid, child int
	if _, err := fmt.Sscan(<-lines, &pid, &child); err != nil {
		t.Fatal(err)
	}
	f.Advance(45 * time.Minute)
	if err := syscall.Kill(pid, syscall.SIGINT); err != nil { // 假 agent 收到中断后正常退出
		t.Fatal(err)
	}
	<-lines // 第二步就绪
	f.Advance(15 * time.Minute)
	got := <-done
	if got.err == nil || got.run.Status != task.TimedOut || got.run.Steps[0].Status != task.Succeeded || got.run.Steps[1].Status != task.TimedOut {
		t.Fatalf("result=%+v err=%v", got.run, got.err)
	}
	if !got.run.FinishedAt.Equal(epoch.Add(time.Hour)) || !got.run.Steps[1].StartedAt.Equal(epoch.Add(45*time.Minute)) {
		t.Fatalf("steps=%+v", got.run.Steps)
	}
}

// T9: 每一步开始之前报告各步骤的状态:前面的已有结果,这一步运行中,后面的排队中。
func TestT9ProgressBeforeEachStep(t *testing.T) {
	var seen [][]task.Status
	info := Info{Progress: func(steps []task.StepRun) {
		var row []task.Status
		for _, s := range steps {
			row = append(row, s.Status)
		}
		seen = append(seen, row)
	}}
	input := task.Task{Name: "p", Steps: []task.Step{sh("", "echo a"), text("", "b"), sh("", "exit 75"), sh("", "echo d")}}
	if _, err := (Executor{Clock: clock.NewFake(epoch)}).Run(context.Background(), input, info); err != nil {
		t.Fatal(err)
	}
	want := [][]task.Status{
		{task.Running, task.Queued, task.Queued, task.Queued},
		{task.Succeeded, task.Running, task.Queued, task.Queued},
		{task.Succeeded, task.Succeeded, task.Running, task.Queued},
	}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("progress=%v\nwant %v", seen, want)
	}
}
