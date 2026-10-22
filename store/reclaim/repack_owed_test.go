package reclaim_test

import (
	"context"
	"slices"
	"testing"

	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store/reclaim"
)

// A repack over a slab that holds both a live record and the copy of a record
// this vault forgot, whose deletion is still owed, keeps that slab. The sweep
// that follows in the same reclaim then deletes the copy and releases the slab,
// objects first and slabs after, as every release goes.
func TestARepackKeepsASlabThatStillHoldsAForgottenRecordsCopy(t *testing.T) {
	_, network, writer, r := repackRig(t, "slab-a")
	live := network.holding(t, 1, "slab-a")
	// The forgotten record's copy is still on the network: its deletion is
	// owed, and the catalog's removal still names the object and slab-a.
	owed := network.holding(t, 2, "slab-a")

	var applied catalog
	if _, err := r.Repack(context.Background(), writer, []reclaim.Held{live}, []sia.SlabID{owed.SlabID},
		applied.apply); err != nil {
		t.Fatalf("repack: %v", err)
	}
	if slices.Contains(network.unpinned, "slab-a") {
		t.Errorf("the repack released slab-a while the forgotten record's copy %s was still on it", owed.Ref)
	}

	// The sweep that follows keeps what the catalog names: the record the
	// repack moved, where it moved it.
	keep := reclaim.Live{Objects: map[sia.ObjectRef]struct{}{}, Slabs: map[sia.SlabID]struct{}{}, Catalogued: 1}
	for _, move := range applied.moves {
		keep.Objects[move.To] = struct{}{}
		keep.Slabs[move.SlabID] = struct{}{}
	}
	tracked, err := r.Tracked()
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	plan := reclaim.PlanSweepForTest(keep, tracked, network.walk())
	if !slices.Contains(plan.DeadObjects, owed.Ref) {
		t.Errorf("the sweep does not plan to delete the forgotten record's copy %s: the ledger holds %v, "+
			"so it counts %d object(s) as another installation's", owed.Ref, ledgerNames(t, r), plan.ForeignObjects)
	}
	if !slices.Contains(plan.ReleasableSlabs, "slab-a") {
		t.Errorf("the sweep does not plan to release slab-a: it plans to release %v", plan.ReleasableSlabs)
	}
}
