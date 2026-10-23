package index

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/steven3002/sennit/internal/filelock"
	"github.com/steven3002/sennit/internal/rename"
	"github.com/steven3002/sennit/record"
)

// The two files an index is kept in. The base holds one entry per record as of
// the last compaction; the delta holds everything written since.
const (
	BaseName  = "index.base"
	DeltaName = "index.delta"

	pendingFile = "index.base.tmp"
)

const (
	// CompactRatio is how large the deltas may grow relative to the base before
	// the two are folded together. It is the ratio the catalog uses, for the
	// same reason: it is self-tuning, so the total bytes ever written stay
	// proportional to the record count rather than to its square.
	CompactRatio = 0.25

	// MinCompactBytes is the delta size below which folding is not worth doing.
	// Without a floor a small index would rewrite itself on nearly every write,
	// because a quarter of almost nothing is almost nothing.
	MinCompactBytes = 64 << 10
)

// A Sealer encrypts what the index writes to disk.
//
// It is an interface rather than the concrete type so that this package needs
// to know only that its bytes are protected, not how. Vectors are vault data:
// they are derived from the statements, and enough of the statement survives
// the derivation that storing them in the clear would undo the encryption of the
// records they came from.
type Sealer interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

// An Entry is one record's vector as it is persisted.
//
// The model travels with every vector rather than once per file. A file-level
// header would be smaller, and would also mean that the moment two models were
// ever mixed the evidence for which is which would be gone.
//
// An entry with no vector is a removal. The file is append-only, so forgetting
// a record is another line rather than an edit, exactly as it is in the catalog.
type Entry struct {
	ID     record.ID
	Model  string
	Dim    int
	Vector []float32
}

// Removed reports whether this entry withdraws a record from the index.
func (e Entry) Removed() bool { return e.Dim == 0 }

// Removal is the entry that withdraws a record.
func Removal(id record.ID) Entry { return Entry{ID: id} }

// A Store persists an index as an immutable base plus appended deltas.
//
// The shape is the catalog's, and so is the reason for it. Rewriting every
// vector on every write costs time quadratic in the record count; at a
// 384-dimension model each vector is about one and a half kilobytes, so a
// hundred thousand records would rewrite a hundred and fifty megabytes to add
// one. Appending a delta and folding it in occasionally keeps the cost
// proportional to what actually changed.
type Store struct {
	mu     sync.Mutex
	dir    string
	file   *os.File
	sealer Sealer

	baseBytes, deltaBytes int64
	written               int64
	compactions           int

	// lock is held, across processes, around every write to the delta and the
	// check that settles its end. A record another process is part way through
	// appending looks exactly like one a crash cut off.
	//
	// It is also held around every read of the two files together, and around a
	// whole compaction. A compaction replaces the base and empties the delta as
	// one step, so a reader between the two would see a base without the delta
	// that completes it, and a writer between the read and the emptying would
	// have its record emptied out with the rest.
	lock *filelock.Mutex
	// tail is the size this store last left the delta at, or -1 before it has
	// looked, so that only a delta another process has changed is read again.
	tail int64
	// found, when set, is told of the damage a read of the files or a settling
	// of the delta meets, under the file lock and before anything is done on the
	// strength of it. See OpenStoreWatched.
	found func(Damage) error
}

// OpenStore opens or creates the persisted index in dir.
func OpenStore(dir string, sealer Sealer) (*Store, error) { return openStore(dir, sealer, nil) }

func openStore(dir string, sealer Sealer, found func(Damage) error) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare index directory %s: %w", dir, err)
	}
	store := &Store{dir: dir, sealer: sealer, tail: -1, found: found}

	file, err := os.OpenFile(store.path(DeltaName), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open index delta in %s: %w", dir, err)
	}
	store.file = file
	if store.lock, err = filelock.Open(store.path(LockName)); err != nil {
		file.Close()
		return nil, err
	}

	if store.baseBytes, err = sizeOf(store.path(BaseName)); err != nil {
		store.Close()
		return nil, err
	}
	// The delta is sized once a record a crash cut off its end has been taken
	// off, so the next append starts a record of its own.
	if store.deltaBytes, err = store.openTail(); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// Close releases the delta file and the lock file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	return errors.Join(s.file.Close(), s.lock.Close())
}

