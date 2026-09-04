package index

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"

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
}

// OpenStore opens or creates the persisted index in dir.
func OpenStore(dir string, sealer Sealer) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare index directory %s: %w", dir, err)
	}
	store := &Store{dir: dir, sealer: sealer}

	file, err := os.OpenFile(store.path(DeltaName), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open index delta in %s: %w", dir, err)
	}
	store.file = file

	if store.baseBytes, err = sizeOf(store.path(BaseName)); err != nil {
		return nil, err
	}
	if store.deltaBytes, err = sizeOf(store.path(DeltaName)); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// Close releases the delta file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	return s.file.Close()
}

// Append writes vectors to the delta and flushes them to disk.
//
// It syncs before returning. A vector that is only in the page cache when the
// process dies costs whatever it takes to re-embed the record, which is the one
// cost in this system that is measured in minutes rather than milliseconds.
func (s *Store) Append(entries ...Entry) error {
	if len(entries) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, entry := range entries {
		line, err := encodeEntry(s.sealer, entry)
		if err != nil {
			return err
		}
		n, err := s.file.Write(line)
		if err != nil {
			return fmt.Errorf("append index delta: %w", err)
		}
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
func (s *Store) Hydrate() ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	at := make(map[record.ID]int)
	var out []Entry
	replay := func(name string) error {
		entries, err := readEntries(s.sealer, s.path(name))
		if err != nil {
			return err
		}
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
	if err := replay(BaseName); err != nil {
		return nil, err
	}
	if err := replay(DeltaName); err != nil {
		return nil, err
	}

	live := out[:0]
	for _, entry := range out {
		if !entry.Removed() {
			live = append(live, entry)
		}
	}
	return live, nil
}

// Remove withdraws a record from the persisted index.
//
// Removal markers are not carried into the base. Unlike the catalog, nothing
// ever replays an older file over a newer base here, the base is written from
// what hydration produced, which has already dropped the removed records, so
// there is no path by which a forgotten vector could come back.
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

// Compact folds the deltas into a fresh base holding one entry per record.
//
// The new base is written beside the old one and renamed into place before the
// delta is emptied, so the only state a crash can leave behind is a delta
// holding entries the base already has. Replaying those a second time lands on
// the same value, which is why this order is safe and the reverse is not.
func (s *Store) Compact(entries []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	if err := os.Rename(s.path(pendingFile), s.path(BaseName)); err != nil {
		return fmt.Errorf("install index base: %w", err)
	}

	if err := s.file.Truncate(0); err != nil {
		return fmt.Errorf("empty index delta: %w", err)
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind index delta: %w", err)
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

// readEntries parses a whole file, stopping at the first damage.
//
// Damage is bounded to the tail rather than fatal, matching how a truncated
// record blob is treated: the entries before the break are perfectly good, and
// the records behind the ones that are lost can be re-embedded.
func readEntries(sealer Sealer, path string) ([]Entry, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var out []Entry
	for offset := 0; offset+4 <= len(raw); {
		size := int(binary.LittleEndian.Uint32(raw[offset:]))
		offset += 4
		if size <= 0 || offset+size > len(raw) {
			break
		}
		body, err := sealer.Open(raw[offset : offset+size])
		if err != nil {
			break
		}
		offset += size

		entry, err := decodeEntry(body)
		if err != nil {
			break
		}
		out = append(out, entry)
	}
	return out, nil
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
