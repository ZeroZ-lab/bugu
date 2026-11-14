//go:build darwin || linux

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"bugu/internal/clock"
	"bugu/internal/deliver"
	"bugu/internal/notify"
	"bugu/internal/task"
)

// hook 是测试用的 webhook 接收方:按 codes 依次返回状态码,用完之后一直返回最后一个。
type hook struct {
	mu       sync.Mutex
	codes    []int
	keys     []string
	payloads []deliver.Payload
	srv      *httptest.Server
}

func newHook(t *testing.T, codes ...int) *hook {
	h := &hook{codes: codes}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		var p deliver.Payload
		_ = json.NewDecoder(r.Body).Decode(&p)
		h.payloads = append(h.payloads, p)
		h.keys = append(h.keys, r.Header.Get("Idempotency-Key"))
		code := h.codes[min(len(h.payloads), len(h.codes))-1]
		w.WriteHeader(code)
		if code >= 400 {
			w.Write([]byte("暂时不行"))
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.payloads)
}

func withWebhook(tk task.Task, url string) task.Task {
	tk.Notifications = append(tk.Notifications, task.Notification{Channel: task.ChannelWebhook, URL: url, When: task.NotifyAlways})
	return tk
}

func deliveries(t *testing.T, d *Daemon, runID string) []task.Delivery {
	t.Helper()
	list, err := d.engine.Deliveries(runID)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// N3 N7: 运行结束后投递 webhook;失败按间隔重试,只重试投递不重跑任务;每次投递有记录,
// 重试时去重标识不变。系统通知也留一条记录。
func TestN3N7WebhookRetriesUntilDelivered(t *testing.T) {
	h := newHook(t, 500, 503, 200)
	f := clock.NewFake(epoch)
	var shown []notify.Message
	d, err := Start(Options{Home: t.TempDir(), Listen: "127.0.0.1:0", Clock: f, Notify: func(m notify.Message) { shown = append(shown, m) }})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tk := command("report", "0 2 * * *", "echo 两条新闻")
	tk.Notifications = []task.Notification{{Channel: task.ChannelSystem}}
	create(t, d, withWebhook(tk, h.srv.URL+"/hook"))

	r, err := d.engine.RunNow("report", "")
	if err != nil {
		t.Fatal(err)
	}
	d.Wait()
	list := deliveries(t, d, r.ID)
	if len(list) != 2 || list[0].Status != task.DeliveryDelivered || len(shown) != 1 {
		t.Fatalf("系统通知: %+v shown=%v", list, shown)
	}
	w := list[1]
	if w.Status != task.DeliveryPending || w.Attempts != 1 || !strings.Contains(w.Error, "500") || !w.NextAttempt.Equal(epoch.Add(30*time.Second)) {
		t.Fatalf("第一次失败后: %+v", w)
	}

	f.Advance(30 * time.Second)
	d.engine.deliverDue()
	if w = deliveries(t, d, r.ID)[1]; w.Attempts != 2 || !w.NextAttempt.Equal(epoch.Add(30*time.Second+2*time.Minute)) {
		t.Fatalf("第二次失败后: %+v", w)
	}
	f.Advance(time.Minute) // 还没到时间,不重试
	d.engine.deliverDue()
	if h.count() != 2 {
		t.Fatalf("没到时间就重试了: %d 次", h.count())
	}
	f.Advance(time.Minute)
	d.engine.deliverDue()
	if w = deliveries(t, d, r.ID)[1]; w.Status != task.DeliveryDelivered || w.Attempts != 3 || w.Error != "" {
		t.Fatalf("送达后: %+v", w)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.keys) != 3 || h.keys[0] != r.ID+"/1" || h.keys[2] != h.keys[0] {
		t.Fatalf("去重标识: %v", h.keys)
	}
	p := h.payloads[2]
	if p.Event != "run.finished" || p.Task != "report" || p.RunID != r.ID || p.Status != "succeeded" || p.Summary != "两条新闻" || p.Title != "report 完成" {
		t.Fatalf("请求体: %+v", p)
	}
	if runs := runs(t, d, "report"); len(runs) != 1 {
		t.Fatalf("投递失败不应重跑任务: %d 次运行", len(runs))
	}
}

// N7: 4xx 说明请求本身不被接受,不再重试;一直失败的重试用完后记为失败。
// 没有界面的后台,系统通知记为无处送达。
func TestN7PermanentFailureAndExhaustedRetries(t *testing.T) {
	gone, down := newHook(t, 404), newHook(t, 500)
	f := clock.NewFake(epoch)
	d := start(t, t.TempDir(), f)
	// 下面把时钟往前拨 10 小时;任务的定时选一个这期间不会到的,免得定时触发再跑一次、再发一轮。
	tk := command("a", "0 2 1 1 *", "true")
	tk.Notifications = []task.Notification{{Channel: task.ChannelSystem, When: task.NotifyAlways}}
	create(t, d, withWebhook(withWebhook(tk, gone.srv.URL), down.srv.URL))
	r, _ := d.engine.RunNow("a", "")
	d.Wait()
	list := deliveries(t, d, r.ID)
	if list[0].Status != task.DeliveryDropped || list[1].Status != task.DeliveryFailed || list[1].Attempts != 1 || !strings.Contains(list[1].Error, "404") {
		t.Fatalf("records=%+v", list)
	}
	for range 10 {
		f.Advance(time.Hour)
		d.engine.deliverDue()
	}
	if w := deliveries(t, d, r.ID)[2]; w.Status != task.DeliveryFailed || w.Attempts != 5 || down.count() != 5 {
		t.Fatalf("重试用完后: %+v,共请求 %d 次", w, down.count())
	}
	if n := len(runs(t, d, "a")); n != 1 {
		t.Fatalf("只应有手动的那一次运行: %d 次", n)
	}
}

// N7 与可靠性:后台重启后接着投递上次没发出去的通知。
func TestN7PendingDeliverySurvivesRestart(t *testing.T) {
	h := newHook(t, 500, 200)
	home, f := t.TempDir(), clock.NewFake(epoch)
	d, err := Start(Options{Home: home, Listen: "127.0.0.1:0", Clock: f})
	if err != nil {
		t.Fatal(err)
	}
	create(t, d, withWebhook(command("a", "0 2 * * *", "true"), h.srv.URL))
	r, _ := d.engine.RunNow("a", "")
	d.Wait()
	d.Close()

	f.Advance(time.Hour)
	d = start(t, home, f) // 启动时就把到期的投递发出去
	d.engine.deliverDue()
	if w := deliveries(t, d, r.ID)[0]; w.Status != task.DeliveryDelivered || w.Attempts != 2 {
		t.Fatalf("重启之后: %+v", w)
	}
}

// S4: 补跑策略。08:00 的任务,机器从前一天 01:00 睡到当天 09:00:
// 默认补跑最近一次;skip 不补,留一条"错过"的记录;window 在宽限内才补。
func TestS4CatchupPolicyPerTask(t *testing.T) {
	f := clock.NewFake(epoch)
	d := start(t, t.TempDir(), f)
	policies := map[string]task.Task{}
	for name, set := range map[string]func(*task.Task){
		"default": func(*task.Task) {},
		"skip":    func(t *task.Task) { t.OnMissed = task.MissedSkip },
		"wide":    func(t *task.Task) { t.OnMissed, t.MissedWindow = task.MissedWindow, task.Duration(2*time.Hour) },
		"narrow":  func(t *task.Task) { t.OnMissed, t.MissedWindow = task.MissedWindow, task.Duration(30*time.Minute) },
	} {
		tk := command(name, "0 8 * * *", "true")
		set(&tk)
		policies[name] = tk
		create(t, d, tk)
	}
	advance(d, f, at(12, 9, 0))
	settle(d, f)
	for name, want := range map[string]task.Status{"default": task.Succeeded, "skip": task.Missed, "wide": task.Succeeded, "narrow": task.Missed} {
		list := runs(t, d, name)
		if len(list) != 1 || list[0].Status != want {
			t.Errorf("%s: %+v", name, list)
			continue
		}
		if want == task.Missed && (!strings.Contains(list[0].Note, "错过 2 次") || !list[0].ScheduledFor.Equal(at(12, 8, 0))) {
			t.Errorf("%s 的错过记录: %+v", name, list[0])
		}
	}
	// 按时的触发不受策略影响。
	advance(d, f, at(13, 8, 0))
	if list := runs(t, d, "skip"); list[0].Status != task.Succeeded {
		t.Fatalf("按时的触发: %+v", list[0])
	}
}

// E7: 任务的时区决定 cron 的含义:纽约的"每天 9 点"是 UTC 14:00。
func TestE7TaskTimezone(t *testing.T) {
	f := clock.NewFake(epoch)
	d := start(t, t.TempDir(), f)
	tk := command("ny", "0 9 * * *", "echo {date}")
	tk.Timezone = "America/New_York"
	create(t, d, tk)
	info, err := d.engine.Task("ny")
	if err != nil || !info.NextRun.Equal(time.Date(2026, 11, 11, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("下次运行 %v %v", info.NextRun, err)
	}
	advance(d, f, at(11, 9, 0)) // UTC 9 点,纽约还是凌晨,不触发
	if len(runs(t, d, "ny")) != 0 {
		t.Fatal("按 UTC 触发了")
	}
	advance(d, f, at(11, 14, 0))
	if list := runs(t, d, "ny"); len(list) != 1 || list[0].Status != task.Succeeded {
		t.Fatalf("runs=%+v", list)
	}
	bad := command("bad", "0 9 * * *", "true")
	bad.Timezone = "Mars/Olympus"
	if _, err := d.engine.CreateTask(bad); err == nil || !strings.Contains(err.Error(), "时区") {
		t.Fatalf("err=%v", err)
	}
}
