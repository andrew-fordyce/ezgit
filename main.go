// ezgit - easy git. Friendly, interactive wrappers around everyday git commands.
//
//	ezgit status [--no-tip]
//	ezgit add    [--no-tip]
package main

import (
	"fmt"
	"io"
	"os"
)

type command struct {
	name    string
	summary string
	run     func(options) int // returns the process exit code
}

// To add a command: write a `func runSomething(o options) int` in its own file and list it here.
var commands = []command{
	{name:"status", summary:"show your branch, how it compares to the remote, and which files changed", run:runStatus},
	{name:"add",    summary:"choose which changed files to stage, by ID",                               run:runAdd},
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: ezgit <command> [--no-tip]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-8s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "environment: NO_COLOR turns colours off; NONSENSE_NO_TIPS hides the explanatory tips")
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(os.Stdout)
		return
	}

	var chosen *command
	for i := range commands {
		if commands[i].name == args[0] {
			chosen = &commands[i]
		}
	}
	if chosen == nil {
		fmt.Fprintf(os.Stderr, "ezgit: unknown command %q\n\n", args[0])
		usage(os.Stderr)
		os.Exit(2)
	}

	opts := options{noTip: os.Getenv("NONSENSE_NO_TIPS") != ""}
	for _, arg := range args[1:] {
		switch arg {
		case "--no-tip":
			opts.noTip = true
		case "-h", "--help":
			fmt.Printf("usage: ezgit %s [--no-tip]\n  %s\n", chosen.name, chosen.summary)
			return
		default:
			fmt.Fprintf(os.Stderr, "ezgit %s: unknown option %q\n", chosen.name, arg)
			os.Exit(2)
		}
	}

	useColor = os.Getenv("NO_COLOR") == "" && enableColor()
	os.Exit(chosen.run(opts))
}
