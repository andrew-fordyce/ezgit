// undo.go - the safety-first implementation of `ezgit undo`.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type undoItem struct {
	RelPath, TargetPath, FullPath string
	status                        string
	modified                      time.Time
}

// resolveUndoTarget deliberately returns a commit, rather than a ref.  This makes a
// file match compare a topic branch with the point at which it forked.
func resolveUndoTarget(root, ref string) (string, bool) {
	if ref == "HEAD" {
		out, code := gitCode(root, "rev-parse", "HEAD^{commit}")
		return strings.TrimSpace(out), code == 0
	}
	out, code := gitCode(root, "merge-base", "HEAD", ref)
	return strings.TrimSpace(out), code == 0 && strings.TrimSpace(out) != ""
}

func parseUndoDiff(raw string) ([]undoItem, bool) {
	t := strings.Split(raw, "\x00")
	var items []undoItem
	for i := 0; i < len(t); {
		if t[i] == "" {
			i++
			continue
		}
		status := t[i]
		i++
		if i >= len(t) || t[i] == "" {
			return nil, false
		}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if i+1 >= len(t) || t[i] == "" || t[i+1] == "" {
				return nil, false
			}
			items = append(items, undoItem{RelPath: t[i+1], TargetPath: t[i], status: status})
			i += 2
		} else {
			items = append(items, undoItem{RelPath: t[i], status: status})
			i++
		}
	}
	return items, true
}

// changedUndoItems includes staged, unstaged, deleted, renames, and untracked paths.
func changedUndoItems(root, target string) ([]undoItem, bool) {
	raw, ok := git(root, "diff", target, "--name-status", "--find-renames", "-z", "--")
	if !ok {
		return nil, false
	}
	items, good := parseUndoDiff(raw)
	if !good {
		return nil, false
	}
	status, ok := git(root, "status", "--porcelain=v1", "-z")
	if !ok {
		return nil, false
	}
	seen := map[string]bool{}
	for _, x := range items {
		seen[x.RelPath] = true
	}
	for _, x := range parseStatus(status) {
		if x.code == "??" && !seen[x.path] {
			items = append(items, undoItem{RelPath: x.path, status: "??"})
			seen[x.path] = true
		}
	}
	tree, ok := git(root, "ls-tree", "-r", "--name-only", target)
	if !ok {
		return nil, false
	}
	inTree := map[string]bool{}
	for _, p := range nonEmptyLines(tree) {
		inTree[p] = true
	}
	for i := range items {
		if items[i].TargetPath == "" && inTree[items[i].RelPath] {
			items[i].TargetPath = items[i].RelPath
		}
		items[i].FullPath = filepath.Join(root, filepath.FromSlash(items[i].RelPath))
		if info, err := os.Stat(items[i].FullPath); err == nil {
			items[i].modified = info.ModTime()
		}
	}
	return items, true
}

// changedPaths is retained as a small compatibility helper for callers in older code.
func changedPaths(root string) ([]string, bool) {
	items, ok := changedUndoItems(root, "HEAD")
	var out []string
	for _, x := range items {
		out = append(out, x.RelPath)
	}
	return out, ok
}

func selectPath(paths []string, title string) (string, bool) {
	if len(paths) == 0 {
		return "", false
	}
	rows := make([][]cell, len(paths))
	for i, p := range paths {
		rows[i] = []cell{{p, ""}}
	}
	table(title, []string{"Path"}, rows, true)
	a, ok := prompt("Input file ID or 'q' to quit")
	if !ok || strings.EqualFold(strings.TrimSpace(a), "q") {
		return "", false
	}
	n, err := strconv.Atoi(strings.TrimSpace(a))
	if err != nil || n < 1 || n > len(paths) {
		return "", false
	}
	return paths[n-1], true
}

func targetTree(root, ref string) (map[string]bool, bool) {
	out, ok := git(root, "ls-tree", "-r", "--name-only", ref)
	if !ok {
		return nil, false
	}
	m := map[string]bool{}
	for _, p := range nonEmptyLines(out) {
		m[p] = true
	}
	return m, true
}

func undoRunGitSilent(root string, args ...string) bool {
	_, code := gitCode(root, args...)
	return code == 0
}

