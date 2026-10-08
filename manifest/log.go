package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/steven3002/sennit/internal/filelock"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
)

// The files a catalog is kept in. The snapshot holds one line per live record;
// the log holds everything appended since it was taken.
const (
	LogName      = "manifest.log"
	SnapshotName = "manifest.snapshot"
	// LockName is the file every process with the catalog open locks while it
	// changes the catalog's files, or reads the log to decide what to drop.
	LockName = "manifest.lock"

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
	// lock is held, across processes, around every change to the catalog's
	// files and around the replay that decides what to drop from the log. A
	// line another process is part way through appending looks exactly like
	// one a crash cut off, and only a line nobody holds the lock over can be
	// the second.
	lock *filelock.Mutex
	// logBytes and snapshotBytes size the two files, which is what the
	// compaction ratio is measured on.
	logBytes, snapshotBytes int64
	// written counts every byte this log has put on disk, appends and
	// snapshots alike, so the catalog's write amplification is measurable
	// rather than argued about.
	written int64
	// compactions counts how many snapshots have been taken.
	compactions int
	// dropped counts the lines found cut off at the end of the log and taken
	// off it since it was opened.
	dropped int
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
	if log.lock, err = filelock.Open(log.path(LockName)); err != nil {
		file.Close()
		return nil, err
	}

	if log.logBytes, err = sizeOf(log.path(LogName)); err != nil {
		log.Close()
		return nil, err
	}
	if log.snapshotBytes, err = sizeOf(log.path(SnapshotName)); err != nil {
		log.Close()
		return nil, err
	}
	return log, nil
}

func (l *Log) path(name string) string { return filepath.Join(l.dir, name) }

// Close releases the log file and the lock file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return errors.Join(l.file.Close(), l.lock.Close())
}

// Append writes one sealed entry and flushes it to disk.
//
// It holds the catalog's file lock while it writes, because every process with
// the catalog open appends to the same log, and one that opened it while a line
// was half written would otherwise take that line for one a crash cut off.
func (l *Log) Append(entry Entry) (err error) {
	line, err := encodeEntry(l.sealer, entry)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.lock.Lock(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, l.lock.Unlock()) }()

	end, err := l.settle()
	if err != nil {
		return err
	}
	if _, err := l.file.Write(line); err != nil {
		// Whatever part of the line reached the file comes back off, so that
		// the next append does not start part way through it.
		return errors.Join(fmt.Errorf("append manifest entry %s: %w", entry.ID, err), l.cut(end))
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
//
// A last line of the log that cannot be read is what an append interrupted by a
// crash leaves. Losing that one change is correct where refusing to open the
// vault would not be, so the line is dropped, counted, and cut off the end of
// the file, which lets the next append start a line of its own. A line that
// cannot be read anywhere else, in the log or in the snapshot, is damage and
// stops the replay. All of it happens under the file lock, so the last line is
// never one another process is still writing, nothing another process appends
// after the replay has looked can be cut, and no compaction can replace the
// snapshot between the reading of the one file and of the other.
func (l *Log) Replay() (entries map[record.ID]Entry, order []record.ID, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.lock.Lock(); err != nil {
		return nil, nil, err
	}
	defer func() { err = errors.Join(err, l.lock.Unlock()) }()

	entries = make(map[record.ID]Entry)

	snapshot, err := os.Open(l.path(SnapshotName))
	switch {
	case err == nil:
		read, err := readEntries(snapshot, l.sealer, SnapshotName, entries, order)
		snapshot.Close()
		if err == nil {
			// A snapshot is written beside the old one and renamed into place
			// whole, so no crash leaves one with its last line cut off.
			err = read.cutOff
		}
		if err != nil {
			return nil, nil, err
		}
		order = read.order
	case !os.IsNotExist(err):
		return nil, nil, fmt.Errorf("open manifest snapshot: %w", err)
	}

	if _, err := l.file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, fmt.Errorf("rewind manifest log: %w", err)
	}
	read, err := readEntries(l.file, l.sealer, LogName, entries, order)
	if err != nil {
		return nil, nil, err
	}
	if read.cutOff != nil {
		if err := l.cut(read.end); err != nil {
			return nil, nil, err
		}
		l.dropped++
	}
	if l.logBytes, err = l.settle(); err != nil {
		return nil, nil, err
	}
	if _, err := l.file.Seek(0, io.SeekEnd); err != nil {
		return nil, nil, fmt.Errorf("seek to end of manifest log: %w", err)
	}
	return entries, read.order, nil
}

