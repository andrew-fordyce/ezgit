package main

import (
	"strings"
	"testing"
	"time"
)

func TestDiffNumstatColumnsAndBinary(t *testing.T) {
	got, ok := parseNumstatLine("12\t3\tsrc/a.go")
	if !ok || got.ins != 12 || got.del != 3 || got.binary {
		t.Fatalf("got %#v, ok=%v", got, ok)
	}
	bin, ok := parseNumstatLine("-\t-\tassets/logo.png")
	if !ok || !bin.binary || bin.ins != 0 || bin.del != 0 {
		t.Fatalf("binary %#v, ok=%v", bin, ok)
	}
}

func TestDiffWildcardFilterMatchesFileAndPath(t *testing.T) {
	files := []diffFile{{path: "src/api/server.go", fileName: "server.go", dir: "src/api"}, {path: "docs/readme.md", fileName: "readme.md", dir: "docs"}}
	if got := filterDiffFiles(files, "*.go"); len(got) != 1 || got[0].path != "src/api/server.go" {
		t.Fatalf("wildcard result %#v", got)
	}
	if got := filterDiffFiles(files, "api"); len(got) != 1 || got[0].path != "src/api/server.go" {
		t.Fatalf("path result %#v", got)
	}
}

func TestDiffDefaultIndicesSelectAllLatestFiles(t *testing.T) {
	d := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	got := diffDefaultIndices([]diffFile{{date: d.Add(-time.Hour)}, {date: d}, {date: d}, {date: d.Add(-time.Minute)}})
	if len(got) != 2 || got[0] != "2" || got[1] != "3" {
		t.Fatalf("defaults = %#v", got)
	}
}

func TestDiffUntrackedOnlyOffersToggle(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "new.txt", "new content\n")
	setMockStdin(t, "q\n")
	out := captureOutput(t, func() {
		if code := runDiff([]string{"HEAD"}); code != 0 {
			t.Fatalf("runDiff code = %d", code)
		}
	})
	if !strings.Contains(out, "No tracked changes vs 'HEAD', but 1 untracked file(s) exist.") ||
		!strings.Contains(out, "Input 'u' to include untracked files, 'c' to change the comparison") {
		t.Fatalf("output did not offer untracked toggle: %s", out)
	}
}

func TestDiffUntrackedToggleThenBlankDiffsLatest(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "new.txt", "one\ntwo\n")
	setMockStdin(t, "u\n\n")
	out := captureOutput(t, func() {
		if code := runDiff([]string{"HEAD"}); code != 0 {
			t.Fatalf("runDiff code = %d", code)
		}
	})
	for _, want := range []string{"Including untracked files.", "[blank] = default: 1,", "Executing git diff for 1 file(s)",
		"New untracked file", "two"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestDiffTableShowsLineCountsAndVolume(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "README.md", "# Changed\nextra\nmore\n")
	setMockStdin(t, "q\n")
	out := captureOutput(t, func() {
		if code := runDiff(nil); code != 0 {
			t.Fatalf("runDiff code = %d", code)
		}
	})
	for _, want := range []string{"Uncommitted changes found in your working tree", "by path:", "Volume",
		"File Name", "README.md", "Modified", "█"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
	var row string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "1 ") {
			row = line
		}
	}
	if f := strings.Fields(row); len(f) < 7 || f[5] != "3" || f[6] != "1" {
		t.Fatalf("file row should show +3 -1, got %q", row)
	}
}

func TestDiffVolumeBarSplitsAndMarksBinary(t *testing.T) {
	if ins, del, bin := diffVolumeBar(30, 10, 0, 40, 20); ins != 15 || del != 5 || bin != 0 {
		t.Fatalf("bar = %d %d %d", ins, del, bin)
	}
	if ins, del, bin := diffVolumeBar(0, 0, 2, 40, 20); ins != 0 || del != 0 || bin != 2 {
		t.Fatalf("binary bar = %d %d %d", ins, del, bin)
	}
}
