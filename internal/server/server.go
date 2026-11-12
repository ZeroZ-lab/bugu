// Package server 把后台的能力暴露为 HTTP JSON 接口,并负责令牌认证和请求来源校验。
package server

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"bugu/internal/api"
	"bugu/internal/task"
)

// Backend 是接口背后的实现。出错时返回 *api.Error 以指明错误类别,其他错误一律视为内部错误。
type Backend interface {
	Tasks() ([]api.TaskInfo, error)
	Task(name string) (api.TaskInfo, error)
	// CreateTask 在同名任务已存在且定义完全相同时返回已有的任务,所以重复提交是安全的(C8)。
	CreateTask(t task.Task) (api.TaskInfo, error)
	UpdateTask(name string, t task.Task) (api.TaskInfo, error)
	DeleteTask(name string) error
	SetPaused(name string, paused bool) (api.TaskInfo, error)
	// RunNow 立即运行一次。key 不为空且之前用过时,返回当时那次运行(C8)。
	RunNow(name, key string) (task.Run, error)
	Runs(taskName string, limit int) ([]task.Run, error)
	Run(id string) (task.Run, error)
	Logs(id string) (api.Logs, error)
	Runners() ([]api.Runner, error)
	// CheckRunner 实际启动一次运行器的检查命令,可能需要几秒。
	CheckRunner(name string) (api.RunnerCheck, error)
}

// 前端的静态文件。它们不含用户数据,不需要令牌(决策记录 0007)。
//
//go:embed static
var static embed.FS

// pages 是对外提供的静态文件,键是请求路径。只提供列出的这几个,其余路径一律走接口的校验。
var pages = map[string]string{
	"/":          "static/index.html",
	"/app.js":    "static/app.js",
	"/style.css": "static/style.css",
}

// 页面只能加载同源的脚本和样式,不能被其他页面嵌入,也不向外泄露来源。
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
	"img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func servePage(w http.ResponseWriter, r *http.Request, name string) {
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, static, name)
}

// maxBody 是请求体的大小上限。任务定义很小,超过即视为误用。
const maxBody = 1 << 20

// New 返回提供全部接口和前端页面的处理器。valid 判断令牌是否有效;
// 除了几个固定的静态文件,每个请求都必须通过它(C1,决策记录 0007)。
func New(b Backend, valid func(token string) bool) http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, fn func(r *http.Request) (any, error)) {
		method, path, _ := strings.Cut(pattern, " ")
		mux.HandleFunc(method+" "+api.Prefix+path, func(w http.ResponseWriter, r *http.Request) {
			v, err := fn(r)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, v)
		})
	}

	handle("GET /health", func(*http.Request) (any, error) { return api.Health{OK: true}, nil })
	handle("GET /tasks", func(*http.Request) (any, error) {
		tasks, err := b.Tasks()
		if tasks == nil {
			tasks = []api.TaskInfo{}
		}
		return tasks, err
	})
	handle("POST /tasks", func(r *http.Request) (any, error) {
		t, err := decodeTask(r)
		if err != nil {
			return nil, err
		}
		return b.CreateTask(t)
	})
	handle("GET /tasks/{name}", func(r *http.Request) (any, error) { return b.Task(r.PathValue("name")) })
	handle("PUT /tasks/{name}", func(r *http.Request) (any, error) {
		t, err := decodeTask(r)
		if err != nil {
			return nil, err
		}
		return b.UpdateTask(r.PathValue("name"), t)
	})
	handle("DELETE /tasks/{name}", func(r *http.Request) (any, error) {
		return struct{}{}, b.DeleteTask(r.PathValue("name"))
	})
	handle("POST /tasks/{name}/pause", func(r *http.Request) (any, error) { return b.SetPaused(r.PathValue("name"), true) })
	handle("POST /tasks/{name}/resume", func(r *http.Request) (any, error) { return b.SetPaused(r.PathValue("name"), false) })
	handle("POST /tasks/{name}/run", func(r *http.Request) (any, error) {
		return b.RunNow(r.PathValue("name"), r.Header.Get(api.KeyHeader))
	})
	handle("GET /runs", func(r *http.Request) (any, error) {
		limit := 0
		if s := r.URL.Query().Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 {
				return nil, &api.Error{Code: api.CodeInvalid, Message: fmt.Sprintf("limit 应为非负整数,收到 %q", s)}
			}
			limit = n
		}
		runs, err := b.Runs(r.URL.Query().Get("task"), limit)
		if runs == nil {
			runs = []task.Run{}
		}
		return runs, err
	})
	handle("GET /runs/{id}", func(r *http.Request) (any, error) { return b.Run(r.PathValue("id")) })
	handle("GET /runs/{id}/logs", func(r *http.Request) (any, error) { return b.Logs(r.PathValue("id")) })

	handle("GET /runners", func(*http.Request) (any, error) {
		runners, err := b.Runners()
		if runners == nil {
			runners = []api.Runner{}
		}
		return runners, err
	})
	handle("POST /runners/{name}/check", func(r *http.Request) (any, error) { return b.CheckRunner(r.PathValue("name")) })

	guarded := guard(mux, valid)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name, ok := pages[r.URL.Path]; ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			servePage(w, r, name)
			return
		}
		guarded.ServeHTTP(w, r)
	})
}

// guard 在任何处理器之前校验来源和令牌,没有任何路径可以绕过。
func guard(next http.Handler, valid func(string) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 浏览器发起的跨站请求会带上 Origin。只接受与请求目标同源的页面,
		// 防止用户打开的恶意网页请求本机端口(C4)。命令行等非浏览器客户端不带这个头。
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
			writeError(w, &api.Error{Code: api.CodeForbidden, Message: "请求来源不被允许"})
			return
		}
		// 令牌只从请求头读取,不读 Cookie,也不读查询参数(C4)。
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" || !valid(token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, &api.Error{Code: api.CodeUnauthorized, Message: "缺少令牌或令牌无效,请在 Authorization 请求头中携带 Bearer 令牌"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

func decodeTask(r *http.Request) (task.Task, error) {
	var t task.Task
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // 写错的字段名直接报错,而不是被悄悄忽略
	if err := dec.Decode(&t); err != nil {
		return t, &api.Error{Code: api.CodeInvalid, Message: "任务定义不是合法的 JSON: " + err.Error()}
	}
	return t, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var statuses = map[string]int{
	api.CodeUnauthorized: http.StatusUnauthorized,
	api.CodeForbidden:    http.StatusForbidden,
	api.CodeNotFound:     http.StatusNotFound,
	api.CodeConflict:     http.StatusConflict,
	api.CodeInvalid:      http.StatusBadRequest,
}

func writeError(w http.ResponseWriter, err error) {
	var e *api.Error
	if !errors.As(err, &e) {
		e = &api.Error{Code: api.CodeInternal, Message: err.Error()}
	}
	status, ok := statuses[e.Code]
	if !ok {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, api.ErrorResponse{Error: e})
}
