package vault

import (
	"context"
	"testing"

	"github.com/steven3002/sennit/embed"
	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// tidepoolNetwork has a writer store two memories and a conversation and flush
// them to the network the test stands in for. It returns the writer, the
// memories' ids and what the network holds.
func tidepoolNetwork(t *testing.T) (*Vault, []record.ID, []StoredObject) {
	t.Helper()
	writer := OpenWithLedgerForTest(t, t.TempDir())
	memories := []record.ID{
		rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour"),
		rememberFact(t, writer, "The river gauge at Tidepool reads in millimetres.", "river"),
	}
	if _, err := writer.SaveSession(context.Background(), SaveSessionRequest{
		Title:    "Tidepool gauge survey",
		Messages: exchange("survey", true),
	}); err != nil {
		t.Fatalf("save the conversation: %v", err)
	}

	if got, want := restoration(t, writer), (Restoration{Untouched: true}); got != want {
		t.Errorf("a writer whose records have not reached the network reports %+v, want %+v", got, want)
	}
	return writer, memories, FlushToNetworkForTest(t, writer)
}

// restoration reads what a device has restored, and fails the test if it cannot.
func restoration(t *testing.T, v *Vault) Restoration {
	t.Helper()
	got, err := v.Restoration()
	if err != nil {
		t.Fatalf("restoration: %v", err)
	}
	return got
}

// A device can tell, from what it already keeps, whether it has restored the
// vault, how much of what its catalog names it does not hold, and how many of
// the records it holds have no search vector.
//
// The catalog and the slab ledger are written by every flush and at every depth
// of a hydrate, and a hydrate at catalog depth stops there. So a device that
// never hydrated has named nothing in either, one hydrated at catalog depth
// names records it holds none of, and a writer or a device restored deeper
// holds everything its catalog names. Only a device restored at index depth
// holds a search vector for each of them.
func TestRestorationTellsADeviceThatHasNotRestoredTheVaultFromOneThatHas(t *testing.T) {
	writer, memories, network := tidepoolNetwork(t)

	fresh := OpenWithLedgerForTest(t, t.TempDir())
	catalogued := OpenWithLedgerForTest(t, t.TempDir())
	RestoreForTest(t, catalogued, network, HydrateCatalog)
	restored := OpenWithLedgerForTest(t, t.TempDir())
	RestoreForTest(t, restored, network, HydrateMetadata)
	indexed := OpenWithLedgerForTest(t, t.TempDir())
	RestoreForTest(t, indexed, network, HydrateIndex)
	// A hydrate at metadata depth restores every record and none of their
	// search vectors: the two memories and the conversation rebuilt.
	withoutVectors := Unembedded{Memories: 2, Sessions: 1}

	// A process holds the catalog it loaded when it opened the vault, so one
	// that opened this directory before another process hydrated it names
	// nothing in its catalog. The ledger they share says otherwise.
	home := t.TempDir()
	opened := OpenWithLedgerForTest(t, home)
	RestoreForTest(t, OpenWithLedgerForTest(t, home), network, HydrateMetadata)

	for _, tc := range []struct {
		name   string
		device *Vault
		want   Restoration
	}{
		{"the writer, after its flush", writer, Restoration{}},
		{"a device that never hydrated", fresh, Restoration{Untouched: true}},
		{"a device hydrated at catalog depth", catalogued, Restoration{Memories: 2, Chunks: 1}},
		{"a device hydrated at metadata depth", restored, Restoration{Unembedded: withoutVectors}},
		{"a device hydrated at index depth", indexed, Restoration{}},
		{"a device another process hydrated after this one opened it", opened,
			Restoration{Unembedded: withoutVectors}},
	} {
		if got := restoration(t, tc.device); got != tc.want {
			t.Errorf("%s reports %+v, want %+v", tc.name, got, tc.want)
		}
	}

	// Forgetting everything it restored leaves a device that did restore the
	// vault, and a vault that is empty as far as this device knows.
	for _, id := range memories {
		if err := restored.Forget(id); err != nil {
			t.Fatalf("forget %s: %v", id, err)
		}
	}
	sessions, err := restored.ListSessions(local.SessionQuery{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	for _, session := range sessions {
		if err := restored.ForgetSession(session.ID); err != nil {
			t.Fatalf("forget session %s: %v", session.ID, err)
		}
	}
	if got, want := restoration(t, restored), (Restoration{}); got != want {
		t.Errorf("a restored device that forgot everything reports %+v, want %+v", got, want)
	}
}

// A memory is restored when this device can list, count and rank it, not when
// it merely holds the body.
//
// Reading a memory on demand keeps its body on this device and nothing else, so
// on a device hydrated at catalog depth it is still missing from every listing
// and every count. Records this device wrote itself are not missing from
// anything, and they do not hide the ones that are.
func TestAMemoryIsRestoredByItsMetadataAndNotByItsBody(t *testing.T) {
	writer, memories, network := tidepoolNetwork(t)
	catalogued := OpenWithLedgerForTest(t, t.TempDir())
	RestoreForTest(t, catalogued, network, HydrateCatalog)

	// What a read from the network leaves behind: the body, kept so the next
	// read is free.
	body, err := writer.local.GetBody(memories[0])
	if err != nil {
		t.Fatalf("the writer's body: %v", err)
	}
	if err := catalogued.local.PutBody(memories[0], record.KindMemory, body); err != nil {
		t.Fatalf("keep the body read on demand: %v", err)
	}
	rememberFact(t, catalogued, "The canal gauge at Tidepool reads in inches.", "canal")

	want := Restoration{Memories: 2, Chunks: 1}
	if got := restoration(t, catalogued); got != want {
		t.Errorf("a catalog-depth device holding one body read on demand and one memory of its own "+
			"reports %+v, want %+v", got, want)
	}
}

// renamed is the stub embedder under another model's name, which is how a vault
// whose vectors one model made looks to a build that embeds with another.
type renamed struct{ *embedtest.Stub }

func (renamed) Model() embed.Model {
	return embed.Model{Name: "a-later-model", Dim: embedtest.StubModel.Dim}
}

// A device counts the records it holds that have no search vector: every one a
// hydrate at metadata depth restored, until a hydrate at index depth has run
// over them. A record whose vector another model made is not counted, because
// it has a vector, and the index's health counts it as one this vault cannot
// search.
func TestRestorationCountsTheRecordsHeldWithoutASearchVector(t *testing.T) {
	_, _, network := tidepoolNetwork(t)
	device := OpenWithLedgerForTest(t, t.TempDir())

	RestoreForTest(t, device, network, HydrateMetadata)
	if got, want := restoration(t, device), (Restoration{Unembedded: Unembedded{Memories: 2, Sessions: 1}}); got != want {
		t.Errorf("a device restored at metadata depth reports %+v, want %+v", got, want)
	}
	RestoreForTest(t, device, network, HydrateIndex)
	if got := restoration(t, device); got != (Restoration{}) {
		t.Errorf("once a hydrate at index depth has run over it, the device reports %+v, want every record "+
			"it holds searchable", got)
	}

	home := t.TempDir()
	writer := ledgerVault(t, home)
	rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	rememberFact(t, writer, "The river gauge at Tidepool reads in millimetres.", "river")
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	later, err := Open(context.Background(), Options{
		Home: home, Phrase: ledgerPhrase, Embedder: renamed{embedtest.NewStub()}, Offline: true,
	})
	if err != nil {
		t.Fatalf("open the vault with another model: %v", err)
	}
	t.Cleanup(func() { later.Close() })
	if stale := later.IndexHealth().Stale(); stale != 2 {
		t.Fatalf("the index counts %d vector(s) from another model, want the 2 memories'", stale)
	}
	if got := restoration(t, later); got.Unembedded != (Unembedded{}) {
		t.Errorf("a device whose vectors another model made counts %+v without one", got.Unembedded)
	}
}
