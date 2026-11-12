package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"bugu/internal/clock"
	"bugu/internal/runner"
	"bugu/internal/task"
)

// outputGrace 是父进程被回收后，等待输出管道关闭的最长时间。
const outputGrace = 2 * time.Second

// checkTimeout 是预检和运行器检查里每条辅助命令的时长上限。
const checkTimeout = 30 * time.Second

// Lookup 按名字查找运行器;不存在时返回 runner.ErrNotFound。
type Lookup func(name string) (runner.Runner, error)

// Executor 执行任务的步骤。Clock 必须由调用者注入。
// macOS/Linux 通过进程组清理普通后代；主动脱离进程组的程序不受此保证。
// 这类程序若仍持有输出管道，执行器在父进程回收后最多再等 outputGrace 就停止采集：
// 运行状态和退出码仍是父进程的真实结果，此后写入的输出不再采集。
type Executor struct {
	Clock clock.Clock
	// MaxOutput 是标准输出和标准错误各自保留的字节数上限,0 表示使用 DefaultMaxOutput。
	// 超出时保留开头和结尾,中间用一行说明代替。
	MaxOutput int
	// Runners 查找 AI 命令行步骤用到的运行器;为空时这类步骤无法执行。
	Runners Lookup
	// LoginShell 是用户登录 shell 的路径。不为空时子进程经它启动,
	// 这样 PATH 和登录态与用户在终端里看到的一致(E1);为空时直接启动。
	LoginShell string
}

// Info 是执行一次运行时,任务定义之外的上下文。
type Info struct {
	RunDir  string    // 本次运行的存档目录,提示词保存在其中;为空时不保存
	LastRun time.Time // 上次成功运行的开始时间;零值时取任务的创建时间
}

// DefaultMaxOutput 是每路输出默认保留的字节数。
const DefaultMaxOutput = 1 << 20

// promptFile 是提示词在运行存档目录里的文件名。
const promptFile = "prompt.txt"

// Validate 报告执行器能否执行该任务,不启动任何进程。创建和修改任务时用它提前拒绝。
func Validate(t task.Task, runners Lookup) error {
	if len(t.Steps) != 1 {
		return errors.New("目前只支持恰好一个步骤")
	}
	switch s := t.Steps[0]; s.Type {
	case task.StepScript:
		if (s.Script.Path == "") == (s.Script.Command == "") {
			return errors.New("脚本步骤必须在 path 和 command 之间二选一")
		}
	case task.StepAgent:
		if s.Agent.Runner == "" {
			return errors.New("AI 命令行步骤必须指定运行器")
		}
		if strings.TrimSpace(s.Agent.Prompt) == "" {
			return errors.New("AI 命令行步骤的提示词不能为空")
		}
		rn, err := findRunner(runners, s.Agent.Runner)
		if err != nil {
			return err
		}
		if _, err := rn.Render(s.Agent, s.Agent.Prompt, promptFile); err != nil {
			return err
		}
	default:
		return fmt.Errorf("不支持的步骤类型 %q,目前支持 %s 和 %s", s.Type, task.StepScript, task.StepAgent)
	}
	if t.Timeout < 0 {
		return errors.New("timeout 不能为负")
	}
	for k, v := range t.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, '\x00') {
			return fmt.Errorf("环境变量 %q 不合法:名字不能为空或包含 =,名字和值都不能包含空字符", k)
		}
	}
	return nil
}

func findRunner(runners Lookup, name string) (runner.Runner, error) {
	if runners == nil {
		return runner.Runner{}, errors.New("这个执行器没有配置运行器,不能执行 AI 命令行步骤")
	}
	rn, err := runners(name)
	if errors.Is(err, runner.ErrNotFound) {
		return rn, fmt.Errorf("没有名为 %q 的运行器,可用 bugu runner list 查看现有的运行器", name)
	}
	return rn, err
}

