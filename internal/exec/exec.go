package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bugu/internal/clock"
	"bugu/internal/task"
)

// outputGrace 是父进程被回收后，等待输出管道关闭的最长时间。
const outputGrace = 2 * time.Second

// Executor 执行一个脚本步骤。Clock 必须由调用者注入。
// macOS/Linux 通过进程组清理普通后代；主动脱离进程组的程序不受此保证。
// 这类程序若仍持有输出管道，执行器在父进程回收后最多再等 outputGrace 就停止采集：
// 运行状态和退出码仍是父进程的真实结果，此后写入的输出不再采集。
type Executor struct{ Clock clock.Clock }

// Run 验证后启动进程，并等待输出采集完毕。非零退出、取消及超时均返回错误。
// 当前仅支持一个脚本步骤，触发及通知由调用层处理。
func (e Executor) Run(ctx context.Context, t task.Task) (r task.Run, err error) {
	r.Status = task.Failed
	if e.Clock == nil {
		return r, errors.New("exec: clock is required")
	}
	if len(t.Steps) != 1 {
		return r, errors.New("exec: exactly one script step is supported")
	}
	s := t.Steps[0]
	if s.Type != task.StepScript {
		return r, fmt.Errorf("exec: unsupported step type %q", s.Type)
	}
	if (s.Script.Path == "") == (s.Script.Command == "") {
		return r, errors.New("exec: specify exactly one script path or command")
	}
	if t.Timeout < 0 {
		return r, errors.New("exec: timeout must not be negative")
	}
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
	path := s.Script.Path
	if path != "" {
		path, err = expandHome(path)
		if err != nil {
			return r, err
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return r, err
		}
	}
	for k, v := range t.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, '\x00') {
			return r, fmt.Errorf("exec: invalid environment variable %q", k)
		}
	}
	argv := []string{path}
	if s.Script.Command != "" {
		argv = []string{"/bin/sh", "-c", s.Script.Command}
	}
	r.Command = append([]string(nil), argv...)
	cmd := osexec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = environment(cmd.Environ(), t.Env)
	// nil Stdin 由 os/exec 接到空设备，子进程立即读到 EOF。
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
	if t.Timeout > 0 {
		timer := e.Clock.NewTimer(t.Timeout)
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
	var out, errOut bytes.Buffer
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
