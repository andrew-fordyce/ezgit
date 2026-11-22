// status.go - `ezgit status`: a friendlier `git status`. Port of Show-GitStatus.ps1.
// Read-only: it inspects the repo and prints suggestions; it never changes anything.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// statusTable prints one of the Status/Path tables used by `status`.
func statusTable(title string, rows []entry) {
	cells := make([][]cell, len(rows))
	for i, r := range rows {
		label, color := describe(r.code)
		cells[i] = []cell{{label, color}, {r.path, ""}}
	}
	table(title, []string{"Status", "Path"}, cells, false)
}

func runStatus(args []string) int {
	fs, o := flagsFor("status")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	// 1. preflight: git exists and we're inside a working tree
	root := preflight()

	showTip(*o, "Shows where you are right now: your branch, how it compares to the remote, and which files have changed.\n"+
		"Git tracks changes in three places - the working tree (files on disk), the index (what's staged for the next commit), and commits.\n"+
		"The tables below split files the same way 'git status' does. Run 'whatdo' to also list every command in this toolbox.")

	// 2. gather state
	branchOut, _ := git(root, "branch", "--show-current")
	branch := strings.TrimSpace(branchOut)
	detached := branch == ""

	_, mergeInProgress := git(root, "rev-parse", "-q", "--verify", "MERGE_HEAD")

	say("DarkGray", "Checking working tree status: git status --porcelain=v1 -z")
	raw, ok := git(root, "status", "--porcelain=v1", "-z")
	if !ok {
		fail("Error: could not inspect working tree status. Aborting.")
	}
	entries := parseStatus(raw)
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := strings.ToLower(entries[i].path), strings.ToLower(entries[j].path)
		if a != b {
			return a < b
		}
		return entries[i].path < entries[j].path
	})

	// bucket the way git status does: X is the index (staged) side, Y the working tree. A file
	// with both lands in Staged and Not staged, each row keeping only its own side.
	conflictCodes := map[string]bool{"UU": true, "AA": true, "DD": true, "UA": true, "AU": true, "UD": true, "DU": true}
	var conflicts, staged, notStaged, untracked []entry
	for _, e := range entries {
		switch {
		case conflictCodes[e.code]:
			conflicts = append(conflicts, e)
		case e.code == "??":
			untracked = append(untracked, e)
		default:
			if e.code[0] != ' ' {
				staged = append(staged, entry{string(e.code[0]) + " ", e.path})
			}
			if e.code[1] != ' ' {
				notStaged = append(notStaged, entry{" " + string(e.code[1]), e.path})
			}
		}
	}
	conflictCount := len(conflicts)
	stagedCount := len(staged)
	unstagedCount := len(notStaged) + len(untracked)
	dirtyCount := len(entries)

	upOut, upOK := git(root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	upstream := strings.TrimSpace(upOut)
	hasUpstream := upOK && upstream != ""

	ahead, behind := 0, 0
	if hasUpstream {
		say("DarkGray", fmt.Sprintf("Comparing with %s: git rev-list --left-right --count %s...HEAD", upstream, upstream))
		if out, ok := git(root, "rev-list", "--left-right", "--count", upstream+"...HEAD"); ok {
			if p := strings.Fields(out); len(p) >= 2 {
				behind, _ = strconv.Atoi(p[0])
				ahead, _ = strconv.Atoi(p[1])
			}
		}
	}

	say("DarkGray", "Counting stashes: git stash list")
	stashCount := 0
	stashOut, _ := git(root, "stash", "list")
	for _, line := range strings.Split(stashOut, "\n") {
		if strings.TrimSpace(line) != "" {
			stashCount++
		}
	}

	// prefer 'origin', else the sole remote, else guess 'origin'. As in the PowerShell original,
	// "has a remote" only means that listing remotes worked.
	remoteName := "origin"
	remotesOut, hasRemote := git(root, "remote")
	if names := strings.Fields(remotesOut); len(names) == 1 {
		remoteName = names[0]
	}

	lastFetch := "never"
	if p, ok := git(root, "rev-parse", "--git-path", "FETCH_HEAD"); ok {
		if p = strings.TrimSpace(p); p != "" {
			if !filepath.IsAbs(p) {
				p = filepath.Join(root, p)
			}
			if st, err := os.Stat(p); err == nil {
				lastFetch = elapsed(st.ModTime())
			}
		}
	}

	// 3. status summary
	fmt.Println()
	if detached {
		short, _ := git(root, "rev-parse", "--short", "HEAD")
		say("Yellow", fmt.Sprintf("HEAD detached at %s (you're not on any branch).", strings.TrimSpace(short)))
	} else {
		fmt.Print("On branch ")
		sayInline("Cyan", branch)
		fmt.Println(" last fetched: " + lastFetch)
		switch {
		case !hasUpstream:
			say("Yellow", fmt.Sprintf("No upstream - this branch hasn't been pushed to %s yet.", remoteName))
		case ahead == 0 && behind == 0:
			fmt.Printf("Up to date with '%s'.\n", upstream)
		default:
			var drift []string
			if ahead > 0 {
				drift = append(drift, fmt.Sprintf("%d commit(s) ahead (yours, not pushed)", ahead))
			}
			if behind > 0 {
				drift = append(drift, fmt.Sprintf("%d commit(s) behind (theirs, not pulled)", behind))
			}
			color := "Gray" // ahead-only is normal; being behind is what needs attention
			if behind > 0 {
				color = "Yellow"
			}
			say(color, fmt.Sprintf("Compared with '%s': %s.", upstream, strings.Join(drift, ", ")))
		}
	}
	if mergeInProgress {
		say("Yellow", "A merge is in progress - resolve any conflicts, then commit to finish it.")
	}
	if stashCount > 0 {
		say("DarkGray", fmt.Sprintf("  %d stash(es) saved.", stashCount))
	}

	if dirtyCount == 0 {
		fmt.Println()
		say("Green", "Nothing to commit, working tree clean.")
	} else {
		for _, t := range []struct {
			title string
			rows  []entry
		}{
			{"Conflicts - both sides changed these, fix them before committing", conflicts},
			{"Staged changes - in the index, will go into your next commit", staged},
			{"Unstaged changes - changed on disk, but not yet added to the index", notStaged},
			{"Untracked changes - new files git isn't tracking yet", untracked},
		} {
			if len(t.rows) > 0 {
				statusTable(fmt.Sprintf("%s (%d):", t.title, len(t.rows)), t.rows)
			}
		}
		stagedPaths := map[string]bool{}
		for _, r := range staged {
			stagedPaths[r.path] = true
		}
		for _, r := range notStaged {
			if stagedPaths[r.path] {
				say("DarkGray", "Note: a file with both Staged changes and Unstaged changes has more edits since you staged it; only the staged version will be committed.")
				break
			}
		}
	}

	// 4. suggestions, most urgent first
	type suggestion struct{ cmd, why string }
	var sug []suggestion
	add := func(cmd, why string) { sug = append(sug, suggestion{cmd, why}) }

	if detached {
		add("gitswitch", "get back onto a real branch before you lose track of this work")
	}
	if conflictCount > 0 {
		add("gitmerge", fmt.Sprintf("resolve the %d conflicted file(s) and finish the merge", conflictCount))
	}
	if dirtyCount > 0 {
		if unstagedCount > 0 {
			add("gitadd", fmt.Sprintf("choose which %d unstaged file(s) to put into the next commit", unstagedCount))
		}
		if stagedCount > 0 {
			add("gitcommit", fmt.Sprintf("commit the %d file(s) already staged in the index", stagedCount))
			add("gitunstage", "take staged file(s) back out of the index, keeping your edits on disk")
		}
		add("gitdiff", "review exactly what changed, line by line, before committing")
		add("gitstash", "shelve these changes to come back to later")
		add("gitundo", "discard changes and return a file (or everything) to a clean state")
	} else {
		add("gitbranch", "start a new branch for a fresh piece of work")
		add("gitswitch", "jump to another branch")
		add("gitlog", "review the recent commit history on this branch")
	}
	if !detached {
		if hasRemote {
			add("git fetch "+remoteName, "update your local view of the remote branches before comparing or merging")
		}
		if !hasUpstream {
			add("git push -u "+remoteName+" HEAD", "publish this branch so it's backed up and visible to the team")
		} else if behind > 0 && conflictCount == 0 {
			add("gitmerge", fmt.Sprintf("pull in %d upstream commit(s) and resolve any drift", behind))
		} else if ahead > 0 && dirtyCount == 0 {
			add("git push", fmt.Sprintf("upload your %d local commit(s) to the remote", ahead))
		}
	}
	if stashCount > 0 {
		add("gitstash", fmt.Sprintf("you have %d stash(es) waiting - view, apply, or drop them", stashCount))
	}

	// a dbt project between here and the repo root? then suggest the dbt entry point
	rootNorm := norm(root)
	if dir, err := os.Getwd(); err == nil {
		for {
			if _, err := os.Stat(filepath.Join(dir, "dbt_project.yml")); err == nil {
				add("dbtrun", "run dbt models by picking them from a list instead of typing --select")
				break
			}
			parent := filepath.Dir(dir)
			if norm(dir) == rootNorm || parent == dir {
				break
			}
			dir = parent
		}
	}

	fmt.Println()
	fmt.Println("Suggested next steps")
	width := 0
	for _, s := range sug {
		width = max(width, utf8.RuneCountInString(s.cmd))
	}
	for _, s := range sug {
		fmt.Print("  ")
		sayInline("Magenta", pad(s.cmd, width))
		say("Gray", "  "+s.why)
	}
	fmt.Println()
	return 0
}
