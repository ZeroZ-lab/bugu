package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	// CreatedAt 由后台在创建时填写,首次运行时用作 {last_run} 的值。
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// Trigger 保存时间触发的定义;Cron 和 At 必须且只能填写一个。调度解释留给 schedule。
type Trigger struct {
	Cron string    `json:"cron,omitempty"`
	At   time.Time `json:"at,omitzero"`
}

// Notification 是任务的一条通知设置:发到哪个渠道,什么情况下发。
type Notification struct {
	Channel string `json:"channel"`
	When    string `json:"when,omitempty"` // 为空时等同于 NotifyOnContent。
}

// ChannelSystem 是系统通知,由桌面应用发出(N1)。
const ChannelSystem = "system"

// 通知时机(N2)。
const (
	NotifyAlways    = "always"     // 每次运行后
	NotifyOnFailure = "on_failure" // 仅失败、超时时
	NotifyOnContent = "on_content" // 失败、超时时,或成功且有输出时。默认值。
)

type StepType string

const (
	StepScript StepType = "script"
	StepAgent  StepType = "agent" // AI 命令行(T2)
)

type Step struct {
	Name   string   `json:"name,omitempty"`
	Type   StepType `json:"type"`
	Script Script   `json:"script,omitzero"`
	Agent  Agent    `json:"agent,omitzero"`
}

// Agent 是 AI 命令行步骤:用哪个运行器,给它什么提示词,允许它做到哪一步。
type Agent struct {
	Runner string `json:"runner"`
	Prompt string `json:"prompt"`
	// Permission 是权限级别,为空时按 PermReadOnly 运行(决策记录 0008)。
	Permission string `json:"permission,omitempty"`
}

// 权限级别,由低到高。
const (
	PermReadOnly = "read_only" // 不修改文件
	PermWriteCwd = "write_cwd" // 可以修改工作目录内的文件
	PermFull     = "full"      // 不加限制
)

// Permissions 按由低到高的顺序列出全部权限级别。
var Permissions = []string{PermReadOnly, PermWriteCwd, PermFull}

// PermissionRank 返回级别的高低次序,数字越大权限越高;不认识的级别返回 false。
func PermissionRank(level string) (int, bool) {
	i := slices.Index(Permissions, level)
	return i, i >= 0
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

// Validate 检查任务定义中与执行方式无关的部分:名字、触发和通知设置。
// 触发表达式本身是否合法由 schedule 判断,步骤能否执行由 exec 判断。
func Validate(t Task) error {
	if !ValidName(t.Name) {
		return fmt.Errorf("任务名 %q 不合法:只能包含字母、数字、- 和 _,以字母或数字开头,最长 64 个字符", t.Name)
	}
	if (t.Trigger.Cron == "") == t.Trigger.At.IsZero() {
		return errors.New("触发方式必须在 cron 表达式和一次性时间点之间二选一")
	}
	for _, n := range t.Notifications {
		if n.Channel != ChannelSystem {
			return fmt.Errorf("不支持的通知渠道 %q,目前只支持 %s", n.Channel, ChannelSystem)
		}
		switch n.When {
		case "", NotifyAlways, NotifyOnFailure, NotifyOnContent:
		default:
			return fmt.Errorf("通知时机 %q 不合法,可用的是 %s、%s、%s", n.When, NotifyAlways, NotifyOnFailure, NotifyOnContent)
		}
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
	Permission   string    `json:"permission,omitempty"` // AI 命令行步骤实际使用的权限级别。
	Stdout       string    `json:"-"`
	Stderr       string    `json:"-"`
}
