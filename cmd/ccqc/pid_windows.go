//go:build windows

package main

import "golang.org/x/sys/windows"

// pidAlive: is the process still running. A finished process whose object is kept alive by
// someone's open handle counts as gone.
func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}
