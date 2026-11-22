// gitinfo.go - questions about the repository that several commands ask: which remote to suggest,
// what the trunk branch is, whether a commit is already pushed, and the "recent commits" table.
package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---------- remotes ----------

// defaultRemote is the best-guess remote name to suggest for a first push: 'origin' if it exists,
// else the only remote if there's exactly one, else 'origin' as a last-resort guess. ok is false
// when git couldn't list the remotes at all.
func defaultRemote(root string) (name string, ok bool) {
	out, ok := git(root, "remote")
	if !ok {
		return "", false
	}
	remotes := nonEmptyLines(out)
	for _, r := range remotes {
		if strings.EqualFold(r, "origin") {
			return "origin", true
		}
	}
	if len(remotes) == 1 {
		return remotes[0], true
	}
	return "origin", true
}

// ---------- trunk branch ----------

type trunkInfo struct {
	ok         bool
	name       string // bare branch name, e.g. "main"
	remoteName string // "origin/main", or just "main" when there is no remote
	err        string // why no trunk was found
}

var remoteHeadRef = regexp.MustCompile(`^refs/remotes/[^/]+/(.+)$`)

// resolveTrunk finds the repository's conventional trunk branch. It trusts a remote's advertised
// HEAD first, then looks for main, master, develop or trunk (remote copies before local ones). It
// reports failure rather than inventing a target.
func resolveTrunk(root string) trunkInfo {
	candidates := []string{"main", "master", "develop", "trunk"}

	out, ok := git(root, "remote")
	if !ok {
		return trunkInfo{err: "Could not list configured Git remotes."}
	}
	var remotes []string // 'origin' first, then the rest in order
	for _, r := range nonEmptyLines(out) {
		if strings.EqualFold(r, "origin") {
			remotes = append([]string{r}, remotes...)
		} else {
			remotes = append(remotes, r)
		}
	}

	// a remote's symbolic HEAD is the authoritative default branch, including unusual names
	for _, remote := range remotes {
		if ref, ok := git(root, "symbolic-ref", "--quiet", "refs/remotes/"+remote+"/HEAD"); ok {
			if m := remoteHeadRef.FindStringSubmatch(strings.TrimSpace(ref)); m != nil {
				if branch := strings.TrimSpace(m[1]); branch != "" {
					return trunkInfo{ok: true, name: branch, remoteName: remote + "/" + branch}
				}
			}
		}
	}
	for _, remote := range remotes {
		for _, c := range candidates {
			if _, ok := git(root, "show-ref", "--verify", "--quiet", "refs/remotes/"+remote+"/"+c); ok {
				return trunkInfo{ok: true, name: c, remoteName: remote + "/" + c}
			}
		}
	}
	for _, c := range candidates {
		if _, ok := git(root, "show-ref", "--verify", "--quiet", "refs/heads/"+c); ok {
			return trunkInfo{ok: true, name: c, remoteName: c}
		}
	}
	return trunkInfo{err: "No trunk branch was found. Checked remote HEADs and main, master, develop, and trunk."}
}

// ---------- is a commit pushed? ----------

// commitPushed checks whether a commit is already on the current branch's upstream. Having no
// upstream is normal and counts as "not pushed". It relies on your last fetch, so a stale remote
// ref can give a false "not pushed". ok is false only if git itself failed.
func commitPushed(root, revision string) (ok, pushed bool, upstream string) {
	out, code := gitCode(root, "rev-parse", "--abbrev-ref", "@{u}")
	upstream = strings.TrimSpace(out)
	if code != 0 || upstream == "" {
		return true, false, ""
	}
	switch _, code := gitCode(root, "merge-base", "--is-ancestor", revision, upstream); code {
	case 0:
		return true, true, upstream
	case 1:
		return true, false, upstream
	}
	return false, false, upstream
}

// ---------- recent commits ----------

type commitInfo struct {
	hash          string
	date          time.Time
	author        string
	decoration    string // refs shown in the table (a remote copy is dropped when the local branch is on the same commit)
	rawDecoration string // every ref, used for colouring
	message       string
}

var (
	logLine       = regexp.MustCompile(`^(.*?)([a-f0-9]{4,40})::(.*)$`)
	decorationCap = regexp.MustCompile(`^\s*\(|\)$`)
)

const logDateLayout = "2006-01-02 15:04:05"

