// Package deliver 把一次运行的结果投递到 webhook(N3):请求体的格式、发送和重试间隔。
// 何时投递、投递记录的保存由后台负责,这里不读写任何文件。
package deliver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"bugu/internal/i18n"
	"bugu/internal/notify"
	"bugu/internal/task"
)

// Payload 是 webhook 请求体(决策记录 0013)。
type Payload struct {
	Event      string    `json:"event"` // 目前只有 "run.finished"
	Task       string    `json:"task"`
	RunID      string    `json:"run_id"`
	Status     string    `json:"status"`
	NoContent  bool      `json:"no_content"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Summary    string    `json:"summary"`
	Artifacts  []string  `json:"artifacts"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
}

// NewPayload 按运行结果生成请求体,标题和正文与系统通知相同(N5)。
func NewPayload(r task.Run) Payload {
	m := notify.Compose(r)
	artifacts := r.Artifacts
	if artifacts == nil {
		artifacts = []string{}
	}
	return Payload{
		Event: "run.finished", Task: r.Task, RunID: r.ID, Status: string(r.Status), NoContent: r.NoContent,
		Title: m.Title, Body: m.Body, Summary: r.Summary, Artifacts: artifacts, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
}

// Permanent 表示重试也不会成功的失败,如地址不存在的 404。
type Permanent struct{ error }

func (p Permanent) Unwrap() error { return p.error }

// Send 发送一次。返回 nil 表示对方以 2xx 接收。key 放进 Idempotency-Key 请求头,
// 重试时不变,接收方据此去重。ctx 结束时放弃这次请求。
func Send(ctx context.Context, c *http.Client, url, key string, p Payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return Permanent{err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Permanent{err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("User-Agent", "bugu")
	resp, err := c.Do(req)
	if err != nil {
		return i18n.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	err = i18n.Errorf("对方返回 %s", resp.Status)
	// 对方的回复会显示在终端和网页里,去掉控制字符,免得带进终端转义序列。
	if s := strings.TrimSpace(printable(string(snippet))); s != "" {
		err = fmt.Errorf("%w:%s", err, s)
	}
	// 4xx 说明请求本身不被接受,重试也一样;超时和限流例外。
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
		return Permanent{err}
	}
	return err
}

// Timeout 是一次请求的时长上限。
const Timeout = 10 * time.Second

// retryAfter 是第 n 次失败之后到下一次尝试的间隔;用完即不再重试,一共尝试 len+1 次。
var retryAfter = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour}

// Backoff 返回已经失败 attempts 次之后,隔多久再试;不再重试时返回 false。
func Backoff(attempts int) (time.Duration, bool) {
	if attempts < 1 || attempts > len(retryAfter) {
		return 0, false
	}
	return retryAfter[attempts-1], true
}

// printable 把控制字符换成空格,换行和制表符也算在内。
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return ' '
		}
		return r
	}, s)
}
