//go:build !windows

package filestat

import "os"

// LookOpens is false here: a stat says which file is at a path, by its device
// and inode, without opening it.
const LookOpens = false

// identity keeps the stat itself. os.SameFile compares two stats by the device
// and inode each read when it was taken, and a file put in place by a rename
// has an inode of its own, made while the one it replaced still existed.
type identity struct{ stat os.FileInfo }

func (a identity) is(b identity) bool { return os.SameFile(a.stat, b.stat) }

func look(path string) (*Info, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &Info{size: info.Size(), modTime: info.ModTime(), id: identity{info}}, nil
}