// dedupeDecoration drops 'origin/x' from a ref list when a local 'x' is on the same commit, so the
// table doesn't show the same branch name twice.
func dedupeDecoration(raw string, remotes []string) string {
	if raw == "" || len(remotes) == 0 {
		return raw
	}
	var refs []string
	for _, r := range regexp.MustCompile(`,\s*`).Split(raw, -1) {
		if r != "" {
			refs = append(refs, r)
		}
	}
	isRemote := func(name string) (string, bool) {
		for _, rn := range remotes {
			if hasPrefixFold(name, rn+"/") {
				return rn, true
			}
		}
		return "", false
	}

	local := map[string]bool{}
	for _, r := range refs {
		if hasPrefixFold(r, "tag: ") {
			continue
		}
		name := r
		if hasPrefixFold(name, "HEAD -> ") {
			name = name[len("HEAD -> "):]
		}
		if _, remote := isRemote(name); !remote {
			local[strings.ToLower(name)] = true
		}
	}

	var kept []string
	for _, r := range refs {
		drop := false
		if !hasPrefixFold(r, "tag: ") {
			if rn, remote := isRemote(r); remote && local[strings.ToLower(r[len(rn)+1:])] {
				drop = true
			}
		}
		if !drop {
			kept = append(kept, r)
		}
	}
	return strings.Join(kept, ", ")
}

// parseLogLines reads lines in the shared log format "hash::date::author::decoration::subject".
func parseLogLines(lines []string, remotes []string) []commitInfo {
	var commits []commitInfo
	for _, line := range lines {
		m := logLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		parts := strings.SplitN(m[3], "::", 4)
		date, err := time.Parse(logDateLayout, strings.TrimSpace(parts[0]))
		if err != nil {
			date = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		c := commitInfo{hash: m[2], date: date}
		if len(parts) > 1 {
			c.author = strings.TrimSpace(parts[1])
		}
		if len(parts) > 2 {
			c.rawDecoration = decorationCap.ReplaceAllString(strings.TrimSpace(parts[2]), "")
		}
		c.decoration = dedupeDecoration(c.rawDecoration, remotes)
		if len(parts) > 3 {
			c.message = strings.TrimSpace(parts[3])
		}
		commits = append(commits, c)
	}
	return commits
}

// recentCommits lists recent commits, optionally limited to a revision range or to commits that
// touched given paths, and prints them as a table when a title is given. An empty repository is
// not an error: it returns no commits. ok is false only if git failed (an error is printed).
func recentCommits(root string, count int, title, revRange string, paths []string) (commits []commitInfo, ok bool) {
	if _, ok := git(root, "rev-parse", "--verify", "HEAD"); !ok {
		return nil, true
	}
	remotesOut, _ := git(root, "remote")
	remotes := nonEmptyLines(remotesOut)

	const format = "%h::%ad::%an::%d::%s"
	args := []string{"log", "-n", strconv.Itoa(count), "--format=" + format,
		"--date=format:%Y-%m-%d %H:%M:%S", "--decorate=short"}
	if revRange != "" {
		args = append(args, revRange)
	}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}

	echo := fmt.Sprintf("git log -n %d --format=%s --date=format:'%%Y-%%m-%%d %%H:%%M:%%S' --decorate=short", count, format)
	if revRange != "" {
		echo += " " + revRange
	}
	if len(paths) > 0 {
		var shown []string
		for _, p := range paths[:min(3, len(paths))] {
			shown = append(shown, `"`+p+`"`)
		}
		more := ""
		if len(paths) > 3 {
			more = fmt.Sprintf(" ... (+%d more)", len(paths)-3)
		}
		echo += " -- " + strings.Join(shown, " ") + more
	}
	say("DarkGray", "Retrieving recent commits: "+echo)

	out, gitOK := git(root, args...)
	if !gitOK {
		fmt.Println()
		say("Red", "Error: could not retrieve recent commits. Aborting.")
		fmt.Println()
		return nil, false
	}
	commits = parseLogLines(nonEmptyLines(out), remotes)

	if title != "" && len(commits) > 0 {
		say("Gray", title)
		rows := make([][]cell, len(commits))
		for i, c := range commits {
			raw := strings.ToLower(c.rawDecoration)
			color := "Gray"
			switch {
			case strings.Contains(raw, "head ->") && strings.Contains(raw, "origin/"):
				color = "Green"
			case strings.Contains(raw, "head ->"):
				color = "Cyan"
			case strings.Contains(raw, "origin/"):
				color = "Blue"
			}
			rows[i] = []cell{
				{c.hash, ""},
				{c.date.Format(logDateLayout), "DarkGray"},
				{truncate(c.message, 50), ""},
				{c.decoration, color},
			}
		}
		table("", []string{"Hash", "Date", "Message", "Status"}, rows, true)
	}
	return commits, true
}
