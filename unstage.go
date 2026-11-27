package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var unstageConflictCodes = map[string]bool{
	"UU": true, "AA": true, "DD": true, "UA": true, "AU": true, "UD": true, "DU": true,
}

type stagedRow struct {
	entry
	orig       string
	fullState  string
	modified   time.Time
	adds, dels int
	binary     bool
}

func parseStagedRows(raw, root, numstat string) []stagedRow {
	stats := map[string][3]int{} // additions, deletions, binary
	for _, line := range nonEmptyLines(numstat) {
		p := strings.SplitN(line, "\t", 3)
		if len(p) != 3 {
			continue
		}
		path := p[2]
		if start := strings.LastIndex(path, "{"); start >= 0 {
			if arrow := strings.Index(path[start:], " => "); arrow >= 0 {
				arrow += start
				if end := strings.Index(path[arrow+4:], "}"); end >= 0 {
					end += arrow + 4
					path = path[:start] + path[arrow+4:end] + path[end+1:]
				}
			}
		} else if i := strings.LastIndex(path, " => "); i >= 0 {
			path = path[i+4:]
		}
		a, d := 0, 0
		bin := p[0] == "-" || p[1] == "-"
		if !bin {
			a, _ = strconv.Atoi(p[0])
			d, _ = strconv.Atoi(p[1])
		}
		stats[path] = [3]int{a, d, boolInt(bin)}
	}
	var rows []stagedRow
	fields := strings.Split(raw, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		code, path := f[:2], f[3:]
		if code[0] == ' ' || code == "??" || unstageConflictCodes[code] {
			continue
		}
		orig := ""
		if strings.ContainsAny(code, "RC") && i+1 < len(fields) {
			i++
			orig = fields[i]
		}
		t := time.Time{}
		if code[1] != 'D' {
			if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err == nil {
				t = st.ModTime()
			}
		}
		s := stats[path]
		rows = append(rows, stagedRow{entry: entry{string(code[0]) + " ", path}, orig: orig, fullState: code, modified: t, adds: s[0], dels: s[1], binary: s[2] != 0})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		iDir, jDir := strings.ToLower(filepath.Dir(rows[i].path)), strings.ToLower(filepath.Dir(rows[j].path))
		if iDir != jDir {
			return iDir < jDir
		}
		return strings.ToLower(filepath.Base(rows[i].path)) < strings.ToLower(filepath.Base(rows[j].path))
	})
	return rows
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func runUnstage(args []string) int {
	fs, o := flagsFor("unstage")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	root := preflight()
	showTip(*o, "Unstaging takes a file back out of the index (what's lined up for your next commit).\nYour edits stay on disk as unstaged changes - nothing is thrown away. To discard edits, use gitundo instead.")
	say("DarkGray", "Checking working tree status: git status")
	raw, ok := git(root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if !ok {
		errBlock("Error: could not retrieve Git status. Aborting.")
		return 1
	}
	say("DarkGray", "Retrieving line-change volume: git diff --cached --numstat")
	num, _ := git(root, "-c", "core.quotepath=off", "diff", "--cached", "--numstat")
	rows := parseStagedRows(raw, root, num)
	if len(rows) == 0 {
		fmt.Println("Nothing is staged, so there's nothing to unstage.")
		for _, e := range parseStatus(raw) {
			if len(e.code) == 2 && e.code[1] != ' ' {
				fmt.Print("To stage files, run: ")
				say("Magenta", "gitadd")
				break
			}
		}
		return 0
	}
	latest := time.Time{}
	for _, r := range rows {
		if r.modified.After(latest) {
			latest = r.modified
		}
	}
	tableRows := make([][]cell, len(rows))
	for i, r := range rows {
		label, color := describe(r.code)
		mt := r.modified.Format("2006-01-02 15:04")
		adds, dels := strconv.Itoa(r.adds), strconv.Itoa(r.dels)
		if r.binary {
			adds, dels = "-", "-"
		}
		tableRows[i] = []cell{{label, color}, {mt, func() string {
			if r.modified.Format("2006-01-02 15:04") == latest.Format("2006-01-02 15:04") {
				return "Cyan"
			}
			return "DarkGray"
		}()}, {filepath.Base(filepath.FromSlash(r.path)), ""}, {adds, "DarkGray"}, {dels, "DarkGray"}, {filepath.Dir(filepath.FromSlash(r.path)), ""}}
	}
	table("Staged changes that can be unstaged:", []string{"Status", "Modified", "File Name", "+", "-", "Path"}, tableRows, true)
	for _, r := range rows {
		if len(r.fullState) == 2 && r.fullState[1] != ' ' {
			say("DarkGray", "Note: some of these files also have unstaged edits; unstaging folds the staged edits back in with them.")
			break
		}
	}
	say("DarkGray", "Syntax: separate by spaces for the listed IDs, hyphenate for the IDs in a range, prefix IDs with ^ to exclude them e.g. 1-3 5 ^2")
	answer, _ := prompt("Input ID(s) of files to unstage, [blank] = default, 'a' for all, 'n' for none, or 'q' to quit, and press enter")
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "" {
		var defaults []string
		for i, r := range rows {
			if r.modified.Format("2006-01-02 15:04") == latest.Format("2006-01-02 15:04") {
				defaults = append(defaults, strconv.Itoa(i+1))
			}
		}
		if len(defaults) == 0 {
			defaults = []string{"1"}
		}
		answer = strings.Join(defaults, " ")
	}
	if answer == "q" || answer == "n" {
		fmt.Println("No files selected for unstaging. Everything staged will remain staged.")
		return 0
	}
	var ids []int
	if answer == "a" {
		for i := range rows {
			ids = append(ids, i+1)
		}
	} else {
		var reason string
		ids, reason = resolveSelection(answer, len(rows))
		if reason != "" {
			errBlock("Error: " + reason + " Aborting.")
			return 1
		}
		if len(ids) == 0 {
			fmt.Println("No files remain after exclusions. Aborting.")
			return 0
		}
	}
	_, head := git(root, "rev-parse", "-q", "--verify", "HEAD")
	fmt.Printf("Preparing to unstage %d file(s)\n", len(ids))
	for position, id := range ids {
		r := rows[id-1]
		paths := []string{r.path}
		if r.orig != "" {
			paths = append(paths, r.orig)
		}
		var cmd []string
		if head {
			cmd = append([]string{"restore", "--staged", "--"}, paths...)
		} else {
			cmd = append([]string{"rm", "--cached", "--quiet", "--"}, paths...)
		}
		description := fmt.Sprintf("Unstaging file %d of %d", position+1, len(ids))
		if !head {
			description += " (no commits yet, so no HEAD to restore from)"
		}
		say("DarkGray", description+": "+displayCommand(cmd))
		if !runGit(root, cmd...) {
			errBlock("Error: Git failed while unstaging '" + r.path + "'. Aborting.")
			return 1
		}
	}
	say("DarkGray", "Checking unstaging result: git status")
	raw2, statusOK := git(root, "status", "--porcelain=v1", "-z")
	remaining := 0
	if !statusOK {
		remaining = len(ids)
	}
	if statusOK {
		for _, id := range ids {
			for _, e := range parseStatus(raw2) {
				if e.path == rows[id-1].path && e.code[0] != ' ' && e.code != "??" {
					remaining++
				}
			}
		}
	}
	if remaining == 0 {
		say("Green", fmt.Sprintf("Unstaging complete: all %d selected file(s) unstaged. Your edits are still on disk.", len(ids)))
	} else {
		say("Yellow", fmt.Sprintf("Unstaging complete. Warning: %d of %d selected file(s) still have staged changes.", remaining, len(ids)))
	}
	return 0
}
