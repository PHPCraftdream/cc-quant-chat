// Package proc finds the Claude Code process shared by the MCP server and the monitor.
package proc

import (
	"errors"
	"path/filepath"
	"strings"
)

type Proc struct {
	PID  int
	Name string
}

// Wrappers between Claude Code and its children; skipped when looking for the session process.
var wrappers = map[string]bool{
	"cmd.exe": true, "bash.exe": true, "sh.exe": true, "powershell.exe": true, "pwsh.exe": true, "conhost.exe": true,
	"bash": true, "sh": true, "zsh": true, "dash": true, "fish": true, "pwsh": true, "env": true,
}

// Ancestors: parent chain of the current process, nearest first.
func Ancestors() ([]Proc, error) { return ancestors() }

// SessionKey: PID of the nearest ancestor that isn't a shell/wrapper, i.e. the Claude Code process.
func SessionKey() (int, error) {
	anc, err := ancestors()
	if err != nil {
		return 0, err
	}
	for _, p := range anc {
		name := strings.TrimPrefix(strings.ToLower(filepath.Base(p.Name)), "-")
		if !wrappers[name] {
			return p.PID, nil
		}
	}
	return 0, errors.New("no session process among ancestors")
}
