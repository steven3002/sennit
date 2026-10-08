package manifest_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/internal/filelock"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/seal"
)

// catalogSealer is the sealer openCatalog uses, so the two read each other's
// files.
func catalogSealer(t *testing.T) *seal.Sealer {
	t.Helper()
	hierarchy, err := keys.Derive(keys.Seed{7})
	if err != nil {
		t.Fatalf("derive keys: %v", err)
	}
	sealer, err := seal.New(hierarchy.Manifest, hierarchy.Content)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	t.Cleanup(sealer.Close)
	return sealer
}

// load opens and replays the catalog in dir, handing back the error a refused
// load ends in. It takes no *testing.T, so a test can run it on a goroutine of
// its own.
func load(dir string, sealer *seal.Sealer) (*manifest.Manifest, error) {
	log, err := manifest.OpenLog(dir, sealer)
	if err != nil {
		return nil, err
	}
	catalog, err := manifest.Load(log)
	if err != nil {
		log.Close()
		return nil, err
	}
	return catalog, nil
}

// loadCatalog is load under the key openCatalog uses, closed when the test ends.
func loadCatalog(t *testing.T, dir string) (*manifest.Manifest, error) {
	t.Helper()
	catalog, err := load(dir, catalogSealer(t))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { catalog.Close() })
	return catalog, nil
}

// threeLines appends three entries to a catalog of its own and returns them,
// with the lines they became on disk, each still ending in its newline.
func threeLines(t *testing.T) ([]manifest.Entry, [][]byte) {
	t.Helper()
	dir := t.TempDir()
	catalog := openCatalog(t, dir)
	written := []manifest.Entry{entry(t, "first"), entry(t, "second"), entry(t, "third")}
	for _, e := range written {
		if err := catalog.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	catalog.Close()

	lines := bytes.SplitAfter(logOf(t, dir), []byte("\n"))
	if last := len(lines) - 1; len(lines[last]) == 0 {
		lines = lines[:last]
	}
	if len(lines) != len(written) {
		t.Fatalf("the log holds %d lines for %d entries", len(lines), len(written))
	}
	return written, lines
}

// joined is a file made of the given pieces, in order.
func joined(pieces ...[]byte) []byte { return bytes.Join(pieces, nil) }

// holding puts a catalog whose log holds exactly log, and whose snapshot holds
// exactly snapshot when it is not nil, into a directory of its own.
func holding(t *testing.T, log, snapshot []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, manifest.LogName), log, 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if snapshot != nil {
		if err := os.WriteFile(filepath.Join(dir, manifest.SnapshotName), snapshot, 0o600); err != nil {
			t.Fatalf("write snapshot: %v", err)
		}
	}
	return dir
}

func logOf(t *testing.T, dir string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, manifest.LogName))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return raw
}

// appendRaw writes bytes onto the end of the log the way another process's
// append does, through a handle of its own.
func appendRaw(t *testing.T, dir string, raw []byte) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(dir, manifest.LogName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer file.Close()
	if _, err := file.Write(raw); err != nil {
		t.Fatalf("write to the log: %v", err)
	}
}

// holds fails the test unless the catalog resolves every one of the entries,
// and no more records than that.
func holds(t *testing.T, catalog *manifest.Manifest, entries ...manifest.Entry) {
	t.Helper()
	if catalog.Len() != len(entries) {
		t.Fatalf("the catalog holds %d records, want %d", catalog.Len(), len(entries))
	}
	for _, want := range entries {
		got, err := catalog.Lookup(want.ID)
		if err != nil {
			t.Fatalf("lookup %s: %v", want.ObjectRef, err)
		}
		if got.ObjectRef != want.ObjectRef {
			t.Fatalf("%s resolves to %q", want.ObjectRef, got.ObjectRef)
		}
	}
}

// dropped fails the test unless the catalog reports exactly want lines dropped.
func dropped(t *testing.T, catalog *manifest.Manifest, want int) {
	t.Helper()
	if got := catalog.Dropped(); got != want {
		t.Fatalf("the catalog reports %d line(s) dropped, want %d", got, want)
	}
}

