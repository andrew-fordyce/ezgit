# ezgit

### early draft state, i dont recommend using it just yet

Friendly, interactive git helpers. Go ports of my PowerShell scripts.

    ezgit status     where you are: branch, remote comparison, changed files, next steps
    ezgit switch     switch to a local or remote branch
    ezgit branch     create and switch to a new branch
    ezgit rename     rename a local branch
    ezgit prune      remove selected local branches already merged into trunk
    ezgit add        pick which changed files to stage by ID  (1-3 5 ^2, a, n, s, q)
    ezgit unstage    pick staged files to remove from the index
    ezgit diff       select files and inspect their patches
    ezgit log        browse commits or a file's history
    ezgit stash      create and manage stashes
    ezgit clean      preview and remove untracked files
    ezgit undo       restore files, roll back a file, or undo a commit
    ezgit commit     create, amend, or fix up a commit
    ezgit merge      check for drift and merge updates

## Files (all one Go package, so they share each other's functions)
    main.go         picks the command from the first argument
    common.go       colours, running git, status parsing, table renderer, prompt, ID-selection grammar
    gitinfo.go      shared remote, trunk, pushed-commit, and recent-log helpers
    <command>.go    one or more related ezgit commands
    term_notwindows.go / term_windows.go   switch on ANSI colour (the right one is chosen per OS automatically)

## Develop
    go build -o /tmp/ezgit . && cd ~/some/repo && /tmp/ezgit add
    go vet ./... && gofmt -l .

## Build
    go build -trimpath -ldflags="-s -w" -o ezgit .
    GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o ezgit.exe .

## Adding a command
Write `func runSomething(args []string) int` in its own file and add one line to `commands` in main.go.

Environment: NO_COLOR disables colour; NONSENSE_NO_TIPS hides the explanatory tips.

currently  in 0.1.0 state
