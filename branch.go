// branch.go - create, remove, and rename local branches.
package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const branchDateLayout = "2006-01-02 15:04:05"

type branchChoice struct {
	ref, display, kind string
	date               time.Time
	current            bool
}

func parseBranchDate(s string) time.Time {
	t, err := time.ParseInLocation(branchDateLayout, strings.TrimSpace(s), time.Local)
	if err != nil {
		return time.Date(1900, 1, 1, 0, 0, 0, 0, time.Local)
	}
	return t
}

// branchChoices mirrors `git branch -a`: symbolic remote HEAD entries are omitted and
// origin/foo is displayed as foo, while retaining the actual ref used for creation.
func branchChoices(root string) ([]branchChoice, bool) {
	format := "%(refname:short)%00%(HEAD)%00%(committerdate:format:%Y-%m-%d %H:%M:%S)%00%(symref)"
	out, ok := git(root, "for-each-ref", "--sort=-committerdate", "--format="+format,
		"refs/heads", "refs/remotes")
	if !ok {
		return nil, false
	}
	var result []branchChoice
	seen := map[string]int{}
	for _, line := range nonEmptyLines(out) {
		p := strings.SplitN(line, "\x00", 4)
		if len(p) < 4 || strings.TrimSpace(p[0]) == "" || strings.TrimSpace(p[3]) != "" {
			continue
		}
		ref := strings.TrimSpace(p[0])
		if strings.HasPrefix(ref, "(HEAD detached") {
			continue
		}
		display, kind := ref, "Local"
		if strings.HasPrefix(ref, "origin/") {
			display, kind = strings.TrimPrefix(ref, "origin/"), "Remote"
		}
		key := strings.ToLower(display)
		choice := branchChoice{
			ref: ref, display: display, kind: kind,
			date: parseBranchDate(p[2]), current: strings.TrimSpace(p[1]) == "*",
		}
		if previous, exists := seen[key]; exists {
			// A local ref is the useful choice when both local and origin/foo exist.
			if result[previous].kind == "Remote" && kind == "Local" {
				result[previous] = choice
			}
			continue
		}
		seen[key] = len(result)
		result = append(result, choice)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].date.After(result[j].date) })
	return result, true
}

func selectBranch(choices []branchChoice, title, defaultDescription string, detached, detachedDefaultsToHead, showType bool) (branchChoice, bool) {
	if len(choices) == 0 {
		return branchChoice{}, false
	}
	rows := make([][]cell, len(choices))
	defaultID := 1
	if !detached {
		for i, b := range choices {
			if b.current {
				defaultID = i + 1
				break
			}
		}
	}
	latest := choices[0].date
	for _, branch := range choices[1:] {
		if branch.date.After(latest) {
			latest = branch.date
		}
	}
	for i, b := range choices {
		current := ""
		if b.current {
			current = "(current)"
		}
		color := ""
		if b.current {
			color = "Cyan"
		}
		dateColor := "Gray"
		if b.date.Equal(latest) {
			dateColor = "Cyan"
		}
		if showType {
			rows[i] = []cell{{b.date.Format(branchDateLayout), dateColor}, {current, "Cyan"}, {b.display, color}, {b.kind, ""}}
		} else {
			rows[i] = []cell{{b.date.Format(branchDateLayout), dateColor}, {current, "Cyan"}, {b.display, color}}
		}
	}
	headers := []string{"Latest Commit", "Current", "Branch Name", "Type"}
	if !showType {
		headers = headers[:3]
	}
	table(title, headers, rows, true)
	answer, got := prompt(fmt.Sprintf("Input branch ID, [blank] = default: %s, or 'q' to quit", defaultDescription))
	if !got || strings.EqualFold(strings.TrimSpace(answer), "q") {
		return branchChoice{}, false
	}
	if detached && detachedDefaultsToHead && strings.TrimSpace(answer) == "" {
		return branchChoice{ref: "HEAD", display: "HEAD", kind: "Local"}, true
	}
	id := defaultID
	if strings.TrimSpace(answer) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(answer))
		if err != nil || n < 1 || n > len(choices) {
			errText := strings.TrimSpace(answer)
			if err != nil {
				errText = fmt.Sprintf("Invalid input '%s', expected ID number.", errText)
			} else {
				errText = fmt.Sprintf("ID '%s' is out of range (1-%d).", errText, len(choices))
			}
			errBlock("Error: " + errText)
			return branchChoice{}, false
		}
		id = n
	}
	return choices[id-1], true
}

