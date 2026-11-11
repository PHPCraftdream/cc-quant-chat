//go:build windows

package proc

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type entry struct {
	ppid uint32
	name string
}

func ancestors() ([]Proc, error) {
	h, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	m := map[uint32]entry{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(h, &e); err == nil; err = windows.Process32Next(h, &e) {
		m[e.ProcessID] = entry{e.ParentProcessID, windows.UTF16ToString(e.ExeFile[:])}
	}
	var out []Proc
	pid := uint32(os.Getpid())
	for i := 0; i < 32; i++ { // cap: PID reuse can form a cycle
		cur, ok := m[pid]
		if !ok || cur.ppid == 0 || cur.ppid == pid {
			break
		}
		parent, ok := m[cur.ppid]
		if !ok {
			break
		}
		out = append(out, Proc{PID: int(cur.ppid), Name: parent.name})
		pid = cur.ppid
	}
	return out, nil
}
