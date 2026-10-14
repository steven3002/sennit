package index_test

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/record"
)

// A vault is opened by every command and by every agent's server, so one index
// directory has several stores over it at once, each in a process of its own. A
// second store over the same directory is what a second process is to the
// files: its own handles, and its own hold of the lock.

// numbered is a vector under an id no other number gives, at the width the
// real model writes.
func numbered(n int) index.Entry {
	var id record.ID
	id[0], id[1], id[2] = 0xee, byte(n>>8), byte(n)
	vector := make([]float32, 384)
	for i := range vector {
		vector[i] = float32(n)*0.001 + float32(i)*0.0001
	}
	return index.Entry{ID: id, Model: "bge-small-en-v1.5-fp32", Dim: len(vector), Vector: vector}
}

// A record another process stores while this one compacts is kept, and one it
// forgets stays forgotten.
//
// Compacting read the two files, wrote the new base from what it had read, and
// only then took the lock, to empty the delta. A vector another process
// appended after the read and before the emptying was in neither: it was
// emptied out with the delta, and the record it belonged to could not be found
// by meaning in any later process. A removal was undone the same way.
func TestAVectorAnotherProcessWritesDuringACompactionIsKept(t *testing.T) {
	dir := t.TempDir()
	compacting, writing := openStore(t, dir), openStore(t, dir)

	// Enough vectors that a compaction takes long enough to be written beside.
	var held []index.Entry
	for n := range 150 {
		held = append(held, numbered(n))
	}
	if err := compacting.Append(held...); err != nil {
		t.Fatalf("append: %v", err)
	}
	retired := held[:40]

	stop := make(chan struct{})
	var folding sync.WaitGroup
	folding.Add(1)
	go func() {
		defer folding.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := compacting.Fold(); err != nil {
				t.Errorf("compact while another process writes: %v", err)
				return
			}
			// The lock is not handed out in turn, so a loop that took it again
			// at once would keep the writer waiting for most of the test.
			time.Sleep(time.Millisecond)
		}
	}()
	var stored []index.Entry
	for n, gone := range retired {
		if err := writing.Remove(gone.ID); err != nil {
			t.Fatalf("remove: %v", err)
		}
		fresh := numbered(1000 + n)
		if err := writing.Append(fresh); err != nil {
			t.Fatalf("append: %v", err)
		}
		stored = append(stored, fresh)
	}
	close(stop)
	folding.Wait()
	compacting.Close()
	writing.Close()

	got := byID(mustHydrate(t, openStore(t, dir)))
	var lost, back int
	for _, e := range append(stored, held[len(retired):]...) {
		if _, ok := got[e.ID]; !ok {
			lost++
		}
	}
	for _, e := range retired {
		if _, ok := got[e.ID]; ok {
			back++
		}
	}
	if lost > 0 {
		t.Errorf("%d vector(s) are gone, of the %d one process stored while the other compacted and the %d it left alone",
			lost, len(stored), len(held)-len(retired))
	}
	if back > 0 {
		t.Errorf("%d of the %d vector(s) one process removed while the other compacted are back", back, len(retired))
	}
}

// Two processes can both find the deltas due and compact at the same moment.
// Each writes its new base to the one pending file and renames it into place,
// so they take turns, and the base either leaves is whole.
func TestProcessesCompactingAtOnceLeaveAWholeBase(t *testing.T) {
	dir := t.TempDir()
	var want []index.Entry
	for n := range 200 {
		want = append(want, numbered(n))
	}
	first := openStore(t, dir)
	if err := first.Append(want...); err != nil {
		t.Fatalf("append: %v", err)
	}

	var compacting sync.WaitGroup
	for _, store := range []*index.Store{first, openStore(t, dir), openStore(t, dir)} {
		compacting.Add(1)
		go func() {
			defer compacting.Done()
			for range 15 {
				if err := store.Fold(); err != nil {
					t.Errorf("compact beside another process compacting: %v", err)
					return
				}
			}
		}()
	}
	compacting.Wait()

	got := byID(mustHydrate(t, openStore(t, dir)))
	if len(got) != len(want) {
		t.Errorf("the index holds %d vector(s) once three processes have compacted it, want %d", len(got), len(want))
	}
	for _, e := range want {
		if _, ok := got[e.ID]; !ok {
			t.Fatalf("vector %s is gone once three processes have compacted the index", e.ID)
		}
	}
}

