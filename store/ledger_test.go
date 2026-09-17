package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steven3002/sennit/sia"
)

// A writeLog is the order a batch write went in, step by step.
//
// The order is the subject of every test in this file. A slab is billed from
// the moment it is pinned, and the ledger is the only thing that can find it
// again, so a write that pins before it records leaves storage nothing can
// reach if it is interrupted in between.
type writeLog struct {
	steps    []string
	ledgered []SlabsWritten
}

func (l *writeLog) step(name string) { l.steps = append(l.steps, name) }

func (l *writeLog) ledger(written SlabsWritten) error {
	l.step("ledger")
	l.ledgered = append(l.ledgered, written)
	return nil
}

// A fakeBoundary stands in for the Sia side of a write.
//
// Nothing here talks to an indexer, which is the point: the real one bills a
// slab for every run.
type fakeBoundary struct {
	log   *writeLog
	slabs []sia.SlabID

	uploadErr     error
	pinSlabsErr   error
	pinObjectsErr error
}

func (b *fakeBoundary) Upload(_ context.Context, payloads [][]byte, _ ...sia.UploadOption) (*uploaded, error) {
	b.log.step("upload")
	if b.uploadErr != nil {
		return nil, b.uploadErr
	}
	out := &uploaded{slabs: b.slabs}
	for i, payload := range payloads {
		var ref sia.ObjectRef
		ref.ID[0] = byte(i + 1)
		out.placements = append(out.placements, sia.Placement{
			Ref:   ref,
			Slab:  b.slabs[0],
			Bytes: len(payload),
		})
	}
	return out, nil
}

func (b *fakeBoundary) PinSlabs(context.Context, *uploaded) error {
	b.log.step("pin slabs")
	return b.pinSlabsErr
}

func (b *fakeBoundary) PinObjects(context.Context, *uploaded) error {
	b.log.step("pin objects")
	return b.pinObjectsErr
}

func (b *fakeBoundary) Download(context.Context, sia.ObjectRef) ([]byte, error) {
	return nil, errors.New("the fake boundary reads nothing")
}

func (b *fakeBoundary) SlabPayloadSize() (int64, error) { return DefaultSlabPayloadSize, nil }

func blobs(sizes ...int) []Blob {
	out := make([]Blob, len(sizes))
	for i, size := range sizes {
		out[i] = Blob{CID: fmt.Sprintf("cid-%d", i+1), Payload: make([]byte, size)}
	}
	return out
}

// storeOver builds a store whose network half is the fake and whose ledger is
// the log.
func storeOver(log *writeLog, boundary *fakeBoundary) *Store {
	return &Store{across: boundary, ledger: log.ledger}
}

const probeSlab sia.SlabID = "f2c9a0f2c6d3f1b0a9e8d7c6b5a493827160fedcba0987654321fedcba098765"

// The slab reaches the ledger between the upload and the first pin, and the
// figures it carries are the whole batch's.
func TestASlabIsRecordedBeforeItIsPinned(t *testing.T) {
	log := &writeLog{}
	s := storeOver(log, &fakeBoundary{log: log, slabs: []sia.SlabID{probeSlab}})

	flush, err := s.PutBatch(context.Background(), blobs(400, 600, 18))
	if err != nil {
		t.Fatalf("PutBatch: %v", err)
	}

	want := []string{"upload", "ledger", "pin slabs", "pin objects"}
	if strings.Join(log.steps, " -> ") != strings.Join(want, " -> ") {
		t.Errorf("the write went %v, want %v", log.steps, want)
	}
	if len(log.ledgered) != 1 {
		t.Fatalf("the ledger was written %d time(s), want once", len(log.ledgered))
	}
	written := log.ledgered[0]
	if len(written.Slabs) != 1 || written.Slabs[0] != probeSlab {
		t.Errorf("the ledger was told about %v, want [%s]", written.Slabs, probeSlab)
	}
	if written.Records != 3 || written.Bytes != 1018 {
		t.Errorf("the ledger was told %d record(s) of %d byte(s), want 3 of 1018",
			written.Records, written.Bytes)
	}
	// What the caller gets back is unchanged by any of this.
	if len(flush.Slabs) != 1 || flush.Slabs[0] != probeSlab || len(flush.Written) != 3 {
		t.Errorf("the flush reported %d object(s) over %v", len(flush.Written), flush.Slabs)
	}
	if flush.Bytes() != 1018 {
		t.Errorf("the flush reported %d byte(s), want 1018", flush.Bytes())
	}
}

