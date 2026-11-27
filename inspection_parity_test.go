package main

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestInspectionParsersPreserveNULSafeRenamePaths(t *testing.T) {
	files := parseNameStatusZ("R100\x00old name\tpart.txt\x00new name\tpart.txt\x00M\x00tab\tpath.txt\x00")
	if len(files) != 2 || files[0].path != "new name\tpart.txt" || files[1].path != "tab\tpath.txt" {
		t.Fatalf("name-status records = %#v", files)
	}
	stats := parseNumstatZ("3\t1\t\x00old name\x00new name\x00-\t-\timage.bin\x00")
	if len(stats) != 2 || stats[0].path != "new name" || stats[0].ins != 3 || !stats[1].binary {
		t.Fatalf("numstat records = %#v", stats)
	}
}

func TestInspectionLogNameStatusMarkers(t *testing.T) {
	got := parseLogNameStatusZ("C=abc\x00R100\x00old.txt\x00new.txt\x00C=def\x00M\x00folder/file.txt\x00", "C=")
	if len(got) != 2 || got[0].marker != "abc" || got[0].path != "new.txt" ||
		got[1].marker != "def" || got[1].path != "folder/file.txt" {
		t.Fatalf("records = %#v", got)
	}
}

func TestInspectionGraphParserKeepsContinuationRows(t *testing.T) {
	rows := parseLogGraphLines("* 1234567::2026-01-02 03:04:05::A:: (HEAD -> main)::subject\n|\\\n", nil)
	if len(rows) != 2 || rows[0].continuation || rows[0].hash != "1234567" || rows[1].prefix != "|\\" {
		t.Fatalf("graph rows = %#v", rows)
	}
}

func TestInspectionGraphParserShortPrefix(t *testing.T) {
	// a line too short to hold graph art plus a hash must not panic and is kept as graph art
	rows := parseLogGraphLines("*::2026-01-02 03:04:05::A:: subject\n|\n\n", nil)
	if len(rows) != 2 || !rows[0].continuation || rows[1].prefix != "|" {
		t.Fatalf("short graph rows = %#v", rows)
	}
	rows = parseLogGraphLines("abcd::2026-01-02 03:04:05::A::::s\n", nil)
	if len(rows) != 1 || rows[0].continuation || rows[0].prefix != "" || rows[0].hash != "abcd" {
		t.Fatalf("prefix-less graph rows = %#v", rows)
	}
}

func TestInspectionLogParsersHandleLogNewlines(t *testing.T) {
	// git log -z puts a newline before each later commit's records
	got := parseLogNameStatusZ("C=2026-01-02 03:04:05\x00\x00M\x00a.txt\x00\nC=2026-01-01 00:00:00\x00\x00A\x00b c.txt\x00", "C=")
	if len(got) != 2 || got[1].marker != "2026-01-01 00:00:00" || got[1].code != "A" || got[1].path != "b c.txt" {
		t.Fatalf("records = %#v", got)
	}
	stats := logParseNumstatZ("1\t2\ta.txt\x00\n3\t0\t\x00old\x00new\x00-\t-\tbin.png\x00")
	if len(stats) != 3 || stats[1].ins != 3 || stats[1].path != "new" || !stats[2].binary {
		t.Fatalf("numstat = %#v", stats)
	}
}

func TestInspectionLogPathMatcherIsPowerShellLike(t *testing.T) {
	for _, tc := range []struct {
		path, filter string
		want         bool
	}{
		{"src/Stash.go", "stash", true},
		{"src/main.go", "src/*.go", true},
		{"lib/src/main.go", "src/*.go", true}, // -like "*filter*" matches anywhere
		{"src/main.txt", "src/*.go", false},
		{"docs/readme.md", "read?e", true},
		{"docs/a1.md", "a[0-9].md", true},
		{"src/main.go", "test", false},
		{"anything", "", true},
	} {
		if got := logPathMatcher(tc.filter)(tc.path); got != tc.want {
			t.Errorf("logPathMatcher(%q)(%q) = %v, want %v", tc.filter, tc.path, got, tc.want)
		}
	}
}

func logTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func logTestCommit(t *testing.T, dir, msg string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		writeRepoFile(t, dir, p, c)
	}
	logTestGit(t, dir, "add", "-A")
	logTestGit(t, dir, "commit", "-q", "-m", msg)
}

func runLogTest(t *testing.T, input string, args ...string) (string, int) {
	t.Helper()
	setMockStdin(t, input)
	code := -1
	out := captureOutput(t, func() { code = runLog(append([]string{"--no-tip"}, args...)) })
	return out, code
}

func TestRunLogFilterAppliesToFileTableNotCommits(t *testing.T) {
	dir := initTempRepo(t)
	logTestCommit(t, dir, "add files", map[string]string{"src/app.go": "package app\n", "docs/guide.md": "guide content\n"})
	logTestCommit(t, dir, "tweak app", map[string]string{"src/app.go": "package app // v2\n"})

	out, code := runLogTest(t, "2\n\n", "-s", "*guide*")
	if code != 0 {
		t.Fatalf("runLog = %d\n%s", code, out)
	}
	for _, want := range []string{"tweak app", "add files", "initial commit", "matching text filter: '*guide*'", "+guide content"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "app.go") {
		t.Fatalf("filtered file table still lists app.go:\n%s", out)
	}
}

func TestRunLogCommitFileTableTextFilterPrompt(t *testing.T) {
	dir := initTempRepo(t)
	logTestCommit(t, dir, "add files", map[string]string{"src/app.go": "package app\n", "docs/guide.md": "guide content\n"})

	// carried filter that matches nothing falls back to all; 't' with no match keeps the list;
	// 't' with a wildcard narrows it; blank picks the first remaining file
	out, code := runLogTest(t, "1\nt\nzzz\nt\n*.go\n\n", "", "nomatch")
	if code != 0 {
		t.Fatalf("runLog = %d\n%s", code, out)
	}
	for _, want := range []string{
		"No files changed in", "match 'nomatch'; showing all.",
		"No changed files match 'zzz'. Keeping the current list.",
		"matching text filter: '*.go'", "+package app",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "+guide content") {
		t.Fatalf("showed the filtered-out file:\n%s", out)
	}
}

