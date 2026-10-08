package manifest

import (
	"errors"
	"fmt"
	"os"
)

// CompactRatio is how large the log may grow relative to the snapshot before
// the two are folded together.
//
// It is a ratio rather than a fixed size because it is self-tuning: a small
// catalog compacts after a few entries and a large one after many, and the
// total bytes ever written stay proportional to the record count instead of to
// its square.
const CompactRatio = 0.25

// MinCompactBytes is the log size below which compaction is not worth doing.
//
// Without a floor, a catalog holding a handful of records would rewrite itself
// on nearly every append: a quarter of almost nothing is almost nothing.
//
// It gives the catalog two regimes. While the snapshot is under four times this
// figure the floor is the binding constraint, so snapshots are taken at even
// intervals and grow by a shrinking multiple; above it the ratio binds and they
// grow geometrically, which is what makes the total bytes written proportional
// to the record count. The first regime covers roughly the first thousand
// records and costs under a megabyte in total, so it is not worth avoiding.
const MinCompactBytes = 64 << 10

// Stats describes what the catalog has cost on disk.
type Stats struct {
	// Records is how many records the catalog holds.
	Records int
	// LogBytes and SnapshotBytes are the two files on disk.
	LogBytes, SnapshotBytes int64
	// Written is every byte this catalog has put on disk since it was opened,
	// appends and snapshots together. It is the figure that separates a
	// log-structured catalog from one that rewrites itself.
	Written int64
	// Compactions is how many snapshots have been taken.
	Compactions int
}

// Stats reports the catalog's cost on disk.
func (m *Manifest) Stats() Stats {
	records := m.Len()

	m.log.mu.Lock()
	defer m.log.mu.Unlock()
	return Stats{
		Records:       records,
		LogBytes:      m.log.logBytes,
		SnapshotBytes: m.log.snapshotBytes,
		Written:       m.log.written,
		Compactions:   m.log.compactions,
	}
}

// DueForCompaction reports whether the log has outgrown its snapshot.
func (m *Manifest) DueForCompaction() bool {
	m.log.mu.Lock()
	defer m.log.mu.Unlock()
	return m.log.dueForCompaction()
}

func (l *Log) dueForCompaction() bool {
	if l.logBytes < MinCompactBytes {
		return false
	}
	return float64(l.logBytes) > CompactRatio*float64(l.snapshotBytes)
}

// Compact folds the log into a fresh snapshot holding one line per record.
//
// The snapshot is written beside the old one and renamed into place before the
// log is emptied, so the only state a crash can leave is a log holding entries
// the snapshot already has. Replaying those a second time lands on the same
// value, which is why the order is safe and the reverse order would not be.
//
// Removal markers are carried into the snapshot rather than dropped. A snapshot
// that forgot them would let a deleted record return the next time an older log
// was replayed over it, which is the one thing a compaction must not do.
func (m *Manifest) Compact() error {
	m.mu.RLock()
	entries := make([]Entry, 0, len(m.order))
	for _, id := range m.order {
		entries = append(entries, m.entries[id])
	}
	m.mu.RUnlock()

	return m.log.compact(entries)
}

func (l *Log) compact(entries []Entry) (err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// The log is emptied under the file lock every process takes to append to
	// it, so it is never emptied part way through another process's line.
	if err := l.lock.Lock(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, l.lock.Unlock()) }()

	pending, err := os.OpenFile(l.path(pendingFile), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open manifest snapshot: %w", err)
	}
	defer os.Remove(l.path(pendingFile))

	var written int64
	for _, entry := range entries {
		line, err := encodeEntry(l.sealer, entry)
		if err != nil {
			pending.Close()
			return err
		}
		n, err := pending.Write(line)
		if err != nil {
			pending.Close()
			return fmt.Errorf("write manifest snapshot: %w", err)
		}
		written += int64(n)
	}
	if err := pending.Sync(); err != nil {
		pending.Close()
		return fmt.Errorf("sync manifest snapshot: %w", err)
	}
	if err := pending.Close(); err != nil {
		return fmt.Errorf("close manifest snapshot: %w", err)
	}
	if err := os.Rename(l.path(pendingFile), l.path(SnapshotName)); err != nil {
		return fmt.Errorf("install manifest snapshot: %w", err)
	}

	if err := l.file.Truncate(0); err != nil {
		return fmt.Errorf("empty manifest log: %w", err)
	}
	if _, err := l.file.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind manifest log: %w", err)
	}
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("sync manifest log: %w", err)
	}

	l.snapshotBytes, l.logBytes = written, 0
	l.written += written
	l.compactions++
	return nil
}
