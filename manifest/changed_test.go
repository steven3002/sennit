package manifest_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
)

// A snapshot another process put in place is seen as a change, though it is as
// long as the one it replaced, was last written at the same moment, and left the
// log as long as this process last saw it.
//
// A compaction is the one change that can leave both of those as they were: it
// empties the log, and the snapshot it writes holds the same number of entries
// whenever it only rewrites one, as a repack does. The moment a file was last
// written is kept to a tick of the clock, so a snapshot written within one tick
// of the one it replaced carries its time. On Windows the catalog was told apart
// by its size and that time alone, so it read as unchanged, and the server went
// on resolving the record to an object the other process had moved it from.
func TestASnapshotReplacedByOneThatLooksTheSameIsSeen(t *testing.T) {
	dir := t.TempDir()
	mine := openCatalog(t, dir)
	stored := repacked(t, "object-qz")
	if err := mine.Append(stored); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := mine.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if mine.Changed() {
		t.Fatal("after its own compaction the catalog reads as changed")
	}
	path := filepath.Join(dir, manifest.SnapshotName)
	was := statOf(t, path)

	// Another process repacks the record and compacts, which writes a snapshot
	// of its own and renames it over this one.
	other := openCatalog(t, dir)
	moved := repacked(t, "object-zq")
	if err := other.Append(moved); err != nil {
		t.Fatalf("the other process appends: %v", err)
	}
	if err := other.Compact(); err != nil {
		t.Fatalf("the other process compacts: %v", err)
	}
	if now := statOf(t, path); now.Size() != was.Size() {
		t.Fatalf("the other process's snapshot holds %d bytes and the one it replaced %d, so this checks nothing about one of the same size",
			now.Size(), was.Size())
	}
	// Written within the same tick of the clock as the one it replaced.
	if err := os.Chtimes(path, was.ModTime(), was.ModTime()); err != nil {
		t.Fatalf("give the snapshot the time the old one was written at: %v", err)
	}
	if size := statOf(t, filepath.Join(dir, manifest.LogName)).Size(); size != 0 {
		t.Fatalf("the log holds %d bytes after the other process compacted, want it empty, as this process last saw it", size)
	}

	if !mine.Changed() {
		t.Error("a snapshot another process put in place, as long as the one before it and written at the same moment, reads as unchanged")
	}
	if _, err := mine.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got, err := mine.Lookup(stored.ID); err != nil || got.ObjectRef != moved.ObjectRef {
		t.Errorf("after a refresh the record resolves to %q (%v), want %q, where the other process moved it",
			got.ObjectRef, err, moved.ObjectRef)
	}
	if mine.Changed() {
		t.Error("after the refresh the catalog still reads as changed")
	}
}

// repacked is the entry of one record, the same every run, at the object ref
// given. A repack moves a record to another object and changes nothing else
// about its entry, so two of these differ only in where they say it is.
//
// A line is compressed before it is sealed, so two refs of the same length can
// still seal to lines of different lengths. Refs made of the same letters in
// another order, letters found nowhere else in the entry, compress to the same
// length, since nothing in the entry repeats them.
func repacked(t *testing.T, ref string) manifest.Entry {
	t.Helper()
	id, err := record.ParseID("5e6e17000000000000000000000000a1")
	if err != nil {
		t.Fatalf("record id: %v", err)
	}
	return manifest.Entry{
		ID:        id,
		Kind:      record.KindMemory,
		Type:      record.TypeFact,
		Version:   1,
		ObjectRef: ref,
		SlabID:    "slab",
		Bytes:     857,
		WrittenAt: record.At(time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)),
	}
}

// statOf reads what is at path now.
func statOf(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}
