package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Task 保持触发、步骤列表、通知列表的线性任务结构。执行器不负责解释触发或通知。
type Task struct {
	Name          string            `json:"name"`
	Trigger       Trigger           `json:"trigger"`
	Steps         []Step            `json:"steps"`
	Notifications []Notification    `json:"notifications,omitempty"`
	Cwd           string            `json:"cwd,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Timeout       Duration          `json:"timeout,omitzero"` // 0 表示不设总时长上限。
	Paused        bool              `json:"paused,omitempty"`
}

// Trigger 保存时间触发的定义;Cron 和 At 必须且只能填写一个。调度解释留给 schedule。
type Trigger struct {
	Cron string    `json:"cron,omitempty"`
	At   time.Time `json:"at,omitzero"`
}

// Notification 仅标识通知渠道,投递配置由 notify 实现时扩展。
type Notification struct {
	Channel string `json:"channel"`
}

type StepType string

const StepScript StepType = "script"

type Step struct {
	Name   string   `json:"name,omitempty"`
	Type   StepType `json:"type"`
	Script Script   `json:"script,omitzero"`
}

// Script 的 Path 和 Command 必须且只能填写一个。Path 直接执行,解释器由文件决定;
// Command 使用系统的 /bin/sh -c 解释,允许用户显式指定解释器。
type Script struct {
	Path    string `json:"path,omitempty"`
	Command string `json:"command,omitempty"`
}

// Duration 在 JSON 中写成 "90s"、"1h30m" 这样的字符串。
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New(`时长应写成字符串,例如 "30m"`)
	}
	if s == "" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf(`无法解析时长 %q,应写成 "90s"、"1h30m" 这样的形式`, s)
	}
	*d = Duration(v)
	return nil
}

// 任务名同时用作文件名,所以只允许不会引起路径歧义的字符。
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidName 报告 name 能否作为任务名。
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Validate 检查任务定义中与执行方式无关的部分:名字和触发。
// 触发表达式本身是否合法由 schedule 判断,步骤能否执行由 exec 判断。
func Validate(t Task) error {
	if !ValidName(t.Name) {
		return fmt.Errorf("任务名 %q 不合法:只能包含字母、数字、- 和 _,以字母或数字开头,最长 64 个字符", t.Name)
	}
	if (t.Trigger.Cron == "") == t.Trigger.At.IsZero() {
		return errors.New("触发方式必须在 cron 表达式和一次性时间点之间二选一")
	}
	return nil
}

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

// Finished 报告该状态是否为终态。
func (s Status) Finished() bool { return s != Queued && s != Running }

// 运行的来源。
const (
	BySchedule = "schedule"
	ByManual   = "manual"
)

// Run 是任务的一次执行。执行核心填写状态、命令、起止时间、退出码和输出;
// 编号、所属任务、来源等由后台填写。没有启动或未正常退出时 ExitCode 为 nil。
// 输出不随元数据序列化,由存档层单独保存。
type Run struct {
	ID           string    `json:"id"`
	Task         string    `json:"task"`
	Status       Status    `json:"status"`
	Trigger      string    `json:"trigger"`
	ScheduledFor time.Time `json:"scheduled_for,omitzero"` // 定时触发时,原定的触发时间点。
	Note         string    `json:"note,omitempty"`         // 后台的说明,如跳过原因、错过的次数。
	Error        string    `json:"error,omitempty"`        // 执行未成功时的原因。
	RequestKey   string    `json:"request_key,omitempty"`  // 手动触发时客户端提供的幂等标识。
	Command      []string  `json:"command,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitzero"`
	StartedAt    time.Time `json:"started_at,omitzero"`
	FinishedAt   time.Time `json:"finished_at,omitzero"`
	ExitCode     *int      `json:"exit_code,omitempty"`
	Stdout       string    `json:"-"`
	Stderr       string    `json:"-"`
}
