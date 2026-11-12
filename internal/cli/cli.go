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

	"bugu/internal/client"
	"bugu/internal/daemon"
	"bugu/internal/gui"
	"bugu/internal/task"
)

const usage = `bugu:桌面优先的定时任务应用

用法:
  bugu app                           打开桌面应用(菜单栏图标加主窗口),同时承载后台
  bugu serve [--listen 地址]         只启动后台,没有界面(默认只监听本机)
  bugu add <任务> --schedule <cron> (--command <命令> | --path <脚本>)
           [--at <时间>] [--cwd <目录>] [--env 名=值]... [--timeout <时长>]
           [--notify on_content|on_failure|always|none]
  bugu add --file <文件>             从 JSON 文件新建任务,- 表示标准输入
  bugu list                          列出任务及下次运行时间
  bugu show <任务>                   查看任务定义(JSON,可用于 add --file)
  bugu edit <任务> [选项]            修改任务,选项同 add,只改给出的部分
  bugu rm <任务>                     删除任务
  bugu run <任务>                    立即运行一次
  bugu pause <任务> / resume <任务>  暂停和恢复
  bugu runs [任务] [-n 条数]         运行历史
  bugu logs <运行编号>               查看某次运行的输出
  bugu version                       显示版本

数据目录默认为 ~/.bugu,可用环境变量 BUGU_HOME 指定。
其余命令尚未实现,见 docs/PRD.md 第 11 章的里程碑。
`

// errUsage 表示命令的用法有误,退出码为 2。
type errUsage struct{ msg string }

func (e errUsage) Error() string { return e.msg }

func usagef(format string, args ...any) error { return errUsage{fmt.Sprintf(format, args...)} }

type env struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	home           string
}

// Main 执行一次命令行调用,返回进程退出码。
func Main(version string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		// 不带参数就是打开桌面应用(PRD 5.1),双击应用包走的就是这里。无界面构建里显示用法。
		if !gui.Available {
			fmt.Fprint(stdout, usage)
			return 0
		}
		args = []string{"app"}
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "bugu", version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	}
	commands := map[string]func(*env, []string) error{
		"app": appCmd, "serve": serveCmd, "add": addCmd, "list": listCmd, "show": showCmd, "edit": editCmd, "rm": rmCmd,
		"run": runCmd, "pause": pauseCmd, "resume": resumeCmd, "runs": runsCmd, "logs": logsCmd,
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "bugu: 未知命令 %q\n\n%s", args[0], usage)
		return 2
	}
	home, err := homeDir()
	if err != nil {
		fmt.Fprintln(stderr, "bugu:", err)
		return 1
	}
	err = cmd(&env{stdin: os.Stdin, stdout: stdout, stderr: stderr, home: home}, args[1:])
	var u errUsage
	switch {
	case err == nil:
		return 0
	case errors.As(err, &u):
		fmt.Fprintf(stderr, "bugu %s: %s\n用 bugu help 查看用法\n", args[0], u.msg)
		return 2
	default:
		fmt.Fprintln(stderr, "bugu:", err)
		return 1
	}
}

func homeDir() (string, error) {
	if dir := os.Getenv("BUGU_HOME"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户主目录,请用 BUGU_HOME 指定数据目录: %w", err)
	}
	return filepath.Join(home, ".bugu"), nil
}

// parse 解析命令的参数。位置参数可以写在选项之前或之后。
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
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
		return "", usagef("需要且只需要一个参数:%s", what)
	}
	return rest[0], nil
}

func appCmd(e *env, args []string) error {
	if rest, err := parse(flag.NewFlagSet("app", flag.ContinueOnError), args); err != nil {
		return err
	} else if len(rest) != 0 {
		return usagef("不接受参数 %q", rest[0])
	}
	return gui.Run(e.home, e.stderr)
}

