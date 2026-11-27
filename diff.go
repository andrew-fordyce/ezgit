// diff.go - `ezgit diff`: list changed files, then show focused patches. Port of Show-GitDiff.ps1.
//
//	ezgit diff                  dirty tree: your uncommitted changes vs HEAD; clean tree: this branch vs the trunk
//	ezgit diff develop          this branch vs develop (from their merge-base: git diff develop...HEAD)
//	ezgit diff develop "*.sql"  ...only files whose name or folder matches the filter (wildcards * ? [abc] work)
//	ezgit diff main --direct    compare the two branch tips (git diff main HEAD)
//	ezgit diff --fetch          fetch the target first
//	ezgit diff --untracked      include brand-new untracked files, diffed against an empty file
//	ezgit diff --no-list        skip the tables and prompt, and diff everything
//
// At the file prompt, 'u' includes hidden untracked files and 'c' switches to another comparison point.
package main

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// diffFile is one changed file. log.go also uses the code/path/ins/del/binary fields.
type diffFile struct {
	code, path string
	ins, del   int
	binary     bool
	date       time.Time
	// display fields filled in by diffDecorate
	status, statusColor, fileName, dir string
}

// Git's -z formats are deliberately parsed here rather than with strings.Fields: paths may
// contain tabs, quotes, and spaces.
func parseNameStatusZ(raw string) []diffFile {
	f := strings.Split(raw, "\x00")
	var out []diffFile
	for i := 0; i < len(f); {
		if f[i] == "" {
			i++
			continue
		}
		code := f[i]
		i++
		if i >= len(f) {
			break
		}
		p := f[i]
		i++
		if strings.HasPrefix(code, "R") || strings.HasPrefix(code, "C") {
			if i >= len(f) {
				break
			}
			p = f[i]
			i++
		}
		out = append(out, diffFile{code: code, path: p})
	}
	return out
}

// diffResolveNumPath turns the rename forms of non -z numstat ("old => new", "dir/{old => new}/file")
// into the final (new) path.
func diffResolveNumPath(p string) string {
	if i := strings.Index(p, "{"); i >= 0 {
		if j := strings.Index(p[i:], " => "); j >= 0 {
			j += i
			if k := strings.Index(p[j:], "}"); k >= 0 {
				k += j
				return strings.ReplaceAll(p[:i]+p[j+4:k]+p[k+1:], "//", "/")
			}
		}
	}
	if i := strings.Index(p, " => "); i >= 0 {
		return p[i+4:]
	}
	return p
}

// parseNumstatZ reads `git diff --numstat -z` output (newline-separated output is accepted too). In the
// -z form a rename has an empty path field followed by the old and new paths.
func parseNumstatZ(raw string) []diffFile {
	var out []diffFile
	if !strings.Contains(raw, "\x00") {
		for _, line := range strings.Split(raw, "\n") {
			if d, ok := parseNumstatLine(line); ok {
				d.path = diffResolveNumPath(d.path)
				out = append(out, d)
			}
		}
		return out
	}
	f := strings.Split(raw, "\x00")
	for i := 0; i < len(f); {
		if f[i] == "" {
			i++
			continue
		}
		d, ok := parseNumstatLine(f[i])
		i++
		if !ok {
			continue
		}
		if d.path == "" && i+1 < len(f) { // rename/copy: old and new paths follow
			d.path = f[i+1]
			i += 2
		}
		out = append(out, d)
	}
	return out
}

func parseNumstatLine(s string) (diffFile, bool) {
	p := strings.SplitN(strings.TrimSuffix(s, "\r"), "\t", 3)
	if len(p) != 3 {
		return diffFile{}, false
	}
	d := diffFile{path: p[2]}
	if p[0] == "-" || p[1] == "-" {
		d.binary = true
	} else {
		d.ins, _ = strconv.Atoi(p[0])
		d.del, _ = strconv.Atoi(p[1])
	}
	return d, true
}

// diffSplitPath splits a repo-relative path into its file name and folder ("." for the repo root).
func diffSplitPath(p string) (name, dir string) {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:], p[:i]
	}
	return p, "."
}

