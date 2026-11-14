package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"encoding/json"

	"bugu/internal/clock"
	"bugu/internal/i18n"
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
	// RunDir 是本次运行的存档目录。每个步骤的输出和提示词保存在其中的 steps/<序号>/ 下;
	// 为空时使用临时目录,运行结束后删除。
	RunDir   string
	StateDir string    // 本任务的状态目录,由调用者事先建好
	LastRun  time.Time // 上次成功运行的开始时间;零值时取任务的创建时间
	// Progress 在每一步开始之前收到各步骤此刻的状态:之前的已有结果,这一步运行中,之后的排队中(T9)。
	// 可以为空。
	Progress func(steps []task.StepRun)
	// Live 不为空时,运行过程中可以从它读到正在执行的那一步已有的输出(G6)。
	Live *Live
	// Offset 是这次定时触发的随机偏移(S12)。模板变量里的时间去掉它,按时的运行就是原定时间点。
	Offset time.Duration
}

// DefaultMaxOutput 是每路输出默认保留的字节数。
const DefaultMaxOutput = 1 << 20

// 每个步骤在存档目录里的文件。
const (
	promptFile = "prompt.txt" // AI 命令行步骤实际发出的提示词
	stdoutFile = "stdout.log"
	stderrFile = "stderr.log"
)

// StepDir 返回第 i 个步骤(从 0 起)在运行存档目录里的子目录。
func StepDir(runDir string, i int) string {
	return filepath.Join(runDir, "steps", strconv.Itoa(i+1))
}

// StepOutput 读取第 i 个步骤保存下来的标准输出和标准错误;没有执行到的步骤返回空内容。
func StepOutput(runDir string, i int) (stdout, stderr string, err error) {
	read := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(StepDir(runDir, i), name))
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return string(b), err
	}
	if stdout, err = read(stdoutFile); err != nil {
		return "", "", err
	}
	stderr, err = read(stderrFile)
	return stdout, stderr, err
}

// Validate 报告执行器能否执行该任务,不启动任何进程。创建和修改任务时用它提前拒绝。
func Validate(t task.Task, runners Lookup) error {
	if len(t.Steps) == 0 {
		return i18n.Errorf("任务至少要有一个步骤")
	}
	names := map[string]bool{}
	for i, s := range t.Steps {
		if err := validateStep(s, i, names, runners); err != nil {
			return fmt.Errorf("%s%w", where(t, i), err)
		}
		if s.Name != "" {
			names[s.Name] = true
		}
	}
	if t.Timeout < 0 {
		return i18n.Errorf("timeout 不能为负")
	}
	if o := t.Output; o != nil {
		switch {
		case strings.TrimSpace(o.Dir) == "":
			return i18n.Errorf("产物设置缺少目录(output.dir)")
		case strings.TrimSpace(o.Name) == "":
			return i18n.Errorf(`产物设置缺少文件名(output.name),可以使用模板变量,如 "{date}.md"`)
		case strings.ContainsRune(o.Name, '/'):
			return i18n.Errorf("产物文件名 %q 不能包含 /;目录写在 output.dir 里", o.Name)
		case o.From != "" && o.From != task.OutputFromStdout && o.From != task.OutputFromTask:
			return i18n.Errorf("产物来源 %q 不合法,可用的是 %s(保存最后一步的输出)和 %s(由任务自己写入 {output_file})", o.From, task.OutputFromStdout, task.OutputFromTask)
		}
	} else {
		for i, s := range t.Steps {
			if strings.Contains(s.Script.Command+s.Agent.Prompt+s.Text, "{output_file}") {
				return i18n.Errorf("%s用到了 {output_file},但任务没有设置产物(output)", where(t, i))
			}
		}
	}
	for k, v := range t.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, '\x00') {
			return i18n.Errorf("环境变量 %q 不合法:名字不能为空或包含 =,名字和值都不能包含空字符", k)
		}
	}
	return nil
}

