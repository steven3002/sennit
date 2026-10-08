package index_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/internal/filelock"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
)

// deltaSealer is the vault's own authenticated cipher. The question here is
// what a record cut short does, and only a cipher that checks what it opens can
// tell a cut record from a whole one.
func deltaSealer(t *testing.T) *seal.Sealer {
	t.Helper()
	hierarchy, err := keys.Derive(keys.Seed{9})
	if err != nil {
		t.Fatalf("derive keys: %v", err)
	}
	sealer, err := seal.New(hierarchy.Record, hierarchy.Content)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	t.Cleanup(sealer.Close)
	return sealer
}

// sealedStore opens a store in dir under deltaSealer, closed when the test ends.
func sealedStore(t *testing.T, dir string) *index.Store {
	t.Helper()
	store, err := index.OpenStore(dir, deltaSealer(t))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// hydrated is the ids a store hydrates, and fails the test if it cannot.
func hydrated(t *testing.T, store *index.Store) map[record.ID]bool {
	t.Helper()
	entries, err := store.Hydrate()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	out := make(map[record.ID]bool, len(entries))
	for _, e := range entries {
		out[e.ID] = true
	}
	return out
}

func deltaOf(t *testing.T, dir string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, index.DeltaName))
	if err != nil {
		t.Fatalf("read delta: %v", err)
	}
	return raw
}

// records splits a delta into its records, each with its length prefix.
func records(t *testing.T, raw []byte) [][]byte {
	t.Helper()
	var out [][]byte
	for at := 0; at < len(raw); {
		size := int(binary.LittleEndian.Uint32(raw[at:]))
		out = append(out, raw[at:at+4+size])
		at += 4 + size
	}
	return out
}

// threeRecords appends three vectors to a store of its own and returns them,
// with the records they became in its delta.
func threeRecords(t *testing.T) ([]index.Entry, [][]byte) {
	t.Helper()
	dir := t.TempDir()
	store := sealedStore(t, dir)
	written := []index.Entry{entry(t, 1, 8), entry(t, 2, 8), entry(t, 3, 8)}
	if err := store.Append(written...); err != nil {
		t.Fatalf("append: %v", err)
	}
	store.Close()
	return written, records(t, deltaOf(t, dir))
}

// withDelta puts a store whose delta holds exactly raw into a directory of its
// own.
func withDelta(t *testing.T, raw []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, index.DeltaName), raw, 0o600); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	return dir
}

// appendRaw writes bytes onto the end of the delta the way another process's
// append does, through a handle of its own.
func appendRaw(t *testing.T, dir string, raw []byte) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(dir, index.DeltaName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open delta: %v", err)
	}
	defer file.Close()
	if _, err := file.Write(raw); err != nil {
		t.Fatalf("write to the delta: %v", err)
	}
}

// garbled is a copy of a record with one byte of its sealed body changed, which
// keeps its framing whole and stops it opening.
func garbled(rec []byte) []byte {
	out := bytes.Clone(rec)
	out[len(out)/2] ^= 0xff
	return out
}

// A crash part way through a delta write leaves a record cut short at the end
// of the delta. Losing that one vector is the cost of the crash, but every
// vector written after it is a different matter: appended onto the cut record,
// the replay reads them as the rest of it, fails to open the result, and stops,
// so they were lost too, at the next open and for good at the next compaction.
// The cut record is taken off when the store opens, so the next append starts a
// record of its own.
func TestAVectorWrittenAfterACutOffRecordSurvivesAReopen(t *testing.T) {
	dir := t.TempDir()
	first := sealedStore(t, dir)
	if err := first.Append(entry(t, 1, 8), entry(t, 2, 8), entry(t, 3, 8)); err != nil {
		t.Fatalf("append: %v", err)
	}
	first.Close()

	raw := deltaOf(t, dir)
	if err := os.WriteFile(filepath.Join(dir, index.DeltaName), raw[:len(raw)-12], 0o600); err != nil {
		t.Fatalf("cut delta: %v", err)
	}

	reopened := sealedStore(t, dir)
	if got := hydrated(t, reopened); len(got) != 2 {
		t.Fatalf("hydrated %d vectors from a delta whose last record was cut off, want the 2 before it", len(got))
	}
	later := entry(t, 4, 8)
	if err := reopened.Append(later); err != nil {
		t.Fatalf("append after the cut: %v", err)
	}
	reopened.Close()

	got := hydrated(t, sealedStore(t, dir))
	if !got[later.ID] {
		t.Fatalf("a vector written after a cut-off record was lost on the next open: hydrated %d", len(got))
	}
	if len(got) != 3 {
		t.Fatalf("hydrated %d vectors, want the 2 before the cut and the 1 after it", len(got))
	}
}

