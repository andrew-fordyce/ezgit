package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNameStatusZ(t *testing.T) {
	got := parseNameStatusZ("M\x00one.txt\x00R100\x00old.txt\x00new.txt\x00")
	if len(got) != 2 || got[0] != (diffFile{code: "M", path: "one.txt"}) ||
		got[1] != (diffFile{code: "R100", path: "new.txt"}) {
		t.Fatalf("parseNameStatusZ() = %#v", got)
	}
}

func TestRunClean_RemovesConfirmedUntrackedFile(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "temporary.txt", "delete me\n")
	setMockStdin(t, "y\n")

	if code := runClean([]string{"--no-tip"}); code != 0 {
		t.Fatalf("runClean() = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "temporary.txt")); !os.IsNotExist(err) {
		t.Fatalf("temporary.txt still exists or stat failed unexpectedly: %v", err)
	}
}

func TestRunStash_PushesWorkingChanges(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "README.md", "# Changed\n")

	if code := runStash([]string{"--no-tip", "--push", "--message", "test stash"}); code != 0 {
		t.Fatalf("runStash() = %d, want 0", code)
	}
	status, _ := git(dir, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("working tree is still dirty: %q", status)
	}
	stashes, ok := listStashes(dir)
	if !ok || len(stashes) != 1 || !strings.Contains(stashes[0].message, "test stash") {
		t.Fatalf("stashes = %#v, ok = %v", stashes, ok)
	}
}

func TestRunBranchAndRename(t *testing.T) {
	dir := initTempRepo(t)
	setMockStdin(t, "1\n")
	if code := runBranch([]string{"--no-tip", "feature/draft"}); code != 0 {
		t.Fatalf("runBranch() = %d, want 0", code)
	}
	current, _ := git(dir, "branch", "--show-current")
	if strings.TrimSpace(current) != "feature/draft" {
		t.Fatalf("current branch = %q", strings.TrimSpace(current))
	}

	setMockStdin(t, "1\nrename\n")
	if code := runRename([]string{"--no-tip", "feature/renamed"}); code != 0 {
		t.Fatalf("runRename() = %d, want 0", code)
	}
	current, _ = git(dir, "branch", "--show-current")
	if strings.TrimSpace(current) != "feature/renamed" {
		t.Fatalf("current branch after rename = %q", strings.TrimSpace(current))
	}
}

func TestRunPrune_DeletesMergedBranch(t *testing.T) {
	dir := initTempRepo(t)
	if _, ok := git(dir, "branch", "finished"); !ok {
		t.Fatal("could not create finished branch")
	}
	setMockStdin(t, "1\ndelete\n")

	if code := runPrune([]string{"--no-tip", "--no-fetch"}); code != 0 {
		t.Fatalf("runPrune() = %d, want 0", code)
	}
	if _, code := gitCode(dir, "show-ref", "--verify", "--quiet", "refs/heads/finished"); code == 0 {
		t.Fatal("finished branch still exists")
	}
}

func TestRunUnstage_UnstagesSelectedFile(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "README.md", "# Changed\n")
	if _, ok := git(dir, "add", "README.md"); !ok {
		t.Fatal("git add failed")
	}
	setMockStdin(t, "1\n")

	if code := runUnstage([]string{"--no-tip"}); code != 0 {
		t.Fatalf("runUnstage() = %d, want 0", code)
	}
	raw, _ := git(dir, "status", "--porcelain=v1", "-z")
	entries := parseStatus(raw)
	if len(entries) != 1 || entries[0].code != " M" {
		t.Fatalf("status after unstage = %#v", entries)
	}
}

func TestRunDiffAndLog(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "README.md", "# Changed\n")
	if _, ok := git(dir, "add", "README.md"); !ok {
		t.Fatal("git add failed")
	}
	diffOut := captureOutput(t, func() {
		if code := runDiff([]string{"--no-tip", "--no-list"}); code != 0 {
			t.Fatalf("runDiff() = %d, want 0", code)
		}
	})
	if !strings.Contains(diffOut, "README.md") || !strings.Contains(diffOut, "# Changed") {
		t.Fatalf("diff output did not contain expected patch:\n%s", diffOut)
	}

	setMockStdin(t, "1\n")
	logOut := captureOutput(t, func() {
		if code := runLog([]string{"--no-tip", "-n", "1"}); code != 0 {
			t.Fatalf("runLog() = %d, want 0", code)
		}
	})
	if !strings.Contains(logOut, "initial commit") {
		t.Fatalf("log output did not contain initial commit:\n%s", logOut)
	}
}

func TestRunUndo_RestoresTrackedFile(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "README.md", "# Changed\n")
	setMockStdin(t, "r\n1\ny\n")

	if code := runUndo([]string{"--no-tip"}); code != 0 {
		t.Fatalf("runUndo() = %d, want 0", code)
	}
	content, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(content), "\r\n", "\n") != "# Hello\n" {
		t.Fatalf("README.md = %q, want original content", string(content))
	}
}