// Run 验证并预检后启动进程，等待输出采集完毕。非零退出、取消及超时均返回错误。
// 当前仅支持一个步骤，触发及通知由调用层处理。
func (e Executor) Run(ctx context.Context, t task.Task, info Info) (r task.Run, err error) {
	r.Status = task.Failed
	if e.Clock == nil {
		return r, errors.New("exec: clock is required")
	}
	if err := Validate(t, e.Runners); err != nil {
		return r, err
	}
	s := t.Steps[0]
	if err := ctx.Err(); err != nil {
		r.Status = task.Canceled
		return r, err
	}
	cwd, err := expandHome(t.Cwd)
	if err != nil {
		return r, err
	}
	if cwd != "" {
		cwd, err = filepath.Abs(cwd)
		if err != nil {
			return r, err
		}
	}
	vars := runner.Vars{TaskName: t.Name, Now: e.Clock.Now(), LastRun: info.LastRun, RunDir: info.RunDir}
	if vars.LastRun.IsZero() {
		vars.LastRun = t.CreatedAt
	}
	if vars.LastRun.IsZero() {
		vars.LastRun = vars.Now
	}

	p := process{dir: cwd, env: t.Env, timeout: time.Duration(t.Timeout)}
	var rn runner.Runner
	var argv []string
	switch s.Type {
	case task.StepScript:
		if s.Script.Command != "" {
			argv = []string{"/bin/sh", "-c", vars.Expand(s.Script.Command)}
			break
		}
		path, err := expandHome(s.Script.Path)
		if err != nil {
			return r, err
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		if path, err = filepath.Abs(path); err != nil {
			return r, err
		}
		argv = []string{path}
	case task.StepAgent:
		if rn, err = findRunner(e.Runners, s.Agent.Runner); err != nil {
			return r, err
		}
		prompt := vars.Expand(s.Agent.Prompt)
		// 提示词存进运行存档,既供运行器读取,也让人事后能看到实际发出的内容。
		dir := info.RunDir
		if dir == "" && rn.PromptVia != runner.ViaArg {
			if dir, err = os.MkdirTemp("", "bugu-prompt-"); err != nil {
				return r, err
			}
			defer os.RemoveAll(dir)
		}
		file := ""
		if dir != "" {
			file = filepath.Join(dir, promptFile)
			if err = os.WriteFile(file, []byte(prompt), 0o600); err != nil {
				return r, fmt.Errorf("保存提示词: %w", err)
			}
		}
		inv, err := rn.Render(s.Agent, prompt, file)
		if err != nil {
			return r, err
		}
		argv, r.Permission = inv.Argv, inv.Level
		if inv.Stdin {
			p.stdin = file
		}
	}
	r.Command = append([]string(nil), argv...)

	// 预检(E6):该有的东西不在,就不启动,并把原因说清楚。
	if cwd != "" {
		if st, statErr := os.Stat(cwd); statErr != nil || !st.IsDir() {
			return r, fmt.Errorf("预检未通过:工作目录 %s 不存在", cwd)
		}
	}
	if s.Type == task.StepAgent {
		if found, findErr := e.resolve(ctx, argv[0], cwd, t.Env); findErr != nil {
			if ctx.Err() != nil {
				r.Status = task.Canceled
			}
			return r, findErr
		} else if !found {
			return r, fmt.Errorf("预检未通过:%s", e.missing(rn.Name, argv[0]))
		}
	}

	var carry map[string]string
	p.argv, carry = e.wrap(argv, t.Env)
	p.env = with(p.env, carry)
	res, err := e.launch(ctx, p)
	r.Status, r.StartedAt, r.FinishedAt, r.ExitCode, r.Stdout, r.Stderr = res.Status, res.StartedAt, res.FinishedAt, res.ExitCode, res.Stdout, res.Stderr
	if s.Type == task.StepAgent && r.Status == task.Succeeded {
		if err = rn.Succeeded(r.Stdout); err != nil {
			r.Status = task.Failed
		}
	}
	return r, err
}

// Check 检查运行器能否使用(R8):命令是否存在,以及配置里的检查命令能否正常结束。
// detail 在通过时是检查命令输出的第一行(通常是版本号),不通过时是原因。
func (e Executor) Check(ctx context.Context, rn runner.Runner) (ok bool, detail string) {
	if e.Clock == nil {
		return false, "exec: clock is required"
	}
	found, err := e.resolve(ctx, rn.Command[0], "", nil)
	if err != nil {
		return false, err.Error()
	}
	if !found {
		return false, e.missing(rn.Name, rn.Command[0])
	}
	if len(rn.Check) == 0 {
		return true, "命令 " + rn.Command[0] + " 存在"
	}
	check, _ := e.wrap(rn.Check, nil)
	res, err := e.launch(ctx, process{argv: check, timeout: checkTimeout})
	line := firstLine(res.Stdout)
	if line == "" {
		line = firstLine(res.Stderr)
	}
	if res.Status != task.Succeeded {
		reason := "无法启动"
		switch {
		case res.Status == task.TimedOut:
			reason = "没有在限定时间内结束"
		case res.ExitCode != nil:
			reason = fmt.Sprintf("以退出码 %d 结束", *res.ExitCode)
		case err != nil:
			reason = err.Error()
		}
		if line != "" {
			reason += ":" + line
		}
		return false, fmt.Sprintf("检查命令 %s %s", strings.Join(rn.Check, " "), reason)
	}
	return true, line
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func (e Executor) missing(name, command string) string {
	where := "后台进程的 PATH"
	if e.posixShell() {
		where = "登录 shell(" + e.LoginShell + ")的 PATH"
	}
	return fmt.Sprintf("找不到运行器 %s 的命令 %s。请确认它已经安装,并且在%s 里", name, command, where)
}

// posixShell 报告登录 shell 是否是可以用 exec "$@" 转交命令的那一类。
// 其他 shell(语法不同)不经它启动,子进程直接继承后台的环境。
func (e Executor) posixShell() bool {
	switch filepath.Base(e.LoginShell) {
	case "sh", "bash", "zsh", "dash", "ksh":
		return true
	}
	return false
}

// carryPrefix 是任务的环境变量在穿过登录 shell 时临时使用的名字前缀。
const carryPrefix = "BUGU_CARRY_"

var shellName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// wrap 让命令经登录 shell 启动(E1)。shell 读完自己的启动文件后用 exec 换成目标命令,
// 所以进程组、退出码和输出都与直接启动相同。
//
// 任务自己的环境变量要在启动文件之后再设置一次,以免被覆盖。它们的值可能是密钥,
// 不能出现在命令行参数里(同一台机器上的其他用户看得到),所以用带前缀的名字放进进程环境,
// 由脚本在启动文件之后改回原名;脚本里只出现变量名。返回的 carry 要并入子进程的环境。
// 名字不是合法 shell 标识符的变量无法这样恢复,它们只按原名传入。
func (e Executor) wrap(argv []string, env map[string]string) (wrapped []string, carry map[string]string) {
	if !e.posixShell() {
		return argv, nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		if shellName.MatchString(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var script strings.Builder
	carry = map[string]string{}
	for _, k := range keys {
		carry[carryPrefix+k] = env[k]
		fmt.Fprintf(&script, "%s=$%s%s; export %s; unset %s%s; ", k, carryPrefix, k, k, carryPrefix, k)
	}
	script.WriteString(`exec "$@"`)
	return append([]string{e.LoginShell, "-l", "-c", script.String(), "bugu"}, argv...), carry
}

// with 返回 base 加上 extra 的环境变量表,不修改两者。
func with(base, extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return base
	}
	merged := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged
}

// resolve 报告命令 name 在子进程将要使用的环境里能否找到。
func (e Executor) resolve(ctx context.Context, name, cwd string, env map[string]string) (bool, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		if !filepath.IsAbs(name) {
			name = filepath.Join(cwd, name)
		}
		st, err := os.Stat(name)
		return err == nil && !st.IsDir(), nil
	}
	if !e.posixShell() {
		_, err := osexec.LookPath(name)
		return err == nil, nil
	}
	probe, carry := e.wrap([]string{"/bin/sh", "-c", `command -v "$1" >/dev/null 2>&1`, "bugu", name}, env)
	res, err := e.launch(ctx, process{argv: probe, dir: cwd, env: with(env, carry), timeout: checkTimeout})
	switch {
	case res.Status == task.Succeeded:
		return true, nil
	case res.ExitCode != nil:
		return false, nil
	case res.Status == task.TimedOut:
		return false, fmt.Errorf("预检未通过:登录 shell %s 没有在 %s 内完成启动", e.LoginShell, checkTimeout)
	}
	return false, err
}

// process 是要启动的一个进程。
type process struct {
	argv    []string
	dir     string
	env     map[string]string
	stdin   string        // 作为标准输入的文件;为空时标准输入是空设备(E5)
	timeout time.Duration // 0 表示不限
}

// launch 启动进程,等它结束并采集输出。返回值里只填写状态、起止时间、退出码和输出。
func (e Executor) launch(ctx context.Context, p process) (r task.Run, err error) {
	r.Status = task.Failed
	cmd := osexec.Command(p.argv[0], p.argv[1:]...)
	cmd.Dir = p.dir
	cmd.Env = environment(cmd.Environ(), p.env)
	// nil Stdin 由 os/exec 接到空设备，子进程立即读到 EOF。
	if p.stdin != "" {
		in, err := os.Open(p.stdin)
		if err != nil {
			return r, err
		}
		defer in.Close()
		cmd.Stdin = in
	}
	if err = prepareProcess(cmd); err != nil {
		return r, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return r, err
	}
	defer outR.Close()
	defer outW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		return r, err
	}
	defer errR.Close()
	defer errW.Close()
	cmd.Stdout, cmd.Stderr = outW, errW
	r.StartedAt = e.Clock.Now()
	// 先登记定时器，避免进程就绪后测试推进时钟却遗漏超时。
	var timeout <-chan time.Time
	stopTimeout := func() {}
	if p.timeout > 0 {
		timer := e.Clock.NewTimer(p.timeout)
		stopTimeout = func() { timer.Stop() }
		timeout = timer.C()
	}
	defer stopTimeout()
	if err = cmd.Start(); err != nil {
		r.FinishedAt = e.Clock.Now()
		return r, err
	}
	r.Status = task.Running
	outW.Close()
	errW.Close()
	limit := e.MaxOutput
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	out, errOut := capture{limit: limit}, capture{limit: limit}
	readDone := make(chan error, 2)
	go func() { _, err := io.Copy(&out, outR); readDone <- err }()
	go func() { _, err := io.Copy(&errOut, errR); readDone <- err }()
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	select {
	case err = <-wait:
		if err == nil {
			r.Status = task.Succeeded
		} else {
			r.Status = task.Failed
		}
	case <-ctx.Done():
		r.Status, err = task.Canceled, ctx.Err()
		killProcessTree(cmd)
		<-wait
	case <-timeout:
		r.Status, err = task.TimedOut, errors.New("exec: execution timed out")
		killProcessTree(cmd)
		<-wait
	}
	// 状态、退出码和结束时间在父进程被回收时确定，之后的输出采集不再改变它们。
	r.FinishedAt = e.Clock.Now()
	stopTimeout()
	// 父进程正常结束也清理后代，避免持有输出管道的后台进程让采集挂死。
	killProcessTree(cmd)
	grace := e.Clock.NewTimer(outputGrace)
	defer grace.Stop()
	if readErr := collectOutput(grace.C(), readDone, func() { outR.Close(); errR.Close() }); readErr != nil {
		err = errors.Join(err, readErr)
		if r.Status == task.Succeeded {
			r.Status = task.Failed
		}
	}

	r.Stdout, r.Stderr = out.String(), errOut.String()
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		code := cmd.ProcessState.ExitCode()
		r.ExitCode = &code
	}
	return r, err
}

