// Package filestat tells, from two stats of one path, whether the file there is
// the one that was there before, unchanged.
//
// It is how a process holding a vault open notices that another process has
// written to the files they share. Every such file is either appended to, which
// changes its size, or replaced whole by a rename, which puts another file at
// the path, and both show in a stat. Reading the files again to find out would
// cost what loading them costs, and the question is asked before every answer a
// server gives.
package filestat

import (
	"os"
	"runtime"
)

// Same reports whether two stats of one path found the same file, unchanged
// between them: absent both times, or present both times with the same size,
// last written at the same time, and, outside Windows, the same file.
//
// Windows is not asked whether it is the same file. os.SameFile opens a file
// there to find out, and the files a vault replaces by a rename are opened only
// under the lock that the rename is made under, because a file that is open on
// Windows cannot be renamed over. A file put in place by a rename was written
// at another time than the one it replaced, so its size and the time it was
// last written stand for it there.
func Same(was, now os.FileInfo) bool {
	if was == nil || now == nil {
		return was == nil && now == nil
	}
	if was.Size() != now.Size() || !was.ModTime().Equal(now.ModTime()) {
		return false
	}
	return runtime.GOOS == "windows" || os.SameFile(was, now)
}
