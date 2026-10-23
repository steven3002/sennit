package index

import (
	"errors"
	"fmt"

	"github.com/steven3002/sennit/record"
)

// A Damage is what a read of the index's files could not read.
//
// A record is found by the length written in front of it, and is read only if
// it opens under the vault's key, so damage takes one of two shapes. A record
// whose length is whole and whose contents do not open is stepped over, and the
// read goes on with the record after it. A length that cannot be true, because
// it claims more than the file holds or claims nothing, says nothing about where
// the next record starts, so everything from there to the end of its file is
// lost to the read.
type Damage struct {
	// Skipped counts the records stepped over.
	Skipped int
	// Lost counts the bytes from a length that cannot be true to the end of its
	// file.
	Lost int64
}

// Found reports whether there was any damage.
func (d Damage) Found() bool { return d.Skipped > 0 || d.Lost > 0 }

// Add is what two reads found between them.
func (d Damage) Add(other Damage) Damage {
	return Damage{Skipped: d.Skipped + other.Skipped, Lost: d.Lost + other.Lost}
}

// A Reading is what one read of the index's two files found.
type Reading struct {
	// Entries are the vectors the files hold, one per record, a later entry for
	// a record having replaced an earlier one.
	Entries []Entry
	// Removed names the records whose latest entry withdraws them: records
	// forgotten here, or being forgotten, whose removal has not yet been folded
	// into a base.
	Removed map[record.ID]bool
	// Damage is what the read could not read.
	Damage Damage
}

// OpenStoreWatched opens or creates the persisted index in dir, as OpenStore
// does, and tells found of the damage every read of its files meets from then
// on, the settling of the delta as it opens included.
//
// found is called under the index's file lock, before what was read is acted
// on: before a compaction writes a new base from it and empties the delta, and
// before a length that cannot be true is cut off the end of the delta. That is
// the last moment the damage is on the disk to be seen, in this process or any
// other, so it is the moment a caller that has to rebuild what the damage took
// must record that it owes the rebuild. An error from found fails the read, and
// nothing that read would have erased is erased.
func OpenStoreWatched(dir string, sealer Sealer, found func(Damage) error) (*Store, error) {
	return openStore(dir, sealer, found)
}

// Read reads the two files under the index's file lock, and hands what it found
// to fn while the lock is still held.
//
// It is for a caller whose decision has to hold for the files as they are: no
// other process can append, compact or meet damage between the read and what fn
// does about it. fn must not call back into the store.
func (s *Store) Read(fn func(Reading) error) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.lock.Lock(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.lock.Unlock()) }()

	reading, err := s.read()
	if err != nil {
		return err
	}
	return fn(reading)
}

// tell passes damage to the store's watcher, if it has one.
func (s *Store) tell(damage Damage) error {
	if s.found == nil {
		return nil
	}
	if err := s.found(damage); err != nil {
		return fmt.Errorf("record that the search index is damaged: %w", err)
	}
	return nil
}

// holdsWholeRecord reports whether a whole record that opens starts anywhere in
// raw.
//
// It is what tells damage from an interrupted append where the framing of the
// delta gives out. An append that was stopped leaves at most the one record it
// was writing, short or unreadable, at the end of the file. A length damaged in
// the middle leaves the records after it whole, and a record that opens under
// the vault's key cannot be produced by accident, so finding one settles which
// of the two it is.
//
// Every offset is tried, because a damaged length says nothing about where the
// next record starts. A sealed record that opens has to begin with its own true
// length, and a length that does not fit in what is left is passed over without
// trying to open anything, which is nearly every offset in sealed bytes.
func holdsWholeRecord(sealer Sealer, raw []byte) bool {
	for at := 0; at < len(raw); at++ {
		size, framed := frameAt(raw, at)
		if !framed {
			continue
		}
		if _, err := openEntry(sealer, raw[at+4:at+4+size]); err == nil {
			return true
		}
	}
	return false
}