func restoreUndoItems(root, ref string, items []undoItem, lookup map[string]string) (int, []string) {
	tree, ok := targetTree(root, ref)
	if !ok {
		return 0, []string{"<target tree>"}
	}
	done := 0
	var failed []string
	for _, item := range items {
		target := item.TargetPath
		if target == "" {
			target = item.RelPath
			if lookup != nil && lookup[ref] != "" {
				target = lookup[ref]
			}
		}
		if tree[target] {
			if target != item.RelPath {
				removed := undoRunGitSilent(root, "rm", "-f", "--", item.RelPath)
				_, statErr := os.Stat(item.FullPath)
				if !removed && statErr == nil {
					failed = append(failed, item.RelPath)
					continue
				}
			}
			say("DarkGray", "Restoring target content: "+displayCommand([]string{"restore", "--source=" + ref, "--worktree", "--staged", "--", target}))
			if undoRunGitSilent(root, "restore", "--source="+ref, "--worktree", "--staged", "--", target) {
				done++
			} else {
				failed = append(failed, item.RelPath)
			}
			continue
		}
		say("DarkGray", "Removing path absent at target: "+displayCommand([]string{"rm", "-f", "--", item.RelPath}))
		if undoRunGitSilent(root, "rm", "-f", "--", item.RelPath) {
			done++
			continue
		}
		if err := os.RemoveAll(item.FullPath); err != nil {
			failed = append(failed, item.RelPath)
			continue
		}
		// An untracked path has nothing for `git add -u` to stage.  Its
		// absence is nevertheless the requested target state.
		_ = undoRunGitSilent(root, "add", "-u", "--", item.RelPath)
		done++
	}
	return done, failed
}

func restoreFiles(root, source string, paths []string) int {
	items := make([]undoItem, 0, len(paths))
	for _, p := range paths {
		items = append(items, undoItem{RelPath: p, FullPath: filepath.Join(root, filepath.FromSlash(p))})
	}
	done, failed := restoreUndoItems(root, source, items, nil)
	if len(failed) > 0 {
		errBlock("Error: one or more selected paths could not be restored.")
		return 1
	}
	if done > 0 {
		say("Green", fmt.Sprintf("Restored %d path(s) from %s.", done, source))
	}
	return 0
}

type undoFlowResult struct {
	code int
	mode string
}

func undoItemAction(item undoItem) string {
	if item.TargetPath == "" {
		return "Delete (Untracked)"
	}
	if item.TargetPath != item.RelPath {
		return "Restore (Rename)"
	}
	return "Restore (Undo Your Edit)"
}

func undoFileRows(items []undoItem) [][]cell {
	rows := make([][]cell, len(items))
	for i, item := range items {
		folder := filepath.ToSlash(filepath.Dir(filepath.FromSlash(item.RelPath)))
		if folder == "." || folder == "" {
			folder = "/"
		}
		action := undoItemAction(item)
		color := "Cyan"
		if strings.HasPrefix(action, "Delete") {
			color = "Red"
		}
		rows[i] = []cell{{filepath.Base(filepath.FromSlash(item.RelPath)), ""}, {folder, ""}, {action, color}}
	}
	return rows
}

