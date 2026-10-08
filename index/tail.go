package index

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

// LockName is the file every process with the index open locks while it writes
// to the delta, or reads its end to decide what to take off.
const LockName = "index.lock"

// openTail settles the end of the delta as the store opens, under the file
// lock, and returns its size.
func (s *Store) openTail() (size int64, err error) {
	if err := s.lock.Lock(); err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, s.lock.Unlock()) }()
	return s.settle()
}

// settle makes sure the delta ends where a record ends, so that what is
// appended next is read as a record of its own, and returns where the delta
// then ends.
//
// It runs under the file lock, so no process is part way through an append. A
// delta that stops part way through a record was left that way by a process
// that stopped while it was appending, and that record never finished. Its
// vector is lost either way. Left in place, it would also take every record
// appended after it: the replay would read them as the rest of it, fail to open
// the result, and stop, and the next compaction would fold the delta without
// them. So it is taken off.
//
// The delta is read only when its size is not the size this store last left it
// at, since only another process can have changed it.
func (s *Store) settle() (int64, error) {
	info, err := s.file.Stat()
	if err != nil {
		return 0, fmt.Errorf("size index delta: %w", err)
	}
	if info.Size() == s.tail {
		return s.tail, nil
	}
	raw, err := os.ReadFile(s.path(DeltaName))
	if err != nil {
		return 0, fmt.Errorf("read index delta: %w", err)
	}
	end := wholeRecords(s.sealer, raw)
	if end < len(raw) {
		if err := s.cut(int64(end)); err != nil {
			return 0, err
		}
	}
	s.tail = int64(end)
	return s.tail, nil
}

// wholeRecords finds where the whole records at the start of a delta end: at
// the end of the file, or where a last record that never finished begins.
//
// A record is framed by its length, so a record cut off part way is the one the
// framing runs out on: its length prefix is short, or it claims more bytes than
// the file holds, or it claims none, which no record does. The replay reads
// nothing past that point, so cutting there loses nothing it could have
// reached. A last record whose bytes all landed is still taken for one that
// never finished if it does not open, because what reached the disk is not what
// was written. A record that does not open with another one after it is damage
// rather than an interrupted append, and it is left where it is, for the replay
// to stop at as it always has.
func wholeRecords(sealer Sealer, raw []byte) int {
	at, last := 0, -1
	for at < len(raw) {
		if len(raw)-at < 4 {
			return at
		}
		size := int64(binary.LittleEndian.Uint32(raw[at:]))
		if size == 0 || int64(at)+4+size > int64(len(raw)) {
			return at
		}
		last, at = at, at+4+int(size)
	}
	if last < 0 {
		return at
	}
	body, err := sealer.Open(raw[last+4:])
	if err != nil {
		return last
	}
	if _, err := decodeEntry(body); err != nil {
		return last
	}
	return at
}

// cut takes the delta back to size bytes and syncs the change.
//
// It truncates through a handle opened for writing rather than through the one
// appends go through. Go opens that one for appending only, which on Windows
// leaves it without the right to shorten the file.
func (s *Store) cut(size int64) error {
	file, err := os.OpenFile(s.path(DeltaName), os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("cut the index delta back to %d bytes: %w", size, err)
	}
	if err := file.Truncate(size); err != nil {
		file.Close()
		return fmt.Errorf("cut the index delta back to %d bytes: %w", size, err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync index delta: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	s.tail = size
	return nil
}
