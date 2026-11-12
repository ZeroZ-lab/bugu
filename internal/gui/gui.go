//go:build !headless

package gui

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/url"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/egoist/mygo"

	"bugu/internal/client"
	"bugu/internal/daemon"
	"bugu/internal/notify"
)

// Available 报告这个构建是否包含桌面界面。
const Available = true

type app struct {
	home string
	logf func(format string, args ...any)

	// 以下字段在应用就绪后设置。
	addr, token string
	backend     *daemon.Daemon // 为 nil 表示本机已有后台,这个进程只做界面(G8)
	win         *mygo.Window

	quitting    atomic.Bool // 用户已决定退出:关窗口时不再改为隐藏
	quitAllowed atomic.Bool // 退出前的确认已经通过
}

// Run 启动桌面应用,阻塞到应用退出。必须在主 goroutine 上调用。
func Run(home string, log io.Writer) error {
	a := &app{home: home, logf: func(format string, args ...any) { fmt.Fprintf(log, "bugu: "+format+"\n", args...) }}
	var startErr error

	mygo.App.SetName("Bugu")
	// 有这个监听器,关掉最后一个窗口时应用不会退出,任务照常运行(G9)。
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnBeforeQuit(a.confirmQuit)
	mygo.App.OnActivate(func(hasVisibleWindows bool) {
		if !hasVisibleWindows {
			a.open("")
		}
	})
	// 点通知打开对应的结果(N1)。通知的标识就是运行编号。
	mygo.App.OnNotificationClick(func(id string) { a.open(id) })
	mygo.App.WhenReady(func() {
		// 后台要等应用就绪后再启动:它一启动就可能有运行结束并要求发通知。
		if startErr = a.connect(); startErr != nil {
			a.quitAllowed.Store(true)
			mygo.App.Quit()
			return
		}
		a.tray()
		if !mygo.App.WasOpenedAtLogin() { // 开机自启时静默驻留菜单栏(G12)
			a.open("")
		}
	})

	// 终端里的 Ctrl+C 和系统的终止信号也走正常的退出流程,让后台有机会收尾。
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		a.quitAllowed.Store(true)
		mygo.App.Quit()
	}()

	err := mygo.App.Run()
	if a.backend != nil {
		err = errors.Join(err, a.backend.Close())
	}
	return errors.Join(startErr, err)
}

// connect 找到要显示的后台:本机已经有后台在运行就只做它的界面,否则自己承载一个(PRD 5.3)。
func (a *app) connect() error {
	if c, addr, token, err := client.LocalEndpoint(a.home); err == nil && c.Health() == nil {
		a.addr, a.token = addr, token
		a.logf("本机已有后台在运行(%s),这个窗口只作为它的界面;系统通知由承载后台的那个应用发出", addr)
		return nil
	}
	d, err := daemon.Start(daemon.Options{Home: a.home, Notify: a.notify, Log: logWriter(a.logf)})
	if err != nil {
		return err
	}
	a.backend = d
	_, addr, token, err := client.LocalEndpoint(a.home)
	if err != nil {
		return err
	}
	a.addr, a.token = addr, token
	a.logf("后台已启动,监听 http://%s,数据目录 %s", addr, a.home)
	return nil
}

type logWriter func(format string, args ...any)

func (w logWriter) Write(p []byte) (int, error) {
	w("%s", bytes.TrimPrefix(bytes.TrimRight(p, "\n"), []byte("bugu: ")))
	return len(p), nil
}

