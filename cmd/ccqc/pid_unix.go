//go:build !windows

package main

import (
	"os"
	"syscall"
)

// pidAlive: does a process with this pid exist (a zombie counts until its parent reaps it).
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}
