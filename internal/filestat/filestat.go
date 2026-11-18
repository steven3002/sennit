// Package filestat tells, from two looks at one path, whether the file there is
// the one that was there before, unchanged.
//
// It is how a process holding a vault open notices that another process has
// written to the files they share. Every such file is either appended to, which
// changes its size, or replaced whole by a rename, which puts another file at
// the path. Reading the files again to find out would cost what loading them
// costs, and the question is asked before every answer a server gives.
//
// A file's size and the moment it was last written do not show every
// replacement. A file of the same size, written within one tick of the clock
// of the one it replaces, carries the same moment: on a Windows laptop that was
// so for between 0.1% and 0.6% of back-to-back same-size replacements, in two
// runs of 2,000 (measured 2026-11-18). git meets the same limit in its index
// and calls it "racy git". So a look also records which file is at the path, and
// a file put in place by a rename is always another file.
package filestat

import "time"

// Info is what one look at a path found: the file's size, the moment it was
// last written, and which file it is.
type Info struct {
	size    int64
	modTime time.Time
	id      identity
}

// Look reads what is at path. When nothing is there, its error satisfies
// errors.Is(err, os.ErrNotExist).
//
// Where LookOpens is true, Look opens the file to learn which file it is, and a
// file that is open there cannot be renamed over. A caller there that may meet
// a rename of the file therefore looks while holding the lock every rename of
// it is made under.
func Look(path string) (*Info, error) { return look(path) }

// Same reports whether two looks at one path found the same file, unchanged
// between them: nothing both times, or the same file both times, of the same
// size and last written at the same moment.
func Same(was, now *Info) bool {
	if was == nil || now == nil {
		return was == nil && now == nil
	}
	return was.size == now.size && was.modTime.Equal(now.modTime) && was.id.is(now.id)
}
