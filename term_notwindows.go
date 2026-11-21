//go:build !windows

package main

import "os"

// enableColor reports whether stdout is a terminal (Linux/macOS terminals understand ANSI colour).
func enableColor() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
