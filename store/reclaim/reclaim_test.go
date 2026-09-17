package reclaim_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store/reclaim"
)

func refs(n int) map[sia.ObjectRef]struct{} {
	out := make(map[sia.ObjectRef]struct{}, n)
	for i := 0; i < n; i++ {
		var ref sia.ObjectRef
		ref.ID[0] = byte(i + 1)
		out[ref] = struct{}{}
	}
	return out
}

func slabs(names ...string) map[sia.SlabID]struct{} {
	out := make(map[sia.SlabID]struct{}, len(names))
	for _, name := range names {
		out[sia.SlabID(name)] = struct{}{}
	}
	return out
}

// I12. A keep-set that does not account for the vault must stop the sweep.
//
// Every refusing case below is a state this project has actually reached or
// come one defect away from reaching. The keep-set that destroyed four slabs in
// S6 was the first of them: it counted twenty-one records and named none of
// their objects, and nothing asked it to agree with itself.
func TestKeepSetMustCoverTheVault(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		live   reclaim.Live
		pinned int
		refuse bool
	}{
		{
			name:   "a keep-set covering every record proceeds",
			live:   reclaim.Live{Objects: refs(3), Slabs: slabs("a"), Catalogued: 3},
			pinned: 2,
		},
		{
			name:   "records catalogued, no objects named",
			live:   reclaim.Live{Objects: refs(0), Slabs: slabs("a"), Catalogued: 21},
			pinned: 5,
			refuse: true,
		},
		{
			name:   "the keep-set covers only part of the catalog",
			live:   reclaim.Live{Objects: refs(2), Slabs: slabs("a"), Catalogued: 3},
			pinned: 1,
			refuse: true,
		},
		{
			name:   "records catalogued, no slab named",
			live:   reclaim.Live{Objects: refs(3), Slabs: slabs(), Catalogued: 3},
			pinned: 1,
			refuse: true,
		},
		{
			name:   "an empty catalog over pinned slabs is not an authorisation to delete",
			live:   reclaim.Live{Objects: refs(0), Slabs: slabs(), Catalogued: 0},
			pinned: 4,
			refuse: true,
		},
		{
			name:   "an emptied vault releases its storage when asked to explicitly",
			live:   reclaim.Live{Objects: refs(0), Slabs: slabs(), Catalogued: 0, ReleaseAll: true},
			pinned: 4,
		},
		{
			name:   "an empty vault with nothing pinned has no work and no reach",
			live:   reclaim.Live{Objects: refs(0), Slabs: slabs(), Catalogued: 0},
			pinned: 0,
		},
		{
			name:   "a negative catalog count is a broken caller, not an empty vault",
			live:   reclaim.Live{Objects: refs(0), Slabs: slabs(), Catalogued: -1, ReleaseAll: true},
			pinned: 4,
			refuse: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.live.Check(tc.pinned)
			switch {
			case tc.refuse && err == nil:
				t.Fatal("the sweep was allowed to proceed on a keep-set that does not cover the vault")
			case tc.refuse && !errors.Is(err, reclaim.ErrKeepSetDegenerate):
				t.Fatalf("refused with %v, which callers cannot distinguish from an ordinary failure", err)
			case !tc.refuse && err != nil:
				t.Fatalf("a sound keep-set was refused: %v", err)
			}
		})
	}
}