func runUndoFileMatchFlow(root, target, filter string) undoFlowResult {
	effective, ok := resolveUndoTarget(root, target)
	if !ok {
		errBlock(fmt.Sprintf("Error: could not resolve a comparison point for '%s'.", target))
		return undoFlowResult{code: 1}
	}
	head, _ := git(root, "rev-parse", "HEAD")
	if strings.TrimSpace(head) == effective {
		fmt.Printf("Comparing against HEAD (commit %s), only uncommitted changes will be found.\n", effective)
	} else {
		fmt.Printf("Comparing against the commit where your branch diverged from %s (%s)\n", target, effective)
	}
	items, ok := changedUndoItems(root, effective)
	if !ok {
		errBlock("Error: could not inspect working tree differences.")
		return undoFlowResult{code: 1}
	}
	var candidates []undoItem
	for _, item := range items {
		if matchesFilter(item.RelPath, filter) {
			candidates = append(candidates, item)
		}
	}
	if len(items) == 0 {
		a, got := prompt(fmt.Sprintf("No changes of yours detected vs %s. Input 'c' to switch to Commit mode, 'r' to switch to File Rollback mode, or 'q' to quit, and press enter", target))
		if !got || strings.EqualFold(strings.TrimSpace(a), "q") {
			fmt.Println("Quit.")
			return undoFlowResult{}
		}
		switch strings.ToLower(strings.TrimSpace(a)) {
		case "c":
			fmt.Println("Switching to Commit mode.")
			return undoFlowResult{mode: "Commit"}
		case "r":
			fmt.Println("Switching to File Rollback mode.")
			return undoFlowResult{mode: "FileRollback"}
		default:
			errBlock(fmt.Sprintf("Error: Invalid input '%s'. Aborting.", a))
			return undoFlowResult{code: 1}
		}
	}
	if len(candidates) == 0 {
		fmt.Printf("No files matching text filter: '%s' found.\n", filter)
		return undoFlowResult{}
	}
	defaultID := 1
	for i := 1; i < len(candidates); i++ {
		if candidates[i].modified.After(candidates[defaultID-1].modified) {
			defaultID = i + 1
		}
	}
	table("File changes vs "+target+":", []string{"File Name", "Folder", "Action"}, undoFileRows(candidates), true)
	_, stagedCode := gitCode(root, "diff", "--cached", "--quiet")
	hasStaged := stagedCode == 1
	fmt.Println("Syntax: separate IDs with spaces, use ranges, and prefix exclusions with ^ (for example 1-3 5 ^2).")
	switchText := "Switches: input 'c' for Commit mode, 'r' for File Rollback mode"
	if hasStaged {
		switchText += ", 'u' to only unstage files instead (keeps your edits)"
	}
	fmt.Println(switchText)
	a, got := prompt(fmt.Sprintf("Input ID(s) of files to revert, [blank] = default: %d, 'a' for all, a switch, or 'q' to quit, and press enter", defaultID))
	if !got {
		return undoFlowResult{}
	}
	a = strings.ToLower(strings.TrimSpace(a))
	switch a {
	case "q":
		fmt.Println("Quit.")
		return undoFlowResult{}
	case "c":
		fmt.Println("Switching to Commit mode.")
		return undoFlowResult{mode: "Commit"}
	case "r":
		fmt.Println("Switching to File Rollback mode.")
		return undoFlowResult{mode: "FileRollback"}
	case "u":
		if hasStaged {
			fmt.Println("Handing over to: gitunstage")
			return undoFlowResult{code: runUnstage(nil)}
		}
	}
	if a == "" {
		a = strconv.Itoa(defaultID)
	}
	ids, reason := resolveSelection(expandAllSelection(a, len(candidates)), len(candidates))
	if reason != "" {
		errBlock("Error: " + reason + " Aborting.")
		return undoFlowResult{code: 1}
	}
	if len(ids) == 0 {
		say("Yellow", "No files remain after exclusions. Aborting.")
		return undoFlowResult{}
	}
	selected := make([]undoItem, 0, len(ids))
	for _, id := range ids {
		selected = append(selected, candidates[id-1])
	}
	table("The following files will be reverted:", []string{"File Name", "Folder", "Action"}, undoFileRows(selected), true)
	fmt.Println("Files marked Restore: content will be reverted to before your changes, and staged.")
	fmt.Println("Files marked Delete: removed because they don't exist at the target commit.")
	confirm, _ := prompt("Input 'y' to confirm, or anything else to cancel, and press enter")
	if strings.TrimSpace(confirm) != "y" {
		fmt.Println("Cancelled.")
		return undoFlowResult{}
	}
	done, failed := restoreUndoItems(root, effective, selected, nil)
	if len(failed) > 0 {
		errBlock(fmt.Sprintf("Restore incomplete: %d of %d file(s) completed. Failed file(s): %s", done, len(selected), strings.Join(failed, ", ")))
		return undoFlowResult{code: 1}
	}
	if done == 0 {
		fmt.Println("No files were changed.")
		return undoFlowResult{}
	}
	fmt.Printf("%d of %d file(s) reverted and staged.\n", done, len(selected))
	remaining, _ := git(root, "status", "--porcelain")
	if effective == strings.TrimSpace(head) {
		if len(nonEmptyLines(remaining)) == 0 {
			fmt.Println("Your working tree is now clean and matches your last commit.")
		} else {
			fmt.Printf("%d file(s) still have uncommitted changes. Run gitundo to revert them, or gitcommit to keep them.\n", len(nonEmptyLines(remaining)))
		}
	} else {
		fmt.Println("To finalise these changes, make a commit by running: gitcommit")
	}
	return undoFlowResult{}
}

func runUndoFileMatch(root, target, filter string) int {
	return runUndoFileMatchFlow(root, target, filter).code
}

func historyPathLookup(root, path string, count int) (map[string]string, bool) {
	out, ok := git(root, "log", "--follow", "-z", "-n", strconv.Itoa(count), "--format=C=%h", "--name-status", "--", path)
	if !ok {
		return nil, false
	}
	t := strings.Split(out, "\x00")
	m := map[string]string{}
	var hash string
	for i := 0; i < len(t); i++ {
		if t[i] == "" {
			continue
		}
		if strings.HasPrefix(t[i], "C=") {
			hash = strings.TrimSpace(t[i][2:])
			continue
		}
		if i+1 >= len(t) {
			break
		}
		if strings.HasPrefix(t[i], "R") {
			if i+2 >= len(t) {
				return nil, false
			}
			m[hash] = t[i+2]
			i += 2
		} else {
			m[hash] = t[i+1]
			i++
		}
	}
	return m, true
}

