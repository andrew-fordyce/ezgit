#!/usr/bin/env bash
# Builds a set of git repos in different states, runs gitstatus from PowerShell, Go and Rust in each,
# and diffs the plain-text output. Usage: ./compare.sh /path/to/pwsh /path/to/NonsenseScripts /path/to/go-binary /path/to/rust-binary
set -u
PWSH=$1; MODULE=$2; GOBIN=$3; RUSTBIN=$4
FX=/tmp/fx; rm -rf "$FX"; mkdir -p "$FX"
export DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1 NONSENSE_NO_BANNER=1 GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
g() { git -c init.defaultBranch=main "$@"; }

new_repo() { mkdir -p "$FX/$1" && cd "$FX/$1" && g init -q . ; }

# A: mixed working tree: staged, modified, staged+modified, deleted, renamed, untracked, spaces, unicode, mixed case
new_repo A
echo 1 > a.txt; echo 1 > b.txt; echo 1 > c.txt; echo 1 > old-name.txt; echo 1 > gone.txt; echo 1 > Zeta.txt
g add -A; g commit -qm base
echo 2 >> a.txt; g add a.txt; echo 3 >> a.txt           # staged + modified again
echo 2 >> b.txt                                          # modified only
g mv old-name.txt new-name.txt                           # renamed (staged)
rm gone.txt                                              # deleted (unstaged)
echo n > "café notes.txt"; echo n > "my file.txt"; echo n > alpha.txt   # untracked

# B: diverged from upstream (1 ahead, 2 behind), clean
new_repo Bremote; cd "$FX"; rm -rf Bremote; g init -q --bare Bremote.git
g clone -q Bremote.git B 2>/dev/null; cd B; echo 1 > f; g add f; g commit -qm one; g push -q origin main 2>/dev/null
cd "$FX"; g clone -q Bremote.git Bother 2>/dev/null; cd Bother; echo 2 >> f; g commit -qam two; echo 3 >> f; g commit -qam three; g push -q origin main 2>/dev/null
cd "$FX/B"; g fetch -q origin; echo 4 >> f; g commit -qam four

# C: clean and up to date with upstream, just fetched
cd "$FX"; g clone -q Bremote.git C 2>/dev/null; cd C; g fetch -q origin

# D: detached HEAD
new_repo D; echo 1 > f; g add f; g commit -qm one; echo 2 >> f; g commit -qam two; g checkout -q --detach HEAD~1

# E: merge conflict in progress
new_repo E; echo base > f; g add f; g commit -qm base; g checkout -qb side; echo side > f; g commit -qam side
g checkout -q main; echo main > f; g commit -qam main; g merge side >/dev/null 2>&1

# F: clean with two stashes
new_repo F; echo 1 > f; g add f; g commit -qm one; echo 2 >> f; g stash -q; echo 3 >> f; g stash -q

# G: dbt project in a subfolder, run from inside it, with an untracked file
new_repo G; mkdir -p dbt/models; echo "name: x" > dbt/dbt_project.yml; echo s > dbt/models/m.sql; g add -A; g commit -qm dbt; echo n > dbt/models/new.sql

# H: not a repository
mkdir -p "$FX/H"

# I: brand-new empty repo, no commits
new_repo I

# J: two remotes, no origin; clean
new_repo J; echo 1 > f; g add f; g commit -qm one; g remote add upstream "$FX/Bremote.git"; g remote add fork "$FX/Bremote.git"

run_case() { # name subdir
  local dir="$FX/$1${2:+/$2}"
  ( cd "$dir"
    "$PWSH" -NoProfile -Command "Import-Module '$MODULE/NonsenseScripts.psd1' -Force; Show-GitStatus -NoTip" > "$FX/$1.ps.txt" 2>&1
    "$GOBIN" --no-tip > "$FX/$1.go.txt" 2>&1
    "$RUSTBIN" --no-tip > "$FX/$1.rs.txt" 2>&1 )
  local gres rres
  diff -q "$FX/$1.ps.txt" "$FX/$1.go.txt" >/dev/null && gres=same || gres=DIFFERENT
  diff -q "$FX/$1.ps.txt" "$FX/$1.rs.txt" >/dev/null && rres=same || rres=DIFFERENT
  printf '%-4s  Go: %-9s Rust: %-9s\n' "$1" "$gres" "$rres"
}

for c in A B C D E F I J; do run_case $c; done
run_case G dbt
run_case H
