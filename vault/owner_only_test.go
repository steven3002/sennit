package vault_test

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/internal/private/privatetest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

func openOffline(t *testing.T, home string) *vault.Vault {
	t.Helper()
	v, err := vault.Open(context.Background(), vault.Options{
		Home: home, Phrase: testPhrase, Embedder: embedtest.NewStub(), Offline: true,
	})
	if err != nil {
		t.Fatalf("open the vault: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

// A vault in a folder it made is this account's alone: the folder, the
// database, and every file made beside it, including those SQLite adds.
func TestANewVaultIsThisAccountsAlone(t *testing.T) {
	home := filepath.Join(t.TempDir(), "new-vault")
	v := openOffline(t, home)
	remember(t, v, "The harbour gauge at Tidepool reads in centimetres.", record.TypeFact, "harbour")

	if who := v.FolderOpenTo(); who != "" {
		t.Errorf("a folder the vault made is reported open to %q", who)
	}
	privatetest.OnlyThisAccountMayEnter(t, home)
	privatetest.OnlyThisAccountMayOpen(t, filepath.Join(home, "vault.db"))

	files := 0
	err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		files++
		privatetest.OnlyThisAccountCanRead(t, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the vault: %v", err)
	}
	if files < 2 {
		t.Errorf("the vault holds %d file(s) after a write, so there was little to check", files)
	}
}

// A folder that was already there and that others can read is left as it was
// set, and the vault says who can read it. Its own database is still made
// this account's alone.
func TestAVaultInAFolderOthersCanReadSaysSo(t *testing.T) {
	home := filepath.Join(t.TempDir(), "shared")
	privatetest.OpenFolderToEveryone(t, home)

	v := openOffline(t, home)
	if who := v.FolderOpenTo(); who == "" {
		t.Errorf("a folder every account can enter was reported as this account's alone")
	}
	remember(t, v, "Written into a folder every account can enter.", record.TypeFact, "shared")

	// Each file Sennit made there is limited on its own, since in this folder
	// none of them can lean on the folder's access.
	files := 0
	err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		files++
		privatetest.OnlyThisAccountMayOpen(t, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the vault: %v", err)
	}
	if files < 4 {
		t.Errorf("the vault holds %d file(s) after a write, so there was little to check", files)
	}
}

// A database an earlier version left open to others is narrowed at the next
// open, and keeps what it holds.
func TestAnEarlierVaultsDatabaseIsNarrowed(t *testing.T) {
	home := filepath.Join(t.TempDir(), "older-vault")
	first := openOffline(t, home)
	id := remember(t, first, "Kept by an earlier version of the vault.", record.TypeFact, "older")
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db := filepath.Join(home, "vault.db")
	privatetest.WidenToEveryone(t, db)

	again := openOffline(t, home)
	privatetest.OnlyThisAccountMayOpen(t, db)
	memory, _, err := again.FetchMemory(context.Background(), id)
	if err != nil || memory.Statement != "Kept by an earlier version of the vault." {
		t.Errorf("the record written before the database was narrowed is gone: %v", err)
	}
}
