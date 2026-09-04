// Package reclaim tracks slabs and reclaims dead ones.
package reclaim

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/sia"
)

// ErrNotOursToRelease reports that an operation would have unpinned storage
// this installation did not pin.
var ErrNotOursToRelease = errors.New("this device did not pin that storage")

// A Reclaimer knows which slabs this vault has pinned.
//
// Nothing else does. The indexer bills a slab whether it holds live records or
// none, the released SDK offers no way to release a recent one, and a
// partially-filled slab can never be extended, so every flush strands one.
// Without this list that quota is unrecoverable and invisible at the same time.
type Reclaimer struct {
	client *sia.Client
	store  *local.Store
}

// New builds a reclaimer over the device's slab ledger.
func New(client *sia.Client, store *local.Store) *Reclaimer {
	return &Reclaimer{client: client, store: store}
}

// Track records that a flush pinned a slab.
func (r *Reclaimer) Track(slabID sia.SlabID, records int, bytes int64) error {
	if slabID == "" {
		return nil
	}
	return r.store.TrackSlab(string(slabID), records, bytes)
}

// Adopt records a slab this installation found by hydrating rather than pinned.
//
// The distinction is the whole of the hydration deletion hazard: a hydrated
// vault can read every slab holding a record its phrase opens, and would
// otherwise inherit the right to unpin storage the device that wrote it is
// still pointing at.
func (r *Reclaimer) Adopt(slabID sia.SlabID, records int, bytes int64) error {
	if slabID == "" {
		return nil
	}
	return r.store.AdoptSlab(string(slabID), records, bytes)
}

// A Plan is what a sweep has decided to destroy, before it destroys anything.
//
// Separating the decision from the act is deliberate. Everything that has gone
// wrong with reclamation here went wrong in the decision, a keep-set that
// matched nothing, an ordering that stranded objects, and a decision that only
// exists inside the loop that acts on it can only be checked by running it
// against real storage.
type Plan struct {
	// DeadObjects sit on this vault's slabs with nothing pointing at them.
	DeadObjects []sia.ObjectRef
	// ReleasableSlabs are this vault's slabs that no live record needs.
	ReleasableSlabs []sia.SlabID
	// HeldSlabs were left alone because another device pinned them.
	HeldSlabs int
	// ForeignObjects sit outside this vault's slabs entirely and are not its
	// business, whether or not the catalog names them.
	ForeignObjects int
}

// planSweep decides what a sweep would destroy.
//
// A slab is shared: one flush packs many records into it and it is billed
// whole. So an object is dead only when the catalog does not name it, and a
// slab is releasable only when the catalog names nothing on it, forgetting one
// record out of a hundred in a slab frees exactly nothing, and a sweep that
// released the slab anyway would take the other ninety-nine with it.
func planSweep(live Live, tracked []Slab, objects []sia.StoredObject) Plan {
	var plan Plan
	ours := make(map[sia.SlabID]struct{}, len(tracked))
	for _, slab := range tracked {
		if !slab.Releasable() {
			plan.HeldSlabs++
			continue
		}
		ours[slab.ID] = struct{}{}
	}

	for _, object := range objects {
		if _, mine := ours[object.Slab]; !mine {
			plan.ForeignObjects++
			continue
		}
		if _, alive := live.Objects[object.Ref]; alive {
			continue
		}
		plan.DeadObjects = append(plan.DeadObjects, object.Ref)
	}

	for slabID := range ours {
		if _, alive := live.Slabs[slabID]; alive {
			continue
		}
		plan.ReleasableSlabs = append(plan.ReleasableSlabs, slabID)
	}
	return plan
}

// A Slab is one tracked slab.
type Slab struct {
	ID       sia.SlabID
	PinnedAt time.Time
	Records  int
	Bytes    int64
	// Origin says whether this installation pinned the slab or found it. Only a
	// slab it pinned is its to release.
	Origin local.SlabOrigin
}

// Releasable reports whether this device may unpin the slab.
func (s Slab) Releasable() bool { return s.Origin.Releasable() }

// Tracked lists every slab in this vault's ledger, oldest first.
func (r *Reclaimer) Tracked() ([]Slab, error) {
	rows, err := r.store.TrackedSlabs()
	if err != nil {
		return nil, err
	}
	out := make([]Slab, len(rows))
	for i, row := range rows {
		out[i] = Slab{
			ID:       sia.SlabID(row.SlabID),
			PinnedAt: row.PinnedAt,
			Records:  row.Records,
			Bytes:    row.Bytes,
			Origin:   row.Origin,
		}
	}
	return out, nil
}