// where 返回错误说明前面指明步骤的部分。只有一个步骤时不必指明。
func where(t task.Task, i int) string {
	if len(t.Steps) == 1 {
		return ""
	}
	if name := t.Steps[i].Name; name != "" {
		return i18n.T("第 %d 步(%s):", i+1, name)
	}
	return i18n.T("第 %d 步:", i+1)
}

// validateStep 检查一个步骤。earlier 是排在它前面的步骤的名字。
func validateStep(s task.Step, i int, earlier map[string]bool, runners Lookup) error {
	if s.Name != "" && (strings.ContainsAny(s.Name, "{}") || strings.TrimSpace(s.Name) != s.Name) {
		return i18n.Errorf("步骤名 %q 不合法:不能包含花括号,首尾不能有空白", s.Name)
	}
	if earlier[s.Name] {
		return i18n.Errorf("步骤名 %q 重复了;后面的步骤靠名字引用输出,名字不能重复", s.Name)
	}
	if s.Timeout < 0 {
		return i18n.Errorf("步骤的 timeout 不能为负")
	}
	var templated string
	switch s.Type {
	case task.StepScript:
		if (s.Script.Path == "") == (s.Script.Command == "") {
			return i18n.Errorf("脚本步骤必须在 path 和 command 之间二选一")
		}
		templated = s.Script.Command
	case task.StepAgent:
		if s.Agent.Runner == "" {
			return i18n.Errorf("AI 命令行步骤必须指定运行器")
		}
		if strings.TrimSpace(s.Agent.Prompt) == "" {
			return i18n.Errorf("AI 命令行步骤的提示词不能为空")
		}
		rn, err := findRunner(runners, s.Agent.Runner)
		if err != nil {
			return err
		}
		if _, err := rn.Render(s.Agent, s.Agent.Prompt, promptFile); err != nil {
			return err
		}
		templated = s.Agent.Prompt
	case task.StepText:
		if strings.TrimSpace(s.Text) == "" {
			return i18n.Errorf("文本步骤的内容不能为空")
		}
		templated = s.Text
	default:
		return i18n.Errorf("不支持的步骤类型 %q,可用的是 %s、%s、%s", s.Type, task.StepScript, task.StepAgent, task.StepText)
	}
	// 引用只能指向前面的步骤(T5)。
	prev, refs := runner.References(templated)
	if prev && i == 0 {
		return i18n.Errorf("第一个步骤没有上一步,不能使用 {prev}")
	}
	for _, name := range refs {
		if !earlier[name] {
			return i18n.Errorf("{steps.%s.output} 引用的步骤不存在或不在这一步之前;只能引用前面已命名的步骤", name)
		}
	}
	return nil
}

func findRunner(runners Lookup, name string) (runner.Runner, error) {
	if runners == nil {
		return runner.Runner{}, i18n.Errorf("这个执行器没有配置运行器,不能执行 AI 命令行步骤")
	}
	rn, err := runners(name)
	if errors.Is(err, runner.ErrNotFound) {
		return rn, i18n.Errorf("没有名为 %q 的运行器,可用 bugu runner list 查看现有的运行器", name)
	}
	return rn, err
}

