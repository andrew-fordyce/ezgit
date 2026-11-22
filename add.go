// add.go - `ezgit add`: interactive staging of changes with a coloured table. Port of
// Add-GitChange.ps1.
//
// Shows files that still have changes to stage, highlights the most recently edited ones, and lets
// you pick files by ID ("1-3 5 ^2"). Files that are already fully staged are not shown; a file with
// both staged and unstaged changes stays visible because there is still something left to stage.
package main

import (
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const timeLayout = "2006-01-02 15:04" // Go's way of writing yyyy-MM-dd HH:mm

// numstat is how many lines were added and removed in one file.
type numstat struct {
	insertions, deletions int
	binary                bool
}

// changedFile is one row of the table.
type changedFile struct {
	modified time.Time // zero value (0001-01-01) for deleted or unreadable files
	state    string    // human label, e.g. "Modified"
	raw      string    // two-letter porcelain code, e.g. " M"
	fileName string
	dir      string // folder part of the path, "." for the repo root
	stat     numstat
	full     string // path relative to the repo root, as git reports it
}

var numstatRename = regexp.MustCompile(`\{.*? => (.*?)\}`)

// parseNumstat reads `git diff --numstat` output ("added<TAB>removed<TAB>path" per line) into a
// path -> numstat lookup. Binary files report "-" for both counts.
func parseNumstat(out string) map[string]numstat {
	lookup := map[string]numstat{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 3)
		if len(parts) < 3 {
			continue
		}
		// renames show as "old => new" or "dir/{old => new}/file"; use the final (new) path
		p := parts[2]
		if numstatRename.MatchString(p) {
			p = numstatRename.ReplaceAllString(p, "${1}")
		} else if i := strings.LastIndex(p, " => "); i >= 0 {
			p = p[i+len(" => "):]
		}
		lookup[p] = toNumstat(parts[0], parts[1])
	}
	return lookup
}

func toNumstat(added, removed string) numstat {
	if added == "-" || removed == "-" {
		return numstat{binary: true}
	}
	a, _ := strconv.Atoi(added)
	r, _ := strconv.Atoi(removed)
	return numstat{insertions: a, deletions: r}
}

// untrackedStat measures a new file against an empty file, because untracked files aren't part of
// a normal `git diff`.
func untrackedStat(root, fullPath string) (numstat, bool) {
	out, _ := git(root, "diff", "--no-index", "--numstat", "--", "/dev/null", fullPath)
	first := strings.TrimRight(strings.SplitN(out, "\n", 2)[0], "\r")
	parts := strings.SplitN(first, "\t", 3)
	if len(parts) < 3 {
		return numstat{}, false
	}
	return toNumstat(parts[0], parts[1]), true
}

// stashPush runs `git stash push` (the 's' switch). Returns false if git failed. With quiet, git's
// own "Saved working directory..." line is not shown (that is how `merge` uses it).
func stashPush(root string, includeUntracked, quiet bool, message string) bool {
	args := []string{"stash", "push"}
	if includeUntracked {
		args = append(args, "-u")
	}
	if message != "" {
		args = append(args, "-m", message)
	}
	say("DarkGray", "Stashing changes: "+displayCommand(args))
	run := runGit
	if quiet {
		run = runGitQuiet
	}
	if !run(root, args...) {
		fmt.Println()
		say("Red", "Error: Failed to create stash. Aborting.")
		fmt.Println()
		return false
	}
	say("Green", "Stash created successfully.")
	return true
}

// formatSeconds rounds to 2 decimal places and drops trailing zeros (1.5, 0.05, 0).
func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(math.Round(d.Seconds()*100)/100, 'f', -1, 64)
}

