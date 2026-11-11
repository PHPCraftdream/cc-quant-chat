//go:build !windows

package client

import (
	"os/exec"
	"syscall"
)

const canBreakaway = false

func detach(cmd *exec.Cmd, _ bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