// diffLike is PowerShell's `-like "*filter*"`: case-insensitive, with * ? [abc] wildcards.
func diffLike(s, filter string) bool {
	return psWildcard("*" + filter + "*").MatchString(s)
}

// filterDiffFiles keeps files whose name or folder matches the text filter (PowerShell -like "*filter*").
func filterDiffFiles(files []diffFile, filter string) []diffFile {
	if strings.TrimSpace(filter) == "" {
		return files
	}
	var out []diffFile
	for _, f := range files {
		name, dir := diffSplitPath(f.path)
		if diffLike(name, filter) || diffLike(dir, filter) {
			out = append(out, f)
		}
	}
	return out
}

// diffHunks prints a diff without its extended header (diff --git / index / --- / +++): the caller has
// already named the file. Binary files have no hunk, so their one-line notice is kept instead.
func diffHunks(raw string) {
	seen := false
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		if seen {
			fmt.Println(line)
		} else if strings.Contains(line, "@@ ") {
			seen = true
			fmt.Println(line)
		} else if strings.Contains(line, "Binary files ") {
			fmt.Println(line)
		}
	}
}

// diffSuggestions lists comparison points that actually resolve in this repo, as "ref (why)" strings.
func diffSuggestions(root string) []string {
	abbrev := func(ref string) string {
		out, ok := git(root, "rev-parse", "--abbrev-ref", ref)
		if !ok {
			return ""
		}
		return strings.TrimSpace(out)
	}
	trunk := ""
	if t := resolveTrunk(root); t.ok {
		trunk = t.remoteName
	}
	parent := ""
	if _, ok := git(root, "rev-parse", "--verify", "--quiet", "HEAD~1"); ok {
		parent = "HEAD~1"
	}
	tag, _ := git(root, "describe", "--tags", "--abbrev=0")
	candidates := []struct{ ref, why string }{
		{trunk, "trunk"},
		{abbrev("@{u}"), "upstream"},
		{abbrev("@{-1}"), "previous branch"},
		{parent, "last commit"},
		{strings.TrimSpace(tag), "latest tag"},
	}
	out := []string{"HEAD (your uncommitted changes)"}
	seen := map[string]bool{}
	for _, c := range candidates {
		if c.ref == "" || seen[c.ref] {
			continue
		}
		seen[c.ref] = true
		out = append(out, c.ref+" ("+c.why+")")
	}
	return out
}

// diffComparison is everything derived from the chosen comparison point.
type diffComparison struct {
	scope, logArgs []string // git diff / git log arguments
	text           string   // scope as shown to the person
	title          string   // table titles
	dateHeader     string
	headline       string // for the announcement
	label          string // what tracked changes are compared against: HEAD or the target
}

func diffComparisonFor(working, direct bool, target string) diffComparison {
	switch {
	case working:
		return diffComparison{scope: []string{"HEAD"}, text: "HEAD", title: "Your uncommitted changes (vs HEAD)",
			dateHeader: "Modified", headline: "your uncommitted changes vs HEAD", label: "HEAD"}
	case direct:
		return diffComparison{scope: []string{target, "HEAD"}, logArgs: []string{target, "HEAD"}, text: target + " HEAD",
			title: "Differences between HEAD and " + target, dateHeader: "Latest Commit",
			headline: "HEAD vs " + target + " (branch tips)", label: target}
	}
	return diffComparison{scope: []string{target + "...HEAD"}, logArgs: []string{target + "..HEAD"}, text: target + "...HEAD",
		title: "Changes on this branch vs " + target, dateHeader: "Latest Commit",
		headline: "this branch vs " + target, label: target}
}

