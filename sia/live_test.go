package sia_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/sia"
)

// LiveEnv opts a run in to talking to a real indexer. It is off by default so
// the ordinary test run touches no network and spends no storage quota.
const LiveEnv = "SENNIT_LIVE"

func liveClient(t *testing.T) *sia.Client {
	t.Helper()
	if os.Getenv(LiveEnv) == "" {
		t.Skipf("set %s=1 to run against a real indexer", LiveEnv)
	}
	appKey, err := keys.AppKeyFromEnv()
	if err != nil {
		t.Fatalf("app key: %v", err)
	}
	client, err := sia.Connect(sia.Config{AppKey: appKey})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// pinBatch writes count random payloads as one packed, pinned batch and
// returns the batch alongside what each object should read back as.
func pinBatch(t *testing.T, client *sia.Client, count, size int) (*sia.Batch, [][]byte) {
	t.Helper()
	ctx := t.Context()

	if _, err := client.WaitReady(ctx, 90*time.Second); err != nil {
		t.Fatalf("account not ready: %v", err)
	}
	payloads := make([][]byte, count)
	for i := range payloads {
		payloads[i] = make([]byte, size)
		if _, err := rand.Read(payloads[i]); err != nil {
			t.Fatalf("payload %d: %v", i, err)
		}
	}
	batch, err := client.UploadPacked(ctx, payloads)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if err := client.PinSlabs(ctx, batch); err != nil {
		t.Fatalf("pin slabs: %v", err)
	}
	if err := client.PinObjects(ctx, batch); err != nil {
		t.Fatalf("pin objects: %v", err)
	}
	return batch, payloads
}

// release returns a test's storage in the order that keeps the account
// healthy: objects first, then the slab underneath them.
//
// The reverse order does not merely leak, it leaves objects that can never be
// opened again, and the account keeps returning them forever.
func release(t *testing.T, client *sia.Client, batch *sia.Batch) {
	t.Helper()
	ctx := context.Background()

	refs := make([]sia.ObjectRef, 0, batch.Len())
	for _, placement := range batch.Placements() {
		refs = append(refs, placement.Ref)
	}
	if err := client.DeleteObjects(ctx, refs); err != nil {
		t.Errorf("delete %d object(s): %v", len(refs), err)
	}
	for _, slabID := range batch.Slabs() {
		if err := client.UnpinSlab(ctx, slabID); err != nil {
			t.Errorf("release slab %s: %v", slabID, err)
		}
	}
}

// TestLiveDeletingEveryObjectImplicitlyUnpinsItsSlab measures whether the
// indexer releases a slab on its own once nothing references it.
//
// The SDK's maintainers say it does: an enhancement request to expose
// UnpinSlab was declined on 2026-08-04 with "the indexer was recently updated
// to automatically unpin slabs when deleting all objects that reference them
// implicitly". If that holds on the indexer we actually talk to, deleting
// objects is the whole of reclamation and the explicit unpin is belt and
// braces.
//
// It is measured rather than believed because this project has twice predicted
// indexer behaviour from sources that turned out not to describe the deployed
// service, and because the consequence of being wrong is a leak of forty MiB
// per flush that reports success.
func TestLiveDeletingEveryObjectImplicitlyUnpinsItsSlab(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	before, err := client.Account(ctx)
	if err != nil {
		t.Fatalf("account: %v", err)
	}

	batch, _ := pinBatch(t, client, 8, 256)
	slabsUsed := batch.Slabs()
	if len(slabsUsed) != 1 {
		t.Fatalf("the batch occupied %d slabs, want 1", len(slabsUsed))
	}
	slabID := slabsUsed[0]
	// Whatever the indexer does or does not do on its own, this test does not
	// leave a slab behind.
	t.Cleanup(func() {
		if err := client.UnpinSlab(context.Background(), slabID); err != nil {
			t.Logf("explicit unpin at cleanup: %v", err)
		}
	})

	written, err := client.Account(ctx)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	billed := written.PinnedData - before.PinnedData
	t.Logf("one flush of 8 records billed %d bytes", billed)

	refs := make([]sia.ObjectRef, 0, batch.Len())
	for _, placement := range batch.Placements() {
		refs = append(refs, placement.Ref)
	}
	if err := client.DeleteObjects(ctx, refs); err != nil {
		t.Fatalf("delete every object over the slab: %v", err)
	}
	t.Logf("deleted all %d objects over slab %s; not unpinning it", len(refs), slabID)

	// Long enough for a maintenance loop to run several times: the indexer's
	// own contract pruning runs about every two minutes.
	const patience = 6 * time.Minute
	deadline := time.Now().Add(patience)
	for {
		account, err := client.Account(ctx)
		if err != nil {
			t.Fatalf("account: %v", err)
		}
		pinned, err := client.PinnedSlabs(ctx)
		if err != nil {
			t.Fatalf("list pinned slabs: %v", err)
		}
		var stillListed bool
		for _, id := range pinned {
			if id == slabID {
				stillListed = true
				break
			}
		}
		elapsed := patience - time.Until(deadline)

		if !stillListed && account.PinnedData <= before.PinnedData {
			t.Logf("after %v the indexer released the slab on its own: quota back to %d bytes",
				elapsed.Truncate(time.Second), account.PinnedData)
			t.Log("deleting objects is sufficient; the explicit unpin is belt and braces")
			return
		}
		if time.Now().After(deadline) {
			t.Logf("after %v the slab is still %s and quota is %d bytes, %d above where it started",
				patience, listedOrNot(stillListed), account.PinnedData, account.PinnedData-before.PinnedData)
			t.Log("deleting every object did NOT release the slab on this indexer: " +
				"the explicit unpin is load-bearing, not belt and braces")
			return
		}
		time.Sleep(30 * time.Second)
	}
}

func listedOrNot(listed bool) string {
	if listed {
		return "listed as pinned"
	}
	return "absent from the slab listing"
}

// TestLiveEnumerateObjects establishes what a walk from the zero cursor
// actually returns.
//
// The indexer's listing is documented as a change feed, objects updated after
// a cursor, not as a listing of everything. Recovery without a catalog stands
// or falls on whether a zero cursor reaches every object, so this measures it
// rather than assuming it: write a batch, enumerate from zero, and require
// every one of the new objects to appear.
func TestLiveEnumerateObjects(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	const count = 12
	batch, payloads := pinBatch(t, client, count, 256)
	t.Cleanup(func() { release(t, client, batch) })

	want := make(map[string][]byte, count)
	for i, placement := range batch.Placements() {
		want[placement.Ref.String()] = payloads[i]
	}

	start := time.Now()
	found := make(map[string][]byte, count)
	var total int
	stats, err := client.WalkObjectsStats(ctx, func(object sia.StoredObject) error {
		total++
		if _, ours := want[object.Ref.String()]; !ours {
			return nil
		}
		body, err := client.ReadObject(object)
		if err != nil {
			return fmt.Errorf("read %s: %w", object.Ref, err)
		}
		found[object.Ref.String()] = body
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	elapsed := time.Since(start)

	t.Logf("walk: %d page(s), %d event(s), %d deletion(s), %d superseded, %d unreadable, %d live object(s) in %v",
		stats.Pages, stats.Events, stats.Deleted, stats.Superseded, len(stats.Unreadable), stats.Live, elapsed)
	t.Logf("page size %d; %d of the account's %d live objects are this test's",
		sia.EnumeratePageSize, len(found), total)

	if len(found) != count {
		t.Fatalf("a walk from the zero cursor reached %d of %d objects just written: "+
			"the feed does not enumerate an account in full, so recovery cannot be built on it",
			len(found), count)
	}
	for ref, body := range found {
		if !bytes.Equal(body, want[ref]) {
			t.Fatalf("object %s came back with %d bytes, want %d", ref, len(body), len(want[ref]))
		}
	}

	// A second walk must reach the same set. If the feed only ever returned
	// what changed since a client last looked, a repeat walk would come back
	// empty and recovery would work exactly once.
	var again int
	if _, err := client.WalkObjectsStats(ctx, func(object sia.StoredObject) error {
		if _, ours := want[object.Ref.String()]; ours {
			again++
		}
		return nil
	}); err != nil {
		t.Fatalf("second walk: %v", err)
	}
	if again != count {
		t.Fatalf("a repeated walk from the zero cursor reached %d of %d objects, want all of them", again, count)
	}
	t.Logf("a repeated walk from the zero cursor reached the same %d objects", again)
}

// TestLiveEnumerateSeesDeletions checks that an object deleted from the
// indexer stops appearing in a walk, so recovery rebuilds what the account
// holds rather than everything it ever held.
func TestLiveEnumerateSeesDeletions(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	batch, _ := pinBatch(t, client, 2, 128)
	written := batch.Placements()
	t.Cleanup(func() { release(t, client, batch) })

	if err := client.DeleteObject(ctx, written[0].Ref); err != nil {
		t.Fatalf("delete object: %v", err)
	}

	var sawDeleted, sawKept bool
	if _, err := client.WalkObjectsStats(ctx, func(object sia.StoredObject) error {
		switch object.Ref.String() {
		case written[0].Ref.String():
			sawDeleted = true
		case written[1].Ref.String():
			sawKept = true
		}
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if sawDeleted {
		t.Error("a deleted object still appears in the walk")
	}
	if !sawKept {
		t.Error("the surviving object is missing from the walk")
	}
	t.Log("a deleted object is absent from the walk and its slab-mate is still present")
}

// TestLiveUnpinBeforeDeleteStrandsObjects records why reclamation releases
// objects before slabs.
//
// An object's identity is derived from the sectors of the slab it points into,
// and its signature is over that identity. Releasing the slab first therefore
// does not merely orphan the object: it changes what the object claims to be,
// and the account is left holding an entry that can never be opened again. It
// still bills, it still comes back from every listing, and no key recovers it.
//
// The blast radius is the reason this is a test rather than a comment. The
// SDK's own listing helper opens every object in a page and fails the whole
// call if one of them will not open, so a single stranded object is enough to
// stop an account enumerating itself at all, which is the one thing recovery
// without a catalog depends on.
func TestLiveUnpinBeforeDeleteStrandsObjects(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	before, err := client.WalkObjectsStats(ctx, func(sia.StoredObject) error { return nil })
	if err != nil {
		t.Fatalf("walk before: %v", err)
	}

	batch, _ := pinBatch(t, client, 2, 128)
	// Whatever state this leaves, these objects are not coming back, so they
	// must not be left behind for the next run to trip over.
	t.Cleanup(func() {
		refs := make([]sia.ObjectRef, 0, batch.Len())
		for _, placement := range batch.Placements() {
			refs = append(refs, placement.Ref)
		}
		if err := client.DeleteObjects(context.Background(), refs); err != nil {
			t.Errorf("delete %d stranded object(s): %v", len(refs), err)
		}
	})
	for _, slabID := range batch.Slabs() {
		if err := client.UnpinSlab(ctx, slabID); err != nil {
			t.Fatalf("release slab %s: %v", slabID, err)
		}
	}

	// The account must still be able to list itself, whatever the objects were
	// left in. That is the property the walk exists to preserve, and it is why
	// it opens objects one at a time rather than a page at a time.
	after, err := client.WalkObjectsStats(ctx, func(sia.StoredObject) error { return nil })
	if err != nil {
		t.Fatalf("a stranded object stopped the account enumerating itself: %v", err)
	}
	t.Logf("unreadable objects: %d before, %d after releasing a slab under two live objects",
		len(before.Unreadable), len(after.Unreadable))
	t.Logf("live objects: %d before, %d after", before.Live, after.Live)

	if stranded := len(after.Unreadable) - len(before.Unreadable); stranded > 0 {
		t.Logf("releasing the slab first stranded %d object(s): they still bill, still list, "+
			"and no key opens them", stranded)
	} else {
		t.Log("no object was stranded on this run; the damage is not always immediate, " +
			"which is why the ordering is enforced rather than tested for")
	}
}