func serveCmd(e *env, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", daemon.DefaultListen, "")
	if rest, err := parse(fs, args); err != nil {
		return err
	} else if len(rest) != 0 {
		return usagef("不接受参数 %q", rest[0])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, e, *listen)
}

// serve 运行后台直到 ctx 结束。
func serve(ctx context.Context, e *env, listen string) error {
	d, err := daemon.Start(daemon.Options{Home: e.home, Listen: listen, Log: e.stderr})
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stderr, "bugu: 后台已启动,监听 http://%s,数据目录 %s\n", d.Addr(), e.home)
	<-ctx.Done()
	fmt.Fprintln(e.stderr, "bugu: 正在停止,进行中的运行将被终止并记为中断")
	return d.Close()
}

// envFlag 收集可重复的 --env 名=值。
type envFlag map[string]string

func (f envFlag) String() string { return "" }
func (f envFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("应写成 名=值,收到 %q", s)
	}
	f[k] = v
	return nil
}

// taskFlags 是 add 和 edit 共用的选项。
type taskFlags struct {
	fs                                     *flag.FlagSet
	schedule, at, command, path, cwd, file string
	timeout, notify                        string
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
		return usagef("--schedule 和 --at 只能给一个")
	}
	if given["command"] && given["path"] {
		return usagef("--command 和 --path 只能给一个")
	}
	if given["schedule"] {
		t.Trigger = task.Trigger{Cron: f.schedule}
	}
	if given["at"] {
		at, err := parseTime(f.at)
		if err != nil {
			return err
		}
		t.Trigger = task.Trigger{At: at}
	}
	if given["command"] || given["path"] {
		t.Steps = []task.Step{{Type: task.StepScript, Script: task.Script{Command: f.command, Path: f.path}}}
	}
	if given["cwd"] {
		t.Cwd = f.cwd
	}
	if given["timeout"] {
		d, err := time.ParseDuration(f.timeout)
		if err != nil {
			return usagef(`--timeout %q 无法解析,应写成 "30m"、"1h30m" 这样的形式,0 表示不设上限`, f.timeout)
		}
		t.Timeout = task.Duration(d)
	}
	if given["notify"] {
		// 只替换系统通知这一条,其他渠道的设置保持不变。
		kept := slices.DeleteFunc(slices.Clone(t.Notifications), func(n task.Notification) bool { return n.Channel == task.ChannelSystem })
		switch f.notify {
		case "none":
		case task.NotifyOnContent, task.NotifyOnFailure, task.NotifyAlways:
			kept = append(kept, task.Notification{Channel: task.ChannelSystem, When: f.notify})
		default:
			return usagef("--notify %q 不合法,可用的是 on_content(失败或有输出时)、on_failure、always、none", f.notify)
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
	return time.Time{}, usagef(`--at %q 无法解析,应写成 "2026-11-12 02:00"(本机时区)或 2026-11-12T02:00:00+08:00`, s)
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
		return t, fmt.Errorf("%s 不是合法的任务定义: %v", file, err)
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
		return usagef("只能给一个任务名,多出了 %q", rest[1])
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
		return usagef("缺少任务名")
	}
	if err := f.apply(&t); err != nil {
		return err
	}
	if f.file == "" {
		if t.Trigger == (task.Trigger{}) {
			return usagef("缺少触发方式:用 --schedule 给出 cron 表达式,或用 --at 给出一次性时间点")
		}
		if len(t.Steps) == 0 {
			return usagef("缺少要执行的内容:用 --command 给出一条命令,或用 --path 给出脚本文件")
		}
	}
	c, err := client.Local(e.home)
	if err != nil {
		return err
	}
	info, err := c.CreateTask(t)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "已创建任务 %s,下次运行:%s\n", info.Name, when(info.NextRun))
	return nil
}

