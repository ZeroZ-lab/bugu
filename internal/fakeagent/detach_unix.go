//go:build darwin || linux

package main

import (
	"os/exec"
	"syscall"
)

// detach 让子进程在新会话中启动,从而离开父进程所在的进程组。
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
