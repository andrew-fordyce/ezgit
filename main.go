// ezgit - easy git. Friendly, interactive wrappers around everyday git commands.
//
//	ezgit status
//	ezgit add
//	ezgit unstage
//	ezgit diff [branch-target] [text-filter]
//	ezgit log [revision-target] [text-filter]
//	ezgit branch [name]
//	ezgit rename [new-name]
//	ezgit prune
//	ezgit stash
//	ezgit clean
//	ezgit undo
//	ezgit commit [--amend | --fixup] [--fetch] [--branch-target X] [-n N]
//	ezgit merge  [--fetch] [--branch-target X]
//
// Every command also accepts --no-tip and --help.
package main

import (
	"fmt"
	"io"
	"os"
)

type command struct {
	name    string
	summary string
	run     func(args []string) int // gets the arguments after the command name; returns the exit code
}

// To add a command: write a `func runSomething(args []string) int` in its own file and list it here.
var commands = []command{
	{"status", "show your branch, how it compares to the remote, and which files changed", runStatus},
	{"switch", "interactively switch to a local or remote branch", runSwitch},
	{"branch", "create and switch to a new branch", runBranch},
	{"rename", "rename a local branch", runRename},
	{"prune", "delete selected local branches already merged into trunk", runPrune},
	{"add", "choose which changed files to stage, by ID", runAdd},
	{"unstage", "choose which staged files to remove from the index", runUnstage},
	{"diff", "choose changed files and show their patches", runDiff},
	{"log", "browse commits or a file's history", runLog},
	{"stash", "create and manage Git stashes", runStash},
	{"clean", "preview and remove untracked files", runClean},
	{"undo", "restore files, roll back a file, or undo a commit", runUndo},
	{"commit", "commit the staged changes (also --amend and --fixup)", runCommit},
	{"merge", "check whether you're behind the remote and merge the updates", runMerge},
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: ezgit <command> [options]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-8s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "run `ezgit <command> --help` for a command's options")
	fmt.Fprintln(w, "environment: NO_COLOR turns colours off; NONSENSE_NO_TIPS hides the explanatory tips")
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(os.Stdout)
		return
	}

	for _, c := range commands {
		if c.name == args[0] {
			useColor = os.Getenv("NO_COLOR") == "" && enableColor()
			os.Exit(c.run(args[1:]))
		}
	}
	fmt.Fprintf(os.Stderr, "ezgit: unknown command %q\n\n", args[0])
	usage(os.Stderr)
	os.Exit(2)
}
