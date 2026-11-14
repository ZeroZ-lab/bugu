//go:build darwin || linux

package exec

import (
	"os/exec"
	"syscall"
)

func prepareProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

// 进程组覆盖普通后代；主动 setsid 或更改进程组的程序不在此保证范围内。
func killProcessTree(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
