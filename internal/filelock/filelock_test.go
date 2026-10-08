package filelock_test

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steven3002/sennit/internal/filelock"
)

// The lock is a claim about two operating-system processes, so the other holder
// here is a real one: this test binary run again, holding the lock until it is
// told to stop or is killed.

// holderEnv makes this test binary hold the lock at the path it names instead
// of running tests.
const holderEnv = "SENNIT_TEST_HOLD_LOCK"

func TestMain(m *testing.M) {
	if path := os.Getenv(holderEnv); path != "" {
		os.Exit(holdUntilStdinCloses(path))
	}
	os.Exit(m.Run())
}

// holdUntilStdinCloses takes the lock, says so on stdout, and keeps it until
// its stdin closes.
func holdUntilStdinCloses(path string) int {
	mutex, err := filelock.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer mutex.Close()
	if err := mutex.Lock(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("held")
	io.Copy(io.Discard, os.Stdin)
	if err := mutex.Unlock(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// A holder is another process with the lock taken.
type holder struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

// anotherProcessHolds starts a process that takes the lock at path, and returns
// once it has it.
func anotherProcessHolds(t *testing.T, path string) *holder {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestMain")
	cmd.Env = append(os.Environ(), holderEnv+"="+path)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the other process: %v", err)
	}
	t.Cleanup(func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	})
	said, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || said != "held\n" {
		t.Fatalf("the other process did not take the lock: %q, %v", said, err)
	}
	return &holder{cmd: cmd, stdin: stdin}
}

// lockInBackground takes the lock at path on another goroutine and reports
// when it has it.
func lockInBackground(t *testing.T, path string) <-chan error {
	t.Helper()
	mutex, err := filelock.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { mutex.Close() })
	taken := make(chan error, 1)
	go func() { taken <- mutex.Lock() }()
	return taken
}

// stillWaiting fails the test if the lock was taken within a fifth of a second.
func stillWaiting(t *testing.T, taken <-chan error) {
	t.Helper()
	select {
	case err := <-taken:
		t.Fatalf("the lock was taken while another holder had it (err %v)", err)
	case <-time.After(200 * time.Millisecond):
	}
}

// eventuallyTaken fails the test unless the lock is taken within ten seconds.
func eventuallyTaken(t *testing.T, taken <-chan error) {
	t.Helper()
	select {
	case err := <-taken:
		if err != nil {
			t.Fatalf("lock: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the lock was never taken once its holder let it go")
	}
}

func TestALockAnotherProcessHoldsIsWaitedFor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	other := anotherProcessHolds(t, path)

	taken := lockInBackground(t, path)
	stillWaiting(t, taken)

	other.stdin.Close()
	eventuallyTaken(t, taken)
}

// A process killed while it holds the lock must not leave it held, or every
// later process would wait for it forever.
func TestALockIsReleasedWhenTheProcessHoldingItDies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	other := anotherProcessHolds(t, path)

	taken := lockInBackground(t, path)
	stillWaiting(t, taken)

	if err := other.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the other process: %v", err)
	}
	eventuallyTaken(t, taken)
}

// Two handles in one process exclude each other as two processes do, which is
// what lets a test stand a second handle in for a second process.
func TestTwoHoldersInOneProcessExcludeEachOther(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	first, err := filelock.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer first.Close()
	if err := first.Lock(); err != nil {
		t.Fatalf("lock: %v", err)
	}

	taken := lockInBackground(t, path)
	stillWaiting(t, taken)

	if err := first.Unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	eventuallyTaken(t, taken)
}
