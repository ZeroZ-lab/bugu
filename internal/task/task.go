package task

import (
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"time"

	"bugu/internal/i18n"
)

// Task 保持触发、步骤列表、通知列表的线性任务结构。执行器不负责解释触发或通知。
type Task struct {
	Name          string            `json:"name"`
	Trigger       Trigger           `json:"trigger"`
	Steps         []Step            `json:"steps"`
	Notifications []Notification    `json:"notifications,omitempty"`
	Cwd           string            `json:"cwd,omitempty"` // 为空时在任务的状态目录里运行
	Env           map[string]string `json:"env,omitempty"`
	// EnvFile 是环境变量文件的路径,密钥通过它注入,不写进任务定义;其中的值在输出里脱敏(E8、E10)。
	EnvFile string `json:"env_file,omitempty"`
	// Timezone 是 IANA 时区名,如 "Asia/Shanghai";cron 和 {date} 等按它计算。为空时用主机时区(E7)。
	Timezone string `json:"timezone,omitempty"`
	// OnMissed 是错过触发时的补跑策略,见 Missed*;为空时等同于 MissedRunOnce(S4)。
	OnMissed string `json:"on_missed,omitempty"`
	// MissedWindow 是 MissedWindow 策略的宽限时长:原定时间过去不超过它才补跑。
	MissedWindow Duration `json:"missed_window,omitzero"`
	Timeout      Duration `json:"timeout,omitzero"` // 0 表示不设总时长上限。
	// SilentIf 是静默标记:运行成功且最后一步的输出包含它时,本次记为无内容,不发业务通知(O4)。
	SilentIf string `json:"silent_if,omitempty"`
	// Output 声明产物落盘的位置和来源(O5、O6),为空表示不落盘。
	Output *Output `json:"output,omitempty"`
	Paused bool    `json:"paused,omitempty"`
	// CreatedAt 由后台在创建时填写,首次运行时用作 {last_run} 的值。
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// Output 是产物的落盘设置。文件名可以使用模板变量,同名文件会被覆盖。
type Output struct {
	Dir  string `json:"dir"`            // 产物目录,可以用 ~ 开头,不存在时创建
	Name string `json:"name"`           // 文件名模板,如 "{date}.md";不能包含 /
	From string `json:"from,omitempty"` // 产物来源,见 OutputFrom*;为空时等同于 OutputFromStdout
}

// 产物来源(O6)。
const (
	OutputFromStdout = "stdout" // 由调度器把最后一步的输出保存为文件
	OutputFromTask   = "task"   // 由任务自己写入 {output_file},调度器只检查它存在(R6)
)

// Trigger 保存时间触发的定义;Cron 和 At 必须且只能填写一个。调度解释留给 schedule。
type Trigger struct {
	Cron string    `json:"cron,omitempty"`
	At   time.Time `json:"at,omitzero"`
	// Jitter 是随机偏移的范围(S12,决策记录 0018):每次在原定时间前后不超过它的范围内触发。只用于 cron。
	Jitter Duration `json:"jitter,omitzero"`
}

// 补跑策略(S4)。
const (
	MissedSkip    = "skip"     // 不补:错过的时间点记为错过
	MissedRunOnce = "run_once" // 只补最近一次,默认值
	MissedWindow  = "window"   // 原定时间过去不超过 missed_window 才补最近一次
)

// Notification 是任务的一条通知设置:发到哪个渠道,什么情况下发。
type Notification struct {
	Channel string `json:"channel"`
	When    string `json:"when,omitempty"` // 为空时等同于 NotifyOnContent。
	URL     string `json:"url,omitempty"`  // ChannelWebhook 的地址
}

// 通知渠道。
const (
	ChannelSystem  = "system"  // 系统通知,由桌面应用发出(N1)
	ChannelWebhook = "webhook" // 向一个地址 POST JSON(N3,决策记录 0013)
)

// 通知时机(N2)。
const (
	NotifyAlways    = "always"     // 每次运行后
	NotifyOnFailure = "on_failure" // 仅失败、超时时
	NotifyOnContent = "on_content" // 失败、超时时,或成功且有输出时。默认值。
	// NotifyOnResult 只在成功且有结果时,用于业务结果的通知,和只管失败的系统通知分开设置(N6)。
	NotifyOnResult = "on_result"
)

type StepType string

const (
	StepScript StepType = "script"
	StepAgent  StepType = "agent" // AI 命令行(T2)
	StepText   StepType = "text"  // 一段固定文本,原样作为这一步的输出(T4)
)

// Step 是任务里按顺序执行的一环。按 Type 只填写对应的 Script、Agent 或 Text。
type Step struct {
	// Name 用于在后面的步骤里以 {steps.<名字>.output} 引用这一步的输出,可以不填。
	Name   string   `json:"name,omitempty"`
	Type   StepType `json:"type"`
	Script Script   `json:"script,omitzero"`
	Agent  Agent    `json:"agent,omitzero"`
	Text   string   `json:"text,omitempty"`
	// Timeout 是这一步的时长上限,0 表示只受任务的总时长上限约束(T8)。
	Timeout Duration `json:"timeout,omitzero"`
}

// ExitNothingToDo 是脚本步骤声明"无事可做"的退出码:后面的步骤不再执行,
// 本次运行记为成功但无内容,不发业务通知(T7)。取自 sysexits.h 的 EX_TEMPFAIL,意为"现在没有,以后再试"。
const ExitNothingToDo = 75

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
		return i18n.Errorf(`时长应写成字符串,例如 "30m"`)
	}
	if s == "" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return i18n.Errorf(`无法解析时长 %q,应写成 "90s"、"1h30m" 这样的形式`, s)
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
		return i18n.Errorf("任务名 %q 不合法:只能包含字母、数字、- 和 _,以字母或数字开头,最长 64 个字符", t.Name)
	}
	if (t.Trigger.Cron == "") == t.Trigger.At.IsZero() {
		return i18n.Errorf("触发方式必须在 cron 表达式和一次性时间点之间二选一")
	}
	if t.Trigger.Jitter < 0 {
		return i18n.Errorf("随机偏移不能是负的")
	}
	if t.Trigger.Jitter != 0 && t.Trigger.Cron == "" {
		return i18n.Errorf("随机偏移只用于 cron 定时,一次性任务按设定的时间点运行")
	}
	for _, n := range t.Notifications {
		switch n.Channel {
		case ChannelSystem:
			if n.URL != "" {
				return i18n.Errorf("系统通知不需要 url;要发到某个地址,渠道写 webhook")
			}
		case ChannelWebhook:
			if u, err := url.Parse(n.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return i18n.Errorf("webhook 的地址 %q 不合法,应是 http:// 或 https:// 开头的完整地址", n.URL)
			}
		default:
			return i18n.Errorf("不支持的通知渠道 %q,可用的是 %s 和 %s", n.Channel, ChannelSystem, ChannelWebhook)
		}
		switch n.When {
		case "", NotifyAlways, NotifyOnFailure, NotifyOnContent, NotifyOnResult:
		default:
			return i18n.Errorf("通知时机 %q 不合法,可用的是 %s、%s、%s、%s", n.When, NotifyAlways, NotifyOnFailure, NotifyOnContent, NotifyOnResult)
		}
	}
	switch t.OnMissed {
	case "", MissedSkip, MissedRunOnce:
		if t.MissedWindow != 0 {
			return i18n.Errorf("missed_window 只在 on_missed 为 %s 时有效", MissedWindow)
		}
	case MissedWindow:
		if t.MissedWindow <= 0 {
			return i18n.Errorf("on_missed 为 %s 时要给出正的 missed_window,如 \"2h\"", MissedWindow)
		}
	default:
		return i18n.Errorf("补跑策略 %q 不合法,可用的是 %s(不补)、%s(只补最近一次,默认)、%s(在宽限时长内才补)", t.OnMissed, MissedSkip, MissedRunOnce, MissedWindow)
	}
	if t.Timezone != "" {
		if _, err := time.LoadLocation(t.Timezone); err != nil || t.Timezone == "Local" {
			return i18n.Errorf("时区 %q 不认识,应写成 IANA 时区名,如 Asia/Shanghai、America/New_York、UTC", t.Timezone)
		}
	}
	return nil
}