// Append writes vectors to the delta and flushes them to disk.
//
// It syncs before returning. A vector that is only in the page cache when the
// process dies costs whatever it takes to re-embed the record, which is the one
// cost in this system that is measured in minutes rather than milliseconds.
//
// It holds the index's file lock while it writes, so that no other process can
// take a record half written for one a crash cut off, and it starts where the
// last whole record ends.
func (s *Store) Append(entries ...Entry) (err error) {
	if len(entries) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.lock.Lock(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.lock.Unlock()) }()

	end, err := s.settle()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		line, err := encodeEntry(s.sealer, entry)
		if err != nil {
			return err
		}
		n, err := s.file.Write(line)
		if err != nil {
			// Whatever part of the record reached the file comes back off.
			return errors.Join(fmt.Errorf("append index delta: %w", err), s.cut(end))
		}
		end += int64(n)
		s.tail = end
		s.deltaBytes += int64(n)
		s.written += int64(n)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("sync index delta: %w", err)
	}
	return nil
}

// Hydrate reads the base and replays the deltas over it.
//
// A later entry for a record wins, which is what makes replay idempotent: an
// entry that is in both the base and the delta lands on the same value either
// way. That is also why compaction can install a new base before emptying the
// delta rather than after.
//
// It reads under the index's file lock, so that the base and the delta it
// replays belong together. Another process compacting in between would have
// installed a new base and emptied the delta, and a base read before that with
// a delta read after it is missing everything the delta held. The lock also
// keeps the read clear of the rename that installs a base: on Windows a file
// that is open cannot be renamed over, and one that is being renamed over
// cannot be opened.
//
// What it cannot read it reads past where it can, and tells the store's watcher
// of, as readEntries describes.
func (s *Store) Hydrate() (entries []Entry, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.lock.Lock(); err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, s.lock.Unlock()) }()
	return s.hydrate()
}

func (s *Store) hydrate() ([]Entry, error) {
	reading, err := s.read()
	return reading.Entries, err
}

// read replays the base and then the delta, for a caller that holds the store
// and the file lock, and tells the store's watcher of any damage it met before
// returning what it read.
//
// The watcher is told here, inside the read, because every caller acts on what
// it read while it still holds the lock, and a compaction acts on it by writing
// a new base without the damage and emptying the delta. Told any later, the
// damage would already be off the disk.
func (s *Store) read() (Reading, error) {
	at := make(map[record.ID]int)
	var (
		out    []Entry
		damage Damage
	)
	replay := func(name string, delta bool) error {
		entries, found, err := readEntries(s.sealer, s.path(name), delta)
		if err != nil {
			return err
		}
		damage = damage.Add(found)
		for _, entry := range entries {
			if position, ok := at[entry.ID]; ok {
				out[position] = entry
				continue
			}
			at[entry.ID] = len(out)
			out = append(out, entry)
		}
		return nil
	}
	if err := replay(BaseName, false); err != nil {
		return Reading{}, err
	}
	if err := replay(DeltaName, true); err != nil {
		return Reading{}, err
	}

	reading := Reading{Removed: make(map[record.ID]bool), Damage: damage}
	live := out[:0]
	for _, entry := range out {
		if entry.Removed() {
			reading.Removed[entry.ID] = true
			continue
		}
		live = append(live, entry)
	}
	reading.Entries = live
	if damage.Found() {
		if err := s.tell(damage); err != nil {
			return Reading{}, err
		}
	}
	return reading, nil
}

// Remove withdraws a record from the persisted index.
//
// Removal markers are not carried into the base. Unlike the catalog, nothing
// ever replays an older file over a newer base here, the base is written from
// what hydration produced, which has already dropped the removed records.
//
// Damage is the one way a forgotten vector can come back. A read steps over a
// record that does not open, and when that record was the removal, the vector
// written before it reads as live. That is part of why damage is told to the
// store's watcher: a vault that is told takes out the vector of every record it
// no longer holds before it searches any of them.
func (s *Store) Remove(ids ...record.ID) error {
	entries := make([]Entry, len(ids))
	for i, id := range ids {
		entries[i] = Removal(id)
	}
	return s.Append(entries...)
}

// DueForCompaction reports whether the deltas have outgrown the base.
func (s *Store) DueForCompaction() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dueForCompaction()
}

func (s *Store) dueForCompaction() bool {
	if s.deltaBytes < MinCompactBytes {
		return false
	}
	return float64(s.deltaBytes) > CompactRatio*float64(s.baseBytes)
}

// CompactIfDue folds the deltas into a fresh base once they have outgrown it,
// and reports whether it did.
//
// It reads the files and writes the new base under one hold of the store, which
// is why it is one call rather than DueForCompaction, Hydrate and Compact in
// turn. A vector appended or removed between a read and a later write lands in
// the delta the new base was not built from, and emptying the delta then drops
// it: the next process finds a record just stored missing from the index, and a
// record just forgotten back in it.
//
// The hold is of the index's file lock as well as of this store, because the
// other writer may be another process: a command run while an agent's server
// has the same vault open.
func (s *Store) CompactIfDue() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dueForCompaction() {
		return false, nil
	}
	return true, s.fold()
}

