// Package cli 实现 bugu 的子命令。命令通过 internal/client 访问后台,
// 只有启动后台的命令才引用 internal/daemon。
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"bugu/internal/api"
	"bugu/internal/client"
	"bugu/internal/daemon"
	"bugu/internal/gui"
	"bugu/internal/hosts"
	"bugu/internal/i18n"
	"bugu/internal/task"
)

var usage = i18n.Text{Zh: `bugu:桌面优先的定时任务应用

用法:
  bugu app                           打开桌面应用(菜单栏图标加主窗口),同时承载后台
  bugu serve [--listen 地址]         只启动后台,没有界面(默认只监听本机)
  bugu add <任务> --schedule <cron> (--command <命令> | --path <脚本>)
           [--at <时间>] [--jitter <时长>] [--cwd <目录>] [--env 名=值]... [--timeout <时长>]
           [--notify on_content|on_result|on_failure|always|none] [--silent-if <标记>]
           [--output-dir <目录> --output-name <文件名模板> [--output-from stdout|task]]
           [--timezone <IANA 时区>] [--on-missed run_once|skip|window [--missed-window <时长>]]
           [--env-file <文件>] [--webhook <地址>|none [--webhook-when on_content|on_result|on_failure|always]]
  bugu add <任务> --schedule <cron> --runner <运行器> (--prompt <提示词> | --prompt-file <文件>)
           [--permission read_only|write_cwd|full]  让 AI 命令行执行,默认只读
  bugu add --file <文件>             从 JSON 文件新建任务,- 表示标准输入;多步骤任务用这种方式建
  bugu list                          列出任务及下次运行时间
  bugu show <任务>                   查看任务定义(JSON,可用于 add --file)
  bugu edit <任务> [选项]            修改任务,选项同 add,只改给出的部分
  bugu rm <任务>                     删除任务
  bugu run <任务>                    立即运行一次
  bugu pause <任务> / resume <任务>  暂停和恢复
  bugu runs [任务] [-n 条数]         运行历史
  bugu logs <运行编号>               查看某次运行的输出
  bugu inbox [--all]                 收件箱:未读的结果,--all 包括已读的
  bugu inbox read <运行编号>... | --all  标为已读
  bugu runner list                   列出运行器及各自可选的权限级别
  bugu runner check [运行器]...      检查运行器是否已安装、能否启动,不给名字则检查全部
  bugu language [auto|zh|en]         查看或设置后台和界面的语言,auto 表示跟随系统
  bugu run <任务> --dry-run          列出立即运行时会执行的命令,不真正运行
  bugu runner add <文件>             添加或替换一份运行器配置(JSON),- 表示标准输入
  bugu token list | create <名字> | revoke <名字>  管理访问令牌;新令牌只显示一次
  bugu host add <名字> <地址> --token <令牌> | list | rm <名字>  登记其他主机,之后用 --host <名字> 操作它
  bugu doctor                        检查运行环境:数据目录、后台、登录 shell、运行器、登记的主机
  bugu web [--listen 地址] [--no-open]  启动后台并在浏览器里打开网页面板,用于没有桌面应用的机器
  bugu daemon install | uninstall | status  把后台注册为当前用户的系统服务(launchd 或 systemd)
  bugu skill                         打印教 AI agent 使用 bugu 的说明
  bugu version                       显示版本

提示词和命令里可以使用 {date}、{time}、{datetime}、{last_run}、{run_dir}、{state_dir}、{task_name},
多步骤任务里还有 {prev}(上一步的输出)和 {steps.<步骤名>.output}。
脚本以退出码 75 结束表示无事可做:后面的步骤不再执行,也不发通知。
输出包含 --silent-if 给出的标记时,本次记为无内容,也不发通知。
设置了产物时,{output_file} 是本次的产物文件:默认保存最后一步的输出,--output-from task 表示由任务自己写。
--env-file 指向的文件每行一个 名=值,用来放密钥;其中的值在输出、存档和通知里替换为"[已隐藏 名字]"。
不给 --cwd 时,任务在自己的状态目录 {state_dir} 里运行。
任务可以在 {run_dir}/result.json 写结果信封(status、summary、artifacts、notify、message),有信封时以它为准。
所有命令都接受 --lang zh|en,指定命令行自己说的语言;默认按环境变量 LC_ALL、LC_MESSAGES、LANG。
所有命令都接受 --json(输出接口的 JSON,出错时标准错误是 {"error": {"code", "message"}})和 --host <名字>(对登记过的主机执行)。
数据目录默认为 ~/.bugu,可用环境变量 BUGU_HOME 指定;自定义运行器放在其中的 runners/ 下。
`, En: `bugu: a desktop-first scheduled task app

Usage:
  bugu app                           Open the desktop app (menu bar icon and main window), which also hosts the backend
  bugu serve [--listen addr]         Start only the backend, without a UI (listens on this machine only by default)
  bugu add <task> --schedule <cron> (--command <cmd> | --path <script>)
           [--at <time>] [--jitter <duration>] [--cwd <dir>] [--env NAME=VALUE]... [--timeout <duration>]
           [--notify on_content|on_result|on_failure|always|none] [--silent-if <marker>]
           [--output-dir <dir> --output-name <file name template> [--output-from stdout|task]]
           [--timezone <IANA time zone>] [--on-missed run_once|skip|window [--missed-window <duration>]]
           [--env-file <file>] [--webhook <url>|none [--webhook-when on_content|on_result|on_failure|always]]
  bugu add <task> --schedule <cron> --runner <runner> (--prompt <prompt> | --prompt-file <file>)
           [--permission read_only|write_cwd|full]  Run with an AI CLI, read-only by default
  bugu add --file <file>             Create a task from a JSON file, - for standard input; use this for multi-step tasks
  bugu list                          List tasks and their next run times
  bugu show <task>                   Show a task definition (JSON, usable with add --file)
  bugu edit <task> [options]         Change a task; same options as add, only the given ones change
  bugu rm <task>                     Delete a task
  bugu run <task>                    Run once now
  bugu pause <task> / resume <task>  Pause and resume
  bugu runs [task] [-n count]        Run history
  bugu logs <run ID>                 Show the output of a run
  bugu inbox [--all]                 Inbox: unread results; --all includes read ones
  bugu inbox read <run ID>... | --all  Mark as read
  bugu runner list                   List runners and the permission levels each one offers
  bugu runner check [runner]...      Check that runners are installed and start; checks all of them without names
  bugu language [auto|zh|en]         Show or set the language of the backend and the UI; auto follows the system
  bugu run <task> --dry-run          List the commands a run would execute, without running
  bugu runner add <file>             Add or replace a runner configuration (JSON), - for standard input
  bugu token list | create <name> | revoke <name>  Manage access tokens; a new token is shown only once
  bugu host add <name> <url> --token <token> | list | rm <name>  Register other hosts, then act on one with --host <name>
  bugu doctor                        Check the environment: data directory, backend, login shell, runners, registered hosts
  bugu web [--listen addr] [--no-open]  Start the backend and open the web panel in a browser, for machines without the desktop app
  bugu daemon install | uninstall | status  Register the backend as a service for this user (launchd or systemd)
  bugu skill                         Print the guide that teaches AI agents to use bugu
  bugu version                       Show the version

Prompts and commands can use {date}, {time}, {datetime}, {last_run}, {run_dir}, {state_dir} and {task_name};
multi-step tasks also have {prev} (the previous step's output) and {steps.<step name>.output}.
A script that exits with code 75 means there is nothing to do: later steps don't run and no notification is sent.
When the output contains the marker given by --silent-if, the run counts as having no content and sends no notification.
With an output file set, {output_file} is this run's file: by default it saves the last step's output; --output-from task means the task writes it itself.
Without --cwd, a task runs in its own state folder, {state_dir}.
The file given by --env-file has one NAME=VALUE per line and is meant for secrets; its values are replaced with "[hidden NAME]" in outputs, archives and notifications.
A task can write a result envelope to {run_dir}/result.json (status, summary, artifacts, notify, message); when present, it decides the result.
Every command accepts --lang zh|en for the language of the command line itself; by default it follows LC_ALL, LC_MESSAGES and LANG.
Every command accepts --json (the API's JSON on stdout; on errors, {"error": {"code", "message"}} on stderr) and --host <name> (act on a registered host).
The data directory is ~/.bugu by default and can be set with the BUGU_HOME environment variable; custom runners go in its runners/ folder.
`}

