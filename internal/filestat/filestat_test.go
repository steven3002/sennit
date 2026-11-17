package filestat

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steven3002/sennit/internal/rename"
)

// stat reads what is at path now, nil when nothing is.
func stat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
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

	if !Same(stat(t, path), stat(t, path)) {
		t.Error("nothing there both times reads as a change")
	}
	write(t, path, "the first base")
	if Same(nil, stat(t, path)) {
		t.Error("a file made since reads as the same as nothing")
	}

	was := stat(t, path)
	if !Same(was, stat(t, path)) {
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
	if Same(was, stat(t, path)) {
		t.Error("a file appended to reads as the same")
	}

	// A rename puts another file in place, as a compaction does. One of the
	// same size, written at once, can carry the time the one it replaced was
	// last written at, so it is told apart by being another file, which is
	// asked everywhere but Windows.
	if runtime.GOOS != "windows" {
		was = stat(t, path)
		replace(t, path, "the other base, and more")
		if now := stat(t, path); now.Size() != was.Size() || Same(was, now) {
			t.Errorf("a file of %d bytes renamed over one of %d reads as the same %v, want a change",
				now.Size(), was.Size(), Same(was, now))
		}
	}
	was = stat(t, path)
	replace(t, path, "a longer base, written in its place")
	if Same(was, stat(t, path)) {
		t.Error("a longer file renamed over another reads as the same")
	}

	was = stat(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if Same(was, stat(t, path)) {
		t.Error("a file taken away reads as the same")
	}
}
