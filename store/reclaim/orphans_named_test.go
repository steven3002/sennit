package reclaim_test

import (
	"context"
	"slices"
	"testing"

	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store/reclaim"
)

// A release of slabs that hold nothing keeps one this vault's catalog still
// names for a record it holds, and releases every other, the slabs this
// device's ledger lists and the ones nothing here knows of alike.
//
// The kept slab is what a repack leaves under a record whose copy another
// device deleted. Released, it would leave this device's ledger while the
// catalog went on naming it, and every later repack would refuse to read from
// it.
func TestAnEmptySlabARecordHereNamesIsNotReleasedAsAnOrphan(t *testing.T) {
	t.Parallel()

	pinned := []sia.SlabID{"named", "ours", "stranded", "occupied"}
	occupied := slabs("occupied")
	known := slabs("named", "ours", "occupied")
	named := slabs("named", "occupied")

	orphans := reclaim.OrphansOfForTest(pinned, occupied, known, named)
	want := []reclaim.Orphan{
		{ID: "named", Tracked: true, Named: true},
		{ID: "ours", Tracked: true},
		{ID: "stranded"},
	}
	if !slices.Equal(orphans, want) {
		t.Errorf("the orphan scan found %+v, want %+v", orphans, want)
	}
	release, kept := reclaim.ReleasingForTest(orphans)
	if !slices.Equal(release, []sia.SlabID{"ours", "stranded"}) || kept != 1 {
		t.Errorf("a release would take %v and keep %d, want every orphan but the one a record here names, "+
			"and that one kept", release, kept)
	}
}

// Another device that releases a slab this one keeps for a record left behind
// takes it off the account and not out of this device's ledger. This device
// goes on as before: a second repack leaves the record where it is, a sweep
// keeps the slab, and once the record is forgotten here the sweep releases the
// slab, which counts as done though it is already gone.
func TestASlabAnotherDeviceReleasedLeavesThisDevicesRepackAndSweepWorking(t *testing.T) {
	_, network, writer, r := repackRig(t, "slab-a", "slab-b")
	first := network.holding(t, 1, "slab-a")
	gone := network.holding(t, 2, "slab-a")
	last := network.holding(t, 3, "slab-b")
	delete(network.objects, gone.Ref)

	var applied catalog
	if _, err := r.Repack(context.Background(), writer, []reclaim.Held{first, gone, last}, nil, applied.apply); err != nil {
		t.Fatalf("the first repack: %v", err)
	}
	// Another device's release of slabs that hold nothing takes slab-a: its
	// own catalog does not name it for a record it holds.
	network.released["slab-a"] = true
	if names := ledgerNames(t, r); !slices.Contains(names, "slab-a") {
		t.Fatalf("this device's ledger holds %v, want slab-a still in it", names)
	}

	held := []reclaim.Held{gone}
	for _, move := range applied.moves {
		held = append(held, reclaim.Held{ID: move.ID, CID: move.CID, Ref: move.To, SlabID: move.SlabID})
	}
	var again catalog
	second, err := r.Repack(context.Background(), writer, held, nil, again.apply)
	if err != nil {
		t.Fatalf("the second repack, after another device released slab-a: %v", err)
	}
	if !slices.Equal(second.Gone, []reclaim.Held{gone}) {
		t.Errorf("the second repack left %+v where it was, want the record whose copy is gone", second.Gone)
	}

	keep := func(withGone bool) reclaim.Live {
		live := reclaim.Live{Objects: map[sia.ObjectRef]struct{}{}, Slabs: map[sia.SlabID]struct{}{}}
		for _, move := range again.moves {
			live.Objects[move.To] = struct{}{}
			live.Slabs[move.SlabID] = struct{}{}
			live.Catalogued++
		}
		if withGone {
			live.Objects[gone.Ref] = struct{}{}
			live.Slabs[gone.SlabID] = struct{}{}
			live.Catalogued++
		}
		return live
	}
	tracked, err := r.Tracked()
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if plan := reclaim.PlanSweepForTest(keep(true), tracked, network.walk()); slices.Contains(plan.ReleasableSlabs, "slab-a") {
		t.Errorf("a sweep plans to release slab-a while the catalog names it: %v", plan.ReleasableSlabs)
	}

	// The record is forgotten here: the catalog names slab-a no more.
	plan := reclaim.PlanSweepForTest(keep(false), tracked, network.walk())
	if !slices.Equal(plan.ReleasableSlabs, []sia.SlabID{"slab-a"}) {
		t.Fatalf("once the record is forgotten a sweep plans to release %v, want slab-a", plan.ReleasableSlabs)
	}
	if err := r.Unpin(context.Background(), "slab-a"); err != nil {
		t.Errorf("releasing slab-a, which another device already released: %v", err)
	}
	if names := ledgerNames(t, r); slices.Contains(names, "slab-a") {
		t.Errorf("this device's ledger holds %v, want slab-a gone from it", names)
	}
}