// The guard has to sit in front of the network calls, not among them.
//
// A reclaimer with no client cannot make a single request without a nil
// dereference, so a degenerate sweep that comes back with the refusal, rather
// than a panic, is proof that nothing was attempted first. If the check is
// ever moved below the first client call, or dropped, this fails.
func TestSweepRefusesBeforeItTouchesTheNetwork(t *testing.T) {
	t.Parallel()

	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	for _, id := range []string{"slab-one", "slab-two"} {
		if err := store.TrackSlab(id, 10, 40<<20); err != nil {
			t.Fatalf("track %s: %v", id, err)
		}
	}

	reclaimer := reclaim.New(nil, store)
	degenerate := reclaim.Live{
		Objects:    map[sia.ObjectRef]struct{}{},
		Slabs:      map[sia.SlabID]struct{}{},
		Catalogued: 21,
	}

	report, err := reclaimer.Sweep(context.Background(), degenerate)
	if !errors.Is(err, reclaim.ErrKeepSetDegenerate) {
		t.Fatalf("a degenerate sweep returned %v; the guard is not in front of the network calls", err)
	}
	if report.ObjectsDeleted != 0 || report.SlabsReleased != 0 {
		t.Fatalf("the refused sweep still reports %d object(s) deleted and %d slab(s) released",
			report.ObjectsDeleted, report.SlabsReleased)
	}
	if tracked, err := reclaimer.Tracked(); err != nil || len(tracked) != 2 {
		t.Fatalf("the ledger holds %v (err %v) after a refused sweep, want both slabs", tracked, err)
	}
}

// The orphan scan reads an empty answer as an empty answer, not as an empty
// account. It asks the indexer twice and deletes the difference, so a walk that
// returns nothing turns every slab the account holds into a candidate.
func TestOrphanScanRefusesAnEmptyObjectWalk(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		pinned, occupied int
		releaseAll       bool
		refuse           bool
	}{
		{name: "a walk that found objects proceeds", pinned: 3, occupied: 2},
		{name: "nothing pinned, nothing to reach", pinned: 0, occupied: 0},
		{name: "pinned slabs and an empty walk", pinned: 3, occupied: 0, refuse: true},
		{name: "an account that really is empty, asked for explicitly", pinned: 3, occupied: 0, releaseAll: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := reclaim.CheckOccupancyForTest(tc.pinned, tc.occupied, tc.releaseAll)
			switch {
			case tc.refuse && !errors.Is(err, reclaim.ErrKeepSetDegenerate):
				t.Fatalf("an empty walk over %d pinned slab(s) returned %v, and would have released them",
					tc.pinned, err)
			case !tc.refuse && err != nil:
				t.Fatalf("a sound scan was refused: %v", err)
			}
		})
	}
}

// The hydration deletion hazard: a device that hydrated a vault reaches every
// slab holding a record its phrase opens, and must not be able to release one.
//
// Nothing about the records is wrong, they are this vault's. The slab is not.
// The device that pinned it is still pointing at it, and this one's catalog was
// built from whatever its object walk could read, so an object that failed to
// read here would fall outside the keep-set and be deleted from under a device
// for which it is live.
func TestAHydratedSlabIsNotThisDevicesToRelease(t *testing.T) {
	t.Parallel()

	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	if err := store.TrackSlab("ours", 4, 40<<20); err != nil {
		t.Fatalf("track: %v", err)
	}
	if err := store.AdoptSlab("theirs", 9, 40<<20); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	tracked, err := reclaim.New(nil, store).Tracked()
	if err != nil {
		t.Fatalf("tracked: %v", err)
	}
	if len(tracked) != 2 {
		t.Fatalf("the ledger lists %d slab(s), want both", len(tracked))
	}
	releasable := map[sia.SlabID]bool{}
	for _, slab := range tracked {
		releasable[slab.ID] = slab.Releasable()
	}
	if !releasable["ours"] {
		t.Error("a slab this device pinned came back as another device's")
	}
	if releasable["theirs"] {
		t.Error("a hydrated slab is releasable here, which is the hazard itself")
	}
}

