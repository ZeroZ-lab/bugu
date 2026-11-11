// Package client 是后台 HTTP 接口的 Go 封装。命令行和其他界面都通过它访问后台,
// 自己不读写任务文件。
package client

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// ErrNotRunning 表示在数据目录里找不到正在运行的本机后台。
var ErrNotRunning = errors.New("后台没有在运行,请先执行 bugu serve")

// Local 返回连接本机后台的客户端:地址和令牌读自数据目录(PRD 5.4)。
func Local(home string) (*Client, error) {
	raw, err := os.ReadFile(filepath.Join(home, api.EndpointFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotRunning
	} else if err != nil {
		return nil, err
	}
	var endpoint api.Endpoint
	if err := json.Unmarshal(raw, &endpoint); err != nil || endpoint.Addr == "" {
		return nil, fmt.Errorf("无法解析 %s", filepath.Join(home, api.EndpointFile))
	}
	token, err := os.ReadFile(filepath.Join(home, api.TokenFile))
	if err != nil {
		return nil, fmt.Errorf("读取本机令牌: %w", err)
	}
	return New(endpoint.Addr, strings.TrimSpace(string(token))), nil
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
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接后台 %s: %w", strings.TrimSuffix(c.base, api.Prefix), err)
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
		return fmt.Errorf("后台返回 %s", resp.Status)
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
