//go:build !windows

package main

import (
	"os"
	"testing"
)

// onlyThisAccountMayOpen fails the test unless the file's mode lets its owner
// read and write it and lets nobody else do anything.
func onlyThisAccountMayOpen(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the key file is mode %04o, want 0600: it is a secret", perm)
	}
}

// openToEveryone makes a file any account on the machine can read, which is
// what a key file must not stay once the key is in it.
func openToEveryone(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("make a file anyone can read: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("make a file anyone can read: %v", err)
	}
}
