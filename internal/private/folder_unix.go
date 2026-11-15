//go:build !windows

package private

import (
	"fmt"
	"os"
)

// restrictFolder sets the mode outright, because the one asked of MkdirAll is
// narrowed by the umask and never widened, and a parent made along the way is
// not this folder.
func restrictFolder(dir string) error { return os.Chmod(dir, 0o700) }

func readers(dir string) (string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	switch perm := info.Mode().Perm(); {
	case perm&0o007 != 0:
		return fmt.Sprintf("every account on this machine, since its mode is %04o", perm), nil
	case perm&0o070 != 0:
		return fmt.Sprintf("the accounts in its group, since its mode is %04o", perm), nil
	}
	return "", nil
}