// Fold folds the deltas into a fresh base whether or not they have outgrown it,
// reading and writing under one hold of the store as CompactIfDue does.
func (s *Store) Fold() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fold()
}

func (s *Store) fold() (err error) {
	if err := s.lock.Lock(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.lock.Unlock()) }()

	entries, err := s.hydrate()
	if err != nil {
		return err
	}
	return s.compact(entries)
}

// Compact folds the deltas into a fresh base holding one entry per record.
//
// The new base is written beside the old one and renamed into place before the
// delta is emptied, so the only state a crash can leave behind is a delta
// holding entries the base already has. Replaying those a second time lands on
// the same value, which is why this order is safe and the reverse is not.
//
// The base is written from the entries given, and whatever reached the delta
// after they were read is emptied out with it. A caller folding the store's own
// files wants CompactIfDue or Fold, which read them under the same hold.
func (s *Store) Compact(entries []Entry) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.lock.Lock(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.lock.Unlock()) }()
	return s.compact(entries)
}

// compact writes the new base and empties the delta. The caller holds the
// index's file lock, which every process takes to append to the delta, to read
// the two files, and to compact them. So the delta is never emptied part way
// through another process's record, two processes never write the one pending
// file at once, and the base is never renamed over while another process has
// it open to read.
func (s *Store) compact(entries []Entry) error {
	pending, err := os.OpenFile(s.path(pendingFile), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open index base: %w", err)
	}
	defer os.Remove(s.path(pendingFile))

	var written int64
	for _, entry := range entries {
		line, err := encodeEntry(s.sealer, entry)
		if err != nil {
			pending.Close()
			return err
		}
		n, err := pending.Write(line)
		if err != nil {
			pending.Close()
			return fmt.Errorf("write index base: %w", err)
		}
		written += int64(n)
	}
	if err := pending.Sync(); err != nil {
		pending.Close()
		return fmt.Errorf("sync index base: %w", err)
	}
	if err := pending.Close(); err != nil {
		return fmt.Errorf("close index base: %w", err)
	}
	if err := rename.Over(s.path(pendingFile), s.path(BaseName)); err != nil {
		return fmt.Errorf("install index base: %w", err)
	}

	// The delta is emptied through cut, which truncates through a handle of its
	// own. The handle appends go through cannot shorten the file on Windows.
	if err := s.cut(0); err != nil {
		return fmt.Errorf("empty index delta: %w", err)
	}
	s.baseBytes = written
	s.deltaBytes = 0
	s.written += written
	s.compactions++
	return nil
}

// Stats describes what the persisted index has cost on disk.
type Stats struct {
	// BaseBytes and DeltaBytes are the two files.
	BaseBytes, DeltaBytes int64
	// Written is every byte this store has put on disk since it was opened,
	// appends and bases together, so write amplification is measurable rather
	// than argued about.
	Written int64
	// Compactions is how many bases have been written.
	Compactions int
}

// Stats reports the persisted index's cost on disk.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		BaseBytes:   s.baseBytes,
		DeltaBytes:  s.deltaBytes,
		Written:     s.written,
		Compactions: s.compactions,
	}
}

// ErrCorruptIndex reports a persisted index that cannot be parsed.
var ErrCorruptIndex = errors.New("persisted index is corrupt")

// encodeEntry seals one entry and length-prefixes it.
func encodeEntry(sealer Sealer, entry Entry) ([]byte, error) {
	if len(entry.Vector) != entry.Dim {
		return nil, fmt.Errorf("entry %s declares %d dimensions and carries %d",
			entry.ID, entry.Dim, len(entry.Vector))
	}
	body := make([]byte, 0, 2+len(entry.Model)+2+record.IDSize+4*entry.Dim)
	body = binary.LittleEndian.AppendUint16(body, uint16(len(entry.Model)))
	body = append(body, entry.Model...)
	body = binary.LittleEndian.AppendUint16(body, uint16(entry.Dim))
	body = append(body, entry.ID[:]...)
	for _, value := range entry.Vector {
		body = binary.LittleEndian.AppendUint32(body, math.Float32bits(value))
	}

	sealed, err := sealer.Seal(body)
	if err != nil {
		return nil, fmt.Errorf("seal index entry %s: %w", entry.ID, err)
	}
	line := binary.LittleEndian.AppendUint32(make([]byte, 0, 4+len(sealed)), uint32(len(sealed)))
	return append(line, sealed...), nil
}