// diffDiscover lists the tracked changes for the comparison and the untracked files. A branch comparison
// also overlays local changes (vs HEAD); as in the PowerShell version the later (local) status wins.
func diffDiscover(root string, cmp diffComparison, working bool) (tracked []diffFile, untracked []string) {
	say("DarkGray", "Discovering changed files: git diff "+cmp.text+" --name-status")
	raw, _ := git(root, append([]string{"diff", "--name-status", "-z"}, cmp.scope...)...)
	files := parseNameStatusZ(raw)
	if !working {
		say("DarkGray", "Discovering local changes: git diff HEAD --name-status")
		local, _ := git(root, "diff", "--name-status", "-z", "HEAD")
		files = append(files, parseNameStatusZ(local)...)
	}
	index := map[string]int{}
	for _, f := range files {
		if i, ok := index[norm(f.path)]; ok {
			tracked[i].code = f.code
			continue
		}
		index[norm(f.path)] = len(tracked)
		tracked = append(tracked, diffFile{code: f.code, path: f.path})
	}
	say("DarkGray", "Discovering untracked files: git ls-files --others --exclude-standard")
	out, _ := git(root, "ls-files", "--others", "--exclude-standard", "-z")
	for _, p := range strings.Split(out, "\x00") {
		if strings.TrimSpace(p) != "" {
			untracked = append(untracked, p)
		}
	}
	return tracked, untracked
}

// diffLatestDates maps each path to its latest commit date in the log range (git log is newest first).
func diffLatestDates(raw string) map[string]time.Time {
	dates := map[string]time.Time{}
	var current time.Time
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if s, ok := strings.CutPrefix(line, "__DATE__"); ok {
			current = time.Time{}
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				current = t.Local()
			}
			continue
		}
		if current.IsZero() {
			continue
		}
		key := norm(strings.ReplaceAll(line, "\\", "/"))
		if _, seen := dates[key]; !seen {
			dates[key] = current
		}
	}
	return dates
}

// diffDecorate fills in the line counts, dates and display fields. Commit dates come from the log range;
// uncommitted mode (or a file with no commit in range) uses the file's mtime, or now for a deleted file.
func diffDecorate(root string, files []diffFile, untracked []string, cmp diffComparison, working, quiet bool) {
	note := func(s string) {
		if !quiet {
			say("DarkGray", s)
		}
	}
	stats := map[string]diffFile{}
	note("Retrieving line-change volume: git diff " + cmp.text + " --numstat")
	raw, _ := git(root, append([]string{"diff", "--numstat", "-z"}, cmp.scope...)...)
	for _, d := range parseNumstatZ(raw) {
		stats[norm(d.path)] = d
	}
	if !working {
		note("Retrieving local line-change volume: git diff HEAD --numstat")
		local, _ := git(root, "diff", "--numstat", "-z", "HEAD")
		for _, d := range parseNumstatZ(local) {
			stats[norm(d.path)] = d
		}
	}
	// untracked files have no diff against the branch, so measure each against an empty file (exit code 1
	// just means "different")
	isUntracked := map[string]bool{}
	for _, p := range untracked {
		isUntracked[norm(p)] = true
	}
	for _, f := range files {
		if !isUntracked[norm(f.path)] {
			continue
		}
		out, _ := gitCode(root, "diff", "--no-index", "--numstat", "--", "/dev/null", f.path)
		if d, ok := parseNumstatLine(strings.SplitN(out, "\n", 2)[0]); ok {
			stats[norm(f.path)] = d
		}
	}
	dates := map[string]time.Time{}
	if !working {
		note("Retrieving commit history: git log " + strings.Join(cmp.logArgs, " ") + ` --format="__DATE__%aI" --name-only`)
		logArgs := append([]string{"-c", "core.quotePath=false", "log"}, cmp.logArgs...)
		out, _ := git(root, append(logArgs, "--format=__DATE__%aI", "--name-only")...)
		dates = diffLatestDates(out)
	}
	for i := range files {
		f := &files[i]
		key := norm(f.path)
		if d, ok := stats[key]; ok {
			f.ins, f.del, f.binary = d.ins, d.del, d.binary
		}
		f.fileName, f.dir = diffSplitPath(f.path)
		info, statErr := os.Stat(root + string(os.PathSeparator) + f.path)
		if f.code == "" {
			f.code = "M"
			if statErr != nil {
				f.code = "D"
			}
		}
		f.status, f.statusColor = nameStatus(f.code)
		switch d, ok := dates[key]; {
		case ok:
			f.date = d
		case statErr == nil:
			f.date = info.ModTime()
		default:
			f.date = time.Now()
		}
	}
}

