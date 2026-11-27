package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type stashRow struct {
	ref, message string
	created      time.Time
	files        int
}
type workChange struct {
	path, orig, state string
	untracked         bool
	modified          time.Time
}

func listStashes(root string) ([]stashRow, bool) {
	say("DarkGray", `Listing stash entries: git stash list --date=format:"%Y-%m-%d %H:%M:%S" --pretty=format:"%cd|%gs"`)
	out, ok := git(root, "stash", "list", "--date=format:%Y-%m-%d %H:%M:%S", "--pretty=format:%gd%x00%cd%x00%gs")
	if !ok {
		return nil, false
	}
	var rows []stashRow
	for _, l := range nonEmptyLines(out) {
		p := strings.SplitN(l, "\x00", 3)
		if len(p) < 3 {
			continue
		}
		t, _ := time.Parse("2006-01-02 15:04:05", p[1])
		count := -1
		if files, good := git(root, "stash", "show", "--name-only", "--include-untracked", p[0]); good {
			count = len(nonEmptyLines(files))
		}
		rows = append(rows, stashRow{p[0], p[2], t, count})
	}
	return rows, true
}

func workingChanges(root string) ([]workChange, bool) {
	say("DarkGray", "Checking working tree: git status --untracked-files=all")
	raw, ok := git(root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if !ok {
		return nil, false
	}
	var out []workChange
	f := strings.Split(raw, "\x00")
	for i := 0; i < len(f); i++ {
		if len(f[i]) < 4 {
			continue
		}
		code, path := f[i][:2], f[i][3:]
		orig := ""
		if strings.ContainsAny(code, "RC") && i+1 < len(f) {
			orig = path
			i++
			path = f[i]
		}
		if path == "" {
			continue
		}
		t := time.Time{}
		if code[1] != 'D' {
			if s, e := os.Stat(filepath.Join(root, filepath.FromSlash(path))); e == nil {
				t = s.ModTime()
			}
		}
		out = append(out, workChange{path, orig, code, code == "??", t})
	}
	return out, true
}

func stashTruncate(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width < 4 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func stashShowChanges(title string, changes []workChange) {
	rows := make([][]cell, len(changes))
	latest := time.Time{}
	for _, change := range changes {
		if change.modified.After(latest) {
			latest = change.modified
		}
	}
	for i, change := range changes {
		modified := ""
		if !change.modified.IsZero() {
			modified = change.modified.Format("2006-01-02 15:04")
		}
		label, color := describe(change.state)
		modifiedColor := "DarkGray"
		if modified != "" && modified == latest.Format("2006-01-02 15:04") {
			modifiedColor = "Cyan"
		}
		dir := filepath.Dir(filepath.FromSlash(change.path))
		if dir == "" {
			dir = "."
		}
		rows[i] = []cell{{modified, modifiedColor}, {label, color}, {filepath.Base(filepath.FromSlash(change.path)), ""}, {dir, ""}}
	}
	table(title, []string{"Modified", "Status", "File Name", "Path"}, rows, true)
}

func pushStash(root string, include, keep bool, message string, paths []string) bool {
	a := []string{"stash", "push"}
	if include {
		a = append(a, "-u")
	}
	if keep {
		a = append(a, "--keep-index")
	}
	if strings.TrimSpace(message) != "" {
		a = append(a, "-m", message)
	}
	if len(paths) > 0 {
		a = append(a, "--")
		a = append(a, paths...)
	}
	say("DarkGray", "Stashing changes: "+displayCommand(a))
	if !runGit(root, a...) {
		return false
	}
	say("Green", "Stash created successfully.")
	return true
}

func stashCreate(root string, include, keep bool, message string, paths []string) bool {
	args := []string{"stash", "push", "-m", message}
	if include {
		args = append(args, "-u")
	}
	if keep {
		args = append(args, "--keep-index")
	}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	say("DarkGray", "Creating stash: "+displayCommand(args))
	if !runGit(root, args...) {
		return false
	}
	say("Green", "Stash created successfully.")
	return true
}

func runStash(args []string) int {
	fs, o := flagsFor("stash")
	push := fs.Bool("push", false, "create a stash without opening the interactive manager")
	inc := fs.Bool("include-untracked", false, "include untracked files")
	keep := fs.Bool("keep-index", false, "leave staged changes in the index")
	msg := fs.String("message", "", "stash message")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	root := preflight()
	if *push {
		if !pushStash(root, *inc, *keep, *msg, nil) {
			errBlock("Error: Failed to create stash. Aborting.")
			return 1
		}
		return 0
	}
	showTip(*o, "Stashing temporarily shelves your uncommitted changes so you can switch context with a clean working tree.\nThis function saves your current changes as a stash, or lets you view, apply, pop, drop, or branch from an existing one.")
	changes, ok := workingChanges(root)
	if !ok {
		errBlock("Error: could not retrieve Git status. Aborting.")
		return 1
	}
	rows, ok := listStashes(root)
	if !ok {
		errBlock("Error: could not list Git stashes. Aborting.")
		return 1
	}
	if len(changes) == 0 && len(rows) == 0 {
		fmt.Println("Working tree is clean and there are no stashes, gitstash has nothing to do. Exiting.")
		return 0
	}
	if len(changes) > 0 {
		tr := make([][]cell, len(changes))
		latest := time.Time{}
		for _, c := range changes {
			if c.modified.After(latest) {
				latest = c.modified
			}
		}
		for i, c := range changes {
			mt := ""
			if !c.modified.IsZero() {
				mt = c.modified.Format("2006-01-02 15:04")
			}
			lab, col := describe(c.state)
			tr[i] = []cell{{mt, func() string {
				if !c.modified.IsZero() && c.modified.Format("2006-01-02 15:04") == latest.Format("2006-01-02 15:04") {
					return "Cyan"
				}
				return "DarkGray"
			}()}, {lab, col}, {filepath.Base(filepath.FromSlash(c.path)), ""}, {filepath.Dir(filepath.FromSlash(c.path)), ""}}
		}
		table(fmt.Sprintf("You have %d uncommitted change(s) in your working tree:", len(changes)), []string{"Modified", "Status", "File Name", "Path"}, tr, true)
	} else {
		fmt.Println("Working tree is clean, no changes to stash.")
	}
	if len(rows) == 0 {
		fmt.Println("No stashes exist yet.")
		a, _ := prompt("Input 'c' to create a stash for your changes, or 'q' to quit")
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "c" {
			return createInteractiveStash(root, changes)
		}
		if a == "q" {
			fmt.Println("Quit.")
			return 0
		}
		errBlock(fmt.Sprintf("Error: Invalid input '%s'. Aborting.", a))
		return 1
	}
	if len(rows) > 0 {
		tr := make([][]cell, len(rows))
		for i, r := range rows {
			d := ""
			if !r.created.IsZero() {
				d = r.created.Format("2006-01-02 15:04")
			}
			fc := "?"
			if r.files >= 0 {
				fc = strconv.Itoa(r.files)
			}
			dateColor := "Gray"
			if i == 0 {
				dateColor = "Cyan"
			}
			tr[i] = []cell{{d, dateColor}, {r.ref, ""}, {fc, ""}, {stashTruncate(r.message, 70), ""}}
		}
		table("Current stash entries:", []string{"Created", "Stash", "Files", "Message"}, tr, true)
	}
	idPrompt := "Input stash ID to manage, [blank] = default: 1, or 'q' to quit, and press enter"
	if len(changes) > 0 {
		say("DarkGray", "Switches: you can input 's' to stash your current changes")
		idPrompt = "Input stash ID to manage, [blank] = default: 1, a switch, or 'q' to quit, and press enter"
	}
	id, _ := prompt(idPrompt)
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "s" && len(changes) > 0 {
		return createInteractiveStash(root, changes)
	}
	if id == "" {
		id = "1"
	}
	if id == "q" {
		fmt.Println("Quit.")
		return 0
	}
	n, e := strconv.Atoi(id)
	if e != nil || n < 1 || n > len(rows) {
		errBlock(fmt.Sprintf("Error: Invalid stash ID '%s'. Aborting.", id))
		return 1
	}
	r := rows[n-1]
	fmt.Println("Selected stash: " + r.ref)
	action, _ := prompt("Input 'f' to show files, 'v' to view patch, 'a' to apply, 'p' to pop (apply and delete), 'd' to drop (delete), 'b' to create a branch, or 'q' to quit, and press enter")
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "q" {
		fmt.Println("Quit.")
		return 0
	}
	switch action {
	case "f":
		cmd := []string{"stash", "show", "--name-only", "--include-untracked", r.ref}
		say("DarkGray", "Showing files: "+displayCommand(cmd))
		if !runGit(root, cmd...) {
			errBlock(fmt.Sprintf("Error: Failed to show files for stash '%s'. Aborting.", r.ref))
			return 1
		}
	case "v":
		cmd := []string{"stash", "show", "-p", r.ref}
		say("DarkGray", "Showing patch: "+displayCommand(cmd))
		if !runGit(root, cmd...) {
			errBlock(fmt.Sprintf("Error: Failed to show patch for stash '%s'. Aborting.", r.ref))
			return 1
		}
	case "a":
		cmd := []string{"stash", "apply", r.ref}
		say("DarkGray", "Applying stash: "+displayCommand(cmd))
		if !runGit(root, cmd...) {
			errBlock(fmt.Sprintf("Error: Failed to apply stash '%s'. Aborting.", r.ref))
			return 1
		}
		say("Green", "Stash applied. The stash itself is still saved, your working tree now also includes these changes.")
	case "p":
		c, _ := prompt(fmt.Sprintf("Pop will apply and remove the stash %s. Input 'y' to continue or 'n' to cancel and press enter", r.ref))
		if !strings.EqualFold(strings.TrimSpace(c), "y") {
			fmt.Println("Cancelled.")
			return 0
		}
		cmd := []string{"stash", "pop", r.ref}
		say("DarkGray", "Popping stash: "+displayCommand(cmd))
		if !runGit(root, cmd...) {
			errBlock(fmt.Sprintf("Error: Failed to pop stash '%s'. Changes may have been applied with conflicts while the stash remains saved. Aborting.", r.ref))
			return 1
		}
		say("Green", "Stash popped and removed from the list. Your working tree now includes these changes.")
	case "d":
		c, _ := prompt(fmt.Sprintf("Drop permanently deletes %s. This cannot be undone. Input 'y' to continue or 'n' to cancel and press enter", r.ref))
		if !strings.EqualFold(strings.TrimSpace(c), "y") {
			fmt.Println("Cancelled.")
			return 0
		}
		cmd := []string{"stash", "drop", r.ref}
		say("DarkGray", "Dropping stash: "+displayCommand(cmd))
		if !runGit(root, cmd...) {
			errBlock(fmt.Sprintf("Error: Failed to drop stash '%s'. Aborting.", r.ref))
			return 1
		}
		say("Green", "Stash dropped. This cannot be undone.")
	case "b":
		name, _ := prompt("Input new branch name and press enter. Leave blank and press enter to cancel")
		name = normalizedBranchName(name)
		if name == "" {
			errBlock("Error: Branch name is required. Aborting.")
			return 1
		}
		if _, code := gitCode(root, "check-ref-format", "--branch", name); code != 0 {
			errBlock(fmt.Sprintf("Error: Invalid branch name '%s'. Aborting.", name))
			return 1
		}
		cmd := []string{"stash", "branch", name, r.ref}
		say("DarkGray", "Creating branch from stash: "+displayCommand(cmd))
		if !runGit(root, cmd...) {
			errBlock(fmt.Sprintf("Error: Failed to create branch from stash '%s'. Aborting.", r.ref))
			return 1
		}
		say("Green", fmt.Sprintf("Branch '%s' created from stash and checked out.", name))
	default:
		errBlock(fmt.Sprintf("Error: Invalid action '%s'. Aborting.", action))
		return 1
	}
	return 0
}

func createInteractiveStash(root string, changes []workChange) int {
	refreshed, ok := workingChanges(root)
	if !ok {
		errBlock("Error: could not retrieve Git status. Aborting.")
		return 1
	}
	changes = refreshed
	if len(changes) == 0 {
		say("Yellow", "No local changes to stash.")
		return 0
	}
	stashShowChanges("Changed files:", changes)
	mode, _ := prompt("Input 'a' to stash all tracked changes, 'u' to stash tracked + untracked, 'k' to stash tracked and keep staged index, or 's' to stash selected files by ID/range, or 'q' to quit and press enter")
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "q" {
		fmt.Println("Quit.")
		return 0
	}
	var paths []string
	include := false
	keep := false
	switch mode {
	case "a":
	case "u":
		include = true
	case "k":
		keep = true
	case "s":
		say("DarkGray", "Syntax: separate by spaces for the listed IDs, hyphenate for the IDs in a range, prefix IDs with ^ to exclude them e.g. 1-3 5 ^2")
		sel, _ := prompt("Input ID(s) of files to stash, and press enter")
		ids, reason := resolveSelection(sel, len(changes))
		if reason != "" {
			errBlock("Error: " + reason + " Aborting.")
			return 1
		}
		for _, id := range ids {
			paths = append(paths, changes[id-1].path)
			if changes[id-1].untracked {
				include = true
			}
		}
		if len(paths) == 0 {
			fmt.Println("No files remain after exclusions. Aborting.")
			return 0
		}
	default:
		errBlock(fmt.Sprintf("Error: Invalid mode '%s'. Aborting.", mode))
		return 1
	}
	msg, _ := prompt("Optional stash message (leave blank for auto message)")
	if strings.TrimSpace(msg) == "" {
		msg = "wip: " + time.Now().Format("2006-01-02 15:04")
	}
	if !stashCreate(root, include, keep, msg, paths) {
		errBlock("Error: Failed to create stash. Aborting.")
		return 1
	}
	return 0
}