// settle makes sure the log ends where a line ends, so that what is appended
// next is a line of its own, and returns where the log then ends.
//
// It runs under the file lock, so no process is part way through an append. A
// log that stops part way through a line was left that way by a process that
// stopped while it was appending, and the line is treated as an open treats a
// last line. If it cannot be read, it is the change that never finished, so it
// is taken off and counted as dropped. If it can be read, it reached the disk
// whole but for its newline, so it is kept and the newline is added.
func (l *Log) settle() (int64, error) {
	info, err := l.file.Stat()
	if err != nil {
		return 0, fmt.Errorf("size manifest log: %w", err)
	}
	size := info.Size()
	if size == 0 {
		return 0, nil
	}
	last := make([]byte, 1)
	if _, err := l.file.ReadAt(last, size-1); err != nil {
		return 0, fmt.Errorf("read the end of the manifest log: %w", err)
	}
	if last[0] == '\n' {
		return size, nil
	}

	start, err := l.lineStart(size)
	if err != nil {
		return 0, err
	}
	tail := make([]byte, size-start)
	if _, err := l.file.ReadAt(tail, start); err != nil {
		return 0, fmt.Errorf("read the end of the manifest log: %w", err)
	}
	if text := trimLine(tail); len(text) > 0 {
		if _, err := decodeLine(l.sealer, text); err != nil {
			if err := l.cut(start); err != nil {
				return 0, err
			}
			l.dropped++
			return start, nil
		}
	}
	if _, err := l.file.Write([]byte{'\n'}); err != nil {
		return 0, errors.Join(fmt.Errorf("finish the last line of the manifest log: %w", err), l.cut(size))
	}
	l.logBytes++
	l.written++
	return size + 1, nil
}

// lineStart finds where the line the log ends with begins: just after the
// newline before it, or at the start of the file.
//
// It reads back no further than the longest line the catalog reads. A run of
// bytes longer than that with no newline in it is not a line this catalog
// wrote, whole or in part, and it is refused rather than guessed at.
func (l *Log) lineStart(size int64) (int64, error) {
	block := make([]byte, 64<<10)
	for end := size; end > 0; {
		if size-end > maxLineBytes {
			return 0, fmt.Errorf("the manifest log ends in more than %d bytes with no line break, which no append leaves", maxLineBytes)
		}
		start := max(0, end-int64(len(block)))
		chunk := block[:end-start]
		if _, err := l.file.ReadAt(chunk, start); err != nil {
			return 0, fmt.Errorf("read the end of the manifest log: %w", err)
		}
		if i := bytes.LastIndexByte(chunk, '\n'); i >= 0 {
			return start + int64(i) + 1, nil
		}
		end = start
	}
	return 0, nil
}

// cut takes the log back to size bytes and syncs the change.
//
// It truncates through a handle opened for writing rather than through the one
// appends go through. Go opens that one for appending only, which on Windows
// leaves it without the right to shorten the file.
func (l *Log) cut(size int64) error {
	file, err := os.OpenFile(l.path(LogName), os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("cut the manifest log back to %d bytes: %w", size, err)
	}
	if err := file.Truncate(size); err != nil {
		file.Close()
		return fmt.Errorf("cut the manifest log back to %d bytes: %w", size, err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync manifest log: %w", err)
	}
	return file.Close()
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
