// log.go - `ezgit log` (gitlog): browse commit history or one file's history. Port of Show-GitLog.ps1.
//
//	ezgit log                      recent commits on this branch: pick one, then the file(s) it changed
//	ezgit log develop --graph      graph of the commits HEAD has that develop doesn't
//	ezgit log origin/main -n 5     the last 5 commits your branch has that origin/main doesn't
//	ezgit log --file -s stash      file mode: files whose path contains "stash", then one file's history
//	ezgit log -s "*stash*"         commit mode: the chosen commit's file table is filtered to "stash"
//
// There are two modes. Commit mode lists commits (optionally as a graph); picking one lists the files
// it changed, and you pick one or more of those to see their diffs. File mode lists tracked and
// untracked files; picking one lists the commits that changed it (following renames), and you pick
// one to see the file's diff at that commit. 'f' and 'c' switch between the modes in a flat loop. The
// text filter never switches modes: it filters whichever file table you are looking at.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	logFormat   = "%h::%ad::%an::%d::%s"
	logDateArg  = "--date=format:%Y-%m-%d %H:%M:%S"
	logDateEcho = "--date=format:'%Y-%m-%d %H:%M:%S'"
	// file mode asks for an optional filter before building a table bigger than this
	logFileFilterThreshold = 100
)

var logNoDate = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)

type logNameRecord struct{ marker, code, path string }

// logGraphRow is one line of `git log --graph` output: a commit with the graph art before it, or a
// continuation line that only carries graph art.
type logGraphRow struct {
	prefix string
	commitInfo
	continuation bool
}

// logFileRow is one row of a file table (a commit's changed files, or file mode's file list).
type logFileRow struct {
	code        string // name-status code from a commit/range (A, M, D, R100...)
	currentCode string // two-letter working-tree status (file mode without a target)
	date        time.Time
	fileName    string
	parent      string
	path        string
	ins, del    int
	binary      bool
	untracked   bool
}

type logSession struct {
	root    string
	remotes []string
	target  string
	filter  string
	max     int
	graph   bool
}

// chooseCommit is also used by undo's commit picker.
func chooseCommit(commits []commitInfo) (commitInfo, bool) {
	if len(commits) == 0 {
		return commitInfo{}, false
	}
	answer, ok := prompt("Input commit ID, [blank] = 1, or 'q' to quit")
	fmt.Println()
	if !ok || strings.EqualFold(strings.TrimSpace(answer), "q") {
		return commitInfo{}, false
	}
	id := 1
	if strings.TrimSpace(answer) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(answer))
		if err != nil {
			return commitInfo{}, false
		}
		id = n
	}
	if id < 1 || id > len(commits) {
		return commitInfo{}, false
	}
	return commits[id-1], true
}

// ---------- parsers ----------

// logSplitZ splits NUL-delimited git output, dropping empty fields.
func logSplitZ(raw string) []string {
	var out []string
	for _, f := range strings.Split(raw, "\x00") {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// parseLogNameStatusZ reads `--name-status -z` output into ordered records, resolving renames and
// copies to their new path. With a markerPrefix (e.g. "C=" from --format=C=...%x00) each record
// carries the most recent marker value. git log puts a newline between commits, so status and
// marker fields are trimmed of leading line breaks; path fields are kept exactly.
func parseLogNameStatusZ(raw, markerPrefix string) []logNameRecord {
	fields := logSplitZ(raw)
	var out []logNameRecord
	marker := ""
	for i := 0; i < len(fields); i++ {
		field := strings.TrimLeft(fields[i], "\r\n")
		if field == "" {
			continue
		}
		if markerPrefix != "" && strings.HasPrefix(field, markerPrefix) {
			marker = field[len(markerPrefix):]
			continue
		}
		code := field
		var p string
		if code[0] == 'R' || code[0] == 'C' {
			if i+2 >= len(fields) {
				break
			}
			p = fields[i+2]
			i += 2
		} else {
			if i+1 >= len(fields) {
				break
			}
			p = fields[i+1]
			i++
		}
		out = append(out, logNameRecord{marker, code, p})
	}
	return out
}

// logParseNumstatZ reads `--numstat -z` output. A rename/copy has an empty path in its first field
// and the old and new paths in the next two; binary files report "-" for both counts.
func logParseNumstatZ(raw string) []diffFile {
	fields := logSplitZ(raw)
	var out []diffFile
	for i := 0; i < len(fields); i++ {
		parts := strings.SplitN(strings.TrimLeft(fields[i], "\r\n"), "\t", 3)
		if len(parts) < 3 {
			continue
		}
		p := parts[2]
		if p == "" {
			if i+2 >= len(fields) {
				break
			}
			p = fields[i+2]
			i += 2
		}
		d := diffFile{path: p}
		if parts[0] == "-" || parts[1] == "-" {
			d.binary = true
		} else {
			d.ins, _ = strconv.Atoi(parts[0])
			d.del, _ = strconv.Atoi(parts[1])
		}
		out = append(out, d)
	}
	return out
}

// parseLogGraphLines reads `git log --graph` output in the shared log format. The graph art before
// the hash becomes the row's prefix; lines with no commit (pure graph art) are continuation rows.
func parseLogGraphLines(raw string, remotes []string) []logGraphRow {
	var out []logGraphRow
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := logLine.FindStringSubmatch(line); m != nil {
			if cs := parseLogLines([]string{m[2] + "::" + m[3]}, remotes); len(cs) == 1 {
				out = append(out, logGraphRow{prefix: m[1], commitInfo: cs[0]})
				continue
			}
		}
		if strings.TrimSpace(line) != "" {
			out = append(out, logGraphRow{prefix: line, continuation: true})
		}
	}
	return out
}

// logPathMatcher returns a PowerShell `-like "*filter*"` matcher (case-insensitive; * ? [] wildcards).
func logPathMatcher(filter string) func(string) bool {
	if strings.TrimSpace(filter) == "" {
		return func(string) bool { return true }
	}
	re := psWildcard("*" + filter + "*")
	return func(p string) bool { return re.MatchString(p) }
}

func logFilterPaths(paths []string, filter string) []string {
	match := logPathMatcher(filter)
	var out []string
	for _, p := range paths {
		if match(p) {
			out = append(out, p)
		}
	}
	return out
}

func logFilterRows(rows []logFileRow, filter string) []logFileRow {
	match := logPathMatcher(filter)
	var out []logFileRow
	for _, r := range rows {
		if match(r.path) {
			out = append(out, r)
		}
	}
	return out
}

// logSplitPath splits a repo-relative (forward-slash) path into file name and parent ("." at the root).
func logSplitPath(p string) (name, parent string) {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:], p[:i]
	}
	return p, "."
}