func undoTrackedFiles(root, filter string) ([]string, bool) {
	out, ok := git(root, "ls-files")
	if !ok {
		errBlock("Error: failed to list tracked files. Aborting.")
		return nil, false
	}
	all := nonEmptyLines(out)
	var paths []string
	for _, path := range all {
		if matchesFilter(path, filter) {
			paths = append(paths, path)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		di, dj := filepath.Dir(paths[i]), filepath.Dir(paths[j])
		if di == dj {
			return filepath.Base(paths[i]) < filepath.Base(paths[j])
		}
		return di < dj
	})
	return paths, true
}

func undoRollbackFileRows(paths []string) [][]cell {
	rows := make([][]cell, len(paths))
	for i, path := range paths {
		dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(path)))
		if dir == "" {
			dir = "."
		}
		rows[i] = []cell{{filepath.Base(filepath.FromSlash(path)), ""}, {dir, ""}}
	}
	return rows
}

func runUndoFileRollbackFlow(root, filter, explicit string, max int, forcePick bool) undoFlowResult {
	selectedPath := ""
	if explicit != "" && !forcePick {
		if filepath.IsAbs(explicit) {
			errBlock("Error: --path must be relative to the repository root.")
			return undoFlowResult{code: 1}
		}
		selectedPath = filepath.ToSlash(filepath.Clean(explicit))
		if _, code := gitCode(root, "ls-files", "--error-unmatch", "--", selectedPath); code != 0 {
			errBlock(fmt.Sprintf("Error: tracked path '%s' was not found.", selectedPath))
			return undoFlowResult{code: 1}
		}
	} else {
		all, ok := undoTrackedFiles(root, "")
		if !ok {
			return undoFlowResult{code: 1}
		}
		if len(all) == 0 {
			say("Yellow", "No tracked files found in this repository.")
			return undoFlowResult{}
		}
		if strings.TrimSpace(filter) == "" && len(all) > 100 {
			for {
				term, got := prompt(fmt.Sprintf("%d files found. Input a text filter (matches anywhere in the path), [blank] = list all, or 'q' to quit, and press enter", len(all)))
				if !got || strings.EqualFold(strings.TrimSpace(term), "q") {
					fmt.Println("Quit.")
					return undoFlowResult{}
				}
				term = strings.TrimSpace(term)
				if term == "" {
					break
				}
				matched, _ := undoTrackedFiles(root, term)
				if len(matched) == 0 {
					say("Yellow", fmt.Sprintf("No files match '%s'.", term))
					continue
				}
				filter = term
				break
			}
		}
		for {
			paths, ok := undoTrackedFiles(root, filter)
			if !ok {
				return undoFlowResult{code: 1}
			}
			if len(paths) == 0 {
				fmt.Printf("No files matching text filter: '%s' found.\n", filter)
				return undoFlowResult{}
			}
			title := "Tracked files:"
			if filter != "" {
				title = fmt.Sprintf("Tracked files matching text filter: '%s':", filter)
			}
			table(title, []string{"File Name", "Path"}, undoRollbackFileRows(paths), true)
			fmt.Println("Switches: input 't' for a new text filter, 'm' for File Match mode, 'c' for Commit mode")
			a, got := prompt("Input file ID to browse history, a switch, or 'q' to quit, and press enter")
			if !got {
				return undoFlowResult{}
			}
			a = strings.ToLower(strings.TrimSpace(a))
			switch a {
			case "q":
				fmt.Println("Quit.")
				return undoFlowResult{}
			case "m":
				fmt.Println("Switching to File Match mode.")
				return undoFlowResult{mode: "FileMatch"}
			case "c":
				fmt.Println("Switching to Commit mode.")
				return undoFlowResult{mode: "Commit"}
			case "t":
				term, _ := prompt("Input a text filter (matches anywhere in the path), [blank] = list all, and press enter")
				filter = strings.TrimSpace(term)
				continue
			}
			id, err := strconv.Atoi(a)
			if err != nil || id < 1 || id > len(paths) {
				errBlock(fmt.Sprintf("Error: Invalid file ID '%s'. Aborting.", a))
				return undoFlowResult{code: 1}
			}
			selectedPath = paths[id-1]
			break
		}
	}

	commits, ok := recentCommits(root, max, "History for '"+selectedPath+"':", "", []string{selectedPath})
	if !ok {
		return undoFlowResult{code: 1}
	}
	if len(commits) == 0 {
		say("Yellow", fmt.Sprintf("No commit history found for '%s'. Check the path and try again.", selectedPath))
		return undoFlowResult{}
	}
	lookup, lookupOK := historyPathLookup(root, selectedPath, max)
	if !lookupOK {
		errBlock(fmt.Sprintf("Error: failed to determine historical paths for '%s'. Aborting.", selectedPath))
		return undoFlowResult{code: 1}
	}
	fmt.Println("Switches: input 'm' for File Match mode, 'c' for Commit mode")
	a, got := prompt("Input commit ID to restore this file to, a switch, or 'q' to quit, and press enter")
	if !got {
		return undoFlowResult{}
	}
	a = strings.ToLower(strings.TrimSpace(a))
	switch a {
	case "q":
		fmt.Println("Quit.")
		return undoFlowResult{}
	case "m":
		fmt.Println("Switching to File Match mode.")
		return undoFlowResult{mode: "FileMatch"}
	case "c":
		fmt.Println("Switching to Commit mode.")
		return undoFlowResult{mode: "Commit"}
	}
	id, err := strconv.Atoi(a)
	if err != nil || id < 1 || id > len(commits) {
		errBlock(fmt.Sprintf("Error: Invalid commit ID '%s'. Aborting.", a))
		return undoFlowResult{code: 1}
	}
	chosen := commits[id-1]
	status, ok := git(root, "status", "--porcelain=v1", "-z")
	if !ok {
		errBlock("Error: failed to read git status. Aborting.")
		return undoFlowResult{code: 1}
	}
	for _, e := range parseStatus(status) {
		if e.path == selectedPath {
			say("Yellow", fmt.Sprintf("'%s' has uncommitted changes that will be discarded by this restore.", selectedPath))
			break
		}
	}
	for {
		confirm, _ := prompt(fmt.Sprintf("Input 'y' to restore '%s' to its state at %s (%s), 'd' to preview the diff first, or anything else to cancel, and press enter", selectedPath, chosen.hash, chosen.message))
		if strings.TrimSpace(confirm) == "d" {
			diffPath := selectedPath
			if old := lookup[chosen.hash]; old != "" {
				diffPath = old
			}
			say("DarkGray", "Previewing what this restore would change: "+displayCommand([]string{"diff", chosen.hash, "--", diffPath}))
			_ = runGit(root, "diff", chosen.hash, "--", diffPath)
			continue
		}
		if strings.TrimSpace(confirm) != "y" {
			fmt.Println("Cancelled.")
			return undoFlowResult{}
		}
		break
	}
	item := undoItem{RelPath: selectedPath, FullPath: filepath.Join(root, filepath.FromSlash(selectedPath))}
	done, failed := restoreUndoItems(root, chosen.hash, []undoItem{item}, lookup)
	if done != 1 || len(failed) > 0 {
		errBlock("Error: Restore failed. Check git status for details.")
		return undoFlowResult{code: 1}
	}
	fmt.Printf("'%s' restored to its state at %s and staged. To finalise, run: gitcommit\n", selectedPath, chosen.hash)
	return undoFlowResult{}
}

