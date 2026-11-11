package gitbranch

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBranchOfARepository(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/feature/x\n")
	if got := Branch(root); got != "feature/x" {
		t.Fatalf("branch = %q, want feature/x", got)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Branch(sub); got != "feature/x" {
		t.Fatalf("from a subfolder: %q", got)
	}
}

func TestDetachedHeadIsSevenCharacters(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".git", "HEAD"), "0123456789abcdef0123456789abcdef01234567\n")
	if got := Branch(root); got != "0123456" {
		t.Fatalf("detached = %q, want 0123456", got)
	}
}

func TestWorktreeFollowsGitdir(t *testing.T) {
	main := t.TempDir()
	gitDir := filepath.Join(main, ".git", "worktrees", "wt")
	write(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/wt-branch\n")
	wt := t.TempDir()
	write(t, filepath.Join(wt, ".git"), "gitdir: "+gitDir+"\n")
	if got := Branch(wt); got != "wt-branch" {
		t.Fatalf("worktree = %q, want wt-branch", got)
	}
	// A relative gitdir is taken from the folder that holds the .git file.
	rel := t.TempDir()
	write(t, filepath.Join(rel, "meta", "HEAD"), "ref: refs/heads/rel\n")
	write(t, filepath.Join(rel, ".git"), "gitdir: meta\n")
	if got := Branch(rel); got != "rel" {
		t.Fatalf("relative gitdir = %q, want rel", got)
	}
}

func TestNoRepositoryOrUnreadableHead(t *testing.T) {
	if got := Branch(t.TempDir()); got != "" {
		t.Fatalf("no repository: %q", got)
	}
	if got := Branch(""); got != "" {
		t.Fatalf("empty dir: %q", got)
	}
	root := t.TempDir()
	write(t, filepath.Join(root, ".git", "HEAD"), "garbage\n")
	if got := Branch(root); got != "" {
		t.Fatalf("garbage HEAD: %q", got)
	}
}
