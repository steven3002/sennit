package manifest_test

import (
	"errors"
	"testing"

	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
)

// A removal names its record's object until the object is deleted from the
// network, and is still a removal after that. Both survive a compaction and a
// reopen, which is what lets a deletion owed by a device that forgot a record
// offline wait for a run that reaches the network.
func TestARemovalNamesItsObjectUntilTheObjectIsDeleted(t *testing.T) {
	dir := t.TempDir()
	catalog := openCatalog(t, dir)
	kept, forgotten := entry(t, "kept-object"), entry(t, "forgotten-object")
	for _, e := range []manifest.Entry{kept, forgotten} {
		if err := catalog.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	unknown, err := record.NewID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}
	for _, id := range []record.ID{kept.ID, forgotten.ID, unknown} {
		if _, removed := catalog.Removed(id); removed {
			t.Errorf("%s is reported removed before anything was", id)
		}
	}
	if err := catalog.Remove(forgotten.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if removal, removed := catalog.Removed(forgotten.ID); !removed || removal.ObjectRef != "forgotten-object" {
		t.Errorf("the removal reads %+v, removed %v, want it to name the object still on the network", removal, removed)
	}
	if err := catalog.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	catalog.Close()

	reopened := openCatalog(t, dir)
	if owed := reopened.RemovedOnNetwork(); len(owed) != 1 || owed[0].ID != forgotten.ID {
		t.Fatalf("after a compaction and a reopen the catalog owes %+v, want the removed record's object", owed)
	}
	if err := reopened.ObjectDeleted(forgotten.ID, "forgotten-object"); err != nil {
		t.Fatalf("record the object deleted: %v", err)
	}
	if owed := reopened.RemovedOnNetwork(); len(owed) != 0 {
		t.Errorf("the catalog still owes %+v after the object was deleted", owed)
	}
	if err := reopened.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	reopened.Close()

	last := openCatalog(t, dir)
	if owed := last.RemovedOnNetwork(); len(owed) != 0 {
		t.Errorf("after a reopen the catalog owes %+v, want nothing", owed)
	}
	removal, removed := last.Removed(forgotten.ID)
	if !removed || removal.ObjectRef != "" || removal.SlabID != "" {
		t.Errorf("the removal reads %+v, removed %v, want a removal that locates nothing", removal, removed)
	}
	if _, err := last.Lookup(forgotten.ID); !errors.Is(err, manifest.ErrNotFound) {
		t.Errorf("the removed record resolves again: %v", err)
	}
	if got, err := last.Lookup(kept.ID); err != nil || got.ObjectRef != "kept-object" {
		t.Errorf("the record still held resolves to %+v: %v", got, err)
	}
}

// Recording an object deleted changes only the removal that names it. A record
// still held, a removal naming another object, and a record the catalog never
// named are all left as they are, and the log is not written at all.
func TestAnObjectDeletedChangesOnlyTheRemovalThatNamesIt(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	held, forgotten := entry(t, "held-object"), entry(t, "forgotten-object")
	for _, e := range []manifest.Entry{held, forgotten} {
		if err := catalog.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := catalog.Remove(forgotten.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	unknown, err := record.NewID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}
	written := catalog.Stats().Written

	for _, c := range []struct {
		id  record.ID
		ref string
	}{
		{held.ID, "held-object"},
		{forgotten.ID, "another-object"},
		{unknown, "forgotten-object"},
	} {
		if err := catalog.ObjectDeleted(c.id, c.ref); err != nil {
			t.Fatalf("record %s's object %s deleted: %v", c.id, c.ref, err)
		}
	}

	if got, err := catalog.Lookup(held.ID); err != nil || got.ObjectRef != "held-object" {
		t.Errorf("the record still held resolves to %+v: %v", got, err)
	}
	if owed := catalog.RemovedOnNetwork(); len(owed) != 1 || owed[0].ObjectRef != "forgotten-object" {
		t.Errorf("the catalog owes %+v, want the removal's own object", owed)
	}
	if got := catalog.Stats().Written; got != written {
		t.Errorf("the catalog wrote %d byte(s) for changes it did not make", got-written)
	}
}
