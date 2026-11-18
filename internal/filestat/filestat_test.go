package filestat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steven3002/sennit/internal/rename"
)

// lookAt reads what is at path now, nil when nothing is.
func lookAt(t *testing.T, path string) *Info {
	t.Helper()
	info, err := Look(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("look at %s: %v", path, err)
	}
	return info
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// replace puts a new file holding text at path, written beside it and renamed
// over it, as a compaction installs a snapshot or a base.
func replace(t *testing.T, path, text string) {
	t.Helper()
	pending := path + ".tmp"
	write(t, pending, text)
	if err := rename.Over(pending, path); err != nil {
		t.Fatalf("rename over %s: %v", path, err)
	}
}

// Same tells a file that was left alone from one that was written to, put in
// place by a rename, made, or taken away, each the way a vault's files change.
func TestSameTellsAFileLeftAloneFromOneThatChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "base")

	if !Same(lookAt(t, path), lookAt(t, path)) {
		t.Error("nothing there both times reads as a change")
	}
	write(t, path, "the first base")
	if Same(nil, lookAt(t, path)) {
		t.Error("a file made since reads as the same as nothing")
	}

	was := lookAt(t, path)
	if !Same(was, lookAt(t, path)) {
		t.Error("a file left alone reads as changed")
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open for appending: %v", err)
	}
	if _, err := file.Write([]byte(", and more")); err != nil {
		t.Fatalf("append: %v", err)
	}
	file.Close()
	if Same(was, lookAt(t, path)) {
		t.Error("a file appended to reads as the same")
	}

	was = lookAt(t, path)
	replace(t, path, "a longer base, written in its place")
	if Same(was, lookAt(t, path)) {
		t.Error("a longer file renamed over another reads as the same")
	}

	was = lookAt(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if Same(was, lookAt(t, path)) {
		t.Error("a file taken away reads as the same")
	}
}

// A file renamed over another of the same size, last written at the same
// moment, is still told apart, because it is another file.
//
// That is the replacement a compaction can make: a snapshot that holds a record
// at a new place in place of its old one is as long as the snapshot before it,
// and one written within a tick of the clock of the one it replaces carries the
// same moment. The moment is set here rather than waited for, so the test holds
// on every system and every run.
func TestSameTellsAFileOfTheSameSizeAndMomentRenamedIntoPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot")
	write(t, path, "the record is in object-qz")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	was := lookAt(t, path)

	replace(t, path, "the record is in object-zq")
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatalf("give the new file the old one's moment: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("the replacement is %d bytes at %v and the file it replaced %d bytes at %v, so this checks nothing",
			after.Size(), after.ModTime(), before.Size(), before.ModTime())
	}

	if Same(was, lookAt(t, path)) {
		t.Error("a file of the same size and moment renamed into place reads as the same file")
	}
}

// A look at a path with nothing there says so in the way callers test for.
func TestLookAtNothingIsNotExist(t *testing.T) {
	_, err := Look(filepath.Join(t.TempDir(), "nothing"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a look at nothing returned %v, want an error that is os.ErrNotExist", err)
	}
}
