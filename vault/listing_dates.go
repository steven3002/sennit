package vault

import (
	"errors"
	"fmt"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
)

// listingDatesKey is the setting that records that this device's listing was
// dated by the records it lists. It is stored on every device, so it is never
// renamed: a new name would make every device date its listing again.
const listingDatesKey = "listingDatedByRecords"

// dateListingByRecords dates every row of this device's listing by the time its
// record carries, the first time the vault is opened here by a build that lists
// records that way, and does nothing at every later open but read that it did.
//
// A build before that dated a row by when this device first wrote it, and a row
// keeps the first time written for it. On the device that stored a record that
// was a moment after the record was made, by however long the write spent
// embedding it. On a device that restored the vault it was when the restore
// reached the record, so everything restored then was listed in the order the
// restore walked the account's objects, every date the restore's, and anything
// written or restored since would be listed among those by its own time. The
// same pass takes the restore's time off the conversation heads an earlier
// release dated by it; listingDater.Undated says which.
//
// A rewrite that fails does not stop the vault opening. The listing stays as it
// was, nothing records the rewrite as made, and the next open tries the whole
// of it again. Why it failed is kept, for whoever opened the vault to say.
func (v *Vault) dateListingByRecords() {
	if _, err := v.local.RewriteListingDates(listingDatesKey, &listingDater{v: v}); err != nil {
		v.listingNotDated = err
	}
}

// ListingNotDatedBecause reports why this open could not date the listing by
// the records it lists. It is nil when the open did, and when an earlier open
// already had.
//
// The vault is open either way. A listing this open could not date is listed as
// it was, and the next open tries again.
func (v *Vault) ListingNotDatedBecause() error { return v.listingNotDated }

// timeCarried is the time a record carries, read from this device's copy of it:
// a memory's body or a conversation's head.
//
// It is the time rankingMeta and sessionRankingMeta give a record whose metadata
// is written now, so a row dated here and a row written since agree: a memory's
// own creation time, and a conversation's head Created, which is the zero time
// for a head rebuilt from turns that record none.
func timeCarried(kind record.Kind, held []byte) (record.Time, error) {
	switch kind {
	case record.KindMemory:
		memory, err := record.Unmarshal(held)
		if err != nil {
			return record.Time{}, err
		}
		return rankingMeta(memory).Created, nil
	case record.KindSession:
		session, err := record.UnmarshalSession(held)
		if err != nil {
			return record.Time{}, err
		}
		return sessionRankingMeta(session).Created, nil
	default:
		return record.Time{}, fmt.Errorf("a %s is not listed by a time it carries", kind)
	}
}

// A listingDater is what dating the listing asks of this vault: the time each
// record carries, and which conversation heads lose the dates they carry.
type listingDater struct {
	v *Vault
	// slabs is this device's slab ledger, read the first time a head needs it.
	slabs map[string]local.SlabOrigin
}

func (d *listingDater) TimeOf(kind record.Kind, held []byte) (record.Time, error) {
	return timeCarried(kind, held)
}

// Undated picks out a conversation head that a release before rebuilt heads
// stopped carrying the rebuild's time, v0.1.0-beta-mvp among them, rebuilt from
// turns that record no time, and gives it back without dates.
//
// Such a release gave the head the moment of the rebuild as both its Created and
// its Updated, so a resume took the conversation for the most recent and a
// listing put it first, dated by the restore. Nothing on a head says whether it
// was rebuilt or saved, so such a head is told by three things that hold
// together of it and of no conversation saved on this device:
//
//   - its Created and its Updated are one time, and not the zero time;
//   - every chunk of its transcript is catalogued in a slab this device adopted
//     from the network, and none is queued here or in a slab this device
//     pinned. A conversation saved here never passes: its chunks are queued
//     until a flush writes them, and in a slab this device pinned after it;
//   - every chunk is on this device, and none of its turns records a time.
//
// A head that fails any one is left as it is. The head given back is the one a
// rebuild stores now for such a transcript: no Created and no Updated, one
// revision on from the head it replaces.
func (d *listingDater) Undated(held []byte) (*record.Session, []byte, error) {
	session, err := record.UnmarshalSession(held)
	if err != nil {
		// A head that will not read is left alone. The rewrite counts its row
		// as one whose copy will not read.
		return nil, nil, nil
	}
	if session.Created.IsZero() || !session.Created.Equal(session.Updated.Time) || len(session.Chunks) == 0 {
		return nil, nil, nil
	}
	if adopted, err := d.adoptedFromTheNetwork(session.Chunks); err != nil || !adopted {
		return nil, nil, err
	}
	if untimed, err := d.recordNoTime(session.Chunks); err != nil || !untimed {
		return nil, nil, err
	}
	session.Created, session.Updated = record.Time{}, record.Time{}
	session.Version++
	head, err := record.MarshalSession(session)
	if err != nil {
		return nil, nil, err
	}
	return session, head, nil
}

// adoptedFromTheNetwork reports whether every chunk of a transcript is
// catalogued in a slab this device adopted, and none is queued here.
func (d *listingDater) adoptedFromTheNetwork(chunks []record.ChunkRef) (bool, error) {
	if d.slabs == nil {
		tracked, err := d.v.local.TrackedSlabs()
		if err != nil {
			return false, err
		}
		d.slabs = make(map[string]local.SlabOrigin, len(tracked))
		for _, slab := range tracked {
			d.slabs[slab.SlabID] = slab.Origin
		}
	}
	for _, ref := range chunks {
		entry, err := d.v.manifest.Lookup(ref.ID)
		if errors.Is(err, manifest.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if d.slabs[entry.SlabID] != local.SlabHydrated {
			return false, nil
		}
		if queued, err := d.v.local.Queued(ref.ID); err != nil || queued {
			return false, err
		}
	}
	return true, nil
}

// recordNoTime reports whether every chunk of a transcript is on this device
// and none of its turns records a time. A chunk this device does not hold, or
// cannot read, might hold a turn that does, so it counts as one that does.
func (d *listingDater) recordNoTime(chunks []record.ChunkRef) (bool, error) {
	for _, ref := range chunks {
		kind, body, err := d.v.local.GetRecord(ref.ID)
		if errors.Is(err, local.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if kind != record.KindChunk {
			return false, nil
		}
		chunk, err := record.UnmarshalChunk(body)
		if err != nil {
			return false, nil
		}
		for i := range chunk.Messages {
			if !chunk.Messages[i].Created.IsZero() {
				return false, nil
			}
		}
	}
	return true, nil
}
