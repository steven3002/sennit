package reclaim_test

import (
	"testing"

	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store/reclaim"
)

// The device that wrote a record keeps it when another device that restored the
// vault forgets it and deletes its object, and its catalog goes on naming that
// object. A sweep there is not refused for it and deletes nothing for it, and it
// keeps the slab the catalog names, because the keep-set is checked against the
// catalog itself and the plan acts only on objects the account still holds.
//
// When the deleted object was the last one on its slab, the slab stays billed:
// the writer's catalog still names it. It comes back when the writer forgets the
// record too, or through the release of slabs that hold nothing.
func TestASweepAcceptsACatalogNamingAnObjectAnotherDeviceDeleted(t *testing.T) {
	t.Parallel()
	tracked := pinned("slab-a")

	// Three records on one slab, the third forgotten and deleted elsewhere.
	live := reclaim.Live{Objects: keep(1, 2, 3), Slabs: slabs("slab-a"), Catalogued: 3}
	if err := live.Check(len(tracked)); err != nil {
		t.Fatalf("the writer's keep-set is refused: %v", err)
	}
	plan := reclaim.PlanSweepForTest(live, tracked, []sia.StoredObject{object(1, "slab-a"), object(2, "slab-a")})
	if len(plan.DeadObjects) != 0 || len(plan.ReleasableSlabs) != 0 {
		t.Errorf("the sweep plans to delete %v and release %v, want nothing", plan.DeadObjects, plan.ReleasableSlabs)
	}

	// The forgotten record was the only one on its slab.
	alone := reclaim.Live{Objects: keep(3), Slabs: slabs("slab-a"), Catalogued: 1}
	if err := alone.Check(len(tracked)); err != nil {
		t.Fatalf("the writer's keep-set is refused when the slab's last object is gone: %v", err)
	}
	plan = reclaim.PlanSweepForTest(alone, tracked, nil)
	if len(plan.DeadObjects) != 0 || len(plan.ReleasableSlabs) != 0 {
		t.Errorf("the sweep plans to delete %v and release %v, want nothing while the catalog names the slab",
			plan.DeadObjects, plan.ReleasableSlabs)
	}
}