func logSortRows(rows []logFileRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := strings.ToLower(rows[i].parent), strings.ToLower(rows[j].parent)
		if a != b {
			return a < b
		}
		return strings.ToLower(rows[i].fileName) < strings.ToLower(rows[j].fileName)
	})
}

func logIsDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// logStatusColor colours the Status column from the full decoration, so a deduped 'origin/x'
// still reads as in sync.
func logStatusColor(rawDecoration string) string {
	d := strings.ToLower(rawDecoration)
	switch {
	case strings.Contains(d, "head ->") && strings.Contains(d, "origin/"):
		return "Green"
	case strings.Contains(d, "head ->"):
		return "Cyan"
	case strings.Contains(d, "origin/"):
		return "Blue"
	}
	return "Gray"
}

var logCommitHeaders = []string{"Hash", "Date", "Author", "Message", "Status"}

func logCommitRows(commits []commitInfo) [][]cell {
	rows := make([][]cell, len(commits))
	for i, c := range commits {
		rows[i] = []cell{
			{c.hash, ""},
			{c.date.Format(logDateLayout), "DarkGray"},
			{truncate(c.author, 25), "Gray"},
			{truncate(c.message, 50), ""},
			{c.decoration, logStatusColor(c.rawDecoration)},
		}
	}
	return rows
}

// ---------- rendering ----------

// logShowCommitGraph is a hand-rolled table for the graph view: the table helper's fixed-width
// columns can't preserve git's ASCII topology, so this keeps the graph art in a "Map" column while
// still giving every commit a selectable ID. Continuation rows show only their graph art.
func logShowCommitGraph(rows []logGraphRow) {
	const hashW, dateW, msgLimit, maxAuthLimit = 10, 21, 50, 25
	count, maxMap, maxAuth, maxMsg, maxStat := 0, 0, 0, 0, 0
	for _, r := range rows {
		maxMap = max(maxMap, utf8.RuneCountInString(r.prefix))
		if r.continuation {
			continue
		}
		count++
		maxAuth = max(maxAuth, utf8.RuneCountInString(r.author))
		maxMsg = max(maxMsg, min(msgLimit, utf8.RuneCountInString(r.message)))
		maxStat = max(maxStat, utf8.RuneCountInString(r.decoration))
	}
	idW := max(2, len(strconv.Itoa(count))) + 2
	mapW := max(4, maxMap)
	authW := max(6, min(maxAuthLimit, maxAuth))
	msgW := max(8, maxMsg)
	statW := max(6, maxStat)
	sep := strings.Repeat("-", idW+mapW+hashW+dateW+(authW+2)+(msgW+2)+statW)

	say("DarkGray", sep)
	say("DarkGray", pad("ID", idW)+pad("Map", mapW)+pad("Hash", hashW)+pad("Date", dateW)+
		pad("Author", authW+2)+pad("Message", msgW+2)+"Status")
	say("DarkGray", sep)
	id := 0
	for _, r := range rows {
		if r.continuation {
			if strings.TrimSpace(r.prefix) != "" {
				fmt.Println(strings.Repeat(" ", idW) + pad(r.prefix, mapW))
			}
			continue
		}
		id++
		sayInline("DarkGray", pad(strconv.Itoa(id), idW))
		fmt.Print(pad(r.prefix, mapW))
		fmt.Print(pad(r.hash, hashW))
		sayInline("DarkGray", pad(r.date.Format(logDateLayout), dateW))
		author := []rune(r.author)
		if len(author) > authW {
			sayInline("Gray", string(author[:max(0, authW-2)])+"..  ")
		} else {
			sayInline("Gray", pad(r.author, authW+2))
		}
		msg := []rune(r.message)
		if len(msg) > msgLimit {
			fmt.Print(string(msg[:msgLimit-3]) + "...  ")
		} else {
			fmt.Print(pad(r.message, msgW+2))
		}
		say(logStatusColor(r.rawDecoration), r.decoration)
	}
	say("DarkGray", sep)
}

