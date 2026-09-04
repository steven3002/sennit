package vault

import (
	"context"
	"fmt"

	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store/reclaim"
)

// Reclaim releases storage nothing in the catalog points at any more.
//
// It is not housekeeping. A slab is billed whole and can never be extended, so
// every flush strands one: without this, an account fills over months while
// holding only tens of megabytes of real data, and the leak is invisible
// because releasing storage is the one operation the released SDK reports
// success for without doing.
func (v *Vault) Reclaim(ctx context.Context, opts ReclaimOptions) (reclaim.Sweep, error) {
	if v.client == nil {
		return reclaim.Sweep{}, errOffline
	}
	if pending := v.packer.Pending(); pending > 0 {
		return reclaim.Sweep{}, fmt.Errorf(
			"%d record(s) are queued and not yet on the network: flush before reclaiming", pending)
	}
	live, err := v.live(opts.ReleaseAll)
	if err != nil {
		return reclaim.Sweep{}, err
	}
	return v.reclaimer.Sweep(ctx, live)
}

// ReclaimOptions parameterises a reclamation.
type ReclaimOptions struct {
	// ReleaseAll authorises releasing the storage of a vault whose catalog is
	// empty. Without it an empty catalog stops the sweep, because a vault that
	// has been emptied and a catalog that could not be read are the same
	// observation from here. See reclaim.Live.
	ReleaseAll bool
	// TakeOwnership promotes storage this device hydrated to storage it may
	// release. It is for a vault whose writing installation is gone, and it is
	// wrong for one that is merely switched off. See TakeOwnership.
	TakeOwnership bool
}

// TakeOwnership makes the storage this device hydrated its own to release.
//
// A hydrated vault deliberately cannot release the slabs it reads, because
// another installation pinned them and is still pointing at them. When that
// installation no longer exists, nothing else can release them either and the
// quota is held indefinitely. This is the deliberate act that resolves it, and
// it reports what it took over rather than doing so quietly.
func (v *Vault) TakeOwnership() (int, error) {
	if v.local == nil {
		return 0, errOffline
	}
	return v.local.TakeSlabOwnership()
}

// live names everything the catalog still points at.
//
// An entry whose object ref does not parse fails the whole computation rather
// than being skipped. A skipped entry is a record the keep-set does not name,
// and a record the keep-set does not name is one the sweep deletes: the quiet
// path from a malformed catalog line to deleted data ran through here.
func (v *Vault) live(releaseAll bool) (reclaim.Live, error) {
	entries := v.manifest.Entries()
	set := reclaim.Live{
		Objects:    make(map[sia.ObjectRef]struct{}, len(entries)),
		Slabs:      make(map[sia.SlabID]struct{}, len(entries)),
		Catalogued: len(entries),
		ReleaseAll: releaseAll,
	}
	for _, entry := range entries {
		ref, err := sia.ParseObjectRef(entry.ObjectRef)
		if err != nil {
			return reclaim.Live{}, fmt.Errorf(
				"record %s is catalogued at an object ref that cannot be read, so a sweep cannot tell "+
					"what to keep: %w", entry.ID, err)
		}
		set.Objects[ref] = struct{}{}
		if entry.SlabID != "" {
			set.Slabs[sia.SlabID(entry.SlabID)] = struct{}{}
		}
	}
	return set, nil
}

// Repack rewrites every live record into as few slabs as they fit in and
// releases the ones they came from.
//
// It runs when it is asked to and never on its own. The operation holds the old
// slabs and the new ones at the same time, and what it does under concurrent
// writes, what it leaves behind if it is interrupted, and what an account does
// when it runs out of room have not been measured. Scheduling it on those terms
// would risk wedging an account at the exact moment it needed the space.
func (v *Vault) Repack(ctx context.Context) (reclaim.Repack, error) {
	if v.client == nil {
		return reclaim.Repack{}, errOffline
	}
	if pending := v.packer.Pending(); pending > 0 {
		return reclaim.Repack{}, fmt.Errorf(
			"%d record(s) are queued and not yet on the network: flush before repacking", pending)
	}

	entries := v.manifest.Entries()
	held := make([]reclaim.Held, 0, len(entries))
	for _, entry := range entries {
		ref, err := sia.ParseObjectRef(entry.ObjectRef)
		if err != nil {
			return reclaim.Repack{}, fmt.Errorf("record %s: %w", entry.ID, err)
		}
		held = append(held, reclaim.Held{
			ID:     entry.ID,
			CID:    entry.CID.String(),
			Ref:    ref,
			SlabID: sia.SlabID(entry.SlabID),
		})
	}

	return v.reclaimer.Repack(ctx, v.store, held, v.relocate)
}

