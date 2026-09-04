package reclaim

import (
	"context"
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
	// SlabsBefore and SlabsAfter are how many slabs held these records either
	// side of the operation. Collapsing many partly-filled slabs into a full
	// one is the entire point.
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
func (r *Reclaimer) Repack(ctx context.Context, writer *store.Store, held []Held, apply func([]Moved) error) (Repack, error) {
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
	if report.Before, err = r.client.Account(ctx); err != nil {
		return report, err
	}

	read := time.Now()
	blobs := make([]store.Blob, len(held))
	for i, item := range held {
		payload, err := r.client.Download(ctx, item.Ref)
		if err != nil {
			return report, fmt.Errorf("read %s for repack: %w", item.ID, err)
		}
		blobs[i] = store.Blob{CID: item.CID, Payload: payload}
	}
	report.ReadFor = time.Since(read)

	write := time.Now()
	flushed, err := writer.PutBatch(ctx, blobs)
	if err != nil {
		return report, fmt.Errorf("write %d record(s) for repack: %w", len(blobs), err)
	}
	report.WriteFor = time.Since(write)
	report.SlabsAfter = len(flushed.Slabs)
	// Both sets of slabs are pinned at this moment. It is the peak the account
	// has to be able to afford, and the reason the trigger is a watermark
	// rather than a limit.
	report.Peak = report.SlabsBefore + report.SlabsAfter

	if len(flushed.Written) != len(held) {
		return report, fmt.Errorf("repack wrote %d objects for %d records", len(flushed.Written), len(held))
	}
	report.Records = make([]Moved, len(held))
	for i, written := range flushed.Written {
		report.Records[i] = Moved{
			ID:     held[i].ID,
			CID:    written.CID,
			From:   held[i].Ref,
			To:     written.ObjectRef,
			SlabID: written.SlabID,
			Bytes:  written.Bytes,
		}
	}
	for _, slabID := range flushed.Slabs {
		if err := r.Track(slabID, len(held), int64(flushed.Bytes())); err != nil {
			return report, err
		}
	}
	if err := apply(report.Records); err != nil {
		return report, fmt.Errorf("record the new locations: %w", err)
	}

	retire := time.Now()
	stale := make([]sia.ObjectRef, len(held))
	for i, item := range held {
		stale[i] = item.Ref
	}
	if err := r.client.DeleteObjects(ctx, stale); err != nil {
		return report, fmt.Errorf("retire %d old object(s): %w", len(stale), err)
	}
	written := make(map[sia.SlabID]struct{}, len(flushed.Slabs))
	for _, slabID := range flushed.Slabs {
		written[slabID] = struct{}{}
	}
	for slabID := range from {
		// A record can land back in a slab it was already in when nothing else
		// moved; releasing that one would throw away the work.
		if _, reused := written[slabID]; reused {
			continue
		}
		if err := r.Unpin(ctx, slabID); err != nil {
			return report, err
		}
	}
	report.RetireFor = time.Since(retire)

	if report.After, err = r.client.Account(ctx); err != nil {
		return report, err
	}
	report.Elapsed = time.Since(start)
	return report, nil
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