// The case this exists for. Pinning the objects of a batch is one indexer round
// trip per record, sixteen at a time, so an interrupt part way through leaves
// some objects pinned and some not, and the whole batch fails. The slab is
// billed either way, and it has to be in the ledger by then or nothing will
// ever find it.
func TestAnInterruptedObjectPinLeavesTheSlabRecorded(t *testing.T) {
	log := &writeLog{}
	// The error is shaped like the one the Sia package returns from a cancelled
	// object pin: the failures of the calls in flight, joined.
	interrupted := errors.Join(
		fmt.Errorf("pin object 97 of 320 (%s): %w", probeSlab, context.Canceled),
		fmt.Errorf("pin object 98 of 320 (%s): %w", probeSlab, context.Canceled),
	)
	s := storeOver(log, &fakeBoundary{
		log:           log,
		slabs:         []sia.SlabID{probeSlab},
		pinObjectsErr: interrupted,
	})

	if _, err := s.PutBatch(context.Background(), blobs(400, 600, 18)); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutBatch returned %v, want the cancelled object pin", err)
	}
	if len(log.ledgered) != 1 || len(log.ledgered[0].Slabs) != 1 || log.ledgered[0].Slabs[0] != probeSlab {
		t.Fatalf("the interrupted write recorded %v; slab %s is billed and nothing local names it",
			log.ledgered, probeSlab)
	}
	if log.steps[1] != "ledger" {
		t.Errorf("the write went %v, and the ledger must come before either pin", log.steps)
	}
}

// The same holds for an interrupt between the two pins, which is the narrower
// case: the slab is pinned, holds nothing, and is recorded.
func TestAnInterruptedSlabPinLeavesTheSlabRecorded(t *testing.T) {
	log := &writeLog{}
	s := storeOver(log, &fakeBoundary{
		log:         log,
		slabs:       []sia.SlabID{probeSlab},
		pinSlabsErr: context.Canceled,
	})

	if _, err := s.PutBatch(context.Background(), blobs(18)); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutBatch returned %v, want the cancelled slab pin", err)
	}
	if len(log.ledgered) != 1 {
		t.Fatalf("the interrupted write recorded %v; slab %s is billed and nothing local names it",
			log.ledgered, probeSlab)
	}
	if last := log.steps[len(log.steps)-1]; last != "pin slabs" {
		t.Errorf("the write went %v, want it to stop at the slab pin", log.steps)
	}
}

// A ledger that cannot be written stops the write before anything is pinned.
// Carrying on would pin a slab this device could never find again, which is the
// exact state the ordering exists to prevent.
func TestNothingIsPinnedWhenTheSlabCannotBeRecorded(t *testing.T) {
	log := &writeLog{}
	refused := errors.New("the device's database is read-only")
	boundary := &fakeBoundary{log: log, slabs: []sia.SlabID{probeSlab}}
	s := &Store{across: boundary, ledger: func(SlabsWritten) error {
		log.step("ledger")
		return refused
	}}

	_, err := s.PutBatch(context.Background(), blobs(400, 618))
	if !errors.Is(err, refused) {
		t.Fatalf("PutBatch returned %v, want the ledger's own failure", err)
	}
	want := []string{"upload", "ledger"}
	if strings.Join(log.steps, " -> ") != strings.Join(want, " -> ") {
		t.Errorf("the write went %v, want it to stop at %v", log.steps, want)
	}
}

// A store with nowhere to record its slabs refuses to write at all, before it
// uploads anything. It cannot happen through the constructor; the refusal is
// there because the failure it replaces is invisible.
func TestAWriteWithNoLedgerIsRefused(t *testing.T) {
	log := &writeLog{}
	s := &Store{across: &fakeBoundary{log: log, slabs: []sia.SlabID{probeSlab}}}

	if _, err := s.PutBatch(context.Background(), blobs(18)); !errors.Is(err, errNoLedger) {
		t.Fatalf("PutBatch returned %v, want the refusal", err)
	}
	if len(log.steps) != 0 {
		t.Errorf("the refused write still did %v", log.steps)
	}
}

// An empty batch is still nothing at all, ledger or no ledger.
func TestAnEmptyBatchWritesNothing(t *testing.T) {
	log := &writeLog{}
	s := storeOver(log, &fakeBoundary{log: log, slabs: []sia.SlabID{probeSlab}})

	flush, err := s.PutBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("PutBatch: %v", err)
	}
	if len(flush.Slabs) != 0 || len(log.steps) != 0 {
		t.Errorf("an empty batch reported %v and did %v", flush.Slabs, log.steps)
	}
}
