//go:build !windows

package private

import "os"

// Restrict leaves a file readable and writable by its owner and by nobody
// else.
func Restrict(file *os.File) error { return file.Chmod(0o600) }
