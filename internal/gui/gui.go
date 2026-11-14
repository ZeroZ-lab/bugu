//go:build !headless

package gui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/egoist/mygo"
	// 框架自己的文字(对话框按钮、编辑菜单等)的中文;英文是框架的原文(决策记录 0014)。
	_ "github.com/egoist/mygo/locales/zh-Hans"

	"bugu/internal/client"
	"bugu/internal/clock"
	"bugu/internal/daemon"
	"bugu/internal/i18n"
	"bugu/internal/notify"
)

// Available 报告这个构建是否包含桌面界面。
const Available = true

type app struct {
	version string
	home    string
	logf    func(msg string)

	// 以下字段在应用就绪后设置。
	addr, token string
	backend     *daemon.Daemon // 为 nil 表示本机已有后台,这个进程只做界面(G8)
	win         *mygo.Window

	quitting atomic.Bool // 用户已决定退出:关窗口时不再改为隐藏
	// recheck 让菜单栏图标立即重新查一次状态,如运行刚结束、菜单栏换了深浅。
	recheck     chan struct{}
	quitAllowed atomic.Bool // 退出前的确认已经通过
}

// Run 启动桌面应用,阻塞到应用退出。必须在主 goroutine 上调用。
func Run(home, version string, log io.Writer) error {
	a := &app{home: home, version: version, logf: func(msg string) { fmt.Fprintf(log, "bugu: %s\n", msg) }, recheck: make(chan struct{}, 1)}
	var startErr error

	mygo.App.SetName("Bugu")
	// 应用说中文和英文(决策记录 0014)。框架自己的文字(对话框按钮等)跟着应用的语言走。
	mygo.App.SetLanguages("zh-Hans", "en")
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
		// 设置为跟随系统时,按系统设置里的语言顺序,而不是环境变量:从访达打开的应用没有 LANG。
		i18n.SetSystem(mygo.App.PreferredLanguages())
		i18n.OnChange(func(i18n.Lang) { a.kick() })
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
		a.logf(i18n.T("本机已有后台在运行(%s),这个窗口只作为它的界面;系统通知由承载后台的那个应用发出", addr))
		return nil
	}
	d, err := daemon.Start(daemon.Options{Home: a.home, Notify: a.notify, Log: logWriter(a.logf), LoginShell: os.Getenv("SHELL"), Version: a.version})
	if err != nil {
		return err
	}
	a.backend = d
	_, addr, token, err := client.LocalEndpoint(a.home)
	if err != nil {
		return err
	}
	a.addr, a.token = addr, token
	a.logf(i18n.T("后台已启动,监听 http://%s,数据目录 %s", addr, a.home))
	return nil
}

type logWriter func(msg string)

func (w logWriter) Write(p []byte) (int, error) {
	w(string(bytes.TrimPrefix(bytes.TrimRight(p, "\n"), []byte("bugu: "))))
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
		a.win = mygo.NewWindow(mygo.WindowOptions{Title: i18n.T("布谷"), URL: target, Width: 1040, Height: 720, MinWidth: 480, MinHeight: 360})
		// 关窗口只是隐藏,应用留在菜单栏(PRD 5.3)。
		a.win.OnClose(func(e *mygo.CloseEvent) {
			if !a.quitting.Load() {
				e.PreventDefault()
				a.win.Hide()
			}
		})
		return
	}
	// 窗口可能正显示着另一台主机的面板(G5);从菜单栏打开时回到本机。
	away := false
	if u, err := url.Parse(a.win.Page().URL()); err == nil && u.Host != a.addr {
		away = true
	}
	if runID != "" || away {
		if err := a.win.Page().LoadURL(target); err != nil {
			a.logf(i18n.T("打开运行 %s 失败: %v", runID, err))
		}
	}
	a.win.Show()
	a.win.Focus()
}

// menu 用当前语言生成菜单栏图标的菜单。
func (a *app) menu() *mygo.Menu {
	login := &mygo.MenuItem{Label: i18n.T("开机时启动"), Type: mygo.MenuItemCheckbox, Checked: mygo.App.OpenAtLogin()}
	login.Click = func(item *mygo.MenuItem, _ *mygo.Window) {
		if err := mygo.App.SetOpenAtLogin(item.IsChecked()); err != nil {
			item.SetChecked(mygo.App.OpenAtLogin())
			go mygo.Dialog.Error(i18n.T("无法修改开机启动"), err.Error())
		}
	}
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: i18n.T("打开布谷"), Click: func(*mygo.MenuItem, *mygo.Window) { a.open("") }},
		mygo.Separator(),
		login,
		mygo.Separator(),
		{Label: i18n.T("退出布谷"), Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	})
}

