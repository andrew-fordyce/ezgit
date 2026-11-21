// common.go - helpers shared by every ezgit command: coloured output, running git, parsing
// `git status`, the table renderer, and the interactive prompt / ID-selection grammar.
//
// All files in this folder are one Go package ("main"), so anything defined here can be used from
// status.go and add.go without importing it.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------- output helpers ----------

var useColor bool

// PowerShell's console colour names mapped to ANSI codes. As in PowerShell, plain names like
// "Red" are the bright variants and "Gray"/"DarkGray" are the two greys.
var ansi = map[string]string{
	"Gray": "37", "DarkGray": "90", "Red": "91", "Green": "92",
	"Yellow": "93", "Blue": "94", "Magenta": "95", "Cyan": "96",
}

func paint(color, s string) string {
	code, ok := ansi[color]
	if !useColor || !ok {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func say(color, s string)       { fmt.Println(paint(color, s)) }
func sayInline(color, s string) { fmt.Print(paint(color, s)) }

// pad right-pads s with spaces to the given width, counting characters rather than bytes.
func pad(s string, width int) string {
	if n := utf8.RuneCountInString(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

// fail prints the standard red error (plus any guidance lines) and stops with exit code 1.
func fail(msg string, guidance ...string) {
	fmt.Println()
	say("Red", msg)
	for _, line := range guidance {
		fmt.Println(line)
	}
	fmt.Println()
	os.Exit(1)
}

// ---------- running git ----------

// git runs a git command, inside repo `root` when given, and returns its captured output and
// whether it exited successfully. Errors are values in Go: we check the second result instead of
// $LASTEXITCODE. Note the output is returned even when git exits non-zero.
func git(root string, args ...string) (string, bool) {
	if root != "" {
		args = append([]string{"-C", root}, args...)
	}
	out, err := exec.Command("git", args...).Output()
	return string(out), err == nil
}

// runGit runs a git command inside `root` and lets its output go straight to the terminal (use
// this when the person should see what git says, e.g. for `git add` or `git stash`).
func runGit(root string, args ...string) bool {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run() == nil
}

// displayCommand renders a git command the way it is shown to the person: arguments containing
// whitespace are wrapped in single quotes (PowerShell style) so it can be copied and re-run.
func displayCommand(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\r\n") {
			a = "'" + strings.ReplaceAll(a, "'", "''") + "'"
		}
		parts[i] = a
	}
	return "git " + strings.Join(parts, " ")
}

// preflight checks git is installed and we're inside a working tree, and returns the repo root.
func preflight() string {
	if _, err := exec.LookPath("git"); err != nil {
		fail("Error: 'git' command not found in this session PATH. Aborting.",
			"Install Git and/or restart your terminal so 'git' is available, then try again.")
	}
	if out, ok := git("", "rev-parse", "--is-inside-work-tree"); !ok || strings.TrimSpace(out) != "true" {
		fail("Error: you are not inside a Git working tree. Aborting.")
	}
	rootOut, ok := git("", "rev-parse", "--show-toplevel")
	root := strings.TrimSpace(rootOut)
	if !ok || root == "" {
		fail("Error: could not resolve the repository root. Aborting.")
	}
	return root
}

// ---------- git status parsing ----------

type entry struct{ code, path string }

// parseStatus reads `git status --porcelain=v1 -z` output: NUL-separated "XY path" records. A
// rename or copy is followed by an extra field holding the original path, which we skip.
func parseStatus(raw string) []entry {
	var fields []string
	for _, f := range strings.Split(raw, "\x00") {
		if f != "" {
			fields = append(fields, f)
		}
	}
	var out []entry
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		code, p := f[:2], f[3:]
		if strings.ContainsAny(code, "RC") && i+1 < len(fields) {
			i++
		}
		out = append(out, entry{code, p})
	}
	return out
}

// describe maps a two-character status code to a human label and a display colour.
func describe(code string) (label, color string) {
	switch code {
	case "M ":
		return "Staged", "Green"
	case "A ":
		return "Added", "Green"
	case "R ":
		return "Renamed", "Green"
	case "C ":
		return "Copied", "Green"
	case "T ":
		return "TypeChange", "Green"
	case "??":
		return "Untracked", "Green"
	case " M":
		return "Modified", "Yellow"
	case " T":
		return "TypeChange", "Yellow"
	case "MM":
		return "Modified*", "Yellow"
	case "AM":
		return "Added*", "Yellow"
	case "RM":
		return "Renamed*", "Yellow"
	case "CM":
		return "Copied*", "Yellow"
	case "TM":
		return "TypeChange*", "Yellow"
	case "D ", " D", "MD", "AD", "RD", "CD", "TD":
		return "Deleted", "Red"
	case "UU", "AA", "DD", "UA", "AU", "UD", "DU":
		return "CONFLICT", "Magenta"
	case "!!":
		return "Ignored", "Gray"
	}
	return "Changed", "Gray"
}

func elapsed(t time.Time) string {
	d := time.Since(t)
	switch {
	case d.Minutes() < 1:
		return "just now"
	case d.Hours() < 1:
		return fmt.Sprintf("%d minute(s) ago", int(d.Minutes()))
	case d.Hours() < 24:
		return fmt.Sprintf("%d hour(s) ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d day(s) ago", int(d.Hours()/24))
}

// norm makes two paths comparable: git prints forward slashes even on Windows, and Windows
// paths are case-insensitive.
func norm(p string) string {
	p = strings.TrimRight(filepath.ToSlash(p), "/")
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// ---------- table ----------

// A cell is one piece of text in a table, with an optional colour name ("" = default colour).
type cell struct{ text, color string }

// table prints a table with dynamically sized columns, like Show-DynamicTable. Each row is a slice
// of cells, one per header. With withIndex, a 1-based "ID" column is added on the left.
// Prints nothing if there are no rows.
func table(title string, headers []string, rows [][]cell, withIndex bool) {
	if len(rows) == 0 {
		return
	}
	if withIndex {
		headers = append([]string{"ID"}, headers...)
		indexed := make([][]cell, len(rows))
		for i, r := range rows {
			indexed[i] = append([]cell{{text: strconv.Itoa(i + 1)}}, r...)
		}
		rows = indexed
	}

	widths := make([]int, len(headers))
	for c, h := range headers {
		widths[c] = utf8.RuneCountInString(h)
	}
	for _, r := range rows {
		for c, ce := range r {
			widths[c] = max(widths[c], utf8.RuneCountInString(ce.text))
		}
	}
	total := 2 * (len(headers) - 1) // two spaces between columns
	for _, w := range widths {
		total += w
	}
	sep := strings.Repeat("-", total)
	last := len(headers) - 1

	// every column except the last is padded to its width plus a 2-space gap
	fit := func(text string, c int) string {
		if c == last {
			return text
		}
		return pad(text, widths[c]) + "  "
	}

	if title != "" {
		fmt.Println()
		say("Gray", title)
	}
	say("DarkGray", sep)
	for c, h := range headers {
		sayInline("DarkGray", fit(h, c))
	}
	fmt.Println()
	say("DarkGray", sep)
	for _, r := range rows {
		for c, ce := range r {
			sayInline(ce.color, fit(ce.text, c))
		}
		fmt.Println()
	}
	say("DarkGray", sep)
}

// ---------- interactive input ----------

var stdin = bufio.NewReader(os.Stdin)

// stdinIsTerminal is true when a person is typing (as opposed to input coming from a pipe or file).
var stdinIsTerminal = func() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}()

// prompt shows text followed by ": " (like PowerShell's Read-Host) and reads one line. ok is false
// if input ended before anything was typed (Ctrl-D, or a closed pipe), so callers can quit safely
// instead of treating "no input at all" as an empty answer.
func prompt(text string) (line string, ok bool) {
	fmt.Print(text + ": ")
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		fmt.Println()
		return "", false
	}
	line = strings.TrimRight(line, "\r\n")
	if !stdinIsTerminal {
		// a terminal echoes what you type; piped input doesn't, so echo it to keep logs readable
		fmt.Println(line)
	}
	return line, true
}

var (
	rangeToken  = regexp.MustCompile(`^(\d+)[.\-]{1,2}(\d+)$`) // 1-3, 1..3, 1.3
	numberToken = regexp.MustCompile(`^\d+$`)
)

// A range wider than this is rejected as invalid instead of being expanded into millions of IDs.
const maxRangeSpan = 1_000_000

// At most this many out-of-range IDs are listed in the error message.
const maxListedIDs = 20

// resolveSelection turns text like "1-3 5 ^2" into a sorted, de-duplicated list of IDs, where a
// leading ^ excludes an ID or range. limit is the highest valid ID. On bad input it returns a
// plain-English reason instead (the whole input is rejected; nothing is acted on partially).
func resolveSelection(input string, limit int) (ids []int, reason string) {
	include := map[int]bool{}
	exclude := map[int]bool{}

	for _, token := range strings.Fields(input) {
		invalid := fmt.Sprintf("'%s' is not a valid ID or range.", token)
		core := strings.TrimPrefix(token, "^")
		target := include
		if strings.HasPrefix(token, "^") {
			target = exclude
		}

		if m := rangeToken.FindStringSubmatch(core); m != nil {
			start, err1 := strconv.Atoi(m[1])
			finish, err2 := strconv.Atoi(m[2])
			if err1 != nil || err2 != nil {
				return nil, invalid
			}
			if start > finish {
				start, finish = finish, start
			}
			if finish-start > maxRangeSpan {
				return nil, invalid
			}
			for i := start; i <= finish; i++ {
				target[i] = true
			}
		} else if numberToken.MatchString(core) {
			n, err := strconv.Atoi(core)
			if err != nil {
				return nil, invalid
			}
			target[n] = true
		} else {
			return nil, invalid
		}
	}

	// report every out-of-range ID together, so all the bad ones can be fixed at once
	var bad []int
	for _, set := range []map[int]bool{include, exclude} {
		for id := range set {
			if id < 1 || id > limit {
				bad = append(bad, id)
			}
		}
	}
	if len(bad) > 0 {
		sort.Ints(bad)
		var names []string
		for i, id := range bad {
			if i == 0 || id != bad[i-1] {
				names = append(names, strconv.Itoa(id))
			}
		}
		shown := strings.Join(names, ", ")
		if len(names) > maxListedIDs {
			shown = strings.Join(names[:maxListedIDs], ", ") + fmt.Sprintf(", ... (%d more)", len(names)-maxListedIDs)
		}
		return nil, fmt.Sprintf("ID(s) %s are out of range (1-%d).", shown, limit)
	}

	for id := range include {
		if !exclude[id] {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	return ids, ""
}

// ---------- commands ----------

// options are the flags every command understands.
type options struct{ noTip bool }

// showTip prints an explanatory tip unless tips are switched off (--no-tip or NONSENSE_NO_TIPS).
// The PowerShell version shows each tip once per session; a standalone program has no session,
// so here it shows every time unless disabled.
func showTip(o options, text string) {
	if !o.noTip {
		say("Blue", text)
	}
}
