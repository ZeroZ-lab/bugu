// Package server 把后台的能力暴露为 HTTP JSON 接口,并负责令牌认证和请求来源校验。
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"bugu/internal/api"
	"bugu/internal/i18n"
	"bugu/internal/task"
)

// Backend 是接口背后的实现。出错时返回 *api.Error 以指明错误类别,其他错误一律视为内部错误。
type Backend interface {
	Health() api.Health
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
	// Inbox 返回收件箱。all 为假时只列未读的(G3)。
	Inbox(all bool) (api.Inbox, error)
	MarkRead(id string) (task.Run, error)
	MarkAllRead() (int, error)
	// Deliveries 返回一次运行的通知投递记录(N7)。
	Deliveries(id string) ([]task.Delivery, error)
	Runners() ([]api.Runner, error)
	// CheckRunner 实际启动一次运行器的检查命令,可能需要几秒。
	CheckRunner(name string) (api.RunnerCheck, error)
	Settings() (api.Settings, error)
	// 访问令牌(C5)和登记的其他主机(G5)只对本机令牌开放,见 Local。
	Tokens() ([]api.Token, error)
	CreateToken(name string) (api.NewToken, error)
	RevokeToken(name string) error
	// Hosts 列出登记的其他主机;probe 为真时逐个检查连接状态(G14),可能要几秒。
	Hosts(probe bool) ([]api.Host, error)
	PutHost(h api.Host) (api.Host, error)
	RemoveHost(name string) error
	// SaveRunner 保存一份用户自己的运行器配置(bugu runner add)。
	// raw 是运行器配置的 JSON,格式见 internal/runner。
	SaveRunner(name string, raw []byte) (api.Runner, error)
	// Plan 说明立即运行一次会执行什么,但不运行(bugu run --dry-run)。
	Plan(name string) (api.Plan, error)
	// UpdateSettings 保存设置并立即生效,返回生效后的设置。
	UpdateSettings(s api.Settings) (api.Settings, error)
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

// Access 是一个令牌能访问到的程度(决策记录 0015)。两级只是防误操作的分隔:
// Shared 能建任务,也就能执行任意命令,与 Local 同等信任(决策记录 0019)。
type Access int

const (
	Denied Access = iota // 无效的令牌
	Shared               // 另外创建的令牌:管理任务和运行
	Local                // 本机令牌:此外还能管理令牌、查看登记的其他主机(其中有那些主机的令牌)
)

type accessKey struct{}

// New 返回提供全部接口和前端页面的处理器。check 判断令牌能访问到什么程度;
// 除了几个固定的静态文件,每个请求都必须通过它(C1,决策记录 0007)。
func New(b Backend, check func(token string) Access) http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, fn func(r *http.Request) (any, error)) {
		method, path, _ := strings.Cut(pattern, " ")
		local := strings.HasPrefix(path, "/tokens") || strings.HasPrefix(path, "/hosts")
		mux.HandleFunc(method+" "+api.Prefix+path, func(w http.ResponseWriter, r *http.Request) {
			if local && r.Context().Value(accessKey{}) != Local {
				writeError(w, r, api.Wrap(api.CodeForbidden, i18n.Errorf("令牌和其他主机的登记只能用本机令牌管理,也就是在那台主机上执行命令")))
				return
			}
			v, err := fn(r)
			if err != nil {
				writeError(w, r, err)
				return
			}
			// 带有后台生成的说明文字的响应,按请求的语言说出来(决策记录 0014)。
			if l, ok := v.(interface{ Localized(i18n.Lang) any }); ok {
				v = l.Localized(RequestLang(r))
			}
			writeJSON(w, http.StatusOK, v)
		})
	}

	handle("GET /health", func(*http.Request) (any, error) { return b.Health(), nil })
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
				return nil, api.Wrap(api.CodeInvalid, i18n.Errorf("limit 应为非负整数,收到 %q", s))
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

	handle("GET /runs/{id}/deliveries", func(r *http.Request) (any, error) { return b.Deliveries(r.PathValue("id")) })
	handle("POST /runs/{id}/read", func(r *http.Request) (any, error) { return b.MarkRead(r.PathValue("id")) })
	handle("GET /inbox", func(r *http.Request) (any, error) { return b.Inbox(r.URL.Query().Get("all") == "1") })
	handle("POST /inbox/read", func(*http.Request) (any, error) {
		n, err := b.MarkAllRead()
		return api.MarkedRead{Marked: n}, err
	})

	handle("GET /settings", func(*http.Request) (any, error) { return b.Settings() })
	handle("GET /tokens", func(*http.Request) (any, error) { return b.Tokens() })
	handle("POST /tokens", func(r *http.Request) (any, error) {
		var in struct {
			Name string `json:"name"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return b.CreateToken(in.Name)
	})
	handle("DELETE /tokens/{name}", func(r *http.Request) (any, error) {
		return struct{}{}, b.RevokeToken(r.PathValue("name"))
	})
	handle("GET /hosts", func(r *http.Request) (any, error) {
		list, err := b.Hosts(r.URL.Query().Get("probe") != "0")
		if list == nil {
			list = []api.Host{}
		}
		return list, err
	})
	handle("PUT /hosts/{name}", func(r *http.Request) (any, error) {
		var h api.Host
		if err := decode(r, &h); err != nil {
			return nil, err
		}
		h.Name = r.PathValue("name")
		return b.PutHost(h)
	})
	handle("DELETE /hosts/{name}", func(r *http.Request) (any, error) {
		return struct{}{}, b.RemoveHost(r.PathValue("name"))
	})
	handle("PUT /runners/{name}", func(r *http.Request) (any, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		return b.SaveRunner(r.PathValue("name"), raw)
	})
	handle("GET /tasks/{name}/plan", func(r *http.Request) (any, error) { return b.Plan(r.PathValue("name")) })
	handle("PUT /settings", func(r *http.Request) (any, error) {
		var s api.Settings
		if err := decode(r, &s); err != nil {
			return nil, err
		}
		return b.UpdateSettings(s)
	})
	handle("GET /runners", func(*http.Request) (any, error) {
		runners, err := b.Runners()
		if runners == nil {
			runners = []api.Runner{}
		}
		return runners, err
	})
	handle("POST /runners/{name}/check", func(r *http.Request) (any, error) { return b.CheckRunner(r.PathValue("name")) })

	guarded := guard(mux, check)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name, ok := pages[r.URL.Path]; ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			servePage(w, r, name)
			return
		}
		guarded.ServeHTTP(w, r)
	})
}

// guard 在任何处理器之前校验来源和令牌,没有任何路径可以绕过。
func guard(next http.Handler, check func(string) Access) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 浏览器发起的跨站请求会带上 Origin。只接受与请求目标同源的页面,
		// 防止用户打开的恶意网页请求本机端口(C4)。命令行等非浏览器客户端不带这个头。
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
			writeError(w, r, api.Wrap(api.CodeForbidden, i18n.Errorf("请求来源不被允许")))
			return
		}
		// 令牌只从请求头读取,不读 Cookie,也不读查询参数(C4)。
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		access := Denied
		if ok && token != "" {
			access = check(token)
		}
		if access == Denied {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, r, api.Wrap(api.CodeUnauthorized, i18n.Errorf("缺少令牌或令牌无效,请在 Authorization 请求头中携带 Bearer 令牌")))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accessKey{}, access)))
	})
}

// decode 读取请求体里的 JSON。写错的字段名直接报错,而不是被悄悄忽略。
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return api.Wrap(api.CodeInvalid, i18n.Errorf("请求体不是合法的 JSON: %v", err))
	}
	return nil
}

func decodeTask(r *http.Request) (task.Task, error) {
	var t task.Task
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // 写错的字段名直接报错,而不是被悄悄忽略
	if err := dec.Decode(&t); err != nil {
		return t, api.Wrap(api.CodeInvalid, i18n.Errorf("任务定义不是合法的 JSON: %v", err))
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

// writeError 写出错误响应。说明文字用请求的 Accept-Language 指定的语言,没有指定时用后台的语言(决策记录 0014)。
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	out := &api.Error{Code: api.CodeInternal, Message: i18n.Localize(err, RequestLang(r))}
	if e := (*api.Error)(nil); errors.As(err, &e) {
		out.Code = e.Code
	}
	status, ok := statuses[out.Code]
	if !ok {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, api.ErrorResponse{Error: out})
}

// RequestLang 返回请求希望的语言。
func RequestLang(r *http.Request) i18n.Lang {
	if l, ok := i18n.ParseAccept(r.Header.Get("Accept-Language")); ok {
		return l
	}
	return i18n.Current()
}
