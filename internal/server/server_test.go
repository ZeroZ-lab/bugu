package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bugu/internal/api"
	"bugu/internal/task"
)

const token = "secret-token"

// fake 记录收到的调用,并返回预设的结果。
type fake struct {
	calls   []string
	created task.Task
	key     string
	limit   int
	err     error
}

func (f *fake) call(name string) error { f.calls = append(f.calls, name); return f.err }

func (f *fake) Tasks() ([]api.TaskInfo, error)         { return nil, f.call("Tasks") }
func (f *fake) Task(name string) (api.TaskInfo, error) { return api.TaskInfo{}, f.call("Task " + name) }
func (f *fake) CreateTask(t task.Task) (api.TaskInfo, error) {
	f.created = t
	return api.TaskInfo{Task: t}, f.call("CreateTask")
}
func (f *fake) UpdateTask(name string, t task.Task) (api.TaskInfo, error) {
	return api.TaskInfo{Task: t}, f.call("UpdateTask " + name)
}
func (f *fake) DeleteTask(name string) error { return f.call("DeleteTask " + name) }
func (f *fake) SetPaused(name string, paused bool) (api.TaskInfo, error) {
	if paused {
		return api.TaskInfo{}, f.call("Pause " + name)
	}
	return api.TaskInfo{}, f.call("Resume " + name)
}
func (f *fake) RunNow(name, key string) (task.Run, error) {
	f.key = key
	return task.Run{ID: "r1", Task: name}, f.call("RunNow " + name)
}
func (f *fake) Runs(taskName string, limit int) ([]task.Run, error) {
	f.limit = limit
	return nil, f.call("Runs " + taskName)
}
func (f *fake) Run(id string) (task.Run, error) { return task.Run{ID: id}, f.call("Run " + id) }
func (f *fake) Logs(id string) (api.Logs, error) {
	return api.Logs{Stdout: "out"}, f.call("Logs " + id)
}

func (f *fake) Runners() ([]api.Runner, error) { return nil, f.call("Runners") }
func (f *fake) CheckRunner(name string) (api.RunnerCheck, error) {
	return api.RunnerCheck{Name: name, OK: true}, f.call("CheckRunner " + name)
}

func serve(f *fake) http.Handler { return New(f, func(got string) bool { return got == token }) }

func do(h http.Handler, method, path, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, api.Prefix+path, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var authed = map[string]string{"Authorization": "Bearer " + token}

var routes = []struct{ method, path, call string }{
	{"GET", "/health", ""},
	{"GET", "/tasks", "Tasks"},
	{"POST", "/tasks", "CreateTask"},
	{"GET", "/tasks/backup", "Task backup"},
	{"PUT", "/tasks/backup", "UpdateTask backup"},
	{"DELETE", "/tasks/backup", "DeleteTask backup"},
	{"POST", "/tasks/backup/pause", "Pause backup"},
	{"POST", "/tasks/backup/resume", "Resume backup"},
	{"POST", "/tasks/backup/run", "RunNow backup"},
	{"GET", "/runs", "Runs "},
	{"GET", "/runs/r1", "Run r1"},
	{"GET", "/runs/r1/logs", "Logs r1"},
	{"GET", "/runners", "Runners"},
	{"POST", "/runners/stats/check", "CheckRunner stats"},
}

// C1/C4: 每个接口在没有有效令牌时都被拒绝,并且后台逻辑不会被执行。
// 令牌只认 Authorization 请求头:放在 Cookie 或查询参数里无效。
func TestC1C4EveryRouteRequiresHeaderToken(t *testing.T) {
	attempts := map[string]map[string]string{
		"不带令牌":      nil,
		"错误的令牌":     {"Authorization": "Bearer wrong"},
		"空令牌":       {"Authorization": "Bearer "},
		"不是 Bearer": {"Authorization": token},
		"放在 Cookie": {"Cookie": "token=" + token},
	}
	for _, rt := range append(routes, struct{ method, path, call string }{"GET", "/no-such-route", ""}) {
		for name, header := range attempts {
			f := &fake{}
			rec := do(serve(f), rt.method, rt.path, "{}", header)
			if rec.Code != http.StatusUnauthorized || len(f.calls) != 0 {
				t.Errorf("%s %s(%s): 状态 %d,后台调用 %v", rt.method, rt.path, name, rec.Code, f.calls)
			}
		}
		f := &fake{}
		if rec := do(serve(f), rt.method, rt.path+"?token="+token, "{}", nil); rec.Code != http.StatusUnauthorized || len(f.calls) != 0 {
			t.Errorf("%s %s(查询参数): 状态 %d", rt.method, rt.path, rec.Code)
		}
	}
}

// C4: 来自其他站点页面的请求即使带着正确的令牌也被拒绝;同源页面和非浏览器客户端可以通过。
func TestC4CrossOriginRequestsAreRejected(t *testing.T) {
	for origin, want := range map[string]int{
		"https://evil.example":            http.StatusForbidden,
		"http://example.com.evil.example": http.StatusForbidden,
		"null":                            http.StatusForbidden,
		"http://example.com":              http.StatusOK, // httptest 请求的 Host 是 example.com
		"":                                http.StatusOK,
	} {
		f := &fake{}
		header := map[string]string{"Authorization": "Bearer " + token}
		if origin != "" {
			header["Origin"] = origin
		}
		rec := do(serve(f), "POST", "/tasks/backup/run", "", header)
		if rec.Code != want || (want != http.StatusOK && len(f.calls) != 0) {
			t.Errorf("Origin %q: 状态 %d,期望 %d,后台调用 %v", origin, rec.Code, want, f.calls)
		}
	}
}

func TestRoutesReachBackend(t *testing.T) {
	for _, rt := range routes {
		f := &fake{}
		rec := do(serve(f), rt.method, rt.path, `{"name":"backup"}`, authed)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s %s: 状态 %d %s", rt.method, rt.path, rec.Code, rec.Body)
		}
		if rt.call != "" && (len(f.calls) != 1 || f.calls[0] != rt.call) {
			t.Errorf("%s %s: 后台调用 %v,期望 %q", rt.method, rt.path, f.calls, rt.call)
		}
	}
}

