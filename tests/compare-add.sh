#!/usr/bin/env bash
# Compares PowerShell's Add-GitChange with `ezgit add`.
#
# For every scenario it builds a throwaway repo, makes two identical copies (cp -a keeps file
# timestamps, which drive the "Modified" column and the default selection), feeds the same typed
# input to each tool, then compares (1) the text printed and (2) the repo state afterwards.
#
# Usage: compare-add.sh /path/to/pwsh /path/to/NonsenseScripts /path/to/ezgit-binary
set -u
PWSH=$1; MODULE=$2; EZGIT=$3
FX=/tmp/fxadd; rm -rf "$FX"; mkdir -p "$FX"
export DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1 NONSENSE_NO_BANNER=1 NONSENSE_NO_TIPS=1 TZ=UTC
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
g() { git -c init.defaultBranch=main "$@"; }
new_repo() { rm -rf "$FX/$1"; mkdir -p "$FX/$1" && cd "$FX/$1" && g init -q .; }

# ---- fixtures ----------------------------------------------------------------------------------
# MIXED: modified, staged+modified, deleted, renamed+edited, binary, untracked, spaces, unicode,
# nested folders, mixed case, and two files sharing the newest timestamp.
new_repo MIXED
mkdir -p sub/deep
for f in a.txt b.txt c.txt sub/d.txt sub/deep/e.txt gone.txt old-name.txt Zeta.txt "café notes.txt"; do seq 1 5 > "$f"; done
printf '\x89PNG\r\n\x1a\n\x00\x01\x02' > bin.dat
g add -A; g commit -qm base
echo "x" >> a.txt; g add a.txt; echo "y" >> a.txt          # staged, then modified again (MM)
seq 6 12 >> b.txt                                           # modified: 7 lines added
echo "z" >> c.txt; g add c.txt                              # fully staged (hidden from the list)
echo "d" >> sub/d.txt; echo "e" >> sub/deep/e.txt           # nested modifications
rm gone.txt                                                 # deleted
printf '\x89PNG\r\n\x1a\n\x00\x09\x09' > bin.dat            # binary modification
g mv old-name.txt new-name.txt; echo "edit" >> new-name.txt # renamed, then edited (RM)
echo "n1" > new1.txt; seq 1 4 > "my file.txt"; echo "u" > "café new.txt"
mkdir newdir; echo "q" > newdir/inside.txt                  # untracked folder
echo "c" >> "café notes.txt"
find . -path ./.git -prune -o -type f -exec touch -d '2026-03-01 09:00' {} +
touch -d '2026-03-02 17:45:10' b.txt sub/d.txt              # newest minute -> default selection

new_repo CLEAN;  echo 1 > f; g add f; g commit -qm one
new_repo STAGEDONLY; echo 1 > f; g add f; g commit -qm one; echo 2 >> f; g add f; echo n > g; g add g
new_repo DELETES; for f in x y z; do echo 1 > $f; done; g add -A; g commit -qm base; rm x y
new_repo ONEUNTRACKED; echo 1 > f; g add f; g commit -qm one; echo "new" > fresh.txt
new_repo CONFLICT; echo base > f; g add f; g commit -qm base; g checkout -qb side; echo side > f; g commit -qam side
g checkout -q main; echo main > f; g commit -qam main; g merge side >/dev/null 2>&1; echo more > other.txt
mkdir -p "$FX/NOTREPO"

# ---- runner ------------------------------------------------------------------------------------
normalise() { sed -E 's/in [0-9.]+ second\(s\)/in N second(s)/'; }
pass=0; fail=0
run_case() { # fixture "typed input"  (input is sent followed by Enter; "" means just Enter)
  local fx=$1 input=$2 label=$3
  rm -rf "$FX/_ps" "$FX/_go"; cp -a "$FX/$fx" "$FX/_ps"; cp -a "$FX/$fx" "$FX/_go"
  ( cd "$FX/_ps"; printf '%s\n' "$input" | "$PWSH" -NoProfile -Command "Import-Module '$MODULE/NonsenseScripts.psd1' -Force; Add-GitChange" 2>&1 | normalise > "$FX/_ps.out" )
  ( cd "$FX/_go"; printf '%s\n' "$input" | "$EZGIT" add 2>&1 | normalise > "$FX/_go.out" )
  local state_ps state_go
  state_ps=$(cd "$FX/_ps" && g status --porcelain=v1 -z 2>/dev/null | tr '\0' '|'; g stash list 2>/dev/null)
  state_go=$(cd "$FX/_go" && g status --porcelain=v1 -z 2>/dev/null | tr '\0' '|'; g stash list 2>/dev/null)
  local out=same st=same
  diff -q "$FX/_ps.out" "$FX/_go.out" >/dev/null || out=DIFFERENT
  [ "$state_ps" = "$state_go" ] || st=DIFFERENT
  if [ "$out" = same ] && [ "$st" = same ]; then pass=$((pass+1)); else fail=$((fail+1)); safe=$(echo "$label" | tr -c "A-Za-z0-9" "_"); cp "$FX/_ps.out" "$FX/fail-$safe.ps.txt"; cp "$FX/_go.out" "$FX/fail-$safe.go.txt"; fi
  printf '%-34s output: %-9s repo state after: %s\n' "$label" "$out" "$st"
}

run_case MIXED "q"          "MIXED  q (quit)"
run_case MIXED ""           "MIXED  blank (default)"
run_case MIXED "a"          "MIXED  a (all)"
run_case MIXED "A"          "MIXED  A (uppercase)"
run_case MIXED "1 3"        "MIXED  1 3"
run_case MIXED "1-3 ^2"     "MIXED  1-3 ^2"
run_case MIXED "2..4 ^3"    "MIXED  2..4 ^3"
run_case MIXED "3 3 1"      "MIXED  3 3 1 (dupes/order)"
run_case MIXED "n"          "MIXED  n (none)"
run_case MIXED "s"          "MIXED  s (stash)"
run_case MIXED "x"          "MIXED  x (invalid)"
run_case MIXED "1 -"        "MIXED  1 - (invalid token)"
run_case MIXED "99 100"     "MIXED  99 100 (out of range)"
run_case MIXED "0 1"        "MIXED  0 1 (zero)"
run_case MIXED "1 ^1"       "MIXED  1 ^1 (all excluded)"
run_case MIXED "^1"         "MIXED  ^1 (only exclusion)"
run_case CLEAN ""           "CLEAN  (nothing changed)"
run_case STAGEDONLY ""      "STAGEDONLY (hint to unstage)"
run_case DELETES "a"        "DELETES  a (git rm path)"
run_case DELETES ""         "DELETES  blank (default)"
run_case ONEUNTRACKED ""    "ONEUNTRACKED  blank"
run_case CONFLICT "q"       "CONFLICT  q (conflict rows)"
run_case NOTREPO ""         "NOTREPO"

echo; echo "passed: $pass   failed: $fail"