// logFileTable prints a file table. withStats adds the +/- line-volume columns.
func logFileTable(title string, rows []logFileRow, withStats bool, status func(logFileRow) (string, string)) {
	latest := logNoDate
	for _, r := range rows {
		if r.date.After(latest) {
			latest = r.date
		}
	}
	headers := []string{"Status", "Latest Commit", "File Name"}
	if withStats {
		headers = append(headers, "+", "-")
	}
	headers = append(headers, "Path")
	cells := make([][]cell, len(rows))
	for i, r := range rows {
		label, color := status(r)
		dateText, dateColor := r.date.Format(logDateLayout), "Gray"
		if r.date.Equal(latest) {
			dateColor = "Cyan"
		}
		if r.untracked {
			dateText = "-"
		}
		row := []cell{{label, color}, {dateText, dateColor}, {r.fileName, ""}}
		if withStats {
			ins, del := strconv.Itoa(r.ins), strconv.Itoa(r.del)
			if r.binary {
				ins, del = "-", "-"
			}
			row = append(row, cell{ins, "DarkGray"}, cell{del, "DarkGray"})
		}
		cells[i] = append(row, cell{r.parent, ""})
	}
	table(title, headers, cells, true)
}

// logGitCombined runs git in root and returns stdout and stderr together (like PowerShell's 2>&1).
func logGitCombined(root string, args ...string) (string, int) {
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}
	return string(out), -1
}

func logColorArg() string {
	if useColor {
		return "color.diff=always"
	}
	return "color.diff=false"
}

// logWriteHunks prints only the hunk body (and binary notices) of a diff, skipping the file header.
func logWriteHunks(raw string) {
	seen := false
	for _, line := range strings.Split(strings.TrimRight(raw, "\r\n"), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case seen:
			fmt.Println(line)
		case strings.Contains(line, "@@ "):
			seen = true
			fmt.Println(line)
		case strings.Contains(line, "Binary files "):
			fmt.Println(line)
		}
	}
}

// logShowFileDiffs renders each file's diff at a commit with git show.
func logShowFileDiffs(root, hash string, files []logFileRow, scope []string, scopeText string) bool {
	failed := 0
	fmt.Println()
	fmt.Printf("Executing git show for %d file(s)\n", len(files))
	fmt.Println()
	for i, f := range files {
		ctx := ""
		if len(files) > 1 {
			ctx = fmt.Sprintf(" (file %d of %d)", i+1, len(files))
		}
		viewCmd := fmt.Sprintf("git show %s %s -- \"%s\"", scopeText, hash, f.path)
		label := fmt.Sprintf("Changes to %s in commit %s ", f.fileName, hash)
		bar := strings.Repeat("-", max(40, utf8.RuneCountInString(label)))
		say("DarkGray", fmt.Sprintf("Showing changes to %s in commit %s%s: %s", f.fileName, hash, ctx, viewCmd))
		say("DarkGray", bar)
		fmt.Println(label)
		say("DarkGray", bar)
		args := append([]string{"-c", logColorArg(), "show"}, scope...)
		out, code := logGitCombined(root, append(args, hash, "--", f.path)...)
		logWriteHunks(out)
		if code != 0 {
			failed++
			say("Red", fmt.Sprintf("Failed to show changes for '%s' (git exited with code %d).", f.path, code))
		}
		say("DarkGray", bar)
		if i < len(files)-1 {
			fmt.Println()
		}
	}
	fmt.Println()
	return failed == 0
}

// ---------- command ----------

// logParseArgs parses flags that may appear before, between or after the two positional arguments
// (so `ezgit log develop --graph` works). Everything after "--" is positional.
func logParseArgs(fs *flag.FlagSet, args []string, maxPos int) (pos []string, code int, ok bool) {
	rest := args
	var afterDash []string
	for i, a := range args {
		if a == "--" {
			rest, afterDash = args[:i], args[i+1:]
			break
		}
	}
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, 0, false
			}
			return nil, 2, false
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		rest = rest[1:]
	}
	pos = append(pos, afterDash...)
	if len(pos) > maxPos {
		fmt.Fprintf(os.Stderr, "%s: unexpected argument %q\n", fs.Name(), pos[maxPos])
		return nil, 2, false
	}
	return pos, 0, true
}

func runLog(args []string) int {
	fs, o := flagsFor("log", "[revision-target] [text-filter]")
	fileMode := fs.Bool("file", false, "start in file mode (pick a file, then one of its commits)")
	graph := fs.Bool("graph", false, "show the commit graph in commit mode ('g' toggles it)")
	var maxCommits int
	fs.IntVar(&maxCommits, "n", 20, "maximum commits to show (same as --max-commits)")
	fs.IntVar(&maxCommits, "max-commits", 20, "maximum commits to show")
	var textFilter string
	fs.StringVar(&textFilter, "s", "", "filename filter for the file table; wildcards * ? [] work")
	fs.StringVar(&textFilter, "text-filter", "", "filename filter for the file table (same as -s)")
	pos, code, ok := logParseArgs(fs, args, 2)
	if !ok {
		return code
	}

	root := preflight()
	remotesOut, ok := git(root, "remote")
	if !ok {
		errBlock("Error: could not list configured Git remotes. Aborting.")
		return 1
	}
	if maxCommits < 1 {
		errBlock("Error: MaxCommits must be at least 1. Aborting.")
		return 1
	}
	s := &logSession{root: root, remotes: nonEmptyLines(remotesOut), max: maxCommits, graph: *graph}
	s.filter = strings.TrimSpace(textFilter)
	if len(pos) > 0 {
		s.target = strings.TrimSpace(pos[0])
	}
	if len(pos) > 1 {
		s.filter = strings.TrimSpace(pos[1])
	}

	showTip(*o, "The log shows your commit history: who changed what, when, and why.\n"+
		"This command displays your recent commits in a readable table, or the history of specific files "+
		"without needing to remember git log formatting flags.\n"+
		"Press 'f' at the commit table (or pass --file) for a more detailed view of individual file "+
		"histories; 'c' at the file table switches back.")

	// mode switches loop here rather than nesting calls, so bouncing between modes stays flat
	mode := "Commit"
	if *fileMode {
		mode = "File"
	}
	for {
		var next string
		var rc int
		if mode == "Commit" {
			next, rc = s.commitMode()
		} else {
			next, rc = s.fileMode()
		}
		if next == "" {
			return rc
		}
		mode = next
	}
}

