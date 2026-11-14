package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"bugu/internal/api"
	"bugu/internal/clock"
	"bugu/internal/deliver"
	"bugu/internal/exec"
	"bugu/internal/i18n"
	"bugu/internal/notify"
	"bugu/internal/runner"
	"bugu/internal/schedule"
	"bugu/internal/store"
	"bugu/internal/task"
)

// engine 是后台的核心:按时触发任务、管理进行中的运行、响应接口的请求。
// 它实现 server.Backend。
type engine struct {
	clock   clock.Clock
	store   *store.Store
	runners runner.Set
	exec    exec.Executor
	ctx     context.Context // 后台关闭时取消,进行中的运行随之终止
	log     func(msg string)
	// notify 把系统通知交给界面去显示;没有界面时为 nil(N1)。
	notify func(notify.Message)
	// http 用来投递 webhook。
	http *http.Client

	dmu  sync.Mutex    // 同一时刻只有一处在投递,避免同一条记录发两次
	wake chan struct{} // 有新的待投递记录时唤醒投递循环

	version string // 程序的版本

	localToken string              // 本机令牌,即数据目录里的 token 文件
	tmu        sync.RWMutex        // 保护 tokens
	tokens     []store.StoredToken // 另外创建的令牌(C5)
	smu        sync.Mutex          // 设置文件的读写

	// 醒来后的补跑(决策记录 0016):tick 之间隔得比 wakeGap 久,就当作刚醒来;
	// 补跑要等到连续醒着 settle 之后,免得在网络恢复之前就跑。
	wakeGap    time.Duration
	lastTick   time.Time       // 上一次评估的时间
	awakeSince time.Time       // 这一段连续醒着的起点
	deferred   map[string]bool // 因为刚醒来而推迟了补跑的任务

	mu      sync.Mutex
	since   map[string]time.Time  // 每个任务已评估到的时间点
	running map[string]string     // 任务名 → 进行中的运行编号
	live    map[string]*exec.Live // 运行编号 → 进行中的运行正在产生的输出(G6)
	keys    map[string]string     // 幂等标识 → 运行编号
	wg      sync.WaitGroup        // 进行中的运行
}

// settleAfterWake 是醒来或启动之后,要连续醒着多久才补跑错过的触发(决策记录 0016)。
const settleAfterWake = 2 * time.Minute

// 启动时从最近这么多条运行记录里恢复幂等标识。
const recentRuns = 500

