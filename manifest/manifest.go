package manifest

import (
	"errors"
	"fmt"
	"sync"

	"github.com/steven3002/sennit/record"
)

// ErrNotFound reports that the catalog holds no entry for a record.
var ErrNotFound = errors.New("no manifest entry")

// A Manifest is the catalog of record ids to object locations.
//
// It is a performance index, not a correctness requirement: every stored blob
// carries its own record identity, so a lost catalog costs a rebuild rather
// than the data. That is what allows the on-disk form to be an append-only log
// instead of a structure that must be rewritten to stay correct.
type Manifest struct {
	mu      sync.RWMutex
	log     *Log
	entries map[record.ID]Entry
	order   []record.ID
}

// Load opens the catalog at path and replays it.
func Load(log *Log) (*Manifest, error) {
	entries, order, err := log.Replay()
	if err != nil {
		return nil, err
	}
	return &Manifest{log: log, entries: entries, order: order}, nil
}

// Append records where a record version landed.
//
// The catalog folds itself into a snapshot when the log has outgrown one,
// rather than on a schedule or a record count: the ratio is what keeps the
// bytes ever written proportional to the number of records instead of to its
// square, and it needs no tuning as a vault grows.
func (m *Manifest) Append(entry Entry) error {
	if err := m.append(entry); err != nil {
		return err
	}
	if !m.DueForCompaction() {
		return nil
	}
	return m.Compact()
}

func (m *Manifest) append(entry Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.log.Append(entry); err != nil {
		return err
	}
	if _, seen := m.entries[entry.ID]; !seen {
		m.order = append(m.order, entry.ID)
	}
	m.entries[entry.ID] = entry
	return nil
}

// Remove marks a record as no longer held.
//
// It appends rather than erases, because the catalog is a log and an erasure
// would be undone by replaying the lines that came before it. The storage the
// record occupied is not released here: a slab is shared and billed whole, so
// it comes back only once nothing live is left in it and reclamation runs.
func (m *Manifest) Remove(id record.ID) error {
	entry, err := m.Lookup(id)
	if err != nil {
		return err
	}
	entry.Deleted = true
	entry.WrittenAt = record.Now()
	return m.Append(entry)
}

// Lookup resolves a record id to its location.
func (m *Manifest) Lookup(id record.ID) (Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, ok := m.entries[id]
	if !ok || entry.Deleted {
		return Entry{}, fmt.Errorf("%w for %s", ErrNotFound, id)
	}
	return entry, nil
}

// Entries lists every catalogued record in the order it was first written.
func (m *Manifest) Entries() []Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Entry, 0, len(m.order))
	for _, id := range m.order {
		if entry := m.entries[id]; !entry.Deleted {
			out = append(out, entry)
		}
	}
	return out
}

// Len reports how many records the catalog holds.
func (m *Manifest) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var live int
	for _, entry := range m.entries {
		if !entry.Deleted {
			live++
		}
	}
	return live
}

// Close releases the underlying log.
func (m *Manifest) Close() error { return m.log.Close() }