// ErrKeepSetDegenerate reports that a sweep was asked to treat the storage of a
// non-empty vault as dead.
//
// It is returned instead of deleting, because the two situations a degenerate
// keep-set can describe are not equally likely. "Every record is gone" is rare
// and deliberate; "the keep-set was computed wrongly" is a defect, and it has
// happened twice here. Refusing costs a run of a command. Proceeding costs the
// data.
var ErrKeepSetDegenerate = errors.New("refusing to sweep on a keep-set that does not cover the vault")

// A Live set names everything a sweep must leave alone.
//
// It is supplied rather than derived here, because what is live is a question
// about records, and this package deliberately knows nothing about them.
type Live struct {
	Objects map[sia.ObjectRef]struct{}
	Slabs   map[sia.SlabID]struct{}

	// Catalogued is how many records the caller believes it still holds.
	//
	// It is stated separately from Objects rather than inferred from it,
	// because the whole failure this guards against is the two disagreeing.
	// A caller that derived this number from the same map it derived Objects
	// from would agree with itself no matter how wrong both were.
	Catalogued int

	// ReleaseAll authorises a sweep whose keep-set is empty because the vault
	// is genuinely empty rather than because the keep-set is wrong.
	//
	// Emptying a vault and then reclaiming its storage is a legitimate thing to
	// want. It is spelled out here so that it has to be *asked* for: an empty
	// catalog is also what a truncated log, an unreadable snapshot or the wrong
	// home directory looks like, and those must not release anything.
	ReleaseAll bool
}

// Check reports whether this keep-set can be trusted to bound a deletion over
// pinned slabs.
//
// This is invariant I12, and it is the reason the package refuses work rather
// than doing it: an empty keep-set means the computation is wrong, never that
// everything is dead. The keep-set must account for every record the caller
// says it holds, because any record the keep-set fails to name is a record
// whose object becomes a deletion candidate.
func (l Live) Check(pinned int) error {
	switch {
	case l.Catalogued < 0:
		return fmt.Errorf("%w: the catalog reported %d records", ErrKeepSetDegenerate, l.Catalogued)

	case l.Catalogued == 0 && pinned == 0:
		// Nothing held, nothing pinned: the sweep has no work and no reach.
		return nil

	case l.Catalogued == 0 && !l.ReleaseAll:
		return fmt.Errorf(
			"%w: the catalog holds no records while %d slab(s) are pinned. "+
				"That is what an emptied vault looks like and also what a lost or unreadable catalog looks like. "+
				"Check the vault is opened against the right home directory; if the vault really is empty, "+
				"say so explicitly to release its storage",
			ErrKeepSetDegenerate, pinned)

	case l.Catalogued == 0:
		return nil

	case len(l.Objects) == 0:
		return fmt.Errorf(
			"%w: the catalog holds %d record(s) and the keep-set names none of their objects. "+
				"Every one of them would be deleted",
			ErrKeepSetDegenerate, l.Catalogued)

	case len(l.Objects) < l.Catalogued:
		return fmt.Errorf(
			"%w: the catalog holds %d record(s) and the keep-set names only %d object(s). "+
				"The %d that are missing would be deleted",
			ErrKeepSetDegenerate, l.Catalogued, len(l.Objects), l.Catalogued-len(l.Objects))

	case len(l.Slabs) == 0:
		return fmt.Errorf(
			"%w: the catalog holds %d record(s) and the keep-set names no slab to keep. "+
				"Every slab this vault pinned would be released, and the objects on them made unopenable",
			ErrKeepSetDegenerate, l.Catalogued)
	}
	return nil
}

// A Sweep reports what a reclamation found and released.
type Sweep struct {
	// ObjectsSeen is how many objects the account held.
	ObjectsSeen int
	// ObjectsDeleted is how many of those sat on this vault's slabs with
	// nothing pointing at them any more.
	ObjectsDeleted int
	// Unreadable is how many objects the account holds that the indexer could
	// not open. They are reported rather than removed: they carry no slab to
	// check them against, so dropping them is a separate, deliberate act.
	Unreadable int
	// SlabsReleased is how many slabs were unpinned.
	SlabsReleased int
	// SlabsHeld is how many slabs the sweep left alone because another device
	// pinned them and this one only hydrated them. They are reported rather
	// than passed over: to a user they are quota that did not come back, and
	// the reason is not otherwise visible from here.
	SlabsHeld int
	// Before and After are the account's quota either side of the sweep, which
	// is the only figure that says whether anything was actually returned.
	Before, After sia.Account
	Elapsed       time.Duration
}

// Freed reports the quota the sweep returned.
func (s Sweep) Freed() uint64 {
	if s.After.PinnedData >= s.Before.PinnedData {
		return 0
	}
	return s.Before.PinnedData - s.After.PinnedData
}