// readEntries parses one of the index's files, and reports what of it could not
// be read.
//
// Damage is bounded rather than fatal, matching how a truncated record blob is
// treated. A record whose length is whole and whose contents do not open is
// stepped over, because the length still says where the next one starts, and
// every record after it is as good as it was. A length that cannot be true says
// nothing about where anything starts, so the read stops there, and the rest of
// the file is lost to it. Either way the records behind the lost vectors are on
// the device, and can be embedded again.
//
// The end of the delta is read as the store settles it when it opens. A last
// record there that the file runs out in, or that does not open, is what a
// process stopped part way through an append leaves: the record that append was
// writing is lost, and that is the cost of the interruption rather than damage.
// It is damage only when a whole record is to be found past the point the read
// stopped at, because no interrupted append leaves a whole record behind it.
func readEntries(sealer Sealer, path string, delta bool) ([]Entry, Damage, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, Damage{}, nil
	}
	if err != nil {
		return nil, Damage{}, fmt.Errorf("read %s: %w", path, err)
	}

	var (
		out    []Entry
		damage Damage
	)
	for at := 0; at < len(raw); {
		size, framed := frameAt(raw, at)
		if !framed {
			if !delta || holdsWholeRecord(sealer, raw[at:]) {
				damage.Lost += int64(len(raw) - at)
			}
			break
		}
		next := at + 4 + size
		entry, err := openEntry(sealer, raw[at+4:next])
		if err != nil {
			if !delta || next < len(raw) {
				damage.Skipped++
			}
			at = next
			continue
		}
		out = append(out, entry)
		at = next
	}
	return out, damage, nil
}

const (
	// maxEntryBody is the longest body encodeEntry can write. The model's name
	// and the vector's width are each written as a uint16, so a body holds at
	// most a name of 65535 bytes, a vector of 65535 four-byte values, the two
	// lengths and the record id.
	maxEntryBody = 2 + math.MaxUint16 + 2 + record.IDSize + 4*math.MaxUint16
	// maxSealedEntry is the longest a sealed entry can be. Sealing adds to the
	// body: the vault's sealer compresses it, which grows bytes that do not
	// compress by about one in 256, and then adds a 24-byte nonce and a 16-byte
	// tag. The allowance covers both with room to spare.
	maxSealedEntry = maxEntryBody + maxEntryBody/128 + 4096
)

// frameAt reads the length of the record that starts at, and reports whether
// the record it frames is all there: a length is whole, names some bytes, names
// no more than the file holds after it, and no more than any entry can be.
//
// The last test is what bounds the search past a damaged length. A length read
// at an arbitrary offset can claim nearly anything, and one claiming more than
// an entry can be cannot be a record, so it is passed over without opening it.
// Without it the bytes holdsWholeRecord opens grow with the square of what it
// searches.
func frameAt(raw []byte, at int) (int, bool) {
	if len(raw)-at < 4 {
		return 0, false
	}
	size := int64(binary.LittleEndian.Uint32(raw[at:]))
	if size == 0 || size > maxSealedEntry || int64(at)+4+size > int64(len(raw)) {
		return 0, false
	}
	return int(size), true
}

// openEntry unseals and decodes one record.
func openEntry(sealer Sealer, sealed []byte) (Entry, error) {
	body, err := sealer.Open(sealed)
	if err != nil {
		return Entry{}, err
	}
	return decodeEntry(body)
}

func decodeEntry(body []byte) (Entry, error) {
	if len(body) < 2 {
		return Entry{}, ErrCorruptIndex
	}
	nameLen := int(binary.LittleEndian.Uint16(body))
	at := 2
	if at+nameLen+2+record.IDSize > len(body) {
		return Entry{}, ErrCorruptIndex
	}
	entry := Entry{Model: string(body[at : at+nameLen])}
	at += nameLen
	entry.Dim = int(binary.LittleEndian.Uint16(body[at:]))
	at += 2
	copy(entry.ID[:], body[at:at+record.IDSize])
	at += record.IDSize

	if len(body)-at != 4*entry.Dim {
		return Entry{}, ErrCorruptIndex
	}
	entry.Vector = make([]float32, entry.Dim)
	for i := range entry.Vector {
		entry.Vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(body[at+4*i:]))
	}
	return entry, nil
}

func sizeOf(path string) (int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("size %s: %w", path, err)
	}
	return info.Size(), nil
}