var branchWhitespace = regexp.MustCompile(`\s+`)

func normalizedBranchName(raw string) string {
	return branchWhitespace.ReplaceAllString(strings.TrimSpace(raw), "-")
}

func runBranch(args []string) int {
	fs, o := flagsFor("branch", "[name]")
	fetch := fs.Bool("fetch", false, "run `git fetch --all` before listing possible base branches")
	var maxBranches int
	fs.IntVar(&maxBranches, "n", 20, "most recent base branches to show")
	fs.IntVar(&maxBranches, "max-branches", 20, "most recent base branches to show")
	pos, code, ok := parseFlagsPos(fs, args, 1)
	if !ok {
		return code
	}
	if maxBranches < 1 {
		return usageError("--max-branches must be at least 1")
	}

	root := preflight()
	remote, remoteOK := defaultRemote(root)
	if !remoteOK {
		errBlock("Error: could not determine the default remote. Aborting.")
		return 1
	}
	showTip(*o, "Branches are independent lines of work, letting you experiment or build something without touching main.\nThis command lists existing branches so you can pick a base, prompts for a new branch name, then creates and switches you onto it.")
	if *fetch {
		say("DarkGray", "Fetching updates: git fetch --all -q")
		if !runGit(root, "fetch", "--all", "-q") {
			errBlock("Error: Failed to fetch remote updates. Aborting.")
			return 1
		}
	}

	raw, statusOK := git(root, "status", "--porcelain=v1", "-z")
	if !statusOK {
		errBlock("Error: failed to read git status. Aborting.")
		return 1
	}
	changesStashed := false
	if dirty := parseStatus(raw); len(dirty) > 0 {
		fmt.Printf("\nWarning: You have uncommitted changes in %d file(s):\n", len(dirty))
		for _, e := range dirty {
			fmt.Println(" - " + e.path)
		}
		fmt.Println("These changes aren't tied to your current branch until committed, so they'll follow you onto the new branch if you create one now.")
		answer, got := prompt("Input 'c' to carry these changes onto the new branch, 's' to stash them here and not bring them with you, or 'q' to quit, and press enter")
		fmt.Println()
		if !got || strings.EqualFold(strings.TrimSpace(answer), "q") {
			fmt.Println("Quit.")
			return 0
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "c":
			fmt.Println("Continuing, these changes will carry onto the new branch.")
		case "s":
			if !pushStash(root, true, false, "gitbranch auto-stash before branch switch", nil) {
				errBlock("Error: Failed to stash local changes. Aborting.")
				return 1
			}
			changesStashed = true
			fmt.Println("Local changes stashed, if you want these changes back, run:")
			say("Magenta", "git stash pop")
		default:
			errBlock(fmt.Sprintf("Error: Invalid input '%s'. Aborting.", strings.TrimSpace(answer)))
			return 1
		}
	}

	say("DarkGray", "Checking branches: git branch -a")
	choices, ok := branchChoices(root)
	if !ok {
		errBlock("Error: failed to list branches. Aborting.")
		return 1
	}
	if len(choices) == 0 {
		errBlock("Error: No branches found. Aborting.")
		return 1
	}
	trunk := resolveTrunk(root)
	if trunk.ok {
		found := false
		for _, b := range choices {
			if b.display == trunk.name {
				found = true
				break
			}
		}
		if !found {
			if all, ok := branchChoices(root); ok {
				for _, b := range all {
					if b.display == trunk.name {
						choices = append(choices, b)
						break
					}
				}
			}
		}
	}
	total := len(choices)
	if total > maxBranches {
		all := choices
		choices = append([]branchChoice(nil), all[:maxBranches]...)
		// Current and trunk are protected from the display cap.
		for _, b := range all {
			required := b.current || (trunk.ok && b.display == trunk.name)
			if !required {
				continue
			}
			present := false
			for _, shown := range choices {
				if shown.ref == b.ref {
					present = true
					break
				}
			}
			if !present {
				choices = append(choices, b)
			}
		}
		sort.SliceStable(choices, func(i, j int) bool { return choices[i].date.After(choices[j].date) })
	}
	if total > len(choices) {
		extra := ", plus your current branch"
		if trunk.ok {
			extra += " and " + trunk.name
		}
		say("DarkGray", fmt.Sprintf("Showing %d most recently committed branches of %d total%s.", len(choices), total, extra))
	}
	detached := true
	currentOut, _ := git(root, "symbolic-ref", "--quiet", "--short", "HEAD")
	current := strings.TrimSpace(currentOut)
	if current != "" {
		detached = false
	}
	defaultDescription := "HEAD"
	if !detached {
		for i, choice := range choices {
			if choice.current {
				defaultDescription = fmt.Sprintf("%d (your current branch)", i+1)
				break
			}
		}
	}
	base, selected := selectBranch(choices, "Branches available as a base:", defaultDescription, detached, true, true)
	if !selected {
		fmt.Println("Quit.")
		return 0
	}
	name := ""
	if len(pos) > 0 {
		name = pos[0]
	} else {
		fmt.Println("Suggestions: ticket-name, feature/new-feature, fix/broken-thing, note that any spaces will be replaced with hyphens")
		name, _ = prompt("Input the new branch name and press enter, leave blank and press enter to quit")
	}
	name = normalizedBranchName(name)
	if name == "" {
		fmt.Println("Quit.")
		return 0
	}
	if _, c := gitCode(root, "check-ref-format", "--branch", name); c != 0 {
		errBlock(fmt.Sprintf("Error: Invalid Git branch name '%s'. Aborting.", name))
		return 1
	}
	say("DarkGray", "Creating branch: "+displayCommand([]string{"switch", "-c", name, base.ref}))
	if !runGit(root, "switch", "--quiet", "-c", name, base.ref) {
		errBlock(fmt.Sprintf("Error: Failed to create branch '%s'. It may already exist, or '%s' may be invalid. Aborting.", name, base.ref))
		if changesStashed {
			fmt.Println("Your changes remain in the stash, if you want them back then run:")
			say("Magenta", "git stash pop")
		}
		return 1
	}
	say("Green", fmt.Sprintf("Switched to a new branch '%s', based on '%s'.", name, base.ref))
	fmt.Println("\nWhen you're ready to push, run:")
	say("Magenta", "git push -u "+remote+" HEAD")
	fmt.Println("This will upload your branch to GitHub and link it to a remote branch of the same name.")
	return 0
}

