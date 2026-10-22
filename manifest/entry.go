// Package manifest maps record ids to object locations.
package manifest

import (
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
)

// An Entry is the catalog's record of where one record version lives.
//
// It is keyed on the record id and not on the object ref, because repack
// rewrites every object ref: a catalog keyed on storage location would see
// every repacked record as a new record.
type Entry struct {
	ID        record.ID   `json:"id"`
	Kind      record.Kind `json:"kind"`
	Type      record.Type `json:"type"`
	Version   int64       `json:"version"`
	CID       seal.CID    `json:"cid"`
	ObjectRef string      `json:"objectRef"`
	SlabID    string      `json:"slabId,omitempty"`
	Bytes     int         `json:"bytes,omitempty"`
	Tags      []string    `json:"tags,omitempty"`
	WrittenAt record.Time `json:"writtenAt"`
	// Deleted marks a record the vault no longer holds.
	//
	// Removal is an append like every other change, because the catalog is a
	// log: a line that says the record is gone is the only form of removal that
	// survives a replay of the lines before it. The marker is kept rather than
	// dropped at compaction, since a snapshot that forgot it would let the
	// record come back the next time an older log was replayed over it.
	//
	// A removal keeps the record's ObjectRef and SlabID for as long as its
	// object may still be on the network, because a restore on any device that
	// finds the object puts the record back. Once this device has deleted the
	// object, the entry is appended again without either. See ObjectDeleted.
	Deleted bool `json:"deleted,omitempty"`
}
