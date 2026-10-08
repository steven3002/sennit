//go:build !windows

package filelock

import (
	"os"
	"syscall"
)

// flock rather than fcntl, because an fcntl lock belongs to the process: two
// handles in one process would share it rather than exclude each other, and
// closing either would release it.
func lock(file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			return err
		}
	}
}

func unlock(file *os.File) error { return syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }
