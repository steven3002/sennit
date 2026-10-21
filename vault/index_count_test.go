package vault

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/steven3002/sennit/embed"
	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/store/reclaim"
)

// A conversation is counted once among the records searchable by meaning,
// however often it is saved, and the count agrees with the one the next process
// makes when it opens the vault.
//
// Every save of a conversation embeds it again and puts the vector under the id
// it already has. The index replaced the old vector and the count went up
// anyway, so a conversation saved once and appended to three times was four
// searchable records in `sennit://vault` until the next open counted again.
func TestAConversationIsCountedOnceHoweverOftenItIsSaved(t *testing.T) {
	home := t.TempDir()
	v := ledgerVault(t, home)

	rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	saved := saveExchange(t, v, "Tidepool gauge survey", "first")
	counted := []int{v.IndexHealth().Indexed}
	for _, part := range []string{"second", "third", "fourth"} {
		appendExchange(t, v, saved.ID, part)
		counted = append(counted, v.IndexHealth().Indexed)
	}
	if want := []int{2, 2, 2, 2}; !slices.Equal(counted, want) {
		t.Errorf("after the save and each of three appends the index reported %v searchable record(s), want %v",
			counted, want)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened := ledgerVault(t, home)
	defer reopened.Close()
	if got := reopened.IndexHealth().Indexed; got != 2 {
		t.Errorf("the next process counts %d searchable record(s), want 2", got)
	}
}

// Restoring records this device has already given vectors counts each of them
// once: a hydrate at index depth run a second time, a recover that embeds what
// it restores, and a rebuild that embeds the heads it writes over.
//
// Each of them embeds every record it reaches and puts the vector under the id
// the record already has. The index replaced the vector, and the count added
// one for every record each time, so a vault that ran the same hydrate twice
// counted each memory twice until it was opened again.
func TestRestoringRecordsThatAlreadyHaveVectorsCountsEachOnce(t *testing.T) {
	writer := ledgerVault(t, t.TempDir())
	defer writer.Close()
	rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	rememberFact(t, writer, "The river gauge at Tidepool reads in millimetres.", "river")
	saveExchange(t, writer, "Tidepool gauge survey", "survey")
	network := FlushToNetworkForTest(t, writer)

	home := t.TempDir()
	device := ledgerVault(t, home)
	// Two memories and the conversation.
	const searchable = 3
	counted := func(after string) {
		t.Helper()
		if got := device.IndexHealth().Indexed; got != searchable {
			t.Errorf("after %s the index reports %d searchable record(s), want %d", after, got, searchable)
		}
	}

	RestoreForTest(t, device, network, HydrateIndex)
	counted("a hydrate at index depth")
	RestoreForTest(t, device, network, HydrateIndex)
	counted("the same hydrate run again")
	recoverFrom(t, device, network)
	counted("a recover that embeds what it restores")
	if _, err := device.RebuildSessions(context.Background(), RebuildRequest{Embed: true, Overwrite: true}); err != nil {
		t.Fatalf("rebuild the heads over the ones this device holds: %v", err)
	}
	counted("a rebuild that embeds the heads it writes over")
	if err := device.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened := ledgerVault(t, home)
	defer reopened.Close()
	if got := reopened.IndexHealth().Indexed; got != searchable {
		t.Errorf("the next process counts %d searchable record(s), want %d", got, searchable)
	}
}

// Records written at the same time are each counted, and reading the count
// while they are written is safe.
//
// The protocol server answers tool calls concurrently, so two remembers and a
// read of `sennit://vault` can run at once. The count was a plain integer that
// every write added to with nothing guarding it, which the race detector
// reports, and which can lose one write's addition to another's.
func TestRecordsWrittenAtOnceAreEachCounted(t *testing.T) {
	home := t.TempDir()
	v := ledgerVault(t, home)
	ctx := context.Background()

	const writers, each = 4, 3
	var writing sync.WaitGroup
	for writer := range writers {
		writing.Add(1)
		go func() {
			defer writing.Done()
			for i := range each {
				if _, err := v.Remember(ctx, RememberRequest{
					Statement: fmt.Sprintf("Gauge %d-%d at Tidepool reads in centimetres.", writer, i),
					Context:   "From the fictional Tidepool field guide, written by several writers at once.",
					Type:      record.TypeFact,
					Tags:      []string{"gauge"},
				}); err != nil {
					t.Errorf("remember from writer %d: %v", writer, err)
				}
				// What a read of the vault's health does in the middle of it.
				_ = v.IndexHealth()
			}
		}()
	}
	writing.Wait()

	if got := v.IndexHealth().Indexed; got != writers*each {
		t.Errorf("the index reports %d searchable record(s), want the %d written", got, writers*each)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened := ledgerVault(t, home)
	defer reopened.Close()
	if got := reopened.IndexHealth().Indexed; got != writers*each {
		t.Errorf("the next process counts %d searchable record(s), want %d", got, writers*each)
	}
}

// anotherModel is the stub embedder under another model's name, which is what
// a vault opened by a build with a different model meets: vectors of the same
// width that it must never compare with its own.
type anotherModel struct{ *embedtest.Stub }

func (anotherModel) Model() embed.Model {
	return embed.Model{Name: "another-model", Dim: embedtest.StubModel.Dim}
}

// A vector from another model leaves the count of vectors the index cannot
// search once its record is embedded again with this vault's model, or
// forgotten, as it does for the next process to open the vault.
//
// That count is made as the vault opens, and it is what `sennit://vault`
// reports as vectors from another model, which cannot be searched. Nothing
// changed it afterwards, so a conversation saved again with this vault's model,
// or a memory forgotten, went on being reported as unsearchable until the next
// open counted again.
func TestAVectorFromAnotherModelLeavesTheCountWhenItIsReplacedOrForgotten(t *testing.T) {
	home := t.TempDir()
	first := ledgerVault(t, home)
	memory := rememberFact(t, first, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	saved := saveExchange(t, first, "Tidepool gauge survey", "first")
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	open := func() *Vault {
		t.Helper()
		v, err := Open(context.Background(), Options{
			Home:     home,
			Phrase:   ledgerPhrase,
			Embedder: anotherModel{embedtest.NewStub()},
			Offline:  true,
		})
		if err != nil {
			t.Fatalf("open the vault with another model: %v", err)
		}
		v.reclaimer = reclaim.New(nil, v.local)
		return v
	}
	v := open()
	defer v.Close()
	stub := embedtest.StubModel.Name
	if health := v.IndexHealth(); health.Indexed != 0 || health.Foreign[stub] != 2 {
		t.Fatalf("a vault opened with another model reports %+v, want both vectors left out", health)
	}

	// What this process says against what the next process to open the vault
	// counts, which is the count the vectors on disk support.
	agrees := func(after string, foreign map[string]int) {
		t.Helper()
		next := open()
		defer next.Close()
		got, recounted := v.IndexHealth(), next.IndexHealth()
		if !maps.Equal(recounted.Foreign, foreign) {
			t.Fatalf("after %s the next process counts %v vector(s) from another model, want %v",
				after, recounted.Foreign, foreign)
		}
		if !maps.Equal(got.Foreign, recounted.Foreign) || got.Mixed() != recounted.Mixed() {
			t.Errorf("after %s this process reports %v vector(s) from another model, and the next process %v",
				after, got.Foreign, recounted.Foreign)
		}
		if got.Indexed != recounted.Indexed {
			t.Errorf("after %s this process reports %d searchable record(s), and the next process %d",
				after, got.Indexed, recounted.Indexed)
		}
	}

	appendExchange(t, v, saved.ID, "second")
	agrees("the conversation was saved again with this vault's model", map[string]int{stub: 1})
	if err := v.Forget(memory); err != nil {
		t.Fatalf("forget the memory: %v", err)
	}
	agrees("the memory was forgotten", map[string]int{})
}
