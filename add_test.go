package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseNumstat(t *testing.T) {
	out := strings.Join([]string{
		"12\t3\tmain.go",
		"5\t0\tdir/{old => new}/file.go",
		"1\t1\told.txt => sub/renamed.txt",
		"-\t-\tassets/logo.png",
	}, "\n")

	got := parseNumstat(out)
	want := map[string]numstat{
		"main.go":         {insertions: 12, deletions: 3},
		"dir/new/file.go": {insertions: 5, deletions: 0},
		"sub/renamed.txt": {insertions: 1, deletions: 1},
		"assets/logo.png": {binary: true},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseNumstat() = %+v, want %+v", got, want)
	}
}

func TestFormatSeconds(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{1500 * time.Millisecond, "1.5"},
		{50 * time.Millisecond, "0.05"},
		{0, "0"},
	}

	for _, tc := range cases {
		if got := formatSeconds(tc.d); got != tc.want {
			t.Errorf("formatSeconds(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestRunAdd_NoUnstagedChanges(t *testing.T) {
	initTempRepo(t)

	var code int
	out := captureOutput(t, func() {
		code = runAdd([]string{"--no-tip"})
	})

	if code != 0 {
		t.Fatalf("runAdd() = %d, want 0", code)
	}
	if !strings.Contains(out, "No unstaged changes found.") {
		t.Fatalf("expected no unstaged changes message, got:\n%s", out)
	}
}

func TestRunAdd_StagesSelectedIDsWithExclusions(t *testing.T) {
	dir := initTempRepo(t)

	writeRepoFile(t, dir, "a.txt", "one\n")
	writeRepoFile(t, dir, "b.txt", "two\n")
	writeRepoFile(t, dir, "c.txt", "three\n")

	// Sorted alphabetically: a.txt (1), b.txt (2), c.txt (3)
	setMockStdin(t, "1-3 ^2\n")

	var code int
	out := captureOutput(t, func() {
		code = runAdd([]string{"--no-tip"})
	})

	if code != 0 {
		t.Fatalf("runAdd() = %d, want 0\nOutput:\n%s", code, out)
	}

	raw, ok := git(dir, "status", "--porcelain=v1", "-z")
	if !ok {
		t.Fatal("git status failed")
	}
	got := parseStatus(raw)
	want := []entry{
		{code: "A ", path: "a.txt"},
		{code: "A ", path: "c.txt"},
		{code: "??", path: "b.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status entries = %+v, want %+v", got, want)
	}
}

func TestRunAdd_StageAllWithA(t *testing.T) {
	dir := initTempRepo(t)

	writeRepoFile(t, dir, "README.md", "# Changed\n")
	writeRepoFile(t, dir, "newfile.txt", "content\n")
	setMockStdin(t, "a\n")

	var code int
	captureOutput(t, func() {
		code = runAdd([]string{"--no-tip"})
	})

	if code != 0 {
		t.Fatalf("runAdd() = %d, want 0", code)
	}

	raw, _ := git(dir, "status", "--porcelain=v1", "-z")
	for _, e := range parseStatus(raw) {
		if e.code[1] != ' ' {
			t.Fatalf("expected all changes staged, found unstaged entry %+v", e)
		}
	}
}

func TestRunAdd_QuitLeavesUnstaged(t *testing.T) {
	dir := initTempRepo(t)

	writeRepoFile(t, dir, "note.txt", "hello\n")
	setMockStdin(t, "q\n")

	var code int
	captureOutput(t, func() {
		code = runAdd([]string{"--no-tip"})
	})

	if code != 0 {
		t.Fatalf("runAdd() = %d, want 0", code)
	}

	raw, _ := git(dir, "status", "--porcelain=v1", "-z")
	entries := parseStatus(raw)
	if len(entries) != 1 || entries[0].code != "??" {
		t.Fatalf("expected note.txt to remain untracked, got %+v", entries)
	}
}

func TestRunAdd_StashSwitch(t *testing.T) {
	dir := initTempRepo(t)

	writeRepoFile(t, dir, "note.txt", "stash me\n")
	setMockStdin(t, "s\n")

	var code int
	captureOutput(t, func() {
		code = runAdd([]string{"--no-tip"})
	})

	if code != 0 {
		t.Fatalf("runAdd() = %d, want 0", code)
	}

	raw, _ := git(dir, "status", "--porcelain=v1", "-z")
	if len(parseStatus(raw)) != 0 {
		t.Fatalf("expected working tree to be clean after stash, got %+v", parseStatus(raw))
	}
}
