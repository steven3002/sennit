package vault

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
)

// refIDs lists the record ids of a run of chunks, in order.
func refIDs(chunks []record.ChunkRef) []record.ID {
	ids := make([]record.ID, len(chunks))
	for i, chunk := range chunks {
		ids[i] = chunk.ID
	}
	return ids
}

// queuedChunksOf lists the chunks a conversation's head names that are still
// in this device's queue, asked of the queue itself.
func queuedChunksOf(t *testing.T, v *Vault, id record.ID) []record.ID {
	t.Helper()
	head, err := v.Session(id)
	if err != nil {
		t.Fatalf("read the conversation: %v", err)
	}
	var queued []record.ID
	for _, chunk := range head.Chunks {
		held, err := v.local.Queued(chunk.ID)
		if err != nil {
			t.Fatalf("look for chunk %s in the queue: %v", chunk.ID, err)
		}
		if held {
			queued = append(queued, chunk.ID)
		}
	}
	return queued
}

// savedSurvey stores a conversation of four turns on v, with no connection to
// the network, and returns what the save reported.
func savedSurvey(t *testing.T, v *Vault) SaveSessionResult {
	t.Helper()
	saved, err := v.SaveSession(context.Background(), SaveSessionRequest{
		Title:    "Tidepool gauge survey",
		Summary:  "Went through which unit each Tidepool gauge reads in.",
		Messages: exchange("survey", true),
	})
	if err != nil {
		t.Fatalf("save the conversation: %v", err)
	}
	if saved.OnNetwork {
		t.Fatal("a conversation saved with no connection said it was on the network")
	}
	return saved
}

// summarise saves a new summary for a conversation, and no turns.
func summarise(t *testing.T, v *Vault, id record.ID, summary string) SaveSessionResult {
	t.Helper()
	saved, err := v.SaveSession(context.Background(), SaveSessionRequest{ID: id, Summary: summary})
	if err != nil {
		t.Fatalf("save a new summary: %v", err)
	}
	if len(saved.Chunks) != 0 || saved.Flushed != nil {
		t.Fatalf("a save with no turns wrote %d chunk(s) and reported the flush %+v", len(saved.Chunks), saved.Flushed)
	}
	return saved
}

// A new summary and no turns does not put a conversation on the network while
// the turns an earlier save wrote are still waiting here for a flush.
//
// It was found in a host check. An agent saved a conversation while the vault
// could not reach the network, then saved it again with a new summary alone,
// and the answer said the conversation had been written to the network. The
// save asked only about the chunks it had written itself, and a save that
// writes none has none left to wait for. The first save's turns were still
// queued, and the agent told the user they were on the network.
func TestASummaryAloneDoesNotPutAQueuedConversationOnTheNetwork(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	t.Cleanup(func() { v.Close() })
	first := savedSurvey(t, v)

	again := summarise(t, v, first.ID, "Settled that every Tidepool gauge reads in centimetres.")
	if again.OnNetwork {
		t.Error("a new summary said the conversation is on the network, and its turns are still queued")
	}
	// What waits for the network is the first save's turns and nothing else.
	// The head the second save rewrote is not among it: a head never leaves
	// this device.
	if queued := queuedNow(t, v); !slices.Equal(queued, refIDs(first.Chunks)) {
		t.Errorf("the queue holds %v, want only the first save's chunks %v", queued, refIDs(first.Chunks))
	}
	if pending := v.Pending(); pending != len(first.Chunks) {
		t.Errorf("the vault counts %d record(s) owed to the network, want the first save's %d chunk(s)",
			pending, len(first.Chunks))
	}
}

// A conversation is on the network once a flush that wrote every chunk of it
// has finished: not while that flush is still under way, and not while one
// chunk of it is still queued, whichever save queued it.
func TestASummaryAlonePutsAConversationOnTheNetworkOnceEveryChunkIsWritten(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	t.Cleanup(func() { v.Close() })
	ctx := context.Background()
	first := savedSurvey(t, v)

	// A flush has the turns and has not finished with them.
	claimed, err := v.local.ClaimQueued("a flush under way", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("claim the queue: %v", err)
	}
	if saved := summarise(t, v, first.ID, "Started on the harbour gauges."); saved.OnNetwork {
		t.Error("a new summary said the conversation is on the network while the flush writing its turns was under way")
	}
	land(t, v, claimed)
	if saved := summarise(t, v, first.ID, "Finished the harbour gauges."); !saved.OnNetwork {
		t.Error("a new summary said the conversation is not on the network, once a flush had written every chunk of it")
	}

	// An append's turns are queued, and one chunk waiting is enough.
	appended, err := v.SaveSession(ctx, SaveSessionRequest{ID: first.ID, Messages: exchange("followup", true)})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if appended.OnNetwork {
		t.Error("an append queued on a vault with no connection said it was on the network")
	}
	if saved := summarise(t, v, first.ID, "Moved on to the river gauges."); saved.OnNetwork {
		t.Error("a new summary said the conversation is on the network, with one of its two chunks still queued")
	}
	if queued := queuedChunksOf(t, v, first.ID); !slices.Equal(queued, refIDs(appended.Chunks)) {
		t.Errorf("the queue holds %v of the conversation, want the append's chunks %v", queued, refIDs(appended.Chunks))
	}
	flushQueue(t, v)
	if saved := summarise(t, v, first.ID, "Finished the river gauges."); !saved.OnNetwork {
		t.Error("a new summary said the conversation is not on the network, once a flush had written every chunk of it")
	}
}