// diffSortFiles orders files by folder then name (case-insensitively), so IDs are stable.
func diffSortFiles(files []diffFile) {
	sort.SliceStable(files, func(i, j int) bool {
		a, b := strings.ToLower(files[i].dir), strings.ToLower(files[j].dir)
		if a != b {
			return a < b
		}
		return strings.ToLower(files[i].fileName) < strings.ToLower(files[j].fileName)
	})
}

func diffLatest(files []diffFile) time.Time {
	var latest time.Time
	for _, f := range files {
		if f.date.After(latest) {
			latest = f.date
		}
	}
	return latest
}

// diffDefaultIndices is the blank-input default: every file sharing the most recent date.
func diffDefaultIndices(files []diffFile) []string {
	latest := diffLatest(files)
	var out []string
	for i, f := range files {
		if f.date.Equal(latest) {
			out = append(out, strconv.Itoa(i+1))
		}
	}
	return out
}

// diffVolumeBar sizes a path's bar by its line-change volume relative to the busiest path, split into
// green insertions and red deletions. A path with only binary changes gets one neutral block per file.
func diffVolumeBar(ins, del, binaries, maxVolume, width int) (insLen, delLen, binLen int) {
	total := ins + del
	if total == 0 || maxVolume == 0 {
		return 0, 0, min(width, binaries)
	}
	filled := max(1, int(math.RoundToEven(float64(total)/float64(maxVolume)*float64(width))))
	insLen = int(math.RoundToEven(float64(filled) * float64(ins) / float64(total)))
	return insLen, filled - insLen, 0
}

func diffPathTable(files []diffFile, cmp diffComparison) {
	const dateW, barW = 19, 20
	type group struct {
		name                    string
		count, ins, del, binary int
		latest                  time.Time
	}
	var groups []*group
	byName := map[string]*group{}
	pathW := 12
	for _, f := range files {
		g := byName[f.dir]
		if g == nil {
			g = &group{name: f.dir}
			byName[f.dir] = g
			groups = append(groups, g)
		}
		g.count++
		g.ins += f.ins
		g.del += f.del
		if f.binary {
			g.binary++
		}
		if f.date.After(g.latest) {
			g.latest = f.date
		}
		pathW = max(pathW, utf8.RuneCountInString(f.dir))
	}
	sort.SliceStable(groups, func(i, j int) bool { return strings.ToLower(groups[i].name) < strings.ToLower(groups[j].name) })
	maxVolume := 0
	for _, g := range groups {
		maxVolume = max(maxVolume, g.ins+g.del)
	}
	overall := diffLatest(files)

	header := pad(cmp.dateHeader, dateW) + "   " + pad("Path", pathW) + "   " + pad("Files", 8) + "   " + pad("Volume", barW)
	sep := strings.Repeat("-", utf8.RuneCountInString(header))
	fmt.Println()
	fmt.Println(cmp.title + " by path:")
	say("DarkGray", sep)
	say("DarkGray", header)
	say("DarkGray", sep)
	for _, g := range groups {
		color := "Gray"
		if g.latest.Equal(overall) {
			color = "Cyan"
		}
		sayInline(color, pad(g.latest.Format(logDateLayout), dateW)+"   ")
		fmt.Print(pad(g.name, pathW) + "   " + pad(strconv.Itoa(g.count), 8) + "   ")
		insLen, delLen, binLen := diffVolumeBar(g.ins, g.del, g.binary, maxVolume, barW)
		sayInline("Green", strings.Repeat("█", insLen+binLen))
		sayInline("Red", strings.Repeat("█", delLen))
		fmt.Println()
	}
	say("DarkGray", sep)
}

