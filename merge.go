// merge.go - `ezgit merge`: checks whether your branch is behind its upstream (and the trunk branch),
// and offers to merge the updates. Port of Resolve-GitMerge.ps1.
//
// It is also the "drift check" that `ezgit commit` runs before committing: commit calls
// resolveMerge directly and acts on the status string it returns.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Outcomes of resolveMerge. A caller stops on QUIT, CONFLICT, BLOCKED or ERROR.
const (
	mergeOK       = "OK"       // nothing blocking; carry on
	mergeContinue = "CONTINUE" // a merge was skipped or declined; carry on
	mergeQuit     = "QUIT"
	mergeConflict = "CONFLICT"
	mergeBlocked  = "BLOCKED"
	mergeError    = "ERROR"
)

var (
	conflictMarker = regexp.MustCompile(`(?m)^(<<<<<<<|=======|>>>>>>>)`)
	mergeMsgBranch = regexp.MustCompile(`Merge (?:branch|remote-tracking branch|commit) '(.*?)'`)
)

// incomingMergeFiles lists the files changed on `branch` since it diverged from HEAD. Those are the
// only files a merge could overwrite; diffing against HEAD would also list everything your own
// branch has changed.
func incomingMergeFiles(root, branch string) (files []string, problem string) {
	base, ok := git(root, "merge-base", "HEAD", branch)
	if !ok {
		return nil, fmt.Sprintf("Could not determine the merge base with '%s'.", branch)
	}
	var raw string
	if base = strings.TrimSpace(base); base != "" {
		raw, ok = git(root, "diff", "--name-only", "-z", base, branch)
	} else {
		raw, ok = git(root, "diff", "--name-only", "-z", "HEAD.."+branch)
	}
	if !ok {
		return nil, fmt.Sprintf("Could not determine files changed on '%s'.", branch)
	}
	for _, f := range strings.Split(raw, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, ""
}

// overlapping returns the dirty files that the incoming files would also change.
func overlapping(dirty []entry, incoming []string) []entry {
	set := map[string]bool{}
	for _, f := range incoming {
		set[strings.ToLower(f)] = true
	}
	var out []entry
	for _, e := range dirty {
		if set[strings.ToLower(e.path)] {
			out = append(out, e)
		}
	}
	return out
}

// showMergeConflicts prints how to resolve conflicts. Returns CONFLICT, or ERROR when git aborted
// the merge before it created any conflicts.
func showMergeConflicts(root, branchName string) string {
	out, _ := git(root, "diff", "--name-only", "--diff-filter=U")
	unresolved := nonEmptyLines(out)
	if len(unresolved) == 0 {
		fmt.Println("Git aborted the merge before it could start. No conflict markers were created.")
		return mergeError
	}

	fmt.Println()
	say("Red", "Error: Merge conflicts detected. Aborting.")
	fmt.Println("The following file(s) have conflicts that need to be resolved:")
	for _, f := range unresolved {
		fmt.Println(" - " + filepath.FromSlash(f))
	}

	fmt.Println()
	fmt.Println("To resolve these conflicts manually, you need to:")
	fmt.Println("  1. Open the file(s) with conflicts listed above.")
	fmt.Println("  2. Look for the conflict markers")
	marker := func(label, text string) {
		sayInline("Gray", "     "+pad(label, 45)+" ")
		say("Cyan", text)
	}
	marker("Your local changes start at:", "<<<<<<< HEAD")
	marker("Divider between local and incoming changes:", "=======")
	marker("The incoming changes end at:", ">>>>>>> "+branchName)
	fmt.Println("  3. Choose what to keep, delete what you don't want, then delete all three types of conflict markers.")
	fmt.Println("     Git only requires you to stage a file to mark merge conflicts as resolved, but leaving markers behind will cause syntax errors in many file types.")
	fmt.Println("     If you open a file with conflicts in VS Code, use the 'Accept' buttons above conflict sections.")
	fmt.Println("  4. Save the file(s).")
	fmt.Println("  5. Stage resolved files and commit to complete the merge, for example via:")
	say("Magenta", "     gitcommit")
	fmt.Println()
	fmt.Println("Alternatively, to undo this merge entirely and return to a clean slate, run:")
	say("Magenta", "git merge --abort")
	fmt.Println()
	return mergeConflict
}

// invokeGitMerge merges `branch`. Unless skipDirty, it first refuses if you have uncommitted
// changes in files the merge would touch. Returns MERGED, CONFLICT, BLOCKED or ERROR.
func invokeGitMerge(root, branch string, skipDirty bool) string {
	if !skipDirty {
		raw, ok := git(root, "status", "--porcelain=v1", "-z")
		if !ok {
			fmt.Println()
			say("Red", "Error: failed to read git status. Aborting the merge.")
			fmt.Println()
			return mergeError
		}
		if dirty := parseStatus(raw); len(dirty) > 0 {
			incoming, problem := incomingMergeFiles(root, branch)
			if problem != "" {
				fmt.Println()
				say("Red", "Error: "+problem+" Aborting the merge.")
				fmt.Println()
				return mergeError
			}
			if clash := overlapping(dirty, incoming); len(clash) > 0 {
				fmt.Println()
				say("Yellow", fmt.Sprintf("Merging blocked: you have uncommitted changes in file(s) that '%s' also touches:", branch))
				for _, e := range clash {
					say("DarkGray", " - "+e.path)
				}
				fmt.Println()
				fmt.Println("Stage, commit, or stash these before proceeding with your merge.")
				fmt.Println()
				return mergeBlocked
			}
		}
	}

	fmt.Println()
	say("DarkGray", fmt.Sprintf("Merging changes from %s: git merge %s", branch, branch))
	if runGit(root, "merge", branch) {
		say("Green", "Merge successful, no conflicts detected.")
		return "MERGED"
	}
	return showMergeConflicts(root, branch)
}

// resolveDirtyBeforeMerge is the pre-merge check for when you are behind but have uncommitted
// changes. Returns CLEAN (safe to go ahead), STASH, MERGE, CONTINUE, QUIT or ERROR.
func resolveDirtyBeforeMerge(root, branch string) string {
	raw, ok := git(root, "status", "--porcelain=v1", "-z")
	if !ok {
		fmt.Println()
		say("Red", "Error: failed to read git status. Aborting.")
		fmt.Println()
		return mergeError
	}
	dirty := parseStatus(raw)
	if len(dirty) == 0 {
		return "CLEAN"
	}

	incoming, problem := incomingMergeFiles(root, branch)
	if problem != "" {
		fmt.Println()
		say("Red", "Error: "+problem+" Aborting.")
		fmt.Println()
		return mergeError
	}
	clash := overlapping(dirty, incoming)
	if len(clash) == 0 {
		fmt.Println()
		say("DarkGray", fmt.Sprintf("You are behind '%s', but none of your %d uncommitted file(s) overlap with its incoming changes, so merging is safe.", branch, len(dirty)))
		return "CLEAN"
	}

	say("Yellow", fmt.Sprintf("You are behind '%s' and currently have uncommitted changes in %d file(s) that '%s' also touches:", branch, len(clash), branch))
	for _, e := range clash {
		say("DarkGray", " - "+e.path)
	}
	fmt.Println()
	say("Yellow", "A merge is likely to be blocked until these are staged, committed, or stashed.")

	line, gotInput := prompt("Input 'm' to attempt to merge without stashing your changes, 's' to stash your changes and then merge, 'c' to continue " +
		"checking for drift and to skip this merge for now, or 'q' to quit, and press enter")
	choice := strings.ToLower(strings.TrimSpace(line))
	if !gotInput {
		choice = "q"
	}
	fmt.Println()
	switch choice {
	case "q":
		fmt.Println("Quit.")
		fmt.Println()
		return mergeQuit
	case "c":
		say("DarkGray", fmt.Sprintf("Continuing drift checks, skipped merge from '%s' due to uncommitted changes.", branch))
		return mergeContinue
	case "s":
		if !stashPush(root, true, true, "gitmerge auto-stash before merge from "+branch) {
			say("Yellow", fmt.Sprintf("Failed to stash local changes. Skipping merge from '%s' for now.", branch))
			return mergeContinue
		}
		stashOut, _ := git(root, "stash", "list", "-1", "--format=%gd")
		stashName := strings.TrimSpace(stashOut)
		if stashName == "" {
			say("Yellow", "Stash command reported success but created no stash entry; nothing to restore later.")
			return mergeContinue
		}
		fmt.Println("Local changes stashed.")
		fmt.Println("After the merge is complete, restore your changes by running:")
		say("Magenta", "git stash pop "+stashName)
		return "STASH"
	case "m":
		fmt.Println("Attempting merge with uncommitted changes.")
		return "MERGE"
	}
	fmt.Println()
	say("Red", fmt.Sprintf("Error: Invalid input '%s'. Expected 'm', 'c', 's', or 'q'. Aborting.", choice))
	fmt.Println()
	return mergeQuit
}

// offerMerge handles being behind `branch`: checks the working tree, asks what to do, and merges.
// It returns a status when the drift check must stop here, or "" to carry on. Branches that were
// skipped are added to blocked (uncommitted changes in the way) or declined (you said no).
func offerMerge(root, branch, mergePrompt string, announceSkip bool, blocked, declined *[]string) string {
	stopOn := func(res string) string {
		if res == mergeConflict || res == mergeBlocked || res == mergeError {
			return res
		}
		return ""
	}

	switch resolveDirtyBeforeMerge(root, branch) {
	case mergeQuit:
		return mergeQuit
	case mergeError:
		return mergeError
	case "STASH":
		return stopOn(invokeGitMerge(root, branch, false))
	case "MERGE":
		return stopOn(invokeGitMerge(root, branch, true))
	case mergeContinue:
		if announceSkip {
			say("DarkGray", fmt.Sprintf("Skipping merge from '%s' due to uncommitted changes, continuing drift checks.", branch))
		}
		*blocked = append(*blocked, branch)
	case "CLEAN":
		choice := ""
		for {
			line, gotInput := prompt(mergePrompt)
			choice = strings.TrimSpace(line)
			if !gotInput {
				choice = "q"
			}
			fmt.Println()
			if !strings.EqualFold(choice, "d") {
				break
			}
			say("DarkGray", fmt.Sprintf("Inspecting changes: gitdiff -BranchTarget %s -Direct", branch))
			// TODO: `ezgit diff` hasn't been ported yet; call it here once it exists.
			say("Yellow", fmt.Sprintf("ezgit diff isn't available yet. For now run: git diff --stat HEAD...%s", branch))
		}
		switch {
		case strings.EqualFold(choice, "q"):
			fmt.Println("Quit.")
			fmt.Println()
			return mergeQuit
		case strings.EqualFold(choice, "y"):
			return stopOn(invokeGitMerge(root, branch, false))
		default:
			say("DarkGray", fmt.Sprintf("Skipping merge from '%s' for now.", branch))
			*declined = append(*declined, branch)
		}
	}
	return ""
}

// sortedUnique returns the distinct values, sorted without regard to case.
func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if !seen[strings.ToLower(v)] {
			seen[strings.ToLower(v)] = true
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// resolveMerge is the drift check and merge helper. `embedded` is true when another command (commit)
// is calling it: the tip and the closing blank line are then left out. Returns one of the merge*
// status constants.
func resolveMerge(o options, root string, fetch bool, branchTarget string, embedded bool) string {
	// HEAD doesn't exist between `git init` and the first commit, so there is no history to compare
	if _, ok := git(root, "rev-parse", "--verify", "--quiet", "HEAD"); !ok {
		say("Yellow", "This repository has no commits yet, so there is no branch history to check for drift.")
		fmt.Println("Skipping the drift check so you can create the first commit.")
		return mergeOK
	}

	if !embedded {
		showTip(o, "Merging combines another branch's changes into the one you're on, resolving what it can automatically.\n"+
			"This command checks how far behind you are, merges in the changes, and flags any conflicts for you to resolve.")
	}

	// --- resolve state
	branchOut, _ := git(root, "branch", "--show-current")
	currentBranch := strings.TrimSpace(branchOut)
	targetSpecified := strings.TrimSpace(branchTarget) != ""
	trunk := resolveTrunk(root)
	mainBranch, remoteMain := "", ""
	if trunk.ok {
		mainBranch, remoteMain = trunk.name, trunk.remoteName
	}
	gitDirOut, _ := git(root, "rev-parse", "--absolute-git-dir")
	gitDir := strings.TrimSpace(gitDirOut)
	_, mergeHeadErr := os.Stat(filepath.Join(gitDir, "MERGE_HEAD"))
	isMerging := mergeHeadErr == nil

	// --- a merge already in progress is its own situation: handle it and stop
	if isMerging {
		fmt.Println(gitDir + "/MERGE_HEAD exists therefore a merge is currently in progress")
		say("DarkGray", "Identifying unmerged files: git diff --name-only --diff-filter=U")
		out, _ := git(root, "diff", "--name-only", "--diff-filter=U")
		unmerged := nonEmptyLines(out)

		if len(unmerged) > 0 {
			stillHasMarkers := false
			for _, rel := range unmerged {
				full := filepath.Join(root, filepath.FromSlash(rel))
				say("DarkGray", fmt.Sprintf("Scanning for conflict markers: Select-String -LiteralPath '%s' -Pattern '^<<<<<<<|^=======|^>>>>>>>' -Quiet", full))
				if data, err := os.ReadFile(full); err == nil && conflictMarker.Match(data) {
					stillHasMarkers = true
				}
			}

			if stillHasMarkers {
				// find the incoming branch name so the instructions say what to look for
				incoming := ""
				if msg, err := os.ReadFile(filepath.Join(gitDir, "MERGE_MSG")); err == nil {
					if m := mergeMsgBranch.FindStringSubmatch(string(msg)); m != nil {
						incoming = m[1]
					}
				}
				if strings.TrimSpace(incoming) == "" {
					nameRev, _ := git(root, "name-rev", "--name-only", "MERGE_HEAD")
					incoming = strings.TrimRight(nameRev, "\r\n")
				}
				incoming = strings.TrimPrefix(incoming, "remotes/")
				if strings.EqualFold(incoming, "origin/HEAD") || strings.EqualFold(incoming, "HEAD") || strings.TrimSpace(incoming) == "" {
					incoming = "origin/main"
				}
				showMergeConflicts(root, incoming)
				return mergeConflict
			}
			fmt.Println()
			fmt.Println("No conflict markers found in unmerged files.")
		} else {
			fmt.Println()
			fmt.Println("All files with conflicts have been staged.")
		}

		// both resolved cases land here: the merge just needs committing to finish
		fmt.Println()
		fmt.Println("Conflicts appear resolved. Stage any remaining files and commit to finish the merge, for example via:")
		say("Magenta", "gitcommit")
		fmt.Println()
		return mergeOK
	}

	// --- check drift against the upstream
	upOut, _ := git(root, "rev-parse", "--abbrev-ref", currentBranch+"@{u}")
	upstream := strings.TrimSpace(upOut)
	switch {
	case targetSpecified:
		if !hasPrefixFold(branchTarget, "origin/") {
			branchTarget = "origin/" + branchTarget
		}
	case upstream != "":
		branchTarget = upstream
	default:
		branchTarget = remoteMain
	}

	if strings.TrimSpace(branchTarget) == "" {
		fmt.Println()
		say("Red", "Error: could not determine a target branch. "+trunk.err+" Specify -BranchTarget and try again. Aborting.")
		fmt.Println()
		return mergeError
	}

	if fetch {
		say("DarkGray", "Fetching updates: git fetch --all -q")
		runGit(root, "fetch", "--all", "-q")
	}

	if _, ok := git(root, "rev-parse", "--verify", "--quiet", branchTarget); !ok {
		if targetSpecified {
			fmt.Println()
			say("Red", fmt.Sprintf("Error: Could not find '%s'. Check the branch name or run with -Fetch if it exists on a remote. Aborting.", branchTarget))
			fmt.Println()
			return mergeError
		}
		say("Yellow", "No upstream or remote trunk branch is available, so there is nothing to check for drift.")
		fmt.Println("Continuing with this local repository. You can add a remote later with:")
		say("Magenta", "git remote add origin <repository-url>")
		fmt.Println()
		return mergeOK
	}

	var blocked, declined []string

	say("DarkGray", fmt.Sprintf("Checking drift from upstream repo: git rev-list --count \"HEAD..%s\"", branchTarget))
	behindOut, ok := git(root, "rev-list", "--count", "HEAD.."+branchTarget)
	if !ok {
		fmt.Println()
		say("Red", fmt.Sprintf("Error: Could not compare against '%s' it may not exist locally. Try running with -Fetch, or check for a typo. Aborting.", branchTarget))
		fmt.Println()
		return mergeError
	}
	if behind, _ := strconv.Atoi(strings.TrimSpace(behindOut)); behind > 0 {
		fmt.Println()
		say("Yellow", fmt.Sprintf("Your branch is %d commits behind its remote counterpart (%s).", behind, branchTarget))
		question := fmt.Sprintf("Merge from %s? Input 'y' for yes, 'n' for no, 'd' to inspect changes, or 'q' to quit, and press enter", branchTarget)
		if stop := offerMerge(root, branchTarget, question, true, &blocked, &declined); stop != "" {
			return stop
		}
	}

	// --- optionally check trunk drift as well
	if trunk.ok && !strings.EqualFold(branchTarget, remoteMain) && !strings.EqualFold(currentBranch, mainBranch) {
		say("DarkGray", fmt.Sprintf("Checking drift from main: git rev-list --count \"HEAD..%s\"", remoteMain))
		mainOut, ok := git(root, "rev-list", "--count", "HEAD.."+remoteMain)
		if !ok {
			fmt.Println()
			say("Yellow", fmt.Sprintf("Could not compare against HEAD..'%s' because the trunk ref is not available locally. Skipping the optional trunk drift check.", remoteMain))
		} else if behind, _ := strconv.Atoi(strings.TrimSpace(mainOut)); behind > 0 {
			question := fmt.Sprintf("Merge updates from %s? Input 'y' for yes, 'n' for no, 'd' to inspect changes, or 'q' to quit, and press enter", remoteMain)
			if stop := offerMerge(root, remoteMain, question, false, &blocked, &declined); stop != "" {
				return stop
			}
		}
	}

	// --- final report
	if len(blocked) > 0 || len(declined) > 0 {
		blockedUnique, declinedUnique := sortedUnique(blocked), sortedUnique(declined)
		fmt.Println()
		if len(blockedUnique) > 0 {
			say("Yellow", "Drift check complete. The following branches could not be merged due to uncommitted changes:")
			for _, b := range blockedUnique {
				fmt.Println(" - " + b)
			}
			fmt.Println()
			fmt.Println("To merge these updates, commit your changes and then call this function again by running:")
			say("Magenta", "gitcommit")
			say("Magenta", "gitmerge")
			fmt.Println()
			fmt.Println("Or, to merge without committing, stash your changes first, and then call this function again by running:")
			say("Magenta", "gitstash")
			say("Magenta", "gitmerge")
		}
		if len(declinedUnique) > 0 {
			if len(blockedUnique) > 0 {
				fmt.Println()
			}
			fmt.Println("Drift check complete. You chose not to merge updates from the following branch(es):")
			for _, b := range declinedUnique {
				fmt.Println(" - " + b)
			}
			fmt.Println()
			fmt.Println("When you'd like to pull them into this branch, run:")
			say("Magenta", "gitmerge")
		}
		fmt.Println()
		return mergeContinue
	}

	fmt.Println()
	fmt.Println("Drift check and merge resolution complete, no blocking issues detected.")
	if !embedded {
		fmt.Println()
	}
	return mergeOK
}

func runMerge(args []string) int {
	fs, o := flagsFor("merge")
	fetch := fs.Bool("fetch", false, "run `git fetch --all` before checking for drift")
	target := fs.String("branch-target", "", "branch to compare against (default: your upstream, then the trunk branch)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	root := preflight()
	switch resolveMerge(*o, root, *fetch, *target, false) {
	case mergeOK, mergeContinue:
		return 0
	}
	return 1
}