// In 把时间换到任务的时区(E7)。没有设置时区时原样返回:后台的时钟本来就是主机时区。
// 时区名在保存任务时已经校验过,这里读不到时也原样返回。
func (t Task) In(at time.Time) time.Time {
	if t.Timezone == "" {
		return at
	}
	loc, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return at
	}
	return at.In(loc)
}

// Delivery 是一次通知投递的记录。投递与执行解耦:失败只重试投递,不重跑任务(N7)。
type Delivery struct {
	RunID   string `json:"run_id"`
	Task    string `json:"task"`
	Index   int    `json:"index"`   // 任务的第几条通知设置,从 0 起
	Channel string `json:"channel"` // ChannelSystem 或 ChannelWebhook
	Target  string `json:"target,omitempty"`
	// Key 是去重标识:运行编号加通知序号。webhook 在 Idempotency-Key 请求头里带上它,
	// 重试时不变,接收方可以据此丢掉重复的投递。
	Key         string         `json:"key"`
	Status      DeliveryStatus `json:"status"`
	Attempts    int            `json:"attempts"`
	Error       string         `json:"error,omitempty"` // 最近一次失败的原因
	CreatedAt   time.Time      `json:"created_at"`
	NextAttempt time.Time      `json:"next_attempt,omitzero"` // 等待重试时,下一次尝试的时间
	DeliveredAt time.Time      `json:"delivered_at,omitzero"`
}