func editCmd(e *env, args []string) error {
	f := newTaskFlags("edit")
	rest, err := parse(f.fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("需要且只需要一个参数:任务名")
	}
	if len(f.given()) == 0 {
		return usagef("没有给出要修改的内容")
	}
	c, err := client.Local(e.home)
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
			return usagef("文件里的任务名是 %q,与要修改的 %q 不一致;不支持改名", t.Name, rest[0])
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
	fmt.Fprintf(e.stdout, "已修改任务 %s,下次运行:%s\n", info.Name, when(info.NextRun))
	return nil
}

func when(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func trigger(tr task.Trigger) string {
	if tr.Cron != "" {
		return tr.Cron
	}
	return "一次 " + when(tr.At)
}

func listCmd(e *env, args []string) error {
	if rest, err := parse(flag.NewFlagSet("list", flag.ContinueOnError), args); err != nil {
		return err
	} else if len(rest) != 0 {
		return usagef("不接受参数 %q", rest[0])
	}
	c, err := client.Local(e.home)
	if err != nil {
		return err
	}
	tasks, err := c.Tasks()
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		fmt.Fprintln(e.stdout, "还没有任务。用 bugu add 新建一个。")
		return nil
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
	name, err := one("show", "任务名", args)
	if err != nil {
		return err
	}
	c, err := client.Local(e.home)
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
	name, err := one("rm", "任务名", args)
	if err != nil {
		return err
	}
	c, err := client.Local(e.home)
	if err != nil {
		return err
	}
	if err := c.DeleteTask(name); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "已删除任务 %s,运行记录仍然保留\n", name)
	return nil
}

func pauseCmd(e *env, args []string) error {
	name, err := one("pause", "任务名", args)
	if err != nil {
		return err
	}
	c, err := client.Local(e.home)
	if err != nil {
		return err
	}
	if _, err := c.Pause(name); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "已暂停任务 %s\n", name)
	return nil
}

func resumeCmd(e *env, args []string) error {
	name, err := one("resume", "任务名", args)
	if err != nil {
		return err
	}
	c, err := client.Local(e.home)
	if err != nil {
		return err
	}
	info, err := c.Resume(name)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "已恢复任务 %s,下次运行:%s\n", name, when(info.NextRun))
	return nil
}

func runCmd(e *env, args []string) error {
	name, err := one("run", "任务名", args)
	if err != nil {
		return err
	}
	c, err := client.Local(e.home)
	if err != nil {
		return err
	}
	r, err := c.RunNow(name, client.NewKey())
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "已开始运行 %s,运行编号 %s\n用 bugu logs %s 查看输出\n", name, r.ID, r.ID)
	return nil
}

func runsCmd(e *env, args []string) error {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	limit := fs.Int("n", 20, "")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 || *limit < 0 {
		return usagef("用法:bugu runs [任务] [-n 条数]")
	}
	name := ""
	if len(rest) == 1 {
		name = rest[0]
	}
	c, err := client.Local(e.home)
	if err != nil {
		return err
	}
	runs, err := c.Runs(name, *limit)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Fprintln(e.stdout, "没有运行记录。")
		return nil
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
		if note == "" && r.Status != task.Failed { // 普通的非零退出已经体现在 EXIT 一栏
			note = r.Error
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Task, r.Status, exit, r.Trigger, when(started), note)
	}
	return w.Flush()
}

func logsCmd(e *env, args []string) error {
	id, err := one("logs", "运行编号", args)
	if err != nil {
		return err
	}
	c, err := client.Local(e.home)
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
	// 运行的标准输出和标准错误分别写到本命令的标准输出和标准错误,便于接管道。
	fmt.Fprint(e.stdout, logs.Stdout)
	fmt.Fprint(e.stderr, logs.Stderr)
	switch {
	case !r.Status.Finished():
		fmt.Fprintf(e.stderr, "bugu: 运行 %s 尚未结束,输出在结束后才能查看\n", id)
	case logs.Stdout == "" && logs.Stderr == "":
		fmt.Fprintf(e.stderr, "bugu: 运行 %s(%s)没有输出\n", id, r.Status)
	}
	return nil
}
