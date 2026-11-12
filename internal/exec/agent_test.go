//go:build darwin || linux

package exec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"bugu/internal/clock"
	"bugu/internal/runner"
	"bugu/internal/task"
)

// lookup 返回只认识给定运行器的查找函数。
func lookup(runners ...runner.Runner) Lookup {
	return func(name string) (runner.Runner, error) {
		for _, r := range runners {
			if r.Name == name {
				return r, nil
			}
		}
		return runner.Runner{}, runner.ErrNotFound
	}
}

func agentTask(runnerName, prompt string) task.Task {
	return task.Task{Name: "triage", Steps: []task.Step{{Type: task.StepAgent, Agent: task.Agent{Runner: runnerName, Prompt: prompt}}}}
}

// fakeLoginShell 生成一个假的登录 shell:它模拟启动文件设置环境(把 bin 放进 PATH,
// 设置 FROM_LOGIN 和 BUGU_T),然后照常执行 -c 后面的内容。测试因此不依赖本机真实的 shell 配置。
func fakeLoginShell(t *testing.T, bin string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "zsh")
	body := fmt.Sprintf(`#!/bin/sh
[ "$1" = "-l" ] || exit 90
[ "$2" = "-c" ] || exit 91
export FROM_LOGIN=1 BUGU_T=from-profile PATH="%s:$PATH"
script=$3
shift 3
exec /bin/sh -c "$script" "$@"
`, bin)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// T2 R4 R7: AI 命令行步骤按运行器配置启动;提示词里的模板变量已展开;默认以只读级别运行,
// 对应的参数传给了命令,实际使用的级别记在运行结果里。
func TestT2R4R7AgentStepRunsThroughRunner(t *testing.T) {
	rn := runner.Runner{Name: "fake", Command: []string{agent}, PromptVia: runner.ViaArg, Permissions: map[string][]string{
		task.PermReadOnly: {"--ro"}, task.PermWriteCwd: {"--rw"},
	}}
	e := Executor{Clock: clock.NewFake(epoch), Runners: lookup(rn)}
	input := agentTask("fake", "issues of {task_name} since {last_run} on {date}")
	input.CreatedAt = epoch.Add(-24 * time.Hour)

	r, err := e.Run(context.Background(), input, Info{})
	if err != nil || r.Status != task.Succeeded || r.Permission != task.PermReadOnly {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if want := "fakeagent: --ro issues of triage since 2026-11-10T09:00:00Z on 2026-11-11\n"; r.Stdout != want {
		t.Fatalf("stdout=%q want %q", r.Stdout, want)
	}

	// {last_run} 有上次成功运行时取它的开始时间;显式放开权限后换成对应的参数。
	input.Steps[0].Agent.Permission = task.PermWriteCwd
	r, err = e.Run(context.Background(), input, Info{LastRun: epoch.Add(-time.Hour)})
	if err != nil || r.Permission != task.PermWriteCwd || !strings.HasPrefix(r.Stdout, "fakeagent: --rw issues of triage since 2026-11-11T08:00:00Z") {
		t.Fatalf("result=%+v err=%v", r, err)
	}
}

// R4: 提示词经标准输入或文件传递,并保存在运行存档目录里。
func TestR4PromptViaStdinAndFile(t *testing.T) {
	stdin := runner.Runner{Name: "in", Command: []string{agent}, PromptVia: runner.ViaStdin}
	file := runner.Runner{Name: "file", Command: []string{"/bin/sh", "-c", `printf 'file: '; cat "$1"`, "sh", "{prompt_file}"}, PromptVia: runner.ViaFile}
	e := Executor{Clock: clock.NewFake(epoch), Runners: lookup(stdin, file)}

	runDir := t.TempDir()
	r, err := e.Run(context.Background(), agentTask("in", "第一行\n{date} 第二行"), Info{RunDir: runDir})
	if err != nil || r.Stdout != "fakeagent: 第一行\n2026-11-11 第二行\n" || r.Permission != "" {
		t.Fatalf("stdin: result=%+v err=%v", r, err)
	}
	saved, err := os.ReadFile(filepath.Join(runDir, "prompt.txt"))
	if err != nil || string(saved) != "第一行\n2026-11-11 第二行" {
		t.Fatalf("存档里的提示词: %q, %v", saved, err)
	}

	// 没有存档目录时也能传递,用完即删。
	r, err = e.Run(context.Background(), agentTask("file", "hello"), Info{})
	if err != nil || r.Stdout != "file: hello" {
		t.Fatalf("file: result=%+v err=%v", r, err)
	}
	if strings.HasPrefix(r.Command[len(r.Command)-1], runDir) {
		t.Fatalf("没有存档目录时不应写入别处的存档: %v", r.Command)
	}
	if _, err := os.Stat(r.Command[len(r.Command)-1]); !os.IsNotExist(err) {
		t.Fatalf("临时的提示词文件应已删除: %v", err)
	}
}

// R7 与决策记录 0008: 保存任务时就拒绝运行器不支持或超过上限的权限级别,以及不存在的运行器。
func TestR7ValidateRejectsBadAgentSteps(t *testing.T) {
	limited := runner.Runner{Name: "limited", Command: []string{agent}, PromptVia: runner.ViaArg, MaxPermission: task.PermReadOnly,
		Permissions: map[string][]string{task.PermReadOnly: nil, task.PermWriteCwd: nil}}
	plain := runner.Runner{Name: "plain", Command: []string{agent}}
	runners := lookup(limited, plain)

	ok := agentTask("limited", "p")
	if err := Validate(ok, runners); err != nil {
		t.Fatal(err)
	}
	over := agentTask("limited", "p")
	over.Steps[0].Agent.Permission = task.PermWriteCwd
	for name, bad := range map[string]task.Task{
		"超过上限":    over,
		"不存在的运行器": agentTask("missing", "p"),
		"没有运行器":   agentTask("", "p"),
		"空提示词":    agentTask("limited", " \n"),
		"不能用于 AI": agentTask("plain", "p"),
	} {
		if err := Validate(bad, runners); err == nil {
			t.Errorf("%s: 应被拒绝", name)
		}
	}
	if err := Validate(ok, nil); err == nil {
		t.Error("没有配置运行器的执行器不能接受 AI 命令行步骤")
	}
	// 运行时同样检查:配置在任务保存之后被收紧时,不会按旧的级别执行。
	r, err := (Executor{Clock: clock.NewFake(epoch), Runners: runners}).Run(context.Background(), over, Info{})
	if err == nil || r.Status != task.Failed || !r.StartedAt.IsZero() {
		t.Fatalf("result=%+v err=%v", r, err)
	}
}

// E6: 工作目录不存在、运行器的命令找不到时不启动,本次记为失败并说明原因。
func TestE6PrecheckFailsWithReason(t *testing.T) {
	rn := runner.Runner{Name: "fake", Command: []string{agent}, PromptVia: runner.ViaArg}
	gone := runner.Runner{Name: "gone", Command: []string{"bugu-test-no-such-command"}, PromptVia: runner.ViaArg}
	e := Executor{Clock: clock.NewFake(epoch), Runners: lookup(rn, gone)}

	input := agentTask("fake", "p")
	input.Cwd = filepath.Join(t.TempDir(), "missing")
	r, err := e.Run(context.Background(), input, Info{})
	if err == nil || r.Status != task.Failed || !r.StartedAt.IsZero() || !strings.Contains(err.Error(), "工作目录") {
		t.Fatalf("工作目录: result=%+v err=%v", r, err)
	}

	r, err = e.Run(context.Background(), agentTask("gone", "p"), Info{})
	if err == nil || r.Status != task.Failed || !r.StartedAt.IsZero() || !strings.Contains(err.Error(), "bugu-test-no-such-command") {
		t.Fatalf("命令: result=%+v err=%v", r, err)
	}
}

// E1: 子进程经登录 shell 启动,拿到 shell 启动文件设置的环境;任务自己的环境变量仍然优先;
// 只在登录 shell 的 PATH 里才有的运行器能通过预检并运行,退出码照常传回。
func TestE1E2E6LoginShell(t *testing.T) {
	bin := t.TempDir()
	if err := os.Symlink(agent, filepath.Join(bin, "bugu-test-agent")); err != nil {
		t.Fatal(err)
	}
	here := runner.Runner{Name: "here", Command: []string{"bugu-test-agent"}, PromptVia: runner.ViaArg}
	gone := runner.Runner{Name: "gone", Command: []string{"bugu-test-no-such-command"}, PromptVia: runner.ViaArg}
	e := Executor{Clock: clock.NewFake(epoch), Runners: lookup(here, gone), LoginShell: fakeLoginShell(t, bin)}

	input := task.Task{Env: map[string]string{"BUGU_T": "from task"}, Steps: []task.Step{{Type: task.StepScript, Script: task.Script{
		Command: `printf '%s|%s' "$FROM_LOGIN" "$BUGU_T"; exit 7`,
	}}}}
	r, err := e.Run(context.Background(), input, Info{})
	if err == nil || r.Stdout != "1|from task" || r.ExitCode == nil || *r.ExitCode != 7 {
		t.Fatalf("脚本: result=%+v err=%v", r, err)
	}
	if !reflect.DeepEqual(r.Command[:2], []string{"/bin/sh", "-c"}) {
		t.Fatalf("记录的命令应是步骤本身的命令,不含登录 shell: %v", r.Command)
	}

	r, err = e.Run(context.Background(), agentTask("here", "hello"), Info{})
	if err != nil || r.Stdout != "fakeagent: hello\n" {
		t.Fatalf("运行器: result=%+v err=%v", r, err)
	}

	r, err = e.Run(context.Background(), agentTask("gone", "hello"), Info{})
	if err == nil || !r.StartedAt.IsZero() || !strings.Contains(err.Error(), "登录 shell") {
		t.Fatalf("预检: result=%+v err=%v", r, err)
	}
}

// E5 E9: AI 命令行要求人工确认而无法交互时,本次运行以失败结束,不会挂住。
func TestE5E9AgentAskingForConfirmationFails(t *testing.T) {
	rn := runner.Runner{Name: "fake", Command: []string{agent}, PromptVia: runner.ViaArg}
	input := agentTask("fake", "p")
	input.Env = map[string]string{"FAKEAGENT_ASK": "1"}
	r, err := (Executor{Clock: clock.NewFake(epoch), Runners: lookup(rn)}).Run(context.Background(), input, Info{})
	if err == nil || r.Status != task.Failed || r.ExitCode == nil || *r.ExitCode != 3 {
		t.Fatalf("result=%+v err=%v", r, err)
	}
}

// R6: 退出码为 0 但不满足运行器的成功条件时记为失败,并说明原因。
func TestR6SuccessConditionFailsRun(t *testing.T) {
	rn := runner.Runner{Name: "fake", Command: []string{agent}, PromptVia: runner.ViaArg, Success: runner.Success{Contains: "DONE"}}
	e := Executor{Clock: clock.NewFake(epoch), Runners: lookup(rn)}
	r, err := e.Run(context.Background(), agentTask("fake", "p"), Info{})
	if err == nil || r.Status != task.Failed || r.ExitCode == nil || *r.ExitCode != 0 || !strings.Contains(err.Error(), "DONE") {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	r, err = e.Run(context.Background(), agentTask("fake", "all DONE"), Info{})
	if err != nil || r.Status != task.Succeeded {
		t.Fatalf("result=%+v err=%v", r, err)
	}
}

// R8: 运行器检查报告命令是否存在、检查命令能否正常结束。
func TestR8RunnerCheck(t *testing.T) {
	bin := t.TempDir()
	if err := os.Symlink(agent, filepath.Join(bin, "bugu-test-agent")); err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"", fakeLoginShell(t, bin)} {
		e := Executor{Clock: clock.NewFake(epoch), LoginShell: shell}
		ok, detail := e.Check(context.Background(), runner.Runner{Name: "a", Command: []string{agent}, Check: []string{agent, "1.2.3"}})
		if !ok || detail != "fakeagent: 1.2.3" {
			t.Errorf("shell=%q 可用的运行器: %v %q", shell, ok, detail)
		}
		ok, detail = e.Check(context.Background(), runner.Runner{Name: "a", Command: []string{agent}})
		if !ok || detail == "" {
			t.Errorf("shell=%q 没有检查命令时只看命令是否存在: %v %q", shell, ok, detail)
		}
		ok, detail = e.Check(context.Background(), runner.Runner{Name: "a", Command: []string{"bugu-test-no-such-command"}})
		if ok || !strings.Contains(detail, "bugu-test-no-such-command") {
			t.Errorf("shell=%q 没有安装: %v %q", shell, ok, detail)
		}
		ok, detail = e.Check(context.Background(), runner.Runner{Name: "a", Command: []string{agent}, Check: []string{"/bin/sh", "-c", "echo 未登录 >&2; exit 4"}})
		if ok || !strings.Contains(detail, "退出码 4") || !strings.Contains(detail, "未登录") {
			t.Errorf("shell=%q 无法启动: %v %q", shell, ok, detail)
		}
	}
	// 只在登录 shell 的 PATH 里的命令,经登录 shell 才找得到。
	rn := runner.Runner{Name: "a", Command: []string{"bugu-test-agent"}}
	if ok, _ := (Executor{Clock: clock.NewFake(epoch)}).Check(context.Background(), rn); ok {
		t.Error("不经登录 shell 时不应找到")
	}
	if ok, detail := (Executor{Clock: clock.NewFake(epoch), LoginShell: fakeLoginShell(t, bin)}).Check(context.Background(), rn); !ok {
		t.Errorf("经登录 shell 应能找到: %s", detail)
	}
}
