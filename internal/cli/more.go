package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"bugu/internal/api"
	"bugu/internal/client"
	"bugu/internal/daemon"
	"bugu/internal/gui"
	"bugu/internal/hosts"
	"bugu/internal/i18n"
	"bugu/internal/service"
	"bugu/skills"
)

// probeTimeout 是检查另一台主机是否在线时每次请求的上限。
const probeTimeout = 5 * time.Second

// printPlan 把 bugu run --dry-run 的结果写给人看。
func printPlan(w io.Writer, p api.Plan) {
	fmt.Fprint(w, i18n.T("任务 %s 立即运行时会执行以下步骤(没有真正运行),工作目录 %s:\n", p.Task, p.Cwd))
	for i, s := range p.Steps {
		head := i18n.T("第 %d 步", i+1)
		if s.Name != "" {
			head += " · " + s.Name
		}
		fmt.Fprintf(w, "\n%s(%s)\n", head, s.Type)
		switch {
		case s.Text != "":
			fmt.Fprintf(w, "  %s\n", strings.ReplaceAll(s.Text, "\n", "\n  "))
		default:
			fmt.Fprintf(w, "  $ %s\n", shellJoin(s.Command))
			if s.Permission != "" {
				fmt.Fprint(w, i18n.T("  权限:%s\n", s.Permission))
			}
			if s.Prompt != "" {
				fmt.Fprint(w, i18n.T("  提示词:\n"))
				fmt.Fprintf(w, "    %s\n", strings.ReplaceAll(s.Prompt, "\n", "\n    "))
			}
		}
	}
}