type DeliveryStatus string

const (
	DeliveryPending   DeliveryStatus = "pending"   // 等待投递或重试
	DeliveryDelivered DeliveryStatus = "delivered" // 已送达
	DeliveryFailed    DeliveryStatus = "failed"    // 重试用完仍未送达
	DeliveryDropped   DeliveryStatus = "dropped"   // 没有可送达的地方,如后台没有界面时的系统通知
)

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
	// NoContent 表示运行成功但没有需要告诉用户的内容:某一步声明了无事可做(T7),
	// 或输出包含静默标记(O4)。这样的运行不发业务通知。
	NoContent bool `json:"no_content,omitempty"`
	// Summary 是结果摘要:取自结果信封(O12),没有时取输出的开头。用于收件箱和通知。
	Summary string `json:"summary,omitempty"`
	// Artifacts 是本次生成的产物文件的完整路径(O5、O12)。
	Artifacts []string `json:"artifacts,omitempty"`
	// Notify 是结果信封里"是否请求通知"的值,没有写时为 nil,按任务的通知时机决定。
	Notify *bool `json:"notify,omitempty"`
	// Message 是结果信封里给出的通知正文。
	Message string `json:"message,omitempty"`
	// ReadAt 是用户在收件箱里读过这次结果的时间,未读时为零值(G3)。
	ReadAt time.Time `json:"read_at,omitzero"`
	// Steps 是每个步骤的结果,与任务的步骤一一对应(T6、T8)。Command、ExitCode、Permission
	// 和输出取自最后执行的那一步。
	Steps  []StepRun `json:"steps,omitempty"`
	Stdout string    `json:"-"`
	Stderr string    `json:"-"`
}

// StepRun 是一个步骤在一次运行里的结果。没有执行到的步骤状态为 Skipped。
// 输出不随元数据序列化,由存档层单独保存。
type StepRun struct {
	Name        string    `json:"name,omitempty"`
	Type        StepType  `json:"type"`
	Status      Status    `json:"status"`
	Error       string    `json:"error,omitempty"`
	Command     []string  `json:"command,omitempty"`
	StartedAt   time.Time `json:"started_at,omitzero"`
	FinishedAt  time.Time `json:"finished_at,omitzero"`
	ExitCode    *int      `json:"exit_code,omitempty"`
	Permission  string    `json:"permission,omitempty"`
	NothingToDo bool      `json:"nothing_to_do,omitempty"` // 这一步声明了无事可做(T7)
	Stdout      string    `json:"-"`
	Stderr      string    `json:"-"`
}

// InInbox 报告这次运行是否进入收件箱:已经结束,并且有需要用户看的结果。
// 失败、超时、中断都要看;成功的要有内容,声明了无内容的不算。
func (r Run) InInbox() bool {
	switch r.Status {
	case Failed, TimedOut, Interrupted:
		return true
	case Succeeded:
		return !r.NoContent && (r.Summary != "" || len(r.Artifacts) > 0)
	}
	return false
}

// Unread 报告这次运行是否是收件箱里的未读结果。
func (r Run) Unread() bool { return r.InInbox() && r.ReadAt.IsZero() }