func newEngine(ctx context.Context, c clock.Clock, st *store.Store, loginShell string, log func(string)) (*engine, error) {
	runners := runner.Open(st.RunnersDir())
	e := &engine{
		clock: c, store: st, runners: runners, ctx: ctx, log: log,
		exec:    exec.Executor{Clock: c, Runners: runners.Get, LoginShell: loginShell},
		running: map[string]string{}, live: map[string]*exec.Live{}, keys: map[string]string{}, deferred: map[string]bool{}, wakeGap: time.Minute,
		http: &http.Client{}, wake: make(chan struct{}, 1),
	}
	e.applySettings()
	// 上一个后台留下的"运行中"已经没有进程在管,标为中断(O3)。
	if n, err := st.InterruptUnfinished(c.Now()); err != nil {
		return nil, err
	} else if n > 0 {
		log(i18n.T("上次退出时有 %d 次运行没有结束,已标为中断", n))
	}
	// 系统通知只在运行结束的那一刻有意义;上一个后台没来得及发的,不再补发。
	if pending, err := st.PendingDeliveries(); err == nil {
		for _, d := range pending {
			if d.Channel == task.ChannelSystem {
				d.Status, d.Error = task.DeliveryDropped, i18n.T("后台在发出之前退出")
				e.save(d)
			}
		}
	}
	var err error
	if e.since, err = st.LoadState(); errors.Is(err, store.ErrStateBroken) {
		// 坏了的进度不该让后台起不来:各任务从现在开始计时,只是这段时间错过的不补跑。
		log(i18n.T("调度进度文件损坏,已改名为 state.json.broken;各任务从现在开始计时,停机期间错过的触发不补跑: %v", err))
	} else if err != nil {
		return nil, i18n.Errorf("读取调度进度: %w", err)
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
	if e.lastTick.IsZero() || now.Sub(e.lastTick) > e.wakeGap {
		e.awakeSince = now // 后台刚启动,或者两次评估之间电脑睡着了
	}
	e.lastTick = now
	tasks, err := e.store.ListTasks()
	if err != nil {
		e.log(i18n.T("读取任务失败: %v", err))
		return
	}
	for _, t := range tasks {
		if t.Paused {
			continue
		}
		since, seen := e.since[t.Name]
		if !seen { // 不经接口放进来的任务文件:从现在开始计时
			e.startFrom(t, now)
			continue
		}
		d, err := schedule.Evaluate(t.Trigger, t.Name, t.In(since), t.In(now)) // cron 按任务的时区计算(E7)
		if err != nil {
			e.log(i18n.T("任务 %s 的触发定义有误: %v", t.Name, err))
			continue
		}
		if !d.Fire {
			continue
		}
		run, missed := schedule.Catchup(d, now, t.OnMissed, time.Duration(t.MissedWindow))
		// 补跑(过了原定时间才触发)等电脑连续醒着一会儿再跑:刚醒来时网络往往还没恢复,
		// 短暂的后台唤醒也会马上再睡。不保存进度,下一次评估时还是这个决定(S4,决策记录 0016)。
		if run && now.Sub(d.At) > schedule.OnTime && now.Sub(e.awakeSince) < settleAfterWake {
			e.deferred[t.Name] = true
			continue
		}
		// 先保存进度再启动:宁可在保存和启动之间崩溃时少跑一次,也不在重启后重复触发(S7)。
		if !e.setSince(t.Name, now) {
			continue
		}
		if !run {
			if err := e.missed(t, d.ScheduledFor, missed); err != nil {
				e.log(i18n.T("任务 %s 的错过记录保存失败: %v", t.Name, err))
			}
			continue
		}
		note := ""
		if missed > 0 {
			note = i18n.T("此前错过 %d 次触发,只补跑最近一次", missed)
		}
		if e.deferred[t.Name] {
			delete(e.deferred, t.Name)
			waited := i18n.T("电脑刚醒来,等了一会儿才补跑")
			if note == "" {
				note = waited
			} else {
				note = i18n.T("%s;%s", note, waited)
			}
		}
		if _, err := e.start(t, task.BySchedule, d.ScheduledFor, note, ""); err != nil {
			e.log(i18n.T("任务 %s 启动失败: %v", t.Name, err))
		}
	}
}

// missed 为按补跑策略不再运行的触发留一条"错过"的记录(S4、O3)。调用者持有锁。
func (e *engine) missed(t task.Task, scheduledFor time.Time, count int) error {
	now := e.clock.Now()
	id, err := e.store.NewRunID(now)
	if err != nil {
		return err
	}
	why := i18n.T("任务设置为错过不补跑")
	if t.OnMissed == task.MissedWindow {
		why = i18n.T("已超过 %s 的补跑宽限", time.Duration(t.MissedWindow))
	}
	r := task.Run{
		ID: id, Task: t.Name, Trigger: task.BySchedule, ScheduledFor: scheduledFor, Status: task.Missed,
		CreatedAt: now, FinishedAt: now, Note: i18n.T("错过 %d 次触发,%s", count, why),
	}
	return e.store.CreateRun(r, t)
}

// startFrom 让任务从 now 开始计时(新建、修改了触发定义、恢复时),此前的时间点不算错过。
// 设置了随机偏移时,原定时间点已经过去、实际时间因为偏移还没到的那一次也不再触发,
// 免得修改设置后把刚跑过的那一次再跑一遍(决策记录 0018)。调用者持有锁。
func (e *engine) startFrom(t task.Task, now time.Time) {
	since := now
	if o, err := schedule.NextOccurrence(t.Trigger, t.Name, t.In(now)); err == nil && !o.At.IsZero() && !o.ScheduledFor.After(now) {
		since = o.At
	}
	e.setSince(t.Name, since)
}

// setSince 更新并保存任务的评估点。调用者持有锁。
func (e *engine) setSince(name string, at time.Time) bool {
	if at.IsZero() {
		delete(e.since, name)
	} else {
		e.since[name] = at
	}
	if err := e.store.SaveState(e.since); err != nil {
		e.log(i18n.T("保存调度进度失败: %v", err))
		return false
	}
	return true
}

// start 为任务建立一次运行并开始执行。调用者持有锁。
// 同一任务上一次还没结束时,定时触发记为跳过(S5),手动触发直接报冲突。
func (e *engine) start(t task.Task, trigger string, scheduledFor time.Time, note, key string) (task.Run, error) {
	now := e.clock.Now()
	id, err := e.store.NewRunID(now)
	if err != nil {
		return task.Run{}, err
	}
	r := task.Run{
		ID: id, Task: t.Name, Trigger: trigger, ScheduledFor: scheduledFor,
		Note: note, RequestKey: key, CreatedAt: now,
	}
	if busy, ok := e.running[t.Name]; ok {
		if trigger == task.ByManual {
			return r, api.Wrap(api.CodeConflict, i18n.Errorf("任务 %s 正在运行(运行编号 %s),结束后才能再次运行", t.Name, busy))
		}
		r.Status, r.FinishedAt = task.Skipped, now
		r.Note = i18n.T("上一次运行 %s 尚未结束,本次跳过", busy)
		return r, e.store.CreateRun(r, t)
	}
	r.Status, r.StartedAt = task.Running, now
	if err := e.store.CreateRun(r, t); err != nil {
		return r, err
	}
	e.running[t.Name] = r.ID
	live := &exec.Live{}
	e.live[r.ID] = live
	if key != "" {
		e.keys[key] = r.ID
	}
	e.wg.Add(1)
	go e.execute(r, t, live)
	return r, nil
}

func (e *engine) execute(r task.Run, t task.Task, live *exec.Live) {
	defer e.wg.Done()
	info := exec.Info{LastRun: e.lastSuccess(t.Name), Live: live}
	if r.Trigger == task.BySchedule {
		// 模板里的时间去掉随机偏移:提前到前一天 23:55 跑的 00:00 任务,{date} 仍是当天(S12)。
		info.Offset = schedule.Offset(t.Trigger, t.Name, r.ScheduledFor)
	}
	info.RunDir, _ = e.store.RunDir(r.ID)
	if len(t.Steps) > 1 {
		// 多步骤任务运行中,每一步开始时记下各步骤的状态,运行详情里的步骤卡片据此更新(T9)。
		info.Progress = func(steps []task.StepRun) {
			now := r
			now.Status, now.Steps = task.Running, steps
			_ = e.store.UpdateRun(now)
		}
	}
	var res task.Run
	var err error
	if info.StateDir, err = e.store.StateDir(t.Name); err != nil {
		err = i18n.Errorf("预检未通过:无法建立状态目录: %w", err)
		res.Status = task.Failed
	} else {
		res, err = e.exec.Run(e.ctx, t, info)
	}
	r.Status, r.Command, r.ExitCode, r.Stdout, r.Stderr = res.Status, res.Command, res.ExitCode, res.Stdout, res.Stderr
	r.Permission, r.NoContent, r.Steps = res.Permission, res.NoContent, res.Steps
	r.Summary, r.Artifacts, r.Notify, r.Message = res.Summary, res.Artifacts, res.Notify, res.Message
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
		r.Status, r.Error = task.Interrupted, i18n.T("后台关闭,运行被终止")
	}
	for i := range r.Steps {
		if r.Steps[i].Status == task.Canceled {
			r.Steps[i].Status = task.Interrupted
		}
	}
	// 只读运行失败,有可能是因为任务其实需要写权限。Bugu 分辨不出来,所以只作提示(决策记录 0008)。
	if r.Status == task.Failed && r.Permission == task.PermReadOnly {
		hint := i18n.T("本次以只读权限运行;如果失败是因为需要修改文件,可以在任务里放开权限")
		if r.Note != "" {
			hint = i18n.T("%s;%s", r.Note, hint)
		}
		r.Note = hint
	}
	if r.Summary == "" {
		r.Summary = notify.Summary(r)
	}
	// 按运行开始时的任务配置决定是否通知,与存档里的快照一致。投递记录在运行标为结束之前建好,
	// 看到运行结束的客户端一定也看得到它们;真正的投递在之后,失败只重试投递,不影响运行的结果(N7)。
	queued := e.queue(t, r)
	if err := e.store.FinishRun(r); err != nil {
		e.log(i18n.T("保存运行 %s 的结果失败: %v", r.ID, err))
	}
	e.mu.Lock()
	delete(e.running, t.Name)
	delete(e.live, r.ID)
	e.mu.Unlock()
	for _, d := range queued {
		if d.Channel == task.ChannelSystem {
			e.showSystem(d, r)
		}
	}
	e.deliverDue()
}

