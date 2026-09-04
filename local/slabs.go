package local

import (
	"fmt"
	"time"

	"github.com/steven3002/sennit/record"
)

// A SlabOrigin says how a slab came to be in this device's ledger, which is
// what decides whether the device may release it.
type SlabOrigin string

const (
	// SlabPinned marks a slab this installation pinned itself, by flushing or
	// repacking into it. It is this device's to release.
	SlabPinned SlabOrigin = "pinned"
	// SlabHydrated marks a slab this installation found by hydrating: it holds
	// records this vault's phrase opens, but another device pinned it and is
	// still pointing at it. It is billed to the shared account and it is not
	// this device's to release.
	SlabHydrated SlabOrigin = "hydrated"
)

// Releasable reports whether a slab of this origin may be unpinned here.
func (o SlabOrigin) Releasable() bool { return o != SlabHydrated }

// A TrackedSlab is a slab this vault's ledger knows about.
type TrackedSlab struct {
	SlabID   string
	PinnedAt time.Time
	Records  int
	Bytes    int64
	Origin   SlabOrigin
}

// TrackSlab records that a flush pinned a slab.
//
// Every flush strands a slab that can never be extended, so this list is the
// difference between storage that can be reclaimed later and storage that leaks
// a full slab's worth of quota per flush with nothing to show which ones.
func (s *Store) TrackSlab(slabID string, records int, bytes int64) error {
	return s.trackSlab(slabID, records, bytes, SlabPinned)
}

// AdoptSlab records a slab this installation found rather than pinned.
//
// Hydration reaches every slab holding a record this phrase can open, which is
// correct, the records are this vault's, but the slab is not. Filing it under
// the same origin as a flush would hand a second device the authority to unpin
// storage the first one is still using, so the two are kept apart here rather
// than in the callers.
//
// A slab this device really did pin is never demoted by a later hydration: the
// pin is the stronger claim and it is the one that has to survive.
func (s *Store) AdoptSlab(slabID string, records int, bytes int64) error {
	return s.trackSlab(slabID, records, bytes, SlabHydrated)
}

func (s *Store) trackSlab(slabID string, records int, bytes int64, origin SlabOrigin) error {
	_, err := s.db.Exec(
		`INSERT INTO slabs (slab_id, pinned_at, records, bytes, origin) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(slab_id) DO UPDATE SET
		     records = slabs.records + excluded.records,
		     bytes   = slabs.bytes + excluded.bytes,
		     origin  = CASE WHEN slabs.origin = ? OR excluded.origin = ? THEN ? ELSE ? END`,
		slabID, record.Now().String(), records, bytes, string(origin),
		string(SlabPinned), string(SlabPinned), string(SlabPinned), string(SlabHydrated))
	if err != nil {
		return fmt.Errorf("track slab %s: %w", slabID, err)
	}
	return nil
}

// TrackedSlabs lists every slab in this vault's ledger, oldest first.
func (s *Store) TrackedSlabs() ([]TrackedSlab, error) {
	rows, err := s.db.Query(`SELECT slab_id, pinned_at, records, bytes, origin FROM slabs ORDER BY pinned_at`)
	if err != nil {
		return nil, fmt.Errorf("read tracked slabs: %w", err)
	}
	defer rows.Close()

	var out []TrackedSlab
	for rows.Next() {
		var slab TrackedSlab
		var pinnedAt, origin string
		if err := rows.Scan(&slab.SlabID, &pinnedAt, &slab.Records, &slab.Bytes, &origin); err != nil {
			return nil, fmt.Errorf("scan tracked slab: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, pinnedAt)
		if err != nil {
			return nil, fmt.Errorf("slab %s has an unreadable timestamp %q: %w", slab.SlabID, pinnedAt, err)
		}
		slab.PinnedAt = parsed
		slab.Origin = SlabOrigin(origin)
		out = append(out, slab)
	}
	return out, rows.Err()
}

// TakeSlabOwnership promotes hydrated slabs to pinned, making them this
// device's to release.
//
// It exists for the case the origin split otherwise leaves stuck: storage
// written by an installation that no longer exists. Hydrating reaches the
// records but not the right to release the slabs under them, and if the device
// that pinned them is gone, reinstalled, replaced, or run from a directory
// that has since been deleted, there is no other device left to run the
// reclamation from, and the quota is held forever.
//
// It is deliberately a separate, explicit act rather than a fallback. Taking
// ownership while the other device is merely switched off would let this one
// delete storage that device is still pointing at, which is the hazard the
// split exists to prevent.
func (s *Store) TakeSlabOwnership() (int, error) {
	result, err := s.db.Exec(`UPDATE slabs SET origin = ? WHERE origin = ?`, string(SlabPinned), string(SlabHydrated))
	if err != nil {
		return 0, fmt.Errorf("take ownership of hydrated slabs: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count the slabs taken over: %w", err)
	}
	return int(affected), nil
}

// ForgetSlab drops a slab from the tracking list once it has been released.
func (s *Store) ForgetSlab(slabID string) error {
	if _, err := s.db.Exec(`DELETE FROM slabs WHERE slab_id = ?`, slabID); err != nil {
		return fmt.Errorf("forget slab %s: %w", slabID, err)
	}
	return nil
}
