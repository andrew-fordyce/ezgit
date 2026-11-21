// gitstatus - a friendlier `git status`. A Rust port of Show-GitStatus.ps1.
// Read-only: it inspects the repo and prints suggestions; it never changes anything.
use std::collections::HashSet;
use std::path::Path;
use std::process::{self, Command};
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::SystemTime;
use std::{env, fs};

// ---------- output helpers ----------

static COLOR: AtomicBool = AtomicBool::new(false);

// PowerShell's console colour names mapped to ANSI codes. As in PowerShell, plain names like
// "Red" are the bright variants and "Gray"/"DarkGray" are the two greys.
fn paint(color: &str, s: &str) -> String {
    let code = match color {
        "Gray" => "37",
        "DarkGray" => "90",
        "Red" => "91",
        "Green" => "92",
        "Yellow" => "93",
        "Blue" => "94",
        "Magenta" => "95",
        "Cyan" => "96",
        _ => "",
    };
    if COLOR.load(Ordering::Relaxed) && !code.is_empty() {
        format!("\x1b[{code}m{s}\x1b[0m")
    } else {
        s.to_string()
    }
}

fn say(color: &str, s: &str) {
    println!("{}", paint(color, s));
}

fn say_inline(color: &str, s: &str) {
    print!("{}", paint(color, s));
}

// fail prints the standard red error (plus any guidance lines) and stops with exit code 1.
// The `!` return type means "this function never returns".
fn fail(msg: &str, guidance: &[&str]) -> ! {
    println!();
    say("Red", msg);
    for line in guidance {
        println!("{line}");
    }
    println!();
    process::exit(1);
}

// Switches the Windows console into ANSI-colour mode. Returns false when stdout is not a console
// (for example when redirected to a file), which also tells us to skip colour.
#[cfg(windows)]
fn enable_color() -> bool {
    use std::os::windows::io::AsRawHandle;
    use std::ffi::c_void;

    #[link(name = "kernel32")]
    extern "system" {
        fn GetConsoleMode(handle: *mut c_void, mode: *mut u32) -> i32;
        fn SetConsoleMode(handle: *mut c_void, mode: u32) -> i32;
    }
    const ENABLE_VIRTUAL_TERMINAL_PROCESSING: u32 = 0x0004;

    let handle = std::io::stdout().as_raw_handle();
    let mut mode = 0u32;
    // unsafe is required for any call into non-Rust code
    unsafe {
        if GetConsoleMode(handle, &mut mode) == 0 {
            return false;
        }
        SetConsoleMode(handle, mode | ENABLE_VIRTUAL_TERMINAL_PROCESSING) != 0
    }
}

// Linux/macOS terminals understand ANSI colour; just check stdout is a terminal.
#[cfg(not(windows))]
fn enable_color() -> bool {
    use std::io::IsTerminal;
    std::io::stdout().is_terminal()
}

// ---------- git helpers ----------

// Runs a git command, inside repo `root` when given. Returns Some(stdout) if git exited
// successfully and None otherwise. Option<T> is Rust's "a value, or nothing" type, which plays
// the role of checking $LASTEXITCODE.
fn git(root: Option<&str>, args: &[&str]) -> Option<String> {
    let mut cmd = Command::new("git");
    if let Some(r) = root {
        cmd.args(["-C", r]);
    }
    let out = cmd.args(args).output().ok()?;
    out.status
        .success()
        .then(|| String::from_utf8_lossy(&out.stdout).into_owned())
}

struct Entry {
    code: String,
    path: String,
}

// Reads `git status --porcelain=v1 -z` output: NUL-separated "XY path" records. A rename or copy
// is followed by an extra field holding the original path, which we skip.
fn parse_status(raw: &str) -> Vec<Entry> {
    let fields: Vec<&str> = raw.split('\0').filter(|f| !f.is_empty()).collect();
    let mut out = Vec::new();
    let mut i = 0;
    while i < fields.len() {
        let f = fields[i];
        if f.len() >= 4 {
            if let (Some(code), Some(path)) = (f.get(..2), f.get(3..)) {
                if code.chars().any(|c| c == 'R' || c == 'C') && i + 1 < fields.len() {
                    i += 1;
                }
                out.push(Entry {
                    code: code.to_string(),
                    path: path.to_string(),
                });
            }
        }
        i += 1;
    }
    out
}

