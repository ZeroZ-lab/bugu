package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"bugu/internal/api"
	"bugu/internal/clock"
	"bugu/internal/exec"
	"bugu/internal/schedule"
	"bugu/internal/store"
	"bugu/internal/task"
)

// engine 是后台的核心:按时触发任务、管理进行中的运行、响应接口的请求。
// 它实现 server.Backend。
type engine struct {
	clock clock.Clock
	store *store.Store
	exec  exec.Executor
	ctx   context.Context // 后台关闭时取消,进行中的运行随之终止
	logf  func(format string, args ...any)

	mu      sync.Mutex
	since   map[string]time.Time // 每个任务已评估到的时间点
	running map[string]string    // 任务名 → 进行中的运行编号
	keys    map[string]string    // 幂等标识 → 运行编号
	wg      sync.WaitGroup       // 进行中的运行
}

// 启动时从最近这么多条运行记录里恢复幂等标识。
const recentRuns = 500

func newEngine(ctx context.Context, c clock.Clock, st *store.Store, logf func(string, ...any)) (*engine, error) {
	e := &engine{
		clock: c, store: st, exec: exec.Executor{Clock: c}, ctx: ctx, logf: logf,
		running: map[string]string{}, keys: map[string]string{},
	}
	// 上一个后台留下的"运行中"已经没有进程在管,标为中断(O3)。
	if n, err := st.InterruptUnfinished(c.Now()); err != nil {
		return nil, err
	} else if n > 0 {
		logf("上次退出时有 %d 次运行没有结束,已标为中断", n)
	}
	var err error
	if e.since, err = st.LoadState(); err != nil {
		return nil, fmt.Errorf("读取调度进度: %w", err)
	}
	runs, err := st.ListRuns("", recentRuns)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		if r.RequestKey != "" {
			e.keys[r.RequestKey] = r.ID
		}
	}
	return e, nil
}

// tick 评估所有任务,触发到点的。后台每隔一小段时间调用一次,启动时也调用一次以处理停机期间错过的。
func (e *engine) tick() {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.clock.Now()
	tasks, err := e.store.ListTasks()
	if err != nil {
		e.logf("读取任务失败: %v", err)
		return
	}
	for _, t := range tasks {
		if t.Paused {
			continue
		}
		since, seen := e.since[t.Name]
		if !seen { // 不经接口放进来的任务文件:从现在开始计时
			e.setSince(t.Name, now)
			continue
		}
		d, err := schedule.Evaluate(t.Trigger, since, now)
		if err != nil {
			e.logf("任务 %s 的触发定义有误: %v", t.Name, err)
			continue
		}
		if !d.Fire {
			continue
		}
		// 先保存进度再启动:宁可在保存和启动之间崩溃时少跑一次,也不在重启后重复触发(S7)。
		if !e.setSince(t.Name, now) {
			continue
		}
		note := ""
		if d.Missed > 0 {
			note = fmt.Sprintf("此前错过 %d 次触发,只补跑最近一次", d.Missed)
		}
		if _, err := e.start(t, task.BySchedule, d.ScheduledFor, note, ""); err != nil {
			e.logf("任务 %s 启动失败: %v", t.Name, err)
		}
	}
}

// setSince 更新并保存任务的评估点。调用者持有锁。
func (e *engine) setSince(name string, at time.Time) bool {
	if at.IsZero() {
		delete(e.since, name)
	} else {
		e.since[name] = at
	}
	if err := e.store.SaveState(e.since); err != nil {
		e.logf("保存调度进度失败: %v", err)
		return false
	}
	return true
}