func (a *app) tray() {
	icon, _ := drawTray(trayIdle, 0, false)
	tray, err := mygo.NewTray(mygo.TrayOptions{ID: "bugu", Icon: icon, IconIsTemplate: true, ToolTip: i18n.T("布谷"), Menu: a.menu()})
	if err != nil {
		a.logf(i18n.T("菜单栏图标创建失败: %v", err))
		return
	}
	tray.OnAppearanceChange(a.kick)
	go a.watch(tray)
}

// 菜单栏图标多久查一次收件箱,以及运行中那一圈虚线多久转一格。
const (
	trayPoll  = 5 * time.Second
	trayFrame = 250 * time.Millisecond
	trayTurn  = 2 * math.Pi / 8 / 12 // 每格转过的角度:三秒转过一段虚线
)

// kick 让菜单栏图标尽快更新。
func (a *app) kick() {
	select {
	case a.recheck <- struct{}{}:
	default:
	}
}

// watch 让菜单栏图标跟着后台的状态变:未读条数、失败、运行中(G9)。
// 只有运行中需要动画,那时才按帧重画;其余时候只在查到变化时重画。
func (a *app) watch(tray *mygo.Tray) {
	c, clk := client.New(a.addr, a.token), clock.Real{}
	shown, shownCount, shownDark, shownLang := trayState(-1), -1, false, i18n.Lang("") // 第一次也要按设置的语言设一遍框架
	state, count, phase := trayIdle, 0, 0.0
	nextPoll := clk.Now()
	for {
		if now := clk.Now(); !now.Before(nextPoll) {
			if in, err := c.Inbox(false); err == nil {
				state, count = stateOf(in)
			}
			// 只做界面时,语言设置在承载后台的那个进程里改,这里跟着它(决策记录 0014)。
			if a.backend == nil {
				if s, err := c.Settings(); err == nil && s.Effective != "" {
					i18n.Set(i18n.Lang(s.Effective))
				}
			}
			nextPoll = now.Add(trayPoll)
		}
		if lang := i18n.Current(); lang != shownLang {
			a.relabel(tray)
			shown, shownLang = -1, lang
			shownCount = -1
		}
		dark := tray.Dark()
		if state != shown || count != shownCount || dark != shownDark || state == trayRunning {
			icon, template := drawTray(state, phase, dark)
			if err := tray.SetIcon(icon, template); err != nil {
				a.logf(i18n.T("更新菜单栏图标失败: %v", err))
			}
			if count != shownCount {
				title, tip := "", i18n.T("布谷")
				if count > 0 {
					title = fmt.Sprint(count)
					tip = i18n.T("布谷 · %d 条未读", count)
					if state == trayFailed {
						tip = i18n.T("布谷 · %d 条失败未读", count)
					}
				}
				tray.SetTitle(title)
				tray.SetToolTip(tip)
			}
			shown, shownCount, shownDark = state, count, dark
		}
		wait := trayPoll
		if state == trayRunning {
			wait, phase = trayFrame, math.Mod(phase+trayTurn, 2*math.Pi)
		}
		select {
		case <-clk.After(wait):
		case <-a.recheck:
			nextPoll = clk.Now()
		}
	}
}

// relabel 在语言改变后重新设置菜单、窗口标题,并让框架自己的文字也换成这种语言。
// 设置为跟随系统时,框架按系统的语言走。
func (a *app) relabel(tray *mygo.Tray) {
	tag := ""
	if lang := i18n.Current(); lang != i18n.System() {
		tag = map[i18n.Lang]string{i18n.Zh: "zh-Hans", i18n.En: "en"}[lang]
	}
	mygo.App.SetLocale(tag)
	tray.SetMenu(a.menu())
	// 窗口标题跟着页面的 <title> 走:页面按语言、以及是不是另一台主机自己设置(G5)。
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
			Message: i18n.T("退出后任务将不再运行"),
			Detail:  i18n.T("布谷还有已启用的任务。退出应用后,它们不会按时执行,直到你再次打开布谷。只想关掉窗口的话,直接关闭窗口即可,布谷会留在菜单栏。"),
			Buttons: []string{i18n.T("取消"), i18n.T("仍然退出")}, DefaultButton: 0, CancelButton: 0, DestructiveButtons: []int{1},
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
	a.kick() // 运行刚结束,菜单栏的未读条数要跟着变
	if !mygo.NotificationsSupported() {
		a.logf(i18n.T("通知(当前环境无法显示系统通知): %s", m.Title))
		return
	}
	if err := mygo.NewNotification(mygo.NotificationOptions{ID: m.RunID, Title: m.Title, Body: m.Body}).Show(); err != nil {
		a.logf(i18n.T("通知 %q 发送失败: %v", m.Title, err))
	}
}
