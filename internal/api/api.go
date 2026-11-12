// Package api 定义 HTTP 接口的请求与响应类型,以及客户端和后台共同约定的名字。
// 只有类型和常量,没有逻辑。
package api

import (
	"time"

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
}

// Logs 是一次运行的输出。
type Logs struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
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
}

// Health 是健康检查的响应。
type Health struct {
	OK bool `json:"ok"`
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
}

func (e *Error) Error() string { return e.Message }

// ErrorResponse 是出错时的响应体。
type ErrorResponse struct {
	Error *Error `json:"error"`
}
