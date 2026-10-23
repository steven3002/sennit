package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/record"
)

// A forget is a removal appended to the index, after the vector it withdraws.
// When the removal is the record a read cannot open, the vector before it reads
// as live, and a forgotten record could be found by meaning again: a recall that
// ranked it failed, since nothing on the device can return it. Once the index is
// found damaged the vault keeps vectors only for records it still holds, so the
// forgotten one is taken out, on the disk as well as in memory.
func TestAForgottenRecordsVectorDoesNotComeBackWhenItsRemovalIsDamaged(t *testing.T) {
	t.Run("a memory", func(t *testing.T) {
		home := t.TempDir()
		v := ledgerVault(t, home)
		kept := rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
		forgotten := rememberFact(t, v, "The old breakwater at Tidepool was demolished.", "breakwater")
		if err := v.Forget(forgotten); err != nil {
			t.Fatalf("forget: %v", err)
		}
		after := rememberFact(t, v, "The tide tables at Tidepool are printed every spring.", "tides")
		v.Close()
		damageIndexRecord(t, home, index.DeltaName, 3, garbleRecord)

		reopened := openWith(t, home, counting())
		if reopened.index.Has(forgotten) {
			t.Error("the forgotten memory can be found by meaning again")
		}
		searchable(t, reopened, "memories still held", kept, after)
		if slices.Contains(recallIDs(t, reopened, "the old breakwater at Tidepool was demolished"), forgotten) {
			t.Error("a recall answered with the forgotten memory")
		}
		want := IndexRepair{Ran: true, Found: index.Damage{Skipped: 1}, Dropped: 1}
		if got := reopened.IndexRepair(); got != want {
			t.Errorf("the vault reports %+v, want %+v", got, want)
		}
		reopened.Close()

		if again := openWith(t, home, counting()); again.index.Has(forgotten) {
			t.Error("the forgotten memory is back in the open after the rebuild")
		}
	})

	// A memory forgotten once it reached the network leaves its removal in the
	// catalog, which no longer names it as held.
	t.Run("a memory already on the network", func(t *testing.T) {
		home := t.TempDir()
		v := ledgerVault(t, home)
		kept := rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
		forgotten := rememberFact(t, v, "The old breakwater at Tidepool was demolished.", "breakwater")
		flushQueue(t, v)
		if err := v.Forget(forgotten); err != nil {
			t.Fatalf("forget: %v", err)
		}
		after := rememberFact(t, v, "The tide tables at Tidepool are printed every spring.", "tides")
		v.Close()
		damageIndexRecord(t, home, index.DeltaName, 3, garbleRecord)

		reopened := openWith(t, home, counting())
		if reopened.index.Has(forgotten) {
			t.Error("the forgotten memory can be found by meaning again")
		}
		searchable(t, reopened, "memories still held", kept, after)
		if slices.Contains(recallIDs(t, reopened, "the old breakwater at Tidepool was demolished"), forgotten) {
			t.Error("a recall answered with the forgotten memory")
		}
		if got := reopened.IndexRepair(); got.Dropped != 1 {
			t.Errorf("the vault reports %+v, want the forgotten memory's vector taken out", got)
		}
	})

	t.Run("a conversation", func(t *testing.T) {
		home := t.TempDir()
		v := ledgerVault(t, home)
		saved, err := v.SaveSession(context.Background(), SaveSessionRequest{
			Title:    "Breakwater demolition",
			Summary:  "Planned the demolition of the old breakwater.",
			Messages: exchange("breakwater", false),
		})
		if err != nil {
			t.Fatalf("save the conversation: %v", err)
		}
		if err := v.ForgetSession(saved.ID); err != nil {
			t.Fatalf("forget the conversation: %v", err)
		}
		after := rememberFact(t, v, "The tide tables at Tidepool are printed every spring.", "tides")
		v.Close()
		damageIndexRecord(t, home, index.DeltaName, 2, garbleRecord)

		reopened := openWith(t, home, counting())
		if reopened.index.Has(saved.ID) {
			t.Error("the forgotten conversation can be found by meaning again")
		}
		searchable(t, reopened, "memories still held", after)
		if got := reopened.IndexRepair(); got.Dropped != 1 {
			t.Errorf("the vault reports %+v, want the forgotten conversation's vector taken out", got)
		}
	})
}

// What the device holds decides what the rebuild gives a vector to, and only
// that. A memory a later one replaced is held, and keeps a vector, because
// recall asked for history returns it. A conversation is held through its head.
// A record restored at catalog depth is not held here, only located, and a
// transcript chunk is never searched, so neither is embedded.
func TestWhatTheDeviceHoldsDecidesWhatTheRebuildEmbeds(t *testing.T) {
	network, located, locatedConversation := writerNetwork(t)
	home := t.TempDir()
	v := ledgerVault(t, home)
	RestoreForTest(t, v, network, HydrateCatalog)

	ctx := context.Background()
	replaced := rememberFact(t, v, "The ferry to Tidepool leaves at quarter past.", "ferry")
	current, err := v.Remember(ctx, RememberRequest{
		Statement:  "The ferry to Tidepool leaves on the hour.",
		Context:    "The timetable changed in the spring.",
		Type:       record.TypeFact,
		Tags:       []string{"ferry"},
		Supersedes: &replaced,
	})
	if err != nil {
		t.Fatalf("remember the replacement: %v", err)
	}
	conversation, err := v.SaveSession(ctx, SaveSessionRequest{
		Title:    "Ferry timetable",
		Summary:  "Checked the new ferry times.",
		Messages: exchange("ferry", false),
	})
	if err != nil {
		t.Fatalf("save the conversation: %v", err)
	}
	last := rememberFact(t, v, "The pier at Tidepool was rebuilt in stone.", "pier")
	head, err := v.Session(conversation.ID)
	if err != nil {
		t.Fatalf("read the conversation: %v", err)
	}
	var chunks []record.ID
	for _, chunk := range head.Chunks {
		chunks = append(chunks, chunk.ID)
	}
	for _, entry := range v.Entries() {
		if entry.Kind == record.KindChunk {
			chunks = append(chunks, entry.ID)
		}
	}
	v.Close()
	damageIndexRecord(t, home, index.DeltaName, 1, garbleRecord)
	damageIndexRecord(t, home, index.DeltaName, 3, garbleRecord)

	embedder := counting()
	reopened := openWith(t, home, embedder)
	searchable(t, reopened, "records held here", replaced, current.ID, conversation.ID, last)
	if got := embedder.embedded(); len(got) != 2 {
		t.Errorf("the open embedded %d record(s), want the replaced memory and the conversation: %q", len(got), got)
	}
	for _, id := range append(append(located, locatedConversation), chunks...) {
		if reopened.index.Has(id) {
			t.Errorf("record %s was given a vector, and this device holds it only as a catalog entry or a chunk", id)
		}
	}
}

