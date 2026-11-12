package runner

import (
	"strings"
	"time"
)

// Vars 是一次运行里可用的模板变量(PRD 7.4)。
type Vars struct {
	TaskName string
	Now      time.Time // 本次运行开始的时间
	LastRun  time.Time // 上次成功运行的开始时间;没有时由调用者填入任务的创建时间
	RunDir   string    // 本次运行的存档目录
}

// Expand 把 s 里的 {date}、{last_run} 等模板变量换成本次运行的值。
// 不认识的花括号内容原样保留,所以提示词和命令里可以放心出现 JSON 或 shell 的花括号。
func (v Vars) Expand(s string) string {
	if !strings.Contains(s, "{") {
		return s
	}
	return strings.NewReplacer(
		"{date}", v.Now.Format("2006-01-02"),
		"{time}", v.Now.Format("15:04:05"),
		"{datetime}", v.Now.Format(time.RFC3339),
		"{last_run}", v.LastRun.Format(time.RFC3339),
		"{run_dir}", v.RunDir,
		"{task_name}", v.TaskName,
	).Replace(s)
}
