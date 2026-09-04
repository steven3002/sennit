package manifest_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
)

func openCatalog(t *testing.T, dir string) *manifest.Manifest {
	t.Helper()
	hierarchy, err := keys.Derive(keys.Seed{7})
	if err != nil {
		t.Fatalf("derive keys: %v", err)
	}
	sealer, err := seal.New(hierarchy.Manifest, hierarchy.Content)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	log, err := manifest.OpenLog(dir, sealer)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	catalog, err := manifest.Load(log)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(func() {
		catalog.Close()
		sealer.Close()
	})
	return catalog
}

func entry(t *testing.T, ref string) manifest.Entry {
	t.Helper()
	id, err := record.NewID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}
	return manifest.Entry{
		ID:        id,
		Kind:      record.KindMemory,
		Type:      record.TypeFact,
		Version:   1,
		ObjectRef: ref,
		SlabID:    "slab",
		Bytes:     857,
		WrittenAt: record.Now(),
	}
}

func TestAppendSurvivesAReopen(t *testing.T) {
	dir := t.TempDir()

	catalog := openCatalog(t, dir)
	first := entry(t, "aa")
	second := entry(t, "bb")
	for _, e := range []manifest.Entry{first, second} {
		if err := catalog.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	catalog.Close()

	reopened := openCatalog(t, dir)
	if reopened.Len() != 2 {
		t.Fatalf("catalog holds %d entries after a reopen, want 2", reopened.Len())
	}
	got, err := reopened.Lookup(first.ID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ObjectRef != "aa" {
		t.Fatalf("entry resolves to %q", got.ObjectRef)
	}
}

// Repack rewrites object refs, so a later entry for one record has to win
// without the record becoming two records.
func TestAppendSupersedesAnEarlierLocation(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())

	first := entry(t, "before-repack")
	if err := catalog.Append(first); err != nil {
		t.Fatalf("append: %v", err)
	}
	moved := first
	moved.ObjectRef = "after-repack"
	if err := catalog.Append(moved); err != nil {
		t.Fatalf("append: %v", err)
	}

	if catalog.Len() != 1 {
		t.Fatalf("catalog holds %d entries for one record", catalog.Len())
	}
	got, err := catalog.Lookup(first.ID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ObjectRef != "after-repack" {
		t.Fatalf("entry resolves to %q, want the newer location", got.ObjectRef)
	}
}

func TestLookupReportsAMissingRecord(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	id, _ := record.NewID()
	if _, err := catalog.Lookup(id); !errors.Is(err, manifest.ErrNotFound) {
		t.Fatalf("lookup of an absent record returned %v", err)
	}
}

// The catalog names every record the vault holds, so it must not be readable
// from disk.
func TestLogIsEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	catalog := openCatalog(t, dir)
	if err := catalog.Append(entry(t, "an-object-ref-worth-hiding")); err != nil {
		t.Fatalf("append: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, manifest.LogName))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, plaintext := range []string{"an-object-ref-worth-hiding", "memory", "fact"} {
		if contains(raw, plaintext) {
			t.Fatalf("the log holds %q in the clear", plaintext)
		}
	}
}

func contains(haystack []byte, needle string) bool {
	for n := 0; n+len(needle) <= len(haystack); n++ {
		if string(haystack[n:n+len(needle)]) == needle {
			return true
		}
	}
	return false
}