// 空列表序列化为 [] 而不是 null,客户端不需要特殊处理。
func TestEmptyListsAreArrays(t *testing.T) {
	for _, path := range []string{"/tasks", "/runs"} {
		if body := strings.TrimSpace(do(serve(&fake{}), "GET", path, "", authed).Body.String()); body != "[]" {
			t.Errorf("%s 返回 %s", path, body)
		}
	}
}

// C8: 幂等标识和查询条件原样交给后台。
func TestC8KeyAndQueryArePassedThrough(t *testing.T) {
	f := &fake{}
	header := map[string]string{"Authorization": "Bearer " + token, api.KeyHeader: "k-1"}
	do(serve(f), "POST", "/tasks/backup/run", "", header)
	do(serve(f), "GET", "/runs?task=backup&limit=5", "", authed)
	if f.key != "k-1" || f.limit != 5 || f.calls[1] != "Runs backup" {
		t.Fatalf("key=%q limit=%d calls=%v", f.key, f.limit, f.calls)
	}
}

// 错误信息要具体到能据此修正(7.7):错误类别映射到状态码,响应体带代码和说明。
func TestErrorsCarryCodeAndMessage(t *testing.T) {
	tests := []struct {
		name         string
		method, path string
		body         string
		backend      error
		status       int
		code, hint   string
	}{
		{"不存在", "GET", "/tasks/nope", "", &api.Error{Code: api.CodeNotFound, Message: "没有名为 nope 的任务"}, 404, api.CodeNotFound, "nope"},
		{"冲突", "POST", "/tasks", `{}`, &api.Error{Code: api.CodeConflict, Message: "已存在"}, 409, api.CodeConflict, "已存在"},
		{"校验失败", "POST", "/tasks", `{}`, &api.Error{Code: api.CodeInvalid, Message: "cron 表达式有误"}, 400, api.CodeInvalid, "cron"},
		{"内部错误", "GET", "/tasks", "", errString("磁盘已满"), 500, api.CodeInternal, "磁盘已满"},
		{"不是 JSON", "POST", "/tasks", `{`, nil, 400, api.CodeInvalid, "JSON"},
		{"未知字段", "POST", "/tasks", `{"nmae":"x"}`, nil, 400, api.CodeInvalid, "nmae"},
		{"limit 有误", "GET", "/runs?limit=many", "", nil, 400, api.CodeInvalid, "limit"},
		{"请求体过大", "POST", "/tasks", `{"name":"` + strings.Repeat("a", maxBody) + `"}`, nil, 400, api.CodeInvalid, "JSON"},
	}
	for _, tt := range tests {
		rec := do(serve(&fake{err: tt.backend}), tt.method, tt.path, tt.body, authed)
		var resp api.ErrorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Error == nil {
			t.Errorf("%s: 响应体不是错误格式: %s", tt.name, rec.Body)
			continue
		}
		if rec.Code != tt.status || resp.Error.Code != tt.code || !strings.Contains(resp.Error.Message, tt.hint) {
			t.Errorf("%s: 状态 %d 代码 %q 说明 %q", tt.name, rec.Code, resp.Error.Code, resp.Error.Message)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// 决策记录 0007: 前端的静态文件不需要令牌,带着限制页面行为的响应头;
// 除了列出的这几个文件,其他任何路径仍然要令牌,也不能用静态文件的路径绕到接口上。
func TestStaticPagesAreServedWithoutTokenAndNothingElseIs(t *testing.T) {
	get := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		serve(&fake{}).ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	for path, contentType := range map[string]string{"/": "text/html", "/app.js": "javascript", "/style.css": "text/css"} {
		rec := get("GET", path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), contentType) || rec.Body.Len() == 0 {
			t.Errorf("GET %s: 状态 %d 类型 %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		csp := rec.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'self'") ||
			!strings.Contains(csp, "frame-ancestors 'none'") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: 响应头不完整: %v", path, rec.Header())
		}
	}
	for _, req := range [][2]string{
		{"GET", "/index.html"}, {"GET", "/static/app.js"}, {"GET", "/app.js/../api/v1/tasks"}, {"GET", "/favicon.ico"},
		{"POST", "/"}, {"DELETE", "/app.js"}, {"GET", "/api/v1/tasks"}, {"GET", "/api/v1/../v1/tasks"},
	} {
		if rec := get(req[0], req[1]); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: 状态 %d,应要求令牌", req[0], req[1], rec.Code)
		}
	}
}

// 页面里不能有内联脚本和内联样式,否则会被内容安全策略拦下而悄悄失效。
func TestPagesHaveNoInlineScriptOrStyle(t *testing.T) {
	html, err := static.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<script>", "<style", " style=", " onclick=", "javascript:"} {
		if strings.Contains(string(html), bad) {
			t.Errorf("index.html 含有 %q", bad)
		}
	}
}
