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

// Refresh reads the catalog's files again when another process has written to
// them since this one last read them, and reports whether it did.
//
// The catalog is read once, when it is loaded, and every change this process
// makes is made to that copy as well as to the files. Every process with the
// vault open appends to the same files, though: a flush or a forget run in a
// terminal, or a second agent's server. Without this, a long-lived process goes
// on answering from a copy that no longer says what the vault holds, until it
// restarts.
//
// Telling whether the files have changed costs a stat of each, so it can be
// asked before every answer. Reading them again costs what loading them does,
// and is done only when they have changed. The copy is replaced while the
// catalog is held, so a reader sees the copy from before or the one from after,
// and nothing this process appends in the meantime is left out of either.
func (m *Manifest) Refresh() (bool, error) {
	if !m.Changed() {
		return false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Another call may have read them while this one waited for the catalog.
	if !m.Changed() {
		return false, nil
	}
	entries, order, err := m.log.Replay()
	if err != nil {
		return false, err
	}
	m.entries, m.order = entries, order
	return true, nil
}

// Changed reports whether another process has written to the catalog's files
// since this one last read them, which is when Refresh reads them again. It
// costs a stat of each file. See Log.Changed.
func (m *Manifest) Changed() bool { return m.log.Changed() }

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

// Named reports how many records the catalog has ever named, counting the ones
// it has since marked as no longer held.
//
// A removal is kept as an entry, so this only ever grows. That makes it the one
// figure here that says whether this catalog has held a record at all, which
// Len cannot once every record it held has been removed.
func (m *Manifest) Named() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}

// Dropped reports how many changes this catalog has found cut off at the end of
// its log, and dropped, since it was opened.
//
// A process that stops part way through an append leaves whatever part of the
// line reached the disk, and nothing can read it. Each such line was one
// record's latest change, and it is the only thing lost with it: the catalog
// holds what it held for that record before the change was made.
func (m *Manifest) Dropped() int {
	m.log.mu.Lock()
	defer m.log.mu.Unlock()
	return m.log.dropped
}

// Close releases the underlying log.
func (m *Manifest) Close() error { return m.log.Close() }