// Sweep releases the storage this vault pinned and no longer needs.
//
// Deleting a record frees nothing on its own, because a slab is shared and
// billed whole: the space comes back only when a slab has no live object left
// and the slab itself is released. So this runs in two passes, and their order
// is not interchangeable. Releasing a slab while an object still points into it
// does not merely orphan the object, the object's identity is derived from the
// slab's sectors, so it becomes something no key opens, and it goes on billing
// and on being listed. Objects first, then slabs.
//
// The ledger, not the account listing, is what bounds this. An account can hold
// objects this vault did not write, another vault under the same key, or an
// older tool, and a sweep that reasoned from "everything the catalog does not
// name" would delete them. Only slabs this vault pinned are its to release.
//
// The keep-set is checked against the ledger before any of that begins. See
// Live.Check: a sweep is the one operation here that cannot be undone, so it
// refuses a keep-set it cannot trust rather than acting on it.
func (r *Reclaimer) Sweep(ctx context.Context, live Live) (Sweep, error) {
	start := time.Now()
	report := Sweep{}

	tracked, err := r.Tracked()
	if err != nil {
		return report, err
	}
	if err := live.Check(len(tracked)); err != nil {
		return report, err
	}
	before, err := r.client.Account(ctx)
	if err != nil {
		return report, err
	}
	report.Before = before

	var objects []sia.StoredObject
	stats, err := r.client.WalkObjectsStats(ctx, func(object sia.StoredObject) error {
		report.ObjectsSeen++
		objects = append(objects, object)
		return nil
	})
	if err != nil {
		return report, err
	}
	report.Unreadable = len(stats.Unreadable)

	// Decided in full before anything is destroyed. See planSweep.
	plan := planSweep(live, tracked, objects)
	report.SlabsHeld = plan.HeldSlabs

	if err := r.client.DeleteObjects(ctx, plan.DeadObjects); err != nil {
		return report, fmt.Errorf("delete %d dead object(s): %w", len(plan.DeadObjects), err)
	}
	report.ObjectsDeleted = len(plan.DeadObjects)

	for _, slabID := range plan.ReleasableSlabs {
		if err := r.Unpin(ctx, slabID); err != nil {
			return report, err
		}
		report.SlabsReleased++
	}

	if report.After, err = r.client.Account(ctx); err != nil {
		return report, err
	}
	report.Elapsed = time.Since(start)
	return report, nil
}

// An Orphan is a slab the account is billed for that holds nothing at all.
type Orphan struct {
	ID sia.SlabID
	// Tracked reports whether this device's ledger knew about it. A slab that
	// was not tracked was stranded by an installation that is gone.
	Tracked bool
}

// checkOccupancy applies I12 to the orphan scan.
//
// The scan asks the indexer twice, once for what is pinned, once for what
// holds an object, and calls the difference dead. If the second answer comes
// back empty while the first does not, the difference is the whole account, and
// a walk that yielded nothing is a far more likely explanation than an account
// of slabs that all hold nothing. The caller states which it is.
func checkOccupancy(pinned, occupied int, releaseAll bool) error {
	if pinned == 0 || occupied > 0 || releaseAll {
		return nil
	}
	return fmt.Errorf(
		"%w: the account has %d pinned slab(s) and the object walk found nothing on any of them. "+
			"An empty walk and an empty account look identical from here; if the account really holds "+
			"no objects, say so explicitly to release the slabs",
		ErrKeepSetDegenerate, pinned)
}

