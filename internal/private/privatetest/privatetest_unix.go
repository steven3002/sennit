//go:build !windows

package privatetest

import (
	"os"
	"testing"
)

// OnlyThisAccountMayOpen fails the test unless the file's mode lets its owner
// read and write it and lets nobody else do anything.
func OnlyThisAccountMayOpen(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the file is mode %04o, want 0600: it is a secret", perm)
	}
}

// OpenToEveryone makes a file any account on the machine can read, which is
// what a file must not stay once a secret is in it.
func OpenToEveryone(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("make a file anyone can read: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("make a file anyone can read: %v", err)
	}
}
