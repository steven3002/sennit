package rename_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steven3002/sennit/internal/rename"
)

// A finished file takes the place of the one already under its name, and the
// name it was written under is gone.
func TestAFileTakesThePlaceOfTheOneAlreadyThere(t *testing.T) {
	dir := t.TempDir()
	pending, installed := filepath.Join(dir, "base.tmp"), filepath.Join(dir, "base")
	if err := os.WriteFile(installed, []byte("old"), 0o600); err != nil {
		t.Fatalf("write the old file: %v", err)
	}
	if err := os.WriteFile(pending, []byte("new"), 0o600); err != nil {
		t.Fatalf("write the new file: %v", err)
	}

	if err := rename.Over(pending, installed); err != nil {
		t.Fatalf("rename over the old file: %v", err)
	}
	if got, err := os.ReadFile(installed); err != nil || string(got) != "new" {
		t.Errorf("the name reads %q (%v) after the rename, want the new file", got, err)
	}
	if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the pending name is still there after the rename (%v)", err)
	}
}

// Something else on the machine can have the old file open at the moment it is
// replaced: a scanner, an indexer, a backup. It lets go a moment later, and the
// new file is in place once it has. On Windows the first try is refused, and a
// refusal taken at once would fail a write over a reader that was nearly done.
func TestAFileIsPutInPlaceOnceWhoeverHasTheOldOneOpenLetsGo(t *testing.T) {
	dir := t.TempDir()
	pending, installed := filepath.Join(dir, "base.tmp"), filepath.Join(dir, "base")
	if err := os.WriteFile(installed, []byte("old"), 0o600); err != nil {
		t.Fatalf("write the old file: %v", err)
	}
	if err := os.WriteFile(pending, []byte("new"), 0o600); err != nil {
		t.Fatalf("write the new file: %v", err)
	}
	reading, err := os.Open(installed)
	if err != nil {
		t.Fatalf("open the old file as another reader would: %v", err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(300 * time.Millisecond)
		reading.Close()
	}()

	err = rename.Over(pending, installed)
	<-released
	if err != nil {
		t.Fatalf("rename over a file another reader had open for a moment: %v", err)
	}
	if got, err := os.ReadFile(installed); err != nil || string(got) != "new" {
		t.Errorf("the name reads %q (%v) after the rename, want the new file", got, err)
	}
}

// A rename that fails for a reason waiting cannot change says so at once.
func TestARenameOfAFileThatIsNotThereFailsAtOnce(t *testing.T) {
	dir := t.TempDir()
	started := time.Now()
	err := rename.Over(filepath.Join(dir, "never-written"), filepath.Join(dir, "base"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("renaming a file that is not there ended with %v", err)
	}
	if waited := time.Since(started); waited > time.Second {
		t.Errorf("the failure took %s to report, and no wait would have changed it", waited)
	}
}
