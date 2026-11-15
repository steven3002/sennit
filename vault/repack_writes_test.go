package vault

import (
	"context"
	"fmt"
	"testing"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/internal/siatest"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
	"github.com/steven3002/sennit/store"
)

// entriesOn builds n catalogued records of size bytes each, spread evenly
// across the named slabs.
func entriesOn(n, size int, slabs ...string) []manifest.Entry {
	out := make([]manifest.Entry, n)
	for i := range out {
		out[i] = manifest.Entry{
			ID:        record.ID{byte(i + 1), byte((i + 1) >> 8)},
			ObjectRef: fmt.Sprintf("%064x", i+1),
			SlabID:    slabs[i%len(slabs)],
			Bytes:     size,
		}
	}
	return out
}

// A repack is counted by what it writes. Sixty small records on four slabs go
// back into one, and one is what it needs room for.
func TestARepackIsCountedByWhatItWrites(t *testing.T) {
	const slab = store.DefaultSlabPayloadSize

	if got := repackWrites(nil, slab); got != 0 {
		t.Errorf("an empty catalog writes nothing, got %d slab(s)", got)
	}
	if got := repackWrites(entriesOn(60, 900, "a", "b", "c", "d"), slab); got != 1 {
		t.Errorf("60 records of 900 B on four slabs fit in one, got %d", got)
	}
	if got := repackWrites(entriesOn(1000, 900, "a", "b", "c", "d"), slab); got != 1 {
		t.Errorf("1,000 records of 900 B fit in one slab, got %d", got)
	}
}

// A record that hydrate or recover catalogued carries the size of its sealed
// body, and a repack uploads it with its frame. Two such records that fit in a
// slab by their bodies and not with their frames are counted as two slabs, so
// the check never passes a repack that then runs out of room.
func TestARecordIsCountedWithItsFrame(t *testing.T) {
	const slab = store.DefaultSlabPayloadSize

	body := int(slab/2) - 10
	twoBodies := entriesOn(2, body, "a")
	if framed := 2 * (body + seal.FrameOverhead(body)); int64(framed) <= slab {
		t.Fatalf("the case is wrong: %d framed bytes fit in a slab of %d", framed, slab)
	}
	if got := repackWrites(twoBodies, slab); got != 2 {
		t.Errorf("two records that need %d B with their frames fill two slabs of %d B, got %d",
			2*(body+seal.FrameOverhead(body)), slab, got)
	}
}

// A record whose size the catalog does not hold leaves the total unknown, and
// the answer falls back to the slabs the records are on now, which they cannot
// outgrow.
func TestAnUnsizedRecordFallsBackToTheSlabsInUse(t *testing.T) {
	const slab = store.DefaultSlabPayloadSize

	unsized := entriesOn(60, 900, "a", "b", "c", "d")
	unsized[7].Bytes = 0
	if got := repackWrites(unsized, slab); got != 4 {
		t.Errorf("with a size missing the answer is the 4 slabs the records are on, got %d", got)
	}
}

// The vault answers from its own catalog. A connected vault whose ledger holds
// four slabs, with sixty small records spread across them, says a repack needs
// one slab free, not four.
func TestTheWatermarkAsksForTheSlabsARepackWrites(t *testing.T) {
	indexer, key := siatest.IndexerWithoutHosts(t)
	v, err := Open(context.Background(), Options{
		Home:     t.TempDir(),
		Phrase:   ledgerPhrase,
		Embedder: embedtest.NewStub(),
		Indexer:  indexer,
		AppKey:   key,
	})
	if err != nil {
		t.Fatalf("open a vault over the test indexer: %v", err)
	}
	t.Cleanup(func() { v.Close() })

	entries := entriesOn(60, 900, "a", "b", "c", "d")
	for _, slabID := range []string{"a", "b", "c", "d"} {
		if err := v.local.TrackSlab(slabID, 15, 15*900); err != nil {
			t.Fatalf("track slab %s: %v", slabID, err)
		}
	}
	for _, entry := range entries {
		entry.Kind, entry.Type, entry.WrittenAt = record.KindMemory, record.TypeFact, record.Now()
		if err := v.manifest.Append(entry); err != nil {
			t.Fatalf("catalogue a record: %v", err)
		}
	}

	mark, err := v.Watermark(context.Background())
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if want := uint64(store.DefaultSlabPayloadSize); mark.Headroom != want {
		t.Errorf("60 small records on four slabs need one slab free, %d B; the vault asked for %d B",
			want, mark.Headroom)
	}
	if !mark.Affordable {
		t.Errorf("the test account has room for one slab, so the repack is affordable")
	}
}