// queue 为这次运行该发的每条通知建一条待投递的记录。
func (e *engine) queue(t task.Task, r task.Run) []task.Delivery {
	var queued []task.Delivery
	for i, n := range t.Notifications {
		if !notify.Due(n.When, r) {
			continue
		}
		d := task.Delivery{
			RunID: r.ID, Task: r.Task, Index: i, Channel: n.Channel, Target: n.URL,
			Key: r.ID + "/" + strconv.Itoa(i), Status: task.DeliveryPending, CreatedAt: e.clock.Now(),
		}
		e.save(d)
		queued = append(queued, d)
	}
	return queued
}

// showSystem 把系统通知交给界面(N1)。没有界面时无处可送,记为未发出。
func (e *engine) showSystem(d task.Delivery, r task.Run) {
	d.Attempts = 1
	if e.notify != nil {
		e.notify(notify.Compose(r))
		d.Status, d.DeliveredAt = task.DeliveryDelivered, e.clock.Now()
	} else {
		d.Status, d.Error = task.DeliveryDropped, i18n.T("这个后台没有界面,系统通知无处显示")
	}
	e.save(d)
}

// deliverLoop 在后台运行期间投递待发的 webhook,并按间隔重试失败的(N7)。启动时接着处理上次没发完的。
func (e *engine) deliverLoop() {
	for {
		next := e.deliverDue()
		var timer clock.Timer
		var fire <-chan time.Time
		if !next.IsZero() {
			timer = e.clock.NewTimer(next.Sub(e.clock.Now()))
			fire = timer.C()
		}
		select {
		case <-e.ctx.Done():
		case <-fire:
		case <-e.wake:
		}
		if timer != nil {
			timer.Stop()
		}
		if e.ctx.Err() != nil {
			return
		}
	}
}

