package index_test

import (
	"sync"
	"testing"

	"github.com/steven3002/sennit/index"
)

// A compaction that reads the store's own files folds exactly what they hold,
// once the deltas are due or whenever it is asked to, and leaves the store
// hydrating to the same vectors as before.
func TestAStoreFoldsItsOwnFilesWhenDueOrAsked(t *testing.T) {
	store := openStore(t, t.TempDir())
	var want []index.Entry
	for i := range 60 {
		want = append(want, entry(t, byte(i+1), 384))
	}
	if err := store.Append(want[:10]...); err != nil {
		t.Fatalf("append: %v", err)
	}
	if folded, err := store.CompactIfDue(); err != nil || folded {
		t.Fatalf("a delta below the floor was folded: %v, %v", folded, err)
	}
	if err := store.Append(want[10:]...); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Remove(want[0].ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !store.DueForCompaction() {
		t.Fatal("the deltas never came due, so this checks nothing about a fold")
	}
	if folded, err := store.CompactIfDue(); err != nil || !folded {
		t.Fatalf("deltas that were due were not folded: %v, %v", folded, err)
	}
	if stats := store.Stats(); stats.DeltaBytes != 0 || stats.Compactions != 1 {
		t.Errorf("after a fold the store holds %d delta byte(s) over %d compaction(s), want none over 1",
			stats.DeltaBytes, stats.Compactions)
	}

	late := entry(t, 200, 384)
	if err := store.Append(late); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Fold(); err != nil {
		t.Fatalf("fold on request: %v", err)
	}
	if stats := store.Stats(); stats.DeltaBytes != 0 || stats.Compactions != 2 {
		t.Errorf("after a fold on request the store holds %d delta byte(s) over %d compaction(s), want none over 2",
			stats.DeltaBytes, stats.Compactions)
	}
	hydrated, err := store.Hydrate()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	got := byID(hydrated)
	if _, ok := got[want[0].ID]; ok {
		t.Error("the fold brought back a removed vector")
	}
	for _, kept := range append(want[1:], late) {
		if _, ok := got[kept.ID]; !ok {
			t.Errorf("the fold lost vector %s", kept.ID)
		}
	}
	if len(got) != len(want) {
		t.Errorf("the store hydrates %d vector(s) after folding, want %d", len(got), len(want))
	}
}

// Writes that wait on a fold land after it and are kept: a removal stays a
// removal and an addition stays added.
func TestWritesMadeWhileAStoreFoldsAreKept(t *testing.T) {
	store := openStore(t, t.TempDir())
	var removed, added []index.Entry
	for i := range 40 {
		removed = append(removed, entry(t, byte(i+1), 384))
	}
	if err := store.Append(removed...); err != nil {
		t.Fatalf("append: %v", err)
	}

	stop := make(chan struct{})
	var folding sync.WaitGroup
	folding.Add(1)
	go func() {
		defer folding.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := store.Fold(); err != nil {
				t.Errorf("fold: %v", err)
				return
			}
		}
	}()
	for i, gone := range removed {
		if err := store.Remove(gone.ID); err != nil {
			t.Fatalf("remove: %v", err)
		}
		fresh := entry(t, byte(i+1), 384)
		fresh.ID[0] = 1
		if err := store.Append(fresh); err != nil {
			t.Fatalf("append: %v", err)
		}
		added = append(added, fresh)
	}
	close(stop)
	folding.Wait()

	hydrated, err := store.Hydrate()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	got := byID(hydrated)
	for _, gone := range removed {
		if _, ok := got[gone.ID]; ok {
			t.Errorf("removed vector %s is back", gone.ID)
		}
	}
	for _, kept := range added {
		if _, ok := got[kept.ID]; !ok {
			t.Errorf("added vector %s is gone", kept.ID)
		}
	}
	if len(got) != len(added) {
		t.Errorf("the store hydrates %d vector(s), want the %d added", len(got), len(added))
	}
}