func (s *logSession) rangeHeader() string {
	if s.graph {
		return "Graph history vs " + s.target
	}
	return "History vs " + s.target
}

func (s *logSession) verifyTarget() bool {
	if _, c := gitCode(s.root, "rev-parse", "--verify", "--end-of-options", s.target+"^{commit}"); c != 0 {
		errBlock(fmt.Sprintf("Error: Revision target '%s' does not resolve to a commit. Aborting.", s.target))
		return false
	}
	return true
}

func logQuit() (string, int) {
	fmt.Println()
	fmt.Println("Quit.")
	fmt.Println()
	return "", 0
}

// commitMode lists commits (table or graph) and lets the person pick one. It returns the next mode
// to switch to, or "" with an exit code when the command is done.
func (s *logSession) commitMode() (string, int) {
	var rangeArgs []string
	var header string
	diffCount := 0
	if s.target == "" {
		out, ok := git(s.root, "rev-parse", "--abbrev-ref", "HEAD")
		branch := strings.TrimSpace(out)
		if !ok || branch == "" {
			errBlock("Error: could not resolve the current branch. Aborting.")
			return "", 1
		}
		rangeArgs = []string{"-" + strconv.Itoa(s.max)}
		if branch == "HEAD" {
			branch = "current branch"
		}
		header = "Recent commits on " + branch
	} else {
		if !s.verifyTarget() {
			return "", 1
		}
		say("DarkGray", fmt.Sprintf("Counting commits between %s and current HEAD: git rev-list --count %s..HEAD", s.target, s.target))
		out, ok := git(s.root, "rev-list", "--count", s.target+"..HEAD")
		n, err := strconv.Atoi(strings.TrimSpace(out))
		if !ok || err != nil {
			errBlock(fmt.Sprintf("Error: could not count commits between %s and HEAD. Aborting.", s.target))
			return "", 1
		}
		diffCount = n
		if n > 0 {
			rangeArgs = []string{s.target + "..HEAD", "-n", strconv.Itoa(min(n, s.max))}
			header = s.rangeHeader()
		} else {
			rangeArgs = []string{"-" + strconv.Itoa(s.max)}
			header = "Recent project history (No diff with " + s.target + ")"
		}
	}

	var chosen commitInfo
	for {
		args := []string{"log", "--format=" + logFormat, logDateArg, "--decorate=short"}
		action, graphEcho := "Retrieving formatted commit history", ""
		if s.graph {
			args = append(args, "--graph", "--topo-order")
			action, graphEcho = "Retrieving visual commit topology and history", "--graph --topo-order "
		}
		args = append(args, rangeArgs...)
		say("DarkGray", fmt.Sprintf("%s: git log --format=%s %s --decorate=short %s%s",
			action, logFormat, logDateEcho, graphEcho, strings.Join(rangeArgs, " ")))
		out, ok := git(s.root, args...)
		if !ok {
			errBlock("Error: git log failed. Aborting.")
			return "", 1
		}
		if strings.TrimSpace(out) == "" {
			say("Yellow", "No commit history found for the specified range.")
			return "", 0
		}
		var rows []logGraphRow
		if s.graph {
			rows = parseLogGraphLines(out, s.remotes)
		} else {
			for _, c := range parseLogLines(nonEmptyLines(out), s.remotes) {
				rows = append(rows, logGraphRow{commitInfo: c})
			}
		}
		var commits []commitInfo
		for _, r := range rows {
			if !r.continuation {
				commits = append(commits, r.commitInfo)
			}
		}
		if len(commits) == 0 {
			fmt.Println()
			return "", 0
		}

		fmt.Println()
		say("Gray", header+":")
		if s.graph {
			logShowCommitGraph(rows)
		} else {
			table("", logCommitHeaders, logCommitRows(commits), true)
		}

		fmt.Println()
		say("DarkGray", "Switches: you can input 'f' for file mode, 'g' to toggle the graph view, 's' to set the file text filter")
		pick, ok := prompt("Input ID of commit to inspect, [blank] = default: 1, a switch, or 'q' to quit, and press enter")
		fmt.Println()
		pick = strings.TrimSpace(pick)
		if !ok || strings.EqualFold(pick, "q") {
			fmt.Println("Quit.")
			fmt.Println()
			return "", 0
		}
		if strings.EqualFold(pick, "g") {
			s.graph = !s.graph
			if s.target != "" && diffCount > 0 {
				header = s.rangeHeader()
			}
			continue
		}
		if strings.EqualFold(pick, "f") {
			fmt.Println("Switched to File mode.")
			return "File", 0
		}
		if strings.EqualFold(pick, "s") {
			term, _ := prompt("Input a text filter for the changed-files table (matches anywhere in the path), [blank] = no filter, and press enter")
			fmt.Println()
			s.filter = strings.TrimSpace(term)
			if s.filter != "" {
				say("Yellow", fmt.Sprintf("Text filter set to '%s'; it filters the file table of the commit you pick.", s.filter))
			} else {
				say("Yellow", "Text filter cleared.")
			}
			continue
		}
		if pick == "" {
			pick = "1"
		}
		if !logIsDigits(pick) {
			errBlock(fmt.Sprintf("Error: Invalid input '%s'. Input a single commit ID.", pick))
			return "", 1
		}
		id, err := strconv.Atoi(pick)
		if err != nil || id < 1 || id > len(commits) {
			errBlock(fmt.Sprintf("Error: ID '%s' is out of range (1-%d). Aborting.", pick, len(commits)))
			return "", 1
		}
		chosen = commits[id-1]
		break
	}
	return s.showCommit(chosen)
}