// errUsage 表示命令的用法有误,退出码为 2。
type errUsage struct{ err error }

func (e errUsage) Error() string { return e.err.Error() }

// bad 把说明用法错在哪里的错误标为用法错误。
func bad(err error) error { return errUsage{err} }

func usagef(format string, args ...any) error { return errUsage{fmt.Errorf(format, args...)} }

type env struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	home           string
	shell          string // 用户的登录 shell,启动后台时交给它(E1)
	version        string
	json           bool   // --json:输出接口的 JSON,出错时标准错误也是 JSON(A1)
	host           string // --host:对登记过的哪台主机执行,空表示本机
}

// client 返回要访问的后台:本机的,或 --host 指定的那台。
func (e *env) client() (*client.Client, error) {
	if e.host == "" {
		return client.Local(e.home)
	}
	h, err := hosts.Find(e.home, e.host)
	if err != nil {
		return nil, err
	}
	return client.New(h.URL, h.Token), nil
}

// show 输出一个结果:--json 时输出 v,否则调用 text 输出给人看的文字。
func (e *env) show(v any, text func()) error {
	if e.json {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	text()
	return nil
}

// localOnly 拒绝对 --host 执行只对本机有意义的命令。
func (e *env) localOnly(name string) error {
	if e.host != "" {
		return bad(i18n.Errorf("bugu %s 只能在本机执行,不接受 --host", name))
	}
	return nil
}

// noJSON 拒绝对长时间运行、没有结果可输出的命令使用 --json。
func (e *env) noJSON(name string) error {
	if e.json {
		return bad(i18n.Errorf("bugu %s 一直运行到退出,没有 JSON 结果,不接受 --json", name))
	}
	return nil
}

// Main 执行一次命令行调用,返回进程退出码。
func Main(version string, args []string, stdout, stderr io.Writer) int {
	args, g, err := globalFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, "bugu:", err)
		return 2
	}
	i18n.Set(g.lang)
	if len(args) == 0 {
		// 不带参数就是打开桌面应用(PRD 5.1),双击应用包走的就是这里。无界面构建里显示用法。
		if !gui.Available {
			fmt.Fprint(stdout, usage.String())
			return 0
		}
		args = []string{"app"}
	}
	switch args[0] {
	case "version", "--version", "-v":
		if g.json {
			fmt.Fprintf(stdout, "{\"version\": %q}\n", version)
		} else {
			fmt.Fprintln(stdout, "bugu", version)
		}
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage.String())
		return 0
	}
	commands := map[string]func(*env, []string) error{
		"app": appCmd, "serve": serveCmd, "add": addCmd, "list": listCmd, "show": showCmd, "edit": editCmd, "rm": rmCmd,
		"run": runCmd, "pause": pauseCmd, "resume": resumeCmd, "runs": runsCmd, "logs": logsCmd, "runner": runnerCmd,
		"inbox": inboxCmd, "language": languageCmd, "token": tokenCmd, "host": hostCmd, "doctor": doctorCmd,
		"web": webCmd, "daemon": daemonCmd, "skill": skillCmd,
	}
	cmd, ok := commands[args[0]]
	if !ok {
		return fail(stderr, g.json, args[0], errUsage{i18n.Errorf("未知命令 %q", args[0])})
	}
	home, err := homeDir()
	if err != nil {
		return fail(stderr, g.json, args[0], err)
	}
	e := &env{stdin: os.Stdin, stdout: stdout, stderr: stderr, home: home, shell: os.Getenv("SHELL"), version: version, json: g.json, host: g.host}
	err = cmd(e, args[1:])
	if errors.Is(err, errHelp) {
		text := commandHelp(args[0])
		return fail(stderr, g.json, args[0], e.show(map[string]string{"help": text}, func() { fmt.Fprint(stdout, text) }))
	}
	return fail(stderr, g.json, args[0], err)
}

