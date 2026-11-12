package notify

import (
	"strings"
	"testing"

	"bugu/internal/task"
)

func with(when string) task.Task {
	return task.Task{Name: "backup", Notifications: []task.Notification{{Channel: task.ChannelSystem, When: when}}}
}

func run(status task.Status, stdout, stderr string) task.Run {
	return task.Run{ID: "20261112T020000-000000", Task: "backup", Status: status, Stdout: stdout, Stderr: stderr}
}

// N2: 通知时机。失败和超时在三种时机下都通知;成功是否通知取决于时机和有没有输出;
// 没有真正执行的运行不通知。
func TestN2WhenToNotify(t *testing.T) {
	tests := []struct {
		when   string
		run    task.Run
		notify bool
	}{
		{task.NotifyOnFailure, run(task.Failed, "", ""), true},
		{task.NotifyOnFailure, run(task.TimedOut, "", ""), true},
		{task.NotifyOnFailure, run(task.Succeeded, "out", ""), false},
		{task.NotifyAlways, run(task.Succeeded, "", ""), true},
		{task.NotifyAlways, run(task.Failed, "", ""), true},
		{task.NotifyOnContent, run(task.Succeeded, "", ""), false},
		{task.NotifyOnContent, run(task.Succeeded, " \n", ""), false},
		{task.NotifyOnContent, run(task.Succeeded, "新内容", ""), true},
		{task.NotifyOnContent, run(task.Succeeded, "", "warn"), true},
		{task.NotifyOnContent, run(task.Failed, "", ""), true},
		{"", run(task.Succeeded, "新内容", ""), true}, // 没写时机时按"有内容时"
		{"", run(task.Succeeded, "", ""), false},
		{task.NotifyAlways, run(task.Skipped, "", ""), false},
		{task.NotifyAlways, run(task.Missed, "", ""), false},
		{task.NotifyAlways, run(task.Interrupted, "", ""), false},
		{task.NotifyAlways, run(task.Running, "", ""), false},
	}
	for _, tt := range tests {
		if _, got := System(with(tt.when), tt.run); got != tt.notify {
			t.Errorf("时机 %q 状态 %s 输出 %q: 通知=%v,期望 %v", tt.when, tt.run.Status, tt.run.Stdout+tt.run.Stderr, got, tt.notify)
		}
	}
	if _, got := System(task.Task{Name: "backup"}, run(task.Failed, "", "")); got {
		t.Error("没有设置通知的任务不应通知")
	}
}

// N5: 通知包含任务名、状态、结果摘要和查看命令。
func TestN5Content(t *testing.T) {
	code := 3
	failed := run(task.Failed, "partial", "disk\n  full\n")
	failed.ExitCode, failed.Error = &code, "exit status 3"
	m, _ := System(with(""), failed)
	if m.Title != "backup 失败(退出码 3)" || m.Body != "disk full\nbugu logs 20261112T020000-000000" || m.RunID != failed.ID || m.Task != "backup" {
		t.Errorf("失败: %+v", m)
	}

	m, _ = System(with(""), run(task.Succeeded, "已备份 12 个文件\n", ""))
	if m.Title != "backup 完成" || !strings.HasPrefix(m.Body, "已备份 12 个文件\n") {
		t.Errorf("成功: %+v", m)
	}

	m, _ = System(with(""), run(task.TimedOut, "", ""))
	if m.Title != "backup 超时" || !strings.Contains(m.Body, "最长运行时间") {
		t.Errorf("超时: %+v", m)
	}

	noOutput := run(task.Failed, "", "")
	noOutput.Error = "fork/exec /nope: no such file or directory"
	if m, _ = System(with(""), noOutput); !strings.Contains(m.Body, "no such file") {
		t.Errorf("没有输出时应显示失败原因: %+v", m)
	}

	if m, _ = System(with(task.NotifyAlways), run(task.Succeeded, "", "")); m.Body != "bugu logs 20261112T020000-000000" {
		t.Errorf("没有摘要时只留查看命令: %+v", m)
	}

	long, _ := System(with(""), run(task.Succeeded, strings.Repeat("长", 500), ""))
	if summary := strings.Split(long.Body, "\n")[0]; len([]rune(summary)) != summaryRunes+1 || !strings.HasSuffix(summary, "…") {
		t.Errorf("摘要应截断到 %d 字: %d", summaryRunes, len([]rune(summary)))
	}
}
