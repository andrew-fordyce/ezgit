# gitstatus: Go vs Rust comparison

A port of `Show-GitStatus` (from NonsenseScripts) in two languages, standard library only (no dependencies).

    go/            main.go + two tiny files that enable ANSI colour on Windows / elsewhere
    rust/          Cargo.toml + src/main.rs
    bin/           prebuilt, size-tuned binaries (Linux + Windows x64) - see warning below
    compare.sh     builds 10 throwaway git repos and diffs PowerShell vs Go vs Rust output

## Build
    cd go   && go build -trimpath -ldflags="-s -w" -o gitstatus .
    cd rust && cargo build --release                  # size-tuned profile is in Cargo.toml

    # cross-compile for Windows
    cd go   && GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o gitstatus.exe .
    cd rust && rustup target add x86_64-pc-windows-gnu && cargo build --release --target x86_64-pc-windows-gnu
                # (needs a MinGW linker on Linux, e.g. gcc-mingw-w64-x86-64)

## Usage
    gitstatus [--no-tip]        # NO_COLOR and NONSENSE_NO_TIPS are honoured

## Warning about the .exe files
They were cross-compiled on Linux and have NOT been run on Windows. They are unsigned, so security
software may flag them. Prefer building from the source here if in any doubt.
