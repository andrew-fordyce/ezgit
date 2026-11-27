package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func undoGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestUndoDiffParsesRenameAndOrdinaryRecords(t *testing.T) {
	items, ok := parseUndoDiff("R100\x00old name.txt\x00new name.txt\x00M\x00folder/file.txt\x00")
	if !ok || len(items) != 2 {
		t.Fatalf("items=%#v ok=%v", items, ok)
	}
	if items[0].RelPath != "new name.txt" || items[0].TargetPath != "old name.txt" {
		t.Fatalf("rename=%#v", items[0])
	}
	if items[1].RelPath != "folder/file.txt" || items[1].TargetPath != "" {
		t.Fatalf("ordinary=%#v", items[1])
	}
}

func TestUndoRestoreDeletesPathAbsentFromTarget(t *testing.T) {
	dir := t.TempDir()
	undoGit(t, dir, "init", "-b", "main")
	undoGit(t, dir, "config", "user.name", "Undo Test")
	undoGit(t, dir, "config", "user.email", "undo@example.test")
	if err := os.WriteFile(dir+"\\kept.txt", []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	undoGit(t, dir, "add", ".")
	undoGit(t, dir, "commit", "-m", "base")
	if err := os.WriteFile(dir+"\\kept.txt", []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"\\new.txt", []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, ok := changedUndoItems(dir, "HEAD")
	if !ok || len(items) != 2 {
		t.Fatalf("items=%#v ok=%v", items, ok)
	}
	done, failed := restoreUndoItems(dir, "HEAD", items, nil)
	if done != 2 || len(failed) != 0 {
		t.Fatalf("done=%d failed=%v", done, failed)
	}
	if _, err := os.Stat(dir + "\\new.txt"); !os.IsNotExist(err) {
		t.Fatalf("new.txt still exists: %v", err)
	}
}

func TestUndoDirtyTreeChoices(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "README.md", "dirty\n")
	cases := []struct {
		name, input, wantMode, wantTarget string
		wantProceed                       bool
	}{
		{"revert first", "r\n", "FileMatch", "HEAD", true},
		{"continue", "c\n", "Commit", "main", true},
		{"quit", "q\n", "Commit", "main", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setMockStdin(t, tc.input)
			mode, target, code, proceed := undoDirtyDecision(dir, "Commit", "main")
			if code != 0 || proceed != tc.wantProceed || mode != tc.wantMode || target != tc.wantTarget {
				t.Fatalf("mode=%q target=%q code=%d proceed=%v", mode, target, code, proceed)
			}
		})
	}
}

func TestUndoFileMatchDeletesPathAbsentAtTarget(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "only-now.txt", "temporary\n")
	setMockStdin(t, "1\ny\n")
	result := runUndoFileMatchFlow(dir, "HEAD", "only-now")
	if result.code != 0 {
		t.Fatalf("code=%d", result.code)
	}
	if _, err := os.Stat(dir + "\\only-now.txt"); !os.IsNotExist(err) {
		t.Fatalf("path was not deleted: %v", err)
	}
}

func TestUndoFileMatchModeSwitches(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "README.md", "edited\n")
	for _, tc := range []struct{ input, want string }{{"c\n", "Commit"}, {"r\n", "FileRollback"}} {
		setMockStdin(t, tc.input)
		result := runUndoFileMatchFlow(dir, "HEAD", "")
		if result.code != 0 || result.mode != tc.want {
			t.Fatalf("input=%q result=%+v", tc.input, result)
		}
	}
}

func TestUndoHardResetRequiresPhraseAndCreatesBackup(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "second.txt", "second\n")
	undoGit(t, dir, "add", "second.txt")
	undoGit(t, dir, "commit", "-m", "second")
	before := strings.TrimSpace(undoGit(t, dir, "rev-parse", "HEAD"))

	setMockStdin(t, "h\n2\nnot it\n")
	if code := runUndoCommit(dir, 20); code != 0 {
		t.Fatalf("cancelled hard reset code=%d", code)
	}
	if got := strings.TrimSpace(undoGit(t, dir, "rev-parse", "HEAD")); got != before {
		t.Fatalf("HEAD changed without exact phrase: %s", got)
	}
	if branches := undoGit(t, dir, "branch", "--list", "undo-backup/*"); strings.TrimSpace(branches) != "" {
		t.Fatalf("backup created before confirmation: %q", branches)
	}

	setMockStdin(t, "h\n2\nhard reset\n")
	if code := runUndoCommit(dir, 20); code != 0 {
		t.Fatalf("hard reset code=%d", code)
	}
	if got := strings.TrimSpace(undoGit(t, dir, "log", "-1", "--format=%s")); got != "initial commit" {
		t.Fatalf("HEAD subject=%q", got)
	}
	if branches := undoGit(t, dir, "branch", "--list", "undo-backup/*"); !strings.Contains(branches, "undo-backup/") {
		t.Fatalf("missing backup branch: %q", branches)
	}
}

func TestUndoRevertsMergeWithSelectedMainline(t *testing.T) {
	dir := initTempRepo(t)
	undoGit(t, dir, "switch", "-c", "feature")
	writeRepoFile(t, dir, "feature.txt", "feature\n")
	undoGit(t, dir, "add", "feature.txt")
	undoGit(t, dir, "commit", "-m", "feature work")
	undoGit(t, dir, "switch", "main")
	writeRepoFile(t, dir, "main.txt", "main\n")
	undoGit(t, dir, "add", "main.txt")
	undoGit(t, dir, "commit", "-m", "main work")
	undoGit(t, dir, "merge", "--no-ff", "feature", "-m", "merge feature")

	setMockStdin(t, "r\n1\n1\n")
	if code := runUndoCommit(dir, 20); code != 0 {
		t.Fatalf("merge revert code=%d", code)
	}
	if subject := strings.TrimSpace(undoGit(t, dir, "log", "-1", "--format=%s")); !strings.HasPrefix(subject, "Revert") {
		t.Fatalf("subject=%q", subject)
	}
	if _, err := os.Stat(dir + "\\feature.txt"); !os.IsNotExist(err) {
		t.Fatalf("mainline revert did not remove feature side: %v", err)
	}
}
