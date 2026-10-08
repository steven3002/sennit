//go:build windows

package filelock

import (
	"os"

	"golang.org/x/sys/windows"
)

// Every holder locks the first byte of the file. The file is empty, and a lock
// past the end of a file is allowed, so there is no data for the lock to stand
// in front of. The handle is a synchronous one, so the call waits until the
// lock can be granted.
func lock(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, new(windows.Overlapped))
}

func unlock(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, new(windows.Overlapped))
}
