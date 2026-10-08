package vault

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
	"github.com/steven3002/sennit/store/reclaim"
)

// cutTheCatalogLog takes the last n bytes off the catalog's log under home,
// which is what a crash leaves when it stops an append n bytes short of the end
// of its line.
func cutTheCatalogLog(t *testing.T, home string, n int) {
	t.Helper()
	path := filepath.Join(home, manifest.LogName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the catalog's log: %v", err)
	}
	if len(raw) < n {
		t.Fatalf("the catalog's log holds %d bytes, fewer than the %d to cut", len(raw), n)
	}
	if err := os.WriteFile(path, raw[:len(raw)-n], 0o600); err != nil {
		t.Fatalf("cut the catalog's log: %v", err)
	}
}

// openAfterTheCrash opens the vault under home as the next process to use it
// does, with a ledger attached as ledgerVault attaches one.
func openAfterTheCrash(t *testing.T, home string) *Vault {
	t.Helper()
	v, err := Open(context.Background(), Options{
		Home:     home,
		Phrase:   ledgerPhrase,
		Embedder: embedtest.NewStub(),
		Offline:  true,
	})
	if err != nil {
		t.Fatalf("a vault whose catalog's last line was cut off would not open: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	v.reclaimer = reclaim.New(nil, v.local)
	return v
}

// catalogued is the ids the catalog names, in the order it first named them.
func catalogued(v *Vault) []record.ID {
	var ids []record.ID
	for _, entry := range v.Entries() {
		ids = append(ids, entry.ID)
	}
	return ids
}

// recoverFrom runs what Recover runs on each object its walk of the network
// reads, over the objects the test's network holds.
func recoverFrom(t *testing.T, v *Vault, objects []StoredObject) {
	t.Helper()
	for _, stored := range objects {
		frames, err := seal.Frames(stored.Payload)
		if err != nil {
			t.Fatalf("read the frames of object %s: %v", stored.Object.Ref, err)
		}
		for _, frame := range frames {
			if _, err := v.recoverFrame(context.Background(), frame, stored.Object, RecoveryRequest{Embed: true}); err != nil {
				t.Fatalf("recover %s: %v", frame.ID, err)
			}
		}
	}
}

// A crash part way through an append leaves the catalog's last line cut short.
// The vault opens over it, with the change that line held dropped and counted,
// where it used to refuse to open at all: for the command line, for the server,
// and for `sennit recover`, which opens the vault like every other command and
// is what puts a lost change back.
func TestAVaultWhoseCatalogEndsInACutOffLineOpensWithoutIt(t *testing.T) {
	home := t.TempDir()
	writer := ledgerVault(t, home)
	ids := []record.ID{
		rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour"),
		rememberFact(t, writer, "The river gauge at Tidepool reads in millimetres.", "river"),
		rememberFact(t, writer, "The tide tables at Tidepool are printed every spring.", "tides"),
	}
	network := FlushToNetworkForTest(t, writer)
	if got := catalogued(writer); !slices.Equal(got, ids) {
		t.Fatalf("the flush catalogued %v, want %v", got, ids)
	}
	writer.Close()

	cutTheCatalogLog(t, home, 40)

	reopened := openAfterTheCrash(t, home)
	if got := reopened.DroppedCatalogChanges(); got != 1 {
		t.Fatalf("the vault reports %d dropped catalog change(s), want 1", got)
	}
	if got := catalogued(reopened); !slices.Equal(got, ids[:2]) {
		t.Fatalf("the catalog names %v after the drop, want the two whose lines were whole, %v", got, ids[:2])
	}

	// What recover reads back from the network catalogues the record again.
	recoverFrom(t, reopened, network)
	if got := catalogued(reopened); !slices.Equal(got, ids) {
		t.Fatalf("the catalog names %v after a recover, want all three, %v", got, ids)
	}
	reopened.Close()

	again := openAfterTheCrash(t, home)
	if got := again.DroppedCatalogChanges(); got != 0 {
		t.Fatalf("a vault whose catalog was repaired reports %d dropped change(s) the next time it opens", got)
	}
	if got := catalogued(again); !slices.Equal(got, ids) {
		t.Fatalf("the catalog names %v once reopened, want %v", got, ids)
	}
}

// landWithoutFinishing does what a flush does once the network has written a
// batch, up to and including cataloguing it, and stops before the batch leaves
// the queue, as a process that dies in the middle of its catalogue does.
func landWithoutFinishing(t *testing.T, v *Vault, claimed []local.QueuedBlob) {
	t.Helper()
	var result packer.Result
	var bytes int64
	for _, blob := range claimed {
		var ref sia.ObjectRef
		copy(ref.ID[:], blob.ID[:])
		result.Records = append(result.Records, packer.Queued{
			ID: blob.ID, Kind: blob.Kind, Blob: store.Blob{CID: blob.CID, Payload: blob.Payload}, Since: blob.QueuedAt,
		})
		result.Flush.Written = append(result.Flush.Written, store.Written{
			CID: blob.CID, ObjectRef: ref, SlabID: probeSlab, Bytes: len(blob.Payload),
		})
		bytes += int64(len(blob.Payload))
	}
	result.Flush.Slabs = []sia.SlabID{probeSlab}
	if err := v.recordSlabs(store.SlabsWritten{Slabs: result.Flush.Slabs, Records: len(claimed), Bytes: bytes}); err != nil {
		t.Fatalf("record the slab: %v", err)
	}
	if err := v.recordFlush(result); err != nil {
		t.Fatalf("catalog the flush: %v", err)
	}
}

// The warning about a dropped change promises that a record whose flush was
// being catalogued is still queued and that a later flush writes it again. A
// flush takes its batch off the queue only once the whole batch is catalogued,
// so a crash in the middle of cataloguing leaves the batch queued under the
// dead process's claim, and the next flush to find that claim stale takes it
// over.
func TestARecordWhoseCatalogLineWasCutOffIsCataloguedByALaterFlush(t *testing.T) {
	home := t.TempDir()
	v := ledgerVault(t, home)
	ids := []record.ID{
		rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour"),
		rememberFact(t, v, "The river gauge at Tidepool reads in millimetres.", "river"),
		rememberFact(t, v, "The tide tables at Tidepool are printed every spring.", "tides"),
	}
	claimed, err := v.local.ClaimQueued("a flush that crashed", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("claim the queue: %v", err)
	}
	landWithoutFinishing(t, v, claimed)
	v.Close()

	cutTheCatalogLog(t, home, 40)

	reopened := openAfterTheCrash(t, home)
	if got := reopened.DroppedCatalogChanges(); got != 1 {
		t.Fatalf("the vault reports %d dropped catalog change(s), want 1", got)
	}
	if got := catalogued(reopened); !slices.Equal(got, ids[:2]) {
		t.Fatalf("the catalog names %v after the drop, want %v", got, ids[:2])
	}
	if got := reopened.Pending(); got != len(ids) {
		t.Fatalf("%d record(s) are queued after the crash, want the whole batch of %d", got, len(ids))
	}

	// A staleness of zero takes the claim over now, which is what a flush does
	// once the dead process's claim has aged past its timeout.
	again, err := reopened.local.ClaimQueued("a later flush", 0, 0)
	if err != nil {
		t.Fatalf("claim the queue again: %v", err)
	}
	land(t, reopened, again)
	if got := catalogued(reopened); !slices.Equal(got, ids) {
		t.Fatalf("the catalog names %v after the later flush, want all three, %v", got, ids)
	}
}
