package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
	"github.com/steven3002/sennit/vault"
)

const testPhrase = "abandon abandon abandon abandon abandon abandon " +
	"abandon abandon abandon abandon abandon about"

// catalogueTwoRecords writes two entries into the catalog under home, sealed
// with the key the phrase derives, as two flushes would have.
func catalogueTwoRecords(t *testing.T, home string) {
	t.Helper()
	seed, err := keys.SeedFromPhrase(testPhrase)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	hierarchy, err := keys.Derive(seed)
	if err != nil {
		t.Fatalf("derive keys: %v", err)
	}
	sealer, err := seal.New(hierarchy.Manifest, hierarchy.Content)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	defer sealer.Close()
	log, err := manifest.OpenLog(home, sealer)
	if err != nil {
		t.Fatalf("open the catalog: %v", err)
	}
	catalog, err := manifest.Load(log)
	if err != nil {
		t.Fatalf("load the catalog: %v", err)
	}
	defer catalog.Close()
	for range 2 {
		id, err := record.NewID()
		if err != nil {
			t.Fatalf("new id: %v", err)
		}
		if err := catalog.Append(manifest.Entry{
			ID: id, Kind: record.KindMemory, Type: record.TypeFact, Version: 1,
			ObjectRef: "object-" + id.String(), WrittenAt: record.Now(),
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

// announced is what the server tells its host's log as it starts serving the
// vault under home.
func announced(t *testing.T, home string) []string {
	t.Helper()
	v, err := vault.Open(context.Background(), vault.Options{
		Home: home, Phrase: testPhrase, Embedder: embedtest.NewStub(), Offline: true,
	})
	if err != nil {
		t.Fatalf("open the vault: %v", err)
	}
	defer v.Close()
	var said strings.Builder
	announce(&said, v)
	return strings.Split(strings.TrimSuffix(said.String(), "\n"), "\n")
}

// A host shows a server's stderr to the person running it, and a server has no
// other way to tell that person that the vault it opened had a change cut off
// by a crash. It is said before the line that says what is being served.
func TestTheServerTellsItsHostOfACatalogChangeACrashCutOff(t *testing.T) {
	home := t.TempDir()
	catalogueTwoRecords(t, home)
	path := filepath.Join(home, manifest.LogName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the catalog's log: %v", err)
	}
	if err := os.WriteFile(path, raw[:len(raw)-40], 0o600); err != nil {
		t.Fatalf("cut the catalog's log: %v", err)
	}

	lines := announced(t, home)
	if len(lines) != 2 {
		t.Fatalf("the server said %d line(s), want the dropped change and then what it serves: %q", len(lines), lines)
	}
	for _, says := range []string{"sennit-mcp: ", "cut off by a crash", "one record's latest change", "run again"} {
		if !strings.Contains(lines[0], says) {
			t.Errorf("the line about the dropped change does not say %q: %q", says, lines[0])
		}
	}
	if !strings.HasPrefix(lines[1], "sennit-mcp: serving ") {
		t.Errorf("the last line is not the one that says what is served: %q", lines[1])
	}

	// The next start finds nothing cut off and says only what it serves.
	if again := announced(t, home); len(again) != 1 || !strings.HasPrefix(again[0], "sennit-mcp: serving ") {
		t.Errorf("a server over a catalog with nothing cut off said %q", again)
	}
}