// start 为任务建立一次运行并开始执行。调用者持有锁。
// 同一任务上一次还没结束时,定时触发记为跳过(S5),手动触发直接报冲突。
func (e *engine) start(t task.Task, trigger string, scheduledFor time.Time, note, key string) (task.Run, error) {
	now := e.clock.Now()
	r := task.Run{
		ID: store.NewRunID(now), Task: t.Name, Trigger: trigger, ScheduledFor: scheduledFor,
		Note: note, RequestKey: key, CreatedAt: now,
	}
	if busy, ok := e.running[t.Name]; ok {
		if trigger == task.ByManual {
			return r, &api.Error{Code: api.CodeConflict, Message: fmt.Sprintf("任务 %s 正在运行(运行编号 %s),结束后才能再次运行", t.Name, busy)}
		}
		r.Status, r.FinishedAt = task.Skipped, now
		r.Note = fmt.Sprintf("上一次运行 %s 尚未结束,本次跳过", busy)
		return r, e.store.CreateRun(r, t)
	}
	r.Status, r.StartedAt = task.Running, now
	if err := e.store.CreateRun(r, t); err != nil {
		return r, err
	}
	e.running[t.Name] = r.ID
	if key != "" {
		e.keys[key] = r.ID
	}
	e.wg.Add(1)
	go e.execute(r, t)
	return r, nil
}

func (e *engine) execute(r task.Run, t task.Task) {
	defer e.wg.Done()
	res, err := e.exec.Run(e.ctx, t)
	r.Status, r.Command, r.ExitCode, r.Stdout, r.Stderr = res.Status, res.Command, res.ExitCode, res.Stdout, res.Stderr
	if !res.StartedAt.IsZero() {
		r.StartedAt = res.StartedAt
	}
	if r.FinishedAt = res.FinishedAt; r.FinishedAt.IsZero() {
		r.FinishedAt = e.clock.Now()
	}
	if err != nil {
		r.Error = err.Error()
	}
	// 目前只有后台关闭会取消运行,所以记为中断而不是已取消(O3)。
	if r.Status == task.Canceled {
		r.Status, r.Error = task.Interrupted, "后台关闭,运行被终止"
	}
	if err := e.store.FinishRun(r); err != nil {
		e.logf("保存运行 %s 的结果失败: %v", r.ID, err)
	}
	e.mu.Lock()
	delete(e.running, t.Name)
	e.mu.Unlock()
}

func invalid(format string, args ...any) error {
	return &api.Error{Code: api.CodeInvalid, Message: fmt.Sprintf(format, args...)}
}

func noTask(name string) error {
	return &api.Error{Code: api.CodeNotFound, Message: fmt.Sprintf("没有名为 %q 的任务,可用 bugu list 查看现有任务", name)}
}

func noRun(id string) error {
	return &api.Error{Code: api.CodeNotFound, Message: fmt.Sprintf("没有编号为 %q 的运行,可用 bugu runs 查看运行记录", id)}
}

// check 在保存之前完整校验任务定义,错误说明要具体到能据此修正。
func (e *engine) check(t task.Task) error {
	if err := task.Validate(t); err != nil {
		return invalid("%v", err)
	}
	if err := schedule.Check(t.Trigger); err != nil {
		return invalid("%v", err)
	}
	if err := exec.Validate(t); err != nil {
		return invalid("%v", err)
	}
	return nil
}

// info 给任务定义补上运行时信息。调用者持有锁。
func (e *engine) info(t task.Task) api.TaskInfo {
	i := api.TaskInfo{Task: t}
	_, i.Running = e.running[t.Name]
	if !t.Paused {
		i.NextRun, _ = schedule.Next(t.Trigger, e.clock.Now())
	}
	return i
}

func (e *engine) load(name string) (task.Task, error) {
	t, err := e.store.LoadTask(name)
	if errors.Is(err, store.ErrNotFound) {
		return t, noTask(name)
	}
	return t, err
}

func (e *engine) Tasks() ([]api.TaskInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	tasks, err := e.store.ListTasks()
	if err != nil {
		return nil, err
	}
	infos := make([]api.TaskInfo, len(tasks))
	for i, t := range tasks {
		infos[i] = e.info(t)
	}
	return infos, nil
}

