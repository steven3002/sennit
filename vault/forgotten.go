package vault

import (
	"context"
	"errors"
	"fmt"

	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
)

// errForgotten reports that a restore found a record this device has forgotten.
// The restore leaves it out and counts it, rather than failing.
var errForgotten = errors.New("this device has forgotten that record")

// ForgottenOnNetwork reports how many records this vault has forgotten whose
// copy on the network this device has not deleted yet.
//
// Until it is deleted, a restore on another device finds that copy and brings
// the record back there. A restore on this device leaves it out.
func (v *Vault) ForgottenOnNetwork() int { return len(v.manifest.RemovedOnNetwork()) }

// DeleteForgotten deletes from the network the copies of the records this vault
// has forgotten, and reports how many records it deleted the copies of.
//
// Forgetting a record takes it off this device and marks it removed in the
// catalog, and that is all it does, since it needs no connection. The copy on
// the network is what a restore on any device finds, so it is deleted here, by a
// later run that reaches the network, the way reclaim deletes an object nothing
// points at. The storage does not come back with it: a slab is shared and billed
// whole, and only reclaim releases one.
//
// The copy is deleted wherever it is, in storage another device wrote and this
// one restored from as much as in its own. A sweep leaves such storage to the
// device that wrote it, and here only the objects of records this vault has
// forgotten are deleted from it. No slab is released, and the device that wrote
// the record keeps its own copy of it, because a forget is not shared between
// devices.
//
// An object a record this vault still holds is catalogued at is never deleted,
// whatever a removal says of it, because deleting it would take that record
// with it. A deletion that fails leaves its batch, and every batch after it, to
// be tried again, and deleting an object that is already gone counts as done.
func (v *Vault) DeleteForgotten(ctx context.Context) (int, error) {
	if v.client == nil {
		return 0, errOffline
	}
	return v.deleteForgotten(ctx, v.client.DeleteObjects)
}

// deleteBatch is how many forgotten records' copies one call to the network
// deletes.
//
// A call that fails part way does not say which of its objects went, so the
// whole call is tried again later. Batches keep what a failure costs small: a
// catalog written before forgotten copies were deleted at all may name hundreds
// of them, and the first run to reach the network deletes every one.
const deleteBatch = 64

// deleteForgotten is DeleteForgotten through del, the network's deletion of a
// set of objects.
func (v *Vault) deleteForgotten(ctx context.Context, del func(context.Context, []sia.ObjectRef) error) (int, error) {
	removed := v.manifest.RemovedOnNetwork()
	if len(removed) == 0 {
		return 0, nil
	}
	held := make(map[string]record.ID)
	for _, entry := range v.manifest.Entries() {
		held[entry.ObjectRef] = entry.ID
	}

	var (
		targets  []manifest.Entry
		refs     []sia.ObjectRef
		problems []error
	)
	for _, entry := range removed {
		if holder, ok := held[entry.ObjectRef]; ok {
			problems = append(problems, fmt.Errorf("forgotten record %s shares its object with record %s, "+
				"which this vault still holds, so the object stays on the network", entry.ID, holder))
			continue
		}
		ref, err := sia.ParseObjectRef(entry.ObjectRef)
		if err != nil {
			problems = append(problems, fmt.Errorf("forgotten record %s: %w", entry.ID, err))
			continue
		}
		targets = append(targets, entry)
		refs = append(refs, ref)
	}

	var deleted int
	for start := 0; start < len(targets); start += deleteBatch {
		end := min(start+deleteBatch, len(targets))
		if err := del(ctx, refs[start:end]); err != nil {
			problems = append(problems, fmt.Errorf("delete the copies of %d forgotten record(s) from the network: %w",
				len(targets)-start, err))
			break
		}
		for _, entry := range targets[start:end] {
			if err := v.manifest.ObjectDeleted(entry.ID, entry.ObjectRef); err != nil {
				return deleted, errors.Join(append(problems, err)...)
			}
			deleted++
			// The cached location describes an object that is gone.
			if err := v.local.ForgetLocation(entry.ObjectRef); err != nil {
				return deleted, errors.Join(append(problems, err)...)
			}
		}
	}
	return deleted, errors.Join(problems...)
}

// slabsOwedADeletion lists the slabs that still hold the copy of a record this
// vault forgot, whose deletion is owed: the slabs its removals still name.
//
// A repack keeps them, so that the sweep after it deletes the copy before it
// releases the slab. See reclaim.Reclaimer.Repack.
func (v *Vault) slabsOwedADeletion() []sia.SlabID {
	var out []sia.SlabID
	for _, entry := range v.manifest.RemovedOnNetwork() {
		if entry.SlabID != "" {
			out = append(out, sia.SlabID(entry.SlabID))
		}
	}
	return out
}

// slabsNamed lists the slabs this vault's catalog names for records it still
// holds. A release of slabs that hold nothing keeps these: one of them is what a
// repack leaves under a record whose copy another device deleted. See
// reclaim.Reclaimer.ReleaseOrphans.
func (v *Vault) slabsNamed() map[sia.SlabID]struct{} {
	named := make(map[sia.SlabID]struct{})
	for _, entry := range v.manifest.Entries() {
		if entry.SlabID != "" {
			named[sia.SlabID(entry.SlabID)] = struct{}{}
		}
	}
	return named
}

// deleteForgottenInBackground is what the background flush does about forgotten
// records on each of its ticks: it deletes their copies when there are any, and
// when there are none it does nothing at all, not even ask the network.
func (v *Vault) deleteForgottenInBackground(ctx context.Context) error {
	if v.client == nil || v.ForgottenOnNetwork() == 0 {
		return nil
	}
	_, err := v.DeleteForgotten(ctx)
	return err
}

// A Removal says how far forgetting some records has reached beyond this
// device.
type Removal struct {
	// OnNetwork counts the records forgotten here whose copy is still on the
	// network, where a restore on another device can find it, until this device
	// deletes it.
	OnNetwork int
	// Deleted counts the records whose copy this device has deleted from the
	// network.
	Deleted int
}

// Removal reports how far forgetting the records named has reached.
//
// A record the catalog does not mark removed counts in neither. Once a forget
// has succeeded, that is a record no flush ever wrote: forgetting it took it out
// of the upload queue, so nothing of it reached the network.
func (v *Vault) Removal(ids ...record.ID) Removal {
	var removal Removal
	for _, id := range ids {
		entry, removed := v.manifest.Removed(id)
		switch {
		case !removed:
		case entry.ObjectRef != "":
			removal.OnNetwork++
		default:
			removal.Deleted++
		}
	}
	return removal
}