// A pin outranks a hydration whichever order they arrive in. A device that
// wrote into a slab keeps the right to release it even if it later hydrates and
// meets the same slab again, and a device that hydrates a slab it already
// pinned does not lose it.
func TestPinningOutranksHydration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		apply func(*local.Store) error
	}{
		{
			name: "hydrated after being pinned",
			apply: func(s *local.Store) error {
				if err := s.TrackSlab("slab", 1, 1); err != nil {
					return err
				}
				return s.AdoptSlab("slab", 1, 1)
			},
		},
		{
			name: "pinned after being hydrated",
			apply: func(s *local.Store) error {
				if err := s.AdoptSlab("slab", 1, 1); err != nil {
					return err
				}
				return s.TrackSlab("slab", 1, 1)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer store.Close()

			if err := tc.apply(store); err != nil {
				t.Fatalf("apply: %v", err)
			}
			rows, err := store.TrackedSlabs()
			if err != nil {
				t.Fatalf("tracked: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("%d ledger row(s) for one slab", len(rows))
			}
			if rows[0].Origin != local.SlabPinned {
				t.Fatalf("origin is %q; a slab this device pinned must stay its to release", rows[0].Origin)
			}
		})
	}
}

// Repack's last act is to unpin every slab it read from, so it must refuse
// before it writes anything when those slabs are another device's.
func TestRepackRefusesAnotherDevicesSlabs(t *testing.T) {
	t.Parallel()

	store, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	if err := store.AdoptSlab("theirs", 9, 40<<20); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	heldID, err := record.NewID()
	if err != nil {
		t.Fatalf("record id: %v", err)
	}

	// A nil client again: reaching the network at all would mean the check runs
	// too late to prevent the write.
	_, err = reclaim.New(nil, store).Repack(context.Background(), nil,
		[]reclaim.Held{{ID: heldID, SlabID: "theirs"}}, func([]reclaim.Moved) error { return nil })
	if !errors.Is(err, reclaim.ErrNotOursToRelease) {
		t.Fatalf("repack over a hydrated slab returned %v, want a refusal before any write", err)
	}
}

func account(pinned, max uint64) sia.Account {
	return sia.Account{Ready: true, PinnedData: pinned, MaxPinnedData: max}
}

// The client-side half of what an account does as it approaches its ceiling.
//
// Filling a 46.57 GiB tier to find out is the one experiment this project has
// only one account to run, so what is checked here is the gate that stands in
// front of it: repack must be refused on the transient peak it needs, not on
// the steady state it would end at. An account can be under its limit both
// before and after a repack and unable to afford the moment in between, and
// that moment is when both sets of slabs are pinned.
func TestWatermarkRefusesWithoutRoomForThePeak(t *testing.T) {
	t.Parallel()

	const slab = 40 << 20
	const tier = 50_000_000_000

	cases := []struct {
		name            string
		pinned          uint64
		slabs           int
		due, affordable bool
	}{
		{name: "an empty account", pinned: 0, slabs: 0, affordable: true},
		{name: "below the watermark", pinned: 10 << 30, slabs: 4, affordable: true},
		{name: "past the watermark with room for the peak", pinned: 40 << 30, slabs: 4, due: true, affordable: true},
		{
			// The state the gate exists for. Repack is worth doing and the
			// account can no longer hold the old slabs and the new ones at once,
			// so the operation that would give the space back is the one it can
			// no longer afford.
			name:   "past the watermark with less than the peak free",
			pinned: tier - slab, slabs: 4, due: true, affordable: false,
		},
		{name: "exactly full", pinned: tier, slabs: 1, due: true, affordable: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check := reclaim.Watermark(account(tc.pinned, tier), tc.slabs, slab)
			if check.Due != tc.due {
				t.Errorf("Due is %v, want %v at %.1f%% used", check.Due, tc.due, 100*check.Used)
			}
			if check.Affordable != tc.affordable {
				t.Errorf("Affordable is %v, want %v: needs %d B free of %d B",
					check.Affordable, tc.affordable, check.Headroom, tier-tc.pinned)
			}
		})
	}
}

func object(ref byte, slab string) sia.StoredObject {
	var r sia.ObjectRef
	r.ID[0] = ref
	return sia.StoredObject{Ref: r, Slab: sia.SlabID(slab)}
}

func objectRef(ref byte) sia.ObjectRef {
	var r sia.ObjectRef
	r.ID[0] = ref
	return r
}

