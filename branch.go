// branch.go - ezgit branch - Interactive branch creation with base-branch selection
// and uncommitted-change awareness. Port of New-GitBranch.ps1.
//
// Lists local and remote branches to pick a base from, warns about any uncommitted
// changes that will carry over to the new branch, suggests (but doesn't enforce) a
// naming prefix, and reminds you how to set the upstream on first push.
package main

import (
	"fmt"
	"strings"
)

func runBranch(args []string) int {
	fs, o := flagsFor("branch", "")
	fetch := fs.Bool("fetch", false, "run `git fetch --all` before listing branches")
	var maxBranches int
	fs.IntVar(&maxBranches, "n", 20, "most recent branches to show when there is no filter (see --max-branches)")
	fs.IntVar(&maxBranches, "max-branches", 20, "most recent branches to show when there is no filter")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	// preflight: git exists and we're inside a working tree
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

	// validate max branches argument
	if maxBranches < 1 {
		errBlock("Error: --max-branches must be at least 1. Aborting.")
		return 1
	}

	showTip(*o, "Branches are independent lines of work, letting you experiment or build "+
		"something without touching main.\nThis function lists existing branches so you can "+
		"pick a base, prompts you for a new branch name, then creates and switches you onto it.")

	if *fetch {
		say("DarkGray", "Fetching updates: git fetch --all -q")
		if !runGit(root, "fetch", "all", "-q") {
			errBlock("Error: Failed to fetch remote updates. Aborting.")
			return 1
		}
	}

	// warn about uncommitted changes that will carry over
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
		fmt.Println("These changes aren't tied to your current branch until committed, so they'll follow you onto the " +
			"new branch if you create one now.")
		line, gotInput := prompt("Input 'c' to carry these changes onto the new branch, 's' to stash them here and not " +
			"bring them with you, or 'q' to quit, and press enter")
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
			changesStashed = true
			fmt.Println("Local changes stashed before switching branch, to restore them later, first make sure you are on the intended branch, and then run:")
			say("Magenta", "git stash pop")
		case "c":
			fmt.Println("Continuing with the changes in your working tree. These changes will carry over to the new branch.")
			fmt.Println()
		default:
			errBlock(fmt.Sprintf("Error: Invalid input '%s'. Aborting.", choice))
			return 1
		}
	}
}