func diffFileTable(files []diffFile, cmp diffComparison) {
	overall := diffLatest(files)
	rows := make([][]cell, len(files))
	for i, f := range files {
		dateColor := "Gray"
		if f.date.Equal(overall) {
			dateColor = "Cyan"
		}
		plus, minus := strconv.Itoa(f.ins), strconv.Itoa(f.del)
		if f.binary {
			plus, minus = "-", "-"
		}
		rows[i] = []cell{{f.status, f.statusColor}, {f.date.Format(logDateLayout), dateColor}, {f.fileName, ""},
			{plus, "DarkGray"}, {minus, "DarkGray"}, {f.dir, ""}}
	}
	table(cmp.title+" by file:", []string{"Status", cmp.dateHeader, "File Name", "+", "-", "Path"}, rows, true)
}

// diffReadComparison asks for a new comparison point. status is keep, change, quit or error; HEAD means
// "my uncommitted work" (a HEAD...HEAD range would be empty).
func diffReadComparison(root string, suggestions []string) (status string, working bool, target string) {
	say("DarkGray", "Suggestions: "+strings.Join(suggestions, ", "))
	choice, ok := prompt("Input a branch or revision to compare against, [blank] = keep the current comparison, or 'q' to quit, and press enter")
	choice = strings.TrimSpace(choice)
	switch {
	case !ok || strings.EqualFold(choice, "q"):
		fmt.Println()
		return "quit", false, ""
	case choice == "":
		fmt.Println()
		return "keep", false, ""
	case choice == "HEAD":
		return "change", true, ""
	}
	if _, ok := git(root, "rev-parse", "--verify", "--quiet", "--end-of-options", choice+"^{commit}"); !ok {
		errBlock(fmt.Sprintf("Error: '%s' does not resolve to a commit. Aborting.", choice))
		return "error", false, ""
	}
	return "change", false, choice
}

// diffReview runs git diff for each selected file, echoing the real command first. A file with local
// changes is diffed vs HEAD; an untracked file against an empty file.
func diffReview(root string, files []diffFile, cmp diffComparison, working bool) int {
	fmt.Println()
	fmt.Printf("Executing git diff for %d file(s)\n", len(files))
	fmt.Println()
	failed := 0
	for i, f := range files {
		label := "Git diff for: " + f.fileName
		bar := strings.Repeat("-", max(80, utf8.RuneCountInString(label)))
		say("DarkGray", fmt.Sprintf("Checking for unsaved/staged changes in file %d of %d: git status -- \"%s\"", i+1, len(files), f.path))
		local, _ := git(root, "status", "--porcelain", "--", f.path)
		var args []string
		var note string
		switch {
		case strings.HasPrefix(strings.TrimSpace(local), "??"):
			args = []string{"diff", "--no-index", "--", "/dev/null", f.path}
			note = "New untracked file, showing its full contents as an addition"
		case strings.TrimSpace(local) != "":
			args = []string{"diff", "HEAD", "--", f.path}
			note = "Showing local modifications vs last commit"
			if !working {
				say("Yellow", "Warning: Unsaved/staged changes detected, falling back to diff vs HEAD instead of vs the branch target")
			}
		default:
			args = append(append([]string{"diff"}, cmp.scope...), "--", f.path)
			note = "No unsaved/staged changes, showing comparison"
		}
		say("DarkGray", note+": git "+strings.Join(args[:len(args)-1], " ")+" \""+f.path+"\"")
		say("DarkGray", bar)
		fmt.Println(label)
		say("DarkGray", bar)
		out, exit := gitCode(root, append([]string{"-c", "color.diff=always"}, args...)...)
		// --no-index exits 1 when the files differ, which is the expected result for an untracked file
		if exit != 0 && !(args[1] == "--no-index" && exit == 1) {
			failed++
			say("Red", fmt.Sprintf("Error: git diff failed for '%s'.", f.path))
		}
		diffHunks(out)
		say("DarkGray", bar)
		if i < len(files)-1 {
			fmt.Println()
		}
	}
	fmt.Println()
	if failed > 0 {
		errBlock(fmt.Sprintf("Error: git diff failed for %d of %d file(s).", failed, len(files)))
		return 1
	}
	return 0
}

