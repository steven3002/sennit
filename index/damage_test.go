package index_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/steven3002/sennit/index"
)

// told is what a store's watcher has been told, in order.
type told struct {
	mu      sync.Mutex
	damages []index.Damage
}

func (w *told) all() []index.Damage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]index.Damage(nil), w.damages...)
}

// watchedStore opens a store over dir under deltaSealer with a watcher that
// records what it is told, and calls also, when it is set, while the store
// still holds the lock.
func watchedStore(t *testing.T, dir string, also func(index.Damage) error) (*index.Store, *told) {
	t.Helper()
	w := &told{}
	store, err := index.OpenStoreWatched(dir, deltaSealer(t), func(d index.Damage) error {
		w.mu.Lock()
		w.damages = append(w.damages, d)
		w.mu.Unlock()
		if also != nil {
			return also(d)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, w
}

// withBase writes raw into dir as the index's base.
func withBase(t *testing.T, dir string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, index.BaseName), raw, 0o600); err != nil {
		t.Fatalf("write base: %v", err)
	}
}

func fileOf(t *testing.T, dir, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return raw
}

// misframed is a copy of a record whose length claims more than any file holds,
// which keeps its sealed body as written and loses where the next record starts.
func misframed(rec []byte) []byte {
	out := bytes.Clone(rec)
	binary.LittleEndian.PutUint32(out, 0x7fffffff)
	return out
}

// A record whose length is whole and whose contents do not open was the end of
// the read: every vector after it was dropped without a word, and the next
// compaction wrote the new base without them. The length still says where the
// next record starts, so the read steps over it, in the delta and in the base
// alike, and says what it stepped over.
func TestAReadStepsOverADamagedRecordInTheMiddleOfEitherFile(t *testing.T) {
	written, recs := threeRecords(t)
	damaged := joined(recs[0], garbled(recs[1]), recs[2])

	for _, file := range []string{index.DeltaName, index.BaseName} {
		t.Run(file, func(t *testing.T) {
			dir := t.TempDir()
			if file == index.DeltaName {
				dir = withDelta(t, damaged)
			} else {
				withBase(t, dir, damaged)
			}
			store, watcher := watchedStore(t, dir, nil)

			got := hydrated(t, store)
			if !got[written[0].ID] || !got[written[2].ID] {
				t.Errorf("hydrated %d vector(s) from a %s damaged in the middle, and not both whole ones", len(got), file)
			}
			if got[written[1].ID] {
				t.Errorf("the damaged record hydrated")
			}
			if said := watcher.all(); len(said) != 1 || said[0] != (index.Damage{Skipped: 1}) {
				t.Errorf("the watcher was told %+v, want one record stepped over", said)
			}
			if left := fileOf(t, dir, file); !bytes.Equal(left, damaged) {
				t.Errorf("reading changed the %s from %d bytes to %d", file, len(damaged), len(left))
			}
		})
	}
}

// A length that cannot be true says nothing about where the next record
// starts, so the rest of that file is lost to the read. It is told, and how much
// of the file it took with it.
func TestALengthThatCannotBeTrueLosesTheRestOfTheBaseAndIsTold(t *testing.T) {
	written, recs := threeRecords(t)
	dir := t.TempDir()
	withBase(t, dir, joined(recs[0], misframed(recs[1]), recs[2]))
	store, watcher := watchedStore(t, dir, nil)

	got := hydrated(t, store)
	if len(got) != 1 || !got[written[0].ID] {
		t.Errorf("hydrated %d vector(s), want only the one before the damaged length", len(got))
	}
	want := index.Damage{Lost: int64(len(recs[1]) + len(recs[2]))}
	if said := watcher.all(); len(said) != 1 || said[0] != want {
		t.Errorf("the watcher was told %+v, want %+v", said, want)
	}
}

// A length damaged in the middle of the delta stops its framing, and the store
// cuts the delta back there as it opens, as it does for an append a crash cut
// off. The records after it go with the cut, so it is told, and told while they
// are still on the disk: once the cut is made nothing shows they were there.
func TestALengthDamagedInTheMiddleOfTheDeltaIsToldBeforeItIsCut(t *testing.T) {
	written, recs := threeRecords(t)
	delta := joined(recs[0], misframed(recs[1]), recs[2])
	dir := withDelta(t, delta)

	var sizeWhenTold int
	store, watcher := watchedStore(t, dir, func(index.Damage) error {
		sizeWhenTold = len(fileOf(t, dir, index.DeltaName))
		return nil
	})

	want := index.Damage{Lost: int64(len(recs[1]) + len(recs[2]))}
	if said := watcher.all(); len(said) != 1 || said[0] != want {
		t.Fatalf("opening told the watcher %+v, want %+v", said, want)
	}
	if sizeWhenTold != len(delta) {
		t.Errorf("the watcher was told once the delta was %d bytes, want the whole %d still there", sizeWhenTold, len(delta))
	}
	if left := deltaOf(t, dir); !bytes.Equal(left, recs[0]) {
		t.Errorf("opening left %d bytes of delta, want the %d of the record before the damage", len(left), len(recs[0]))
	}
	if got := hydrated(t, store); len(got) != 1 || !got[written[0].ID] {
		t.Errorf("hydrated %d vector(s) after the cut, want the one before it", len(got))
	}
}

// A process stopped part way through an append leaves the record it was writing
// short or unreadable at the end of the delta, and nothing after it. That is the
// cost of the interruption, cut off as the store opens and as an append finds
// it, and it is not damage: nothing is told.
func TestAnAppendCutOffAtTheEndOfTheDeltaIsNotDamage(t *testing.T) {
	_, recs := threeRecords(t)
	whole, last := joined(recs[0], recs[1]), recs[2]

	for _, c := range []struct {
		name  string
		delta []byte
	}{
		{"cut inside its length", joined(whole, last[:2])},
		{"cut just after its length", joined(whole, last[:4])},
		{"cut half way through its body", joined(whole, last[:len(last)/2])},
		{"all there in length but not as written", joined(whole, garbled(last))},
		{"the only record, cut half way", last[:len(last)/2]},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := withDelta(t, c.delta)
			store, watcher := watchedStore(t, dir, nil)
			hydrated(t, store)
			if err := store.Fold(); err != nil {
				t.Fatalf("fold: %v", err)
			}
			if said := watcher.all(); len(said) != 0 {
				t.Errorf("an interrupted append was told as damage: %+v", said)
			}
		})
	}

	// A record whose length landed and whose contents did not is the other thing
	// a stopped append leaves. Left by another process after this store opened,
	// it is met by a read before any append settles it, and a read does not take
	// it for damage either.
	t.Run("whole in length but unreadable, left by another process after the store opened", func(t *testing.T) {
		dir := withDelta(t, whole)
		store, watcher := watchedStore(t, dir, nil)
		appendRaw(t, dir, garbled(last))
		if got := hydrated(t, store); len(got) != 2 {
			t.Errorf("hydrated %d vector(s), want the 2 whole ones before the unreadable record", len(got))
		}
		if err := store.Fold(); err != nil {
			t.Fatalf("fold: %v", err)
		}
		if said := watcher.all(); len(said) != 0 {
			t.Errorf("an interrupted append was told as damage: %+v", said)
		}
	})

	// Left by another process after this store opened, the cut record is met
	// first by the store's next read or append rather than by its open.
	t.Run("left by another process after the store opened", func(t *testing.T) {
		dir := withDelta(t, whole)
		store, watcher := watchedStore(t, dir, nil)
		appendRaw(t, dir, last[:len(last)/2])
		hydrated(t, store)
		if err := store.Append(entry(t, 9, 8)); err != nil {
			t.Fatalf("append: %v", err)
		}
		if said := watcher.all(); len(said) != 0 {
			t.Errorf("an interrupted append was told as damage: %+v", said)
		}
	})
}