// Maps a two-character status code to a human label and a display colour.
fn describe(code: &str) -> (&'static str, &'static str) {
    match code {
        "M " => ("Staged", "Green"),
        "A " => ("Added", "Green"),
        "R " => ("Renamed", "Green"),
        "C " => ("Copied", "Green"),
        "T " => ("TypeChange", "Green"),
        "??" => ("Untracked", "Green"),
        " M" => ("Modified", "Yellow"),
        " T" => ("TypeChange", "Yellow"),
        "MM" => ("Modified*", "Yellow"),
        "AM" => ("Added*", "Yellow"),
        "RM" => ("Renamed*", "Yellow"),
        "CM" => ("Copied*", "Yellow"),
        "TM" => ("TypeChange*", "Yellow"),
        "D " | " D" | "MD" | "AD" | "RD" | "CD" | "TD" => ("Deleted", "Red"),
        "UU" | "AA" | "DD" | "UA" | "AU" | "UD" | "DU" => ("CONFLICT", "Magenta"),
        "!!" => ("Ignored", "Gray"),
        _ => ("Changed", "Gray"),
    }
}

fn elapsed(t: SystemTime) -> String {
    let secs = t.elapsed().map(|d| d.as_secs()).unwrap_or(0);
    let (minutes, hours, days) = (secs / 60, secs / 3600, secs / 86400);
    if minutes < 1 {
        "just now".to_string()
    } else if hours < 1 {
        format!("{minutes} minute(s) ago")
    } else if days < 1 {
        format!("{hours} hour(s) ago")
    } else {
        format!("{days} day(s) ago")
    }
}

// Makes two paths comparable: git prints forward slashes even on Windows, and Windows paths are
// case-insensitive.
fn norm(p: &Path) -> String {
    let s = p.to_string_lossy().replace('\\', "/");
    let s = s.trim_end_matches('/').to_string();
    if cfg!(windows) {
        s.to_lowercase()
    } else {
        s
    }
}

// ---------- table ----------

struct Row {
    code: String,
    path: String,
}

fn table(title: &str, rows: &[Row]) {
    let labels: Vec<(&str, &str)> = rows.iter().map(|r| describe(&r.code)).collect();
    let w0 = labels
        .iter()
        .map(|l| l.0.chars().count())
        .max()
        .unwrap_or(0)
        .max("Status".len());
    let w1 = rows
        .iter()
        .map(|r| r.path.chars().count())
        .max()
        .unwrap_or(0)
        .max("Path".len());
    let sep = "-".repeat(w0 + 2 + w1);

    println!();
    say("Gray", title);
    say("DarkGray", &sep);
    say_inline("DarkGray", &format!("{:<w0$}  ", "Status"));
    say("DarkGray", "Path");
    say("DarkGray", &sep);
    for (row, (label, color)) in rows.iter().zip(labels) {
        say_inline(color, &format!("{label:<w0$}  "));
        println!("{}", row.path);
    }
    say("DarkGray", &sep);
}

// ---------- main ----------