// A durable save of a new summary writes the turns earlier saves left queued,
// and says the conversation is on the network once that flush has written them.
//
// Durable asks for the conversation to be on the network before the call
// returns. A save that wrote no chunk took itself for one with nothing to
// flush, so it ran no flush and said the conversation was on the network.
func TestADurableSummaryAloneFlushesTheTurnsEarlierSavesLeftQueued(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	t.Cleanup(func() { v.Close() })
	ctx := context.Background()
	first := savedSurvey(t, v)

	// The flush the save runs, with the test standing in for the network. It
	// writes everything queued, as land does, and reports what it wrote the
	// way a flush does, by content address.
	var (
		flushes int
		wrote   []record.ID
	)
	flush := func(context.Context) (*store.Flush, error) {
		flushes++
		claimed, err := v.local.ClaimQueued("a durable save's flush", packer.DefaultClaimTimeout, 0)
		if err != nil || len(claimed) == 0 {
			return nil, err
		}
		land(t, v, claimed)
		report := &store.Flush{Slabs: []sia.SlabID{probeSlab}}
		for _, blob := range claimed {
			wrote = append(wrote, blob.ID)
			report.Written = append(report.Written, store.Written{CID: blob.CID, SlabID: probeSlab, Bytes: len(blob.Payload)})
		}
		return report, nil
	}

	saved, err := v.saveSessionOver(ctx, SaveSessionRequest{
		ID: first.ID, Summary: "Settled that every Tidepool gauge reads in centimetres.", Durable: true,
	}, flush)
	if err != nil {
		t.Fatalf("a durable save of a new summary: %v", err)
	}
	if flushes != 1 || !slices.Equal(wrote, refIDs(first.Chunks)) {
		t.Fatalf("the durable save ran %d flush(es) writing %v, want one writing the first save's chunks %v",
			flushes, wrote, refIDs(first.Chunks))
	}
	if !saved.OnNetwork || saved.Flushed == nil {
		t.Error("the durable save does not say the conversation is on the network, and its flush wrote every chunk of it")
	}

	// Nothing of it waits now, so a second durable save of a summary has
	// nothing to write and runs no flush, which would cost a whole slab.
	again, err := v.saveSessionOver(ctx, SaveSessionRequest{
		ID: first.ID, Summary: "Settled it again.", Durable: true,
	}, flush)
	if err != nil {
		t.Fatalf("a second durable save of a new summary: %v", err)
	}
	if flushes != 1 {
		t.Error("a durable save of a conversation already on the network ran a flush")
	}
	if !again.OnNetwork {
		t.Error("a durable save says the conversation is not on the network, and every chunk of it was written")
	}
}

// A durable save of a new summary that cannot reach the network says so and
// keeps the summary, as a durable save of new turns does.
//
// Durable asks for the conversation to be on the network before the call
// returns. The turns an earlier save left queued are part of it, so the save
// runs the flush for them, and a flush that fails is the call's error, beside a
// result that names the conversation it stored.
func TestADurableSummaryAloneThatCannotReachTheNetworkSaysSoAndKeepsTheSummary(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	t.Cleanup(func() { v.Close() })
	first := savedSurvey(t, v)

	const summary = "Settled that every Tidepool gauge reads in centimetres."
	saved, err := v.SaveSession(context.Background(), SaveSessionRequest{
		ID: first.ID, Summary: summary, Durable: true,
	})
	if !errors.Is(err, packer.ErrOffline) {
		t.Fatalf("a durable save with no connection, of a conversation whose turns are queued, returned %v, "+
			"want the flush's failure", err)
	}
	if saved.ID != first.ID || saved.Version != first.Version+1 {
		t.Errorf("the failed save names conversation %s at version %d, want %s at %d",
			saved.ID, saved.Version, first.ID, first.Version+1)
	}
	if saved.OnNetwork || saved.Flushed != nil {
		t.Error("the failed save says the conversation reached the network")
	}
	head, err := v.Session(first.ID)
	if err != nil {
		t.Fatalf("read the conversation: %v", err)
	}
	if head.Summary != summary {
		t.Errorf("the conversation's summary is %q, want the one the failed flush followed", head.Summary)
	}
	if queued := queuedChunksOf(t, v, first.ID); !slices.Equal(queued, refIDs(first.Chunks)) {
		t.Errorf("the queue holds %v of the conversation, want the first save's chunks %v", queued, refIDs(first.Chunks))
	}
}
