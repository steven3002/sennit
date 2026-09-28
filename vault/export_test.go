package vault

import (
	"context"
	"testing"
	"time"

	"github.com/steven3002/sennit/seal"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store/packer"
)

// The network, stood in for by the test, for the package's external tests.
//
// A device that has hydrated a vault is reachable only through hydrate's network
// walk, and outside this package nothing can stand in for the network. So the
// two halves are exposed here instead: the flush that puts a writer's records on
// the network, and the per-frame restore a hydrate runs on what its walk read
// back. Neither adds a seam to the vault itself.

// A StoredObject is one object the test's network holds: where it is, and the
// bytes a walk reads back from it.
type StoredObject struct {
	Object  sia.StoredObject
	Payload []byte
}

// OpenWithLedgerForTest opens an offline vault at home with a slab ledger
// attached, which a flush and a hydrate both write to, and closes it when the
// test ends.
func OpenWithLedgerForTest(t *testing.T, home string) *Vault {
	t.Helper()
	v := ledgerVault(t, home)
	t.Cleanup(func() { v.Close() })
	return v
}

// FlushToNetworkForTest writes everything the writer has queued, with the test
// standing in for the network, and returns what the network then holds.
//
// It is flushQueue with the payloads kept. Each object is placed where the
// writer's own catalog says it landed, so a device restored from these objects
// catalogues them exactly where the writer does.
func FlushToNetworkForTest(t *testing.T, writer *Vault) []StoredObject {
	t.Helper()
	claimed, err := writer.local.ClaimQueued("test flush", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("claim the queue: %v", err)
	}
	land(t, writer, claimed)

	objects := make([]StoredObject, 0, len(claimed))
	for _, blob := range claimed {
		entry, err := writer.manifest.Lookup(blob.ID)
		if err != nil {
			t.Fatalf("the flush did not catalogue %s: %v", blob.ID, err)
		}
		ref, err := sia.ParseObjectRef(entry.ObjectRef)
		if err != nil {
			t.Fatalf("catalogued object ref for %s: %v", blob.ID, err)
		}
		objects = append(objects, StoredObject{
			Object: sia.StoredObject{
				Ref:       ref,
				Slab:      sia.SlabID(entry.SlabID),
				Bytes:     uint64(len(blob.Payload)),
				UpdatedAt: time.Now(),
			},
			Payload: blob.Payload,
		})
	}
	return objects
}

// RestoreForTest puts the network's objects back on a device to a depth, the
// way Hydrate does once its walk has read them: every frame through
// hydrateFrame, and then, from HydrateMetadata on, the session heads rebuilt
// from the chunks.
func RestoreForTest(t *testing.T, v *Vault, objects []StoredObject, depth HydrateDepth) {
	t.Helper()
	ctx := context.Background()
	req := HydrateRequest{Depth: depth}
	var report HydrateReport
	for _, stored := range objects {
		frames, err := seal.Frames(stored.Payload)
		if err != nil {
			t.Fatalf("read the frames of object %s: %v", stored.Object.Ref, err)
		}
		for _, frame := range frames {
			if err := v.hydrateFrame(ctx, frame, stored.Object, req, &report); err != nil {
				t.Fatalf("restore %s to depth %s: %v", frame.ID, depth, err)
			}
		}
	}
	if !depth.includes(HydrateMetadata) {
		return
	}
	if _, err := v.RebuildSessions(ctx, RebuildRequest{Embed: depth.includes(HydrateIndex)}); err != nil {
		t.Fatalf("rebuild the session heads: %v", err)
	}
}