// shellJoin 把参数拼成可以复制到终端的样子。
func shellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`;&|<>()*?[]{}~#!") {
			parts[i] = a
			continue
		}
		parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " ")
}

// runnerAdd 读取一份运行器配置交给后台保存(R1)。
func runnerAdd(e *env, c *client.Client, file string) error {
	raw, err := e.readFile(file)
	if err != nil {
		return err
	}
	var head struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &head); err != nil || head.Name == "" {
		return bad(i18n.Errorf("%s 不是合法的运行器配置:需要一个 JSON 对象,其中有 name", file))
	}
	r, err := c.SaveRunner(head.Name, raw)
	if err != nil {
		return err
	}
	return e.show(r, func() {
		fmt.Fprint(e.stdout, i18n.T("已保存运行器 %s。用 bugu runner check %s 检查它能否启动\n", r.Name, r.Name))
	})
}

func (e *env) readFile(file string) ([]byte, error) {
	if file == "-" {
		return io.ReadAll(e.stdin)
	}
	return os.ReadFile(file)
}

// tokenCmd 管理访问令牌(C5,决策记录 0015)。
func tokenCmd(e *env, args []string) error {
	rest, err := parse(flag.NewFlagSet("token", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	usage := bad(i18n.Errorf("用法:bugu token list,bugu token create <名字>,或 bugu token revoke <名字>"))
	if len(rest) == 0 {
		return usage
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	switch {
	case rest[0] == "list" && len(rest) == 1:
		list, err := c.Tokens()
		if err != nil {
			return err
		}
		return e.show(list, func() {
			w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tCREATED")
			for _, t := range list {
				created := when(t.CreatedAt)
				if t.Local {
					created = i18n.T("本机令牌,不能作废")
				}
				fmt.Fprintf(w, "%s\t%s\n", t.Name, created)
			}
			w.Flush()
		})
	case rest[0] == "create" && len(rest) == 2:
		t, err := c.CreateToken(rest[1])
		if err != nil {
			return err
		}
		return e.show(t, func() {
			fmt.Fprintln(e.stdout, t.Token)
			fmt.Fprint(e.stderr, i18n.T("bugu: 已创建令牌 %s。它只显示这一次,请现在保存;不用时用 bugu token revoke %s 作废\n", t.Name, t.Name))
			// 决策记录 0019:令牌能建任务,也就能执行任意命令,两级访问不是安全边界。
			fmt.Fprint(e.stderr, i18n.T("bugu: 持有它的人可以建任务,也就能以你的身份在这台主机上执行任意命令。只交给你信任的机器\n"))
		})
	case rest[0] == "revoke" && len(rest) == 2:
		if err := c.RevokeToken(rest[1]); err != nil {
			return err
		}
		return e.show(map[string]string{"revoked": rest[1]}, func() {
			fmt.Fprint(e.stdout, i18n.T("已作废令牌 %s,使用它的客户端立即失去访问权\n", rest[1]))
		})
	}
	return usage
}

// hostCmd 登记其他主机(PRD 5.4)。登记表在本机数据目录里,不经过后台。
func hostCmd(e *env, args []string) error {
	if err := e.localOnly("host"); err != nil {
		return err
	}
	fs := flag.NewFlagSet("host", flag.ContinueOnError)
	token := fs.String("token", "", "")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	usage := bad(i18n.Errorf("用法:bugu host add <名字> <地址> --token <令牌>(- 表示从标准输入读),bugu host list,或 bugu host rm <名字>"))
	if len(rest) == 0 {
		return usage
	}
	switch {
	case rest[0] == "add" && len(rest) == 3:
		if *token == "-" {
			raw, err := io.ReadAll(e.stdin)
			if err != nil {
				return err
			}
			*token = string(raw)
		}
		h, err := hosts.Validate(hosts.Host{Name: rest[1], URL: rest[2], Token: *token})
		if err != nil {
			return bad(err)
		}
		if err := hosts.Put(e.home, h); err != nil {
			return err
		}
		out := probeHost(h)
		return e.show(out, func() {
			fmt.Fprint(e.stdout, i18n.T("已登记主机 %s(%s),状态:%s\n", h.Name, h.URL, statusText(out.Status)))
			if out.PlainPublic {
				fmt.Fprint(e.stderr, plainPublicWarning(h.URL))
			}
		})
	case rest[0] == "list" && len(rest) == 1:
		list, err := hosts.Load(e.home)
		if err != nil {
			return err
		}
		out := make([]api.Host, len(list))
		var wg sync.WaitGroup
		for i, h := range list {
			wg.Add(1)
			go func() { defer wg.Done(); out[i] = probeHost(h) }()
		}
		wg.Wait()
		return e.show(out, func() {
			if len(out) == 0 {
				fmt.Fprintln(e.stdout, i18n.T("还没有登记其他主机。用 bugu host add <名字> <地址> --token <令牌> 登记一台。"))
				return
			}
			w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tURL\tSTATUS\tVERSION")
			for _, h := range out {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", h.Name, h.URL, statusText(h.Status), h.Version)
			}
			w.Flush()
		})
	case rest[0] == "rm" && len(rest) == 2:
		if err := hosts.Remove(e.home, rest[1]); err != nil {
			return err
		}
		return e.show(map[string]string{"removed": rest[1]}, func() {
			fmt.Fprint(e.stdout, i18n.T("已删除主机 %s 的登记\n", rest[1]))
		})
	}
	return usage
}

// probeHost 检查一台主机能否连上、令牌是否被接受(G14)。返回的结果不含令牌。
func probeHost(h hosts.Host) api.Host {
	out := api.Host{Name: h.Name, URL: h.URL, Status: api.HostOffline, PlainPublic: hosts.PlainPublic(h.URL)}
	c := client.New(h.URL, h.Token).WithTimeout(probeTimeout)
	if _, err := c.Tasks(); err != nil {
		var apiErr *api.Error
		if errors.As(err, &apiErr) && apiErr.Code == api.CodeUnauthorized {
			out.Status = api.HostUnauthorized
		}
		return out
	}
	out.Status = api.HostOnline
	if about, err := c.About(); err == nil {
		out.Version = about.Version
	}
	return out
}

func statusText(s string) string {
	switch s {
	case api.HostOnline:
		return i18n.T("在线")
	case api.HostUnauthorized:
		return i18n.T("令牌被拒绝")
	}
	return i18n.T("离线或连不上")
}

// plainPublicWarning 是添加经公网的明文 HTTP 地址时的警告(C6)。
func plainPublicWarning(u string) string {
	return i18n.T("bugu: 注意:%s 看起来是公网上的明文 HTTP 地址。令牌和任务内容会以明文经过网络,任何能看到这些流量的人都能拿到令牌并在那台主机上执行命令。建议放在提供 HTTPS 的反向代理之后,或改用组网工具(如 Tailscale)的内网地址。\n", u)
}

// doctorCheck 是 bugu doctor 的一项检查。
type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok、warn、fail
	Detail string `json:"detail"`
}

// doctorCmd 检查运行环境(PRD 5.7):数据目录和令牌文件的权限、后台、系统服务、登录 shell、运行器、登记的主机。
func doctorCmd(e *env, args []string) error {
	if rest, err := parse(flag.NewFlagSet("doctor", flag.ContinueOnError), args); err != nil {
		return err
	} else if len(rest) != 0 {
		return bad(i18n.Errorf("不接受参数 %q", rest[0]))
	}
	var checks []doctorCheck
	add := func(name, status, detail string) { checks = append(checks, doctorCheck{name, status, detail}) }
	local := e.host == ""

	if local {
		if st, err := os.Stat(e.home); err != nil {
			add(i18n.T("数据目录"), "warn", i18n.T("%s 还不存在,后台第一次启动时会建立", e.home))
		} else if st.Mode().Perm()&0o077 != 0 {
			add(i18n.T("数据目录"), "warn", i18n.T("%s 的权限是 %v,其他用户也能读;建议 chmod 700", e.home, st.Mode().Perm()))
		} else {
			add(i18n.T("数据目录"), "ok", e.home)
		}
		if st, err := os.Stat(filepath.Join(e.home, api.TokenFile)); err == nil && st.Mode().Perm()&0o077 != 0 {
			add(i18n.T("令牌文件"), "fail", i18n.T("权限是 %v,其他用户能读到令牌;执行 chmod 600 %s", st.Mode().Perm(), filepath.Join(e.home, api.TokenFile)))
		}
		switch shell := filepath.Base(e.shell); shell {
		case "sh", "bash", "zsh", "dash", "ksh":
			add(i18n.T("登录 shell"), "ok", e.shell)
		case ".", "":
			add(i18n.T("登录 shell"), "warn", i18n.T("没有 SHELL 环境变量,任务的子进程直接继承后台的环境,PATH 可能和终端里不同"))
		default:
			add(i18n.T("登录 shell"), "warn", i18n.T("%s 不是支持的登录 shell(sh、bash、zsh、dash、ksh),任务的子进程直接继承后台的环境", e.shell))
		}
		if m, err := serviceManager(); err == nil {
			if st, err := m.Status(); err == nil && st.Installed {
				add(i18n.T("系统服务"), "ok", i18n.T("已安装:%s", st.File))
			} else {
				if gui.Available {
					add(i18n.T("系统服务"), "ok", i18n.T("没有安装;后台随桌面应用运行。要让任务不依赖应用,执行 bugu daemon install"))
				} else {
					add(i18n.T("系统服务"), "warn", i18n.T("没有安装;后台要一直开着,任务才会按时运行。要开机自动启动,执行 bugu daemon install;在容器里交给容器的重启策略"))
				}
			}
		}
	}

	c, err := e.client()
	if err == nil {
		var about api.Health
		if about, err = c.About(); err == nil {
			add(i18n.T("后台"), "ok", i18n.T("正在运行,版本 %s", about.Version))
		}
	}
	if err != nil {
		add(i18n.T("后台"), "fail", err.Error())
	} else {
		runners, err := c.Runners()
		if err != nil {
			add(i18n.T("运行器"), "fail", err.Error())
		}
		for _, r := range runners {
			check, err := c.CheckRunner(r.Name)
			name := i18n.T("运行器 %s", r.Name)
			switch {
			case err != nil:
				add(name, "fail", err.Error())
			case check.OK && r.Agent:
				add(name, "ok", i18n.T("%s。能启动不代表已登录:第一次使用前,在终端里运行一次这个工具,确认它不需要再登录", check.Detail))
			case check.OK:
				add(name, "ok", check.Detail)
			default:
				// 没装的运行器只有在任务用到时才是问题。
				add(name, "warn", check.Detail)
			}
		}
	}

	if local {
		list, _ := hosts.Load(e.home)
		for _, h := range list {
			p := probeHost(h)
			status := "ok"
			if p.Status != api.HostOnline {
				status = "warn"
			}
			detail := i18n.T("%s,%s", h.URL, statusText(p.Status))
			if p.PlainPublic {
				status, detail = "warn", i18n.T("%s;经公网的明文 HTTP,令牌会以明文传输", detail)
			}
			add(i18n.T("主机 %s", h.Name), status, detail)
		}
	}

	failed := 0
	for _, c := range checks {
		if c.Status == "fail" {
			failed++
		}
	}
	if err := e.show(checks, func() {
		marks := map[string]string{"ok": "✓", "warn": "!", "fail": "✗"}
		for _, c := range checks {
			fmt.Fprintf(e.stdout, "%s %s:%s\n", marks[c.Status], c.Name, c.Detail)
		}
	}); err != nil {
		return err
	}
	if failed > 0 {
		return i18n.Errorf("%d 项检查没有通过", failed)
	}
	return nil
}

// webCmd 启动后台并打开网页面板(PRD 5.1),用于没有桌面应用的机器。本机已有后台在运行时只打开它的面板。
func webCmd(e *env, args []string) error {
	if err := errors.Join(e.localOnly("web"), e.noJSON("web")); err != nil {
		return err
	}
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	listen := fs.String("listen", daemon.DefaultListen, "")
	noOpen := fs.Bool("no-open", false, "")
	if rest, err := parse(fs, args); err != nil {
		return err
	} else if len(rest) != 0 {
		return bad(i18n.Errorf("不接受参数 %q", rest[0]))
	}
	if c, addr, token, err := client.LocalEndpoint(e.home); err == nil && c.Health() == nil {
		panel(e, addr, token, *noOpen)
		fmt.Fprint(e.stderr, i18n.T("bugu: 本机已有后台在运行,打开的是它的网页面板\n"))
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d, err := daemon.Start(daemon.Options{Home: e.home, Listen: *listen, Log: e.stderr, LoginShell: e.shell, Version: e.version})
	if err != nil {
		return err
	}
	fmt.Fprint(e.stderr, i18n.T("bugu: 后台已启动,监听 %s,数据目录 %s\n", d.Listening(), e.home))
	if _, _, token, err := client.LocalEndpoint(e.home); err == nil {
		panel(e, d.Addr(), token, *noOpen)
	}
	<-ctx.Done()
	fmt.Fprintln(e.stderr, i18n.T("bugu: 正在停止,进行中的运行将被终止并记为中断"))
	return d.Close()
}

// panel 给出网页面板的网址(令牌放在网址片段里,决策记录 0007),能打开浏览器时顺便打开。
func panel(e *env, addr, token string, noOpen bool) {
	link := "http://" + addr + "/#" + url.Values{"token": {token}}.Encode()
	if !noOpen && openBrowser(link) == nil {
		fmt.Fprint(e.stderr, i18n.T("bugu: 已在浏览器里打开网页面板 http://%s\n", addr))
		return
	}
	fmt.Fprint(e.stderr, i18n.T("bugu: 在浏览器里打开下面的网址(其中含有令牌,不要分享):\n"))
	fmt.Fprintln(e.stdout, link)
}

// openBrowser 用系统的默认浏览器打开网址。没有图形界面的 Linux 上会失败。
func openBrowser(link string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", link).Run()
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no display")
		}
		return exec.Command("xdg-open", link).Start()
	}
	return errors.New("unsupported")
}

// serviceManager 返回当前系统上的服务管理。
func serviceManager() (service.Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return service.Manager{}, err
	}
	return service.Manager{GOOS: runtime.GOOS, UserHome: home, UID: os.Getuid(),
		Run: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return string(out), err
		},
		Has: func(name string) bool { _, err := exec.LookPath(name); return err == nil },
	}, nil
}

// daemonCmd 把后台注册为系统服务(PRD 5.2、5.3)。
func daemonCmd(e *env, args []string) error {
	if err := e.localOnly("daemon"); err != nil {
		return err
	}
	rest, err := parse(flag.NewFlagSet("daemon", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || (rest[0] != "install" && rest[0] != "uninstall" && rest[0] != "status") {
		return bad(i18n.Errorf("用法:bugu daemon install、bugu daemon uninstall 或 bugu daemon status"))
	}
	m, err := serviceManager()
	if err != nil {
		return err
	}
	running := false
	if c, err := client.Local(e.home); err == nil && c.Health() == nil {
		running = true
	}
	switch rest[0] {
	case "install":
		if st, _ := m.Status(); running && !st.Loaded {
			return i18n.Errorf("本机已有后台在运行(多半是桌面应用或 bugu serve)。同一个数据目录只能有一个后台:先退出它,再安装服务")
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return err
		}
		spec := service.Spec{Exe: exe, Shell: e.shell, Log: filepath.Join(e.home, "serve.log")}
		if os.Getenv("BUGU_HOME") != "" {
			spec.Home = e.home
		}
		file, err := m.Install(spec)
		if err != nil {
			return err
		}
		return e.show(map[string]string{"installed": file}, func() {
			fmt.Fprint(e.stdout, i18n.T("已安装为系统服务:%s\n后台会在登录后自动启动,异常退出时重启。桌面应用打开时只做它的界面。\n", file))
			if runtime.GOOS == "linux" {
				fmt.Fprint(e.stdout, i18n.T("在服务器上要让它不登录也运行,执行 loginctl enable-linger %s\n", os.Getenv("USER")))
			}
		})
	case "uninstall":
		if err := m.Uninstall(); err != nil {
			return err
		}
		return e.show(map[string]bool{"uninstalled": true}, func() {
			fmt.Fprintln(e.stdout, i18n.T("已卸载系统服务。之后后台随桌面应用或 bugu serve 运行。"))
		})
	}
	st, err := m.Status()
	if err != nil {
		return err
	}
	return e.show(struct {
		service.Status
		Running bool `json:"running"`
	}{st, running}, func() {
		switch {
		case !st.Installed:
			fmt.Fprintln(e.stdout, i18n.T("没有安装为系统服务。"))
		case st.Loaded:
			fmt.Fprint(e.stdout, i18n.T("已安装为系统服务(%s),系统已经加载它。\n", st.File))
		default:
			fmt.Fprint(e.stdout, i18n.T("已安装为系统服务(%s),但系统没有加载它;重新执行 bugu daemon install 试试。\n", st.File))
		}
		if running {
			fmt.Fprintln(e.stdout, i18n.T("后台正在运行。"))
		} else {
			fmt.Fprintln(e.stdout, i18n.T("后台没有在运行。"))
		}
	})
}

// skillCmd 打印教 AI agent 使用 bugu 的技能说明文件(A2)。
func skillCmd(e *env, args []string) error {
	if rest, err := parse(flag.NewFlagSet("skill", flag.ContinueOnError), args); err != nil {
		return err
	} else if len(rest) != 0 {
		return bad(i18n.Errorf("不接受参数 %q", rest[0]))
	}
	text := skills.Zh
	if i18n.Current() == i18n.En {
		text = skills.En
	}
	return e.show(map[string]string{"skill": text}, func() { fmt.Fprint(e.stdout, text) })
}