// Only the delta's last record is taken for an interrupted append, wherever in
// it the cut fell. A record that does not open with a whole one after it was not
// cut off by a crash, and it is left where it is: the replay stops at it, as it
// always has, and nothing is cut from the file.
func TestOnlyARecordCutOffAtTheEndOfTheDeltaIsTakenOff(t *testing.T) {
	written, recs := threeRecords(t)
	whole := bytes.Join(recs[:2], nil)
	last := recs[2]

	for _, c := range []struct {
		name  string
		delta []byte
		// left is what opening leaves of the delta, and hydrates how many
		// vectors it hydrates.
		left     []byte
		hydrates int
	}{
		{"cut inside its length", joined(whole, last[:2]), whole, 2},
		{"cut just after its length", joined(whole, last[:4]), whole, 2},
		{"cut half way through its body", joined(whole, last[:len(last)/2]), whole, 2},
		{"all there in length but not as written", joined(whole, garbled(last)), whole, 2},
		{"a record that does not open with a whole one after it",
			joined(recs[0], garbled(recs[1]), last), joined(recs[0], garbled(recs[1]), last), 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := withDelta(t, c.delta)
			store := sealedStore(t, dir)
			if got := deltaOf(t, dir); !bytes.Equal(got, c.left) {
				t.Fatalf("opening left a delta of %d bytes, want %d", len(got), len(c.left))
			}
			if got, want := store.Stats().DeltaBytes, int64(len(c.left)); got != want {
				t.Fatalf("the store counts %d bytes of delta, and the delta holds %d", got, want)
			}
			got := hydrated(t, store)
			if len(got) != c.hydrates {
				t.Fatalf("hydrated %d vectors, want %d", len(got), c.hydrates)
			}
			for _, e := range written[:c.hydrates] {
				if !got[e.ID] {
					t.Fatalf("a whole record before the damage did not hydrate")
				}
			}
		})
	}
}

// Another process can stop part way through a delta write after this store
// opened. The next append from this store finds the record it left, takes it
// off, and starts a record of its own.
func TestAnAppendTakesOffARecordAnotherProcessLeftCutOff(t *testing.T) {
	written, recs := threeRecords(t)
	dir := withDelta(t, bytes.Join(recs[:2], nil))
	store := sealedStore(t, dir)

	appendRaw(t, dir, recs[2][:len(recs[2])/2])

	later := entry(t, 4, 8)
	if err := store.Append(later); err != nil {
		t.Fatalf("append after another process stopped part way: %v", err)
	}
	if got, want := store.Stats().DeltaBytes, int64(len(deltaOf(t, dir))); got != want {
		t.Fatalf("the store counts %d bytes of delta, and the delta holds %d", got, want)
	}
	store.Close()

	got := hydrated(t, sealedStore(t, dir))
	for _, e := range []index.Entry{written[0], written[1], later} {
		if !got[e.ID] {
			t.Fatalf("hydrated %d vectors, and not every whole one", len(got))
		}
	}
	if len(got) != 3 {
		t.Fatalf("hydrated %d vectors, want 3", len(got))
	}
}

// anotherProcessHolds takes the index's lock in dir the way another process
// does for the length of an append, and returns the function that lets it go.
func anotherProcessHolds(t *testing.T, dir string) func() {
	t.Helper()
	other, err := filelock.Open(filepath.Join(dir, index.LockName))
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
		t.Fatalf("%s went ahead while another process held the index's lock (err %v)", what, err)
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

// A record another process is part way through writing looks, to a store
// opening the delta, exactly like one a crash cut off. Opening waits until the
// writer has finished, so the record is read whole and nothing is cut.
func TestOpeningTheIndexWaitsForAnotherProcessPartWayThroughAnAppend(t *testing.T) {
	written, recs := threeRecords(t)
	dir := withDelta(t, bytes.Join(recs[:2], nil))

	release := anotherProcessHolds(t, dir)
	appendRaw(t, dir, recs[2][:len(recs[2])/2])

	sealer := deltaSealer(t)
	var store *index.Store
	opened := make(chan error, 1)
	go func() {
		var err error
		store, err = index.OpenStore(dir, sealer)
		opened <- err
	}()
	waits(t, opened, "opening the index")

	appendRaw(t, dir, recs[2][len(recs[2])/2:])
	release()
	finishes(t, opened, "opening the index")
	defer store.Close()

	got := hydrated(t, store)
	for _, e := range written {
		if !got[e.ID] {
			t.Fatalf("hydrated %d vectors once both were done, want all 3", len(got))
		}
	}
}

// An append waits for another process's append to finish rather than writing
// into the middle of its record.
func TestAnIndexAppendWaitsForAnotherProcessPartWayThroughOne(t *testing.T) {
	written, recs := threeRecords(t)
	dir := withDelta(t, bytes.Join(recs[:2], nil))
	store := sealedStore(t, dir)

	release := anotherProcessHolds(t, dir)
	appendRaw(t, dir, recs[2][:len(recs[2])/2])

	later := entry(t, 4, 8)
	appended := make(chan error, 1)
	go func() { appended <- store.Append(later) }()
	waits(t, appended, "the append")

	appendRaw(t, dir, recs[2][len(recs[2])/2:])
	release()
	finishes(t, appended, "the append")
	store.Close()

	got := hydrated(t, sealedStore(t, dir))
	for _, e := range append(written, later) {
		if !got[e.ID] {
			t.Fatalf("hydrated %d vectors once both were done, want all 4", len(got))
		}
	}
}

// Compaction empties the delta, so it takes the same lock before it does, and
// waits for another process's append rather than emptying the delta part way
// through its record.
func TestEmptyingTheDeltaWaitsForAnotherProcessPartWayThroughAnAppend(t *testing.T) {
	written, recs := threeRecords(t)
	dir := withDelta(t, bytes.Join(recs, nil))
	store := sealedStore(t, dir)
	entries, err := store.Hydrate()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}

	release := anotherProcessHolds(t, dir)
	compacted := make(chan error, 1)
	go func() { compacted <- store.Compact(entries) }()
	waits(t, compacted, "compaction")

	release()
	finishes(t, compacted, "compaction")
	store.Close()

	got := hydrated(t, sealedStore(t, dir))
	for _, e := range written {
		if !got[e.ID] {
			t.Fatalf("hydrated %d vectors after the compaction, want all 3", len(got))
		}
	}
}

// joined is a file made of the given pieces, in order.
func joined(pieces ...[]byte) []byte { return bytes.Join(pieces, nil) }