// A compaction writes its new base from what it read and empties the delta, so
// the damage it read past is off the disk once it is done. It is told before
// that, while the damaged record is still in the file, and a read after the
// compaction has nothing to tell.
func TestDamageIsToldBeforeACompactionTakesItOffTheDisk(t *testing.T) {
	written, recs := threeRecords(t)
	damaged := joined(recs[0], garbled(recs[1]), recs[2])
	dir := t.TempDir()
	withBase(t, dir, damaged)

	var baseWhenTold []byte
	store, watcher := watchedStore(t, dir, func(index.Damage) error {
		baseWhenTold = fileOf(t, dir, index.BaseName)
		return nil
	})
	if err := store.Fold(); err != nil {
		t.Fatalf("fold: %v", err)
	}
	if said := watcher.all(); len(said) != 1 {
		t.Fatalf("the compaction told the watcher %+v, want the one damaged record", said)
	}
	if !bytes.Equal(baseWhenTold, damaged) {
		t.Errorf("the watcher was told once the base had been replaced")
	}

	got := hydrated(t, store)
	if len(got) != 2 || !got[written[0].ID] || !got[written[2].ID] {
		t.Errorf("hydrated %d vector(s) after the compaction, want the 2 whole ones", len(got))
	}
	if said := watcher.all(); len(said) != 1 {
		t.Errorf("a read after the compaction told the watcher again: %+v", said)
	}
}

// A watcher that cannot record what it was told stops whatever the read was
// for, and a compaction that stops leaves the damage where it was, so that the
// next read meets it and tells it again.
func TestDamageAWatcherCouldNotRecordStaysOnTheDisk(t *testing.T) {
	_, recs := threeRecords(t)
	damaged := joined(recs[0], garbled(recs[1]), recs[2])
	dir := t.TempDir()
	withBase(t, dir, damaged)

	refused := errors.New("the device's store is full")
	store, _ := watchedStore(t, dir, func(index.Damage) error { return refused })
	if err := store.Fold(); !errors.Is(err, refused) {
		t.Fatalf("a compaction whose watcher failed returned %v, want its error", err)
	}
	if left := fileOf(t, dir, index.BaseName); !bytes.Equal(left, damaged) {
		t.Errorf("a compaction whose watcher failed replaced the base anyway")
	}
}

