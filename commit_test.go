package main

import (
	"strings"
	"testing"
)

func TestRunCommit_AmendAndFixupConflict(t *testing.T) {
	var code int
	captureOutput(t, func() {
		code = runCommit([]string{"--amend", "--fixup", "--no-tip"})
	})
	if code != 2 {
		t.Fatalf("runCommit() = %d, want usage exit 2", code)
	}
}

func TestRunCommit_CreatesCommitFromStagedFile(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "new.txt", "hello\n")
	if _, ok := git(dir, "add", "new.txt"); !ok {
		t.Fatal("git add failed")
	}

	setMockStdin(t, "Test commit\n")
	var code int
	out := captureOutput(t, func() {
		code = runCommit([]string{"--no-tip"})
	})

	if code != 0 {
		t.Fatalf("runCommit() = %d, want 0\n%s", code, out)
	}
	subject, ok := git(dir, "log", "-1", "--format=%s")
	if !ok || strings.TrimSpace(subject) != "Test commit" {
		t.Fatalf("latest commit subject = %q, want %q", strings.TrimSpace(subject), "Test commit")
	}
	if !strings.Contains(out, "Commit made locally") {
		t.Errorf("missing commit success message in output:\n%s", out)
	}
}

func TestRunCommit_NoStagedChangesOffersRecentCommits(t *testing.T) {
	initTempRepo(t)
	setMockStdin(t, "q\n")

	var code int
	out := captureOutput(t, func() {
		code = runCommit([]string{"--no-tip"})
	})

	if code != 0 {
		t.Fatalf("runCommit() = %d, want 0", code)
	}
	if !strings.Contains(out, "Nothing is staged") {
		t.Errorf("missing clean staged-state message in output:\n%s", out)
	}
	if !strings.Contains(out, "Quit.") {
		t.Errorf("missing quit message in output:\n%s", out)
	}
}
