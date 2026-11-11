//go:build !darwin && !linux

package exec

import (
	"errors"
	"os/exec"
)

func prepareProcess(cmd *exec.Cmd) error { return errors.New("exec: unsupported operating system") }
func killProcessTree(cmd *exec.Cmd)      { _ = cmd.Process.Kill() }