func keep(refs ...byte) map[sia.ObjectRef]struct{} {
	out := make(map[sia.ObjectRef]struct{}, len(refs))
	for _, ref := range refs {
		out[objectRef(ref)] = struct{}{}
	}
	return out
}

func pinned(ids ...string) []reclaim.Slab {
	out := make([]reclaim.Slab, len(ids))
	for i, id := range ids {
		out[i] = reclaim.Slab{ID: sia.SlabID(id), Origin: local.SlabPinned}
	}
	return out
}

// A slab is shared and billed whole, so reclamation is about slabs and not
// about records. Forgetting one record out of a slab returns nothing and must
// not endanger the rest; the space comes back only when the slab holds nothing
// live at all.
func TestReclaimOnASharedSlab(t *testing.T) {
	t.Parallel()

	// Two slabs of three records each, which is what two flushes leave behind.
	all := []sia.StoredObject{
		object(1, "slab-a"), object(2, "slab-a"), object(3, "slab-a"),
		object(4, "slab-b"), object(5, "slab-b"), object(6, "slab-b"),
	}

	cases := []struct {
		name     string
		live     reclaim.Live
		tracked  []reclaim.Slab
		wantDead []byte
		wantFree []string
	}{
		{
			name: "nothing forgotten frees nothing",
			live: reclaim.Live{
				Objects: keep(1, 2, 3, 4, 5, 6), Slabs: slabs("slab-a", "slab-b"), Catalogued: 6,
			},
			tracked: pinned("slab-a", "slab-b"),
		},
		{
			name: "one record forgotten out of a shared slab frees no slab",
			live: reclaim.Live{
				Objects: keep(1, 2, 4, 5, 6), Slabs: slabs("slab-a", "slab-b"), Catalogued: 5,
			},
			tracked:  pinned("slab-a", "slab-b"),
			wantDead: []byte{3},
		},
		{
			name: "emptying one slab frees exactly that slab",
			live: reclaim.Live{
				Objects: keep(4, 5, 6), Slabs: slabs("slab-b"), Catalogued: 3,
			},
			tracked:  pinned("slab-a", "slab-b"),
			wantDead: []byte{1, 2, 3},
			wantFree: []string{"slab-a"},
		},
		{
			name: "a slab this device never pinned is not touched, whatever the catalog says",
			live: reclaim.Live{
				Objects: keep(1, 2, 3), Slabs: slabs("slab-a"), Catalogued: 3,
			},
			tracked: pinned("slab-a"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := reclaim.PlanSweepForTest(tc.live, tc.tracked, all)

			var wantDead []sia.ObjectRef
			for _, ref := range tc.wantDead {
				wantDead = append(wantDead, objectRef(ref))
			}
			if len(plan.DeadObjects) != len(wantDead) {
				t.Fatalf("plans to delete %d object(s), want %d", len(plan.DeadObjects), len(wantDead))
			}
			dead := map[sia.ObjectRef]bool{}
			for _, ref := range plan.DeadObjects {
				dead[ref] = true
			}
			for _, ref := range wantDead {
				if !dead[ref] {
					t.Errorf("object %s survives and nothing points at it", ref)
				}
			}
			for ref := range tc.live.Objects {
				if dead[ref] {
					t.Fatalf("object %s is live in the catalog and the sweep plans to delete it", ref)
				}
			}

			free := map[sia.SlabID]bool{}
			for _, id := range plan.ReleasableSlabs {
				free[id] = true
			}
			if len(plan.ReleasableSlabs) != len(tc.wantFree) {
				t.Fatalf("plans to release %v, want %v", plan.ReleasableSlabs, tc.wantFree)
			}
			for _, id := range tc.wantFree {
				if !free[sia.SlabID(id)] {
					t.Errorf("slab %s holds nothing live and is not released", id)
				}
			}
			for id := range tc.live.Slabs {
				if free[id] {
					t.Fatalf("slab %s still holds live records and the sweep plans to release it", id)
				}
			}
		})
	}
}

