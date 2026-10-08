// Package filelock holds a lock that every process on the machine respects, for
// the files a vault appends to from more than one process at once.
//
// A vault is opened by each command and by each agent's server, and all of them
// append to the same catalog and the same index. An append part way through
// looks, to a process reading the file, exactly like one a crash cut off, and
// the two need opposite answers: what a crash cut off is taken off the end of
// the file, and what a live process is still writing has to be left alone.
// Holding this lock around every append, and around the check that decides what
// to take off, is what keeps the first answer for the case it is right for.
//
// The lock is advisory. It binds the processes that take it, which is every
// process that writes these files, and nothing else. The operating system
// releases it when the process holding it exits, however that happens, so a
// process that dies holding it does not wedge the next one.
package filelock

import (
	"fmt"
	"os"
)

// A Mutex is a lock file, held open and ready to be taken.
//
// It excludes every other holder of the same file, in this process or another,
// and it is not reentrant: taking it twice without releasing it in between is a
// deadlock on some platforms.
type Mutex struct {
	path string
	file *os.File
}

// Open opens the lock file at path, creating it if it is not there yet.
//
// The file holds nothing. It exists so that there is something to lock that no
// read or write ever touches, because on Windows a lock is enforced against
// reads and writes through every other handle to the file it is taken on.
func Open(path string) (*Mutex, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}
	return &Mutex{path: path, file: file}, nil
}

// Lock waits until no other holder has the lock, then takes it.
func (m *Mutex) Lock() error {
	if err := lock(m.file); err != nil {
		return fmt.Errorf("lock %s: %w", m.path, err)
	}
	return nil
}

// Unlock releases the lock.
func (m *Mutex) Unlock() error {
	if err := unlock(m.file); err != nil {
		return fmt.Errorf("unlock %s: %w", m.path, err)
	}
	return nil
}

// Close closes the lock file, which releases the lock if it is still held.
func (m *Mutex) Close() error { return m.file.Close() }