func runUndoFileRollbackAt(root, filter, explicit string, max int) int {
	return runUndoFileRollbackFlow(root, filter, explicit, max, false).code
}
func runUndoFileRollback(root, filter string, max int) int {
	return runUndoFileRollbackAt(root, filter, "", max)
}

func createBackupBranch(root, target string) (string, bool) {
	branch, _ := git(root, "branch", "--show-current")
	branch = strings.TrimSpace(branch)
	if branch == "" {
		branch = "detached"
	}
	invalid := regexp.MustCompile(`[^a-zA-Z0-9._/-]`)
	branch = invalid.ReplaceAllString(branch, "-")
	now := time.Now()
	stamp := fmt.Sprintf("%s-%03d", now.Format("20060102-150405"), now.Nanosecond()/1_000_000)
	name := fmt.Sprintf("undo-backup/%s-%s", stamp, branch)
	say("DarkGray", "Creating safety backup branch: "+displayCommand([]string{"branch", name}))
	return name, runGit(root, "branch", name, target)
}

func undoStatus(root string) ([]entry, bool) {
	raw, ok := git(root, "status", "--porcelain=v1", "-z")
	if !ok {
		return nil, false
	}
	return parseStatus(raw), true
}

func undoCommitRows(commits []commitInfo) [][]cell {
	rows := make([][]cell, len(commits))
	for i, c := range commits {
		color := "Gray"
		if i == 0 {
			color = "Cyan"
		}
		rows[i] = []cell{{c.date.Format("2006-01-02 15:04"), color}, {c.hash, color}, {truncate(c.author, 20), color}, {truncate(c.message, 80), color}}
	}
	return rows
}

func undoPickCommit(commits []commitInfo, action string) (commitInfo, bool, bool) {
	wording := "reverse"
	if action != "r" {
		wording = "roll the branch back to"
	}
	a, got := prompt(fmt.Sprintf("Input the commit ID to %s, [blank] = default: 1, or 'q' to quit, and press enter", wording))
	if !got || strings.EqualFold(strings.TrimSpace(a), "q") {
		fmt.Println("Quit.")
		return commitInfo{}, false, false
	}
	a = strings.TrimSpace(a)
	if a == "" {
		a = "1"
	}
	id, err := strconv.Atoi(a)
	if err != nil || id < 1 || id > len(commits) {
		errBlock(fmt.Sprintf("Error: Invalid commit ID '%s'. Aborting.", a))
		return commitInfo{}, false, true
	}
	return commits[id-1], true, false
}

func runUndoCommitFlow(root string, max int) undoFlowResult {
	branch, _ := git(root, "branch", "--show-current")
	branch = strings.TrimSpace(branch)
	if branch == "" {
		errBlock("Error: Detached HEAD detected. Commit mode currently expects a branch checkout. Aborting.")
		return undoFlowResult{code: 1}
	}
	commits, ok := recentCommits(root, max, "", "", nil)
	if !ok {
		return undoFlowResult{code: 1}
	}
	if len(commits) == 0 {
		say("Yellow", "No commits found. Aborting.")
		return undoFlowResult{}
	}
	table("Recent commits reachable from '"+branch+"':", []string{"Date", "Hash", "Author", "Message"}, undoCommitRows(commits), true)
	fmt.Println("ID 1 is current HEAD.")
	fmt.Println("Choose how you want to undo your changes:")
	fmt.Println("Revert operates on one selected commit; reset rolls the branch back to a selected point in history.")
	fmt.Println("  r = revert            add a new commit that reverses one selected commit")
	fmt.Println("  s = reset --soft      keep the undone commits' changes staged")
	fmt.Println("  m = reset --mixed     keep the undone commits' changes unstaged")
	fmt.Println("  h = reset --hard      discard the undone commits' changes")
	fmt.Println("Switches: input 'f' for File Match mode or 'l' for File Rollback mode")
	action, got := prompt("Input an undo option, a switch, or 'q' to quit, and press enter")
	if !got {
		return undoFlowResult{}
	}
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "q":
		fmt.Println("Quit.")
		return undoFlowResult{}
	case "f":
		fmt.Println("Switching to File Match mode.")
		return undoFlowResult{mode: "FileMatch"}
	case "l":
		fmt.Println("Switching to File Rollback mode.")
		return undoFlowResult{mode: "FileRollback"}
	}
	if action != "r" && action != "s" && action != "m" && action != "h" {
		errBlock(fmt.Sprintf("Error: Invalid undo option '%s'. Aborting.", action))
		return undoFlowResult{code: 1}
	}
	chosen, picked, bad := undoPickCommit(commits, action)
	if bad {
		return undoFlowResult{code: 1}
	}
	if !picked {
		return undoFlowResult{}
	}
	head, headOK := git(root, "rev-parse", "--short", "HEAD")
	if !headOK {
		errBlock("Error: failed to determine HEAD revision. Aborting.")
		return undoFlowResult{code: 1}
	}
	head = strings.TrimSpace(head)
	if action != "r" && chosen.hash == head {
		errBlock("Error: the selected commit is already HEAD; resetting to HEAD would not change the branch. Aborting.")
		return undoFlowResult{code: 1}
	}
	aheadRaw, _ := git(root, "rev-list", "--count", chosen.hash+"..HEAD")
	ahead := strings.TrimSpace(aheadRaw)
	if action == "r" {
		fmt.Printf("Selected commit to reverse: %s %s\n", chosen.hash, chosen.message)
	} else {
		fmt.Printf("Selected branch rollback target: %s %s\nCommits above target: %s\n", chosen.hash, chosen.message, ahead)
	}

	if action != "r" {
		dirty, statusOK := undoStatus(root)
		if !statusOK {
			errBlock("Error: failed to read git status. Aborting.")
			return undoFlowResult{code: 1}
		}
		if len(dirty) > 0 {
			say("Yellow", "Working tree is not clean. Rewrite modes are blocked to keep this safe.")
			fmt.Println("Commit, stash, or clean your changes first, then rerun: gitundo -Commit")
			return undoFlowResult{code: 1}
		}
		upstream, code := gitCode(root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
		if code == 0 && strings.TrimSpace(upstream) != "" {
			upstream = strings.TrimSpace(upstream)
			countRaw, countCode := gitCode(root, "rev-list", "--count", chosen.hash+".."+upstream)
			count, _ := strconv.Atoi(strings.TrimSpace(countRaw))
			if countCode == 0 && count > 0 {
				say("Red", fmt.Sprintf("Warning: %d commit(s) beyond your target are already on '%s'.", count, upstream))
				fmt.Println("Rewriting history will diverge from the shared remote and require a force-push.")
				answer, _ := prompt("Input 'y' to continue anyway, or anything else to cancel, and press enter")
				if strings.TrimSpace(answer) != "y" {
					fmt.Println("Cancelled.")
					return undoFlowResult{}
				}
			}
		}
	} else {
		dirty, statusOK := undoStatus(root)
		if !statusOK {
			errBlock("Error: failed to read git status. Aborting.")
			return undoFlowResult{code: 1}
		}
		if len(dirty) > 0 {
			say("Yellow", fmt.Sprintf("You have uncommitted changes in %d file(s), and git revert is likely to be blocked:", len(dirty)))
			for _, item := range dirty {
				fmt.Println(" - " + item.path)
			}
			choice, _ := prompt("Input 's' to stash changes and continue with revert, or 'q' to quit, and press enter")
			choice = strings.ToLower(strings.TrimSpace(choice))
			if choice == "q" {
				fmt.Println("Quit.")
				return undoFlowResult{}
			}
			if choice != "s" {
				errBlock(fmt.Sprintf("Error: Invalid input '%s'. Aborting.", choice))
				return undoFlowResult{code: 1}
			}
			if !stashPush(root, true, false, "gitundo auto-stash before revert of "+chosen.hash) {
				return undoFlowResult{code: 1}
			}
			fmt.Println("Local changes stashed. Inspect them before running git stash pop if the reverted commit touched the same files.")
		}
		parents, parentOK := git(root, "show", "-s", "--format=%P", chosen.hash)
		parentList := strings.Fields(parents)
		if !parentOK || len(parentList) == 0 {
			errBlock(fmt.Sprintf("Error: Could not determine parent information for '%s'. Aborting.", chosen.hash))
			return undoFlowResult{code: 1}
		}
		args := []string{"revert", "--no-edit"}
		if len(parentList) > 1 {
			say("Yellow", fmt.Sprintf("Selected commit is a merge commit with %d parents.", len(parentList)))
			for i, parent := range parentList {
				fmt.Printf("  %d = %s\n", i+1, parent)
			}
			mainline, _ := prompt(fmt.Sprintf("Input mainline parent number (1-%d) or 'q' to quit, and press enter", len(parentList)))
			if strings.EqualFold(strings.TrimSpace(mainline), "q") {
				fmt.Println("Quit.")
				return undoFlowResult{}
			}
			n, err := strconv.Atoi(strings.TrimSpace(mainline))
			if err != nil || n < 1 || n > len(parentList) {
				errBlock(fmt.Sprintf("Error: Invalid mainline parent '%s'. Aborting.", mainline))
				return undoFlowResult{code: 1}
			}
			args = append(args, "-m", strconv.Itoa(n))
		}
		args = append(args, chosen.hash)
		say("DarkGray", "Reverting commit with default git message: "+displayCommand(args))
		if !runGit(root, args...) {
			errBlock("Error: Revert failed.")
			unmerged, _ := git(root, "diff", "--name-only", "--diff-filter=U")
			if len(nonEmptyLines(unmerged)) > 0 {
				fmt.Println("Revert produced conflicts. Resolve them and run `git revert --continue`, or cancel with `git revert --abort`.")
			} else {
				fmt.Println("Check git status for details, then retry when ready.")
			}
			return undoFlowResult{code: 1}
		}
		say("Green", "Revert complete. History preserved with a new inverse commit.")
		return undoFlowResult{}
	}

	mode := map[string]string{"s": "soft", "m": "mixed", "h": "hard"}[action]
	if action == "h" {
		say("Red", "WARNING: reset --hard discards local state to match the target commit exactly.")
		phrase, _ := prompt("Type 'hard reset' exactly to continue")
		if phrase != "hard reset" {
			say("Yellow", "Confirmation failed. Aborting.")
			return undoFlowResult{}
		}
	}
	backup, backupOK := createBackupBranch(root, "HEAD")
	if !backupOK {
		errBlock("Error: Failed to create safety branch. Aborting.")
		return undoFlowResult{code: 1}
	}
	args := []string{"reset", "--" + mode, chosen.hash}
	say("DarkGray", "Executing reset: "+displayCommand(args))
	if !runGit(root, args...) {
		errBlock(fmt.Sprintf("Error: %s reset failed. Backup branch retained: %s", strings.ToUpper(mode[:1])+mode[1:], backup))
		fmt.Printf("Recover with: git switch %s\n", backup)
		return undoFlowResult{code: 1}
	}
	say("Green", fmt.Sprintf("Reset complete. Backup branch: %s", backup))
	switch action {
	case "s":
		fmt.Println("Your undone commits are now staged as changes.")
	case "m":
		fmt.Println("Your undone commits are now local unstaged changes.")
	case "h":
		fmt.Printf("If needed, recover with: git switch %s\n", backup)
	}
	return undoFlowResult{}
}

