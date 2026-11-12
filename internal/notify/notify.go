// Package notify 决定一次运行结束后要不要通知、通知写什么。
// 它不负责把通知显示出来:系统通知由桌面应用发出(N1),后台只把内容交给它。
package notify

import (
	"fmt"
	"strings"

	"bugu/internal/task"
)

// Message 是一条待发出的通知。
type Message struct {
	Task  string
	RunID string // 点击通知时据此打开对应的结果
	Title string
	Body  string
}

// summaryRunes 是摘要保留的字数(N5)。
const summaryRunes = 120

// System 返回这次运行应发出的系统通知。任务没有设置系统通知,或这次运行不符合它的通知时机时,
// 第二个返回值为 false。
func System(t task.Task, r task.Run) (Message, bool) {
	for _, n := range t.Notifications {
		if n.Channel == task.ChannelSystem && due(n.When, r) {
			return compose(r), true
		}
	}
	return Message{}, false
}

// due 按通知时机判断(N2)。只有真正执行过的运行才通知:跳过、错过、被后台关闭打断的不算。
func due(when string, r task.Run) bool {
	switch r.Status {
	case task.Failed, task.TimedOut:
		return true // 三种时机都包含失败
	case task.Succeeded:
		switch when {
		case task.NotifyAlways:
			return true
		case task.NotifyOnFailure:
			return false
		default: // NotifyOnContent,也是没写时的默认值
			return strings.TrimSpace(r.Stdout) != "" || strings.TrimSpace(r.Stderr) != ""
		}
	}
	return false
}

// compose 生成通知的标题和正文:任务名、状态、结果摘要和查看命令(N5)。
func compose(r task.Run) Message {
	m := Message{Task: r.Task, RunID: r.ID}
	var summary string
	switch r.Status {
	case task.Succeeded:
		m.Title = r.Task + " 完成"
		summary = first(r.Stdout, r.Stderr)
	case task.TimedOut:
		m.Title = r.Task + " 超时"
		summary = first(r.Stderr, r.Stdout, "超过了设定的最长运行时间")
	default:
		m.Title = r.Task + " 失败"
		summary = first(r.Stderr, r.Stdout, r.Error)
		if r.ExitCode != nil {
			m.Title = fmt.Sprintf("%s 失败(退出码 %d)", r.Task, *r.ExitCode)
		}
	}
	lines := []string{}
	if summary != "" {
		lines = append(lines, summary)
	}
	m.Body = strings.Join(append(lines, "bugu logs "+r.ID), "\n")
	return m
}

// first 返回第一个非空内容的摘要:压成一行,超长时截断。
func first(texts ...string) string {
	for _, text := range texts {
		s := strings.Join(strings.Fields(text), " ")
		if s == "" {
			continue
		}
		if runes := []rune(s); len(runes) > summaryRunes {
			return string(runes[:summaryRunes]) + "…"
		}
		return s
	}
	return ""
}