// showCommit lists what a commit changed, lets the person pick file(s) (filtered by the text
// filter, re-filterable with 't'), and shows their diffs.
func (s *logSession) showCommit(chosen commitInfo) (string, int) {
	hash := chosen.hash
	// A merge lists nothing by default, so compare it explicitly with its first parent: diff-tree
	// silently ignores --first-parent, so the parent hash is passed instead, which keeps this list to
	// files that `git show -m --first-parent` can actually display. A root commit needs --root.
	parentOut, ok := git(s.root, "show", "-s", "--format=%p", hash)
	if !ok {
		errBlock(fmt.Sprintf("Error: could not resolve parents for commit %s. Aborting.", hash))
		return "", 1
	}
	parents := strings.Fields(parentOut)
	isMerge := len(parents) > 1
	treeScope := []string{"--root"}
	showScope, showScopeText := []string{"--format="}, "--format="
	if isMerge {
		treeScope = []string{parents[0]}
		showScope, showScopeText = []string{"--format=", "-m", "--first-parent"}, "--format= -m --first-parent"
	}

	say("DarkGray", fmt.Sprintf("Listing files changed in commit %s: git diff-tree --name-status -r %s", hash, hash))
	nsArgs := append([]string{"diff-tree", "--no-commit-id", "--name-status", "-r", "-z", "-M"}, treeScope...)
	raw, ok := git(s.root, append(nsArgs, hash)...)
	if !ok {
		errBlock(fmt.Sprintf("Error: could not retrieve files changed in commit %s. Aborting.", hash))
		return "", 1
	}
	records := parseLogNameStatusZ(raw, "")
	if len(records) == 0 {
		say("Yellow", fmt.Sprintf("No file changes found for commit %s.", hash))
		fmt.Println()
		return "", 0
	}

	say("DarkGray", fmt.Sprintf("Retrieving line-change volume for changed files: git diff-tree --numstat -r %s", hash))
	numArgs := append([]string{"diff-tree", "--no-commit-id", "--numstat", "-r", "-z", "-M"}, treeScope...)
	numRaw, ok := git(s.root, append(numArgs, hash)...)
	if !ok {
		errBlock(fmt.Sprintf("Error: could not retrieve line change volume for commit %s. Aborting.", hash))
		return "", 1
	}
	stats := logParseNumstatZ(numRaw)
	// name-status and numstat list the same files; a count mismatch means a parse problem
	if len(stats) != len(records) {
		errBlock(fmt.Sprintf("Error: could not match line-change statistics to all changed files in commit %s. Aborting.", hash))
		return "", 1
	}
	statByPath := map[string]diffFile{}
	for _, st := range stats {
		statByPath[st.path] = st
	}
	all := make([]logFileRow, len(records))
	for i, rec := range records {
		name, parent := logSplitPath(rec.path)
		st := statByPath[rec.path]
		all[i] = logFileRow{code: rec.code, date: chosen.date, fileName: name, parent: parent, path: rec.path,
			ins: st.ins, del: st.del, binary: st.binary}
	}
	logSortRows(all)

	// a carried filter that matches nothing is dropped so the first table isn't empty
	if s.filter != "" && len(logFilterRows(all, s.filter)) == 0 {
		say("Yellow", fmt.Sprintf("No files changed in %s match '%s'; showing all.", hash, s.filter))
		s.filter = ""
	}

	var selected []logFileRow
	for {
		view := logFilterRows(all, s.filter)
		title := fmt.Sprintf("Files changed in %s (%s)", hash, chosen.message)
		if s.filter != "" {
			title += fmt.Sprintf(" matching text filter: '%s'", s.filter)
			fmt.Println()
			say("Yellow", fmt.Sprintf("File table filtered by text: '%s'. Input 't' and press enter twice to view all files.", s.filter))
		}
		logFileTable(title+":", view, true, func(r logFileRow) (string, string) { return nameStatus(r.code) })

		say("DarkGray", "Syntax: separate by spaces for the listed IDs, hyphenate for the IDs in a range, prefix IDs with ^ to exclude them e.g. 1-3 5 ^2")
		say("DarkGray", "Switches: you can input 't' to input a new text filter")
		sel, ok := prompt("Input ID(s) of files to view, [blank] = default: 1, 'a' for all, a switch, or 'q' to quit, and press enter")
		sel = strings.TrimSpace(sel)
		if !ok || strings.EqualFold(sel, "q") {
			return logQuit()
		}
		// 't' re-filters; a blank term shows all, a term that matches nothing keeps the current list
		if strings.EqualFold(sel, "t") || strings.EqualFold(sel, "s") {
			fmt.Println()
			term, _ := prompt("Input a text filter (matches anywhere in the path), [blank] = list all and press enter")
			fmt.Println()
			term = strings.TrimSpace(term)
			if term != "" && len(logFilterRows(all, term)) == 0 {
				say("Yellow", fmt.Sprintf("No changed files match '%s'. Keeping the current list.", term))
				continue
			}
			s.filter = term
			continue
		}
		switch {
		case sel == "":
			selected = view[:1]
		case strings.EqualFold(sel, "a"):
			selected = view
		default:
			ids, reason := resolveSelection(sel, len(view))
			if reason != "" {
				errBlock("Error: " + reason + " Aborting.")
				return "", 1
			}
			if len(ids) == 0 {
				fmt.Println()
				say("Yellow", "No files remain after exclusions. Quitting.")
				fmt.Println()
				return "", 0
			}
			for _, id := range ids {
				selected = append(selected, view[id-1])
			}
		}
		break
	}

	if isMerge {
		fmt.Println()
		say("DarkGray", fmt.Sprintf("Commit %s is a merge; showing its changes against the first parent.", hash))
	}
	if !logShowFileDiffs(s.root, hash, selected, showScope, showScopeText) {
		errBlock("Error: One or more selected files could not be shown. Aborting.")
		return "", 1
	}
	return "", 0
}