func runUndoCommit(root string, max int) int { return runUndoCommitFlow(root, max).code }

func undoDirtyDecision(root, mode, target string) (string, string, int, bool) {
	if mode == "FileRollback" || (mode == "FileMatch" && target == "HEAD") {
		return mode, target, 0, true
	}
	entries, ok := undoStatus(root)
	if !ok {
		errBlock("Error: failed to read git status. Aborting.")
		return mode, target, 1, false
	}
	if len(entries) == 0 {
		return mode, target, 0, true
	}
	say("Yellow", fmt.Sprintf("Warning: You have uncommitted changes in %d file(s) on your current branch:", len(entries)))
	for _, e := range entries {
		fmt.Println(" - " + e.path)
	}
	description := mode + " mode"
	if mode == "FileMatch" {
		description += " comparing against " + target
	}
	fmt.Println("Continuing could overwrite or ignore these files, so it's probably better to revert them first.")
	a, got := prompt(fmt.Sprintf("Input 'r' to revert your uncommitted changes (recommended), 'c' to continue with gitundo in %s, or 'q' to quit, and press enter", description))
	if !got {
		return mode, target, 0, false
	}
	switch strings.ToLower(strings.TrimSpace(a)) {
	case "q":
		fmt.Println("Quit.")
		return mode, target, 0, false
	case "r":
		fmt.Println("Switching gitundo to File Match mode and targeting HEAD to revert your uncommitted changes.")
		return "FileMatch", "HEAD", 0, true
	case "c":
		fmt.Printf("Continuing with gitundo in %s, ignoring uncommitted changes.\n", description)
		return mode, target, 0, true
	default:
		errBlock(fmt.Sprintf("Error: Invalid input '%s'. Aborting.", a))
		return mode, target, 1, false
	}
}

