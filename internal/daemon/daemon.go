// Package daemon 把调度、执行、存档和 HTTP 接口组装成一个运行中的后台。
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"bugu/internal/api"
	"bugu/internal/clock"
	"bugu/internal/i18n"
	"bugu/internal/notify"
	"bugu/internal/server"
	"bugu/internal/store"
)

// DefaultListen 是默认的监听地址:只对本机开放(C3)。
const DefaultListen = "127.0.0.1:7377"

// defaultTick 是两次评估之间的间隔。调度不设长定时器,每次醒来都按墙上时间重新计算(S3)。
const defaultTick = 15 * time.Second

type Options struct {
	Home   string        // 数据目录
	Listen string        // 监听地址,为空时使用 DefaultListen
	Clock  clock.Clock   // 为空时使用系统时钟
	Tick   time.Duration // 评估间隔,为 0 时使用默认值
	Log    io.Writer     // 运行日志,为空时丢弃
	// Notify 接收需要显示给用户的系统通知,由承载后台的界面提供;为空时不发系统通知(N1)。
	// 它在运行结束时被调用,不应长时间阻塞。
	Notify func(notify.Message)
	// LoginShell 是用户登录 shell 的路径,任务的子进程经它启动(E1);为空时直接启动。
	LoginShell string
	// Version 是程序的版本,经健康检查接口告诉界面。
	Version string

	afterTick func() // 测试用:循环里的每次评估结束后调用
}

type Daemon struct {
	engine   *engine
	srv      *http.Server
	ln       net.Listener
	store    *store.Store
	unlock   func()
	cancel   context.CancelFunc
	loopDone chan struct{}
	// deliverDone 在通知的投递循环退出后关闭。
	deliverDone chan struct{}
	closing     sync.Once
	closeErr    error
}

// Start 启动后台:独占数据目录、准备令牌、开始监听和调度。
func Start(o Options) (*Daemon, error) {
	if o.Listen == "" {
		o.Listen = DefaultListen
	}
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	if o.Tick <= 0 {
		o.Tick = defaultTick
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	logf := func(msg string) { fmt.Fprintf(o.Log, "bugu: %s\n", msg) }

	st, err := store.Open(o.Home)
	if err != nil {
		return nil, err
	}
	// 同一份数据只允许一个后台(PRD 5.3)。
	unlock, err := st.Lock()
	if errors.Is(err, store.ErrLocked) {
		return nil, i18n.Errorf("数据目录 %s 已有一个后台在运行", o.Home)
	} else if err != nil {
		return nil, err
	}
	d := &Daemon{store: st, unlock: unlock, loopDone: make(chan struct{}), deliverDone: make(chan struct{})}
	ok := false
	defer func() {
		if !ok {
			unlock()
		}
	}()

	token, err := st.Secret(api.TokenFile)
	if err != nil {
		return nil, i18n.Errorf("准备令牌: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	if d.engine, err = newEngine(ctx, o.Clock, st, o.LoginShell, logf); err != nil {
		cancel()
		return nil, err
	}
	d.engine.version, d.engine.localToken = o.Version, token
	if err := d.engine.loadTokens(); err != nil {
		cancel()
		return nil, i18n.Errorf("读取令牌: %w", err)
	}
	d.engine.notify = o.Notify

	if d.ln, err = net.Listen("tcp", o.Listen); err != nil {
		cancel()
		return nil, i18n.Errorf("监听 %s: %w", o.Listen, err)
	}
	if tcp, isTCP := d.ln.Addr().(*net.TCPAddr); isTCP && !tcp.IP.IsLoopback() {
		logf(i18n.T("注意:正在监听非本机地址 %s,任何能访问该地址并持有令牌的人都可以在这台机器上执行命令", d.ln.Addr()))
	}
	endpoint, _ := json.Marshal(api.Endpoint{Addr: d.Addr()})
	if err := st.WritePrivate(api.EndpointFile, append(endpoint, '\n')); err != nil {
		d.ln.Close()
		cancel()
		return nil, err
	}

	// 两次评估之间隔得比几个评估间隔还久,就是电脑睡着过。
	if o.Tick < time.Hour {
		d.engine.wakeGap = max(time.Minute, 4*o.Tick)
	}
	d.engine.tick() // 处理停机期间错过的触发
	go func() {
		defer close(d.deliverDone)
		d.engine.deliverLoop()
	}()
	go func() {
		defer close(d.loopDone)
		for {
			timer := o.Clock.NewTimer(o.Tick)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C():
				d.engine.tick()
				if o.afterTick != nil {
					o.afterTick()
				}
			}
		}
	}()

	d.srv = &http.Server{Handler: server.New(d.engine, d.engine.access), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := d.srv.Serve(d.ln); !errors.Is(err, http.ErrServerClosed) {
			logf(i18n.T("HTTP 服务异常退出: %v", err))
		}
	}()
	ok = true
	return d, nil
}

// Addr 返回本机客户端应连接的地址。监听全部网卡时返回本机回环地址。
func (d *Daemon) Addr() string {
	addr := d.ln.Addr().String()
	if tcp, ok := d.ln.Addr().(*net.TCPAddr); ok && tcp.IP.IsUnspecified() {
		return net.JoinHostPort("127.0.0.1", fmt.Sprint(tcp.Port))
	}
	return addr
}

// Listening 返回后台实际监听的地址,比如 [::]:7377;Addr 返回的是本机客户端连接用的地址。
func (d *Daemon) Listening() string { return d.ln.Addr().String() }

// Wait 阻塞到当前进行中的运行全部结束。
func (d *Daemon) Wait() { d.engine.wg.Wait() }

// Close 停止后台:不再接受请求,终止进行中的运行并把它们记为中断,释放数据目录。
// 重复调用是安全的。
func (d *Daemon) Close() error {
	d.closing.Do(func() {
		d.closeErr = d.srv.Close()
		d.cancel()
		<-d.loopDone
		d.engine.wg.Wait()
		<-d.deliverDone
		if err := d.store.Remove(api.EndpointFile); d.closeErr == nil {
			d.closeErr = err
		}
		d.unlock()
	})
	return d.closeErr
}