// A process opening the vault reads the base and then the delta, and another
// may compact in between. Every vector written before the read began is in what
// it reads, whichever of the two files held it at the time.
//
// Read apart from the compaction, a base from before it and a delta from after
// it held everything but what the delta had held, so a vault opened at the
// wrong moment searched without its newest records until it was opened again.
// On Windows the two also collide outright: a base that is open to be read
// cannot be renamed over, and one being renamed over cannot be opened.
func TestOpeningBesideACompactionReadsEveryVectorWrittenBefore(t *testing.T) {
	dir := t.TempDir()
	writing, opening := openStore(t, dir), openStore(t, dir)

	// A base large enough that reading it takes a while, which is when the
	// other process's compaction has to land for the two reads to disagree.
	var held []index.Entry
	for n := range 400 {
		held = append(held, numbered(n))
	}
	if err := writing.Append(held...); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := writing.Fold(); err != nil {
		t.Fatalf("fold: %v", err)
	}

	var written atomic.Int64
	written.Store(int64(len(held)))
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for round := range 60 {
			n := len(held) + round
			if err := writing.Append(numbered(n)); err != nil {
				t.Errorf("append: %v", err)
				return
			}
			written.Store(int64(n + 1))
			if err := writing.Fold(); err != nil {
				t.Errorf("compact while another process opens the index: %v", err)
				return
			}
		}
	}()
	var reads int
	for reading := true; reading; reads++ {
		select {
		case <-finished:
			reading = false
		default:
		}
		before := written.Load()
		entries, err := opening.Hydrate()
		if err != nil {
			t.Errorf("read the index while another process compacts it: %v", err)
			break
		}
		if int64(len(entries)) < before {
			t.Errorf("read %d vector(s) from an index that held %d before the read began", len(entries), before)
			break
		}
	}
	<-finished
	if reads < 10 {
		t.Errorf("the index was read %d time(s) beside 60 compactions, too few to have met one", reads)
	}
}

// Something that is not a vault can have the base open at the moment a
// compaction replaces it: a virus scanner, a search indexer, a backup. It reads
// for a moment and lets go, and the compaction goes through once it has. On
// Windows a file that is open cannot be renamed over, so a compaction that took
// the first refusal for an answer failed the write that had set it off.
func TestACompactionReplacesABaseSomethingElseIsReading(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	var want []index.Entry
	for n := range 20 {
		want = append(want, numbered(n))
	}
	if err := store.Append(want[:10]...); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Fold(); err != nil {
		t.Fatalf("fold: %v", err)
	}
	if err := store.Append(want[10:]...); err != nil {
		t.Fatalf("append: %v", err)
	}

	reading, err := os.Open(filepath.Join(dir, index.BaseName))
	if err != nil {
		t.Fatalf("open the base as another reader would: %v", err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(300 * time.Millisecond)
		reading.Close()
	}()

	err = store.Fold()
	<-released
	if err != nil {
		t.Fatalf("compact while something else had the base open for a moment: %v", err)
	}
	store.Close()

	got := byID(mustHydrate(t, openStore(t, dir)))
	if len(got) != len(want) {
		t.Errorf("the index holds %d vector(s) after the compaction, want %d", len(got), len(want))
	}
}

func mustHydrate(t *testing.T, store *index.Store) []index.Entry {
	t.Helper()
	entries, err := store.Hydrate()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	return entries
}