// deliverDue 发送所有到了时间的待投递记录,返回最早的下一次重试时间;没有待重试的返回零值。
func (e *engine) deliverDue() time.Time {
	e.dmu.Lock()
	defer e.dmu.Unlock()
	pending, err := e.store.PendingDeliveries()
	if err != nil {
		e.log(i18n.T("读取待投递的通知失败: %v", err))
		return time.Time{}
	}
	var next time.Time
	for _, d := range pending {
		if d.Channel != task.ChannelWebhook {
			continue // 系统通知在运行结束时当场处理
		}
		if d.NextAttempt.After(e.clock.Now()) {
			if next.IsZero() || d.NextAttempt.Before(next) {
				next = d.NextAttempt
			}
			continue
		}
		if d = e.attempt(d); d.Status == task.DeliveryPending && !d.NextAttempt.IsZero() && (next.IsZero() || d.NextAttempt.Before(next)) {
			next = d.NextAttempt
		}
	}
	return next
}

// attempt 发送一次 webhook 并更新记录。后台正在关闭时放弃,记录保持原样,下次启动再发。
func (e *engine) attempt(d task.Delivery) task.Delivery {
	r, err := e.store.LoadRun(d.RunID)
	if err != nil {
		d.Status, d.Error = task.DeliveryFailed, i18n.T("读取运行记录失败: %v", err)
		e.save(d)
		return d
	}
	ctx, cancel := context.WithCancel(e.ctx)
	timer := e.clock.NewTimer(deliver.Timeout)
	go func() {
		select {
		case <-timer.C():
			cancel()
		case <-ctx.Done():
		}
	}()
	err = deliver.Send(ctx, e.http, d.Target, d.Key, deliver.NewPayload(r))
	timer.Stop()
	cancel()
	if e.ctx.Err() != nil {
		return d
	}
	now := e.clock.Now()
	d.Attempts++
	d.NextAttempt = time.Time{}
	var permanent deliver.Permanent
	switch {
	case err == nil:
		d.Status, d.Error, d.DeliveredAt = task.DeliveryDelivered, "", now
	case errors.As(err, &permanent):
		d.Status, d.Error = task.DeliveryFailed, err.Error()
	default:
		d.Error = err.Error()
		if wait, ok := deliver.Backoff(d.Attempts); ok {
			d.NextAttempt = now.Add(wait)
		} else {
			d.Status = task.DeliveryFailed
		}
	}
	e.save(d)
	return d
}

