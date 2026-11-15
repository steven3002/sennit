package private_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steven3002/sennit/internal/private"
	"github.com/steven3002/sennit/internal/private/privatetest"
)

// A folder made here is this account's alone, and so is every file made in
// it afterwards, without each being limited as it is made.
func TestAFolderMadeHereIsThisAccountsAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "parent", "vault")
	made, err := private.Folder(dir)
	if err != nil {
		t.Fatalf("make the folder: %v", err)
	}
	if !made {
		t.Fatalf("a folder that was not there was reported as already there")
	}
	privatetest.OnlyThisAccountMayEnter(t, dir)

	inside := filepath.Join(dir, "made-later.db")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatalf("make a file inside: %v", err)
	}
	privatetest.OnlyThisAccountCanRead(t, inside)

	if who, err := private.Readers(dir); err != nil || who != "" {
		t.Errorf("a folder made here is reported open to %q (%v)", who, err)
	}
}

// A folder that is already there is left as it was, and Readers says who else
// can reach it.
func TestAFolderAlreadyThereIsLeftAsItWas(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	privatetest.OpenFolderToEveryone(t, dir)

	made, err := private.Folder(dir)
	if err != nil {
		t.Fatalf("folder: %v", err)
	}
	if made {
		t.Errorf("a folder already there was reported as made")
	}
	who, err := private.Readers(dir)
	if err != nil {
		t.Fatalf("readers: %v", err)
	}
	if who == "" {
		t.Errorf("a folder every account can enter was reported as this account's alone")
	}
}

// A file another library will open is made private before it does, and one an
// earlier version left open is narrowed.
func TestAFileIsMadePrivateBeforeItIsUsed(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "vault.db")
	if err := private.File(fresh); err != nil {
		t.Fatalf("file: %v", err)
	}
	privatetest.OnlyThisAccountMayOpen(t, fresh)

	older := filepath.Join(dir, "older.db")
	privatetest.OpenToEveryone(t, older)
	if err := os.WriteFile(older, []byte("records an earlier version wrote"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := private.File(older); err != nil {
		t.Fatalf("file: %v", err)
	}
	privatetest.OnlyThisAccountMayOpen(t, older)
	if raw, err := os.ReadFile(older); err != nil || string(raw) != "records an earlier version wrote" {
		t.Errorf("making the file private changed what is in it: %q (%v)", raw, err)
	}

	if err := private.FileIfThere(filepath.Join(dir, "absent.db-wal")); err != nil {
		t.Errorf("a file that is not there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "absent.db-wal")); !os.IsNotExist(err) {
		t.Errorf("FileIfThere made a file that was not there (%v)", err)
	}
}
