package reclaim

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store"
)

// A Held record is one live record at its current location, which repack reads
// and writes back somewhere else.
type Held struct {
	ID     record.ID
	CID    string
	Ref    sia.ObjectRef
	SlabID sia.SlabID
}

// A Moved record is one that repack has rewritten.
//
// The record id is the same one it went in with and the object ref is not.
// That is the whole shape of the operation: storage locations are a pointer
// that repack rewrites wholesale, and anything treating one as an identity
// would see every record in the vault turn into a new record.
type Moved struct {
	ID     record.ID
	CID    string
	From   sia.ObjectRef
	To     sia.ObjectRef
	SlabID sia.SlabID
	Bytes  int
}

// A Repack reports what one repack moved and freed.
type Repack struct {
	Records []Moved
	// Gone lists the records repack left where they were, because the indexer
	// no longer holds their object. Each stays catalogued as it was, on the slab
	// it was on, and that slab is not released. See Repack.
	Gone []Held
	// SlabsBefore and SlabsAfter are how many slabs held these records either
	// side of the operation, counting the slabs kept for the records in Gone.
	// Collapsing many partly-filled slabs into a full one is the entire point.
	SlabsBefore, SlabsAfter int
	// Peak is the largest number of slabs alive at once during the operation.
	// It is one more than the account started with, which is why an account
	// with no headroom cannot run this.
	Peak int

	Before, After sia.Account
	ReadFor       time.Duration
	WriteFor      time.Duration
	RetireFor     time.Duration
	Elapsed       time.Duration
}

// Freed reports the quota the repack returned.
func (r Repack) Freed() uint64 {
	if r.After.PinnedData >= r.Before.PinnedData {
		return 0
	}
	return r.Before.PinnedData - r.After.PinnedData
}

// Repack rewrites live records into as few slabs as they will fit in, then
// releases the ones they came from.
//
// It is a precondition of the flush cadence rather than housekeeping. A
// partially-filled slab can never be extended, so every flush strands one and
// an account fills up over months regardless of how little the user actually
// stores. This is what gives that space back.
//
// The bytes written are the bytes read, verbatim. Re-sealing would produce a
// perfectly good record too, but reading back exactly what was stored and
// writing exactly that is what makes the result checkable against the original
// rather than merely plausible.
//
// The caller applies the returned moves to its catalog. Nothing is released
// until that has happened, because between the write and the catalog update the
// records exist in two places and only one of them is findable.
//
// A record whose object the indexer no longer holds is left where it is and
// listed in Gone. That is what a forget on another device leaves on this one,
// when this device wrote the record: the forget deleted the object, and forgets
// are not shared, so this device's catalog still names it. Writing it again
// would undo the forget on the network, and dropping it from the catalog would
// forget it here unasked. So it is not written, applied or retired, and the
// slab it sits on is not released. Unpin takes a slab out of the ledger, and a
// slab the catalog names and the ledger does not is one every later repack
// refuses to read from.
//
// Only an answer that the object is not there counts, as sia.ErrObjectGone.
// Every other failure to read stops the repack with nothing written, as it
// always did: a record left behind for a fault the next run would not meet is
// a record whose slab is held for nothing.
//
// owed names the slabs that still hold the copy of a record this vault forgot,
// whose deletion is owed. Those are kept too. A slab released with an object
// still on it leaves that object unreadable, and out of the sweep's reach,
// since the slab has left the ledger. Kept, the slab is released by the sweep
// that follows, after it has deleted the copy.
func (r *Reclaimer) Repack(ctx context.Context, writer batchWriter, held []Held, owed []sia.SlabID, apply func([]Moved) error) (Repack, error) {
	start := time.Now()
	report := Repack{}
	if len(held) == 0 {
		return report, nil
	}

	from := make(map[sia.SlabID]struct{})
	for _, item := range held {
		if item.SlabID != "" {
			from[item.SlabID] = struct{}{}
		}
	}
	report.SlabsBefore = len(from)

	// Repack ends by unpinning everything it read from, so a vault that would
	// be repacking another device's storage is stopped before it writes rather
	// than after. Refusing outright is the honest answer: the alternative,
	// rewriting the records and keeping both copies, would pin new slabs and
	// release none, which is the opposite of what was asked for.
	if err := r.ownsAll(from); err != nil {
		return report, err
	}

	var err error
	if report.Before, err = r.net.Account(ctx); err != nil {
		return report, err
	}

	read := time.Now()
	r.report(Event{Stage: StageRepackRead, Total: int64(len(held))})
	moving := make([]Held, 0, len(held))
	blobs := make([]store.Blob, 0, len(held))
	for _, item := range held {
		payload, err := r.net.Download(ctx, item.Ref)
		if errors.Is(err, sia.ErrObjectGone) {
			report.Gone = append(report.Gone, item)
			continue
		}
		if err != nil {
			return report, fmt.Errorf("read %s for repack: %w", item.ID, err)
		}
		moving = append(moving, item)
		blobs = append(blobs, store.Blob{CID: item.CID, Payload: payload})
	}
	report.ReadFor = time.Since(read)

	// Every slab a record left behind sits on stays, whatever else moves off it.
	kept := make(map[sia.SlabID]struct{}, len(report.Gone))
	for _, item := range report.Gone {
		if item.SlabID != "" {
			kept[item.SlabID] = struct{}{}
		}
	}
	if len(moving) == 0 {
		// No record's object was there to read, so nothing is written or
		// released, and the account is as it was.
		report.SlabsAfter = len(kept)
		report.Peak = report.SlabsBefore
		report.After = report.Before
		report.Elapsed = time.Since(start)
		return report, nil
	}

	// The write records its new slabs in this ledger itself, before it pins
	// them, so they are this device's to release from the moment they can be
	// billed. See store.SlabLedger: a repack interrupted while pinning would
	// otherwise leave a new slab billed with nothing pointing at it and nothing
	// able to find it, on top of the old slabs it had not yet released.
	write := time.Now()
	flushed, err := writer.PutBatch(ctx, blobs)
	if err != nil {
		return report, fmt.Errorf("write %d record(s) for repack: %w", len(blobs), err)
	}
	report.WriteFor = time.Since(write)
	after := make(map[sia.SlabID]struct{}, len(flushed.Slabs)+len(kept))
	for _, slabID := range flushed.Slabs {
		after[slabID] = struct{}{}
	}
	for slabID := range kept {
		after[slabID] = struct{}{}
	}
	report.SlabsAfter = len(after)
	// Both sets of slabs are pinned at this moment. It is the peak the account
	// has to be able to afford, and the reason the trigger is a watermark
	// rather than a limit.
	report.Peak = report.SlabsBefore + len(flushed.Slabs)

	if len(flushed.Written) != len(moving) {
		return report, fmt.Errorf("repack wrote %d objects for %d records", len(flushed.Written), len(moving))
	}
	report.Records = make([]Moved, len(moving))
	for i, written := range flushed.Written {
		report.Records[i] = Moved{
			ID:     moving[i].ID,
			CID:    written.CID,
			From:   moving[i].Ref,
			To:     written.ObjectRef,
			SlabID: written.SlabID,
			Bytes:  written.Bytes,
		}
	}
	if err := apply(report.Records); err != nil {
		return report, fmt.Errorf("record the new locations: %w", err)
	}

	retire := time.Now()
	r.report(Event{Stage: StageRepackRetire})
	stale := make([]sia.ObjectRef, len(moving))
	for i, item := range moving {
		stale[i] = item.Ref
	}
	if err := r.net.DeleteObjects(ctx, stale); err != nil {
		return report, fmt.Errorf("retire %d old object(s): %w", len(stale), err)
	}
	written := make(map[sia.SlabID]struct{}, len(flushed.Slabs))
	for _, slabID := range flushed.Slabs {
		written[slabID] = struct{}{}
	}
	owing := make(map[sia.SlabID]struct{}, len(owed))
	for _, slabID := range owed {
		owing[slabID] = struct{}{}
	}
	for slabID := range from {
		// A record can land back in a slab it was already in when nothing else
		// moved; releasing that one would throw away the work.
		if _, reused := written[slabID]; reused {
			continue
		}
		if _, still := kept[slabID]; still {
			continue
		}
		if _, copyOn := owing[slabID]; copyOn {
			continue
		}
		if err := r.Unpin(ctx, slabID); err != nil {
			return report, err
		}
	}
	report.RetireFor = time.Since(retire)

	if report.After, err = r.net.Account(ctx); err != nil {
		return report, err
	}
	report.Elapsed = time.Since(start)
	return report, nil
}

