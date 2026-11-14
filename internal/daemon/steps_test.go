//go:build darwin || linux

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"bugu/internal/clock"
	"bugu/internal/notify"
	"bugu/internal/task"
)

// PRD 第 8 章的多步骤示例:脚本把门,文本给要求,AI 命令行分析。脚本用状态目录记住已看过的内容,
// 第二天没有新内容时声明无事可做:AI 步骤不运行,运行成功但无内容,不发通知。
// 覆盖 T1、T4、T5、T6、T7、T8、7.4 的 {state_dir}、N2。
func TestT5T7IssueTriageGatedByScript(t *testing.T) {
	home := t.TempDir()
	customRunner(t, home, "echo", echoRunner)
	f := clock.NewFake(epoch)
	var mu sync.Mutex
	var got []notify.Message
	d, err := Start(Options{Home: home, Listen: "127.0.0.1:0", Clock: f, Notify: func(m notify.Message) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, m)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	create(t, d, task.Task{
		Name:          "issue-triage",
		Trigger:       task.Trigger{Cron: "0 9 * * 1-5"},
		Notifications: []task.Notification{{Channel: task.ChannelSystem}},
		Steps: []task.Step{
			{Name: "检查", Type: task.StepScript, Script: task.Script{Command: "[ -f {state_dir}/seen ] && exit 75; touch {state_dir}/seen; echo 'issue #7 启动崩溃'"}},
			{Name: "要求", Type: task.StepText, Text: "按 bug / 需求 / 提问分类。"},
			{Name: "分析", Type: task.StepAgent, Agent: task.Agent{Runner: "echo", Prompt: "{steps.要求.output}\n新 issue:\n{steps.检查.output}"}},
		},
	})

	advance(d, f, at(11, 9, 0))
	r := runs(t, d, "issue-triage")[0]
	if r.Status != task.Succeeded || r.NoContent || len(r.Steps) != 3 || r.Steps[2].Status != task.Succeeded || r.Permission != task.PermReadOnly {
		t.Fatalf("第一天: %+v", r)
	}
	logs, err := d.engine.Logs(r.ID)
	if err != nil || len(logs.Steps) != 3 {
		t.Fatalf("logs=%+v err=%v", logs, err)
	}
	if want := "按 bug / 需求 / 提问分类。\n新 issue:\nissue #7 启动崩溃\n\nargs: --ro\n"; logs.Stdout != want || logs.Steps[2].Stdout != want {
		t.Fatalf("输出 %q,应为 %q", logs.Stdout, want)
	}
	if logs.Steps[0].Stdout != "issue #7 启动崩溃\n" || logs.Steps[1].Stdout != "按 bug / 需求 / 提问分类。" {
		t.Fatalf("各步输出: %+v", logs.Steps)
	}
	if len(got) != 1 || !strings.Contains(got[0].Body, "issue #7") {
		t.Fatalf("第一天的通知: %+v", got)
	}

	advance(d, f, at(12, 9, 0))
	r = runs(t, d, "issue-triage")[0]
	if r.Status != task.Succeeded || !r.NoContent || !r.Steps[0].NothingToDo || r.Steps[1].Status != task.Skipped || r.Steps[2].Status != task.Skipped {
		t.Fatalf("第二天: %+v", r)
	}
	if logs, _ := d.engine.Logs(r.ID); logs.Steps[2].Stdout != "" {
		t.Fatalf("没有执行的步骤不应有输出: %+v", logs.Steps)
	}
	if len(got) != 1 {
		t.Fatalf("无事可做时不应通知: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "issue-triage", "seen")); err != nil {
		t.Fatalf("状态目录: %v", err)
	}
}
