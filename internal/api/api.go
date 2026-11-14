// Package api 定义 HTTP 接口的请求与响应类型,以及客户端和后台共同约定的名字。
// 只有类型和常量,以及接口错误按语言说明的方式。
package api

import (
	"time"

	"bugu/internal/i18n"
	"bugu/internal/task"
)

const (
	// Prefix 是全部接口路径的前缀。
	Prefix = "/api/v1"
	// KeyHeader 携带客户端生成的幂等标识(C8)。带同一个标识重试,不会重复触发运行。
	KeyHeader = "Idempotency-Key"

	// TokenFile 和 EndpointFile 是数据目录下的两个文件,本机客户端靠它们找到并连上后台。
	TokenFile    = "token"
	EndpointFile = "endpoint.json"
)

// Endpoint 是 EndpointFile 的内容:后台实际监听的地址。
type Endpoint struct {
	Addr string `json:"addr"`
}

// TaskInfo 是任务定义加上后台才知道的运行时信息。
type TaskInfo struct {
	task.Task
	NextRun time.Time `json:"next_run,omitzero"` // 暂停或不会再触发时为空。
	Running bool      `json:"running"`
	LastRun *task.Run `json:"last_run,omitempty"` // 最近一次已经结束的运行;还没有时为空。
}

// Logs 是一次运行的输出。Stdout 和 Stderr 是整次运行的结果,Steps 是每个步骤各自的输出,
// 与运行记录里的步骤一一对应;没有执行到的步骤输出为空(T8)。
type Logs struct {
	Stdout string     `json:"stdout"`
	Stderr string     `json:"stderr"`
	Steps  []StepLogs `json:"steps"`
}

// StepLogs 是一个步骤的输出。
type StepLogs struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// Inbox 是收件箱的内容和计数(G3、G9)。
type Inbox struct {
	Unread       int        `json:"unread"`        // 未读结果的条数
	UnreadFailed int        `json:"unread_failed"` // 其中失败、超时、中断的条数
	Running      int        `json:"running"`       // 正在运行的任务数
	Runs         []task.Run `json:"runs"`          // 最新的在前
}

// MarkedRead 是"全部标为已读"的结果。
type MarkedRead struct {
	Marked int `json:"marked"`
}

// Runner 是一个运行器的概况,供界面和命令行在建任务时选择。
type Runner struct {
	Name   string `json:"name"`
	Title  string `json:"title,omitempty"`
	Preset bool   `json:"preset"` // 自带的预设,而不是用户自己的配置
	Agent  bool   `json:"agent"`  // 能否用于 AI 命令行步骤
	// Permissions 是任务可以为它选择的权限级别,由低到高,第一个是默认值;
	// 为空表示这个运行器不适用权限级别。
	Permissions []string `json:"permissions"`
}

// RunnerCheck 是检查一个运行器能否使用的结果(R8)。
type RunnerCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"` // 通过时通常是版本号,不通过时是原因
	// Cause 是后台一侧可翻译的说明;有它时,写出响应时按请求的语言生成 Detail(决策记录 0014)。
	Cause error `json:"-"`
}

// Localized 返回按语言 l 说明的检查结果。
func (c RunnerCheck) Localized(l i18n.Lang) any {
	if c.Cause != nil {
		c.Detail = i18n.Localize(c.Cause, l)
	}
	return c
}

// Health 是健康检查的响应。
type Health struct {
	OK bool `json:"ok"`
	// Version 是后台程序的版本,显示在设置页的"关于"里。
	Version string `json:"version,omitempty"`
	// Host 是后台所在机器的主机名。打开另一台主机的面板时,页面用它标明正在管理的是哪台(G5)。
	Host string `json:"host,omitempty"`
}

// Token 是一个访问令牌的说明(C5,决策记录 0015)。令牌本身只在创建时返回一次。
type Token struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	// Local 表示这是数据目录里 token 文件的本机令牌:本机命令行和应用用它,不能作废。
	Local bool `json:"local,omitempty"`
}

// NewToken 是创建令牌的响应,Token 是明文,之后无法再查看。
type NewToken struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

// Plan 说明立即运行一次会执行什么(bugu run --dry-run)。模板变量按现在的时间展开,
// 上一步的输出({prev} 等)要运行时才知道,原样保留。
type Plan struct {
	Task  string        `json:"task"`
	Cwd   string        `json:"cwd"`
	Steps []PlannedStep `json:"steps"`
}

// PlannedStep 是计划里的一步。
type PlannedStep struct {
	Name       string   `json:"name,omitempty"`
	Type       string   `json:"type"`
	Command    []string `json:"command,omitempty"` // 实际启动的命令及参数
	Prompt     string   `json:"prompt,omitempty"`  // AI 命令行步骤的提示词
	Text       string   `json:"text,omitempty"`    // 文本步骤的内容
	Permission string   `json:"permission,omitempty"`
}

// Host 是登记过的另一台主机(G5),附带这次检查到的状态(G14)。
type Host struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
	// Status 是 online、offline(连不上)或 unauthorized(令牌被拒)。只是连接状态,不代表那里的任务。
	Status  string `json:"status,omitempty"`
	Version string `json:"version,omitempty"`
	// PlainPublic 表示这是经公网的明文 HTTP 地址,令牌会以明文传输(C6)。
	PlainPublic bool `json:"plain_public,omitempty"`
}

// 主机的连接状态。
const (
	HostOnline       = "online"
	HostOffline      = "offline"
	HostUnauthorized = "unauthorized"
)

// 界面语言的设置值(决策记录 0014)。
const (
	LanguageAuto = "auto" // 跟随系统
	LanguageZh   = "zh"
	LanguageEn   = "en"
)

// Settings 是后台的设置,对所有界面生效。
type Settings struct {
	// Language 是界面语言:auto 跟随系统,或 zh、en。
	Language string `json:"language"`
	// Effective 是后台实际说的语言,auto 时是系统的语言。只在响应里有。
	Effective string `json:"effective_language,omitempty"`
}

// 错误代码。
const (
	CodeUnauthorized = "unauthorized" // 没有令牌或令牌无效
	CodeForbidden    = "forbidden"    // 请求来源不被允许
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeInvalid      = "invalid" // 请求内容有误,Message 说明如何修正
	CodeInternal     = "internal"
)

// Error 是接口返回的错误。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Cause 是后台一侧的说明。有它时,Message 在写出响应时按请求的语言由它生成(决策记录 0014)。
	Cause error `json:"-"`
}

// Wrap 返回一个可按请求语言说明的接口错误。
func Wrap(code string, err error) *Error { return &Error{Code: code, Cause: err} }

func (e *Error) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Message
}

// In 用指定的语言说明这个错误。
func (e *Error) In(l i18n.Lang) string {
	if e.Cause != nil {
		return i18n.Localize(e.Cause, l)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

// ErrorResponse 是出错时的响应体。
type ErrorResponse struct {
	Error *Error `json:"error"`
}
