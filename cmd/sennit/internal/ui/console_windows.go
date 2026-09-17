//go:build windows

package ui

import (
	"os"

	"golang.org/x/sys/windows"
)

// EnableEscapes turns on virtual terminal processing for a console, so that the
// colour and cursor sequences are interpreted rather than printed, and returns a
// function that puts the console mode back.
//
// A new console screen buffer interprets no output sequences until
// ENABLE_VIRTUAL_TERMINAL_PROCESSING is set, and golang.org/x/term only ever sets
// the input flag. The mode belongs to the console rather than to this process,
// so it is restored on the way out instead of being left changed for whatever
// runs next in the same window. A handle that is not a console, a pipe or a file,
// interprets nothing and needs nothing. A console that refuses the flag reports
// false, and the caller draws nothing that depends on it.
func EnableEscapes(f *os.File) (bool, func()) {
	handle := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return true, func() {}
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true, func() {}
	}
	if err := windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		return false, func() {}
	}
	return true, func() { _ = windows.SetConsoleMode(handle, mode) }
}
