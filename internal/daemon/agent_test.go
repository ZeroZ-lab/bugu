//go:build darwin || linux

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bugu/internal/api"
	"bugu/internal/clock"
	"bugu/internal/task"
)

// customRunner 在数据目录里放一份自定义运行器配置。这里验证的是后台的接线,
// 所以用一条 shell 命令充当 AI 命令行:它原样输出收到的提示词和参数。
func customRunner(t *testing.T, home, name, config string) {
	t.Helper()
	dir := filepath.Join(home, "runners")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

const echoRunner = `{
  "title": "Echo",
  "command": ["/bin/sh", "-c", "cat; echo; echo \"args: $*\"", "sh"],
  "prompt_via": "stdin",
  "permissions": {"read_only": ["--ro"], "write_cwd": ["--rw"], "full": ["--all"]},
  "max_permission": "write_cwd"
}`

func agent(name, cron, runnerName, prompt string) task.Task {
	return task.Task{
		Name:    name,
		Trigger: task.Trigger{Cron: cron},
		Steps:   []task.Step{{Type: task.StepAgent, Agent: task.Agent{Runner: runnerName, Prompt: prompt}}},
	}
}

// 场景一:工作日 9 点让 AI 命令行看新 issue。提示词里的 {last_run} 首次是任务的创建时间,
// 之后是上次成功运行的开始时间;默认只读,实际使用的级别和发出的提示词都留在存档里(T2、R3、R7、7.4)。
func TestScenario1AgentStepOnSchedule(t *testing.T) {
	home := t.TempDir()
	customRunner(t, home, "echo", echoRunner)
	f := clock.NewFake(epoch)
	d := start(t, home, f)
	create(t, d, agent("issues", "0 9 * * 1-5", "echo", "列出 {last_run} 之后的新 issue,只读,不要回复"))

	advance(d, f, at(11, 9, 0))
	advance(d, f, at(12, 9, 0))
	list := runs(t, d, "issues")
	if len(list) != 2 {
		t.Fatalf("应有两次运行: %+v", list)
	}
	for i, since := range []string{"2026-11-11T09:00:00Z", "2026-11-11T01:00:00Z"} { // 最新的在前
		r := list[i]
		if r.Status != task.Succeeded || r.Permission != task.PermReadOnly {
			t.Fatalf("运行 %d: %+v", i, r)
		}
		logs, err := d.engine.Logs(r.ID)
		want := "列出 " + since + " 之后的新 issue,只读,不要回复\nargs: --ro\n"
		if err != nil || logs.Stdout != want {
			t.Fatalf("运行 %d 的输出 %q,应为 %q(%v)", i, logs.Stdout, want, err)
		}
		saved, err := os.ReadFile(filepath.Join(home, "runs", r.ID, "prompt.txt"))
		if err != nil || !strings.Contains(string(saved), since) {
			t.Fatalf("存档里的提示词: %q, %v", saved, err)
		}
	}
}

// 决策记录 0008:放开权限要显式写出;超过运行器上限的在保存时被拒绝;修改任务不改变创建时间。
func TestR7PermissionIsExplicitAndCapped(t *testing.T) {
	home := t.TempDir()
	customRunner(t, home, "echo", echoRunner)
	f := clock.NewFake(epoch)
	d := start(t, home, f)

	tk := agent("fix", "0 9 * * *", "echo", "修正拼写")
	tk.Steps[0].Agent.Permission = task.PermFull
	if _, err := d.engine.CreateTask(tk); code(err) != api.CodeInvalid || !strings.Contains(err.Error(), "最高只允许") {
		t.Fatalf("超过上限: %v", err)
	}
	if _, err := d.engine.CreateTask(agent("x", "0 9 * * *", "missing", "p")); code(err) != api.CodeInvalid || !strings.Contains(err.Error(), "bugu runner list") {
		t.Fatalf("不存在的运行器: %v", err)
	}

	tk.Steps[0].Agent.Permission = ""
	create(t, d, tk)
	f.Set(at(11, 5, 0))
	tk.Steps[0].Agent.Permission = task.PermWriteCwd
	info, err := d.engine.UpdateTask("fix", tk)
	if err != nil || !info.CreatedAt.Equal(epoch) {
		t.Fatalf("修改任务: %+v, %v", info, err)
	}
	if _, err := d.engine.RunNow("fix", ""); err != nil {
		t.Fatal(err)
	}
	d.Wait()
	r := runs(t, d, "fix")[0]
	logs, _ := d.engine.Logs(r.ID)
	if r.Permission != task.PermWriteCwd || !strings.Contains(logs.Stdout, "args: --rw") || r.Note != "" {
		t.Fatalf("放开后的运行: %+v %q", r, logs.Stdout)
	}
}

// 决策记录 0008:只读运行失败时,运行记录提示可能需要放开权限;放开后的失败没有这条提示。
func TestR7ReadOnlyFailureCarriesHint(t *testing.T) {
	home := t.TempDir()
	customRunner(t, home, "deny", `{"command": ["/bin/sh", "-c", "echo permission denied >&2; exit 1"], "prompt_via": "stdin",
		"permissions": {"read_only": [], "write_cwd": []}}`)
	f := clock.NewFake(epoch)
	d := start(t, home, f)
	tk := agent("fix", "0 9 * * *", "deny", "修正拼写")
	create(t, d, tk)
	d.engine.RunNow("fix", "")
	d.Wait()
	if r := runs(t, d, "fix")[0]; r.Status != task.Failed || !strings.Contains(r.Note, "只读权限") {
		t.Fatalf("只读失败: %+v", r)
	}
	tk.Steps[0].Agent.Permission = task.PermWriteCwd
	if _, err := d.engine.UpdateTask("fix", tk); err != nil {
		t.Fatal(err)
	}
	d.engine.RunNow("fix", "")
	d.Wait()
	if r := runs(t, d, "fix")[0]; r.Status != task.Failed || r.Note != "" {
		t.Fatalf("放开后的失败: %+v", r)
	}
}

// R2 R3 R8: 接口列出预设和自定义运行器及各自可选的权限级别;检查能区分可用和不可用。
func TestR2R3R8RunnersListAndCheck(t *testing.T) {
	home := t.TempDir()
	customRunner(t, home, "echo", echoRunner)
	customRunner(t, home, "gone", `{"command": ["bugu-test-no-such-command"], "prompt_via": "arg"}`)
	customRunner(t, home, "plain", `{"command": ["/bin/sh"]}`)
	customRunner(t, home, "broken", `{`)
	d := start(t, home, clock.NewFake(epoch))

	list, err := d.engine.Runners()
	if err != nil || len(list) != 6 {
		t.Fatalf("应有 3 份预设加 3 份可用的自定义配置: %+v, %v", list, err)
	}
	byName := map[string]api.Runner{}
	presets := 0
	for _, r := range list {
		byName[r.Name] = r
		if r.Preset {
			presets++
		}
	}
	if e := byName["echo"]; presets != 3 || e.Preset || !e.Agent || e.Title != "Echo" ||
		strings.Join(e.Permissions, ",") != "read_only,write_cwd" {
		t.Fatalf("echo=%+v presets=%d", e, presets)
	}
	if p := byName["plain"]; p.Agent || p.Permissions == nil {
		t.Fatalf("plain=%+v", p)
	}

	if c, err := d.engine.CheckRunner("echo"); err != nil || !c.OK {
		t.Fatalf("echo: %+v, %v", c, err)
	}
	if c, err := d.engine.CheckRunner("gone"); err != nil || c.OK || !strings.Contains(c.Detail, "bugu-test-no-such-command") {
		t.Fatalf("gone: %+v, %v", c, err)
	}
	if _, err := d.engine.CheckRunner("missing"); code(err) != api.CodeNotFound {
		t.Fatalf("missing: %v", err)
	}
	if _, err := d.engine.CheckRunner("broken"); code(err) != api.CodeInvalid {
		t.Fatalf("broken: %v", err)
	}
}