// A record whose vector is from another model has a vector. It goes on being
// counted as one the index cannot search, as it was before the damage, and the
// rebuild does not embed it again.
func TestAVectorFromAnotherModelIsNotEmbeddedAgain(t *testing.T) {
	home := t.TempDir()
	v := ledgerVault(t, home)
	older := rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	v.Close()

	vectors, err := index.OpenStore(home, recordSealer(t))
	if err != nil {
		t.Fatalf("open the index: %v", err)
	}
	if err := vectors.Append(index.Entry{ID: older, Model: "an-older-model", Dim: 4, Vector: []float32{1, 0, 0, 0}}); err != nil {
		t.Fatalf("append a vector from another model: %v", err)
	}
	vectors.Close()

	v = ledgerVault(t, home)
	ids := []record.ID{
		rememberFact(t, v, "The river gauge at Tidepool reads in millimetres.", "river"),
		rememberFact(t, v, "The tide tables at Tidepool are printed every spring.", "tides"),
	}
	v.Close()
	damageIndexRecord(t, home, index.DeltaName, 3, garbleRecord)

	embedder := counting()
	reopened := openWith(t, home, embedder)
	searchable(t, reopened, "memories written with this model", ids...)
	if got := embedder.embedded(); len(got) != 1 || !strings.Contains(got[0], "river gauge") {
		t.Errorf("the open embedded %q, want only the memory whose vector was damaged", got)
	}
	health := reopened.IndexHealth()
	if health.Foreign["an-older-model"] != 1 || health.Stale() != 1 {
		t.Errorf("the index reports %+v, want the one vector from another model still counted", health)
	}
}

// The end of the delta cut part way through a record is an append that never
// finished, and the store cuts it off as it opens, as it has since a crash
// mid-append first left one. That is not damage, and it owes no rebuild: on a
// device restored at metadata depth a rebuild would embed every record it holds,
// for the one vector the interrupted write was adding.
func TestAnAppendCutOffAtTheEndOfTheDeltaOwesNoRebuild(t *testing.T) {
	network, restored, _ := writerNetwork(t)
	home := t.TempDir()
	device := ledgerVault(t, home)
	RestoreForTest(t, device, network, HydrateMetadata)
	cut := rememberFact(t, device, "The pier at Tidepool was rebuilt in stone.", "pier")
	device.Close()

	path := filepath.Join(home, index.DeltaName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the delta: %v", err)
	}
	if err := os.WriteFile(path, raw[:len(raw)-12], 0o600); err != nil {
		t.Fatalf("cut the delta: %v", err)
	}

	embedder := counting()
	v := openWith(t, home, embedder)
	if got := embedder.embedded(); len(got) != 0 {
		t.Errorf("the open embedded %d record(s) for an append cut off at the end of the delta", len(got))
	}
	if got := v.IndexRepair(); got != (IndexRepair{}) {
		t.Errorf("the open reports a rebuild for an append cut off at the end of the delta: %+v", got)
	}
	if rebuildOwed(t, v) {
		t.Error("an append cut off at the end of the delta was recorded as owing a rebuild")
	}
	for _, id := range append(restored, cut) {
		if v.index.Has(id) {
			t.Errorf("record %s was given a vector", id)
		}
	}
}

// A record the model will not embed does not stop the rebuild: it stays without
// a vector, found by its words alone, it is counted, and every other record is
// embedded. The vault opens.
func TestARecordTheModelCannotEmbedDoesNotStopTheRebuild(t *testing.T) {
	home, held := restoredAndDamaged(t)
	embedder := counting()
	embedder.before = func(_ int, text string) error {
		if strings.Contains(text, "lighthouse") {
			return errors.New("the text is longer than the model reads")
		}
		return nil
	}

	v := openWith(t, home, embedder)
	want := IndexRepair{Ran: true, Found: index.Damage{Skipped: 1}, Embedded: 3, Failed: 1}
	if got := v.IndexRepair(); got != want {
		t.Errorf("the vault reports %+v, want %+v", got, want)
	}
	if rebuildOwed(t, v) {
		t.Error("a rebuild that embedded all the model would is still recorded as owed")
	}
	var unsearchable int
	for _, id := range held {
		if !v.index.Has(id) {
			unsearchable++
		}
	}
	if unsearchable != 1 {
		t.Errorf("%d record(s) cannot be found by meaning, want only the one the model would not embed", unsearchable)
	}
}
