//go:build !windows

package main

import "os"

// keepPrivate leaves a file readable and writable by its owner and by nobody
// else.
func keepPrivate(file *os.File) error { return file.Chmod(0o600) }