// Run 验证后按顺序执行任务的步骤。某一步失败、超时或被取消即停止(T6),
// 某一步声明无事可做也停止,但运行算成功(T7)。触发及通知由调用层处理。
func (e Executor) Run(ctx context.Context, t task.Task, info Info) (r task.Run, err error) {
	r.Status = task.Failed
	if e.Clock == nil {
		return r, errors.New("exec: clock is required")
	}
	if err := Validate(t, e.Runners); err != nil {
		return r, err
	}
	if err := ctx.Err(); err != nil {
		r.Status = task.Canceled
		return r, err
	}
	cwd, err := expandHome(t.Cwd)
	if err != nil {
		return r, err
	}
	// 没有指定工作目录时在任务的状态目录里运行(E2),而不是后台进程碰巧所在的目录:
	// 结果不取决于后台从哪里启动,AI 命令行可写的范围也只是这个任务自己的目录。
	if cwd == "" {
		cwd = info.StateDir
	}
	if cwd != "" {
		cwd, err = filepath.Abs(cwd)
		if err != nil {
			return r, err
		}
	}
	// 环境变量文件(E8):每次运行时读取,任务自己的 env 优先。其中的值在输出里脱敏(E10)。
	hide := func(s string) string { return s }
	if t.EnvFile != "" {
		fromFile, fileErr := readEnvFile(t.EnvFile, cwd)
		if fileErr != nil {
			return r, fileErr
		}
		hide = redactor(fromFile)
		// 所有出口的错误都要脱敏:错误信息可能引用了输出或信封里的内容。
		defer func() {
			if err != nil {
				if s := hide(err.Error()); s != err.Error() {
					err = errors.New(s)
				}
			}
		}()
		merged := make(map[string]string, len(fromFile)+len(t.Env))
		for k, v := range fromFile {
			merged[k] = v
		}
		for k, v := range t.Env {
			merged[k] = v
		}
		t.Env = merged
	}
	runDir := info.RunDir
	if runDir == "" {
		if runDir, err = os.MkdirTemp("", "bugu-run-"); err != nil {
			return r, err
		}
		defer os.RemoveAll(runDir)
	}
	// {date}、{time} 等按任务的时区显示(E7)。
	vars := runner.Vars{TaskName: t.Name, Now: t.In(e.Clock.Now().Add(-info.Offset)), LastRun: t.In(info.LastRun), RunDir: runDir, StateDir: info.StateDir, Steps: map[string]runner.Output{}}
	if info.LastRun.IsZero() {
		vars.LastRun = t.In(t.CreatedAt)
	}
	if vars.LastRun.IsZero() {
		vars.LastRun = vars.Now
	}

	r.Steps = make([]task.StepRun, len(t.Steps))
	for i, s := range t.Steps {
		r.Steps[i] = task.StepRun{Name: s.Name, Type: s.Type, Status: task.Skipped}
	}
	if t.Output != nil {
		if vars.OutputFile, err = outputFile(t, cwd, vars); err != nil {
			return r, err
		}
	}
	envelope := filepath.Join(runDir, EnvelopeFile)
	began := e.Clock.Now()
	var stderr []string
	last := -1
	for i, s := range t.Steps {
		timeout := time.Duration(s.Timeout)
		if t.Timeout > 0 {
			left := time.Duration(t.Timeout) - e.Clock.Now().Sub(began)
			if left <= 0 {
				r.Steps[i].Status, r.Steps[i].Error = task.TimedOut, i18n.T("任务的总时长已经用完,这一步没有启动")
				r.Status, err = task.TimedOut, errors.New(where(t, i)+r.Steps[i].Error)
				break
			}
			if timeout == 0 || left < timeout {
				timeout = left
			}
		}
		if info.Progress != nil {
			now := slices.Clone(r.Steps)
			for j := i; j < len(now); j++ {
				now[j].Status = task.Queued
			}
			now[i].Status = task.Running
			info.Progress(now)
		}
		dir := StepDir(runDir, i)
		if err = os.MkdirAll(dir, 0o700); err != nil {
			break
		}
		info.Live.begin(i, hide, t.EnvFile != "")
		sr, stepErr := e.step(ctx, t, i, step{cwd: cwd, dir: dir, vars: vars, timeout: timeout, live: info.Live})
		sr.Name, sr.Type = s.Name, s.Type
		sr.Stdout, sr.Stderr = hide(sr.Stdout), hide(sr.Stderr)
		if stepErr != nil {
			stepErr = errors.New(hide(stepErr.Error()))
			sr.Error = stepErr.Error()
		}
		r.Steps[i], last = sr, i
		out := filepath.Join(dir, stdoutFile)
		if writeErr := errors.Join(os.WriteFile(out, []byte(sr.Stdout), 0o600), os.WriteFile(filepath.Join(dir, stderrFile), []byte(sr.Stderr), 0o600)); writeErr != nil && stepErr == nil {
			stepErr = i18n.Errorf("保存这一步的输出: %w", writeErr)
			sr.Status = task.Failed
		}
		info.Live.end() // 输出已经在存档里,不再从内存提供
		if sr.Stderr != "" {
			stderr = append(stderr, label(t, i)+sr.Stderr)
		}
		if stepErr != nil {
			r.Status, err = sr.Status, fmt.Errorf("%s%w", where(t, i), stepErr)
			break
		}
		if sr.NothingToDo {
			r.NoContent = true
			break
		}
		// 任一步骤都可以用结果信封声明无事可做(T7)。
		if env, envErr := readEnvelope(envelope); envErr != nil {
			r.Status, err = task.Failed, envErr
			break
		} else if env != nil && env.Status == statusNoContent && i < len(t.Steps)-1 {
			r.NoContent = true
			break
		}
		vars.Prev = runner.Output{Text: sr.Stdout, File: out}
		if s.Name != "" {
			vars.Steps[s.Name] = vars.Prev
		}
	}
	if err == nil {
		r.Status = task.Succeeded
	}
	if last >= 0 {
		first, final := r.Steps[0], r.Steps[last]
		r.StartedAt, r.FinishedAt = first.StartedAt, final.FinishedAt
		r.Command, r.ExitCode, r.Permission, r.Stdout = final.Command, final.ExitCode, final.Permission, final.Stdout
		r.Stderr = strings.Join(stderr, "\n")
	}
	if r.Status != task.Succeeded {
		return r, err
	}
	// 静默条件(O4):成功且结果里有约定的标记,就记为无内容。
	if t.SilentIf != "" && strings.Contains(r.Stdout, t.SilentIf) {
		r.NoContent = true
	}
	if err = applyEnvelope(&r, envelope, runDir); err == nil {
		err = saveOutput(&r, t, vars.OutputFile)
	}
	r.Summary, r.Message = hide(r.Summary), hide(r.Message)
	if err != nil {
		r.Status = task.Failed
	}
	return r, err
}