func runAdd(args []string) int {
	fs, o := flagsFor("add")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	// 1. preflight and tip
	root := preflight()

	showTip(*o, "Staging tells Git which changes you want included in your next commit.\n"+
		"This function shows your unstaged changes and lets you choose which ones to stage.\n"+
		"To take staged files back out instead (your edits stay on disk), use gitunstage.")

	// 2. changed files
	say("DarkGray", "Checking working tree status: git status")
	raw, ok := git(root, "status", "--porcelain=v1", "-z")
	if !ok {
		fmt.Println()
		say("Red", "Error: could not retrieve Git status. Aborting.")
		fmt.Println()
		return 1
	}
	entries := parseStatus(raw)

	// anything staged makes the 'u' hand-off to unstage worth offering
	stagedExists := false
	for _, e := range entries {
		if e.code[0] != ' ' && e.code != "??" {
			stagedExists = true
		}
	}

	say("DarkGray", "Retrieving line-change volume: git diff --numstat")
	numOut, _ := git(root, "-c", "core.quotepath=off", "diff", "--numstat")
	stats := parseNumstat(numOut)

	var files []changedFile
	for _, e := range entries {
		// porcelain status is XY: X = index (staged), Y = working tree (unstaged).
		// only show entries that still have working-tree changes to stage
		if e.code[1] == ' ' {
			continue
		}

		fullPath := filepath.Join(root, filepath.FromSlash(e.path))
		state, _ := describe(e.code)

		if e.code == "??" {
			if _, err := os.Lstat(fullPath); err != nil {
				continue
			}
		}

		var modified time.Time
		if state != "Deleted" {
			if info, err := os.Lstat(fullPath); err == nil {
				modified = info.ModTime()
			}
		}

		stat, haveStat := stats[e.path]
		if e.code == "??" && !haveStat {
			stat, _ = untrackedStat(root, fullPath)
		}

		trimmed := strings.TrimRight(e.path, "/") // untracked folders are reported as "dir/"
		files = append(files, changedFile{
			modified: modified,
			state:    state,
			raw:      e.code,
			fileName: path.Base(trimmed),
			dir:      path.Dir(trimmed),
			stat:     stat,
			full:     e.path,
		})
	}

	if len(files) == 0 {
		fmt.Println()
		fmt.Println("No unstaged changes found. Any remaining changes are already staged.")
		if stagedExists {
			fmt.Print("To take files back out of the index, run: ")
			say("Magenta", "gitunstage")
		}
		fmt.Println()
		return 0
	}

	sort.SliceStable(files, func(i, j int) bool {
		a, b := strings.ToLower(files[i].dir), strings.ToLower(files[j].dir)
		if a != b {
			return a < b
		}
		return strings.ToLower(files[i].fileName) < strings.ToLower(files[j].fileName)
	})

	// 3. file table. The most recently edited minute is highlighted and becomes the default pick.
	latest := files[0].modified
	for _, f := range files {
		if f.modified.After(latest) {
			latest = f.modified
		}
	}
	latestStamp := latest.Format(timeLayout)

	var defaultIDs []string
	rows := make([][]cell, len(files))
	for i, f := range files {
		stamp := f.modified.Format(timeLayout)
		stampColor := "DarkGray"
		if stamp == latestStamp {
			stampColor = "Cyan"
			defaultIDs = append(defaultIDs, strconv.Itoa(i+1))
		}
		plus, minus := strconv.Itoa(f.stat.insertions), strconv.Itoa(f.stat.deletions)
		if f.stat.binary {
			plus, minus = "-", "-"
		}
		_, stateColor := describe(f.raw)
		rows[i] = []cell{
			{f.state, stateColor},
			{stamp, stampColor},
			{f.fileName, ""},
			{plus, "DarkGray"},
			{minus, "DarkGray"},
			{f.dir, ""},
		}
	}
	table("Unstaged changes ready to stage:",
		[]string{"Status", "Modified", "File Name", "+", "-", "Path"}, rows, true)

	// explain the "staged and then edited again" case without complicating the table
	for _, f := range files {
		if f.raw[0] != ' ' && f.raw[0] != '?' && f.raw[1] != ' ' {
			say("DarkGray", "Note: files with both staged and unstaged changes are shown because they still have changes available to stage.")
			break
		}
	}

	// 4. file selection
	defaultStr := strings.Join(defaultIDs, " ")

	say("DarkGray", "Syntax: separate by spaces for the listed IDs, hyphenate for the IDs in a range, prefix IDs with ^ to exclude them e.g. 1-3 5 ^2")
	switchText := "Switches: you can input 's' to stash your changes instead"
	if stagedExists {
		switchText += ", 'u' to unstage files instead (hands over to gitunstage)"
	}
	say("DarkGray", switchText)

	line, gotInput := prompt(fmt.Sprintf("Input ID(s) of files to stage, [blank] = default: %s, 'a' for all, 'n' for none, "+
		"a switch, or 'q' to quit, and press enter", defaultStr))
	selection := strings.TrimSpace(line)
	if !gotInput {
		selection = "q" // input ended: quit rather than silently staging the default
	} else if selection == "" {
		selection = defaultStr
	}
	is := func(word string) bool { return strings.EqualFold(selection, word) }

	if is("q") {
		fmt.Println()
		fmt.Println("Quit.")
		fmt.Println()
		return 0
	}

	// one-way hand-off to unstage: it reads status fresh, and this list would be stale afterwards.
	if is("u") && stagedExists {
		fmt.Println()
		say("DarkGray", "Handing over to: gitunstage")
		// TODO: `ezgit unstage` hasn't been ported yet; call it here once it exists.
		say("Yellow", "ezgit unstage isn't available yet. For now run: gitunstage (PowerShell), or: git restore --staged -- <path>")
		fmt.Println()
		return 1
	}

	if is("s") {
		if !stashPush(root, true, false, "gitadd stash") {
			fmt.Println()
			say("Red", "Error: Failed to stash local changes. Aborting.")
			fmt.Println()
			return 1
		}
		fmt.Println()
		fmt.Println("Local changes stashed, if you want these changes back, run:")
		say("Magenta", "git stash pop")
		return 0
	}

	if is("n") {
		fmt.Println()
		fmt.Println("No files selected for staging. Any files already staged will remain staged.")
		fmt.Println()
		return 0
	}

	var valid []changedFile
	if is("a") {
		valid = files
	} else {
		ids, reason := resolveSelection(selection, len(files))
		if reason != "" {
			fmt.Println()
			say("Red", "Error: "+reason+" Aborting.")
			fmt.Println()
			return 1
		}
		if len(ids) == 0 {
			fmt.Println()
			say("Yellow", "No files remain after exclusions. Aborting.")
			fmt.Println()
			return 1
		}
		for _, id := range ids {
			valid = append(valid, files[id-1])
		}
	}

	// 5. staging
	fmt.Println()
	fmt.Printf("Preparing to stage %d file(s)\n", len(valid))
	start := time.Now()

	for i, item := range valid {
		// drop control characters from the path before handing it to git
		clean := strings.Map(func(r rune) rune {
			if r < 0x20 {
				return -1
			}
			return r
		}, item.full)
		onDisk := filepath.Join(root, filepath.FromSlash(clean))

		if item.state == "Deleted" {
			say("DarkGray", fmt.Sprintf("Staging file %d of %d: git rm --quiet --ignore-unmatch -- \"%s\"", i+1, len(valid), clean))
			// PowerShell also adds each command to up-arrow history. A standalone program can't touch
			// the shell's history, so the exact command is printed above instead, ready to copy.
			if !runGit(root, "rm", "--quiet", "--ignore-unmatch", "--", clean) {
				fmt.Println()
				say("Red", fmt.Sprintf("Error: Git rm failed while staging '%s'. Aborting.", clean))
				fmt.Println()
				return 1
			}
		} else if _, err := os.Lstat(onDisk); err == nil {
			say("DarkGray", fmt.Sprintf("Staging file %d of %d: git add -- \"%s\"", i+1, len(valid), clean))
			if !runGit(root, "add", "--", clean) {
				fmt.Println()
				say("Red", fmt.Sprintf("Error: Git add failed while staging '%s'. Check for locked files. Aborting.", clean))
				fmt.Println()
				return 1
			}
		} else {
			fmt.Printf("Skipping missing/unreachable file: %s\n", clean)
		}
	}
	fmt.Printf("Successfully processed %d file(s) in %s second(s).\n", len(valid), formatSeconds(time.Since(start)))

	// 6. check results: staging is done for a file when nothing is left unstaged
	fmt.Println()
	say("DarkGray", "Checking staging result: git status")
	notDone := 0
	afterRaw, afterOK := git(root, "status", "--porcelain=v1", "-z")
	if afterOK {
		after := parseStatus(afterRaw)
		for _, item := range valid {
			for _, e := range after {
				if e.path == item.full {
					if e.code[1] != ' ' {
						notDone++
					}
					break
				}
			}
		}
	} else {
		notDone = len(valid)
	}

	if afterOK && notDone == 0 {
		say("Green", fmt.Sprintf("Staging complete: all %d selected file(s) staged successfully.", len(valid)))
	} else {
		say("Yellow", fmt.Sprintf("Staging complete. Warning: %d of %d selected file(s) were not fully staged.", notDone, len(valid)))
	}
	fmt.Println()
	return 0
}