// The plan never reaches outside this device's ledger. An account can hold
// another installation's storage, or another tool's, and neither is this
// vault's to delete however little the catalog knows about it.
func TestASweepNeverReachesOutsideItsOwnLedger(t *testing.T) {
	t.Parallel()

	all := []sia.StoredObject{
		object(1, "ours"),
		object(9, "someone-elses"),
		object(8, "hydrated-here"),
	}
	live := reclaim.Live{Objects: keep(1), Slabs: slabs("ours"), Catalogued: 1}
	tracked := append(pinned("ours"),
		reclaim.Slab{ID: "hydrated-here", Origin: local.SlabHydrated})

	plan := reclaim.PlanSweepForTest(live, tracked, all)

	if len(plan.DeadObjects) != 0 {
		t.Fatalf("plans to delete %v; none of it is this device's", plan.DeadObjects)
	}
	if len(plan.ReleasableSlabs) != 0 {
		t.Fatalf("plans to release %v; none of it is this device's", plan.ReleasableSlabs)
	}
	if plan.HeldSlabs != 1 {
		t.Errorf("held %d hydrated slab(s), want 1", plan.HeldSlabs)
	}
	if plan.ForeignObjects != 2 {
		t.Errorf("counted %d object(s) outside this ledger, want 2", plan.ForeignObjects)
	}
}

// A flush interrupted while pinning its objects leaves a slab that is billed
// and holds part of a batch the catalog never learned about. Whether an
// ordinary sweep can reach that slab is decided entirely by whether the ledger
// names it.
//
// Both halves are here because the second is what the write path now
// guarantees, and the first is what it used to leave behind. The sweep itself
// is the same in both.
func TestAStrandedSlabIsReachedOnlyThroughTheLedger(t *testing.T) {
	t.Parallel()

	// One slab holding a flush that completed, one holding the objects an
	// interrupted flush managed to pin, one belonging to another installation.
	all := []sia.StoredObject{
		object(1, "landed"), object(2, "landed"), object(3, "landed"),
		object(4, "stranded"), object(5, "stranded"),
		object(9, "someone-elses"),
	}
	// The catalog names the completed flush and nothing else. The interrupted
	// flush never got as far as cataloguing anything, and its records are back
	// on the queue.
	live := reclaim.Live{Objects: keep(1, 2, 3), Slabs: slabs("landed"), Catalogued: 3}

	t.Run("the ledger names it, so the sweep releases it", func(t *testing.T) {
		t.Parallel()
		plan := reclaim.PlanSweepForTest(live, pinned("landed", "stranded"), all)

		if len(plan.DeadObjects) != 2 {
			t.Errorf("plans to delete %v, want the two objects the interrupt pinned", plan.DeadObjects)
		}
		if len(plan.ReleasableSlabs) != 1 || plan.ReleasableSlabs[0] != "stranded" {
			t.Errorf("plans to release %v, want the stranded slab", plan.ReleasableSlabs)
		}
		if plan.ForeignObjects != 1 {
			t.Errorf("counted %d object(s) outside this ledger, want 1", plan.ForeignObjects)
		}
	})

	t.Run("the ledger does not name it, so nothing ordinary can", func(t *testing.T) {
		t.Parallel()
		plan := reclaim.PlanSweepForTest(live, pinned("landed"), all)

		if len(plan.DeadObjects) != 0 || len(plan.ReleasableSlabs) != 0 {
			t.Errorf("plans to delete %v and release %v from outside its ledger",
				plan.DeadObjects, plan.ReleasableSlabs)
		}
		// The stranded slab's objects are counted as another installation's,
		// which is what `status` says about them too, and it is false.
		if plan.ForeignObjects != 3 {
			t.Errorf("counted %d object(s) outside this ledger, want 3", plan.ForeignObjects)
		}
	})
}