// errHelp 表示命令带了 -h 或 --help:显示这个命令的用法,退出码为 0(A1)。
var errHelp = errors.New("help requested")

// commandHelp 从总用法里取出命令 name 的那几行,加上末尾关于模板变量、退出码等的说明。
func commandHelp(name string) string {
	// 总用法分三段:标题、命令列表(以"用法:"开头)、说明。
	parts := strings.SplitN(usage.String(), "\n\n", 3)
	lines := strings.Split(parts[1], "\n")
	names := []string{name}
	if name == "edit" {
		names = append(names, "add") // edit 的选项同 add
	}
	var b strings.Builder
	b.WriteString(lines[0] + "\n")
	in := false
	for _, l := range lines[1:] {
		switch {
		case slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(l, "  bugu "+n+" ") }):
			in = true
		case strings.HasPrefix(l, "  bugu "):
			in = false
		case !strings.HasPrefix(l, "    "):
			in = false
		}
		if in {
			b.WriteString(l + "\n")
		}
	}
	return b.String() + "\n" + parts[2]
}

// 命令行自己的错误代码,与接口的错误代码(api.Code…)放在一起供 --json 的使用者区分。
const (
	codeUsage       = "usage"       // 用法有误,退出码 2
	codeUnavailable = "unavailable" // 连不上后台
	codeError       = "error"       // 其他错误
)

// fail 报告命令的结果并返回退出码。--json 时错误写成 {"error": {"code", "message"}}(A1)。
func fail(stderr io.Writer, asJSON bool, name string, err error) int {
	if err == nil {
		return 0
	}
	exit, code := 1, codeError
	var u errUsage
	var apiErr *api.Error
	var unknown hosts.ErrUnknown
	switch {
	case errors.As(err, &u):
		exit, code = 2, codeUsage
	case errors.As(err, &apiErr):
		code = apiErr.Code
	case errors.Is(err, client.ErrNotRunning), errors.As(err, new(client.ErrUnreachable)):
		code = codeUnavailable
	case errors.As(err, &unknown):
		exit, code = 2, codeUsage
	}
	if asJSON {
		b, _ := json.Marshal(map[string]api.Error{"error": {Code: code, Message: err.Error()}})
		fmt.Fprintf(stderr, "%s\n", b)
		return exit
	}
	if exit == 2 && errors.As(err, &u) {
		fmt.Fprint(stderr, i18n.T("bugu %s: %s\n用 bugu help 查看用法\n", name, u.err))
		return exit
	}
	fmt.Fprintln(stderr, "bugu:", err)
	return exit
}

// globals 是可以写在任何位置的全局参数。
type globals struct {
	lang i18n.Lang
	json bool
	host string
}

// globalFlags 从参数里取出 --lang、--json 和 --host,它们可以出现在任何位置。
// 没有给出 --lang 时按环境变量(决策记录 0014)。
func globalFlags(args []string) (rest []string, g globals, err error) {
	g.lang = i18n.System()
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--json" {
			g.json = true
			continue
		}
		name, v, hasValue := strings.Cut(a, "=")
		if name != "--lang" && name != "--host" {
			rest = append(rest, a)
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, g, i18n.Errorf("%s 需要一个值", name)
			}
			v = args[i+1]
			i++
		}
		if name == "--host" {
			g.host = v
			continue
		}
		switch l := i18n.Lang(v); l {
		case i18n.Zh, i18n.En:
			g.lang = l
		default:
			return nil, g, i18n.Errorf("--lang %q 不合法,可用的是 zh、en", v)
		}
	}
	return rest, g, nil
}

