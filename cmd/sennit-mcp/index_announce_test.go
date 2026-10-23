package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// rememberThree stores three memories in the vault under home, whose vectors
// reach the index in the order given.
func rememberThree(t *testing.T, home string) {
	t.Helper()
	v, err := vault.Open(context.Background(), vault.Options{
		Home: home, Phrase: testPhrase, Embedder: embedtest.NewStub(), Offline: true,
	})
	if err != nil {
		t.Fatalf("open the vault: %v", err)
	}
	defer v.Close()
	for _, statement := range []string{
		"The harbour gauge at Tidepool reads in centimetres.",
		"The river gauge at Tidepool reads in millimetres.",
		"The tide tables at Tidepool are printed every spring.",
	} {
		if _, err := v.Remember(context.Background(), vault.RememberRequest{
			Statement: statement,
			Context:   "From the fictional Tidepool field guide.",
			Type:      record.TypeFact,
			Tags:      []string{"tidepool"},
		}); err != nil {
			t.Fatalf("remember: %v", err)
		}
	}
}

// damageTheSecondVector changes a byte in the middle of the second record of
// the index's delta under home, which keeps its length whole and stops it
// opening.
func damageTheSecondVector(t *testing.T, home string) {
	t.Helper()
	path := filepath.Join(home, index.DeltaName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the index's delta: %v", err)
	}
	first := 4 + int(binary.LittleEndian.Uint32(raw))
	second := 4 + int(binary.LittleEndian.Uint32(raw[first:]))
	damaged := bytes.Clone(raw)
	damaged[first+second/2] ^= 0xff
	if err := os.WriteFile(path, damaged, 0o600); err != nil {
		t.Fatalf("damage the index's delta: %v", err)
	}
}

// A server that rebuilt its search index as its vault opened says so to its
// host's log, what it found and what it rebuilt, before the line that says what
// it serves. The next start finds nothing to rebuild and says only that.
func TestTheServerTellsItsHostOfASearchIndexItRebuilt(t *testing.T) {
	home := t.TempDir()
	rememberThree(t, home)
	damageTheSecondVector(t, home)

	lines := announced(t, home)
	if len(lines) != 2 {
		t.Fatalf("the server said %d line(s), want the rebuild and then what it serves: %q", len(lines), lines)
	}
	for _, says := range []string{
		"sennit-mcp: ", "search index on this device was damaged", "rebuilt before serving",
		"1 record in it could not be read", "given one, 1 in all",
	} {
		if !strings.Contains(lines[0], says) {
			t.Errorf("the line about the rebuild does not say %q: %q", says, lines[0])
		}
	}
	if !strings.HasPrefix(lines[1], "sennit-mcp: serving ") {
		t.Errorf("the last line is not the one that says what is served: %q", lines[1])
	}

	if again := announced(t, home); len(again) != 1 || !strings.HasPrefix(again[0], "sennit-mcp: serving ") {
		t.Errorf("a server over an index with nothing left to rebuild said %q", again)
	}
}

// While the rebuild holds the server's start it says on stderr that it is
// rebuilding and how far it has got: once as it starts, and every ten seconds
// after, until it is done. Nothing else the vault reports is written there.
func TestTheServerSaysHowFarARebuildHasGot(t *testing.T) {
	start := time.Date(2026, time.October, 22, 9, 0, 0, 0, time.UTC)
	at := start
	var log strings.Builder
	report := rebuildProgress(&log, func() time.Time { return at })

	for _, step := range []struct {
		after time.Duration
		p     vault.Progress
	}{
		{0, vault.Progress{Phase: vault.PhaseModelLoad}},
		{0, vault.Progress{Phase: vault.PhaseIndexRebuild, Total: 300, Unit: vault.UnitRecords}},
		{4 * time.Second, vault.Progress{Phase: vault.PhaseIndexRebuild, Done: 12, Total: 300, Unit: vault.UnitRecords}},
		{10 * time.Second, vault.Progress{Phase: vault.PhaseIndexRebuild, Done: 31, Total: 300, Unit: vault.UnitRecords}},
		{15 * time.Second, vault.Progress{Phase: vault.PhaseIndexRebuild, Done: 45, Total: 300, Unit: vault.UnitRecords}},
		{21 * time.Second, vault.Progress{Phase: vault.PhaseIndexRebuild, Done: 64, Total: 300, Unit: vault.UnitRecords}},
		{95 * time.Second, vault.Progress{Phase: vault.PhaseIndexRebuild, Done: 300, Total: 300, Unit: vault.UnitRecords}},
		{96 * time.Second, vault.Progress{Phase: vault.PhaseUpload, Done: 3, Total: 30, Unit: vault.UnitShards}},
	} {
		at = start.Add(step.after)
		report(step.p)
	}

	want := "sennit-mcp: rebuilding the search index before serving: 300 records to embed\n" +
		"sennit-mcp: still rebuilding the search index: 31 of 300 records embedded, 10s so far\n" +
		"sennit-mcp: still rebuilding the search index: 64 of 300 records embedded, 21s so far\n"
	if got := log.String(); got != want {
		t.Errorf("the server's log reads\n%s\nwant\n%s", got, want)
	}
}