func runDiff(args []string) int {
	fs, o := flagsFor("diff", "[branch-target] [text-filter]")
	fetch := fs.Bool("fetch", false, "fetch the branch target before comparing (origin/x fetches x from origin; x fetches x from origin)")
	noList := fs.Bool("no-list", false, "skip the tables and prompt and diff every changed file")
	direct := fs.Bool("direct", false, "compare the branch tips (git diff target HEAD) instead of from the merge-base")
	untrackedFlag := fs.Bool("untracked", false, "also include untracked (new) files")
	pos, code, ok := parseFlagsPos(fs, args, 2)
	if !ok {
		return code
	}
	target, filter := "", ""
	if len(pos) > 0 {
		target = pos[0]
	}
	if len(pos) > 1 {
		filter = pos[1]
	}
	root := preflight()
	showTip(*o, "A diff shows the exact lines that differ between two points, like your branch and main.\n"+
		"This command lists which files changed and lets you pick one to view without typing out file paths by hand.")

	// with no explicit target, a dirty tree defaults to your uncommitted edits (vs HEAD); a clean tree, or
	// --fetch/--direct, compares against the trunk. Uncommitted mode always includes untracked files, so
	// remember the person's own choice for when they switch away from it.
	untrackedRequested := *untrackedFlag
	includeUntracked := untrackedRequested
	explicit := target != "" || *fetch || *direct
	working := false
	if !explicit {
		say("DarkGray", "Checking working tree status: git status --porcelain")
		if s, _ := git(root, "status", "--porcelain"); strings.TrimSpace(s) != "" {
			working, includeUntracked = true, true
		}
	}
	if !working && target == "" {
		t := resolveTrunk(root)
		if !t.ok {
			errBlock("Error: could not determine a trunk branch. " + t.err + " Specify a branch target and try again. Aborting.")
			return 1
		}
		target = t.remoteName
	}

	var (
		cmp         diffComparison
		tracked     []diffFile
		untracked   []string
		suggestions []string
	)
	needsSetup := true
	for {
		if needsSetup {
			needsSetup = false
			if *fetch && !working {
				remote, branch := "origin", target
				if p := strings.SplitN(target, "/", 2); len(p) == 2 {
					remote, branch = p[0], p[1]
				}
				say("DarkGray", "Syncing branch target with remote: git fetch "+remote+" "+branch)
				if _, c := gitCode(root, "fetch", remote, branch); c != 0 {
					errBlock(fmt.Sprintf("Error: Failed to fetch '%s' from '%s'. Aborting.", branch, remote))
					return 1
				}
			}
			if !working {
				if _, ok := git(root, "rev-parse", "--verify", "--quiet", target+"^{commit}"); !ok {
					errBlock(fmt.Sprintf("Error: Branch target '%s' does not exist. Aborting.", target))
					return 1
				}
			}
			cmp = diffComparisonFor(working, *direct, target)
			reason := "Comparing against " + cmp.label
			if !explicit && working {
				reason = "Uncommitted changes found in your working tree, defaulting to a comparison against HEAD"
			} else if !explicit {
				reason = "No uncommitted changes, defaulting to a comparison against the trunk, " + target
			}
			fmt.Println()
			say("Gray", reason+".")
			say("DarkGray", "Comparing "+cmp.headline+": git diff "+cmp.text)
			tracked, untracked = diffDiscover(root, cmp, working)
		}

		files := append([]diffFile(nil), tracked...)
		if includeUntracked {
			for _, p := range untracked {
				files = append(files, diffFile{code: "A", path: p})
			}
		}
		hidden := !includeUntracked && len(untracked) > 0
		hasFiles := len(files) > 0

		var defaults []string
		var promptText string
		if hasFiles {
			diffDecorate(root, files, untracked, cmp, working, *noList)
			if strings.TrimSpace(filter) != "" {
				files = filterDiffFiles(files, filter)
				if len(files) == 0 {
					say("Yellow", fmt.Sprintf("No changes found matching text filter: '%s'", filter))
					if !includeUntracked {
						matching := 0
						for _, p := range untracked {
							name, dir := diffSplitPath(p)
							if diffLike(name, filter) || diffLike(dir, filter) {
								matching++
							}
						}
						if matching > 0 {
							say("Yellow", fmt.Sprintf("%d untracked file(s) match but aren't shown by default.", matching))
							say("Magenta", "Re-run to include them: ezgit diff --untracked")
						}
					}
					fmt.Println()
					return 0
				}
				say("DarkGray", fmt.Sprintf("Filtering results by: '%s'", filter))
			}
			diffSortFiles(files)
			if hidden {
				fmt.Println()
				say("Yellow", fmt.Sprintf("Warning: %d untracked file(s) are not being shown, git diff ignores untracked files by default.", len(untracked)))
			}
			if *noList {
				return diffReview(root, files, cmp, working)
			}
			diffPathTable(files, cmp)
			diffFileTable(files, cmp)
			defaults = diffDefaultIndices(files)
			hints := []string{}
			if hidden {
				hints = append(hints, "'u' to include untracked files")
			}
			hints = append(hints, "'c' to change the comparison")
			say("DarkGray", "Syntax: separate by spaces for the listed IDs, hyphenate for the IDs in a range, prefix IDs with ^ to exclude them e.g. 1-3 5 ^2")
			say("DarkGray", "Switches: you can input "+strings.Join(hints, ", "))
			promptText = "Input ID(s) of files to diff, [blank] = default: " + strings.Join(defaults, " ") +
				", 'a' for all, a switch, or 'q' to quit, and press enter"
		} else {
			if *noList {
				if hidden {
					say("Yellow", fmt.Sprintf("No tracked changes vs '%s', but %d untracked file(s) exist and aren't shown by default.", cmp.label, len(untracked)))
					say("Magenta", "Re-run to include them: ezgit diff --untracked")
				} else {
					fmt.Println("No file changes detected for this comparison. Quitting.")
					fmt.Println()
				}
				return 0
			}
			fmt.Println()
			uOption := ""
			if hidden {
				say("Yellow", fmt.Sprintf("No tracked changes vs '%s', but %d untracked file(s) exist.", cmp.label, len(untracked)))
				uOption = "'u' to include untracked files, "
			} else {
				say("Yellow", fmt.Sprintf("No file changes found vs '%s'.", cmp.label))
			}
			promptText = "Input " + uOption + "'c' to change the comparison, or 'q' to quit, and press enter"
		}

		selection, gotInput := prompt(promptText)
		fmt.Println()
		command := strings.ToLower(strings.TrimSpace(selection))
		if !gotInput || command == "q" {
			fmt.Println("Quit.")
			fmt.Println()
			return 0
		}
		if command == "u" && hidden {
			say("DarkGray", "Including untracked files.")
			includeUntracked = true
			continue
		}
		if command == "c" {
			if suggestions == nil {
				suggestions = diffSuggestions(root)
			}
			status, toWorking, toTarget := diffReadComparison(root, suggestions)
			switch status {
			case "quit":
				fmt.Println("Quit.")
				fmt.Println()
				return 0
			case "error":
				return 1
			case "change":
				working, target = toWorking, toTarget
				includeUntracked = working || untrackedRequested
				explicit, needsSetup = true, true
			}
			continue
		}
		if !hasFiles {
			say("Red", fmt.Sprintf("Error: Invalid input '%s'. Aborting.", selection))
			fmt.Println()
			return 1
		}

		var selected []diffFile
		switch {
		case command == "a":
			selected = files
		case command == "":
			for _, id := range defaults {
				n, _ := strconv.Atoi(id)
				selected = append(selected, files[n-1])
			}
		default:
			ids, reason := resolveSelection(selection, len(files))
			if reason != "" {
				say("Red", "Error: "+reason+" Aborting.")
				fmt.Println()
				return 1
			}
			if len(ids) == 0 {
				say("Yellow", "No files remain after exclusions. Quitting.")
				fmt.Println()
				return 0
			}
			for _, id := range ids {
				selected = append(selected, files[id-1])
			}
		}
		if len(selected) == 0 {
			say("Yellow", "No files selected. Aborting.")
			fmt.Println()
			return 0
		}
		return diffReview(root, selected, cmp, working)
	}
}
