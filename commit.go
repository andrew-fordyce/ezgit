// commit.go - `ezgit commit`: interactive creation of a commit from staged changes. Port of
// New-GitCommit.ps1.
//
// This command does not stage anything (use `ezgit add` first). Before committing it runs the drift
// check from merge.go, shows the staged changes, and asks for a message. It also supports
//
//	--amend   add the staged changes to your last commit (optionally rewording it)
//	--fixup   make a fixup commit for an earlier commit you pick, then offer to squash it in
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// errBlock prints a red error surrounded by blank lines. errLine is for when a blank line has just
// been printed already (for example after a prompt).
func errBlock(msg string) { fmt.Println(); say("Red", msg); fmt.Println() }
func errLine(msg string)  { say("Red", msg); fmt.Println() }

// ---------- editor ----------

var (
	quotedEditor = regexp.MustCompile(`^\s*"([^"]+)"(?:\s+(.*))?$`)
	whitespace   = regexp.MustCompile(`\s+`)
)

// splitEditor splits a git editor setting such as `"C:\path\code.exe" --wait` or `nano -w` into the
// program and the rest of its arguments.
func splitEditor(spec string) (name, rest string) {
	if m := quotedEditor.FindStringSubmatch(spec); m != nil {
		return m[1], m[2]
	}
	parts := whitespace.Split(strings.TrimSpace(spec), 2)
	if len(parts) > 1 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

// findVim looks for vim on the PATH, then for the copy bundled with Git for Windows, and on other
// systems falls back to plain vi. Returns "" if none is found.
func findVim() string {
	for _, name := range []string{"vim", "vim.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if runtime.GOOS == "windows" {
		if gitExe, err := exec.LookPath("git"); err == nil {
			bundled := filepath.Join(filepath.Dir(filepath.Dir(gitExe)), "usr", "bin", "vim.exe")
			if _, err := os.Stat(bundled); err == nil {
				return bundled
			}
		}
	} else if p, err := exec.LookPath("vi"); err == nil {
		return p
	}
	return ""
}

// editCommitMessage opens an editor on an empty temporary file and returns what was written.
// wantVim chooses Vim; otherwise git's configured editor is used. ok is false if no editor could
// be found or it exited with an error (a message has been printed).
func editCommitMessage(wantVim bool) (message string, ok bool) {
	kind := "Default"
	if wantVim {
		kind = "Vim"
	}
	editorSpec, editorPath := "", ""
	var editorArgs []string

	if !wantVim {
		out, _ := git("", "var", "GIT_EDITOR")
		editorSpec = strings.TrimSpace(out)
		if editorSpec == "" {
			editorSpec = "vi"
			if runtime.GOOS == "windows" {
				editorSpec = "notepad.exe"
			}
		}
		name, rest := splitEditor(editorSpec)
		editorArgs = strings.Fields(rest)

		switch strings.ToLower(name) {
		case "vi", "vi.exe", "vim", "vim.exe":
			wantVim, kind = true, "Vim"
		default:
			if info, err := os.Stat(name); err == nil && !info.IsDir() {
				editorPath, _ = filepath.Abs(name)
			} else if p, err := exec.LookPath(name); err == nil {
				editorPath = p
			}
		}
	}
	if wantVim {
		editorPath = findVim()
	}

	if editorPath == "" {
		fmt.Println()
		if kind == "Default" {
			say("Red", "Error: the configured default editor could not be found. Aborting.")
			fmt.Println("Configure Git with: git config --global core.editor <editor-command>")
		} else {
			say("Red", "Error: Vim was not found. Aborting.")
			fmt.Println("Install Vim or Git for Windows, which normally includes Vim.")
		}
		fmt.Println()
		return "", false
	}

	tmp, err := os.CreateTemp("", "nonsense-commit-*.txt")
	if err != nil {
		errBlock("Error: could not create a temporary file for the commit message. Aborting.")
		return "", false
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	var args []string
	if wantVim {
		args = []string{"--", tmpPath}
	} else {
		args = append(editorArgs, tmpPath)
	}
	label := "Vim"
	if !wantVim {
		label = "the default editor (" + editorSpec + ")"
	}
	// PowerShell also adds this command to up-arrow history; here it is just printed, ready to copy.
	say("DarkGray", fmt.Sprintf("Opening commit message in %s: \"%s\" %s", label, editorPath, strings.Join(args, " ")))

	cmd := exec.Command(editorPath, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		errBlock(fmt.Sprintf("Error: %s exited with code %d. Aborting.", kind, code))
		return "", false
	}
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		errBlock("Error: could not read the commit message back from the editor. Aborting.")
		return "", false
	}
	return string(data), true
}

// quoteForDisplay wraps an argument in double quotes when it contains whitespace or quotes, so a
// printed command can be copied and re-run.
func quoteForDisplay(a string) string {
	if strings.ContainsAny(a, " \t\r\n\"") {
		return `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
	}
	return a
}

// ---------- staged changes table ----------

// stagedChangesTable prints the staged files (index vs HEAD) with line counts. ok is false if git
// failed or the list came back empty (an error has been printed).
func stagedChangesTable(root, title string) (ok bool) {
	say("DarkGray", "Retrieving staged change details: git diff --cached --name-status")
	diffOut, good := git(root, "-c", "core.quotepath=off", "diff", "--cached", "--name-status")
	if !good {
		errBlock("Error: could not retrieve staged change details. Aborting.")
		return false
	}

	// line-change volume for the +/- columns (staged changes = index vs HEAD)
	say("DarkGray", "Retrieving staged line-change volume: git diff --cached --numstat")
	numOut, _ := git(root, "-c", "core.quotepath=off", "diff", "--cached", "--numstat")
	stats := parseNumstat(numOut)

	type row struct {
		modified time.Time
		code     string
		fileName string
		dir      string
		stat     numstat
	}
	var rows []row
	for _, line := range strings.Split(diffOut, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		code, current := parts[0], parts[len(parts)-1]
		var modified time.Time
		if !strings.HasPrefix(code, "D") {
			if info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(current))); err == nil {
				modified = info.ModTime()
			}
		}
		rows = append(rows, row{modified, code, path.Base(current), path.Dir(current), stats[current]})
	}
	if len(rows) == 0 {
		errBlock("Error: git reported staged files but none could be read from the index. Aborting.")
		return false
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := strings.ToLower(rows[i].dir), strings.ToLower(rows[j].dir)
		if a != b {
			return a < b
		}
		return strings.ToLower(rows[i].fileName) < strings.ToLower(rows[j].fileName)
	})

	latest := rows[0].modified
	for _, r := range rows {
		if r.modified.After(latest) {
			latest = r.modified
		}
	}
	latestStamp := latest.Format(timeLayout)

	cells := make([][]cell, len(rows))
	for i, r := range rows {
		label, color := nameStatus(r.code)
		stamp, stampColor := "", "DarkGray"
		if !r.modified.IsZero() {
			stamp = r.modified.Format(timeLayout)
			if stamp == latestStamp {
				stampColor = "Cyan"
			}
		}
		plus, minus := strconv.Itoa(r.stat.insertions), strconv.Itoa(r.stat.deletions)
		if r.stat.binary {
			plus, minus = "-", "-"
		}
		cells[i] = []cell{{label, color}, {stamp, stampColor}, {r.fileName, ""}, {plus, "DarkGray"}, {minus, "DarkGray"}, {r.dir, ""}}
	}
	table(title, []string{"Status", "Modified", "File Name", "+", "-", "Path"}, cells, true)
	fmt.Println()
	return true
}

// ---------- fixup target ----------

type fixupTarget struct {
	hash, subject string
	pushed        bool
	upstream      string
	canSquash     bool
	blockReason   string
	rebaseBase    []string // what to rebase onto: `--root`, or `<hash>~1`
}

// pickerLevel is one list of commits the fixup picker can show; 's' at the prompt moves to the next.
type pickerLevel struct {
	title, label, revRange string
	paths                  []string
	count                  int
}

// countCommits runs `git rev-list --count ...` and returns the number (0 if git failed).
func countCommits(root string, args ...string) (n int, ok bool) {
	out, ok := git(root, append([]string{"rev-list", "--count"}, args...)...)
	n, _ = strconv.Atoi(strings.TrimSpace(out))
	return n, ok
}

// chooseFixupTarget works out which lists of commits to offer, lets the person pick the commit
// their staged changes belong to, and checks whether it can be squashed safely. Levels, narrowest
// first (a level is left out if it wouldn't show anything new):
//  1. commits that touched the staged files (within this branch's commits, if it has any)
//  2. every commit on this branch that isn't on the trunk yet
//  3. those branch commits plus the previous `fixupCount` commits
//  4. the full repository history
func chooseFixupTarget(root string, stagedCount, fixupCount int) (t fixupTarget, code int, ok bool) {
	trunk := resolveTrunk(root)

	branchRange, branchCount := "", 0
	if trunk.ok {
		if n, good := countCommits(root, trunk.remoteName+"..HEAD"); good && n > 0 {
			branchRange, branchCount = trunk.remoteName+"..HEAD", n
		}
	}

	totalCount, good := countCommits(root, "HEAD")
	if !good {
		errBlock("Error: could not determine repository history. Aborting.")
		return t, 1, false
	}
	expandedCount := min(totalCount, branchCount+fixupCount)

	// --no-renames lists both the old and new path of a rename, so history under either name counts
	var stagedPaths []string
	if stagedCount > 0 {
		if out, good := git(root, "-c", "core.quotepath=off", "diff", "--cached", "--name-only", "--no-renames"); good {
			stagedPaths = nonEmptyLines(out)
		}
	}

	var levels []pickerLevel

	// level 1: commits that touched the staged files
	if len(stagedPaths) > 0 {
		touchedRange := "HEAD"
		if branchRange != "" {
			touchedRange = branchRange
		}
		args := append([]string{touchedRange, "--"}, stagedPaths...)
		touched, _ := countCommits(root, args...)

		// without a branch-specific range, don't let this level grow past the initial recent-history view
		shown, nextSize := touched, branchCount
		if branchRange == "" {
			shown, nextSize = min(touched, fixupCount), expandedCount
		}
		if shown > 0 && shown < nextSize {
			title := "Recent commits that touched your staged files:"
			if branchRange != "" {
				title = "Commits on this branch that touched your staged files:"
			}
			levels = append(levels, pickerLevel{title, "commits that touched your staged files", branchRange, stagedPaths, shown})
		}
	}
	// level 2: every commit on this branch
	if branchRange != "" && branchCount > 0 {
		levels = append(levels, pickerLevel{
			"Commits on this branch, not yet on " + trunk.remoteName + ":", "all commits on this branch", branchRange, nil, branchCount})
	}
	// level 3: branch commits plus a chunk of older history (only if it shows more than level 2)
	if branchRange != "" && expandedCount > branchCount {
		levels = append(levels, pickerLevel{"Recent commits, including older history:", "older commits as well", "", nil, expandedCount})
	}
	// level 4: the full history (based on totalCount, not expandedCount)
	if totalCount > 0 && (len(levels) == 0 || levels[len(levels)-1].count < totalCount) {
		levels = append(levels, pickerLevel{"Full repository history:", "the full repository history", "", nil, totalCount})
	}

	if len(levels) == 0 {
		errBlock("Error: there are no commits to fix up yet. Aborting.")
		return t, 1, false
	}

	// pick a target, starting at the narrowest useful level
	levelIndex := 0
	var commits []commitInfo
	pick := ""
	for {
		level := levels[levelIndex]
		var good bool
		commits, good = recentCommits(root, level.count, level.title, level.revRange, level.paths)
		if !good {
			return t, 1, false
		}
		if len(commits) == 0 {
			errBlock("Error: there are no commits to fix up yet. Aborting.")
			return t, 1, false
		}

		isWidest := levelIndex >= len(levels)-1
		widen := ""
		if !isWidest {
			widen = ", 's' to show " + levels[levelIndex+1].label
		}
		line, gotInput := prompt("\nInput ID of the commit your staged changes belong to" + widen + ", or 'q' to quit, and press enter")
		pick = strings.TrimSpace(line)
		if !gotInput {
			pick = "q"
		}
		fmt.Println()

		if strings.EqualFold(pick, "q") {
			fmt.Println("Quit.")
			fmt.Println()
			return t, 0, false
		}
		if strings.EqualFold(pick, "s") && !isWidest {
			levelIndex++
			continue
		}
		break
	}

	id, err := strconv.Atoi(pick)
	if !regexp.MustCompile(`^\d+$`).MatchString(pick) || err != nil {
		errLine(fmt.Sprintf("Error: Invalid input '%s'. Input a single commit ID. Aborting.", pick))
		return t, 1, false
	}
	if id < 1 || id > len(commits) {
		errLine(fmt.Sprintf("Error: ID '%d' is out of range (1-%d). Aborting.", id, len(commits)))
		return t, 1, false
	}
	t.hash, t.subject = commits[id-1].hash, commits[id-1].message

	// has the target already been pushed? only matters for the squash, not the fixup commit itself
	pushedOK, pushed, upstream := commitPushed(root, t.hash)
	if !pushedOK {
		errLine(fmt.Sprintf("Error: could not check whether commit %s has been pushed. Aborting.", t.hash))
		return t, 1, false
	}
	t.pushed, t.upstream = pushed, upstream

	// parent count: 0 = root commit (use rebase --root), 1 = normal, >1 = merge commit
	parentLine, good := git(root, "rev-list", "--parents", "-n", "1", t.hash)
	if !good || strings.TrimSpace(parentLine) == "" {
		errLine(fmt.Sprintf("Error: could not inspect commit %s. Aborting.", t.hash))
		return t, 1, false
	}
	parentCount := len(strings.Fields(parentLine)) - 1

	// any merge between the target and HEAD would be flattened by the rebase
	mergesOut, good := git(root, "rev-list", "--merges", t.hash+"..HEAD")
	if !good {
		errLine(fmt.Sprintf("Error: could not inspect the commits after %s. Aborting.", t.hash))
		return t, 1, false
	}

	t.canSquash = true
	switch {
	case parentCount > 1:
		t.canSquash, t.blockReason = false, fmt.Sprintf("commit %s is a merge commit", t.hash)
	case len(nonEmptyLines(mergesOut)) > 0:
		t.canSquash, t.blockReason = false, fmt.Sprintf("there is a merge commit between %s and your latest commit", t.hash)
	}
	if parentCount == 0 {
		t.rebaseBase = []string{"--root"}
	} else {
		t.rebaseBase = []string{t.hash + "~1"}
	}
	return t, 0, true
}

// ---------- after the commit: squash and push guidance ----------

// afterCommit offers to squash a fixup commit into its target and tells the person what to do next
// to share the work (push, force-push, set an upstream, add a remote).
func afterCommit(root string, amend, fixup, wasPushed bool, target fixupTarget) int {
	doneMsg := "Commit made locally"
	if amend {
		doneMsg = "Commit amended"
	} else if fixup {
		doneMsg = "Fixup commit made locally"
	}
	fixupSquashed := false

	if fixup {
		rebaseCmd := "git rebase -i --autosquash --autostash " + strings.Join(target.rebaseBase, " ")

		fmt.Println()
		if !target.canSquash {
			say("Yellow", fmt.Sprintf("This fixup can't be squashed automatically because %s.", target.blockReason))
			fmt.Println("It stays as a separate commit. To squash it yourself later, run:")
			say("Magenta", rebaseCmd)
			fmt.Println()
		} else {
			if target.pushed {
				say("Yellow", fmt.Sprintf("Warning: commit %s has already been pushed to %s.", target.hash, target.upstream))
				fmt.Println("Squashing rewrites it and every commit after it, so you will need to force push afterwards.")
				fmt.Println()
			}

			line, gotInput := prompt(fmt.Sprintf("Input 's' to squash the fixup into %s now (runs a rebase), or 'q' to leave it as a separate commit", target.hash))
			choice := strings.TrimSpace(line)
			if !gotInput {
				choice = "q"
			}
			fmt.Println()

			switch {
			case strings.EqualFold(choice, "s"):
				say("DarkGray", "Squashing fixup: "+rebaseCmd)
				// 'true' accepts the autosquashed todo list as-is instead of opening an editor
				args := append([]string{"rebase", "-i", "--autosquash", "--autostash"}, target.rebaseBase...)
				if !runGitEnv(root, []string{"GIT_SEQUENCE_EDITOR=true"}, args...) {
					fmt.Println()
					say("Red", "Error: the rebase did not complete. Your fixup commit has been made, but is not yet squashed.")
					fmt.Println("If git stopped on conflicts, resolve them, stage the files, then run:")
					say("Magenta", "git rebase --continue")
					fmt.Println("To put everything back as it was before the rebase, run:")
					say("Magenta", "git rebase --abort")
					fmt.Println()
					return 1
				}
				fixupSquashed = true
				doneMsg = "Fixup squashed"
			case strings.EqualFold(choice, "q"):
				fmt.Println("Left as a separate fixup commit.")
				fmt.Println()
			default:
				errLine(fmt.Sprintf("Error: Invalid input '%s'. The fixup commit was made but not squashed.", choice))
				return 1
			}
		}
	}

	branchOut, code := gitCode(root, "branch", "--show-current")
	if code != 0 {
		fmt.Println()
		say("Yellow", doneMsg+", but the current branch could not be determined, so push guidance was skipped.")
		fmt.Println()
		return 0
	}
	currentBranch := strings.TrimSpace(branchOut)
	if currentBranch == "" {
		fmt.Println()
		say("Yellow", doneMsg+". Warning: you are in a detached HEAD state, so this commit is not on any branch.")
		fmt.Println()
		fmt.Println("To keep it, create a branch from here by running:")
		say("Magenta", "gitbranch")
		fmt.Println()
		return 0
	}

	// rewriting commits that are already on the remote needs a force push
	if (amend && wasPushed) || (fixupSquashed && target.pushed) {
		fmt.Println()
		say("Green", doneMsg+".")
		fmt.Println()
		fmt.Println("The original commit was already on the remote, so a normal push will be rejected. To update it, run:")
		say("Magenta", "git push --force-with-lease")
		fmt.Println()
		fmt.Println("Unlike --force, this refuses to overwrite the remote if someone else has pushed to the branch since you last fetched.")
		fmt.Println()
		return 0
	}

	// no upstream is a normal state (non-zero exit), so it is not treated as an error
	upOut, upCode := gitCode(root, "rev-parse", "--abbrev-ref", currentBranch+"@{u}")
	upstreamFull := strings.TrimSpace(upOut)
	if upCode != 0 {
		upstreamFull = ""
	}

	if upstreamFull == "" {
		remotesOut, ok := git(root, "remote")
		if !ok {
			fmt.Println()
			say("Yellow", doneMsg+", but the list of remotes could not be read, so push guidance was skipped.")
			fmt.Println()
			return 0
		}
		if len(nonEmptyLines(remotesOut)) == 0 {
			fmt.Println()
			say("Yellow", doneMsg+". Warning: This repository does not have a remote yet.")
			fmt.Println()
			fmt.Println("Create an empty repository in GitHub or your team's Git service, copy its URL, then run:")
			say("Magenta", "git remote add origin <repository-url>")
			say("Magenta", "git push -u origin HEAD")
			fmt.Println()
			fmt.Println("The first command links this local repository to the remote; the second uploads your branch and sets its upstream.")
		} else {
			remoteName, ok := defaultRemote(root)
			if !ok {
				errBlock("Error: could not determine the default remote. Could not list configured Git remotes. Aborting.")
				return 1
			}
			fmt.Println()
			say("Yellow", doneMsg+". Warning: this branch is not tracking a remote (upstream) branch on GitHub.")
			fmt.Println()
			fmt.Println("To make sure your work is backed up and visible to the team, run:")
			say("Magenta", "git push -u "+remoteName+" HEAD")
			fmt.Println()
			fmt.Println("This will upload your code to GitHub and link your local branch to a remote branch.")
		}
	} else {
		upstreamNameOnly := upstreamFull
		if i := strings.Index(upstreamFull, "/"); i >= 0 {
			upstreamNameOnly = upstreamFull[i+1:]
		}
		if !strings.EqualFold(currentBranch, upstreamNameOnly) {
			upstreamRemote := strings.SplitN(upstreamFull, "/", 2)[0]
			fmt.Println()
			say("Yellow", doneMsg+". Warning: this branch appears to be tracking a different remote branch.")
			fmt.Println("Local Branch:    " + currentBranch)
			fmt.Println("Upstream Branch: " + upstreamFull)
			fmt.Println()
			fmt.Println("To track a remote branch with the same name, run:")
			say("Magenta", "git push -u "+upstreamRemote+" HEAD")
			fmt.Println()
			fmt.Println("This will upload your code to GitHub and link your local branch to a remote branch with the same name.")
		} else {
			fmt.Println()
			say("Green", doneMsg+".")
			fmt.Println()
			fmt.Println("To push changes to the remote repo, run:")
			say("Magenta", "git push")
		}
	}
	fmt.Println()
	return 0
}

// ---------- the command ----------

func usageError(msg string) int {
	fmt.Fprintln(os.Stderr, "ezgit commit: "+msg)
	return 2
}

func runCommit(args []string) int {
	fs, o := flagsFor("commit")
	fetch := fs.Bool("fetch", false, "run `git fetch --all` before the drift check")
	branchTarget := fs.String("branch-target", "", "branch to check for drift (default: your upstream, then the trunk branch)")
	amendFlag := fs.Bool("amend", false, "add the staged changes to your last commit (optionally rewording it)")
	fixupFlag := fs.Bool("fixup", false, "make a fixup commit for an earlier commit you pick, then offer to squash it in")
	var maxCommits int
	fs.IntVar(&maxCommits, "n", 20, "commits to show (see --max-commits)")
	fs.IntVar(&maxCommits, "max-commits", 20, "table size when nothing is staged (default 5); with --fixup, how many older commits to add when widening (default 20)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	maxSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "n" || f.Name == "max-commits" {
			maxSet = true
		}
	})
	switch {
	case *amendFlag && *fixupFlag:
		return usageError("--amend and --fixup can't be used together")
	case (*amendFlag || *fixupFlag) && (*fetch || *branchTarget != ""):
		return usageError("--fetch and --branch-target only apply to a normal commit (the drift check is skipped when amending or fixing up)")
	case maxCommits < 1 || maxCommits > 200:
		return usageError("--max-commits must be between 1 and 200")
	}
	amend, fixup := *amendFlag, *fixupFlag

	// --max-commits sets the table size in the nothing-staged view, and how many older commits are
	// added when widening the fixup picker before its final "all commits" level
	recentCount := 5
	if maxSet {
		recentCount = maxCommits
	}
	fixupCount := maxCommits

	// 1. preflight and tip
	root := preflight()
	showTip(*o, "A commit is a saved snapshot of your staged changes, with a message describing what and why.\n"+
		"This function creates a commit from changes that are already staged.\n"+
		"Use gitadd first if you need to choose files to stage.\n"+
		"Use -Amend to add changes to your last commit, or -Fixup to add them to an older one.")

	// 2. check for staged changes
	say("DarkGray", "Checking working tree status: git status")
	raw, ok := git(root, "status", "--porcelain=v1", "-z")
	if !ok {
		errBlock("Error: could not inspect working tree status. Aborting.")
		return 1
	}
	entries := parseStatus(raw)
	stagedCount := 0
	for _, e := range entries {
		if len(e.code) >= 2 && !strings.ContainsRune(" ?!", rune(e.code[0])) {
			stagedCount++
		}
	}

	amendOffered, fixupReword := false, false

	if stagedCount == 0 && !amend {
		unstaged := 0
		for _, e := range entries {
			if len(e.code) >= 2 && e.code[1] != ' ' {
				unstaged++
			}
		}

		fmt.Println()
		if unstaged > 0 {
			fmt.Printf("Nothing is staged for commit, but you have %d unstaged file(s). To stage changes, run:\n", unstaged)
			say("Magenta", "gitadd")
			fmt.Println()
			fmt.Println("Then to commit staged changes, run:")
			say("Magenta", "gitcommit")
			fmt.Println()
			return 0
		}

		fmt.Println("Nothing is staged for commit, and no unstaged changes were found.")

		// --fixup folds staged changes into an earlier commit, so there is nothing to offer without them
		if fixup {
			fmt.Println()
			return 0
		}

		fmt.Println()
		recent, good := recentCommits(root, recentCount, "Your recent commits:", "", nil)
		if !good || len(recent) == 0 {
			fmt.Println()
			return 0
		}
		fmt.Println()

		line, gotInput := prompt("Input 'a' to amend the comment of your latest commit, 'f' to fixup an older commit, or 'q' to quit, and press enter")
		offer := strings.TrimSpace(line)
		if !gotInput {
			offer = "q"
		}
		switch {
		case strings.EqualFold(offer, "a"):
			amend, amendOffered = true, true
		case strings.EqualFold(offer, "f"):
			fixup, fixupReword = true, true
		case strings.EqualFold(offer, "q"):
			fmt.Println()
			fmt.Println("Quit.")
			fmt.Println()
			return 0
		default:
			errBlock(fmt.Sprintf("Error: Invalid input '%s'. Aborting.", offer))
			return 1
		}
	}

	// 3. amend preflight
	wasPushed := false
	if amend {
		if _, ok := git(root, "rev-parse", "--verify", "HEAD"); !ok {
			errBlock("Error: there is no commit to amend yet. Aborting.")
			return 1
		}

		say("DarkGray", "Retrieving the commit to amend: git log -1 --format=\"%h  %s\"")
		lastOut, good := git(root, "log", "-1", "--format=%h  %s")
		lastCommit := strings.TrimRight(lastOut, "\r\n")
		if !good || lastCommit == "" {
			errBlock("Error: could not read the commit to amend. Aborting.")
			return 1
		}

		// no upstream is a normal state (non-zero exit), so it is not treated as an error
		upOut, upCode := gitCode(root, "rev-parse", "--abbrev-ref", "@{u}")
		upstreamRef := strings.TrimSpace(upOut)
		if upCode != 0 {
			upstreamRef = ""
		}
		if upstreamRef != "" {
			// exit 0 = HEAD is already on the upstream, 1 = it is not, anything else = a real error
			switch _, code := gitCode(root, "merge-base", "--is-ancestor", "HEAD", upstreamRef); code {
			case 0:
				wasPushed = true
			case 1:
			default:
				errBlock("Error: could not check whether your last commit has been pushed. Aborting.")
				return 1
			}
		}

		if !amendOffered {
			fmt.Println()
			fmt.Println("You are about to amend your most recent commit:")
			say("Cyan", "  "+lastCommit)
			fmt.Println()
		}

		if wasPushed {
			say("Yellow", fmt.Sprintf("Warning: this commit has already been pushed to %s.", upstreamRef))
			fmt.Println("Amending rewrites it, so you will need to force push afterwards. If anyone else")
			fmt.Println("has pulled this branch, amending will cause problems for them.")
			fmt.Println()
			line, _ := prompt("Type 'y' to amend anyway, or press enter to cancel")
			if confirm := strings.ToLower(strings.TrimSpace(line)); confirm != "y" && confirm != "yes" {
				fmt.Println()
				fmt.Println("Cancelled. Nothing was changed.")
				fmt.Println()
				return 0
			}
			fmt.Println()
		}
	}

	// 4. drift check (skipped when amending or fixing up)
	if !amend && !fixup {
		echo := "ezgit merge"
		if *fetch {
			echo += " --fetch"
		}
		if *branchTarget != "" {
			echo += " --branch-target " + *branchTarget
		}
		say("DarkGray", "Checking for drift: "+echo)
		switch resolveMerge(*o, root, *fetch, *branchTarget, true) {
		case mergeQuit:
			return 0
		case mergeConflict, mergeBlocked, mergeError:
			return 1
		}
	}

	// 5. show staged changes
	if stagedCount > 0 {
		title := "Staged changes ready to commit:"
		if amend {
			title = "Staged changes to add to your last commit:"
		} else if fixup {
			title = "Staged changes to fix up into an earlier commit:"
		}
		if !stagedChangesTable(root, title) {
			return 1
		}
	} else {
		if fixup {
			fmt.Println()
			fmt.Println("Nothing is staged, so the fixup will only reword the commit message of the commit that you pick.")
		} else {
			fmt.Println("Nothing is staged, so only the commit message of your last commit can change.")
		}
		fmt.Println()
	}

	// 6. fixup target
	var target fixupTarget
	if fixup {
		var code int
		var good bool
		if target, code, good = chooseFixupTarget(root, stagedCount, fixupCount); !good {
			return code
		}
	}

	// 7. commit
	askMessage := func(promptText string) (msg string, ok bool) {
		const editorNote = "Editor options: you can input 'e' to open the message in your configured editor or 'v' for Vim"
		say("DarkGray", editorNote)
		line, gotInput := prompt(promptText)
		fmt.Println()
		if !gotInput {
			line = "q"
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "q":
			fmt.Println("Quit.")
			fmt.Println()
			return "", false
		case "e":
			return editCommitMessage(false)
		case "v":
			return editCommitMessage(true)
		}
		return line, true
	}

	var commitArgs []string
	switch {
	case fixup && fixupReword:
		// build the amend! commit ourselves from an in-terminal message so no editor opens; the rebase
		// --autosquash later folds it into the target via 'fixup -C', keeping this message
		fmt.Printf("Current message of %s:\n", target.hash)
		say("Cyan", "  "+target.subject)
		fmt.Println()
		msg, ok := askMessage(fmt.Sprintf("Input the new commit message for %s, an editor option, [blank] = exit without committing, or 'q' to quit, and press enter", target.hash))
		if !ok {
			return 0
		}
		if strings.TrimSpace(msg) == "" {
			fmt.Println("Commit message blank, no fixup commit has been made. Exiting.")
			fmt.Println()
			return 0
		}
		// 'amend! <subject>' is what git's autosquash matches to the target; the second -m is its new message
		commitArgs = []string{"commit", "--allow-empty", "-m", "amend! " + target.subject, "-m", msg}
	case fixup:
		commitArgs = []string{"commit", "--fixup=" + target.hash}
	default:
		question := "Input a commit message, [blank] = exit without committing"
		if amend {
			question = "Input a new commit message, [blank] = keep the existing message"
		}
		msg, ok := askMessage(question + ", an editor option, or 'q' to quit, and press enter")
		if !ok {
			return 0
		}
		msgBlank := strings.TrimSpace(msg) == ""
		if msgBlank && !amend {
			fmt.Println("Commit message blank, staged files will remain staged; no commit has been made. Exiting.")
			fmt.Println()
			return 0
		}
		if msgBlank && amend && stagedCount == 0 {
			fmt.Println("Nothing staged and message unchanged, so there is nothing to amend. Exiting.")
			fmt.Println()
			return 0
		}
		commitArgs = []string{"commit"}
		if amend {
			commitArgs = append(commitArgs, "--amend")
		}
		if msgBlank {
			commitArgs = append(commitArgs, "--no-edit")
		} else {
			commitArgs = append(commitArgs, "-m", msg)
		}
	}

	shown := make([]string, len(commitArgs))
	for i, a := range commitArgs {
		shown[i] = quoteForDisplay(a)
	}
	say("DarkGray", "Making commit: git "+strings.Join(shown, " "))
	// PowerShell also adds this command to up-arrow history; here it is printed above, ready to copy.
	if !runGit(root, commitArgs...) {
		errBlock("Error: Commit failed; check the output above for details.")
		return 1
	}

	// 8. squash offer and push guidance
	return afterCommit(root, amend, fixup, wasPushed, target)
}