func mustCurrent(root string) string {
	out, _ := git(root, "symbolic-ref", "--quiet", "--short", "HEAD")
	return strings.TrimSpace(out)
}

func worktreeBranches(root string) (map[string]bool, bool) {
	out, ok := git(root, "worktree", "list", "--porcelain")
	result := map[string]bool{}
	if !ok {
		return result, false
	}
	for _, line := range nonEmptyLines(out) {
		if strings.HasPrefix(line, "branch refs/heads/") {
			result[strings.TrimPrefix(line, "branch refs/heads/")] = true
		}
	}
	return result, true
}

func runPrune(args []string) int {
	fs, o := flagsFor("prune")
	noFetch := fs.Bool("no-fetch", false, "skip `git fetch --all --prune`")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	root := preflight()
	showTip(*o, "Pruning removes remote-tracking names that no longer exist on the server; deleting merged local branches removes labels, not commits still reachable from trunk.\nThis function protects your current, trunk, unmerged, and worktree branches.")
	if !*noFetch {
		say("DarkGray", "Updating and pruning remote references: "+displayCommand([]string{"-C", root, "fetch", "--all", "--prune"}))
	}
	if !*noFetch && !runGit(root, "fetch", "--all", "--prune") {
		errBlock("Error: failed to fetch and prune remote references. Aborting.")
		fmt.Println("Run ezgit prune --no-fetch to inspect merged branches without fetching.")
		return 1
	}
	trunk := resolveTrunk(root)
	if !trunk.ok {
		errBlock("Error: could not determine the trunk branch. " + trunk.err + " Aborting.")
		return 1
	}
	say("DarkGray", fmt.Sprintf("Finding local branches merged into %s: git for-each-ref --merged=%s refs/heads/", trunk.remoteName, trunk.remoteName))
	out, ok := git(root, "for-each-ref", "--merged="+trunk.remoteName, "--sort=-committerdate",
		"--format=%(refname:short)%00%(committerdate:format:%Y-%m-%d %H:%M:%S)%00%(upstream:short)%00%(upstream:track)", "refs/heads")
	if !ok {
		errBlock("Error: failed to find branches merged into '" + trunk.remoteName + "'. Aborting.")
		return 1
	}
	current := mustCurrent(root)
	inWorktree, ok := worktreeBranches(root)
	if !ok {
		errBlock("Error: failed to inspect Git worktrees. Aborting.")
		return 1
	}
	var names, dates, upstreams, statuses []string
	for _, line := range nonEmptyLines(out) {
		p := strings.SplitN(line, "\x00", 4)
		if len(p) < 4 {
			continue
		}
		name := strings.TrimSpace(p[0])
		if name == trunk.name || name == current || inWorktree[name] {
			continue
		}
		upstream := strings.TrimSpace(p[2])
		if upstream == "" {
			upstream = "-"
		}
		status := "Local only"
		if strings.TrimSpace(p[3]) == "[gone]" {
			status = "Gone"
		} else if strings.TrimSpace(p[2]) != "" {
			status = "Tracked"
		}
		names = append(names, name)
		dates = append(dates, strings.TrimSpace(p[1]))
		upstreams = append(upstreams, upstream)
		statuses = append(statuses, status)
	}
	if len(names) == 0 {
		fmt.Println("No merged local branches are available to clean up.")
		return 0
	}
	rows := make([][]cell, len(names))
	for i := range names {
		color := ""
		if statuses[i] == "Gone" {
			color = "Yellow"
		}
		rows[i] = []cell{{dates[i], "Gray"}, {names[i], ""}, {upstreams[i], ""}, {statuses[i], color}}
	}
	table("Local branches already merged into "+trunk.remoteName+":", []string{"Latest Commit", "Branch Name", "Upstream", "Status"}, rows, true)
	say("DarkGray", "Syntax: separate by spaces for the listed IDs, hyphenate for the IDs in a range, prefix IDs with ^ to exclude them e.g. 1-3 5 ^2")
	answer, got := prompt("Input ID(s) of branches to delete, 'a' for all, or 'q' to quit, and press enter")
	fmt.Println()
	answer = strings.TrimSpace(answer)
	if !got || strings.EqualFold(answer, "q") || answer == "" {
		fmt.Println("Quit.")
		return 0
	}
	ids, reason := resolveSelection(expandAllSelection(answer, len(names)), len(names))
	if reason != "" {
		errBlock("Error: " + reason + " Aborting.")
		return 1
	}
	if len(ids) == 0 {
		fmt.Println("No branches remain after exclusions. Aborting.")
		return 0
	}
	fmt.Println("Branches selected for safe deletion:")
	selected := make([]string, 0, len(ids))
	for _, id := range ids {
		selected = append(selected, names[id-1])
		fmt.Println(" - " + names[id-1])
	}
	confirm, _ := prompt(fmt.Sprintf("Delete these %d local branch(es) with git branch -d? Input 'y' to confirm, or anything else to quit", len(selected)))
	fmt.Println()
	if !strings.EqualFold(strings.TrimSpace(confirm), "y") && !strings.EqualFold(strings.TrimSpace(confirm), "delete") {
		fmt.Println("Quit.")
		return 0
	}
	deleteArgs := append([]string{"branch", "-d", "--"}, selected...)
	say("DarkGray", "Deleting merged local branches: "+displayCommand(append([]string{"-C", root}, deleteArgs...)))
	if !runGit(root, deleteArgs...) {
		errBlock("Error: Git refused to delete one or more selected branches. Aborting.")
		fmt.Println("The branches Git did delete are already gone; review the messages above before trying again.")
		return 1
	}
	say("Green", fmt.Sprintf("Deleted %d merged local branch(es).", len(selected)))
	return 0
}

