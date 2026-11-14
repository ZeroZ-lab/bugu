// Package client 是后台 HTTP 接口的 Go 封装。命令行和其他界面都通过它访问后台,
// 自己不读写任务文件。
package client

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bugu/internal/api"
	"bugu/internal/i18n"
	"bugu/internal/task"
)

type Client struct {
	base  string
	token string
	http  *http.Client
}

// New 返回连接指定后台的客户端。addr 可以是 "主机:端口",也可以带 http:// 或 https://。
func New(addr, token string) *Client {
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	return &Client{
		base: strings.TrimRight(addr, "/") + api.Prefix, token: token,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// ErrUnreachable 表示连不上后台:它没在运行、离线,或者地址不对(G14)。不说明那里的任务出了问题。
type ErrUnreachable struct {
	Addr string
	Err  error
}

func (e ErrUnreachable) Error() string {
	return i18n.T("连不上 %s:它可能已经停止、离线,或者地址不对(%v)", e.Addr, e.Err)
}

func (e ErrUnreachable) Unwrap() error { return e.Err }

// ErrNotRunning 表示在数据目录里找不到正在运行的本机后台。
var ErrNotRunning = i18n.Errorf("后台没有在运行,请先执行 bugu serve")

// Local 返回连接本机后台的客户端:地址和令牌读自数据目录(PRD 5.4)。
func Local(home string) (*Client, error) {
	c, _, _, err := LocalEndpoint(home)
	return c, err
}

// LocalEndpoint 与 Local 相同,并一并返回本机后台的地址和令牌。
// 桌面外壳用它们拼出要在窗口里打开的网址。
func LocalEndpoint(home string) (c *Client, addr, token string, err error) {
	raw, err := os.ReadFile(filepath.Join(home, api.EndpointFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", "", ErrNotRunning
	} else if err != nil {
		return nil, "", "", err
	}
	var endpoint api.Endpoint
	if err := json.Unmarshal(raw, &endpoint); err != nil || endpoint.Addr == "" {
		return nil, "", "", i18n.Errorf("无法解析 %s", filepath.Join(home, api.EndpointFile))
	}
	secret, err := os.ReadFile(filepath.Join(home, api.TokenFile))
	if err != nil {
		return nil, "", "", i18n.Errorf("读取本机令牌: %w", err)
	}
	token = strings.TrimSpace(string(secret))
	return New(endpoint.Addr, token), endpoint.Addr, token, nil
}

// do 发出一次请求。后台返回的错误以 *api.Error 的形式交给调用者。
func (c *Client) do(method, path string, header map[string]string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	// 后台按这个语言写错误信息,和命令行自己说的语言一致(决策记录 0014)。
	req.Header.Set("Accept-Language", string(i18n.Current()))
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrUnreachable{Addr: strings.TrimSuffix(c.base, api.Prefix), Err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e api.ErrorResponse
		if json.Unmarshal(raw, &e) == nil && e.Error != nil {
			return e.Error
		}
		return i18n.Errorf("后台返回 %s", resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func taskPath(name string, more ...string) string {
	return "/tasks/" + url.PathEscape(name) + strings.Join(more, "")
}

func (c *Client) Health() error { return c.do("GET", "/health", nil, nil, nil) }

// About 返回后台的健康检查结果,其中有程序的版本。
func (c *Client) About() (h api.Health, err error) { return h, c.do("GET", "/health", nil, nil, &h) }

func (c *Client) Tasks() (tasks []api.TaskInfo, err error) {
	return tasks, c.do("GET", "/tasks", nil, nil, &tasks)
}

func (c *Client) Task(name string) (info api.TaskInfo, err error) {
	return info, c.do("GET", taskPath(name), nil, nil, &info)
}

func (c *Client) CreateTask(t task.Task) (info api.TaskInfo, err error) {
	return info, c.do("POST", "/tasks", nil, t, &info)
}

func (c *Client) UpdateTask(t task.Task) (info api.TaskInfo, err error) {
	return info, c.do("PUT", taskPath(t.Name), nil, t, &info)
}

func (c *Client) DeleteTask(name string) error { return c.do("DELETE", taskPath(name), nil, nil, nil) }

func (c *Client) Pause(name string) (info api.TaskInfo, err error) {
	return info, c.do("POST", taskPath(name, "/pause"), nil, nil, &info)
}

func (c *Client) Resume(name string) (info api.TaskInfo, err error) {
	return info, c.do("POST", taskPath(name, "/resume"), nil, nil, &info)
}

func (c *Client) Runners() (runners []api.Runner, err error) {
	return runners, c.do("GET", "/runners", nil, nil, &runners)
}

// CheckRunner 让后台检查一个运行器能否使用,可能需要几秒。
func (c *Client) CheckRunner(name string) (check api.RunnerCheck, err error) {
	return check, c.do("POST", "/runners/"+url.PathEscape(name)+"/check", nil, nil, &check)
}

// NewKey 生成一个幂等标识。
func NewKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand 不会失败
	}
	return hex.EncodeToString(b[:])
}

// RunNow 立即运行一次任务。带同一个 key 重试不会重复触发(C8);key 为空时不做去重。
func (c *Client) RunNow(name, key string) (r task.Run, err error) {
	var header map[string]string
	if key != "" {
		header = map[string]string{api.KeyHeader: key}
	}
	return r, c.do("POST", taskPath(name, "/run"), header, nil, &r)
}

// Runs 返回运行记录,最新的在前。taskName 为空表示全部任务,limit 为 0 表示不限数量。
func (c *Client) Runs(taskName string, limit int) (runs []task.Run, err error) {
	q := url.Values{}
	if taskName != "" {
		q.Set("task", taskName)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/runs"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return runs, c.do("GET", path, nil, nil, &runs)
}

func (c *Client) Run(id string) (r task.Run, err error) {
	return r, c.do("GET", "/runs/"+url.PathEscape(id), nil, nil, &r)
}

func (c *Client) Logs(id string) (logs api.Logs, err error) {
	return logs, c.do("GET", "/runs/"+url.PathEscape(id)+"/logs", nil, nil, &logs)
}

// Inbox 返回收件箱。all 为假时只列未读的结果。
func (c *Client) Inbox(all bool) (in api.Inbox, err error) {
	path := "/inbox"
	if all {
		path += "?all=1"
	}
	return in, c.do("GET", path, nil, nil, &in)
}

// Settings 返回后台的设置。
func (c *Client) Settings() (s api.Settings, err error) {
	return s, c.do("GET", "/settings", nil, nil, &s)
}

// UpdateSettings 修改后台的设置。
func (c *Client) UpdateSettings(in api.Settings) (s api.Settings, err error) {
	return s, c.do("PUT", "/settings", nil, in, &s)
}

// MarkRead 把一次运行标为已读。
func (c *Client) MarkRead(id string) (r task.Run, err error) {
	return r, c.do("POST", "/runs/"+url.PathEscape(id)+"/read", nil, nil, &r)
}

// MarkAllRead 把收件箱里全部未读结果标为已读,返回标记的条数。
func (c *Client) MarkAllRead() (int, error) {
	var m api.MarkedRead
	return m.Marked, c.do("POST", "/inbox/read", nil, nil, &m)
}

// Deliveries 返回一次运行的通知投递记录。
func (c *Client) Deliveries(id string) (list []task.Delivery, err error) {
	return list, c.do("GET", "/runs/"+url.PathEscape(id)+"/deliveries", nil, nil, &list)
}

// Tokens 列出访问令牌(C5)。只有本机令牌能管理令牌。
func (c *Client) Tokens() (list []api.Token, err error) {
	return list, c.do("GET", "/tokens", nil, nil, &list)
}

// CreateToken 创建一个令牌,返回的明文之后无法再查看。
func (c *Client) CreateToken(name string) (t api.NewToken, err error) {
	return t, c.do("POST", "/tokens", nil, map[string]string{"name": name}, &t)
}

// RevokeToken 作废一个令牌。
func (c *Client) RevokeToken(name string) error {
	return c.do("DELETE", "/tokens/"+url.PathEscape(name), nil, nil, nil)
}

// SaveRunner 保存一份运行器配置(JSON),同名的替换。
func (c *Client) SaveRunner(name string, raw json.RawMessage) (r api.Runner, err error) {
	return r, c.do("PUT", "/runners/"+url.PathEscape(name), nil, raw, &r)
}

// Plan 说明立即运行一次会执行什么,不运行。
func (c *Client) Plan(name string) (p api.Plan, err error) {
	return p, c.do("GET", taskPath(name, "/plan"), nil, nil, &p)
}

// WithTimeout 返回一个每次请求最多等 d 的客户端,用于检查另一台主机是否在线。
func (c *Client) WithTimeout(d time.Duration) *Client {
	cp := *c
	cp.http = &http.Client{Timeout: d}
	return &cp
}