func homeDir() (string, error) {
	if dir := os.Getenv("BUGU_HOME"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", i18n.Errorf("找不到用户主目录,请用 BUGU_HOME 指定数据目录: %w", err)
	}
	return filepath.Join(home, ".bugu"), nil
}

// parse 解析命令的参数。位置参数可以写在选项之前或之后。
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, errHelp
			}
			return nil, usagef("%v", err)
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// one 解析只接受一个位置参数、没有选项的命令。
func one(name, what string, args []string) (string, error) {
	rest, err := parse(flag.NewFlagSet(name, flag.ContinueOnError), args)
	if err != nil {
		return "", err
	}
	if len(rest) != 1 {
		return "", bad(i18n.Errorf("需要且只需要一个参数:%s", what))
	}
	return rest[0], nil
}

func appCmd(e *env, args []string) error {
	if err := errors.Join(e.localOnly("app"), e.noJSON("app")); err != nil {
		return err
	}
	if rest, err := parse(flag.NewFlagSet("app", flag.ContinueOnError), args); err != nil {
		return err
	} else if len(rest) != 0 {
		return bad(i18n.Errorf("不接受参数 %q", rest[0]))
	}
	return gui.Run(e.home, e.version, e.stderr)
}

func serveCmd(e *env, args []string) error {
	if err := errors.Join(e.localOnly("serve"), e.noJSON("serve")); err != nil {
		return err
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", daemon.DefaultListen, "")
	if rest, err := parse(fs, args); err != nil {
		return err
	} else if len(rest) != 0 {
		return bad(i18n.Errorf("不接受参数 %q", rest[0]))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, e, *listen)
}

// serve 运行后台直到 ctx 结束。
func serve(ctx context.Context, e *env, listen string) error {
	d, err := daemon.Start(daemon.Options{Home: e.home, Listen: listen, Log: e.stderr, LoginShell: e.shell, Version: e.version})
	if err != nil {
		return err
	}
	fmt.Fprint(e.stderr, i18n.T("bugu: 后台已启动,监听 %s,数据目录 %s\n", d.Listening(), e.home))
	<-ctx.Done()
	fmt.Fprintln(e.stderr, i18n.T("bugu: 正在停止,进行中的运行将被终止并记为中断"))
	return d.Close()
}

// envFlag 收集可重复的 --env 名=值。
type envFlag map[string]string

func (f envFlag) String() string { return "" }
func (f envFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return i18n.Errorf("应写成 名=值,收到 %q", s)
	}
	f[k] = v
	return nil
}

// taskFlags 是 add 和 edit 共用的选项。
type taskFlags struct {
	fs                                     *flag.FlagSet
	schedule, at, command, path, cwd, file string
	timeout, notify, silentIf              string
	outputDir, outputName, outputFrom      string
	timezone, onMissed, missedWindow       string
	jitter                                 string
	envFile, webhook, webhookWhen          string
	runner, prompt, promptFile, permission string
	env                                    envFlag
}

func newTaskFlags(name string) *taskFlags {
	f := &taskFlags{fs: flag.NewFlagSet(name, flag.ContinueOnError), env: envFlag{}}
	f.fs.StringVar(&f.schedule, "schedule", "", "")
	f.fs.StringVar(&f.at, "at", "", "")
	f.fs.StringVar(&f.command, "command", "", "")
	f.fs.StringVar(&f.path, "path", "", "")
	f.fs.StringVar(&f.cwd, "cwd", "", "")
	f.fs.StringVar(&f.timeout, "timeout", "", "")
	f.fs.StringVar(&f.file, "file", "", "")
	f.fs.StringVar(&f.notify, "notify", "", "")
	f.fs.StringVar(&f.silentIf, "silent-if", "", "")
	f.fs.StringVar(&f.outputDir, "output-dir", "", "")
	f.fs.StringVar(&f.outputName, "output-name", "", "")
	f.fs.StringVar(&f.outputFrom, "output-from", "", "")
	f.fs.StringVar(&f.timezone, "timezone", "", "")
	f.fs.StringVar(&f.onMissed, "on-missed", "", "")
	f.fs.StringVar(&f.missedWindow, "missed-window", "", "")
	f.fs.StringVar(&f.jitter, "jitter", "", "")
	f.fs.StringVar(&f.envFile, "env-file", "", "")
	f.fs.StringVar(&f.webhook, "webhook", "", "")
	f.fs.StringVar(&f.webhookWhen, "webhook-when", "", "")
	f.fs.StringVar(&f.runner, "runner", "", "")
	f.fs.StringVar(&f.prompt, "prompt", "", "")
	f.fs.StringVar(&f.promptFile, "prompt-file", "", "")
	f.fs.StringVar(&f.permission, "permission", "", "")
	f.fs.Var(f.env, "env", "")
	return f
}

// given 返回命令行上实际出现过的选项。
func (f *taskFlags) given() map[string]bool {
	set := map[string]bool{}
	f.fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
	return set
}

