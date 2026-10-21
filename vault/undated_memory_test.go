package vault

import (
	"context"
	"testing"
	"time"

	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
	"github.com/steven3002/sennit/sia"
)

// A memory stored without a creation time is still restored, read and listed.
//
// Validation now refuses such a memory before it is written. Nothing that reads
// a stored record validates it, and this holds that in place: a record already
// on the network is immutable, so a device that refused to restore or read one
// would lose it. No write path in this build stores a memory without the time,
// so the memory here is sealed by hand, the way a writer that left it out would
// have stored it.
func TestAMemoryStoredWithoutACreationTimeIsStillRestoredAndRead(t *testing.T) {
	ctx := context.Background()
	writer := ledgerVault(t, t.TempDir())
	defer writer.Close()

	id, err := record.NewID()
	if err != nil {
		t.Fatalf("record id: %v", err)
	}
	memory := &record.Memory{
		ID: id, Kind: record.KindMemory, Type: record.TypeFact,
		SchemaVersion: record.SchemaVersion, Version: 1,
		Statement: "The harbour gauge at Tidepool reads in centimetres.",
		Context:   "Stored by a writer that did not stamp the time.",
	}
	body, err := record.Marshal(memory)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sealed, err := writer.sealer.Seal(body)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	payload, err := seal.FrameRecord(record.KindMemory, id, sealed)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	object := sia.StoredObject{Slab: probeSlab, UpdatedAt: time.Now()}
	copy(object.Ref.ID[:], id[:])
	network := []StoredObject{{Object: object, Payload: payload}}

	hydrated := ledgerVault(t, t.TempDir())
	defer hydrated.Close()
	RestoreForTest(t, hydrated, network, HydrateIndex)
	read, _, err := hydrated.FetchMemory(ctx, id)
	if err != nil {
		t.Fatalf("read the hydrated memory: %v", err)
	}
	if read.Statement != memory.Statement || !read.CreatedAt.IsZero() {
		t.Errorf("the hydrated memory reads back as %q created %s", read.Statement, read.CreatedAt)
	}
	listed, err := hydrated.Browse(BrowseRequest{})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(listed.Rows) != 1 || listed.Rows[0].ID != id {
		t.Errorf("the hydrated device lists %+v, want the one memory", listed.Rows)
	}
	if !hydrated.index.Has(id) {
		t.Error("the hydrated memory was not made searchable")
	}

	recovered := ledgerVault(t, t.TempDir())
	defer recovered.Close()
	recoverFrom(t, recovered, network)
	if _, _, err := recovered.FetchMemory(ctx, id); err != nil {
		t.Errorf("read the recovered memory: %v", err)
	}
}
