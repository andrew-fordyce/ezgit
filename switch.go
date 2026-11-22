// switch.go - `ezgit switch`: interactive branch switcher with search. Port of Switch-GitBranch.ps1.
//
//	ezgit switch                 the 20 most recently committed-to local branches
//	ezgit switch taxi            local branches matching "taxi" (falls back to the remote if none)
//	ezgit switch "feature/*"     wildcards work too: * ? [abc]
//	ezgit switch 2917 --remote   fetch first, then search the default remote's branches
//
// Remote branches you pick get a local tracking branch created for them. Uncommitted changes can be
// carried over or stashed first, and after switching it reports the upstream status.
package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// branchRef is one row of the branch table.
type branchRef struct {
	name string    // "feature/x" for a local branch, "origin/feature/x" for a remote one
	date time.Time // date of the latest commit
	kind string    // "Local" or "Remote"
}

// psWildcard converts a small PowerShell-style wildcard pattern into a regular expression.
func psWildcard(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("(?i)^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		case '[':
			j := i + 1
			if j < len(pattern) && pattern[j] == '!' {
				j++
			}
			for j < len(pattern) && pattern[j] != ']' {
				j++
			}
			if j < len(pattern) {
				chars := pattern[i+1 : j]
				if strings.HasPrefix(chars, "!") {
					chars = "^" + chars[1:]
				}
				b.WriteByte('[')
				b.WriteString(chars)
				b.WriteByte(']')
				i = j
			} else {
				b.WriteString(regexp.QuoteMeta(string(c)))
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// matchesFilter reports whether a branch name matches the text filter: no filter matches everything,
// a filter containing * or ? is a wildcard pattern, anything else matches anywhere in the name.
func matchesFilter(name, filter string) bool {
	if strings.TrimSpace(filter) == "" {
		return true
	}
	pattern := "*" + filter + "*"
	if strings.ContainsAny(filter, "*?") {
		pattern = filter
	}
	return psWildcard(pattern).MatchString(name)
}

// getRefs lists the branches under refPath (refs/heads/ or refs/remotes/<remote>/), newest commit
// first, keeping those that match the filter. Symbolic refs such as origin/HEAD are ignored. ok is
// false if git failed (an error has been printed), which is different from a valid empty list.
func getRefs(root, refPath, kind, filter string) (refs []branchRef, ok bool) {
	say("DarkGray", "Getting list of "+strings.ToLower(kind)+" branches: git for-each-ref --sort=-committerdate")
	out, code := gitCode(root, "for-each-ref",
		"--format=%(refname:short)\t%(committerdate:format:%Y-%m-%d %H:%M:%S)\t%(symref)",
		"--sort=-committerdate", refPath)
	if code != 0 {
		errBlock(fmt.Sprintf("Error: failed to retrieve %s branches (git exited with code %d). Aborting.", strings.ToLower(kind), code))
		return nil, false
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 3)
		if len(parts) < 3 || parts[2] != "" { // blank line, or a symbolic ref such as origin/HEAD
			continue
		}
		if !matchesFilter(parts[0], filter) {
			continue
		}
		date, err := time.Parse(logDateLayout, parts[1])
		if err != nil {
			date = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		refs = append(refs, branchRef{parts[0], date, kind})
	}
	return refs, true
}

// fetchRemoteBranches updates the local view of a remote and lists its branches.
func fetchRemoteBranches(root, remote, filter string) (refs []branchRef, ok bool) {
	say("DarkGray", fmt.Sprintf("Updating local list of remote branches: git -C %s fetch %s", root, remote))
	if !runGit(root, "fetch", remote) {
		errBlock(fmt.Sprintf("Error: Failed to fetch from %s. Aborting.", remote))
		return nil, false
	}
	return getRefs(root, "refs/remotes/"+remote+"/", "Remote", filter)
}

// arrange sorts branches newest first and, when not searching, keeps only the newest `limit`.
// latest is the newest commit date among all the matches.
func arrange(branches []branchRef, searching bool, limit int) (arranged []branchRef, latest time.Time) {
	for i, b := range branches {
		if i == 0 || b.date.After(latest) {
			latest = b.date
		}
	}
	arranged = append([]branchRef(nil), branches...)
	sort.SliceStable(arranged, func(i, j int) bool { return arranged[i].date.After(arranged[j].date) })
	if !searching && len(arranged) > limit {
		arranged = arranged[:limit]
	}
	return arranged, latest
}

// firstWithDate is the index of the first branch whose commit date is `latest` (the default pick).
func firstWithDate(branches []branchRef, latest time.Time) int {
	for i, b := range branches {
		if b.date.Equal(latest) {
			return i
		}
	}
	return 0
}

// branchTable prints the numbered branch table; the newest commit date is highlighted.
func branchTable(title string, branches []branchRef, latest time.Time) {
	rows := make([][]cell, len(branches))
	for i, b := range branches {
		color := "Gray"
		if b.date.Equal(latest) {
			color = "Cyan"
		}
		rows[i] = []cell{{b.date.Format(logDateLayout), color}, {b.name, ""}, {b.kind, ""}}
	}
	table(title, []string{"Latest Commit", "Branch Name", "Type"}, rows, true)
}

// stripRemote removes a leading "<remote>/" from a remote branch name.
func stripRemote(name, remote string) string {
	if hasPrefixFold(name, remote+"/") {
		return name[len(remote)+1:]
	}
	return name
}

// quoteName wraps a branch name in single quotes (doubling any inside it), PowerShell style.
func quoteName(name string) string {
	return "'" + strings.ReplaceAll(name, "'", "''") + "'"
}

func runSwitch(args []string) int {
	fs, o := flagsFor("switch", "[filter]")
	remoteFlag := fs.Bool("remote", false, "fetch the default remote first and search its branches straight away")
	var maxBranches int
	fs.IntVar(&maxBranches, "n", 20, "most recent branches to show when there is no filter (see --max-branches)")
	fs.IntVar(&maxBranches, "max-branches", 20, "most recent branches to show when there is no filter")
	pos, code, ok := parseFlagsPos(fs, args, 1)
	if !ok {
		return code
	}
	filter := ""
	if len(pos) > 0 {
		filter = pos[0]
	}

	// --- setup
	root := preflight()

	// establish a default remote if one is configured
	remotesOut, good := git(root, "remote")
	if !good {
		errBlock("Error: could not list configured Git remotes. Aborting.")
		return 1
	}
	defaultRemoteName := ""
	if len(nonEmptyLines(remotesOut)) > 0 {
		name, ok := defaultRemote(root)
		if !ok {
			errBlock("Error: could not determine the default remote. Could not list configured Git remotes. Aborting.")
			return 1
		}
		defaultRemoteName = name
	}

	if maxBranches < 1 {
		errBlock("Error: MaxBranches must be at least 1. Aborting.")
		return 1
	}

	// search mode (a filter) or recent mode (no filter); this is updated inside the loop below
	searching := strings.TrimSpace(filter) != ""

	showTip(*o, "Switching branches changes which version of the code you're working on; "+
		"your commits stay safe on their own branch.\nThis function lists your recent or matching "+
		"branches, and switches you to the one you pick.")

	// --- find branches, show them and ask which to switch to; repeats if the filter is changed
	var branches []branchRef
	var defaultIndex int
	var selection string
	for {
		branches = nil
		hasSearchedRemote := false

		// guard the remote if someone is explicitly searching for one
		if *remoteFlag && defaultRemoteName == "" {
			errBlock("Error: No configured remote found. Cannot search remote branches. Aborting.")
			return 1
		}

		if *remoteFlag {
			if searching {
				say("DarkGray", fmt.Sprintf("Searching for remote branches that match '%s'", filter))
			} else {
				say("DarkGray", fmt.Sprintf("No text filter specified, showing %d most recent remote branches", maxBranches))
			}
			refs, ok := fetchRemoteBranches(root, defaultRemoteName, filter)
			if !ok {
				return 1
			}
			branches, hasSearchedRemote = refs, true
		} else {
			if searching {
				say("DarkGray", fmt.Sprintf("Searching for local branches that match '%s'", filter))
			} else {
				say("DarkGray", fmt.Sprintf("No text filter specified, showing %d most recent local branches", maxBranches))
			}
			refs, ok := getRefs(root, "refs/heads/", "Local", filter)
			if !ok {
				return 1
			}
			branches = refs

			// fall back to the remote if a local search found nothing
			if len(branches) == 0 && searching {
				if defaultRemoteName == "" {
					errBlock("Error: No local branches found and no remote is configured, so there is nothing further to search. Aborting.")
					return 1
				}
				fmt.Println()
				fmt.Println("No local branches found, checking remote")
				refs, ok := fetchRemoteBranches(root, defaultRemoteName, filter)
				if !ok {
					return 1
				}
				branches, hasSearchedRemote = refs, true
			}
		}

		if len(branches) == 0 {
			errBlock("Error: No branches found. Aborting.")
			return 1
		}

		// --- show the table
		var latest time.Time
		branches, latest = arrange(branches, searching, maxBranches)
		defaultIndex = firstWithDate(branches, latest)

		sourceText := "Local"
		if hasSearchedRemote {
			sourceText = "Local & remote"
		}
		title := fmt.Sprintf("%d most recent %s branches:", maxBranches, strings.ToLower(sourceText))
		if searching {
			title = fmt.Sprintf("%s branches matching text filter: '%s':", sourceText, filter)
		}
		branchTable(title, branches, latest)

		// --- ask which branch
		hints := []string{}
		if !hasSearchedRemote && defaultRemoteName != "" {
			hints = append(hints, "'r' to search remotes")
		}
		hints = append(hints, "'t' to input a new text filter")
		say("DarkGray", "Switches: you can input "+strings.Join(hints, ", "))
		askBranch := func() string {
			line, gotInput := prompt(fmt.Sprintf("Input ID of branch to switch to, [blank] = default: %d, a switch, or 'q' to quit, and press enter", defaultIndex+1))
			fmt.Println()
			if !gotInput {
				return "q"
			}
			return strings.TrimSpace(line)
		}
		askFilter := func() {
			line, _ := prompt("Input a text filter (matches anywhere in the branch name), [blank] = list all and press enter")
			filter = strings.TrimSpace(line)
			searching = filter != ""
			fmt.Println()
		}
		selection = askBranch()

		if strings.EqualFold(selection, "q") {
			fmt.Println("Quit.")
			fmt.Println()
			return 0
		}
		if strings.EqualFold(selection, "t") {
			askFilter()
			continue
		}

		// --- optional interactive remote re-search
		if strings.EqualFold(selection, "r") && !hasSearchedRemote {
			if defaultRemoteName == "" {
				errBlock("Error: No configured remote found. Cannot search remote branches. Aborting.")
				return 1
			}
			if searching {
				say("DarkGray", fmt.Sprintf("Searching for remote branches that match '%s'", filter))
			} else {
				say("DarkGray", fmt.Sprintf("No text filter specified, showing %d most recent remote branches", maxBranches))
			}
			remoteRefs, ok := fetchRemoteBranches(root, defaultRemoteName, filter)
			if !ok {
				return 1
			}

			// add remote branches to the list, skipping any that already have a local branch shown
			shownLocal := map[string]bool{}
			for _, b := range branches {
				shownLocal[strings.ToLower(b.name)] = true
			}
			for _, r := range remoteRefs {
				if !shownLocal[strings.ToLower(stripRemote(r.name, defaultRemoteName))] {
					branches = append(branches, r)
				}
			}

			// sort and show again
			branches, latest = arrange(branches, searching, maxBranches)
			defaultIndex = firstWithDate(branches, latest)
			title = fmt.Sprintf("%d most recent local & remote branches:", maxBranches)
			if searching {
				title = fmt.Sprintf("Local & remote branches matching text filter: '%s':", filter)
			}
			branchTable(title, branches, latest)

			say("DarkGray", "Switches: you can input 't' to input a new text filter")
			selection = askBranch()
			if strings.EqualFold(selection, "q") {
				fmt.Println("Quit.")
				fmt.Println()
				return 0
			}
			if strings.EqualFold(selection, "t") {
				askFilter()
				continue
			}
		}
		// anything else is a selection to act on
		break
	}

	// --- parse the selection
	idx := defaultIndex
	if selection != "" {
		n, err := strconv.Atoi(selection)
		if !regexp.MustCompile(`^\d+$`).MatchString(selection) {
			errBlock(fmt.Sprintf("Error: Invalid input '%s'. Aborting.", selection))
			return 1
		}
		if err != nil {
			n = 0 // too large to be a valid ID
		}
		idx = n - 1
	}
	if idx < 0 || idx >= len(branches) {
		errBlock(fmt.Sprintf("Error: Invalid branch ID '%s'. Aborting.", selection))
		return 1
	}

	target := branches[idx]
	localName := target.name
	if target.kind == "Remote" {
		localName = stripRemote(target.name, defaultRemoteName)
	}
	say("DarkGray", fmt.Sprintf("Checking if branch exists locally: git -C %s show-ref --verify --quiet 'refs/heads/%s'", root, localName))
	// --quiet makes a missing ref exit 1 instead of a fatal 128, so 1 can mean "not found"
	localExists := false
	switch _, code := gitCode(root, "show-ref", "--verify", "--quiet", "refs/heads/"+localName); code {
	case 0:
		localExists = true
	case 1:
	default:
		errBlock(fmt.Sprintf("Error: Git show-ref failed (exit code %d). Aborting.", code))
		return 1
	}

	// --- uncommitted changes would carry over to the new branch
	changesStashed := false
	say("DarkGray", "Checking for uncommitted changes: git status")
	raw, ok := git(root, "status", "--porcelain=v1", "-z")
	if !ok {
		errBlock("Error: failed to read git status. Aborting.")
		return 1
	}
	if dirty := parseStatus(raw); len(dirty) > 0 {
		fmt.Println()
		say("Yellow", fmt.Sprintf("Warning: You have uncommitted changes in %d file(s) on your current branch:", len(dirty)))
		for _, e := range dirty {
			say("DarkGray", " - "+e.path)
		}
		fmt.Println()
		fmt.Println("If you switch branches, these changes normally remain in your working tree and will carry over to the branch you switch to.")
		fmt.Println("Stashing them first leaves the working tree clean. The changes will remain in the stash until you restore them.")
		line, gotInput := prompt("Input 'c' to carry the changes to the branch you switch to, 's' to stash them before switching, or 'q' to quit, and press enter")
		choice := strings.TrimSpace(line)
		if !gotInput {
			choice = "q"
		}
		fmt.Println()
		switch strings.ToLower(choice) {
		case "q":
			fmt.Println("Quit.")
			fmt.Println()
			return 0
		case "s":
			if !stashPush(root, true, false, "gitswitch auto-stash before branch switch") {
				errBlock("Error: Failed to stash local changes. Aborting.")
				return 1
			}
			changesStashed = true
			fmt.Println("Local changes stashed before switching branch, to restore them later, first make sure you are on the intended branch, and then run:")
			say("Magenta", "git stash pop")
		case "c":
			fmt.Println("Continuing with the changes in your working tree. Git will only allow you to switch branches if these changes can safely carry over.")
			fmt.Println()
		default:
			errBlock(fmt.Sprintf("Error: Invalid input '%s'. Aborting.", choice))
			return 1
		}
	}

	// --- do the switch
	var gitArgs []string
	var shown, description string
	switch {
	case target.kind == "Remote" && !localExists:
		gitArgs = []string{"switch", "-q", "-c", localName, "--track", target.name}
		shown = fmt.Sprintf("git switch -c %s --track %s", quoteName(localName), quoteName(target.name))
		description = "Creating local branch and tracking remote:"
	case target.kind == "Remote":
		gitArgs = []string{"switch", "-q", localName}
		shown = "git switch " + quoteName(localName)
		description = "Remote branch selected; switching to existing local branch:"
	default:
		gitArgs = []string{"switch", "-q", localName}
		shown = "git switch " + quoteName(localName)
		description = "Switching to existing local branch:"
	}
	say("DarkGray", description+" "+shown)

	// if git fails, rely on git's own error message above and just explain what happened to any stash
	if !runGit(root, gitArgs...) {
		fmt.Println()
		say("Red", "Error: git switch failed. Aborting.")
		if changesStashed {
			fmt.Println("Your changes were stashed, to retrieve them, run:")
			say("Magenta", "git stash pop")
		}
		fmt.Println()
		return 1
	}
	// PowerShell also adds the command to up-arrow history; here it is printed above, ready to copy.

	// --- report the switch and check the upstream
	branchOut, _ := git(root, "branch", "--show-current")
	currentBranch := strings.TrimSpace(branchOut)
	upOut, _ := git(root, "rev-parse", "--abbrev-ref", currentBranch+"@{u}")
	upstreamFull := strings.TrimSpace(upOut)
	fmt.Println()
	say("Green", fmt.Sprintf("Switched to branch '%s'.", currentBranch))

	if upstreamFull == "" {
		fmt.Println()
		say("Yellow", "Warning: This branch is not tracking a remote (upstream) branch")
		if defaultRemoteName != "" {
			fmt.Println("To make sure your work is backed up and visible to the team, run:")
			say("Magenta", "git push -u "+defaultRemoteName+" HEAD")
			fmt.Println("This will push your local branch to the remote and configure your local branch to track the remote branch.")
		} else {
			fmt.Println("No remote is configured for this repository, so there is no remote branch to track.")
		}
	} else {
		// fetch first so "up to date" reflects the current remote state
		upstreamRemote := strings.SplitN(upstreamFull, "/", 2)[0]
		say("DarkGray", fmt.Sprintf("Checking current remote status: git -C %s fetch %s", root, upstreamRemote))
		fmt.Println()
		if !runGit(root, "fetch", upstreamRemote, "--quiet") {
			say("Yellow", "Warning: Git fetch failed, so the reported status of the remote branch may be stale.")
		}

		upstreamNameOnly := upstreamFull
		if i := strings.Index(upstreamFull, "/"); i >= 0 {
			upstreamNameOnly = upstreamFull[i+1:]
		}
		if !strings.EqualFold(currentBranch, upstreamNameOnly) {
			say("Yellow", "This branch appears to be tracking a different remote branch.")
			fmt.Println("Local Branch:    " + currentBranch)
			fmt.Println("Upstream Branch: " + upstreamFull)
			fmt.Println("To fix this and track a remote branch with the same name, run:")
			say("Magenta", "git push -u "+upstreamRemote+" HEAD")
			fmt.Println("This will push your local branch to the remote and configure your local branch to track the remote branch.")
			fmt.Println()
		}

		if counts, ok := git(root, "rev-list", "--left-right", "--count", currentBranch+"..."+upstreamFull); ok {
			f := strings.Fields(counts)
			if len(f) >= 2 {
				ahead, _ := strconv.Atoi(f[0])
				behind, _ := strconv.Atoi(f[1])
				switch {
				case ahead == 0 && behind == 0:
					fmt.Printf("This branch is up to date with %s.\n", upstreamFull)
				case ahead > 0 && behind == 0:
					fmt.Printf("This branch is %d commit(s) ahead of %s.\n", ahead, upstreamFull)
				case ahead == 0 && behind > 0:
					fmt.Printf("This branch is %d commit(s) behind %s, to bring your local branch up to date, run:\n", behind, upstreamFull)
					say("Magenta", "git pull")
				default:
					say("Yellow", fmt.Sprintf("This branch has diverged from %s (%d ahead, %d behind).", upstreamFull, ahead, behind))
					fmt.Println("Your local branch and the remote both contain commits. Review the changes before pulling.")
				}
			}
		}
	}

	fmt.Println()
	return 0
}
