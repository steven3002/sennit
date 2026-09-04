package manifest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
)

// The files a catalog is kept in. The snapshot holds one line per live record;
// the log holds everything appended since it was taken.
const (
	LogName      = "manifest.log"
	SnapshotName = "manifest.snapshot"

	pendingFile = "manifest.snapshot.tmp"
)

// A Log is the catalog's durable form: a snapshot plus an append-only tail.
//
// Rewriting a whole catalog on every write costs time quadratic in the record
// count, and the bytes it churns are charged for, at a hundred thousand
// records that is the difference between rewriting gigabytes and rewriting tens
// of megabytes. Appending a delta per flush and folding it into a snapshot
// occasionally is what keeps the catalog's cost proportional to what changed.
type Log struct {
	mu     sync.Mutex
	dir    string
	file   *os.File
	sealer *Sealer
	// logBytes and snapshotBytes size the two files, which is what the
	// compaction ratio is measured on.
	logBytes, snapshotBytes int64
	// written counts every byte this log has put on disk, appends and
	// snapshots alike, so the catalog's write amplification is measurable
	// rather than argued about.
	written int64
	// compactions counts how many snapshots have been taken.
	compactions int
}

// A Sealer encrypts catalog entries.
type Sealer = seal.Sealer

// OpenLog opens or creates the catalog in dir.
func OpenLog(dir string, sealer *Sealer) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare manifest directory %s: %w", dir, err)
	}
	log := &Log{dir: dir, sealer: sealer}

	file, err := os.OpenFile(log.path(LogName), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open manifest log in %s: %w", dir, err)
	}
	log.file = file

	if log.logBytes, err = sizeOf(log.path(LogName)); err != nil {
		return nil, err
	}
	if log.snapshotBytes, err = sizeOf(log.path(SnapshotName)); err != nil {
		return nil, err
	}
	return log, nil
}

func (l *Log) path(name string) string { return filepath.Join(l.dir, name) }

// Close releases the log file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

// Append writes one sealed entry and flushes it to disk.
func (l *Log) Append(entry Entry) error {
	line, err := encodeEntry(l.sealer, entry)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, err := l.file.Write(line); err != nil {
		return fmt.Errorf("append manifest entry %s: %w", entry.ID, err)
	}
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("sync manifest log: %w", err)
	}
	l.logBytes += int64(len(line))
	l.written += int64(len(line))
	return nil
}

// Replay reads the snapshot and then the log, returning the current entry per
// record and the order records first appeared.
//
// Reading the snapshot first and the log second is what makes a repeated entry
// harmless: the log may still hold lines a snapshot already folded in, and
// applying them again lands on the same value.
func (l *Log) Replay() (map[record.ID]Entry, []record.ID, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entries := make(map[record.ID]Entry)
	var order []record.ID

	snapshot, err := os.Open(l.path(SnapshotName))
	switch {
	case err == nil:
		order, err = readEntries(snapshot, l.sealer, SnapshotName, entries, order)
		snapshot.Close()
		if err != nil {
			return nil, nil, err
		}
	case !os.IsNotExist(err):
		return nil, nil, fmt.Errorf("open manifest snapshot: %w", err)
	}

	if _, err := l.file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, fmt.Errorf("rewind manifest log: %w", err)
	}
	if order, err = readEntries(l.file, l.sealer, LogName, entries, order); err != nil {
		return nil, nil, err
	}
	if _, err := l.file.Seek(0, io.SeekEnd); err != nil {
		return nil, nil, fmt.Errorf("seek to end of manifest log: %w", err)
	}
	return entries, order, nil
}

func sizeOf(path string) (int64, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("size %s: %w", path, err)
	}
	return info.Size(), nil
}