// sized fails the test unless the catalog counts the log at the size it has on
// disk, which is what the compaction ratio is measured on.
func sized(t *testing.T, catalog *manifest.Manifest, dir string) {
	t.Helper()
	if got, want := catalog.Stats().LogBytes, int64(len(logOf(t, dir))); got != want {
		t.Fatalf("the catalog counts %d bytes of log, and the log holds %d", got, want)
	}
}

// A crash part way through an append leaves the log ending in part of a line.
// That line is the one change that never finished, and every line before it is
// whole, so the line is dropped and the catalog opens without it. The log is cut
// back to the end of the last whole line, because a line appended onto the
// partial one would join it into one line that nothing can read.
func TestALastLineCutPartWayIsDroppedAndTheRestLoads(t *testing.T) {
	written, lines := threeLines(t)
	whole, last := joined(lines[0], lines[1]), lines[2]

	for _, c := range []struct {
		name string
		keep int
		// lost is whether any of the line is left to drop.
		lost bool
	}{
		{"one byte of it reached the disk", 1, true},
		{"forty bytes of it reached the disk", 40, true},
		{"half of it reached the disk", len(last) / 2, true},
		{"its last forty bytes did not reach the disk", len(last) - 40, true},
		{"all but its last character and its newline reached the disk", len(last) - 2, true},
		{"none of it reached the disk", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := holding(t, joined(whole, last[:c.keep]), nil)

			catalog, err := loadCatalog(t, dir)
			if err != nil {
				t.Fatalf("a log whose last line was cut off would not load: %v", err)
			}
			holds(t, catalog, written[0], written[1])
			if _, err := catalog.Lookup(written[2].ID); !errors.Is(err, manifest.ErrNotFound) {
				t.Fatalf("the record whose line was cut off resolves, err %v", err)
			}
			if got := logOf(t, dir); !bytes.Equal(got, whole) {
				t.Fatalf("the log holds %d bytes after the open, want the %d of its whole lines", len(got), len(whole))
			}
			sized(t, catalog, dir)
			if c.lost {
				dropped(t, catalog, 1)
			} else {
				dropped(t, catalog, 0)
			}

			// What is appended next is a line of its own, and survives a reopen.
			fourth := entry(t, "fourth")
			if err := catalog.Append(fourth); err != nil {
				t.Fatalf("append after the cut: %v", err)
			}
			sized(t, catalog, dir)
			catalog.Close()
			reopened, err := loadCatalog(t, dir)
			if err != nil {
				t.Fatalf("reopen after an append: %v", err)
			}
			holds(t, reopened, written[0], written[1], fourth)
			dropped(t, reopened, 0)
		})
	}
}