func (e *engine) save(d task.Delivery) {
	if err := e.store.SaveDelivery(d); err != nil {
		e.log(i18n.T("保存运行 %s 的投递记录失败: %v", d.RunID, err))
	}
}

// Deliveries 返回一次运行的通知投递记录(N7)。
func (e *engine) Deliveries(id string) ([]task.Delivery, error) {
	if _, err := e.Run(id); err != nil {
		return nil, err
	}
	list, err := e.store.Deliveries(id)
	if list == nil {
		list = []task.Delivery{}
	}
	return list, err
}

// lastSuccess 返回任务上次成功运行的开始时间,没有时返回零值。
func (e *engine) lastSuccess(name string) time.Time {
	var at time.Time
	err := e.store.EachRun(func(r store.RunEntry) bool {
		if r.Task == name && r.Status == task.Succeeded {
			at = r.StartedAt
			return false
		}
		return true
	})
	if err != nil {
		e.log(i18n.T("读取任务 %s 的运行记录失败: %v", name, err))
	}
	return at
}

func invalid(err error) error {
	return api.Wrap(api.CodeInvalid, err)
}

func noTask(name string) error {
	return api.Wrap(api.CodeNotFound, i18n.Errorf("没有名为 %q 的任务,可用 bugu list 查看现有任务", name))
}

func noRun(id string) error {
	return api.Wrap(api.CodeNotFound, i18n.Errorf("没有编号为 %q 的运行,可用 bugu runs 查看运行记录", id))
}

// check 在保存之前完整校验任务定义,错误说明要具体到能据此修正。
func (e *engine) check(t task.Task) error {
	if err := task.Validate(t); err != nil {
		return invalid(err)
	}
	if err := schedule.Check(t.Trigger); err != nil {
		return invalid(err)
	}
	if err := exec.Validate(t, e.runners.Get); err != nil {
		return invalid(err)
	}
	return nil
}

// info 给任务定义补上运行时信息。调用者持有锁。
func (e *engine) info(t task.Task, last map[string]*task.Run) api.TaskInfo {
	i := api.TaskInfo{Task: t, LastRun: last[t.Name]}
	_, i.Running = e.running[t.Name]
	if !t.Paused {
		o, _ := schedule.NextOccurrence(t.Trigger, t.Name, t.In(e.clock.Now()))
		i.NextRun = o.At // 设置了随机偏移时就是实际会触发的时间
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
	last, err := e.lastRuns(tasks)
	if err != nil {
		return nil, err
	}
	infos := make([]api.TaskInfo, len(tasks))
	for i, t := range tasks {
		infos[i] = e.info(t, last)
	}
	return infos, nil
}

// lastRuns 找出每个任务最近一次已经结束的运行,任务列表里显示它的结果(G1)。
func (e *engine) lastRuns(tasks []task.Task) (map[string]*task.Run, error) {
	want := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		want[t.Name] = true
	}
	var ids []string
	err := e.store.EachRun(func(r store.RunEntry) bool {
		if want[r.Task] && r.Status.Finished() {
			ids = append(ids, r.ID)
			delete(want, r.Task)
		}
		return len(want) > 0
	})
	if err != nil {
		return nil, err
	}
	runs, err := e.store.LoadRuns(ids)
	if err != nil {
		return nil, err
	}
	last := make(map[string]*task.Run, len(runs))
	for i := range runs {
		last[runs[i].Task] = &runs[i]
	}
	return last, nil
}