// Orphans lists slabs the account pays for that hold no object.
//
// The indexer's slab list is the authority here, not the ledger. A slab whose
// objects have all been deleted goes on billing forever, and if the device that
// pinned it is gone, reinstalled, replaced, or simply run from a different
// directory, nothing local remembers it exists. Asking the indexer what it is
// charging for is the only way to find that storage.
func (r *Reclaimer) Orphans(ctx context.Context, releaseAll bool) ([]Orphan, error) {
	pinned, err := r.client.PinnedSlabs(ctx)
	if err != nil {
		return nil, err
	}
	occupied := make(map[sia.SlabID]struct{}, len(pinned))
	if err := r.client.WalkObjects(ctx, func(object sia.StoredObject) error {
		if object.Slab != "" {
			occupied[object.Slab] = struct{}{}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := checkOccupancy(len(pinned), len(occupied), releaseAll); err != nil {
		return nil, err
	}

	tracked, err := r.Tracked()
	if err != nil {
		return nil, err
	}
	known := make(map[sia.SlabID]struct{}, len(tracked))
	for _, slab := range tracked {
		known[slab.ID] = struct{}{}
	}

	var out []Orphan
	for _, id := range pinned {
		if _, holds := occupied[id]; holds {
			continue
		}
		_, isKnown := known[id]
		out = append(out, Orphan{ID: id, Tracked: isKnown})
	}
	return out, nil
}

// An Unledgered slab is one the account is billed for that this device's
// ledger has no record of.
//
// There are two ways to reach that state and they need telling apart, which is
// why this reports rather than acts. One is another installation of this vault,
// whose storage is not this device's to release. The other is this device's own
// interrupted write: a slab is pinned by the network before its id can be
// written down, so a process killed in that window leaves storage nothing local
// remembers. The second kind is otherwise silent, the ledger-bounded sweep
// cannot see it, and a user meets it as quota that will not come back.
type Unledgered struct {
	ID sia.SlabID
	// Empty reports that no object sits on the slab, which is what an
	// interrupted write leaves behind and what makes the slab safe to release.
	Empty bool
}

// Unledgered lists slabs the account pays for that this device did not record.
func (r *Reclaimer) Unledgered(ctx context.Context) ([]Unledgered, error) {
	pinned, err := r.client.PinnedSlabs(ctx)
	if err != nil {
		return nil, err
	}
	occupied := make(map[sia.SlabID]struct{}, len(pinned))
	if err := r.client.WalkObjects(ctx, func(object sia.StoredObject) error {
		if object.Slab != "" {
			occupied[object.Slab] = struct{}{}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	tracked, err := r.Tracked()
	if err != nil {
		return nil, err
	}
	known := make(map[sia.SlabID]struct{}, len(tracked))
	for _, slab := range tracked {
		known[slab.ID] = struct{}{}
	}

	var out []Unledgered
	for _, id := range pinned {
		if _, seen := known[id]; seen {
			continue
		}
		_, holds := occupied[id]
		out = append(out, Unledgered{ID: id, Empty: !holds})
	}
	return out, nil
}

// ReleaseOrphans unpins slabs that hold nothing and returns their quota.
//
// Nothing readable is lost by construction: a slab with no object over it has
// no way to be read even in principle, since the indexer's object map is the
// only thing that turns a slab into retrievable bytes.
func (r *Reclaimer) ReleaseOrphans(ctx context.Context, releaseAll bool) (Sweep, error) {
	start := time.Now()
	var report Sweep

	var err error
	if report.Before, err = r.client.Account(ctx); err != nil {
		return report, err
	}
	orphans, err := r.Orphans(ctx, releaseAll)
	if err != nil {
		return report, err
	}
	for _, orphan := range orphans {
		if err := r.Unpin(ctx, orphan.ID); err != nil {
			return report, err
		}
		report.SlabsReleased++
	}
	if report.After, err = r.client.Account(ctx); err != nil {
		return report, err
	}
	report.Elapsed = time.Since(start)
	return report, nil
}

// DropUnreadable deletes objects the indexer will not open.
//
// They hold nothing recoverable and keep their slab alive, so they are pure
// cost, but they carry no slab this vault can check them against, which is
// why removing them is asked for rather than assumed. The state is reached by
// releasing a slab while objects still pointed into it.
func (r *Reclaimer) DropUnreadable(ctx context.Context) ([]sia.ObjectRef, error) {
	stats, err := r.client.WalkObjectsStats(ctx, func(sia.StoredObject) error { return nil })
	if err != nil {
		return nil, err
	}
	if len(stats.Unreadable) == 0 {
		return nil, nil
	}
	if err := r.client.DeleteObjects(ctx, stats.Unreadable); err != nil {
		return nil, fmt.Errorf("delete %d unreadable object(s): %w", len(stats.Unreadable), err)
	}
	return stats.Unreadable, nil
}

// Unpin releases one slab and drops it from the ledger.
//
// This is the only mechanism that returns quota promptly. It is exposed for
// deliberate release; scheduling it is a separate decision, because the
// operation needs spare headroom to run and an account that has run out of room
// can no longer afford the very thing that would give it room back.
func (r *Reclaimer) Unpin(ctx context.Context, slabID sia.SlabID) error {
	if err := r.client.UnpinSlab(ctx, slabID); err != nil {
		return err
	}
	if err := r.store.ForgetSlab(string(slabID)); err != nil {
		return fmt.Errorf("slab %s was released but the ledger still lists it: %w", slabID, err)
	}
	// Every cached location over that slab now points at sectors that are gone.
	return r.store.ForgetSlabMeta(string(slabID))
}

// Account reports the vault's standing with the indexer.
func (r *Reclaimer) Account(ctx context.Context) (sia.Account, error) { return r.client.Account(ctx) }