// apply 把给出的选项写进任务定义,没给出的保持不变。
func (f *taskFlags) apply(t *task.Task) error {
	given := f.given()
	if given["schedule"] && given["at"] {
		return bad(i18n.Errorf("--schedule 和 --at 只能给一个"))
	}
	if given["command"] && given["path"] {
		return bad(i18n.Errorf("--command 和 --path 只能给一个"))
	}
	if given["schedule"] {
		t.Trigger = task.Trigger{Cron: f.schedule, Jitter: t.Trigger.Jitter} // 换时间时保留随机偏移
	}
	if given["at"] {
		at, err := parseTime(f.at)
		if err != nil {
			return err
		}
		t.Trigger = task.Trigger{At: at}
	}
	script := given["command"] || given["path"]
	agent := given["runner"] || given["prompt"] || given["prompt-file"] || given["permission"]
	if script && agent {
		return bad(i18n.Errorf("脚本(--command、--path)和 AI 命令行(--runner、--prompt、--prompt-file、--permission)只能选一种"))
	}
	if given["prompt"] && given["prompt-file"] {
		return bad(i18n.Errorf("--prompt 和 --prompt-file 只能给一个"))
	}
	if (script || agent) && len(t.Steps) > 1 {
		return bad(i18n.Errorf("任务 %s 有 %d 个步骤,这些选项只能修改单步任务;用 bugu show %s 导出定义,改好后用 bugu edit %s --file <文件>", t.Name, len(t.Steps), t.Name, t.Name))
	}
	if script {
		t.Steps = []task.Step{{Type: task.StepScript, Script: task.Script{Command: f.command, Path: f.path}}}
	}
	if agent {
		// 已经是 AI 命令行步骤时只改给出的部分,否则换成一个新的。
		var a task.Agent
		if len(t.Steps) == 1 && t.Steps[0].Type == task.StepAgent {
			a = t.Steps[0].Agent
		}
		if given["runner"] {
			a.Runner = f.runner
		}
		if given["prompt"] {
			a.Prompt = f.prompt
		}
		if given["prompt-file"] {
			raw, err := os.ReadFile(f.promptFile)
			if err != nil {
				return i18n.Errorf("读取提示词文件: %w", err)
			}
			a.Prompt = string(raw)
		}
		if given["permission"] {
			a.Permission = f.permission
		}
		if a.Runner == "" {
			return bad(i18n.Errorf("缺少运行器:用 --runner 指定,可用 bugu runner list 查看有哪些"))
		}
		if a.Prompt == "" {
			return bad(i18n.Errorf("缺少提示词:用 --prompt 直接给出,或用 --prompt-file 从文件读取"))
		}
		t.Steps = []task.Step{{Type: task.StepAgent, Agent: a}}
	}
	if given["cwd"] {
		t.Cwd = f.cwd
	}
	if given["silent-if"] {
		t.SilentIf = f.silentIf
	}
	if given["timezone"] {
		t.Timezone = f.timezone
	}
	if given["env-file"] {
		t.EnvFile = f.envFile
	}
	if given["on-missed"] {
		t.OnMissed = f.onMissed
		if f.onMissed != task.MissedWindow && !given["missed-window"] {
			t.MissedWindow = 0
		}
	}
	if given["jitter"] {
		d, err := time.ParseDuration(f.jitter)
		if err != nil {
			return bad(i18n.Errorf(`--jitter %q 无法解析,应写成 "10m"、"90s" 这样的形式,0 表示不偏移`, f.jitter))
		}
		t.Trigger.Jitter = task.Duration(d)
	}
	if given["missed-window"] {
		d, err := time.ParseDuration(f.missedWindow)
		if err != nil {
			return bad(i18n.Errorf(`--missed-window %q 无法解析,应写成 "30m"、"2h" 这样的形式`, f.missedWindow))
		}
		t.MissedWindow = task.Duration(d)
	}
	if given["webhook-when"] && !given["webhook"] {
		return bad(i18n.Errorf("--webhook-when 要和 --webhook 一起给出"))
	}
	if given["webhook"] {
		// 只替换 webhook 这一条,其他渠道的设置保持不变。
		kept := slices.DeleteFunc(slices.Clone(t.Notifications), func(n task.Notification) bool { return n.Channel == task.ChannelWebhook })
		if f.webhook != "none" {
			kept = append(kept, task.Notification{Channel: task.ChannelWebhook, URL: f.webhook, When: f.webhookWhen})
		}
		t.Notifications = kept
	}
	if given["output-dir"] || given["output-name"] || given["output-from"] {
		o := task.Output{}
		if t.Output != nil {
			o = *t.Output
		}
		if given["output-dir"] {
			o.Dir = f.outputDir
		}
		if given["output-name"] {
			o.Name = f.outputName
		}
		if given["output-from"] {
			o.From = f.outputFrom
		}
		t.Output = &o
		if o.Dir == "" && o.Name == "" { // 两项都清空表示不再落盘
			t.Output = nil
		}
	}
	if given["timeout"] {
		d, err := time.ParseDuration(f.timeout)
		if err != nil {
			return bad(i18n.Errorf(`--timeout %q 无法解析,应写成 "30m"、"1h30m" 这样的形式,0 表示不设上限`, f.timeout))
		}
		t.Timeout = task.Duration(d)
	}
	if given["notify"] {
		// 只替换系统通知这一条,其他渠道的设置保持不变。
		kept := slices.DeleteFunc(slices.Clone(t.Notifications), func(n task.Notification) bool { return n.Channel == task.ChannelSystem })
		switch f.notify {
		case "none":
		case task.NotifyOnContent, task.NotifyOnResult, task.NotifyOnFailure, task.NotifyAlways:
			kept = append(kept, task.Notification{Channel: task.ChannelSystem, When: f.notify})
		default:
			return bad(i18n.Errorf("--notify %q 不合法,可用的是 on_content(失败或有输出时)、on_result(只在有结果时)、on_failure、always、none", f.notify))
		}
		t.Notifications = kept
	}
	for k, v := range f.env {
		if t.Env == nil {
			t.Env = map[string]string{}
		}
		t.Env[k] = v
	}
	return nil
}

