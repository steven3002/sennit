package reclaim_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/reclaim"
)

// repackSlab is what the test's account bills for each slab it holds.
const repackSlab = 40 << 20

// A repackNetwork is the test's indexer for a repack: the objects it holds and
// the slab each is on, a failure to give in place of an object, and every
// deletion and release asked of it. It bills a slab's worth for each slab.
type repackNetwork struct {
	objects  map[sia.ObjectRef][]byte
	slabOf   map[sia.ObjectRef]sia.SlabID
	failures map[sia.ObjectRef]error
	deleted  []sia.ObjectRef
	unpinned []sia.SlabID
	// released holds every slab no longer pinned, whoever released it.
	released map[sia.SlabID]bool
	pinned   uint64
}

func (n *repackNetwork) Account(context.Context) (sia.Account, error) {
	return sia.Account{Ready: true, PinnedData: n.pinned, MaxPinnedData: 100 * repackSlab}, nil
}

// Download answers the way the client does: an object the indexer does not hold
// is reported as sia.ErrObjectGone, and a failure set for an object is returned
// as it is.
func (n *repackNetwork) Download(_ context.Context, ref sia.ObjectRef) ([]byte, error) {
	if err, ok := n.failures[ref]; ok {
		return nil, err
	}
	payload, ok := n.objects[ref]
	if !ok {
		return nil, fmt.Errorf("look up object %s: %w", ref, sia.ErrObjectGone)
	}
	return payload, nil
}

func (n *repackNetwork) DeleteObjects(_ context.Context, refs []sia.ObjectRef) error {
	for _, ref := range refs {
		n.deleted = append(n.deleted, ref)
		delete(n.objects, ref)
	}
	return nil
}

// UnpinSlab releases a slab. One that is already released counts as released,
// as the client counts it.
func (n *repackNetwork) UnpinSlab(_ context.Context, id sia.SlabID) error {
	n.unpinned = append(n.unpinned, id)
	if n.released[id] {
		return nil
	}
	n.released[id] = true
	n.pinned -= repackSlab
	return nil
}

// holding puts a record's object on the network, on slab, and returns the
// record as a repack is handed it.
func (n *repackNetwork) holding(t *testing.T, object byte, slab string) reclaim.Held {
	t.Helper()
	id, err := record.NewID()
	if err != nil {
		t.Fatalf("record id: %v", err)
	}
	var ref sia.ObjectRef
	ref.ID[0] = object
	n.objects[ref] = fmt.Appendf(nil, "the sealed record in object %d", object)
	n.slabOf[ref] = sia.SlabID(slab)
	return reclaim.Held{ID: id, CID: "cid-" + id.String(), Ref: ref, SlabID: sia.SlabID(slab)}
}

// walk is what the network holds, the way an object walk lists it.
func (n *repackNetwork) walk() []sia.StoredObject {
	var out []sia.StoredObject
	for ref := range n.objects {
		out = append(out, sia.StoredObject{Ref: ref, Slab: n.slabOf[ref]})
	}
	return out
}

// A repackWriter writes a batch the way the store does: the whole batch into
// one new slab, recorded in the ledger before it is pinned, and each blob at an
// object of its own.
type repackWriter struct {
	network *repackNetwork
	ledger  *local.Store
	batches [][]store.Blob
}

func (w *repackWriter) PutBatch(_ context.Context, blobs []store.Blob) (store.Flush, error) {
	w.batches = append(w.batches, blobs)
	slab := sia.SlabID(fmt.Sprintf("repacked-%d", len(w.batches)))
	var bytes int64
	for _, blob := range blobs {
		bytes += int64(len(blob.Payload))
	}
	if err := w.ledger.TrackSlab(string(slab), len(blobs), bytes); err != nil {
		return store.Flush{}, err
	}
	w.network.pinned += repackSlab
	flush := store.Flush{Slabs: []sia.SlabID{slab}}
	for i, blob := range blobs {
		var ref sia.ObjectRef
		ref.ID[0], ref.ID[1], ref.ID[2] = 0xA0, byte(len(w.batches)), byte(i)
		w.network.objects[ref] = blob.Payload
		w.network.slabOf[ref] = slab
		flush.Written = append(flush.Written,
			store.Written{CID: blob.CID, ObjectRef: ref, SlabID: slab, Bytes: len(blob.Payload)})
	}
	return flush, nil
}

