package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bugu/internal/api"
	"bugu/internal/i18n"
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
func (f *fake) Inbox(all bool) (api.Inbox, error) {
	return api.Inbox{}, f.call(fmt.Sprintf("Inbox %v", all))
}
func (f *fake) MarkRead(id string) (task.Run, error) {
	return task.Run{ID: id}, f.call("MarkRead " + id)
}
func (f *fake) MarkAllRead() (int, error) { return 0, f.call("MarkAllRead") }
func (f *fake) Deliveries(id string) ([]task.Delivery, error) {
	return nil, f.call("Deliveries " + id)
}
func (f *fake) CheckRunner(name string) (api.RunnerCheck, error) {
	return api.RunnerCheck{Name: name, OK: true, Detail: "命令 x 存在", Cause: i18n.Errorf("命令 %s 存在", "x")}, f.call("CheckRunner " + name)
}

func (f *fake) Health() api.Health { return api.Health{OK: true, Version: "test"} }
func (f *fake) Settings() (api.Settings, error) {
	return api.Settings{Language: "auto"}, f.call("Settings")
}
func (f *fake) UpdateSettings(s api.Settings) (api.Settings, error) {
	return s, f.call("UpdateSettings " + s.Language)
}

func (f *fake) Tokens() ([]api.Token, error) {
	return []api.Token{{Name: "local", Local: true}}, f.call("Tokens")
}
func (f *fake) CreateToken(name string) (api.NewToken, error) {
	return api.NewToken{Name: name, Token: "t"}, f.call("CreateToken " + name)
}
func (f *fake) RevokeToken(name string) error        { return f.call("RevokeToken " + name) }
func (f *fake) Hosts(probe bool) ([]api.Host, error) { return nil, f.call(fmt.Sprint("Hosts ", probe)) }
func (f *fake) PutHost(h api.Host) (api.Host, error) { return h, f.call("PutHost " + h.Name) }
func (f *fake) RemoveHost(name string) error         { return f.call("RemoveHost " + name) }
func (f *fake) SaveRunner(name string, raw []byte) (api.Runner, error) {
	return api.Runner{Name: name}, f.call("SaveRunner " + name + " " + string(raw))
}
func (f *fake) Plan(name string) (api.Plan, error) {
	return api.Plan{Task: name}, f.call("Plan " + name)
}

// sharedToken 是另外创建的令牌:能管理任务,不能管理令牌和主机(决策记录 0015)。
const sharedToken = "shared-token"

func serve(f *fake) http.Handler {
	return New(f, func(got string) Access {
		switch got {
		case token:
			return Local
		case sharedToken:
			return Shared
		}
		return Denied
	})
}

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
	{"POST", "/runs/r1/read", "MarkRead r1"},
	{"GET", "/runs/r1/deliveries", "Deliveries r1"},
	{"GET", "/inbox", "Inbox false"},
	{"GET", "/inbox?all=1", "Inbox true"},
	{"POST", "/inbox/read", "MarkAllRead"},
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

// 决策记录 0014:错误信息按请求头 Accept-Language 的语言写;没有这个头时用后台的语言。
func TestErrorsInRequestedLanguage(t *testing.T) {
	h := serve(&fake{})
	for lang, want := range map[string]string{"en-US,en;q=0.9": "limit should be a non-negative integer", "zh-CN": "limit 应为非负整数", "": "limit 应为非负整数"} {
		header := map[string]string{"Authorization": "Bearer " + token}
		if lang != "" {
			header["Accept-Language"] = lang
		}
		rec := do(h, "GET", "/runs?limit=many", "", header)
		var body api.ErrorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 400 || body.Error.Code != api.CodeInvalid || !strings.Contains(body.Error.Message, want) {
			t.Errorf("%q: %d %s", lang, rec.Code, rec.Body.String())
		}
	}
	// 令牌错误同样按请求的语言。
	rec := do(h, "GET", "/tasks", "", map[string]string{"Accept-Language": "en"})
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "missing or invalid token") {
		t.Errorf("令牌: %d %s", rec.Code, rec.Body.String())
	}
}

// 决策记录 0014:设置的读写。
func TestSettingsRoutes(t *testing.T) {
	f := &fake{}
	authed := map[string]string{"Authorization": "Bearer " + token}
	if rec := do(serve(f), "GET", "/settings", "", authed); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"language":"auto"`) {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(serve(f), "PUT", "/settings", `{"language":"en"}`, authed); rec.Code != 200 || f.calls[1] != "UpdateSettings en" {
		t.Fatalf("PUT: %d %s %v", rec.Code, rec.Body.String(), f.calls)
	}
	if rec := do(serve(f), "PUT", "/settings", `{"lang":"en"}`, authed); rec.Code != 400 {
		t.Fatalf("写错字段名: %d", rec.Code)
	}
}

// C5 决策记录 0015:另外创建的令牌能管理任务,但令牌和登记的其他主机只对本机令牌开放。
func TestSharedTokenCannotManageTokensOrHosts(t *testing.T) {
	f := &fake{}
	shared := map[string]string{"Authorization": "Bearer " + sharedToken}
	local := map[string]string{"Authorization": "Bearer " + token}
	if rec := do(serve(f), "GET", "/tasks", "", shared); rec.Code != 200 {
		t.Fatalf("另外创建的令牌应能查看任务: %d", rec.Code)
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/tokens", ""}, {"POST", "/tokens", `{"name":"ci"}`}, {"DELETE", "/tokens/ci", ""},
		{"GET", "/hosts", ""}, {"PUT", "/hosts/nas", `{"url":"http://x","token":"t"}`}, {"DELETE", "/hosts/nas", ""},
	} {
		if rec := do(serve(f), c.method, c.path, c.body, shared); rec.Code != 403 || !strings.Contains(rec.Body.String(), "forbidden") {
			t.Errorf("%s %s 用另外创建的令牌: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
		if rec := do(serve(f), c.method, c.path, c.body, local); rec.Code != 200 {
			t.Errorf("%s %s 用本机令牌: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if len(f.calls) != 7 { // 只有 GET /tasks 和本机令牌的 6 次到达了后台
		t.Errorf("calls=%v", f.calls)
	}
}

// 决策记录 0014:后台生成的运行器检查说明,也按请求的语言写。
func TestRunnerCheckInRequestedLanguage(t *testing.T) {
	for lang, want := range map[string]string{"en": `"detail":"command x exists"`, "zh": `"detail":"命令 x 存在"`} {
		rec := do(serve(&fake{}), "POST", "/runners/fake/check", "", map[string]string{"Authorization": "Bearer " + token, "Accept-Language": lang})
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", lang, rec.Code, rec.Body.String())
		}
	}
}

// G14:列主机时可以先不检查连接状态,页面先显示列表,状态随后补上。
func TestHostsWithoutProbe(t *testing.T) {
	f := &fake{}
	authed := map[string]string{"Authorization": "Bearer " + token}
	do(serve(f), "GET", "/hosts?probe=0", "", authed)
	do(serve(f), "GET", "/hosts", "", authed)
	if strings.Join(f.calls, ";") != "Hosts false;Hosts true" {
		t.Fatalf("calls=%v", f.calls)
	}
}