// readEnvFile 读取环境变量文件:每行一个 名=值,可以有 export 前缀,值可以用单引号或双引号包住;
// 空行和 # 开头的行忽略。相对路径相对于任务的工作目录。
func readEnvFile(path, cwd string) (map[string]string, error) {
	path, err := expandHome(path)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, i18n.Errorf("预检未通过:环境变量文件 %s 读取失败: %v", path, err)
	}
	env := map[string]string{}
	for n, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !shellName.MatchString(k) {
			return nil, i18n.Errorf("环境变量文件 %s 第 %d 行应写成 名=值,名字只含字母、数字和下划线", path, n+1)
		}
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		env[k] = v
	}
	return env, nil
}

// minSecret 是脱敏的最短长度:更短的值(如 1、on)替换掉会把正常输出弄乱,也不像是密钥。
const minSecret = 4

// redactor 返回把这些值替换成"[已隐藏 名字]"的函数(E10)。长的值先替换,以免短值拆坏长值。
func redactor(secrets map[string]string) func(string) string {
	keys := make([]string, 0, len(secrets))
	for k, v := range secrets {
		if len(v) >= minSecret {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return func(s string) string { return s }
	}
	sort.Slice(keys, func(i, j int) bool {
		if a, b := secrets[keys[i]], secrets[keys[j]]; len(a) != len(b) {
			return len(a) > len(b)
		}
		return keys[i] < keys[j]
	})
	pairs := make([]string, 0, 2*len(keys))
	for _, k := range keys {
		pairs = append(pairs, secrets[k], i18n.T("[已隐藏 %s]", k))
	}
	r := strings.NewReplacer(pairs...)
	return r.Replace
}

// EnvelopeFile 是结果信封在运行存档目录里的文件名(O12)。
const EnvelopeFile = "result.json"

// 结果信封里的状态。
const (
	statusSucceeded = "succeeded"
	statusFailed    = "failed"
	statusNoContent = "no_content"
)

// envelope 是任务写在 {run_dir}/result.json 里的结构化结果(O12,决策记录 0012)。每个字段都可以不写。
type envelope struct {
	Status    string   `json:"status"`
	Summary   string   `json:"summary"`
	Artifacts []string `json:"artifacts"`
	Notify    *bool    `json:"notify"`
	Message   string   `json:"message"`
}

// readEnvelope 读取结果信封,没有时返回 nil。
func readEnvelope(path string) (*envelope, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, i18n.Errorf("读取结果信封: %w", err)
	}
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, i18n.Errorf("结果信封 %s 不是合法的 JSON: %v", EnvelopeFile, err)
	}
	switch env.Status {
	case "", statusSucceeded, statusFailed, statusNoContent:
	default:
		return nil, i18n.Errorf("结果信封里的 status %q 不合法,可用的是 %s、%s、%s", env.Status, statusSucceeded, statusFailed, statusNoContent)
	}
	return &env, nil
}