// relocate points the catalog at where repack put each record.
//
// The record ids are unchanged and every object ref is new, so this is an
// update of existing entries rather than the arrival of new records. A catalog
// keyed on storage location instead of on record identity would see the whole
// vault double here.
func (v *Vault) relocate(moved []reclaim.Moved) error {
	for _, move := range moved {
		entry, err := v.manifest.Lookup(move.ID)
		if err != nil {
			return err
		}
		entry.ObjectRef = move.To.String()
		entry.SlabID = string(move.SlabID)
		entry.Bytes = move.Bytes
		entry.WrittenAt = record.Now()
		if err := v.manifest.Append(entry); err != nil {
			return err
		}
		// The cached location describes an object that is about to be deleted.
		if err := v.local.ForgetLocation(move.From.String()); err != nil {
			return err
		}
	}
	return nil
}

// Watermark reports where the account stands against the point at which
// repacking becomes worthwhile, and whether there is still room to do it.
//
// It answers the question; it does not act on it. See Repack.
func (v *Vault) Watermark(ctx context.Context) (reclaim.WatermarkCheck, error) {
	if v.client == nil {
		return reclaim.WatermarkCheck{}, errOffline
	}
	account, err := v.reclaimer.Account(ctx)
	if err != nil {
		return reclaim.WatermarkCheck{}, err
	}
	slabBytes, err := v.store.SlabPayloadSize()
	if err != nil {
		return reclaim.WatermarkCheck{}, err
	}
	tracked, err := v.reclaimer.Tracked()
	if err != nil {
		return reclaim.WatermarkCheck{}, err
	}
	return reclaim.Watermark(account, len(tracked), uint64(slabBytes)), nil
}

// TrackedSlabs lists every slab this vault has pinned.
func (v *Vault) TrackedSlabs() ([]reclaim.Slab, error) {
	if v.reclaimer == nil {
		return nil, errOffline
	}
	return v.reclaimer.Tracked()
}

// Unledgered lists storage the account is billed for that this device has no
// record of pinning.
//
// It is reported rather than reclaimed. Some of it belongs to another
// installation of this vault and is not this device's to release; the rest is
// this device's own interrupted writes, which a ledger-bounded sweep cannot
// reach because the interruption is what stopped the ledger being written.
func (v *Vault) Unledgered(ctx context.Context) ([]reclaim.Unledgered, error) {
	if v.client == nil {
		return nil, errOffline
	}
	return v.reclaimer.Unledgered(ctx)
}

// Orphans lists slabs the account is billed for that hold nothing at all.
func (v *Vault) Orphans(ctx context.Context, opts ReclaimOptions) ([]reclaim.Orphan, error) {
	if v.client == nil {
		return nil, errOffline
	}
	return v.reclaimer.Orphans(ctx, opts.ReleaseAll)
}

// ReleaseOrphans returns the quota held by slabs that hold nothing.
//
// It is separate from Reclaim because it answers a different question. Reclaim
// releases what this vault pinned and no longer needs; this releases what the
// account is being charged for and nothing can read, including storage stranded
// by an installation that no longer exists. The device's ledger cannot see
// that, so the indexer is asked instead.
func (v *Vault) ReleaseOrphans(ctx context.Context, opts ReclaimOptions) (reclaim.Sweep, error) {
	if v.client == nil {
		return reclaim.Sweep{}, errOffline
	}
	return v.reclaimer.ReleaseOrphans(ctx, opts.ReleaseAll)
}

// DropUnreadable deletes objects the indexer holds but will not open.
func (v *Vault) DropUnreadable(ctx context.Context) ([]sia.ObjectRef, error) {
	if v.client == nil {
		return nil, errOffline
	}
	return v.reclaimer.DropUnreadable(ctx)
}

// Forget drops a record from this vault.
//
// The storage does not come back here. A slab is shared between everything in
// one flush and is billed whole, so forgetting a record frees nothing on its
// own; the space returns when a later reclaim finds a slab with nothing live
// left in it. Saying so plainly is better than a delete that appears to work
// and returns no quota.
func (v *Vault) Forget(id record.ID) error {
	if err := v.manifest.Remove(id); err != nil {
		return err
	}
	if err := v.local.ForgetBody(id); err != nil {
		return err
	}
	if err := v.vectors.Remove(id); err != nil {
		return err
	}
	return v.local.ForgetRankingMeta(id)
}

// Entries lists the catalog, so a caller can see what the vault believes it
// holds and where.
func (v *Vault) Entries() []manifest.Entry { return v.manifest.Entries() }

// SlabPayloadSize reports how many payload bytes one slab holds, which is what
// every flush is billed for whether it fills them or not.
func (v *Vault) SlabPayloadSize() (int64, error) {
	if v.store == nil {
		return 0, errOffline
	}
	return v.store.SlabPayloadSize()
}

// ManifestStats reports what the catalog has cost on disk.
func (v *Vault) ManifestStats() manifest.Stats { return v.manifest.Stats() }
