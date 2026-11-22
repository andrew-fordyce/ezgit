package main

import (
	"strings"
	"testing"
)

func TestOverlapping(t *testing.T) {
	dirty := []entry{
		{code: " M", path: "src/main.go"},
		{code: "??", path: "notes.txt"},
	}
	incoming := []string{"SRC/main.go", "README.md"}

	got := overlapping(dirty, incoming)
	if len(got) != 1 || got[0].path != "src/main.go" {
		t.Fatalf("overlapping() = %+v, want src/main.go", got)
	}
}

func TestSortedUnique(t *testing.T) {
	got := sortedUnique([]string{"origin/main", "feature", "Feature", "origin/main"})
	want := []string{"feature", "origin/main"}
	if len(got) != len(want) {
		t.Fatalf("sortedUnique() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortedUnique() = %#v, want %#v", got, want)
		}
	}
}

func TestIncomingMergeFiles(t *testing.T) {
	dir := initTempRepo(t)

	if _, ok := git(dir, "switch", "-c", "incoming"); !ok {
		t.Fatal("git switch -c incoming failed")
	}
	writeRepoFile(t, dir, "incoming.txt", "incoming\n")
	if _, ok := git(dir, "add", "incoming.txt"); !ok {
		t.Fatal("git add failed")
	}
	if _, ok := git(dir, "commit", "-m", "incoming commit"); !ok {
		t.Fatal("git commit failed")
	}
	if _, ok := git(dir, "switch", "main"); !ok {
		t.Fatal("git switch main failed")
	}

	files, problem := incomingMergeFiles(dir, "incoming")
	if problem != "" {
		t.Fatalf("incomingMergeFiles() problem = %q", problem)
	}
	if len(files) != 1 || files[0] != "incoming.txt" {
		t.Fatalf("incomingMergeFiles() = %#v, want incoming.txt", files)
	}
}

func TestRunMerge_UpToDateLocalRepository(t *testing.T) {
	initTempRepo(t)

	var code int
	out := captureOutput(t, func() {
		code = runMerge([]string{"--no-tip"})
	})

	if code != 0 {
		t.Fatalf("runMerge() = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "Drift check and merge resolution complete") {
		t.Errorf("missing merge completion message in output:\n%s", out)
	}
}