// applyEnvelope 把结果信封的内容并入运行结果。有信封时以它为准:它可以把成功改为失败或无内容,
// 但不能把失败的步骤改成成功。列出的产物必须存在。
func applyEnvelope(r *task.Run, path, runDir string) error {
	env, err := readEnvelope(path)
	if err != nil || env == nil {
		return err
	}
	r.Summary, r.Notify, r.Message = strings.TrimSpace(env.Summary), env.Notify, env.Message
	for _, a := range env.Artifacts {
		a, err := expandHome(a)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(a) {
			a = filepath.Join(runDir, a)
		}
		if st, err := os.Stat(a); err != nil || st.IsDir() {
			return i18n.Errorf("结果信封列出的产物 %s 不存在", a)
		}
		r.Artifacts = append(r.Artifacts, a)
	}
	switch env.Status {
	case statusFailed:
		if r.Summary != "" {
			return i18n.Errorf("结果信封声明本次失败:%s", r.Summary)
		}
		return i18n.Errorf("结果信封声明本次失败")
	case statusNoContent:
		r.NoContent = true
	}
	return nil
}

// outputFile 算出本次的产物文件路径,并预检产物目录(E6):不存在就建,建不了或不可写就不启动。
func outputFile(t task.Task, cwd string, vars runner.Vars) (string, error) {
	dir, err := expandHome(t.Output.Dir)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", i18n.Errorf("预检未通过:无法建立产物目录 %s: %v", dir, err)
	}
	probe, err := os.CreateTemp(dir, ".bugu-probe-*")
	if err != nil {
		return "", i18n.Errorf("预检未通过:产物目录 %s 不可写: %v", dir, err)
	}
	probe.Close()
	os.Remove(probe.Name())
	name := vars.Expand(t.Output.Name)
	if name == "" || strings.ContainsRune(name, '/') || name == "." || name == ".." {
		return "", i18n.Errorf("产物文件名 %q 展开后是 %q,不能作为文件名", t.Output.Name, name)
	}
	return filepath.Join(dir, name), nil
}

// saveOutput 落盘产物(O5、O6):由调度器保存最后一步的输出,或检查任务自己写的文件存在(R6)。
// 无内容的运行不落盘。
func saveOutput(r *task.Run, t task.Task, file string) error {
	if t.Output == nil || r.NoContent {
		return nil
	}
	if t.Output.From == task.OutputFromTask {
		if st, err := os.Stat(file); err != nil || st.IsDir() {
			return i18n.Errorf("任务没有生成产物文件 %s", file)
		}
	} else if err := os.WriteFile(file, []byte(r.Stdout), 0o644); err != nil {
		return i18n.Errorf("保存产物: %w", err)
	}
	r.Artifacts = append(r.Artifacts, file)
	return nil
}

// label 是多步骤任务合并标准错误时每一步前面的标题行。
func label(t task.Task, i int) string {
	if len(t.Steps) == 1 {
		return ""
	}
	return "[bugu: " + strings.TrimSuffix(where(t, i), ":") + "]\n"
}

