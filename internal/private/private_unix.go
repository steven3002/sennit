//go:build !windows

package private

import "os"

// Restrict leaves a file readable and writable by its owner and by nobody
// else.
func Restrict(file *os.File) error { return file.Chmod(0o600) }

// restrictPath does what Restrict does, by name, without opening the file.
func restrictPath(path string) error { return os.Chmod(path, 0o600) }
