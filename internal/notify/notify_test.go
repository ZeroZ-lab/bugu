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
		// T7、O4:无事可做或命中静默标记的运行没有内容,"有内容时"不通知,"总是"照常通知。
		{task.NotifyOnContent, task.Run{Status: task.Succeeded, Stdout: "NOTHING_NEW", NoContent: true}, false},
		{task.NotifyAlways, task.Run{Status: task.Succeeded, Stdout: "NOTHING_NEW", NoContent: true}, true},
		// O12:结果信封明确说了要不要通知时,"有内容时"按它来;有产物也算有内容。
		{task.NotifyOnContent, task.Run{Status: task.Succeeded, Stdout: "x", Notify: ptr(false)}, false},
		{task.NotifyOnContent, task.Run{Status: task.Succeeded, Notify: ptr(true)}, true},
		{task.NotifyOnContent, task.Run{Status: task.Succeeded, Artifacts: []string{"/r.md"}}, true},
		{task.NotifyOnFailure, task.Run{Status: task.Succeeded, Notify: ptr(true)}, false},
		{task.NotifyAlways, run(task.Skipped, "", ""), false},
		{task.NotifyAlways, run(task.Missed, "", ""), false},
		{task.NotifyAlways, run(task.Interrupted, "", ""), false},
		{task.NotifyAlways, run(task.Running, "", ""), false},
		// N6:"仅有结果时"给业务结果用,失败和超时交给只管失败的系统通知。
		{task.NotifyOnResult, run(task.Succeeded, "新内容", ""), true},
		{task.NotifyOnResult, run(task.Succeeded, "", ""), false},
		{task.NotifyOnResult, run(task.Failed, "", "坏了"), false},
		{task.NotifyOnResult, run(task.TimedOut, "", ""), false},
		{task.NotifyOnResult, task.Run{Status: task.Succeeded, Stdout: "x", NoContent: true}, false},
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

func ptr(b bool) *bool { return &b }

// O12 N5: 结果信封给出的通知正文和摘要优先于输出。
func TestO12EnvelopeMessage(t *testing.T) {
	r := run(task.Succeeded, "很长的原始输出", "")
	r.Summary = "两条新闻"
	if m, _ := System(with(""), r); !strings.HasPrefix(m.Body, "两条新闻\n") {
		t.Fatalf("body=%q", m.Body)
	}
	r.Message = "去看看"
	if m, _ := System(with(""), r); !strings.HasPrefix(m.Body, "去看看\n") {
		t.Fatalf("body=%q", m.Body)
	}
}

// N5 G3:AI 输出的结果常是 Markdown,摘要里去掉标记,只留文字。
func TestN5SummaryIsPlainText(t *testing.T) {
	out := "### Go 动态\n\n- [Go 迎来 17 周年](https://go.dev/blog/17years):**纪念**文章\n> 引用 `code`\n1. 第一条\n"
	got := Summary(task.Run{Status: task.Succeeded, Stdout: out})
	if want := "Go 动态 Go 迎来 17 周年:纪念文章 引用 code 第一条"; got != want {
		t.Fatalf("got %q", got)
	}
}

// T7、O4:无事可做的运行常常只有 AI 命令行写到标准错误的日志,摘要不能拿它充数;
// 有内容的成功运行只写了标准错误时,仍以它为摘要。
func TestT7NoContentSummaryIgnoresStderr(t *testing.T) {
	log := "Reading prompt from stdin... ERROR models_manager: request timed out"
	if got := Summary(task.Run{Status: task.Succeeded, NoContent: true, Stderr: log}); got != "" {
		t.Fatalf("无内容的运行摘要 = %q,期望为空", got)
	}
	if got := Summary(task.Run{Status: task.Succeeded, NoContent: true, Stdout: "NOTHING_NEW", Stderr: log}); got != "NOTHING_NEW" {
		t.Fatalf("无内容的运行摘要 = %q,期望取标准输出", got)
	}
	if got := Summary(task.Run{Status: task.Succeeded, Stderr: "warn"}); got != "warn" {
		t.Fatalf("只有标准错误的成功运行摘要 = %q,期望 warn", got)
	}
}
