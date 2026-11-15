//go:build !windows

package private_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/steven3002/sennit/internal/private"
	"github.com/steven3002/sennit/internal/private/privatetest"
)

// lockProbe makes the test binary act as a second process that looks for a
// lock on the file it names.
const lockProbe = "SENNIT_PRIVATE_LOCK_PROBE"

func TestMain(m *testing.M) {
	if path := os.Getenv(lockProbe); path != "" {
		os.Exit(probe(path))
	}
	os.Exit(m.Run())
}

// probe exits 0 when another process holds a lock on the file, and 3 when
// nobody does.
func probe(path string) int {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 2
	}
	defer file.Close()
	lock := syscall.Flock_t{Type: syscall.F_WRLCK}
	if err := syscall.FcntlFlock(file.Fd(), syscall.F_GETLK, &lock); err != nil {
		return 2
	}
	if lock.Type == syscall.F_UNLCK {
		return 3
	}
	return 0
}

func lockedFromOutside(t *testing.T, path string) bool {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), lockProbe+"="+path)
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true
	case errors.As(err, &exit) && exit.ExitCode() == 3:
		return false
	}
	t.Fatalf("look for the lock from another process: %v", err)
	return false
}

// A lock this process holds on a file survives the file being made private.
//
// SQLite keeps its locks on Unix as POSIX record locks, and those belong to
// the process: closing any descriptor for the file drops every one it holds.
// Making a file private by opening and closing it released SQLite's locks on
// its shared-memory file while a vault was open, and a second process then
// reset that file under the first, which died of SIGBUS in CI.
func TestALockHeldOnAFileSurvivesMakingItPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db-shm")
	if err := os.WriteFile(path, []byte("shared memory"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	held, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer held.Close()
	lock := syscall.Flock_t{Type: syscall.F_WRLCK}
	if err := syscall.FcntlFlock(held.Fd(), syscall.F_SETLK, &lock); err != nil {
		t.Fatalf("take the lock: %v", err)
	}
	if !lockedFromOutside(t, path) {
		t.Fatal("another process does not see the lock even before anything ran")
	}

	for _, step := range []struct {
		name   string
		narrow func(string) error
	}{{"File", private.File}, {"FileIfThere", private.FileIfThere}, {"Path", private.Path}} {
		if err := step.narrow(path); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if !lockedFromOutside(t, path) {
			t.Fatalf("%s released this process's lock on the file", step.name)
		}
	}
	privatetest.OnlyThisAccountCanRead(t, path)
}
