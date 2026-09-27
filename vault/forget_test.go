package vault

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
)

// rememberFact stores one memory carrying one tag and returns its id.
func rememberFact(t *testing.T, v *Vault, statement, tag string) record.ID {
	t.Helper()
	result, err := v.Remember(context.Background(), RememberRequest{
		Statement: statement,
		Context:   "From the fictional Tidepool field guide, written to be forgotten or kept.",
		Type:      record.TypeFact,
		Tags:      []string{tag},
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	return result.ID
}

// flushQueue writes everything queued, with the test standing in for the
// network, and returns the records the network was handed.
//
// A vault test cannot flush through the packer. Outside its own package a
// store's network half is only ever the real SDK, and an offline vault has no
// store at all. So this runs a flush around that one call: it claims the queue
// the way the packer does, which makes what it is handed exactly what a flush
// would upload, and lands the batch through the vault's own bookkeeping.
func flushQueue(t *testing.T, v *Vault) map[record.ID]bool {
	t.Helper()
	claimed, err := v.local.ClaimQueued("test flush", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("claim the queue: %v", err)
	}
	return land(t, v, claimed)
}

// land completes a flush of records already claimed from the queue.
//
// The network is taken to have written every one of them into one slab. From
// there it is what a flush does: the slab reaches the ledger, the records reach
// the catalog, and their rows leave the queue.
func land(t *testing.T, v *Vault, claimed []local.QueuedBlob) map[record.ID]bool {
	t.Helper()
	written := make(map[record.ID]bool, len(claimed))
	if len(claimed) == 0 {
		return written
	}
	var (
		result packer.Result
		ids    []record.ID
		bytes  int64
	)
	for _, blob := range claimed {
		var ref sia.ObjectRef
		copy(ref.ID[:], blob.ID[:])
		result.Records = append(result.Records, packer.Queued{
			ID:    blob.ID,
			Kind:  blob.Kind,
			Blob:  store.Blob{CID: blob.CID, Payload: blob.Payload},
			Since: blob.QueuedAt,
		})
		result.Flush.Written = append(result.Flush.Written, store.Written{
			CID:       blob.CID,
			ObjectRef: ref,
			SlabID:    probeSlab,
			Bytes:     len(blob.Payload),
		})
		ids = append(ids, blob.ID)
		bytes += int64(len(blob.Payload))
		written[blob.ID] = true
	}
	result.Flush.Slabs = []sia.SlabID{probeSlab}

	if err := v.recordSlabs(store.SlabsWritten{Slabs: result.Flush.Slabs, Records: len(claimed), Bytes: bytes}); err != nil {
		t.Fatalf("record the slab: %v", err)
	}
	if err := v.recordFlush(result); err != nil {
		t.Fatalf("catalog the flush: %v", err)
	}
	if err := v.local.DropQueued(ids); err != nil {
		t.Fatalf("drop the flushed records from the queue: %v", err)
	}
	return written
}

// recallIDs runs one recall and returns the ids it answered with.
func recallIDs(t *testing.T, v *Vault, query string) []record.ID {
	t.Helper()
	found, err := v.Recall(context.Background(), recall.Request{Query: query, Limit: 5})
	if err != nil {
		t.Fatalf("recall %q: %v", query, err)
	}
	ids := make([]record.ID, 0, len(found.Hits))
	for _, hit := range found.Hits {
		ids = append(ids, hit.ID())
	}
	return ids
}

// A memory forgotten before its first flush is gone from everywhere it was,
// and the flush that follows does not write it.
//
// Until that flush a memory exists on this device alone: its body, its ranking
// rows, its vector, and its sealed payload in the queue. It has no catalog entry
// yet, and forgetting it used to stop at exactly that, removing nothing. Recall
// is checked in the process that did the forgetting, because the searchable
// index is loaded once when the vault opens, so a vector removed only from disk
// goes on answering queries until something reopens.
func TestAMemoryForgottenBeforeItsFlushIsGoneEverywhere(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	forgotten := rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	kept := rememberFact(t, v, "The river gauge at Tidepool reads in millimetres.", "river")
	if pending := v.Pending(); pending != 2 {
		t.Fatalf("%d record(s) queued, want both: nothing here has been flushed", pending)
	}
	indexed := v.IndexHealth().Indexed

	if err := v.Forget(forgotten); err != nil {
		t.Fatalf("forget a memory still waiting for its flush: %v", err)
	}

	// The device: the body, and the ranking rows beside it.
	if _, err := v.local.GetBody(forgotten); !errors.Is(err, local.ErrNotFound) {
		t.Errorf("the body is still on the device: %v", err)
	}
	if _, err := v.local.RankingMetaFor(forgotten); !errors.Is(err, local.ErrNotFound) {
		t.Errorf("the ranking metadata is still on the device: %v", err)
	}
	lexical, err := v.local.SearchLexical("harbour centimetres", 10)
	if err != nil {
		t.Fatalf("lexical search: %v", err)
	}
	if slices.Contains(lexical, forgotten) {
		t.Error("the lexical index still finds the forgotten memory by its words")
	}
	vocabulary, err := v.TagVocabulary(0)
	if err != nil {
		t.Fatalf("tag vocabulary: %v", err)
	}
	for _, tag := range vocabulary {
		if tag.Tag == "harbour" {
			t.Errorf("the forgotten memory's tag is still counted on %d record(s)", tag.Records)
		}
	}

	// The vector, on disk and in this process's searchable index.
	stored, err := v.vectors.Hydrate()
	if err != nil {
		t.Fatalf("read the persisted index: %v", err)
	}
	for _, entry := range stored {
		if entry.ID == forgotten {
			t.Error("the persisted index still holds the forgotten vector")
		}
	}
	if v.index.Has(forgotten) {
		t.Error("the searchable index still holds the forgotten vector")
	}
	if got := v.IndexHealth().Indexed; got != indexed-1 {
		t.Errorf("the index reports %d searchable vector(s), want %d", got, indexed-1)
	}

	// Recall, in the process that did the forgetting.
	answered := recallIDs(t, v, "what does the Tidepool gauge read in")
	if slices.Contains(answered, forgotten) {
		t.Error("recall still answers with the forgotten memory")
	}
	if !slices.Contains(answered, kept) {
		t.Error("recall lost the memory that was not forgotten")
	}

	// The queue, and the flush that follows it.
	if pending := v.Pending(); pending != 1 {
		t.Errorf("%d record(s) queued after the forget, want only the one kept", pending)
	}
	written := flushQueue(t, v)
	if written[forgotten] {
		t.Error("the flush wrote the forgotten memory to the network")
	}
	// Without this, the check above would pass on a flush that wrote nothing.
	if !written[kept] {
		t.Fatal("the flush did not write the memory still queued")
	}
	if _, err := v.manifest.Lookup(forgotten); !errors.Is(err, manifest.ErrNotFound) {
		t.Errorf("the catalog holds the forgotten memory: %v", err)
	}
	if _, err := v.manifest.Lookup(kept); err != nil {
		t.Errorf("the catalog lost the memory that was flushed: %v", err)
	}
}

// A memory that did reach the network leaves this process's searches too.
//
// Forgetting it used to take its vector off the disk and leave it in the
// searchable index, which lives until the vault closes. The next recall in the
// same process retrieved the forgotten memory, found nothing on the device or in
// the catalog to read it from, and failed outright, and a restatement of it was
// reported as conflicting with a memory that was gone.
func TestAForgottenMemoryLeavesSearchInTheSameProcess(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	statement := "The harbour gauge at Tidepool reads in centimetres."
	forgotten := rememberFact(t, v, statement, "harbour")
	kept := rememberFact(t, v, "The river gauge at Tidepool reads in millimetres.", "river")
	if written := flushQueue(t, v); !written[forgotten] || !written[kept] {
		t.Fatalf("the flush wrote %d of the 2 memories", len(written))
	}
	if err := v.Forget(forgotten); err != nil {
		t.Fatalf("forget a catalogued memory: %v", err)
	}

	answered := recallIDs(t, v, "what does the Tidepool gauge read in")
	if slices.Contains(answered, forgotten) {
		t.Error("recall still answers with the forgotten memory")
	}
	if !slices.Contains(answered, kept) {
		t.Error("recall lost the memory that was not forgotten")
	}

	restated, err := v.Remember(context.Background(), RememberRequest{
		Statement: statement,
		Context:   "The same statement again, after the first was forgotten.",
		Type:      record.TypeFact,
		Tags:      []string{"harbour"},
	})
	if err != nil {
		t.Fatalf("remember the statement again: %v", err)
	}
	for _, neighbour := range restated.Neighbours {
		if neighbour.ID == forgotten {
			t.Error("a restatement was compared against the forgotten memory")
		}
	}
}

// A memory a flush is already writing is refused with nothing removed, and is
// forgotten like any other once that flush has landed.
//
// The flush read the sealed payload when it claimed the record, and it
// catalogues the record when it lands whatever the queue says by then. Removing
// the rest and reporting success would leave a record in the catalog, and on
// the network, that this device believes is gone.
func TestAMemoryAFlushIsWritingIsForgottenOnceItLands(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	id := rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	// A flush, in this process or another over the same vault, has claimed the
	// queue and is writing it.
	claimed, err := v.local.ClaimQueued("a flush under way", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("claim the queue: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("the flush claimed %d record(s), want 1", len(claimed))
	}

	if err := v.Forget(id); !errors.Is(err, local.ErrClaimed) {
		t.Fatalf("forgetting a memory a flush is writing returned %v, want ErrClaimed", err)
	}
	if _, err := v.local.GetBody(id); err != nil {
		t.Errorf("the refused forget removed the body: %v", err)
	}
	if _, err := v.local.RankingMetaFor(id); err != nil {
		t.Errorf("the refused forget removed the ranking metadata: %v", err)
	}
	if !v.index.Has(id) {
		t.Error("the refused forget removed the vector")
	}

	land(t, v, claimed)
	if err := v.Forget(id); err != nil {
		t.Fatalf("forget once the flush has landed: %v", err)
	}
	if _, err := v.manifest.Lookup(id); !errors.Is(err, manifest.ErrNotFound) {
		t.Errorf("the catalog still holds the forgotten memory: %v", err)
	}
	if _, err := v.local.GetBody(id); !errors.Is(err, local.ErrNotFound) {
		t.Errorf("the body is still on the device: %v", err)
	}
	if v.index.Has(id) {
		t.Error("the searchable index still holds the forgotten vector")
	}
}

// An id that is neither queued nor catalogued is refused, as it always was.
//
// Forget removes rows that sessions keep too, so accepting any id at all would
// let a session's id cost that session its vector and its ranking rows, and
// leave the conversation missing from search with its head still in place.
func TestForgetRefusesAnIDWithNoMemoryBehindIt(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	saved, err := v.SaveSession(context.Background(), SaveSessionRequest{
		Title:   "Tidepool gauge survey",
		Summary: "Went through which of the Tidepool gauges read in which unit.",
		Messages: []record.Message{{
			ID:      "survey-1",
			Role:    record.RoleUser,
			Created: record.Now(),
			Parts:   []record.Part{{Type: record.PartText, Text: "Which unit does each gauge read in?"}},
		}},
	})
	if err != nil {
		t.Fatalf("save session: %v", err)
	}

	if err := v.Forget(saved.ID); !errors.Is(err, manifest.ErrNotFound) {
		t.Fatalf("forgetting a session's id as a memory returned %v, want ErrNotFound", err)
	}
	if !v.index.Has(saved.ID) {
		t.Error("the refused forget removed the session's vector")
	}
	if _, err := v.local.RankingMetaFor(saved.ID); err != nil {
		t.Errorf("the refused forget removed the session's ranking metadata: %v", err)
	}
}