// A batchWriter writes a batch of blobs and makes them retrievable, recording
// the slabs it pins in this device's ledger before it pins them. *store.Store
// is the one a repack is given.
type batchWriter interface {
	PutBatch(ctx context.Context, blobs []store.Blob) (store.Flush, error)
}

// ownsAll reports whether every slab named was pinned by this installation.
//
// A hydrated vault holds records that live on another device's slabs. Reading
// them is what hydration is for; releasing them is not, and repack's last act
// is to release everything it read from.
func (r *Reclaimer) ownsAll(slabs map[sia.SlabID]struct{}) error {
	if len(slabs) == 0 {
		return nil
	}
	tracked, err := r.Tracked()
	if err != nil {
		return err
	}
	ours := make(map[sia.SlabID]bool, len(tracked))
	for _, slab := range tracked {
		ours[slab.ID] = slab.Releasable()
	}
	var foreign int
	for id := range slabs {
		if !ours[id] {
			foreign++
		}
	}
	if foreign == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: %d of %d slab(s) holding these records were pinned by another device and hydrated here. "+
			"Repack releases the slabs it reads from, so run it on the device that wrote them",
		ErrNotOursToRelease, foreign, len(slabs))
}

// DefaultWatermark is the share of quota above which repack should run.
//
// It is deliberately not close to full. The operation holds the old slabs and
// the new ones at the same time, so it needs room for both, and an account that
// has waited until it is full can no longer afford the one operation that would
// give it room back.
const DefaultWatermark = 0.75

// A WatermarkCheck reports whether an account is at the point where repacking
// is worth doing, and whether it can still afford to.
//
// Nothing calls this on a schedule. Repack has been measured exactly once, on
// an idle account: repacking under concurrent writes, and repacking interrupted
// part way, are not measured at all, and neither is what an account does when
// it runs out of room. Until those are known the trigger stays a question this
// answers rather than an action it takes.
type WatermarkCheck struct {
	Used float64
	// Due reports that the account has passed the watermark.
	Due bool
	// Affordable reports that there is room for the transient peak. An account
	// past this point needs a decision, not a scheduler.
	Affordable bool
	// Headroom is the quota that must stay free to write the new slabs before
	// the old ones are released.
	Headroom uint64
}

// Watermark reports where an account stands against the repack threshold.
func Watermark(account sia.Account, slabs int, slabBytes uint64) WatermarkCheck {
	check := WatermarkCheck{Headroom: uint64(slabs) * slabBytes}
	if account.MaxPinnedData == 0 {
		return check
	}
	check.Used = float64(account.PinnedData) / float64(account.MaxPinnedData)
	check.Due = check.Used >= DefaultWatermark
	check.Affordable = account.Free() >= check.Headroom
	return check
}
