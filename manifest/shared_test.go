package manifest_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/steven3002/sennit/manifest"
)

// A vault is opened by every command and by every agent's server, so one
// catalog has several processes appending to its log at once, each holding the
// copy of the catalog it read when it opened. A second catalog over the same
// directory is what a second process is to the files.

// A compaction keeps what another process has appended since this one opened
// the catalog.
//
// The snapshot was written from the compacting process's own copy of the
// catalog, and the log was then emptied. Whatever another process had appended
// since that copy was read was in the log and not in the copy, so it was in
// neither file afterwards: the records were still stored, and no later process
// could find them in the catalog. A removal another process had made was undone
// the same way, and the forgotten record was listed again.
func TestACompactionKeepsWhatAnotherProcessAppended(t *testing.T) {
	dir := t.TempDir()
	compacting := openCatalog(t, dir)
	mine, forgotten := entry(t, "mine"), entry(t, "forgotten")
	for _, e := range []manifest.Entry{mine, forgotten} {
		if err := compacting.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	other := openCatalog(t, dir)
	theirs := []manifest.Entry{entry(t, "theirs-1"), entry(t, "theirs-2"), entry(t, "theirs-3")}
	for _, e := range theirs {
		if err := other.Append(e); err != nil {
			t.Fatalf("the other process appends: %v", err)
		}
	}
	if err := other.Remove(forgotten.ID); err != nil {
		t.Fatalf("the other process removes: %v", err)
	}
	other.Close()

	if err := compacting.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	compacting.Close()

	reopened := openCatalog(t, dir)
	holds(t, reopened, append([]manifest.Entry{mine}, theirs...)...)
	if _, err := reopened.Lookup(forgotten.ID); err == nil {
		t.Error("a record another process removed is back in the catalog after this one compacted")
	}
}

// Processes appending and compacting at the same time lose nothing between
// them, however their appends fall around each other's compactions.
func TestProcessesAppendingAndCompactingAtOnceLoseNothing(t *testing.T) {
	dir := t.TempDir()
	const processes, each = 3, 30

	written := make([][]manifest.Entry, processes)
	var busy sync.WaitGroup
	for p := range processes {
		catalog := openCatalog(t, dir)
		for range each {
			written[p] = append(written[p], entry(t, "shared"))
		}
		busy.Add(1)
		go func() {
			defer busy.Done()
			for n, e := range written[p] {
				if err := catalog.Append(e); err != nil {
					t.Errorf("process %d appends: %v", p+1, err)
					return
				}
				if n%5 == 4 {
					if err := catalog.Compact(); err != nil {
						t.Errorf("process %d compacts: %v", p+1, err)
						return
					}
				}
			}
		}()
	}
	busy.Wait()

	var all []manifest.Entry
	for _, entries := range written {
		all = append(all, entries...)
	}
	holds(t, openCatalog(t, dir), all...)
}

// Something that is not a vault can have the snapshot open at the moment a
// compaction replaces it: a virus scanner, a search indexer, a backup. It reads
// for a moment and lets go, and the compaction goes through once it has. On
// Windows a file that is open cannot be renamed over, so a compaction that took
// the first refusal for an answer failed the write that had set it off.
func TestACompactionReplacesASnapshotSomethingElseIsReading(t *testing.T) {
	written, lines := threeLines(t)
	dir := holding(t, joined(lines[2]), joined(lines[0], lines[1]))
	catalog, err := loadCatalog(t, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	reading, err := os.Open(filepath.Join(dir, manifest.SnapshotName))
	if err != nil {
		t.Fatalf("open the snapshot as another reader would: %v", err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(300 * time.Millisecond)
		reading.Close()
	}()

	err = catalog.Compact()
	<-released
	if err != nil {
		t.Fatalf("compact while something else had the snapshot open for a moment: %v", err)
	}
	catalog.Close()

	reopened, err := loadCatalog(t, dir)
	if err != nil {
		t.Fatalf("reopen after the compaction: %v", err)
	}
	holds(t, reopened, written...)
	if stats := reopened.Stats(); stats.LogBytes != 0 {
		t.Errorf("the log holds %d bytes after the compaction, want it emptied into the snapshot", stats.LogBytes)
	}
}
