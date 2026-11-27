package main

import (
	"strings"
	"testing"
)

func TestNormalizedBranchNameMatchesPowerShellWhitespaceRule(t *testing.T) {
	if got := normalizedBranchName("  feature\tnew\r\nthing  "); got != "feature-new-thing" {
		t.Fatalf("normalizedBranchName() = %q, want %q", got, "feature-new-thing")
	}
}

func TestRunBranchUsesCurrentBranchByDefaultAndNormalizesName(t *testing.T) {
	dir := initTempRepo(t)
	setMockStdin(t, "\n")

	if code := runBranch([]string{"--no-tip", "feature/new work"}); code != 0 {
		t.Fatalf("runBranch() = %d", code)
	}
	out, ok := git(dir, "branch", "--show-current")
	if !ok || strings.TrimSpace(out) != "feature/new-work" {
		t.Fatalf("current branch = %q, want feature/new-work", strings.TrimSpace(out))
	}
}

func TestRunRenameConfirmationIsCaseInsensitiveLikePowerShell(t *testing.T) {
	dir := initTempRepo(t)
	setMockStdin(t, "\nrenamed\nY\n")

	if code := runRename([]string{"--no-tip"}); code != 0 {
		t.Fatalf("runRename() = %d", code)
	}
	out, ok := git(dir, "branch", "--show-current")
	if !ok || strings.TrimSpace(out) != "renamed" {
		t.Fatalf("branch was not renamed after uppercase confirmation: %q", strings.TrimSpace(out))
	}
}

func TestRunPruneDeletesOnlySelectedMergedBranches(t *testing.T) {
	dir := initTempRepo(t)
	if _, ok := git(dir, "switch", "-c", "merged"); !ok {
		t.Fatal("creating merged branch failed")
	}
	writeRepoFile(t, dir, "merged.txt", "merged\n")
	if _, ok := git(dir, "add", "merged.txt"); !ok {
		t.Fatal("staging merged branch failed")
	}
	if _, ok := git(dir, "commit", "-m", "merged change"); !ok {
		t.Fatal("committing merged branch failed")
	}
	if _, ok := git(dir, "switch", "main"); !ok {
		t.Fatal("switching to main failed")
	}
	if _, ok := git(dir, "merge", "--no-ff", "merged", "-m", "merge branch"); !ok {
		t.Fatal("merging branch failed")
	}

	setMockStdin(t, "a\ny\n")
	if code := runPrune([]string{"--no-tip", "--no-fetch"}); code != 0 {
		t.Fatalf("runPrune() = %d", code)
	}
	if _, ok := git(dir, "show-ref", "--verify", "--quiet", "refs/heads/merged"); ok {
		t.Fatal("merged branch still exists after confirmed prune")
	}
}

func TestRunBranchDisclosesDisplayedAndTotalCountsWhenCapped(t *testing.T) {
	dir := initTempRepo(t)
	for _, name := range []string{"one", "two", "three"} {
		if _, ok := git(dir, "branch", name); !ok {
			t.Fatalf("creating branch %q failed", name)
		}
	}
	setMockStdin(t, "q\n")
	var code int
	out := captureOutput(t, func() {
		code = runBranch([]string{"--no-tip", "--max-branches", "1"})
	})
	if code != 0 {
		t.Fatalf("runBranch() = %d; output:\n%s", code, out)
	}
	if !strings.Contains(out, "branches of 4 total") || !strings.Contains(out, "Showing 1") {
		t.Fatalf("branch cap disclosure missing displayed/total counts:\n%s", out)
	}
}

func TestRunBranchWarnsStashRemainsWhenSwitchFails(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "uncommitted.txt", "keep me\n")
	setMockStdin(t, "s\n\n")
	var code int
	out := captureOutput(t, func() {
		code = runBranch([]string{"--no-tip", "main"})
	})
	if code != 1 {
		t.Fatalf("runBranch() = %d, want 1; output:\n%s", code, out)
	}
	if !strings.Contains(out, "Your changes remain in the stash") || !strings.Contains(out, "git stash pop") {
		t.Fatalf("missing recovery warning after failed switch:\n%s", out)
	}
	stashes, ok := listStashes(dir)
	if !ok || len(stashes) != 1 {
		t.Fatalf("auto-stash should remain after failed switch: %#v, ok=%v", stashes, ok)
	}
}