func localBranches(root string) ([]branchChoice, bool) {
	out, ok := git(root, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname:short)%00%(committerdate:format:%Y-%m-%d %H:%M:%S)%00%(HEAD)", "refs/heads")
	if !ok {
		return nil, false
	}
	var branches []branchChoice
	for _, line := range nonEmptyLines(out) {
		p := strings.SplitN(line, "\x00", 3)
		if len(p) == 3 {
			branches = append(branches, branchChoice{ref: strings.TrimSpace(p[0]), display: strings.TrimSpace(p[0]), kind: "Local", date: parseBranchDate(p[1]), current: strings.TrimSpace(p[2]) == "*"})
		}
	}
	return branches, true
}

func runRename(args []string) int {
	fs, o := flagsFor("rename", "[new-name]")
	pos, code, ok := parseFlagsPos(fs, args, 1)
	if !ok {
		return code
	}
	root := preflight()
	showTip(*o, "Renaming a branch changes its label, not the commits on it.\nThis function updates the local name only; published branches need a separate push if the new name should reach the remote.")
	say("DarkGray", "Listing local branches: git for-each-ref refs/heads/")
	branches, ok := localBranches(root)
	if !ok {
		errBlock("Error: failed to list local branches. Aborting.")
		return 1
	}
	if len(branches) == 0 {
		errBlock("Error: no local branches found. Aborting.")
		return 1
	}
	current := mustCurrent(root)
	detached := current == ""
	for i := range branches {
		branches[i].current = branches[i].display == current
	}
	defaultDescription := "1"
	for i, branch := range branches {
		if branch.current {
			defaultDescription = strconv.Itoa(i + 1)
			break
		}
	}
	old, selected := selectBranch(branches, "Local branches available to rename:", defaultDescription, detached, false, false)
	if !selected {
		fmt.Println("Quit.")
		return 0
	}
	newName := ""
	if len(pos) > 0 {
		newName = pos[0]
	} else {
		newName, _ = prompt(fmt.Sprintf("Input the new branch name for '%s', or leave blank to quit", old.display))
	}
	newName = normalizedBranchName(newName)
	if newName == "" {
		fmt.Println("Quit.")
		return 0
	}
	if _, c := gitCode(root, "check-ref-format", "--branch", newName); c != 0 {
		errBlock(fmt.Sprintf("Error: Invalid Git branch name '%s'. Aborting.", newName))
		return 1
	}
	if newName == old.display {
		errBlock("Error: the new branch name is the same as the current name. Aborting.")
		return 1
	}
	renameArgs := []string{"branch", "-m", old.display, newName}
	renameCommand := displayCommand(append([]string{"-C", root}, renameArgs...))
	say("Yellow", fmt.Sprintf("Ready to rename '%s' to '%s'.", old.display, newName))
	confirm, _ := prompt(fmt.Sprintf("Input 'y' to run %s, or anything else to quit", renameCommand))
	fmt.Println()
	if !strings.EqualFold(strings.TrimSpace(confirm), "y") && !strings.EqualFold(strings.TrimSpace(confirm), "rename") {
		fmt.Println("Quit.")
		return 0
	}
	say("DarkGray", "Renaming branch: "+renameCommand)
	if !runGit(root, renameArgs...) {
		errBlock(fmt.Sprintf("Error: failed to rename '%s' to '%s'. Aborting.", old.display, newName))
		return 1
	}
	say("Green", fmt.Sprintf("Renamed branch '%s' to '%s'.", old.display, newName))
	return 0
}