// step 是执行一个步骤所需的上下文。
type step struct {
	cwd     string
	dir     string // 这一步在存档目录里的子目录
	vars    runner.Vars
	timeout time.Duration
	live    *Live
}

// step 执行任务的第 i 个步骤。上一步的输出作为这一步的输入(T5):
// 脚本从标准输入读到它;AI 命令行步骤的提示词没有引用任何步骤的输出时,把它附在提示词后面。
func (e Executor) step(ctx context.Context, t task.Task, i int, c step) (r task.StepRun, err error) {
	r.Status = task.Failed
	s := t.Steps[i]
	if err := ctx.Err(); err != nil {
		r.Status = task.Canceled
		return r, err
	}
	if s.Type == task.StepText {
		r.StartedAt = e.Clock.Now()
		r.Status, r.Stdout, r.FinishedAt = task.Succeeded, c.vars.Expand(s.Text), r.StartedAt
		return r, nil
	}

	p := process{dir: c.cwd, env: t.Env, timeout: c.timeout, live: c.live, wholeLines: t.EnvFile != ""}
	if i > 0 && c.vars.Prev.File != "" {
		p.stdin = c.vars.Prev.File
	}
	var rn runner.Runner
	var argv []string
	switch s.Type {
	case task.StepScript:
		if s.Script.Command != "" {
			argv = []string{"/bin/sh", "-c", c.vars.ExpandCommand(s.Script.Command)}
			break
		}
		path, err := expandHome(s.Script.Path)
		if err != nil {
			return r, err
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(c.cwd, path)
		}
		if path, err = filepath.Abs(path); err != nil {
			return r, err
		}
		argv = []string{path}
	case task.StepAgent:
		if rn, err = findRunner(e.Runners, s.Agent.Runner); err != nil {
			return r, err
		}
		prompt := c.vars.Expand(s.Agent.Prompt)
		if prev, refs := runner.References(s.Agent.Prompt); i > 0 && !prev && len(refs) == 0 && strings.TrimSpace(c.vars.Prev.Text) != "" {
			prompt += "\n\n" + c.vars.Prev.Text
		}
		// 提示词存进运行存档,既供运行器读取,也让人事后能看到实际发出的内容。
		file := filepath.Join(c.dir, promptFile)
		if err = os.WriteFile(file, []byte(prompt), 0o600); err != nil {
			return r, i18n.Errorf("保存提示词: %w", err)
		}
		inv, err := rn.Render(s.Agent, prompt, file)
		if err != nil {
			return r, err
		}
		argv, r.Permission = inv.Argv, inv.Level
		p.stdin = ""
		if inv.Stdin {
			p.stdin = file
		}
	}
	r.Command = append([]string(nil), argv...)

	// 预检(E6):该有的东西不在,就不启动,并把原因说清楚。
	if c.cwd != "" {
		if st, statErr := os.Stat(c.cwd); statErr != nil || !st.IsDir() {
			return r, i18n.Errorf("预检未通过:工作目录 %s 不存在", c.cwd)
		}
	}
	if s.Type == task.StepAgent {
		if found, findErr := e.resolve(ctx, argv[0], c.cwd, t.Env); findErr != nil {
			if ctx.Err() != nil {
				r.Status = task.Canceled
			}
			return r, findErr
		} else if !found {
			return r, i18n.Errorf("预检未通过:%s", e.missing(rn.Name, argv[0]))
		}
	}

	var carry map[string]string
	p.argv, carry = e.wrap(argv, t.Env)
	p.env = with(p.env, carry)
	res, err := e.launch(ctx, p)
	r.Status, r.StartedAt, r.FinishedAt, r.ExitCode, r.Stdout, r.Stderr = res.Status, res.StartedAt, res.FinishedAt, res.ExitCode, res.Stdout, res.Stderr
	switch {
	case s.Type == task.StepScript && r.Status == task.Failed && r.ExitCode != nil && *r.ExitCode == task.ExitNothingToDo:
		r.Status, r.NothingToDo, err = task.Succeeded, true, nil
	case s.Type == task.StepAgent && r.Status == task.Succeeded:
		if err = rn.Succeeded(r.Stdout); err != nil {
			r.Status = task.Failed
		}
	}
	return r, err
}