// A catalog is the caller's side of a repack: every move it was handed, and
// how many times.
type catalog struct {
	moves []reclaim.Moved
	calls int
}

func (c *catalog) apply(moves []reclaim.Moved) error {
	c.calls++
	c.moves = append(c.moves, moves...)
	return nil
}

// repackRig is a ledger with slabs this device pinned, a network that holds and
// bills them, and a writer and a reclaimer over the two.
func repackRig(t *testing.T, slabs ...string) (*local.Store, *repackNetwork, *repackWriter, *reclaim.Reclaimer) {
	t.Helper()
	ledger, err := local.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open the ledger: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })
	network := &repackNetwork{
		objects:  map[sia.ObjectRef][]byte{},
		slabOf:   map[sia.ObjectRef]sia.SlabID{},
		failures: map[sia.ObjectRef]error{},
		released: map[sia.SlabID]bool{},
	}
	for _, slab := range slabs {
		if err := ledger.TrackSlab(slab, 2, repackSlab); err != nil {
			t.Fatalf("pin %s: %v", slab, err)
		}
		network.pinned += repackSlab
	}
	return ledger, network, &repackWriter{network: network, ledger: ledger}, reclaim.NewOverForTest(network, ledger)
}

// ledgerNames lists the slabs the ledger holds.
func ledgerNames(t *testing.T, r *reclaim.Reclaimer) []sia.SlabID {
	t.Helper()
	tracked, err := r.Tracked()
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	names := make([]sia.SlabID, len(tracked))
	for i, slab := range tracked {
		names[i] = slab.ID
	}
	return names
}

// A record whose object another device deleted is left where it is, and the
// rest of the repack goes on. The other records are written and handed back as
// moved. The one left behind is reported and not handed back, its object is not
// deleted a second time, and the slab it sits on stays pinned and in the ledger,
// though the record beside it moved off.
func TestARepackLeavesARecordWhoseObjectIsGoneWhereItIs(t *testing.T) {
	_, network, writer, r := repackRig(t, "slab-a", "slab-b")
	first := network.holding(t, 1, "slab-a")
	gone := network.holding(t, 2, "slab-a")
	last := network.holding(t, 3, "slab-b")
	delete(network.objects, gone.Ref)

	var applied catalog
	report, err := r.Repack(context.Background(), writer, []reclaim.Held{first, gone, last}, nil, applied.apply)
	if err != nil {
		t.Fatalf("repack: %v", err)
	}

	if !slices.Equal(report.Gone, []reclaim.Held{gone}) {
		t.Errorf("the repack reports %+v left where it was, want the record whose object is gone", report.Gone)
	}
	var moved []record.ID
	for _, move := range applied.moves {
		moved = append(moved, move.ID)
	}
	if applied.calls != 1 || !slices.Equal(moved, []record.ID{first.ID, last.ID}) || len(report.Records) != 2 {
		t.Errorf("the catalog was handed %v in %d call(s), and the report lists %d move(s), want the two "+
			"records whose objects are there, once", moved, applied.calls, len(report.Records))
	}
	if len(writer.batches) != 1 || len(writer.batches[0]) != 2 {
		t.Errorf("the repack wrote %v, want one batch of the two records it read", writer.batches)
	}
	if !slices.Equal(network.deleted, []sia.ObjectRef{first.Ref, last.Ref}) {
		t.Errorf("the repack retired %v, want the two objects it moved and not the one already gone", network.deleted)
	}
	if !slices.Equal(network.unpinned, []sia.SlabID{"slab-b"}) {
		t.Errorf("the repack released %v, want slab-b alone: the record left behind is still on slab-a", network.unpinned)
	}
	if names := ledgerNames(t, r); !slices.Contains(names, "slab-a") {
		t.Errorf("the ledger holds %v, want slab-a still in it", names)
	}
	if report.SlabsBefore != 2 || report.SlabsAfter != 2 || report.Peak != 3 {
		t.Errorf("the repack reports %d slab(s) into %d at a peak of %d, want 2 into 2, slab-a kept, at a peak of 3",
			report.SlabsBefore, report.SlabsAfter, report.Peak)
	}
	if freed := report.Freed(); freed != 0 {
		t.Errorf("the repack reports %d byte(s) freed, want none: one slab was written and one released", freed)
	}
}