// fileMode lists tracked and untracked files (or, with a target, the files changed in
// target..HEAD), lets the person pick one, then shows its history and a diff at a chosen commit.
func (s *logSession) fileMode() (string, int) {
	untracked := map[string]bool{}
	current := map[string]string{}
	var rangeArgs []string
	rangeText := ""
	if s.target != "" {
		rangeArgs = []string{s.target + "..HEAD"}
		rangeText = s.target + "..HEAD "
	}

	var all []string
	if s.target != "" {
		if !s.verifyTarget() {
			return "", 1
		}
		say("DarkGray", fmt.Sprintf("Finding files changed vs %s: git log %s..HEAD --name-status -M", s.target, s.target))
		raw, ok := git(s.root, "log", s.target+"..HEAD", "--format=", "--name-status", "-z", "-M")
		if !ok {
			errBlock(fmt.Sprintf("Error: could not retrieve files changed vs %s. Aborting.", s.target))
			return "", 1
		}
		seen := map[string]bool{}
		for _, rec := range parseLogNameStatusZ(raw, "") {
			if !seen[rec.path] {
				seen[rec.path] = true
				all = append(all, rec.path)
			}
		}
		if len(all) == 0 {
			say("Yellow", fmt.Sprintf("No files changed between %s and HEAD.", s.target))
			fmt.Println()
			return "", 0
		}
	} else {
		say("DarkGray", "Finding tracked files: git ls-files")
		say("DarkGray", "Finding untracked files: git ls-files --others --exclude-standard")
		tracked, ok := git(s.root, "ls-files", "-z")
		if !ok {
			errBlock("Error: git ls-files failed while retrieving tracked files. Aborting.")
			return "", 1
		}
		others, ok := git(s.root, "ls-files", "-z", "--others", "--exclude-standard")
		if !ok {
			errBlock("Error: git ls-files --others failed while retrieving untracked files. Aborting.")
			return "", 1
		}
		seen := map[string]bool{}
		for _, p := range logSplitZ(tracked) {
			if !seen[p] {
				seen[p] = true
				all = append(all, p)
			}
		}
		for _, p := range logSplitZ(others) {
			untracked[p] = true
			if !seen[p] {
				seen[p] = true
				all = append(all, p)
			}
		}
		say("DarkGray", "Checking working tree status: git status")
		st, ok := git(s.root, "status", "--porcelain=v1", "-z")
		if !ok {
			errBlock("Error: could not inspect working tree status. Aborting.")
			return "", 1
		}
		for _, e := range parseStatus(st) {
			current[e.path] = e.code
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return strings.ToLower(all[i]) < strings.ToLower(all[j]) })

	matched := logFilterPaths(all, s.filter)
	// listing (and log-walking) every file of a large repo is slow, so offer a filter first
	if s.filter == "" && len(all) > logFileFilterThreshold {
		for {
			fmt.Println()
			term, ok := prompt(fmt.Sprintf("%d files found. Input a text filter to filter (matches anywhere in the path), "+
				"[blank] = list all matches, or 'q' to quit, and press enter", len(all)))
			fmt.Println()
			term = strings.TrimSpace(term)
			if !ok || strings.EqualFold(term, "q") {
				fmt.Println("Quit.")
				fmt.Println()
				return "", 0
			}
			if term == "" {
				break
			}
			filtered := logFilterPaths(all, term)
			if len(filtered) == 0 {
				say("Yellow", fmt.Sprintf("No files match '%s'.", term))
				continue
			}
			matched, s.filter = filtered, term
			break
		}
	}
	if len(matched) == 0 {
		say("Yellow", fmt.Sprintf("No files match '%s'.", s.filter))
		fmt.Println()
		return "", 0
	}

	_, headErr := gitCode(s.root, "rev-parse", "--verify", "--quiet", "HEAD")
	hasHistory := headErr == 0

	// rows are cached across re-filters, so only newly revealed files cost a log walk
	cache := map[string]logFileRow{}
	var fixed logFileRow
	for {
		var toBuild []string
		need := map[string]bool{}
		for _, p := range matched {
			if _, ok := cache[p]; !ok {
				toBuild = append(toBuild, p)
				need[p] = true
			}
		}
		trackedTarget := 0
		for _, p := range toBuild {
			if !untracked[p] {
				trackedTarget++
			}
		}
		untrackedTarget := len(toBuild) - trackedTarget
		dates, codes, stats := map[string]time.Time{}, map[string]string{}, map[string]diffFile{}

		if trackedTarget > 0 && hasHistory {
			say("DarkGray", fmt.Sprintf("Retrieving latest commit and status for %d tracked file(s): git log %s--name-status -z -M", trackedTarget, rangeText))
			args := append(append([]string{"log"}, rangeArgs...), logDateArg, "--format=C=%ad%x00", "--name-status", "-z", "-M")
			raw, ok := git(s.root, args...)
			if !ok {
				errBlock("Error: could not retrieve file commit/status information. Aborting.")
				return "", 1
			}
			for _, rec := range parseLogNameStatusZ(raw, "C=") {
				if need[rec.path] {
					if _, done := dates[rec.path]; !done {
						d, err := time.Parse(logDateLayout, rec.marker)
						if err != nil {
							d = logNoDate
						}
						dates[rec.path], codes[rec.path] = d, rec.code
					}
				}
				if len(dates) >= trackedTarget {
					break
				}
			}
			// +/- volumes only appear in compare mode
			if s.target != "" {
				say("DarkGray", fmt.Sprintf("Retrieving line-change volume for %d tracked file(s): git log %s--numstat -z -M", trackedTarget, rangeText))
				args := append(append([]string{"log"}, rangeArgs...), "--format=", "--numstat", "-z", "-M")
				raw, ok := git(s.root, args...)
				if !ok {
					errBlock("Error: could not retrieve file line-change statistics. Aborting.")
					return "", 1
				}
				for _, st := range logParseNumstatZ(raw) {
					if _, done := stats[st.path]; need[st.path] && !done {
						stats[st.path] = st
					}
					if len(stats) >= trackedTarget {
						break
					}
				}
			}
		}

		if len(toBuild) > 0 {
			what := fmt.Sprintf("%d tracked file(s)", trackedTarget)
			if untrackedTarget > 0 {
				what = fmt.Sprintf("%d file(s) (%d tracked, %d untracked)", len(toBuild), trackedTarget, untrackedTarget)
			}
			say("DarkGray", "Building table for "+what)
		}
		for _, p := range toBuild {
			name, parent := logSplitPath(p)
			row := logFileRow{code: "M", currentCode: current[p], date: logNoDate, fileName: name, parent: parent,
				path: p, untracked: untracked[p]}
			if c, ok := codes[p]; ok {
				row.code = c
			}
			if d, ok := dates[p]; ok {
				row.date = d
			}
			if st, ok := stats[p]; ok {
				row.ins, row.del, row.binary = st.ins, st.del, st.binary
			}
			cache[p] = row
		}

		rows := make([]logFileRow, len(matched))
		for i, p := range matched {
			rows[i] = cache[p]
		}
		logSortRows(rows)

		title := "Tracked and untracked files"
		if s.target != "" {
			title = "Files changed vs " + s.target
		}
		if s.filter != "" {
			title += fmt.Sprintf(" matching text filter: '%s'", s.filter)
			fmt.Println()
			say("Yellow", fmt.Sprintf("File table filtered by text: '%s'. Input 't' and press enter twice to view all files.", s.filter))
		}
		logFileTable(title+":", rows, s.target != "", func(r logFileRow) (string, string) {
			switch {
			case r.untracked:
				return "Untracked", "Green"
			case s.target != "":
				return nameStatus(r.code)
			case r.currentCode != "":
				return describe(r.currentCode)
			}
			return "Unmodified", "DarkGray"
		})

		// default to the most recently committed file
		defaultID, latest := 1, logNoDate
		for i, r := range rows {
			if r.date.After(latest) {
				defaultID, latest = i+1, r.date
			}
		}
		say("DarkGray", "Switches: you can input 't' to input a new text filter, 'c' for commit mode")
		pick, ok := prompt(fmt.Sprintf("Input ID of the file to see its history, [blank] = default: %d, a switch, or 'q' to quit, and press enter", defaultID))
		pick = strings.TrimSpace(pick)
		if !ok || strings.EqualFold(pick, "q") {
			return logQuit()
		}
		if strings.EqualFold(pick, "c") {
			fmt.Println()
			fmt.Println("Switched to Commit mode.")
			return "Commit", 0
		}
		if strings.EqualFold(pick, "t") || strings.EqualFold(pick, "s") {
			fmt.Println()
			term, _ := prompt("Input a text filter (matches anywhere in the path), [blank] = list all, and press enter")
			term = strings.TrimSpace(term)
			next := logFilterPaths(all, term)
			if len(next) == 0 {
				say("Yellow", fmt.Sprintf("No files match '%s'. Keeping the current list.", term))
				continue
			}
			matched, s.filter = next, term
			continue
		}
		fmt.Println()
		if pick == "" {
			pick = strconv.Itoa(defaultID)
		}
		if !logIsDigits(pick) {
			errBlock(fmt.Sprintf("Error: Invalid input '%s'. Input a single file ID.", pick))
			return "", 1
		}
		id, err := strconv.Atoi(pick)
		if err != nil || id < 1 || id > len(rows) {
			errBlock(fmt.Sprintf("Error: ID '%s' is out of range (1-%d). Aborting.", pick, len(rows)))
			return "", 1
		}
		fixed = rows[id-1]
		break
	}

	if fixed.untracked {
		return s.showUntracked(fixed)
	}
	return s.showFileHistory(fixed, rangeArgs, rangeText)
}