// Check 检查运行器能否使用(R8):命令是否存在,以及配置里的检查命令能否正常结束。
// detail 在通过时是检查命令输出的第一行(通常是版本号),不通过时是原因。
// 它是可翻译的文字(i18n.Localize),接口按请求的语言说出来。
func (e Executor) Check(ctx context.Context, rn runner.Runner) (ok bool, detail error) {
	if e.Clock == nil {
		return false, errors.New("exec: clock is required")
	}
	found, err := e.resolve(ctx, rn.Command[0], "", nil)
	if err != nil {
		return false, err
	}
	if !found {
		return false, e.missing(rn.Name, rn.Command[0])
	}
	if len(rn.Check) == 0 {
		return true, i18n.Errorf("命令 %s 存在", rn.Command[0])
	}
	check, _ := e.wrap(rn.Check, nil)
	res, err := e.launch(ctx, process{argv: check, timeout: checkTimeout})
	line := firstLine(res.Stdout)
	if line == "" {
		line = firstLine(res.Stderr)
	}
	if res.Status != task.Succeeded {
		reason := i18n.Errorf("无法启动")
		switch {
		case res.Status == task.TimedOut:
			reason = i18n.Errorf("没有在限定时间内结束")
		case res.ExitCode != nil:
			reason = i18n.Errorf("以退出码 %d 结束", *res.ExitCode)
		case err != nil:
			reason = err
		}
		if line != "" {
			reason = i18n.Errorf("%s:%s", reason, line)
		}
		return false, i18n.Errorf("检查命令 %s %s", strings.Join(rn.Check, " "), reason)
	}
	return true, i18n.Errorf("%s", line)
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func (e Executor) missing(name, command string) error {
	if e.posixShell() {
		return i18n.Errorf("找不到运行器 %s 的命令 %s。请确认它已经安装,并且在登录 shell(%s)的 PATH 里", name, command, e.LoginShell)
	}
	return i18n.Errorf("找不到运行器 %s 的命令 %s。请确认它已经安装,并且在后台进程的 PATH 里", name, command)
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
		return false, i18n.Errorf("预检未通过:登录 shell %s 没有在 %s 内完成启动", e.LoginShell, checkTimeout)
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
	live    *Live         // 可以为空
	// wholeLines 为真时,输出过大省略中间时只在行的边界上截断,任务有要脱敏的值时这样用,见 capture。
	wholeLines bool
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
	out, errOut := capture{limit: limit, whole: p.wholeLines}, capture{limit: limit, whole: p.wholeLines}
	p.live.attach(&out, &errOut)
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
// 运行中可能有别人经 Live 读它,所以加锁。
type capture struct {
	mu         sync.Mutex
	limit      int
	head, tail []byte
	dropped    int64
	// whole 为真时,省略中间时把截断处两边不完整的行也一并省略。脱敏只认得完整的值,
	// 一个密钥被截成两半时两半都认不出来;环境变量文件里的值都在一行之内,只留完整的行就不会有半截(E10)。
	whole bool
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dropped == 0 {
		return string(c.head) + string(c.tail)
	}
	head, tail, dropped := c.head, c.tail, c.dropped
	if c.whole {
		keep := bytes.LastIndexByte(head, '\n') + 1 // 开头保留到最后一个完整的行
		skip := len(tail)                           // 结尾从第一个完整的行开始;没有换行时整段都是半行
		if nl := bytes.IndexByte(tail, '\n'); nl >= 0 {
			skip = nl + 1
		}
		dropped += int64(len(head) - keep + skip)
		head, tail = bytes.TrimSuffix(head[:keep], []byte("\n")), tail[skip:] // 说明行自己带换行
	}
	return i18n.T("%s\n[bugu: 输出过大,此处省略 %d 字节]\n%s", head, dropped, tail)
}
