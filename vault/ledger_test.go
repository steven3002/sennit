package vault

import (
	"context"
	"fmt"
	"testing"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
	"github.com/steven3002/sennit/store/reclaim"
)

const ledgerPhrase = "abandon abandon abandon abandon abandon abandon " +
	"abandon abandon abandon abandon abandon about"

// probeSlab is a slab id of the right shape. Nothing here reaches an indexer,
// so it only has to be distinguishable.
const probeSlab sia.SlabID = "f2c9a0f2c6d3f1b0a9e8d7c6b5a493827160fedcba0987654321fedcba098765"

// ledgerVault opens an offline vault with a ledger attached.
//
// A vault opened offline has no client and therefore no reclaimer. The ledger
// is on the device and needs neither, so one is attached over the device's
// store alone: the subject here is which rows a write leaves behind, not what
// the indexer does with them.
func ledgerVault(t *testing.T, home string) *Vault {
	t.Helper()
	v, err := Open(context.Background(), Options{
		Home:     home,
		Phrase:   ledgerPhrase,
		Embedder: embedtest.NewStub(),
		Offline:  true,
	})
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	v.reclaimer = reclaim.New(nil, v.local)
	return v
}

// aFlushOf builds what a flush of n small records reports, and what the write
// would have told the ledger before it pinned any of it.
func aFlushOf(t *testing.T, v *Vault, n int) (store.SlabsWritten, packer.Result) {
	t.Helper()
	var (
		records []packer.Queued
		written []store.Written
		bytes   int64
	)
	for i := range n {
		id, err := record.NewID()
		if err != nil {
			t.Fatalf("record id: %v", err)
		}
		body := []byte(fmt.Sprintf("the sealed body of record %d", i+1))
		cid, err := v.sealer.CID(body)
		if err != nil {
			t.Fatalf("cid: %v", err)
		}
		var ref sia.ObjectRef
		ref.ID[0] = byte(i + 1)
		records = append(records, packer.Queued{
			ID:   id,
			Kind: record.KindMemory,
			Blob: store.Blob{CID: cid.String(), Payload: body},
		})
		written = append(written, store.Written{
			CID:       cid.String(),
			ObjectRef: ref,
			SlabID:    probeSlab,
			Bytes:     len(body),
		})
		bytes += int64(len(body))
	}
	return store.SlabsWritten{Slabs: []sia.SlabID{probeSlab}, Records: n, Bytes: bytes},
		packer.Result{Records: records, Flush: store.Flush{Written: written, Slabs: []sia.SlabID{probeSlab}}}
}

func onlyLedgerRow(t *testing.T, v *Vault, when string) reclaim.Slab {
	t.Helper()
	tracked, err := v.reclaimer.Tracked()
	if err != nil {
		t.Fatalf("read the ledger %s: %v", when, err)
	}
	if len(tracked) != 1 {
		t.Fatalf("the ledger holds %d slab(s) %s, want 1: %+v", len(tracked), when, tracked)
	}
	return tracked[0]
}

// The slab is recorded once for the whole write, by the step that runs before
// the pin, and the flush's own bookkeeping must not record it again.
//
// TrackSlab adds to whatever the row already holds, because a recovered vault
// builds one row an object at a time. So a second write for the same flush does
// not overwrite the first, it doubles the records and the bytes the ledger
// believes that slab holds.
func TestASlabIsRecordedOnceAcrossAFlush(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	slabs, result := aFlushOf(t, v, 3)
	if err := v.recordSlabs(slabs); err != nil {
		t.Fatalf("record the slabs before the pin: %v", err)
	}

	// Before anything was pinned, the ledger already names the slab. That is
	// the whole of the fix: an interrupt from here on leaves a slab this device
	// can find.
	row := onlyLedgerRow(t, v, "before the pin")
	if row.ID != probeSlab {
		t.Errorf("the ledger names %s, want %s", row.ID, probeSlab)
	}
	if row.Records != slabs.Records || row.Bytes != slabs.Bytes {
		t.Errorf("the ledger holds %d record(s) of %d byte(s) before the pin, want %d of %d",
			row.Records, row.Bytes, slabs.Records, slabs.Bytes)
	}

	if err := v.recordFlush(result); err != nil {
		t.Fatalf("record the flush: %v", err)
	}

	row = onlyLedgerRow(t, v, "after the flush")
	if row.Records != slabs.Records || row.Bytes != slabs.Bytes {
		t.Errorf("the ledger holds %d record(s) of %d byte(s) after the flush, want %d of %d: "+
			"the slab was recorded twice", row.Records, row.Bytes, slabs.Records, slabs.Bytes)
	}
	// The catalog is still written by the flush, and it is the half that says
	// what is readable.
	if entries := len(v.Entries()); entries != len(result.Records) {
		t.Errorf("the catalog holds %d entry(ies), want %d", entries, len(result.Records))
	}
}

// The ledger row has to survive the process that wrote it, or recording before
// the pin buys nothing: the interrupt this guards against is a killed process.
func TestARecordedSlabSurvivesTheProcess(t *testing.T) {
	home := t.TempDir()
	first := ledgerVault(t, home)
	slabs, _ := aFlushOf(t, first, 2)
	if err := first.recordSlabs(slabs); err != nil {
		t.Fatalf("record the slabs: %v", err)
	}
	// Closed without any flush ever completing, which is the state an interrupt
	// leaves: the records are queued and the slab is spoken for.
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := ledgerVault(t, home)
	defer second.Close()
	if row := onlyLedgerRow(t, second, "in a second process"); row.ID != probeSlab {
		t.Errorf("a reopened vault names %s, want %s", row.ID, probeSlab)
	}
}

// A slab this device may release is what the sweep needs. A row filed as
// hydrated would be left alone on the grounds that another installation pinned
// it, which for an interrupted write of our own is the wrong answer.
func TestARecordedSlabIsThisDevicesToRelease(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	slabs, _ := aFlushOf(t, v, 1)
	if err := v.recordSlabs(slabs); err != nil {
		t.Fatalf("record the slabs: %v", err)
	}
	if row := onlyLedgerRow(t, v, "before the pin"); !row.Releasable() {
		t.Errorf("slab %s is filed as %q, so a sweep would leave it alone", row.ID, row.Origin)
	}
}
