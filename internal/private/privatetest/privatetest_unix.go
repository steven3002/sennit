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

// OnlyThisAccountMayEnter fails the test unless the folder's mode lets its
// owner in and nobody else.
func OnlyThisAccountMayEnter(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("the folder %s is mode %04o, want 0700", dir, perm)
	}
}

// OnlyThisAccountCanRead fails the test unless nobody but the file's owner
// may read or write it.
func OnlyThisAccountCanRead(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("%s is mode %04o, which lets other accounts in", path, perm)
	}
}

// OpenFolderToEveryone makes a folder every account on the machine can enter
// and list.
func OpenFolderToEveryone(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make a folder anyone can enter: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("make a folder anyone can enter: %v", err)
	}
}

// WidenToEveryone lets every account read a file that is already there,
// leaving what is in it alone, the way an earlier version left its database.
func WidenToEveryone(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("let every account read %s: %v", path, err)
	}
}
