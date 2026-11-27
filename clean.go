package main

import (
	"fmt"
	"sort"
	"strings"
)

func cleanArgs(ignored bool) (preview, clean string) {
	if ignored {
		return "-ndx", "-fdx"
	}
	return "-nd", "-fd"
}

func cleanPreview(root string, ignored bool) ([]string, bool) {
	preview, _ := cleanArgs(ignored)
	out, ok := git(root, "clean", preview)
	return nonEmptyLines(out), ok
}

func cleanCommand(root, arg string) string {
	return fmt.Sprintf("git -C '%s' clean %s", strings.ReplaceAll(root, "'", "''"), arg)
}

func runClean(args []string) int {
	fs, o := flagsFor("clean")
	ignored := fs.Bool("include-ignored", false, "also remove ignored files")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	root := preflight()
	showTip(*o, "Git clean permanently deletes files and directories Git is not tracking; it does not move them to a recycle bin or stash.\nThis function previews the exact cleanup scope and asks before running it. Ignored files stay protected unless you explicitly include them.")
	if *ignored {
		say("Yellow", "Warning: ignored files are included. This may include local configuration, secrets, dependencies, and build outputs.")
	}
	previewArg, cleanArg := cleanArgs(*ignored)
	say("DarkGray", "Previewing untracked paths: "+cleanCommand(root, previewArg))
	preview, ok := cleanPreview(root, *ignored)
	if !ok {
		errBlock("Error: git clean preview failed. Aborting.")
		return 1
	}
	if len(preview) == 0 {
		say("Green", "No untracked paths are available to clean.")
		return 0
	}
	say("Yellow", "Git proposes to permanently remove:")
	for _, line := range preview {
		say("Gray", "  "+line)
	}
	confirm, got := prompt(func() string {
		if *ignored {
			return "Type 'clean ignored' exactly to permanently delete all paths shown above"
		}
		return "Input 'y' to permanently delete all paths shown above, or anything else to cancel, and press enter"
	}())
	if !got || (!*ignored && !strings.EqualFold(confirm, "y")) || (*ignored && !strings.EqualFold(confirm, "clean ignored")) {
		if *ignored {
			say("Yellow", "Confirmation failed. Aborting.")
		} else {
			fmt.Println("Quit.")
		}
		return 0
	}
	say("DarkGray", "Checking cleanup scope again: "+cleanCommand(root, previewArg))
	again, ok := cleanPreview(root, *ignored)
	if !ok {
		errBlock("Error: final git clean preview failed. Aborting.")
		return 1
	}
	previewSorted := append([]string(nil), preview...)
	againSorted := append([]string(nil), again...)
	sort.Strings(previewSorted)
	sort.Strings(againSorted)
	if strings.Join(previewSorted, "\x00") != strings.Join(againSorted, "\x00") {
		errBlock("Error: the untracked cleanup scope changed after the preview. Aborting.")
		fmt.Println("Run gitclean again to review the current paths before deleting anything.")
		return 1
	}
	say("DarkGray", "Cleaning untracked paths: "+cleanCommand(root, cleanArg))
	if !runGit(root, "clean", cleanArg) {
		errBlock("Error: git clean failed. Aborting.")
		fmt.Println("Review Git's messages above; some paths may already have been removed.")
		return 1
	}
	say("Green", "Untracked paths cleaned successfully.")
	return 0
}