// open 显示主窗口。runID 不为空时打开那次运行的结果。
// 令牌放在网址片段里交给页面,不会发给服务器(决策记录 0007)。
func (a *app) open(runID string) {
	fragment := url.Values{"token": {a.token}}
	if runID != "" {
		fragment.Set("run", runID)
	}
	target := "http://" + a.addr + "/#" + fragment.Encode()
	if a.win == nil {
		a.win = mygo.NewWindow(mygo.WindowOptions{Title: "布谷", URL: target, Width: 1040, Height: 720, MinWidth: 480, MinHeight: 360})
		// 关窗口只是隐藏,应用留在菜单栏(PRD 5.3)。
		a.win.OnClose(func(e *mygo.CloseEvent) {
			if !a.quitting.Load() {
				e.PreventDefault()
				a.win.Hide()
			}
		})
		return
	}
	if runID != "" {
		if err := a.win.Page().LoadURL(target); err != nil {
			a.logf("打开运行 %s 失败: %v", runID, err)
		}
	}
	a.win.Show()
	a.win.Focus()
}

func (a *app) tray() {
	login := &mygo.MenuItem{Label: "开机时启动", Type: mygo.MenuItemCheckbox, Checked: mygo.App.OpenAtLogin()}
	login.Click = func(item *mygo.MenuItem, _ *mygo.Window) {
		if err := mygo.App.SetOpenAtLogin(item.IsChecked()); err != nil {
			item.SetChecked(mygo.App.OpenAtLogin())
			go mygo.Dialog.Error("无法修改开机启动", err.Error())
		}
	}
	_, err := mygo.NewTray(mygo.TrayOptions{
		ID: "bugu", Icon: trayIcon(), IconIsTemplate: true, ToolTip: "布谷",
		Menu: mygo.NewMenu([]*mygo.MenuItem{
			{Label: "打开布谷", Click: func(*mygo.MenuItem, *mygo.Window) { a.open("") }},
			mygo.Separator(),
			login,
			mygo.Separator(),
			{Label: "退出布谷", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
		}),
	})
	if err != nil {
		a.logf("菜单栏图标创建失败: %v", err)
	}
}

// confirmQuit 在退出前提醒:这个应用承载着后台,退出后任务不再运行(G13)。
func (a *app) confirmQuit(e *mygo.QuitEvent) {
	if e.SessionEnding || a.quitAllowed.Load() || a.backend == nil || !a.hasEnabledTasks() {
		a.quitting.Store(true)
		return
	}
	e.PreventDefault()
	go func() { // 对话框会阻塞,不能占着主线程
		result, err := mygo.Dialog.Message(mygo.MessageOptions{
			Type:    mygo.MessageWarning,
			Message: "退出后任务将不再运行",
			Detail:  "布谷还有已启用的任务。退出应用后,它们不会按时执行,直到你再次打开布谷。只想关掉窗口的话,直接关闭窗口即可,布谷会留在菜单栏。",
			Buttons: []string{"取消", "仍然退出"}, DefaultButton: 0, CancelButton: 0, DestructiveButtons: []int{1},
		})
		if err == nil && result.Button == 1 {
			a.quitAllowed.Store(true)
			mygo.App.Quit()
		}
	}()
}

func (a *app) hasEnabledTasks() bool {
	tasks, err := client.New(a.addr, a.token).Tasks()
	if err != nil {
		return false
	}
	for _, t := range tasks {
		if !t.Paused {
			return true
		}
	}
	return false
}

// notify 把后台交来的通知显示为系统通知(N1)。macOS 上只有打包后的应用能显示。
func (a *app) notify(m notify.Message) {
	if !mygo.NotificationsSupported() {
		a.logf("通知(当前环境无法显示系统通知): %s", m.Title)
		return
	}
	if err := mygo.NewNotification(mygo.NotificationOptions{ID: m.RunID, Title: m.Title, Body: m.Body}).Show(); err != nil {
		a.logf("通知 %q 发送失败: %v", m.Title, err)
	}
}

// trayIcon 画一个菜单栏用的模板图标:透明背景上的黑色圆环加中心圆点。
func trayIcon() []byte {
	const size = 36
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			dx, dy := float64(x)-size/2+0.5, float64(y)-size/2+0.5
			if d := dx*dx + dy*dy; d <= 5*5 || (d >= 11*11 && d <= 14.5*14.5) {
				img.Set(x, y, color.NRGBA{A: 255})
			}
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}
