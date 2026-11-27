package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanPreviewHonorsIgnoredMode(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "ordinary.tmp", "x")
	writeRepoFile(t, dir, ".gitignore", "ignored.tmp\n")
	writeRepoFile(t, dir, "ignored.tmp", "x")
	normal, ok := cleanPreview(dir, false)
	if !ok || !strings.Contains(strings.Join(normal, "\n"), "ordinary.tmp") || strings.Contains(strings.Join(normal, "\n"), "ignored.tmp") {
		t.Fatalf("normal clean preview = %#v", normal)
	}
	all, ok := cleanPreview(dir, true)
	if !ok || !strings.Contains(strings.Join(all, "\n"), "ignored.tmp") {
		t.Fatalf("ignored clean preview = %#v", all)
	}
}

func TestParseStagedRowsPreservesRenameSource(t *testing.T) {
	raw := "R  new.txt\x00old.txt\x00"
	rows := parseStagedRows(raw, t.TempDir(), "100\t0\told.txt => new.txt\n")
	if len(rows) != 1 || rows[0].path != "new.txt" || rows[0].orig != "old.txt" {
		t.Fatalf("staged rows = %#v", rows)
	}
}

func TestWorkingChangesIncludesUntrackedFiles(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "new.txt", "new\n")
	changes, ok := workingChanges(dir)
	if !ok || len(changes) != 1 || !changes[0].untracked || changes[0].path != "new.txt" {
		t.Fatalf("working changes = %#v, ok=%v", changes, ok)
	}
}

func TestParseStagedRowsExcludesAllConflictCodes(t *testing.T) {
	raw := "AA both-added.txt\x00DD both-deleted.txt\x00UA theirs-added.txt\x00AU ours-added.txt\x00UD theirs-deleted.txt\x00DU ours-deleted.txt\x00UU both-modified.txt\x00A  staged.txt\x00"
	rows := parseStagedRows(raw, t.TempDir(), "")
	if len(rows) != 1 || rows[0].path != "staged.txt" {
		t.Fatalf("parseStagedRows() = %#v, want only staged.txt", rows)
	}
}

func TestRunStashPopConflictExplainsStashMayRemain(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "README.md", "stashed change\n")
	if _, ok := git(dir, "stash", "push", "-m", "conflicting stash"); !ok {
		t.Fatal("creating stash failed")
	}
	writeRepoFile(t, dir, "README.md", "committed change\n")
	if _, ok := git(dir, "add", "README.md"); !ok {
		t.Fatal("staging conflicting change failed")
	}
	if _, ok := git(dir, "commit", "-m", "conflicting commit"); !ok {
		t.Fatal("committing conflicting change failed")
	}

	setMockStdin(t, "\np\nY\n")
	var code int
	out := captureOutput(t, func() { code = runStash([]string{"--no-tip"}) })
	if code != 1 {
		t.Fatalf("runStash() = %d, want 1; output:\n%s", code, out)
	}
	if !strings.Contains(out, "Changes may have been applied with conflicts while the stash remains saved") {
		t.Fatalf("pop conflict explanation missing from output:\n%s", out)
	}
	stashes, ok := listStashes(dir)
	if !ok || len(stashes) != 1 {
		t.Fatalf("stash should remain after conflict: %#v, ok=%v", stashes, ok)
	}
}

func TestRunCleanAcceptsUppercaseConfirmationAndShowsCommands(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "temporary.txt", "remove me\n")
	setMockStdin(t, "Y\n")
	var code int
	out := captureOutput(t, func() { code = runClean([]string{"--no-tip"}) })
	if code != 0 {
		t.Fatalf("runClean() = %d; output:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "temporary.txt")); !os.IsNotExist(err) {
		t.Fatalf("temporary.txt was not cleaned: %v", err)
	}
	for _, want := range []string{
		"Previewing untracked paths: git -C '",
		"Checking cleanup scope again: git -C '",
		"Cleaning untracked paths: git -C '",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunCleanAcceptsUppercaseIgnoredPhrase(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, ".gitignore", "ignored.tmp\n")
	if _, ok := git(dir, "add", ".gitignore"); !ok {
		t.Fatal("staging gitignore failed")
	}
	if _, ok := git(dir, "commit", "-m", "ignore temp file"); !ok {
		t.Fatal("committing gitignore failed")
	}
	writeRepoFile(t, dir, "ignored.tmp", "remove me\n")
	setMockStdin(t, "CLEAN IGNORED\n")
	if code := runClean([]string{"--no-tip", "--include-ignored"}); code != 0 {
		t.Fatalf("runClean() = %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "ignored.tmp")); !os.IsNotExist(err) {
		t.Fatalf("ignored.tmp was not cleaned: %v", err)
	}
}
