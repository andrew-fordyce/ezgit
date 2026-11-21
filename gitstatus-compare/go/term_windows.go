//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// enableColor switches the Windows console into ANSI-colour mode. It returns false when stdout is
// not a console (for example when redirected to a file), which also tells us to skip colour.
func enableColor() bool {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getMode := kernel32.NewProc("GetConsoleMode")
	setMode := kernel32.NewProc("SetConsoleMode")

	handle := os.Stdout.Fd()
	var mode uint32
	if ok, _, _ := getMode.Call(handle, uintptr(unsafe.Pointer(&mode))); ok == 0 {
		return false
	}
	const enableVirtualTerminalProcessing = 0x0004
	ok, _, _ := setMode.Call(handle, uintptr(mode|enableVirtualTerminalProcessing))
	return ok != 0
}
