//go:build !windows

package proc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func info(pid int) (ppid int, name string, err error) {
	out, err := exec.Command("ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, "", err
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return 0, "", fmt.Errorf("ps: unexpected output for pid %d", pid)
	}
	ppid, err = strconv.Atoi(f[0])
	return ppid, f[1], err
}

func ancestors() ([]Proc, error) {
	var out []Proc
	pid := os.Getpid()
	for i := 0; i < 32; i++ {
		ppid, _, err := info(pid)
		if err != nil || ppid <= 1 {
			break
		}
		_, name, err := info(ppid)
		if err != nil {
			break
		}
		out = append(out, Proc{PID: ppid, Name: name})
		pid = ppid
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no ancestors found for pid %d", os.Getpid())
	}
	return out, nil
}
