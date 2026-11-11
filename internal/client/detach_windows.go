//go:build windows

package client

import (
	"os/exec"
	"syscall"
)

const canBreakaway = true

const (
	detachedProcess        = 0x00000008
	createNewProcessGroup  = 0x00000200
	createBreakawayFromJob = 0x01000000
)

// detach: broker must outlive the Claude Code session that started it.
func detach(cmd *exec.Cmd, breakaway bool) {
	flags := uint32(detachedProcess | createNewProcessGroup)
	if breakaway {
		flags |= createBreakawayFromJob
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
}