func (e *engine) Task(name string) (api.TaskInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, err := e.load(name)
	if err != nil {
		return api.TaskInfo{}, err
	}
	last, err := e.lastRuns([]task.Task{t})
	if err != nil {
		return api.TaskInfo{}, err
	}
	return e.info(t, last), nil
}

// sameDefinition 比较两个任务定义,不看由后台填写的创建时间。
func sameDefinition(a, b task.Task) bool {
	a.CreatedAt, b.CreatedAt = time.Time{}, time.Time{}
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
		return e.info(existing, nil), nil // 重复提交同一个定义(C8)
	case err == nil:
		return api.TaskInfo{}, api.Wrap(api.CodeConflict, i18n.Errorf("已有名为 %q 的任务且定义不同;修改请用 bugu edit,或换一个名字", t.Name))
	case !errors.Is(err, store.ErrNotFound):
		return api.TaskInfo{}, err
	}
	now := e.clock.Now()
	if !t.Trigger.At.IsZero() && !t.Trigger.At.After(now) {
		return api.TaskInfo{}, invalid(i18n.Errorf("一次性时间点 %s 已经过去", t.Trigger.At.Format(time.RFC3339)))
	}
	t.CreatedAt = now
	if err := e.store.SaveTask(t); err != nil {
		return api.TaskInfo{}, err
	}
	e.startFrom(t, now) // 创建之前的时间点不算错过
	return e.info(t, nil), nil
}

func (e *engine) UpdateTask(name string, t task.Task) (api.TaskInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t.Name == "" {
		t.Name = name
	}
	if t.Name != name {
		return api.TaskInfo{}, invalid(i18n.Errorf("不支持改名:请求修改的是 %q,定义里的名字是 %q", name, t.Name))
	}
	old, err := e.load(name)
	if err != nil {
		return api.TaskInfo{}, err
	}
	if err := e.check(t); err != nil {
		return api.TaskInfo{}, err
	}
	t.CreatedAt = old.CreatedAt
	if err := e.store.SaveTask(t); err != nil {
		return api.TaskInfo{}, err
	}
	// 触发方式变了,或从暂停恢复:从现在重新计时,不补跑之前的时间点。
	if x, y := old.Trigger, t.Trigger; x.Cron != y.Cron || !x.At.Equal(y.At) || x.Jitter != y.Jitter || (old.Paused && !t.Paused) {
		e.startFrom(t, e.clock.Now())
	}
	return e.info(t, nil), nil
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
			e.startFrom(t, e.clock.Now()) // 暂停期间的时间点不补跑
		}
	}
	return e.info(t, nil), nil
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
	} else if err != nil {
		return api.Logs{}, err
	}
	logs := api.Logs{Stdout: stdout, Stderr: stderr, Steps: []api.StepLogs{}}
	r, err := e.store.LoadRun(id)
	if err != nil {
		return logs, err
	}
	dir, _ := e.store.RunDir(id)
	for i := range r.Steps {
		var l api.StepLogs
		if l.Stdout, l.Stderr, err = exec.StepOutput(dir, i); err != nil {
			return logs, err
		}
		logs.Steps = append(logs.Steps, l)
	}
	// 运行中:正在执行的那一步的输出还没进存档,从内存里取(G6)。
	e.mu.Lock()
	live := e.live[id]
	e.mu.Unlock()
	if live == nil {
		return logs, nil
	}
	if i, stdout, stderr, ok := live.Snapshot(); ok {
		stdout, stderr = liveTail(stdout), liveTail(stderr)
		if len(logs.Steps) > 1 && i < len(logs.Steps) {
			logs.Steps[i] = api.StepLogs{Stdout: stdout, Stderr: stderr}
		} else {
			logs.Stdout, logs.Stderr = stdout, stderr
		}
	}
	return logs, nil
}