func environment(base []string, extra map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range base {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			values[k] = v
		}
	}
	for k, v := range extra {
		values[k] = v
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, k := range keys {
		result = append(result, k+"="+values[k])
	}
	return result
}

// 只展开当前用户的 ~/，不执行 shell 替换。
func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// collectOutput 等两条输出管道读完。进程组已被清理，正常情况下管道随即到 EOF；
// 宽限期到了仍未读完，说明有脱离进程组的后代持有管道，此时关闭读端结束采集。
func collectOutput(grace <-chan time.Time, done <-chan error, closeReaders func()) (err error) {
	for remaining := 2; remaining > 0; {
		select {
		case readErr := <-done:
			remaining--
			if readErr != nil && !errors.Is(readErr, os.ErrClosed) {
				err = errors.Join(err, readErr)
			}
		case <-grace:
			closeReaders()
			grace = nil
		}
	}
	return err
}

// capture 采集一路输出,超过上限时只保留开头和结尾(O2),内存占用不随输出增长。
type capture struct {
	limit      int
	head, tail []byte
	dropped    int64
}

func (c *capture) Write(p []byte) (int, error) {
	n := len(p)
	if room := c.limit/2 - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head = append(c.head, p[:k]...)
		p = p[k:]
	}
	c.tail = append(c.tail, p...)
	// 攒到一个上限再裁到一半,避免每次写入都搬动整段结尾。
	if len(c.tail) > c.limit {
		cut := len(c.tail) - c.limit/2
		c.dropped += int64(cut)
		c.tail = append(c.tail[:0], c.tail[cut:]...)
	}
	return n, nil
}

func (c *capture) String() string {
	if c.dropped == 0 {
		return string(c.head) + string(c.tail)
	}
	return fmt.Sprintf("%s\n[bugu: 输出过大,此处省略 %d 字节]\n%s", c.head, c.dropped, c.tail)
}
