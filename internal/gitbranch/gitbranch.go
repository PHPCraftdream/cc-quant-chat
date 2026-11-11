// Package gitbranch reads the current branch of a working copy straight from .git/HEAD, without
// running git.
package gitbranch

import (
	"os"
	"path/filepath"
	"strings"
)

const maxUp = 25 // parent folders searched for .git

// Branch returns the branch checked out in the repository that holds dir: the branch name for
// "ref: refs/heads/x", the first 7 characters of the hash for a detached head, and "" when there
// is no repository or its HEAD can't be read. A worktree's .git is a file naming its git
// directory ("gitdir: <path>"); that directory's HEAD is read.
func Branch(dir string) string {
	if dir == "" {
		return ""
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for i := 0; i < maxUp; i++ {
		if head := headFile(filepath.Join(dir, ".git")); head != "" {
			return parseHead(head)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// headFile: the content of HEAD for a .git entry (folder or worktree file), "" if none.
func headFile(dotGit string) string {
	fi, err := os.Stat(dotGit)
	if err != nil {
		return ""
	}
	gitDir := dotGit
	if !fi.IsDir() {
		raw, err := os.ReadFile(dotGit)
		if err != nil {
			return ""
		}
		line := strings.TrimSpace(string(raw))
		rest, ok := strings.CutPrefix(line, "gitdir:")
		if !ok {
			return ""
		}
		gitDir = strings.TrimSpace(rest)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(filepath.Dir(dotGit), gitDir)
		}
	}
	raw, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func parseHead(head string) string {
	if ref, ok := strings.CutPrefix(head, "ref:"); ok {
		ref = strings.TrimSpace(ref)
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			return name
		}
		return ref[strings.LastIndex(ref, "/")+1:]
	}
	if len(head) >= 7 && isHex(head) {
		return head[:7]
	}
	return ""
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}