// liveMax 是运行中每路输出返回的字节数上限。运行详情每秒取一次,只给结尾,完整的输出在结束后看。
const liveMax = 64 << 10

// liveTail 只保留输出的结尾,从一行的开头截起,前面用一行说明代替。
func liveTail(s string) string {
	if len(s) <= liveMax {
		return s
	}
	cut := len(s) - liveMax
	if nl := strings.IndexByte(s[cut:], '\n'); nl >= 0 {
		cut += nl + 1
	} else {
		for cut < len(s) && !utf8.RuneStart(s[cut]) {
			cut++
		}
	}
	return i18n.T("[bugu: 运行中只显示最后一部分,前面省略 %d 字节;运行结束后可以看到全部]", cut) + "\n" + s[cut:]
}

func (e *engine) Runners() ([]api.Runner, error) {
	list, err := e.runners.List()
	if err != nil {
		if list == nil {
			return nil, err
		}
		e.log(i18n.T("部分运行器配置有问题,已忽略: %v", err))
	}
	out := make([]api.Runner, len(list))
	for i, r := range list {
		levels := r.Levels()
		if levels == nil {
			levels = []string{}
		}
		out[i] = api.Runner{Name: r.Name, Title: r.Title, Preset: r.Preset, Agent: r.PromptVia != "", Permissions: levels}
	}
	return out, nil
}

func (e *engine) CheckRunner(name string) (api.RunnerCheck, error) {
	r, err := e.runners.Get(name)
	if errors.Is(err, runner.ErrNotFound) {
		return api.RunnerCheck{}, api.Wrap(api.CodeNotFound, i18n.Errorf("没有名为 %q 的运行器,可用 bugu runner list 查看现有的运行器", name))
	} else if err != nil {
		return api.RunnerCheck{}, invalid(err)
	}
	ok, detail := e.exec.Check(e.ctx, r)
	return api.RunnerCheck{Name: name, OK: ok, Detail: detail.Error(), Cause: detail}, nil
}

// inboxLimit 是收件箱一次最多列出的条数。
const inboxLimit = 200

// Inbox 返回收件箱:未读结果(all 为真时包括已读的),以及菜单栏需要的计数(G3、G9)。
func (e *engine) Inbox(all bool) (api.Inbox, error) {
	in := api.Inbox{}
	var ids []string
	err := e.store.EachRun(func(r store.RunEntry) bool {
		if !r.InInbox {
			return true
		}
		if r.Unread {
			in.Unread++
			if r.Status != task.Succeeded {
				in.UnreadFailed++
			}
		}
		if (all || r.Unread) && len(ids) < inboxLimit {
			ids = append(ids, r.ID)
		}
		return true
	})
	if err != nil {
		return api.Inbox{}, err
	}
	if in.Runs, err = e.store.LoadRuns(ids); err != nil {
		return api.Inbox{}, err
	}
	e.mu.Lock()
	in.Running = len(e.running)
	e.mu.Unlock()
	return in, nil
}

// MarkRead 把一次运行标为已读。不在收件箱里的运行和已读的保持不变。
func (e *engine) MarkRead(id string) (task.Run, error) {
	r, err := e.Run(id)
	if err != nil || !r.Unread() {
		return r, err
	}
	r.ReadAt = e.clock.Now()
	return r, e.store.UpdateRun(r)
}

// MarkAllRead 把收件箱里全部未读结果标为已读,返回标记的条数。
func (e *engine) MarkAllRead() (int, error) {
	var ids []string
	err := e.store.EachRun(func(r store.RunEntry) bool {
		if r.Unread {
			ids = append(ids, r.ID)
		}
		return true
	})
	if err != nil {
		return 0, err
	}
	runs, err := e.store.LoadRuns(ids)
	if err != nil {
		return 0, err
	}
	n, now := 0, e.clock.Now()
	for _, r := range runs {
		if !r.Unread() {
			continue
		}
		r.ReadAt = now
		if err := e.store.UpdateRun(r); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