func (e *engine) Task(name string) (api.TaskInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, err := e.load(name)
	if err != nil {
		return api.TaskInfo{}, err
	}
	return e.info(t), nil
}

func sameDefinition(a, b task.Task) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func (e *engine) CreateTask(t task.Task) (api.TaskInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.check(t); err != nil {
		return api.TaskInfo{}, err
	}
	switch existing, err := e.store.LoadTask(t.Name); {
	case err == nil && sameDefinition(existing, t):
		return e.info(existing), nil // 重复提交同一个定义(C8)
	case err == nil:
		return api.TaskInfo{}, &api.Error{Code: api.CodeConflict, Message: fmt.Sprintf("已有名为 %q 的任务且定义不同;修改请用 bugu edit,或换一个名字", t.Name)}
	case !errors.Is(err, store.ErrNotFound):
		return api.TaskInfo{}, err
	}
	now := e.clock.Now()
	if !t.Trigger.At.IsZero() && !t.Trigger.At.After(now) {
		return api.TaskInfo{}, invalid("一次性时间点 %s 已经过去", t.Trigger.At.Format(time.RFC3339))
	}
	if err := e.store.SaveTask(t); err != nil {
		return api.TaskInfo{}, err
	}
	e.setSince(t.Name, now) // 创建之前的时间点不算错过
	return e.info(t), nil
}

func (e *engine) UpdateTask(name string, t task.Task) (api.TaskInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t.Name == "" {
		t.Name = name
	}
	if t.Name != name {
		return api.TaskInfo{}, invalid("不支持改名:请求修改的是 %q,定义里的名字是 %q", name, t.Name)
	}
	old, err := e.load(name)
	if err != nil {
		return api.TaskInfo{}, err
	}
	if err := e.check(t); err != nil {
		return api.TaskInfo{}, err
	}
	if err := e.store.SaveTask(t); err != nil {
		return api.TaskInfo{}, err
	}
	// 触发方式变了,或从暂停恢复:从现在重新计时,不补跑之前的时间点。
	if x, y := old.Trigger, t.Trigger; x.Cron != y.Cron || !x.At.Equal(y.At) || (old.Paused && !t.Paused) {
		e.setSince(name, e.clock.Now())
	}
	return e.info(t), nil
}

func (e *engine) DeleteTask(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.store.DeleteTask(name); errors.Is(err, store.ErrNotFound) {
		return noTask(name)
	} else if err != nil {
		return err
	}
	e.setSince(name, time.Time{})
	return nil
}

func (e *engine) SetPaused(name string, paused bool) (api.TaskInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, err := e.load(name)
	if err != nil {
		return api.TaskInfo{}, err
	}
	if t.Paused != paused {
		t.Paused = paused
		if err := e.store.SaveTask(t); err != nil {
			return api.TaskInfo{}, err
		}
		if !paused {
			e.setSince(name, e.clock.Now()) // 暂停期间的时间点不补跑
		}
	}
	return e.info(t), nil
}

func (e *engine) RunNow(name, key string) (task.Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id, ok := e.keys[key]; ok && key != "" {
		return e.store.LoadRun(id)
	}
	t, err := e.load(name)
	if err != nil {
		return task.Run{}, err
	}
	return e.start(t, task.ByManual, time.Time{}, "", key)
}

func (e *engine) Runs(taskName string, limit int) ([]task.Run, error) {
	return e.store.ListRuns(taskName, limit)
}

func (e *engine) Run(id string) (task.Run, error) {
	r, err := e.store.LoadRun(id)
	if errors.Is(err, store.ErrNotFound) {
		return r, noRun(id)
	}
	return r, err
}

func (e *engine) Logs(id string) (api.Logs, error) {
	stdout, stderr, err := e.store.LoadOutput(id)
	if errors.Is(err, store.ErrNotFound) {
		return api.Logs{}, noRun(id)
	}
	return api.Logs{Stdout: stdout, Stderr: stderr}, err
}