// An index nothing has damaged tells its watcher nothing, whatever is done to
// it.
func TestAnUndamagedIndexTellsNothing(t *testing.T) {
	dir := t.TempDir()
	store, watcher := watchedStore(t, dir, nil)
	if err := store.Append(entry(t, 1, 8), entry(t, 2, 8)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Remove(entry(t, 1, 8).ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := store.Fold(); err != nil {
		t.Fatalf("fold: %v", err)
	}
	if err := store.Append(entry(t, 3, 8)); err != nil {
		t.Fatalf("append: %v", err)
	}
	hydrated(t, store)
	if said := watcher.all(); len(said) != 0 {
		t.Errorf("an undamaged index told its watcher %+v", said)
	}
}

// countingSealer opens nothing, and keeps count of the bytes it was asked to
// open.
type countingSealer struct{ opened atomic.Int64 }

func (c *countingSealer) Seal(plaintext []byte) ([]byte, error) { return plaintext, nil }

func (c *countingSealer) Open(sealed []byte) ([]byte, error) {
	c.opened.Add(int64(len(sealed)))
	return nil, errors.New("does not open")
}

// Past a length that cannot be true, the store looks for a whole record at
// every offset, which is what tells damage from an interrupted append. A length
// read at an offset of random bytes can claim nearly anything, and trying to open
// whatever fit in the rest of the file made the bytes opened grow with the square
// of the tail: tens of megabytes of delta cost terabytes of decryption at one
// open. No entry is longer than the format can encode, so a length that claims
// more is passed over without opening anything, and the work grows with the
// tail alone.
func TestTheSearchPastADamagedLengthOpensNoMoreThanTheLongestEntryAllows(t *testing.T) {
	tail := make([]byte, 4<<20)
	rand.NewChaCha8([32]byte{82}).Read(tail)
	binary.LittleEndian.PutUint32(tail, 0xffffffff)

	sealer := &countingSealer{}
	store, err := index.OpenStore(withDelta(t, tail), sealer)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if opened, limit := sealer.opened.Load(), 32*int64(len(tail)); opened > limit {
		t.Errorf("looking for a whole record in %d bytes past a damaged length opened %d bytes, more than %d",
			len(tail), opened, limit)
	}
}

// The bound passes over no entry the format can encode. One with the longest
// model name and the widest vector a length can hold, sealed by the vault's own
// sealer, reads back from a base, and is found past a damaged length in the
// delta.
func TestAnEntryAsLongAsTheFormatAllowsIsReadAndFound(t *testing.T) {
	random := rand.NewChaCha8([32]byte{9})
	name := make([]byte, math.MaxUint16)
	random.Read(name)
	vector := make([]float32, math.MaxUint16)
	for i := range vector {
		vector[i] = math.Float32frombits(uint32(random.Uint64()))
	}
	longest := index.Entry{ID: entry(t, 7, 1).ID, Model: string(name), Dim: len(vector), Vector: vector}

	scratch := t.TempDir()
	writer := sealedStore(t, scratch)
	if err := writer.Append(longest); err != nil {
		t.Fatalf("append the longest entry: %v", err)
	}
	writer.Close()
	sealed := deltaOf(t, scratch)

	inBase := t.TempDir()
	withBase(t, inBase, sealed)
	if got := hydrated(t, sealedStore(t, inBase)); !got[longest.ID] {
		t.Errorf("an entry of %d sealed bytes, as long as the format allows, did not read", len(sealed)-4)
	}

	_, recs := threeRecords(t)
	_, watcher := watchedStore(t, withDelta(t, joined(misframed(recs[0]), sealed)), nil)
	if said := watcher.all(); len(said) != 1 {
		t.Errorf("the longest entry past a damaged length was not found, so the cut was not told: %+v", said)
	}
}

// A read says which records its latest entry withdraws, as well as which it
// holds, so a caller can tell a record being forgotten from one that has lost
// its vector.
func TestAReadNamesTheRecordsARemovalWithdrew(t *testing.T) {
	dir := t.TempDir()
	store, _ := watchedStore(t, dir, nil)
	kept, gone := entry(t, 1, 8), entry(t, 2, 8)
	if err := store.Append(kept, gone); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Remove(gone.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	var reading index.Reading
	if err := store.Read(func(r index.Reading) error {
		reading = r
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(reading.Entries) != 1 || reading.Entries[0].ID != kept.ID {
		t.Errorf("the read holds %d vector(s), want only the one not removed", len(reading.Entries))
	}
	if !reading.Removed[gone.ID] || reading.Removed[kept.ID] {
		t.Errorf("the read names %v as removed, want only the removed record", reading.Removed)
	}
	if reading.Damage.Found() {
		t.Errorf("an undamaged read reports damage: %+v", reading.Damage)
	}
}
