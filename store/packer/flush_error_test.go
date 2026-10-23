package packer_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/steven3002/sennit/internal/siatest"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
)

// hostlessStore writes through an indexer that refuses every upload, for want
// of hosts to take one. Its ledger records nothing, because no upload gets far
// enough to name a slab.
func hostlessStore(t *testing.T) *store.Store {
	t.Helper()
	indexer, key := siatest.IndexerWithoutHosts(t)
	client, err := sia.Connect(sia.Config{Indexer: indexer, AppKey: key})
	if err != nil {
		t.Fatalf("connect to the test indexer: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return store.New(client, func(store.SlabsWritten) error { return nil })
}

// A flush that a record's arrival sets off, and that fails, comes back as a
// FlushError, with the record still queued and free for the next flush to take.
//
// The record is queued before the flush runs, so the failure does not undo the
// write. Returned as the flush's bare error, it could not be told apart from a
// record the queue refused, and a caller that treated the two alike told its
// own caller that a stored record had never been stored.
func TestAFlushThatFailsAfterTheRecordIsQueuedIsAFlushError(t *testing.T) {
	device := deviceStore(t, t.TempDir())
	p, err := packer.New(hostlessStore(t), device, packer.Immediate(40<<20))
	if err != nil {
		t.Fatalf("new packer: %v", err)
	}
	item := queued(t, 256)

	result, err := p.Add(t.Context(), item)
	var unflushed *packer.FlushError
	if !errors.As(err, &unflushed) {
		t.Fatalf("the failed flush came back as %v, want a FlushError", err)
	}
	if !result.Empty() {
		t.Errorf("a flush whose upload failed reported writing %d record(s)", len(result.Records))
	}
	if p.Pending() != 1 {
		t.Errorf("%d record(s) pending after the failed flush, want the one queued", p.Pending())
	}
	claimed, err := device.ClaimQueued("next flush", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != item.ID {
		t.Errorf("the next flush would take %d record(s), want the one the failed flush handed back", len(claimed))
	}
}

// A record the queue refuses is not a FlushError, because it is stored nowhere
// and no later flush will carry it.
func TestARecordTheQueueRefusedIsNotAFlushError(t *testing.T) {
	dir := t.TempDir()
	device := deviceStore(t, dir)
	p, err := packer.New(hostlessStore(t), device, packer.Immediate(40<<20))
	if err != nil {
		t.Fatalf("new packer: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatalf("open the device's database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER refuse_queue_insert BEFORE INSERT ON queue
		BEGIN SELECT RAISE(ABORT, 'the disk refused the write'); END`); err != nil {
		t.Fatalf("make the device refuse to queue: %v", err)
	}

	_, err = p.Add(t.Context(), queued(t, 256))
	if err == nil {
		t.Fatal("a record the queue refused was accepted")
	}
	var unflushed *packer.FlushError
	if errors.As(err, &unflushed) {
		t.Errorf("the queue's refusal came back as a failed flush: %v", err)
	}
	if p.Pending() != 0 {
		t.Errorf("%d record(s) pending, want none", p.Pending())
	}
}