// A line can reach the disk whole but for its newline. It reads, so it is kept,
// and the next append has to start a line of its own. Written straight onto the
// end of it, the two would join into one line that cannot be read, and once
// another line followed that one it would no longer be the last, and the catalog
// would not open at all.
func TestALineMissingOnlyItsNewlineIsKeptAndTheNextAppendStartsANewLine(t *testing.T) {
	written, lines := threeLines(t)
	all := joined(lines...)
	dir := holding(t, all[:len(all)-1], nil)

	catalog, err := loadCatalog(t, dir)
	if err != nil {
		t.Fatalf("a log missing only its final newline would not load: %v", err)
	}
	holds(t, catalog, written...)
	dropped(t, catalog, 0)
	sized(t, catalog, dir)

	more := []manifest.Entry{entry(t, "fourth"), entry(t, "fifth")}
	for _, e := range more {
		if err := catalog.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	sized(t, catalog, dir)
	catalog.Close()

	reopened, err := loadCatalog(t, dir)
	if err != nil {
		t.Fatalf("the log would not load after appends onto a line missing its newline: %v", err)
	}
	holds(t, reopened, append(append([]manifest.Entry{}, written...), more...)...)
	dropped(t, reopened, 0)
}

// Only the log's last line is taken for an interrupted append. A line that cannot
// be read with another line after it was not cut off by a crash, because the
// append after it went on to finish, so it is damage, and the catalog refuses to
// open over it rather than quietly losing what it held. An empty line holds no
// change, so it neither makes a damaged line the last one nor stops it being
// the last. The snapshot is written whole and renamed into place, so nothing in
// it is an interrupted append either.
func TestOnlyTheFinalLineOfTheLogIsForgiven(t *testing.T) {
	written, lines := threeLines(t)
	// damaged is a line cut short with its newline put back, which no append
	// leaves behind.
	damaged := func(line []byte) []byte {
		return joined(line[:len(line)/2], []byte("\n"))
	}
	empty := []byte("\n")

	for _, c := range []struct {
		name          string
		log, snapshot []byte
		// refused names the line a refused load must report. Empty means the
		// load succeeds.
		refused string
		// holds is what a load that succeeds holds, and left the log it leaves.
		holds []manifest.Entry
		left  []byte
		// dropped is how many lines it drops.
		dropped int
	}{
		{name: "an earlier line damaged",
			log: joined(lines[0], damaged(lines[1]), lines[2]), refused: "manifest.log line 2"},
		{name: "a damaged line with an empty line and then a whole one after it",
			log: joined(lines[0], damaged(lines[1]), empty, lines[2]), refused: "manifest.log line 2"},
		{name: "a damaged last line with an empty line after it",
			log:   joined(lines[0], lines[1], damaged(lines[2]), empty),
			holds: written[:2], left: joined(lines[0], lines[1]), dropped: 1},
		{name: "a last line cut short after an empty line",
			log:   joined(lines[0], lines[1], empty, lines[2][:40]),
			holds: written[:2], left: joined(lines[0], lines[1]), dropped: 1},
		{name: "an empty line after the whole ones",
			log:   joined(lines[0], lines[1], lines[2], empty),
			holds: written, left: joined(lines[0], lines[1], lines[2], empty)},
		{name: "the log's only line cut short",
			log:   lines[0][:40],
			holds: nil, left: []byte{}, dropped: 1},
		{name: "the snapshot's last line cut short",
			snapshot: joined(lines[0], lines[1], lines[2][:40]), log: []byte{}, refused: "manifest.snapshot line 3"},
		{name: "the log's last line cut short over a snapshot",
			snapshot: joined(lines[0], lines[1]), log: lines[2][:40],
			holds: written[:2], left: []byte{}, dropped: 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := holding(t, c.log, c.snapshot)
			catalog, err := loadCatalog(t, dir)
			if c.refused != "" {
				if err == nil {
					t.Fatalf("the catalog opened over damage, holding %d records", catalog.Len())
				}
				if !strings.Contains(err.Error(), c.refused) {
					t.Fatalf("the refusal %q does not name %s", err, c.refused)
				}
				if got := logOf(t, dir); !bytes.Equal(got, c.log) {
					t.Fatalf("a refused load changed the log from %d bytes to %d", len(c.log), len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("a log whose last line was cut off would not load: %v", err)
			}
			holds(t, catalog, c.holds...)
			dropped(t, catalog, c.dropped)
			if got := logOf(t, dir); !bytes.Equal(got, c.left) {
				t.Fatalf("the load left a log of %d bytes, want %d", len(got), len(c.left))
			}
			sized(t, catalog, dir)
		})
	}
}

// Another process can stop part way through an append after this one opened
// the catalog. The next append from this one finds the line it left: one that
// cannot be read is dropped and taken off, exactly as an open would have done,
// and one that reached the disk whole but for its newline is kept and finished.
// Either way the append that follows is a line of its own.
func TestAnAppendSettlesALineAnotherProcessLeftUnfinished(t *testing.T) {
	written, lines := threeLines(t)
	for _, c := range []struct {
		name    string
		left    []byte
		kept    bool
		dropped int
	}{
		{"a line cut part way", lines[2][:40], false, 1},
		{"a line missing only its newline", lines[2][:len(lines[2])-1], true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := holding(t, joined(lines[0], lines[1]), nil)
			catalog, err := loadCatalog(t, dir)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			appendRaw(t, dir, c.left)

			fourth := entry(t, "fourth")
			if err := catalog.Append(fourth); err != nil {
				t.Fatalf("append after another process stopped part way: %v", err)
			}
			dropped(t, catalog, c.dropped)
			catalog.Close()

			reopened, err := loadCatalog(t, dir)
			if err != nil {
				t.Fatalf("the log would not load after an append onto another process's unfinished line: %v", err)
			}
			want := []manifest.Entry{written[0], written[1], fourth}
			if c.kept {
				want = append(want, written[2])
			}
			holds(t, reopened, want...)
			dropped(t, reopened, 0)
		})
	}
}

// anotherProcessHolds takes the catalog's lock in dir the way another process
// does for the length of an append, and returns the function that lets it go.
func anotherProcessHolds(t *testing.T, dir string) func() {
	t.Helper()
	other, err := filelock.Open(filepath.Join(dir, manifest.LockName))
	if err != nil {
		t.Fatalf("open the lock: %v", err)
	}
	t.Cleanup(func() { other.Close() })
	if err := other.Lock(); err != nil {
		t.Fatalf("lock: %v", err)
	}
	return func() {
		if err := other.Unlock(); err != nil {
			t.Fatalf("unlock: %v", err)
		}
	}
}

// waits fails the test if done reports within a fifth of a second, which is
// what it would do if it did not wait for the lock another process holds.
func waits(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s went ahead while another process held the catalog's lock (err %v)", what, err)
	case <-time.After(200 * time.Millisecond):
	}
}