// After a repack has left a record behind, a second repack leaves it again and
// refuses nothing, and a sweep neither deletes anything for it nor releases its
// slab, which the catalog still names.
//
// The slab holds no object by then, so a release of slabs that hold nothing
// takes it, and takes it out of the ledger. From then on a repack refuses to
// read from it, until the record is forgotten on this device too.
func TestASecondRepackAndASweepKeepTheSlabOfARecordLeftBehind(t *testing.T) {
	ledger, network, writer, r := repackRig(t, "slab-a", "slab-b")
	first := network.holding(t, 1, "slab-a")
	gone := network.holding(t, 2, "slab-a")
	last := network.holding(t, 3, "slab-b")
	delete(network.objects, gone.Ref)

	var applied catalog
	if _, err := r.Repack(context.Background(), writer, []reclaim.Held{first, gone, last}, nil, applied.apply); err != nil {
		t.Fatalf("the first repack: %v", err)
	}
	// The catalog after the first: the moved records where the repack put them,
	// and the one left behind where it was.
	held := []reclaim.Held{gone}
	for _, move := range applied.moves {
		held = append(held, reclaim.Held{ID: move.ID, CID: move.CID, Ref: move.To, SlabID: move.SlabID})
	}

	var again catalog
	second, err := r.Repack(context.Background(), writer, held, nil, again.apply)
	if err != nil {
		t.Fatalf("the second repack: %v", err)
	}
	if !slices.Equal(second.Gone, []reclaim.Held{gone}) || len(second.Records) != 2 {
		t.Errorf("the second repack moved %d and left %+v, want the two moved again and the same one left",
			len(second.Records), second.Gone)
	}
	if slices.Contains(network.unpinned, "slab-a") {
		t.Errorf("a repack released slab-a, under the record left behind: released %v", network.unpinned)
	}

	keep := reclaim.Live{
		Objects:    map[sia.ObjectRef]struct{}{gone.Ref: {}},
		Slabs:      map[sia.SlabID]struct{}{gone.SlabID: {}},
		Catalogued: 3,
	}
	for _, move := range again.moves {
		keep.Objects[move.To] = struct{}{}
		keep.Slabs[move.SlabID] = struct{}{}
	}
	tracked, err := r.Tracked()
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if err := keep.Check(len(tracked)); err != nil {
		t.Errorf("a sweep refuses the catalog: %v", err)
	}
	plan := reclaim.PlanSweepForTest(keep, tracked, network.walk())
	if len(plan.DeadObjects) != 0 || len(plan.ReleasableSlabs) != 0 {
		t.Errorf("a sweep plans to delete %v and release %v, want nothing", plan.DeadObjects, plan.ReleasableSlabs)
	}

	if err := ledger.ForgetSlab(string(gone.SlabID)); err != nil {
		t.Fatalf("take slab-a out of the ledger: %v", err)
	}
	if _, err := r.Repack(context.Background(), writer, held, nil, (&catalog{}).apply); !errors.Is(err, reclaim.ErrNotOursToRelease) {
		t.Errorf("a repack over a slab the ledger no longer names returned %v, want a refusal", err)
	}
}

// A failure to read that is not an answer that the object is gone stops the
// repack before it writes anything, with the error it has always stopped with.
// A record left behind for a fault the next run would not meet is a slab held
// for nothing.
func TestATransientFailureToReadStillStopsTheRepack(t *testing.T) {
	_, network, writer, r := repackRig(t, "slab-a")
	first := network.holding(t, 1, "slab-a")
	flaky := network.holding(t, 2, "slab-a")
	last := network.holding(t, 3, "slab-a")
	timeout := fmt.Errorf("look up object %s: failed to get locked object: %w", flaky.Ref, context.DeadlineExceeded)
	network.failures[flaky.Ref] = timeout

	var applied catalog
	report, err := r.Repack(context.Background(), writer, []reclaim.Held{first, flaky, last}, nil, applied.apply)
	if want := fmt.Sprintf("read %s for repack: %s", flaky.ID, timeout); err == nil || err.Error() != want {
		t.Errorf("the repack returned %v, want %q", err, want)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("the timeout is no longer in the error")
	}
	stoppedCleanly(t, report, writer, network, applied)
}

