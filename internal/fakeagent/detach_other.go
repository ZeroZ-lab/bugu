//go:build !darwin && !linux

package main

import "os/exec"

func detach(cmd *exec.Cmd) {}