// showUntracked renders an untracked file's whole contents as an add-diff against /dev/null.
func (s *logSession) showUntracked(f logFileRow) (string, int) {
	diffCmd := fmt.Sprintf("git diff --no-index -- /dev/null \"%s\"", f.path)
	fmt.Println()
	say("DarkGray", f.path+" is untracked (no commit history); showing its working-tree contents.")
	say("DarkGray", "Showing untracked file: "+diffCmd)
	label := fmt.Sprintf("Untracked file %s (working tree) ", f.fileName)
	bar := strings.Repeat("-", max(40, utf8.RuneCountInString(label)))
	say("DarkGray", bar)
	fmt.Println(label)
	say("DarkGray", bar)
	// --no-index exits 1 when the files differ, which they always do against /dev/null
	out, code := logGitCombined(s.root, "-c", logColorArg(), "diff", "--no-index", "--", "/dev/null", f.path)
	if code < 0 || code > 1 {
		errBlock("Error: could not read untracked file diff. Aborting.")
		return "", 1
	}
	logWriteHunks(out)
	say("DarkGray", bar)
	fmt.Println()
	return "", 0
}

// showFileHistory lists the commits that changed a file (following renames, scoped to the target
// range if any) and shows the file's diff at the chosen commit, using the path it had there.
func (s *logSession) showFileHistory(f logFileRow, rangeArgs []string, rangeText string) (string, int) {
	n := strconv.Itoa(s.max)
	say("DarkGray", fmt.Sprintf("Retrieving history for %s: git log --follow -n %s --format=\"%s\" %s --decorate=short %s-- \"%s\"",
		f.path, n, logFormat, logDateEcho, rangeText, f.path))
	args := append([]string{"log", "--follow", "-n", n, "--format=" + logFormat, logDateArg, "--decorate=short"}, rangeArgs...)
	out, ok := git(s.root, append(args, "--", f.path)...)
	if !ok {
		errBlock("Error: could not load the file's commit history. Aborting.")
		return "", 1
	}
	commits := parseLogLines(nonEmptyLines(out), s.remotes)

	say("DarkGray", fmt.Sprintf("Retrieving path history for %s: git log --follow -n %s --format=\"C=%%h%%x00\" --name-status -z -M %s-- \"%s\"",
		f.path, n, rangeText, f.path))
	nsArgs := append([]string{"log", "--follow", "-n", n, "--format=C=%h%x00", "--name-status", "-z", "-M"}, rangeArgs...)
	nsRaw, ok := git(s.root, append(nsArgs, "--", f.path)...)
	if !ok {
		errBlock("Error: could not retrieve the file's path history. Aborting.")
		return "", 1
	}
	pathAt := map[string]string{}
	for _, rec := range parseLogNameStatusZ(nsRaw, "C=") {
		if rec.marker != "" {
			pathAt[rec.marker] = rec.path
		}
	}

	if len(commits) == 0 {
		scope := ""
		if s.target != "" {
			scope = " in " + s.target + "..HEAD"
		}
		say("Yellow", fmt.Sprintf("No commit history found for %s%s.", f.path, scope))
		fmt.Println()
		return "", 0
	}
	header := "History of " + f.path
	if s.target != "" {
		header += " vs " + s.target
	}
	fmt.Println()
	say("Gray", header+":")
	table("", logCommitHeaders, logCommitRows(commits), true)

	// blank defaults to the most recently dated commit
	defaultID := 1
	for i, c := range commits {
		if c.date.After(commits[defaultID-1].date) {
			defaultID = i + 1
		}
	}
	fmt.Println()
	pick, ok := prompt(fmt.Sprintf("Input ID of commit to inspect, [blank] = default: %d, or 'q' to quit, and press enter", defaultID))
	pick = strings.TrimSpace(pick)
	if !ok || strings.EqualFold(pick, "q") {
		return logQuit()
	}
	if pick == "" {
		pick = strconv.Itoa(defaultID)
	}
	if !logIsDigits(pick) {
		errBlock(fmt.Sprintf("Error: Invalid input '%s'. Input a single commit ID.", pick))
		return "", 1
	}
	id, err := strconv.Atoi(pick)
	if err != nil || id < 1 || id > len(commits) {
		errBlock(fmt.Sprintf("Error: ID '%s' is out of range (1-%d). Aborting.", pick, len(commits)))
		return "", 1
	}
	hash := commits[id-1].hash

	parentOut, ok := git(s.root, "show", "-s", "--format=%P", hash)
	if !ok {
		errBlock(fmt.Sprintf("Error: could not inspect commit %s. Aborting.", hash))
		return "", 1
	}
	parents := strings.Fields(parentOut)
	showScope, showScopeText := []string{"--format="}, "--format="
	switch {
	case len(parents) > 1:
		showScope, showScopeText = []string{"--format=", "-m", "--first-parent"}, "--format= -m --first-parent"
		fmt.Println()
		say("DarkGray", fmt.Sprintf("Commit %s is a merge; showing its changes against the first parent.", hash))
	case len(parents) == 0:
		showScope, showScopeText = []string{"--format=", "--root"}, "--format= --root"
		fmt.Println()
		say("DarkGray", fmt.Sprintf("Commit %s is the root commit; showing the file as a new file.", hash))
	}
	path := f.path
	if p, ok := pathAt[hash]; ok {
		path = p
	}
	name, parent := logSplitPath(path)
	file := logFileRow{path: path, fileName: name, parent: parent}
	if !logShowFileDiffs(s.root, hash, []logFileRow{file}, showScope, showScopeText) {
		errBlock("Error: The selected file could not be shown. Aborting.")
		return "", 1
	}
	return "", 0
}
