package vault_test

import (
	"context"
	"testing"
	"time"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/vault"
)

// TestLiveDoesDeletingObjectsReleaseTheSlab asks whether the indexer now gives
// quota back on its own.
//
// The project's account of reclamation says it does not: a slab is billed whole
// and comes back only when it is explicitly unpinned, which is why the ledger
// and the two-phase sweep exist at all. A repack measured on 2026-08-07 found
// its old slabs already gone by the time it tried to release them, which is the
// opposite. This settles which is true today, on one slab, without unpinning
// anything.
func TestLiveDoesDeletingObjectsReleaseTheSlab(t *testing.T) {
	requireLive(t)

	home := t.TempDir()
	v, err := openLiveVaultAt(t, home)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	ctx := context.Background()
	if _, err := v.WaitReady(ctx, 90*time.Second); err != nil {
		v.Close()
		t.Fatalf("account not ready: %v", err)
	}
	defer releaseByLedger(t, home)
	defer v.Close()

	baseline, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("account: %v", err)
	}

	for i := range 4 {
		if _, err := v.Remember(ctx, vault.RememberRequest{
			Statement: "Whether deleting every object over a slab returns its quota without an unpin.",
			Context:   "Written only to occupy one slab for the duration of this question.",
			Type:      record.TypeFact,
			Tags:      []string{"g3", "autorelease"},
		}); err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
	}
	if _, err := v.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	pinned, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("account after the flush: %v", err)
	}
	t.Logf("after one flush: %s pinned, up %s from %s",
		human(pinned.PinnedData), human(pinned.PinnedData-baseline.PinnedData), human(baseline.PinnedData))

	refs := make([]sia.ObjectRef, 0, 4)
	for _, entry := range v.Entries() {
		ref, err := sia.ParseObjectRef(entry.ObjectRef)
		if err != nil {
			t.Fatalf("parse ref for %s: %v", entry.ID, err)
		}
		refs = append(refs, ref)
	}

	// The objects go through a client of this test's own, so that nothing in
	// the vault's reclamation path runs and no unpin is issued.
	appKey, err := keys.AppKeyFromEnv()
	if err != nil {
		t.Fatalf("app key: %v", err)
	}
	client, err := sia.Connect(sia.Config{AppKey: appKey, Indexer: vault.DefaultIndexer()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	deletedAt := time.Now()
	if err := client.DeleteObjects(ctx, refs); err != nil {
		t.Fatalf("delete %d object(s): %v", len(refs), err)
	}
	t.Logf("deleted %d object(s) over one slab; no unpin was issued", len(refs))

	// Polled rather than read once: an immediate answer cannot tell an
	// unchanged figure from one that has not caught up yet.
	var released bool
	for range 12 {
		account, err := client.Account(ctx)
		if err != nil {
			t.Fatalf("account: %v", err)
		}
		elapsed := time.Since(deletedAt).Truncate(100 * time.Millisecond)
		t.Logf("  +%s  pinned %s", elapsed, human(account.PinnedData))
		if account.PinnedData <= baseline.PinnedData {
			t.Logf("RELEASED WITHOUT AN UNPIN after %s: quota is back to %s",
				elapsed, human(account.PinnedData))
			released = true
			break
		}
		time.Sleep(5 * time.Second)
	}

	slabs, err := client.PinnedSlabs(ctx)
	if err != nil {
		t.Fatalf("pinned slabs: %v", err)
	}
	t.Logf("the indexer lists %d pinned slab(s) after the deletions", len(slabs))
	if !released {
		t.Logf("STILL BILLED: deleting every object over a slab did NOT return its quota "+
			"within %s; the explicit unpin is still what frees storage", time.Since(deletedAt).Truncate(time.Second))
	}
}
