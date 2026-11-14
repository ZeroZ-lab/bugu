// Package notify 决定一次运行结束后要不要通知、通知写什么。
// 它不负责把通知显示出来:系统通知由桌面应用发出(N1),后台只把内容交给它。
package notify

import (
	"regexp"
	"strings"

	"bugu/internal/i18n"
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
		if n.Channel == task.ChannelSystem && Due(n.When, r) {
			return Compose(r), true
		}
	}
	return Message{}, false
}

// Due 按通知时机判断这次运行要不要通知(N2)。只有真正执行过的运行才通知:跳过、错过、被后台关闭打断的不算。
func Due(when string, r task.Run) bool {
	switch r.Status {
	case task.Failed, task.TimedOut:
		return when != task.NotifyOnResult // 只有"仅有结果时"不含失败(N6)
	case task.Succeeded:
		switch when {
		case task.NotifyAlways:
			return true
		case task.NotifyOnFailure:
			return false
		default: // NotifyOnContent 和 NotifyOnResult,也是没写时的默认值,也是没写时的默认值。声明了无事可做或命中静默标记的不算有内容(T7、O4)。
			if r.NoContent {
				return false
			}
			if r.Notify != nil { // 结果信封明确说了要不要通知(O12)
				return *r.Notify
			}
			return strings.TrimSpace(r.Stdout) != "" || strings.TrimSpace(r.Stderr) != "" || len(r.Artifacts) > 0
		}
	}
	return false
}

// Compose 生成通知的标题和正文:任务名、状态、结果摘要和查看命令(N5)。
func Compose(r task.Run) Message {
	m := Message{Task: r.Task, RunID: r.ID}
	var summary string
	switch r.Status {
	case task.Succeeded:
		m.Title = i18n.T("%s 完成", r.Task)
		summary = first(r.Message, r.Summary, r.Stdout, r.Stderr)
	case task.TimedOut:
		m.Title = i18n.T("%s 超时", r.Task)
		summary = first(r.Stderr, r.Stdout, i18n.T("超过了设定的最长运行时间"))
	default:
		m.Title = i18n.T("%s 失败", r.Task)
		summary = first(r.Message, r.Stderr, r.Stdout, r.Error)
		if r.ExitCode != nil {
			m.Title = i18n.T("%s 失败(退出码 %d)", r.Task, *r.ExitCode)
		}
	}
	lines := []string{}
	if summary != "" {
		lines = append(lines, summary)
	}
	m.Body = strings.Join(append(lines, "bugu logs "+r.ID), "\n")
	return m
}

// Summary 返回这次运行结果的摘要,用于收件箱:成功时取输出的开头,失败时先看标准错误。
// 无内容的运行不拿标准错误充当摘要:那里通常只是 AI 命令行的启动日志(T7、O4)。
func Summary(r task.Run) string {
	if r.Status == task.Succeeded {
		if r.NoContent {
			return first(r.Stdout)
		}
		return first(r.Stdout, r.Stderr)
	}
	return first(r.Stderr, r.Stdout, r.Error)
}

// first 返回第一个非空内容的摘要:去掉 Markdown 标记,压成一行,超长时截断。
func first(texts ...string) string {
	for _, text := range texts {
		s := strings.Join(strings.Fields(plain(text)), " ")
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

// 摘要里不要 Markdown 的标记:通知和收件箱的摘要都按纯文本显示。
var (
	blockMark = regexp.MustCompile(`(?m)^[ \t]*(#{1,6}[ \t]+|>[ \t]?|[-*+][ \t]+|\d+[.)][ \t]+)`)
	linkMark  = regexp.MustCompile(`\[([^\]]*)\]\([^)\s]*\)`)
	spanMark  = regexp.MustCompile("\\*\\*([^*]+)\\*\\*|__([^_]+)__|`([^`]+)`")
)

// plain 去掉常见的 Markdown 标记:标题、引用和列表的开头,链接只留文字,粗体和行内代码只留内容。
func plain(s string) string {
	s = blockMark.ReplaceAllString(s, "")
	s = linkMark.ReplaceAllString(s, "$1")
	return spanMark.ReplaceAllString(s, "$1$2$3")
}
