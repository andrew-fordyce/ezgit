# ezgit

### early draft state, i dont recommend using it just yet

Friendly, interactive git helpers. Go ports of my PowerShell scripts.

    ezgit status     where you are: branch, remote comparison, changed files, next steps
    ezgit add        pick which changed files to stage by ID  (1-3 5 ^2, a, n, s, q)

## Files (all one Go package, so they share each other's functions)
    main.go         picks the command from the first argument
    common.go       colours, running git, status parsing, table renderer, prompt, ID-selection grammar
    status.go       ezgit status
    add.go          ezgit add
    term_other.go / term_windows.go   switch on ANSI colour (the right one is chosen per OS automatically)
    tests/compare-add.sh   diffs PowerShell Add-GitChange against `ezgit add` across 23 scenarios

## Develop
    go build -o /tmp/ezgit . && cd ~/some/repo && /tmp/ezgit add
    go vet ./... && gofmt -l .

## Build
    go build -trimpath -ldflags="-s -w" -o ezgit .
    GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o ezgit.exe .

## Adding a command
Write `func runSomething(o options) int` in its own file and add one line to `commands` in main.go.

Environment: NO_COLOR disables colour; NONSENSE_NO_TIPS hides the explanatory tips.

currently  in 0.1.0 state