// finishes fails the test unless done reports success within ten seconds.
func finishes(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never finished once the lock was let go", what)
	}
}

// A vault is opened by every command and every agent's server at once, and a
// line another process is part way through writing looks, to one reading the
// log, exactly like a line a crash cut off. Opening the catalog waits until the
// writer has finished, so the line is read whole: nothing is dropped, and
// nothing the writer is still putting down is cut out from under it.
func TestOpeningWaitsForAnotherProcessPartWayThroughAnAppend(t *testing.T) {
	written, lines := threeLines(t)
	whole, last := joined(lines[0], lines[1]), lines[2]
	dir := holding(t, whole, nil)

	release := anotherProcessHolds(t, dir)
	appendRaw(t, dir, last[:len(last)/2])

	sealer := catalogSealer(t)
	var catalog *manifest.Manifest
	opened := make(chan error, 1)
	go func() {
		var err error
		catalog, err = load(dir, sealer)
		opened <- err
	}()
	waits(t, opened, "opening the catalog")

	appendRaw(t, dir, last[len(last)/2:])
	release()
	finishes(t, opened, "opening the catalog")
	defer catalog.Close()

	holds(t, catalog, written...)
	dropped(t, catalog, 0)
	if got := logOf(t, dir); !bytes.Equal(got, joined(lines...)) {
		t.Fatalf("the log holds %d bytes once both are done, want the %d of all three lines", len(got), len(joined(lines...)))
	}
}

// An append waits for another process's append to finish rather than writing
// into the middle of its line.
func TestAnAppendWaitsForAnotherProcessPartWayThroughOne(t *testing.T) {
	written, lines := threeLines(t)
	whole, last := joined(lines[0], lines[1]), lines[2]
	dir := holding(t, whole, nil)
	catalog, err := loadCatalog(t, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	release := anotherProcessHolds(t, dir)
	appendRaw(t, dir, last[:len(last)/2])

	fourth := entry(t, "fourth")
	appended := make(chan error, 1)
	go func() { appended <- catalog.Append(fourth) }()
	waits(t, appended, "the append")

	appendRaw(t, dir, last[len(last)/2:])
	release()
	finishes(t, appended, "the append")
	catalog.Close()

	reopened, err := loadCatalog(t, dir)
	if err != nil {
		t.Fatalf("the log would not load after two processes appended to it: %v", err)
	}
	holds(t, reopened, append(append([]manifest.Entry{}, written...), fourth)...)
	dropped(t, reopened, 0)
}

// Compaction empties the log, so it takes the same lock, and waits for another
// process's append rather than emptying the log part way through its line.
func TestCompactionWaitsForAnotherProcessPartWayThroughAnAppend(t *testing.T) {
	written, lines := threeLines(t)
	dir := holding(t, joined(lines...), nil)
	catalog, err := loadCatalog(t, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	release := anotherProcessHolds(t, dir)
	compacted := make(chan error, 1)
	go func() { compacted <- catalog.Compact() }()
	waits(t, compacted, "compaction")

	release()
	finishes(t, compacted, "compaction")
	catalog.Close()

	reopened, err := loadCatalog(t, dir)
	if err != nil {
		t.Fatalf("reopen after compaction: %v", err)
	}
	holds(t, reopened, written...)
	dropped(t, reopened, 0)
}