fn main() {
    let mut no_tip = env::var_os("NONSENSE_NO_TIPS").is_some_and(|v| !v.is_empty());
    for arg in env::args().skip(1) {
        match arg.as_str() {
            "--no-tip" => no_tip = true,
            "-h" | "--help" => {
                println!("usage: gitstatus [--no-tip]");
                return;
            }
            _ => {
                eprintln!("gitstatus: unknown option {arg:?}");
                process::exit(2);
            }
        }
    }
    let no_color = env::var_os("NO_COLOR").is_some_and(|v| !v.is_empty());
    COLOR.store(!no_color && enable_color(), Ordering::Relaxed);

    // 1. preflight: git exists and we're inside a working tree
    if Command::new("git").arg("--version").output().is_err() {
        fail(
            "Error: 'git' command not found in this session PATH. Aborting.",
            &["Install Git and/or restart your terminal so 'git' is available, then try again."],
        );
    }
    match git(None, &["rev-parse", "--is-inside-work-tree"]) {
        Some(out) if out.trim() == "true" => {}
        _ => fail("Error: you are not inside a Git working tree. Aborting.", &[]),
    }
    let root = match git(None, &["rev-parse", "--show-toplevel"]) {
        Some(out) if !out.trim().is_empty() => out.trim().to_string(),
        _ => fail("Error: could not resolve the repository root. Aborting.", &[]),
    };
    let root = root.as_str();

    if !no_tip {
        say(
            "Blue",
            "Shows where you are right now: your branch, how it compares to the remote, and which files have changed.\n\
             Git tracks changes in three places - the working tree (files on disk), the index (what's staged for the next commit), and commits.\n\
             The tables below split files the same way 'git status' does. Run 'whatdo' to also list every command in this toolbox.",
        );
    }

    // 2. gather state
    let branch = git(Some(root), &["branch", "--show-current"])
        .unwrap_or_default()
        .trim()
        .to_string();
    let detached = branch.is_empty();

    let merge_in_progress = git(Some(root), &["rev-parse", "-q", "--verify", "MERGE_HEAD"]).is_some();

    say("DarkGray", "Checking working tree status: git status --porcelain=v1 -z");
    let Some(raw) = git(Some(root), &["status", "--porcelain=v1", "-z"]) else {
        fail("Error: could not inspect working tree status. Aborting.", &[]);
    };
    let mut entries = parse_status(&raw);
    entries.sort_by(|a, b| {
        a.path
            .to_lowercase()
            .cmp(&b.path.to_lowercase())
            .then_with(|| a.path.cmp(&b.path))
    });

    // bucket the way git status does: X is the index (staged) side, Y the working tree. A file
    // with both lands in Staged and Not staged, each row keeping only its own side.
    let conflict_codes = ["UU", "AA", "DD", "UA", "AU", "UD", "DU"];
    let (mut conflicts, mut staged, mut not_staged, mut untracked) = (vec![], vec![], vec![], vec![]);
    for e in &entries {
        let b = e.code.as_bytes();
        if conflict_codes.contains(&e.code.as_str()) {
            conflicts.push(Row { code: e.code.clone(), path: e.path.clone() });
        } else if e.code == "??" {
            untracked.push(Row { code: e.code.clone(), path: e.path.clone() });
        } else {
            if b[0] != b' ' {
                staged.push(Row { code: format!("{} ", b[0] as char), path: e.path.clone() });
            }
            if b[1] != b' ' {
                not_staged.push(Row { code: format!(" {}", b[1] as char), path: e.path.clone() });
            }
        }
    }
    let conflict_count = conflicts.len();
    let staged_count = staged.len();
    let unstaged_count = not_staged.len() + untracked.len();
    let dirty_count = entries.len();

    let upstream = git(Some(root), &["rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"])
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty());
    let has_upstream = upstream.is_some();
    let upstream = upstream.unwrap_or_default();

    let (mut ahead, mut behind) = (0u32, 0u32);
    if has_upstream {
        say(
            "DarkGray",
            &format!("Comparing with {upstream}: git rev-list --left-right --count {upstream}...HEAD"),
        );
        let range = format!("{upstream}...HEAD");
        if let Some(out) = git(Some(root), &["rev-list", "--left-right", "--count", &range]) {
            let parts: Vec<&str> = out.split_whitespace().collect();
            if parts.len() >= 2 {
                behind = parts[0].parse().unwrap_or(0);
                ahead = parts[1].parse().unwrap_or(0);
            }
        }
    }

    say("DarkGray", "Counting stashes: git stash list");
    let stash_count = git(Some(root), &["stash", "list"])
        .unwrap_or_default()
        .lines()
        .filter(|l| !l.trim().is_empty())
        .count();

    // prefer 'origin', else the sole remote, else guess 'origin'. As in the PowerShell original,
    // "has a remote" only means that listing remotes worked.
    let remotes_out = git(Some(root), &["remote"]);
    let has_remote = remotes_out.is_some();
    let remotes_out = remotes_out.unwrap_or_default();
    let names: Vec<&str> = remotes_out.split_whitespace().collect();
    let remote_name = if names.len() == 1 { names[0] } else { "origin" };

    let mut last_fetch = "never".to_string();
    if let Some(p) = git(Some(root), &["rev-parse", "--git-path", "FETCH_HEAD"]) {
        let p = p.trim();
        if !p.is_empty() {
            let path = if Path::new(p).is_absolute() { Path::new(p).to_path_buf() } else { Path::new(root).join(p) };
            if let Ok(modified) = fs::metadata(&path).and_then(|m| m.modified()) {
                last_fetch = elapsed(modified);
            }
        }
    }

    // 3. status summary
    println!();
    if detached {
        let short = git(Some(root), &["rev-parse", "--short", "HEAD"]).unwrap_or_default();
        say("Yellow", &format!("HEAD detached at {} (you're not on any branch).", short.trim()));
    } else {
        print!("On branch ");
        say_inline("Cyan", &branch);
        println!(" last fetched: {last_fetch}");
        if !has_upstream {
            say("Yellow", &format!("No upstream - this branch hasn't been pushed to {remote_name} yet."));
        } else if ahead == 0 && behind == 0 {
            println!("Up to date with '{upstream}'.");
        } else {
            let mut drift = vec![];
            if ahead > 0 {
                drift.push(format!("{ahead} commit(s) ahead (yours, not pushed)"));
            }
            if behind > 0 {
                drift.push(format!("{behind} commit(s) behind (theirs, not pulled)"));
            }
            // ahead-only is normal; being behind is what needs attention
            let color = if behind > 0 { "Yellow" } else { "Gray" };
            say(color, &format!("Compared with '{upstream}': {}.", drift.join(", ")));
        }
    }
    if merge_in_progress {
        say("Yellow", "A merge is in progress - resolve any conflicts, then commit to finish it.");
    }
    if stash_count > 0 {
        say("DarkGray", &format!("  {stash_count} stash(es) saved."));
    }

    if dirty_count == 0 {
        println!();
        say("Green", "Nothing to commit, working tree clean.");
    } else {
        let tables = [
            ("Conflicts - both sides changed these, fix them before committing", &conflicts),
            ("Staged changes - in the index, will go into your next commit", &staged),
            ("Unstaged changes - changed on disk, but not yet added to the index", &not_staged),
            ("Untracked changes - new files git isn't tracking yet", &untracked),
        ];
        for (title, rows) in tables {
            if !rows.is_empty() {
                table(&format!("{title} ({}):", rows.len()), rows);
            }
        }
        let staged_paths: HashSet<&str> = staged.iter().map(|r| r.path.as_str()).collect();
        if not_staged.iter().any(|r| staged_paths.contains(r.path.as_str())) {
            say(
                "DarkGray",
                "Note: a file with both Staged changes and Unstaged changes has more edits since you staged it; only the staged version will be committed.",
            );
        }
    }

    // 4. suggestions, most urgent first
    let mut sug: Vec<(String, String)> = vec![];
    let mut add = |cmd: &str, why: String| sug.push((cmd.to_string(), why));

    if detached {
        add("gitswitch", "get back onto a real branch before you lose track of this work".into());
    }
    if conflict_count > 0 {
        add("gitmerge", format!("resolve the {conflict_count} conflicted file(s) and finish the merge"));
    }
    if dirty_count > 0 {
        if unstaged_count > 0 {
            add("gitadd", format!("choose which {unstaged_count} unstaged file(s) to put into the next commit"));
        }
        if staged_count > 0 {
            add("gitcommit", format!("commit the {staged_count} file(s) already staged in the index"));
            add("gitunstage", "take staged file(s) back out of the index, keeping your edits on disk".into());
        }
        add("gitdiff", "review exactly what changed, line by line, before committing".into());
        add("gitstash", "shelve these changes to come back to later".into());
        add("gitundo", "discard changes and return a file (or everything) to a clean state".into());
    } else {
        add("gitbranch", "start a new branch for a fresh piece of work".into());
        add("gitswitch", "jump to another branch".into());
        add("gitlog", "review the recent commit history on this branch".into());
    }
    if !detached {
        if has_remote {
            add(&format!("git fetch {remote_name}"), "update your local view of the remote branches before comparing or merging".into());
        }
        if !has_upstream {
            add(&format!("git push -u {remote_name} HEAD"), "publish this branch so it's backed up and visible to the team".into());
        } else if behind > 0 && conflict_count == 0 {
            add("gitmerge", format!("pull in {behind} upstream commit(s) and resolve any drift"));
        } else if ahead > 0 && dirty_count == 0 {
            add("git push", format!("upload your {ahead} local commit(s) to the remote"));
        }
    }
    if stash_count > 0 {
        add("gitstash", format!("you have {stash_count} stash(es) waiting - view, apply, or drop them"));
    }

    // a dbt project between here and the repo root? then suggest the dbt entry point
    let root_norm = norm(Path::new(root));
    let mut dir = env::current_dir().ok();
    while let Some(d) = dir {
        if d.join("dbt_project.yml").exists() {
            add("dbtrun", "run dbt models by picking them from a list instead of typing --select".into());
            break;
        }
        if norm(&d) == root_norm {
            break;
        }
        dir = d.parent().map(Path::to_path_buf);
    }

    println!();
    println!("Suggested next steps");
    let width = sug.iter().map(|(c, _)| c.chars().count()).max().unwrap_or(0);
    for (cmd, why) in &sug {
        print!("  ");
        say_inline("Magenta", &format!("{cmd:<width$}"));
        say("Gray", &format!("  {why}"));
    }
    println!();
}