// Only an answer that the object is gone leaves a record behind. A not-found
// that is not that answer, and every other failure, stops the repack.
func TestOnlyAnAnswerThatTheObjectIsGoneLeavesARecordBehind(t *testing.T) {
	var other sia.ObjectRef
	other.ID[0] = 0x77
	for _, c := range []struct {
		name   string
		answer func(sia.ObjectRef) error
	}{
		{"a bare 404", func(ref sia.ObjectRef) error {
			return fmt.Errorf("look up object %s: failed to get locked object: 404 page not found", ref)
		}},
		{"not found, naming another object", func(ref sia.ObjectRef) error {
			return fmt.Errorf("look up object %s: failed to get locked object: object %s not found", ref, other)
		}},
		{"a server error", func(ref sia.ObjectRef) error {
			return fmt.Errorf("look up object %s: failed to get locked object: 500 Internal Server Error", ref)
		}},
		{"a refusal", func(ref sia.ObjectRef) error {
			return fmt.Errorf("look up object %s: failed to get locked object: %w", ref, sia.ErrUnauthorized)
		}},
		{"a transport failure", func(ref sia.ObjectRef) error {
			return fmt.Errorf("look up object %s: failed to get locked object: %w", ref,
				&net.OpError{Op: "dial", Err: errors.New("connect: connection refused")})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, network, writer, r := repackRig(t, "slab-a")
			first := network.holding(t, 1, "slab-a")
			failing := network.holding(t, 2, "slab-a")
			answer := c.answer(failing.Ref)
			network.failures[failing.Ref] = answer

			var applied catalog
			report, err := r.Repack(context.Background(), writer, []reclaim.Held{first, failing}, nil, applied.apply)
			if want := fmt.Sprintf("read %s for repack: %s", failing.ID, answer); err == nil || err.Error() != want {
				t.Errorf("the repack returned %v, want %q", err, want)
			}
			stoppedCleanly(t, report, writer, network, applied)
		})
	}
}

// When every record's object is gone, a repack writes nothing, releases
// nothing, hands the catalog nothing and returns no error. It reports every
// record as left where it was, and the slabs under them as kept.
func TestARepackWhoseObjectsAreAllGoneWritesAndReleasesNothing(t *testing.T) {
	_, network, writer, r := repackRig(t, "slab-a", "slab-b")
	held := []reclaim.Held{
		network.holding(t, 1, "slab-a"),
		network.holding(t, 2, "slab-a"),
		network.holding(t, 3, "slab-b"),
	}
	for _, item := range held {
		delete(network.objects, item.Ref)
	}

	var applied catalog
	report, err := r.Repack(context.Background(), writer, held, nil, applied.apply)
	if err != nil {
		t.Fatalf("repack: %v", err)
	}
	if !slices.Equal(report.Gone, held) || len(report.Records) != 0 {
		t.Errorf("the repack moved %d and left %d, want none moved and all %d left",
			len(report.Records), len(report.Gone), len(held))
	}
	if len(writer.batches) != 0 || applied.calls != 0 || len(network.deleted) != 0 || len(network.unpinned) != 0 {
		t.Errorf("the repack wrote %d batch(es), handed the catalog %d, deleted %v and released %v, want nothing",
			len(writer.batches), applied.calls, network.deleted, network.unpinned)
	}
	if report.SlabsBefore != 2 || report.SlabsAfter != 2 || report.Peak != 2 || report.Freed() != 0 {
		t.Errorf("the repack reports %d slab(s) into %d at a peak of %d, freeing %d byte(s), want both slabs "+
			"kept, a peak of 2 and nothing freed", report.SlabsBefore, report.SlabsAfter, report.Peak, report.Freed())
	}
	if names := ledgerNames(t, r); len(names) != 2 {
		t.Errorf("the ledger holds %v, want both slabs", names)
	}
}

// stoppedCleanly checks that a repack that stopped wrote, handed back, retired
// and released nothing, and left nothing behind either.
func stoppedCleanly(t *testing.T, report reclaim.Repack, writer *repackWriter, network *repackNetwork, applied catalog) {
	t.Helper()
	if len(writer.batches) != 0 || applied.calls != 0 || len(network.deleted) != 0 || len(network.unpinned) != 0 {
		t.Errorf("a repack that stopped wrote %d batch(es), handed the catalog %d, retired %v and released %v, "+
			"want nothing", len(writer.batches), applied.calls, network.deleted, network.unpinned)
	}
	if len(report.Gone) != 0 {
		t.Errorf("a repack that stopped reports %+v left behind, want none", report.Gone)
	}
}