func TestRunLogSetFilterAtCommitPrompt(t *testing.T) {
	dir := initTempRepo(t)
	logTestCommit(t, dir, "add files", map[string]string{"a.txt": "alpha\n", "b.md": "bravo\n"})
	out, code := runLogTest(t, "s\nb.md\n1\n\n")
	if code != 0 {
		t.Fatalf("runLog = %d\n%s", code, out)
	}
	if !strings.Contains(out, "Text filter set to 'b.md'") || !strings.Contains(out, "+bravo") || strings.Contains(out, "+alpha") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestRunLogGraphWithTargetIsSelectableAndToggles(t *testing.T) {
	dir := initTempRepo(t)
	logTestGit(t, dir, "branch", "develop")
	logTestCommit(t, dir, "feature one", map[string]string{"one.txt": "one\n"})
	logTestCommit(t, dir, "feature two", map[string]string{"two.txt": "two\n"})

	out, code := runLogTest(t, "g\n2\n\n", "develop", "--graph")
	if code != 0 {
		t.Fatalf("runLog = %d\n%s", code, out)
	}
	for _, want := range []string{"Graph history vs develop:", "Map", "History vs develop:", "Files changed in", "+one"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "initial commit") {
		t.Fatalf("range develop..HEAD should not list the shared commit:\n%s", out)
	}

	// a graph row is selectable by ID too
	out, code = runLogTest(t, "1\n\n", "--graph", "develop")
	if code != 0 || !strings.Contains(out, "+two") {
		t.Fatalf("graph selection failed (%d):\n%s", code, out)
	}
}

func TestRunLogMergeComparesFirstParent(t *testing.T) {
	dir := initTempRepo(t)
	logTestGit(t, dir, "switch", "-q", "-c", "feature")
	logTestCommit(t, dir, "feature work", map[string]string{"feature.txt": "feature\n"})
	logTestGit(t, dir, "switch", "-q", "main")
	logTestCommit(t, dir, "main work", map[string]string{"main.txt": "main\n"})
	logTestGit(t, dir, "merge", "-q", "--no-ff", "-m", "merge feature", "feature")

	out, code := runLogTest(t, "1\na\n")
	if code != 0 {
		t.Fatalf("runLog = %d\n%s", code, out)
	}
	if !strings.Contains(out, "is a merge; showing its changes against the first parent") || !strings.Contains(out, "+feature") {
		t.Fatalf("merge diff missing:\n%s", out)
	}
	if strings.Contains(out, "main.txt") {
		t.Fatalf("merge listed a file from the first parent's side:\n%s", out)
	}
}

func TestRunLogFileModeUntrackedAndModeSwitch(t *testing.T) {
	dir := initTempRepo(t)
	writeRepoFile(t, dir, "notes/new file.txt", "hello untracked\n")

	out, code := runLogTest(t, "\n", "--file", "-s", "new")
	if code != 0 {
		t.Fatalf("runLog = %d\n%s", code, out)
	}
	for _, want := range []string{"Untracked", "is untracked (no commit history)", "+hello untracked"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}

	out, code = runLogTest(t, "t\nzzz\nt\nREADME\nc\nq\n", "--file")
	if code != 0 {
		t.Fatalf("runLog = %d\n%s", code, out)
	}
	for _, want := range []string{"No files match 'zzz'. Keeping the current list.", "matching text filter: 'README'", "Switched to Commit mode.", "Recent commits on main:", "Quit."} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunLogFileModeFollowsRenamesAndRootCommit(t *testing.T) {
	dir := initTempRepo(t)
	logTestCommit(t, dir, "add old", map[string]string{"old.txt": "line one\nline two\nline three\nline four\n"})
	logTestGit(t, dir, "mv", "old.txt", "new.txt")
	logTestGit(t, dir, "commit", "-q", "-m", "rename")
	logTestCommit(t, dir, "edit new", map[string]string{"new.txt": "line one\nline two\nline three\nline four\nline five\n"})

	// history of new.txt follows the rename; the oldest commit shows the file under its old name
	out, code := runLogTest(t, "\n3\n", "--file", "-s", "new.txt")
	if code != 0 {
		t.Fatalf("runLog = %d\n%s", code, out)
	}
	for _, want := range []string{"History of new.txt:", "edit new", "rename", "add old", "Changes to old.txt in commit", "+line one"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}

	out, code = runLogTest(t, "\n\n", "--file", "-s", "README")
	if code != 0 || !strings.Contains(out, "is the root commit") || !strings.Contains(out, "+# Hello") {
		t.Fatalf("root commit history failed (%d):\n%s", code, out)
	}
}

func TestRunLogFileModeTargetShowsRangeColumns(t *testing.T) {
	dir := initTempRepo(t)
	logTestGit(t, dir, "branch", "develop")
	logTestCommit(t, dir, "branch work", map[string]string{"src/x.go": "a\nb\n"})
	out, code := runLogTest(t, "q\n", "develop", "--file")
	if code != 0 {
		t.Fatalf("runLog = %d\n%s", code, out)
	}
	for _, want := range []string{"Files changed vs develop:", "Added", "x.go", "src"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "README.md") {
		t.Fatalf("compare-mode file table listed a file outside the range:\n%s", out)
	}
}

func TestRunLogFileModeLargeRepoPromptsForFilter(t *testing.T) {
	dir := initTempRepo(t)
	for i := 0; i < 101; i++ {
		writeRepoFile(t, dir, fmt.Sprintf("many/file%03d.txt", i), "n\n")
	}
	out, code := runLogTest(t, "nomatch\nfile050\n\n", "--file")
	if code != 0 {
		t.Fatalf("runLog = %d\n%s", code, out)
	}
	for _, want := range []string{"102 files found.", "No files match 'nomatch'.", "matching text filter: 'file050'", "Untracked file file050.txt"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "file051.txt") {
		t.Fatalf("filter was not applied:\n%s", out)
	}
}
