package task

import "time"

// Task 保持触发、步骤列表、通知列表的线性任务结构。执行器不负责解释触发或通知。
type Task struct {
	Name          string
	Trigger       Trigger
	Steps         []Step
	Notifications []Notification
	Cwd           string
	Env           map[string]string
	Timeout       time.Duration // 0 表示不设总时长上限。
}

// Trigger 保存时间触发的定义；调度解释留给 schedule。
type Trigger struct {
	Cron string
	At   time.Time
}

// Notification 仅标识通知渠道，投递配置由 notify 实现时扩展。
type Notification struct{ Channel string }

type StepType string

const StepScript StepType = "script"

type Step struct {
	Name   string
	Type   StepType
	Script Script
}

// Script 的 Path 和 Command 必须且只能填写一个。Path 直接执行，解释器由文件决定；
// Command 使用系统的 /bin/sh -c 解释，允许用户显式指定解释器。
type Script struct{ Path, Command string }

type Status string

const (
	Queued      Status = "queued"
	Running     Status = "running"
	Succeeded   Status = "succeeded"
	Failed      Status = "failed"
	TimedOut    Status = "timed_out"
	Skipped     Status = "skipped"
	Missed      Status = "missed"
	Canceled    Status = "canceled"
	Interrupted Status = "interrupted"
)

// Run 是执行核心采集的结果；没有启动或未正常退出时 ExitCode 为 nil。
// 持久化、唯一编号和配置快照由后续存档层负责。
type Run struct {
	Status     Status
	Command    []string
	StartedAt  time.Time
	FinishedAt time.Time
	ExitCode   *int
	Stdout     string
	Stderr     string
}