// parseTime 接受带时区的 RFC 3339 时间,或按本机时区解释的 "2006-01-02 15:04"。
func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, bad(i18n.Errorf(`--at %q 无法解析,应写成 "2026-11-12 02:00"(本机时区)或 2026-11-12T02:00:00+08:00`, s))
}

func (e *env) readTask(file string) (task.Task, error) {
	var t task.Task
	var raw []byte
	var err error
	if file == "-" {
		raw, err = io.ReadAll(e.stdin)
	} else {
		raw, err = os.ReadFile(file)
	}
	if err != nil {
		return t, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return t, i18n.Errorf("%s 不是合法的任务定义: %v", file, err)
	}
	return t, nil
}

func addCmd(e *env, args []string) error {
	f := newTaskFlags("add")
	rest, err := parse(f.fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return bad(i18n.Errorf("只能给一个任务名,多出了 %q", rest[1]))
	}
	var t task.Task
	if f.file != "" {
		if t, err = e.readTask(f.file); err != nil {
			return err
		}
	}
	if len(rest) == 1 {
		t.Name = rest[0]
	}
	if t.Name == "" {
		return bad(i18n.Errorf("缺少任务名"))
	}
	if err := f.apply(&t); err != nil {
		return err
	}
	if f.file == "" {
		if t.Trigger == (task.Trigger{}) {
			return bad(i18n.Errorf("缺少触发方式:用 --schedule 给出 cron 表达式,或用 --at 给出一次性时间点"))
		}
		if len(t.Steps) == 0 {
			return bad(i18n.Errorf("缺少要执行的内容:用 --command 给出一条命令,用 --path 给出脚本文件,或用 --runner 和 --prompt 交给 AI 命令行"))
		}
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	info, err := c.CreateTask(t)
	if err != nil {
		return err
	}
	return e.show(info, func() {
		fmt.Fprint(e.stdout, i18n.T("已创建任务 %s,下次运行:%s\n", info.Name, when(info.NextRun)))
	})
}

func editCmd(e *env, args []string) error {
	f := newTaskFlags("edit")
	rest, err := parse(f.fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return bad(i18n.Errorf("需要且只需要一个参数:任务名"))
	}
	if len(f.given()) == 0 {
		return bad(i18n.Errorf("没有给出要修改的内容"))
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	var t task.Task
	if f.file != "" {
		if t, err = e.readTask(f.file); err != nil {
			return err
		}
		if t.Name == "" {
			t.Name = rest[0]
		} else if t.Name != rest[0] {
			return bad(i18n.Errorf("文件里的任务名是 %q,与要修改的 %q 不一致;不支持改名", t.Name, rest[0]))
		}
	} else {
		info, err := c.Task(rest[0])
		if err != nil {
			return err
		}
		t = info.Task
	}
	if err := f.apply(&t); err != nil {
		return err
	}
	info, err := c.UpdateTask(t)
	if err != nil {
		return err
	}
	return e.show(info, func() {
		fmt.Fprint(e.stdout, i18n.T("已修改任务 %s,下次运行:%s\n", info.Name, when(info.NextRun)))
	})
}

func when(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// shortDuration 去掉时长末尾为零的单位:10m0s 写成 10m,1h0m0s 写成 1h,和界面上的"±10 分钟"一致。
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func trigger(tr task.Trigger) string {
	if tr.Cron != "" && tr.Jitter > 0 {
		return fmt.Sprintf("%s ±%s", tr.Cron, shortDuration(time.Duration(tr.Jitter)))
	}
	if tr.Cron != "" {
		return tr.Cron
	}
	return i18n.T("一次 %s", when(tr.At))
}

func listCmd(e *env, args []string) error {
	if rest, err := parse(flag.NewFlagSet("list", flag.ContinueOnError), args); err != nil {
		return err
	} else if len(rest) != 0 {
		return bad(i18n.Errorf("不接受参数 %q", rest[0]))
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	tasks, err := c.Tasks()
	if err != nil {
		return err
	}
	if tasks == nil {
		tasks = []api.TaskInfo{}
	}
	if e.json || len(tasks) == 0 {
		return e.show(tasks, func() { fmt.Fprintln(e.stdout, i18n.T("还没有任务。用 bugu add 新建一个。")) })
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATE\tNEXT RUN\tTRIGGER")
	for _, t := range tasks {
		state := "enabled"
		switch {
		case t.Running:
			state = "running"
		case t.Paused:
			state = "paused"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.Name, state, when(t.NextRun), trigger(t.Trigger))
	}
	return w.Flush()
}

func showCmd(e *env, args []string) error {
	name, err := one("show", i18n.T("任务名"), args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	info, err := c.Task(name)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(info.Task, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "%s\n", b)
	return nil
}

func rmCmd(e *env, args []string) error {
	name, err := one("rm", i18n.T("任务名"), args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	if err := c.DeleteTask(name); err != nil {
		return err
	}
	return e.show(map[string]string{"deleted": name}, func() { fmt.Fprint(e.stdout, i18n.T("已删除任务 %s,运行记录仍然保留\n", name)) })
}

func pauseCmd(e *env, args []string) error {
	name, err := one("pause", i18n.T("任务名"), args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	info, err := c.Pause(name)
	if err != nil {
		return err
	}
	return e.show(info, func() { fmt.Fprint(e.stdout, i18n.T("已暂停任务 %s\n", name)) })
}

func resumeCmd(e *env, args []string) error {
	name, err := one("resume", i18n.T("任务名"), args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	info, err := c.Resume(name)
	if err != nil {
		return err
	}
	return e.show(info, func() { fmt.Fprint(e.stdout, i18n.T("已恢复任务 %s,下次运行:%s\n", name, when(info.NextRun))) })
}

func runCmd(e *env, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return bad(i18n.Errorf("需要且只需要一个参数:%s", i18n.T("任务名")))
	}
	name := rest[0]
	c, err := e.client()
	if err != nil {
		return err
	}
	if *dry {
		p, err := c.Plan(name)
		if err != nil {
			return err
		}
		return e.show(p, func() { printPlan(e.stdout, p) })
	}
	r, err := c.RunNow(name, client.NewKey())
	if err != nil {
		return err
	}
	return e.show(r, func() {
		fmt.Fprint(e.stdout, i18n.T("已开始运行 %s,运行编号 %s\n用 bugu logs %s 查看输出\n", name, r.ID, r.ID))
	})
}

func runsCmd(e *env, args []string) error {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	limit := fs.Int("n", 20, "")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 || *limit < 0 {
		return bad(i18n.Errorf("用法:bugu runs [任务] [-n 条数]"))
	}
	name := ""
	if len(rest) == 1 {
		name = rest[0]
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	runs, err := c.Runs(name, *limit)
	if err != nil {
		return err
	}
	if runs == nil {
		runs = []task.Run{}
	}
	if e.json || len(runs) == 0 {
		return e.show(runs, func() { fmt.Fprintln(e.stdout, i18n.T("没有运行记录。")) })
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTASK\tSTATUS\tEXIT\tTRIGGER\tSTARTED\tNOTE")
	for _, r := range runs {
		exit := "-"
		if r.ExitCode != nil {
			exit = fmt.Sprint(*r.ExitCode)
		}
		started := r.StartedAt
		if started.IsZero() {
			started = r.CreatedAt
		}
		note := r.Note
		if note == "" && (r.Status != task.Failed || len(r.Steps) > 1) { // 单步任务普通的非零退出已经体现在 EXIT 一栏
			note = r.Error
		}
		if r.NoContent {
			if note == "" {
				note = i18n.T("无内容")
			} else {
				note = i18n.T("%s;无内容", note)
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Task, r.Status, exit, r.Trigger, when(started), note)
	}
	return w.Flush()
}

func logsCmd(e *env, args []string) error {
	id, err := one("logs", i18n.T("运行编号"), args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	r, err := c.Run(id)
	if err != nil {
		return err
	}
	logs, err := c.Logs(id)
	if err != nil {
		return err
	}
	if e.json {
		list, err := c.Deliveries(id)
		if err != nil {
			return err
		}
		if list == nil {
			list = []task.Delivery{}
		}
		return e.show(struct {
			Run        task.Run        `json:"run"`
			Logs       api.Logs        `json:"logs"`
			Deliveries []task.Delivery `json:"deliveries"`
		}{r, logs, list}, nil)
	}
	// 运行的标准输出和标准错误分别写到本命令的标准输出和标准错误,便于接管道。
	stdout, stderr := logs.Stdout, logs.Stderr
	if !r.Status.Finished() {
		// 多步骤的运行还没有整次的结果,给出最后一个已经有输出的步骤,通常就是正在执行的那一步(G6)。
		for i := len(logs.Steps) - 1; i >= 0 && stdout == "" && stderr == ""; i-- {
			stdout, stderr = logs.Steps[i].Stdout, logs.Steps[i].Stderr
		}
	}
	fmt.Fprint(e.stdout, stdout)
	fmt.Fprint(e.stderr, stderr)
	// 通知的投递情况(N7)写在最后,也走标准错误,不混进输出。
	if list, err := c.Deliveries(id); err == nil {
		for _, d := range list {
			fmt.Fprint(e.stderr, i18n.T("bugu: 通知 %s\n", describeDelivery(d)))
		}
	}
	switch {
	case !r.Status.Finished():
		fmt.Fprint(e.stderr, i18n.T("bugu: 运行 %s 尚未结束,以上是到目前为止的输出\n", id))
	case logs.Stdout == "" && logs.Stderr == "":
		fmt.Fprint(e.stderr, i18n.T("bugu: 运行 %s(%s)没有输出\n", id, r.Status))
	}
	return nil
}

func runnerCmd(e *env, args []string) error {
	rest, err := parse(flag.NewFlagSet("runner", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if len(rest) == 0 || (rest[0] != "list" && rest[0] != "check" && rest[0] != "add") || (rest[0] != "check" && len(rest) > 2) || (rest[0] == "list" && len(rest) > 1) {
		return bad(i18n.Errorf("用法:bugu runner list,bugu runner check [运行器]...,或 bugu runner add <文件>"))
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	if rest[0] == "add" {
		if len(rest) != 2 {
			return bad(i18n.Errorf("用法:bugu runner add <文件>,- 表示标准输入"))
		}
		return runnerAdd(e, c, rest[1])
	}
	if rest[0] == "list" {
		runners, err := c.Runners()
		if err != nil {
			return err
		}
		if e.json {
			return e.show(runners, nil)
		}
		w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tTITLE\tSOURCE\tPERMISSIONS")
		for _, r := range runners {
			source := "custom"
			if r.Preset {
				source = "preset"
			}
			levels := strings.Join(r.Permissions, ",")
			switch {
			case !r.Agent:
				levels = i18n.T("(不能用于 AI 命令行步骤)")
			case levels == "":
				levels = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Name, r.Title, source, levels)
		}
		return w.Flush()
	}

	names := rest[1:]
	if len(names) == 0 {
		runners, err := c.Runners()
		if err != nil {
			return err
		}
		for _, r := range runners {
			names = append(names, r.Name)
		}
	}
	failed, checks := 0, []api.RunnerCheck{}
	for _, name := range names {
		check, err := c.CheckRunner(name)
		if err != nil {
			return err
		}
		checks = append(checks, check)
		mark := i18n.T("可用")
		if !check.OK {
			mark = i18n.T("不可用")
			failed++
		}
		if !e.json {
			fmt.Fprintf(e.stdout, "%s: %s  %s\n", name, mark, check.Detail)
		}
	}
	if e.json {
		if err := e.show(checks, nil); err != nil {
			return err
		}
	}
	if failed > 0 {
		return i18n.Errorf("%d 个运行器不可用", failed)
	}
	return nil
}

func inboxCmd(e *env, args []string) error {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	all := fs.Bool("all", false, "")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	if len(rest) > 0 && rest[0] == "read" {
		switch {
		case *all && len(rest) == 1:
			n, err := c.MarkAllRead()
			if err != nil {
				return err
			}
			return e.show(map[string]int{"marked": n}, func() { fmt.Fprint(e.stdout, i18n.T("已把 %d 条结果标为已读\n", n)) })
		case *all || len(rest) == 1:
			return bad(i18n.Errorf("用法:bugu inbox read <运行编号>...,或 bugu inbox read --all"))
		}
		for _, id := range rest[1:] {
			if _, err := c.MarkRead(id); err != nil {
				return err
			}
		}
		return e.show(map[string]int{"marked": len(rest) - 1}, func() { fmt.Fprint(e.stdout, i18n.T("已标为已读:%s\n", strings.Join(rest[1:], " "))) })
	}
	if len(rest) > 0 {
		return bad(i18n.Errorf("用法:bugu inbox [--all],或 bugu inbox read <运行编号>... | --all"))
	}
	in, err := c.Inbox(*all)
	if err != nil {
		return err
	}
	if in.Runs == nil {
		in.Runs = []task.Run{}
	}
	if e.json || len(in.Runs) == 0 {
		return e.show(in, func() { fmt.Fprintln(e.stdout, i18n.T("收件箱里没有未读的结果。")) })
	}
	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\tID\tTASK\tSTATUS\tFINISHED\tSUMMARY")
	for _, r := range in.Runs {
		mark := ""
		if r.Unread() {
			mark = "●"
		}
		summary := []rune(r.Summary)
		if len(summary) > 60 {
			summary = append(summary[:60], '…')
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", mark, r.ID, r.Task, r.Status, when(r.FinishedAt), string(summary))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprint(e.stdout, i18n.T("\n%d 条未读。用 bugu logs <运行编号> 看完整输出,bugu inbox read <运行编号> 标为已读\n", in.Unread))
	return nil
}

// describeDelivery 用一行说明一条通知的投递情况。
func describeDelivery(d task.Delivery) string {
	target := i18n.T("系统通知")
	if d.Channel == task.ChannelWebhook {
		target = "webhook " + d.Target
	}
	switch d.Status {
	case task.DeliveryDelivered:
		return i18n.T("%s 已送达", target)
	case task.DeliveryPending:
		return i18n.T("%s 等待重试(已尝试 %d 次,下次 %s):%s", target, d.Attempts, when(d.NextAttempt), d.Error)
	case task.DeliveryDropped:
		return i18n.T("%s 未发出:%s", target, d.Error)
	}
	return i18n.T("%s 失败(尝试 %d 次):%s", target, d.Attempts, d.Error)
}

// languageCmd 查看或设置后台和界面的语言(决策记录 0014)。命令行自己说的语言另由 --lang 和环境变量决定。
func languageCmd(e *env, args []string) error {
	rest, err := parse(flag.NewFlagSet("language", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return bad(i18n.Errorf("用法:bugu language [auto|zh|en]"))
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	var s api.Settings
	if len(rest) == 0 {
		s, err = c.Settings()
	} else {
		s, err = c.UpdateSettings(api.Settings{Language: rest[0]})
	}
	if err != nil {
		return err
	}
	shown := i18n.Lang(s.Effective).Name()
	return e.show(s, func() {
		if s.Language == api.LanguageAuto {
			fmt.Fprintln(e.stdout, i18n.T("界面语言:跟随系统(现在是 %s)", shown))
		} else {
			fmt.Fprintln(e.stdout, i18n.T("界面语言:%s", shown))
		}
	})
}
