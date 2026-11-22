package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunStatus_CleanRepo(t *testing.T) {
	initTempRepo(t)

	var code int
	out := captureOutput(t, func() {
		code = runStatus([]string{"--no-tip"})
	})

	if code != 0 {
		t.Fatalf("runStatus() = %d, want 0", code)
	}
	if !strings.Contains(out, "On branch main") {
		t.Errorf("expected branch name in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Nothing to commit, working tree clean.") {
		t.Errorf("expected clean working tree message, got:\n%s", out)
	}
}

func TestRunStatus_BucketsStagedUnstagedAndUntracked(t *testing.T) {
	dir := initTempRepo(t)

	// Staged file
	writeRepoFile(t, dir, "staged.txt", "staged\n")
	if _, ok := git(dir, "add", "staged.txt"); !ok {
		t.Fatal("git add failed")
	}

	// Unstaged modification to tracked file
	writeRepoFile(t, dir, "README.md", "# Modified\n")

	// Untracked file
	writeRepoFile(t, dir, "untracked.txt", "new\n")

	var code int
	out := captureOutput(t, func() {
		code = runStatus([]string{"--no-tip"})
	})

	if code != 0 {
		t.Fatalf("runStatus() = %d, want 0", code)
	}

	wantSections := []string{
		"Staged changes - in the index, will go into your next commit (1):",
		"Unstaged changes - changed on disk, but not yet added to the index (1):",
		"Untracked changes - new files git isn't tracking yet (1):",
		"staged.txt",
		"README.md",
		"untracked.txt",
	}
	for _, want := range wantSections {
		if !strings.Contains(out, want) {
			t.Errorf("missing expected output %q in:\n%s", want, out)
		}
	}
}

func TestRunStatus_AheadOfUpstream(t *testing.T) {
	dir := initTempRepo(t)

	// Create a bare remote repo on disk and push main with upstream tracking
	remoteDir := filepath.Join(t.TempDir(), "origin.git")
	cmd := exec.Command("git", "init", "--bare", remoteDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare failed: %v\n%s", err, string(out))
	}
	if _, ok := git(dir, "remote", "add", "origin", remoteDir); !ok {
		t.Fatal("git remote add failed")
	}
	if _, ok := git(dir, "push", "-u", "origin", "main"); !ok {
		t.Fatal("git push failed")
	}

	// Make one local commit ahead of origin/main
	writeRepoFile(t, dir, "feature.txt", "ahead\n")
	if _, ok := git(dir, "add", "feature.txt"); !ok {
		t.Fatal("git add failed")
	}
	if _, ok := git(dir, "commit", "-m", "local commit"); !ok {
		t.Fatal("git commit failed")
	}

	var code int
	out := captureOutput(t, func() {
		code = runStatus([]string{"--no-tip"})
	})

	if code != 0 {
		t.Fatalf("runStatus() = %d, want 0", code)
	}
	if !strings.Contains(out, "1 commit(s) ahead (yours, not pushed)") {
		t.Errorf("expected ahead count in status output, got:\n%s", out)
	}
}