func runUndo(args []string) int {
	fs, o := flagsFor("undo")
	var fileMatch, fileRollback, commit bool
	var target, filter, path string
	var max int
	for _, name := range []string{"file-match", "FileMatch"} {
		fs.BoolVar(&fileMatch, name, false, "restore selected changed files from a revision")
	}
	for _, name := range []string{"file-rollback", "FileRollback"} {
		fs.BoolVar(&fileRollback, name, false, "restore a tracked file from an older commit")
	}
	for _, name := range []string{"commit", "Commit"} {
		fs.BoolVar(&commit, name, false, "revert or reset to a selected commit")
	}
	for _, name := range []string{"branch-target", "BranchTarget"} {
		fs.StringVar(&target, name, "", "revision used by File Match")
	}
	for _, name := range []string{"text-filter", "TextFilter"} {
		fs.StringVar(&filter, name, "", "filter file paths (PowerShell wildcards supported)")
	}
	for _, name := range []string{"path", "Path"} {
		fs.StringVar(&path, name, "", "path for File Rollback")
	}
	fs.IntVar(&max, "n", 20, "maximum commits to show")
	fs.IntVar(&max, "max-commits", 20, "maximum commits to show")
	fs.IntVar(&max, "MaxCommits", 20, "maximum commits to show")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	count := 0
	for _, picked := range []bool{fileMatch, fileRollback, commit} {
		if picked {
			count++
		}
	}
	if count > 1 {
		return usageError("undo modes are mutually exclusive")
	}
	if max < 1 {
		return usageError("--max-commits must be at least 1")
	}
	root := preflight()
	if target == "" {
		trunk := resolveTrunk(root)
		if !trunk.ok {
			errBlock("Error: could not determine a trunk branch. " + trunk.err + " Specify --branch-target and try again. Aborting.")
			return 1
		}
		target = trunk.remoteName
	}
	mode := "FileMatch"
	if fileRollback {
		mode = "FileRollback"
	}
	if commit {
		mode = "Commit"
	}
	showTip(*o, "gitundo supports File Match, Commit, and File Rollback modes. Restores are staged; resets require a clean tree and create a safety branch.")
	var code int
	var proceed bool
	mode, target, code, proceed = undoDirtyDecision(root, mode, target)
	if !proceed {
		return code
	}
	forceRollbackPick := false
	for {
		var result undoFlowResult
		switch mode {
		case "FileMatch":
			if _, verifyCode := gitCode(root, "rev-parse", "--verify", target+"^{commit}"); verifyCode != 0 {
				errBlock(fmt.Sprintf("Error: Target ref '%s' does not exist. Aborting.", target))
				return 1
			}
			result = runUndoFileMatchFlow(root, target, filter)
		case "Commit":
			result = runUndoCommitFlow(root, max)
		case "FileRollback":
			result = runUndoFileRollbackFlow(root, filter, path, max, forceRollbackPick)
		}
		if result.mode == "" {
			return result.code
		}
		forceRollbackPick = result.mode == "FileRollback"
		mode = result.mode
	}
}
