package private

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Folder makes dir, with any parents it needs, when it is not there yet, and
// limits the folder it made to this account. It reports whether it made it.
//
// A folder that is already there is left exactly as it was, because it may be
// shared on purpose. Readers says whether it is.
//
// On Windows the limit is an access list that every file later made inside
// inherits, so the vault's files are this account's alone without each one
// being limited as it is made, including the ones SQLite makes beside its
// database. On Unix it is the mode 0700.
func Folder(dir string) (bool, error) {
	if _, err := os.Stat(dir); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	if err := restrictFolder(dir); err != nil {
		return true, fmt.Errorf("limit %s to this account: %w", dir, err)
	}
	return true, nil
}

// File limits the file at path to this account, making it empty first when it
// is not there yet.
//
// It is for a file another library creates, such as SQLite's database. Made
// here first, the file starts private, and SQLite gives the files it adds
// beside it the same mode on Unix. A file an earlier version made more open
// is narrowed.
func File(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := Restrict(file); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// FileIfThere limits the file at path to this account when there is one, and
// does nothing when there is not.
func FileIfThere(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := Restrict(file); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// Readers names who besides this account can read what is in dir, or returns
// "" when nobody can.
//
// On Unix that is the folder's mode: anyone it lets in can see the names and
// sizes of what is there, and read any file whose own mode allows it. On
// Windows it is the folder's access list, read for the entries a file made
// inside would inherit as well as those for the folder itself, since every
// account may by default reach a file by its path through a folder it cannot
// open.
func Readers(dir string) (string, error) { return readers(dir) }
