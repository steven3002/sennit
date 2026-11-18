package index_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steven3002/sennit/index"
)

// A base another process put in place is seen as a change, though it is as long
// as the one it replaced, was last written at the same moment, and left the
// delta as long as this process last saw it.
//
// A compaction is the one change that can leave both of those as they were: it
// empties the delta, and every vector of one model is as long as every other,
// so a base that holds a record's new vector in place of its old one is as long
// as the base before it. The moment a file was last written is kept to a tick of
// the clock, so a base written within one tick of the one it replaced carries
// its time. On Windows the index was told apart by its size and that time alone,
// so it read as unchanged, and the server went on searching the old vector.
func TestABaseReplacedByOneThatLooksTheSameIsSeen(t *testing.T) {
	dir := t.TempDir()
	mine := openStore(t, dir)
	if _, err := mine.Hydrate(); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	stored := numbered(1)
	if err := mine.Append(stored); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := mine.Fold(); err != nil {
		t.Fatalf("fold: %v", err)
	}
	if mine.Changed() {
		t.Fatal("after its own compaction the index reads as changed")
	}
	path := filepath.Join(dir, index.BaseName)
	was := statOf(t, path)

	// Another process embeds the record again and compacts, which writes a base
	// of its own and renames it over this one.
	other := openStore(t, dir)
	embedded := numbered(1)
	embedded.Vector = numbered(2).Vector
	if err := other.Append(embedded); err != nil {
		t.Fatalf("the other process appends: %v", err)
	}
	if err := other.Fold(); err != nil {
		t.Fatalf("the other process folds: %v", err)
	}
	if now := statOf(t, path); now.Size() != was.Size() {
		t.Fatalf("the other process's base holds %d bytes and the one it replaced %d, so this checks nothing about one of the same size",
			now.Size(), was.Size())
	}
	// Written within the same tick of the clock as the one it replaced.
	if err := os.Chtimes(path, was.ModTime(), was.ModTime()); err != nil {
		t.Fatalf("give the base the time the old one was written at: %v", err)
	}
	if size := statOf(t, filepath.Join(dir, index.DeltaName)).Size(); size != 0 {
		t.Fatalf("the delta holds %d bytes after the other process folded, want it empty, as this process last saw it", size)
	}

	if !mine.Changed() {
		t.Error("a base another process put in place, as long as the one before it and written at the same moment, reads as unchanged")
	}
	var read []index.Entry
	if _, err := mine.Refresh(func(reading index.Reading) error {
		read = reading.Entries
		return nil
	}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(read) != 1 || read[0].Vector[0] != embedded.Vector[0] {
		t.Errorf("the refresh handed over %d vector(s), want the 1 the other process wrote", len(read))
	}
	if mine.Changed() {
		t.Error("after the refresh the index still reads as changed")
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
