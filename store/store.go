// Package store reads and writes content-addressed objects.
package store

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/steven3002/sennit/sia"
)

// DefaultSlabPayloadSize is how many payload bytes one slab holds at the
// default erasure coding, ten data shards of four MiB each.
//
// The live value is read from the SDK; this constant is for the offline case,
// where nothing can be written anyway and the only thing that needs a number is
// the queue's size ceiling.
const DefaultSlabPayloadSize = 10 * 4 << 20

// A Blob is one framed, sealed record on its way to storage.
type Blob struct {
	// CID is the keyed content address of the record body.
	CID string
	// Payload is the framed ciphertext exactly as it will be written.
	Payload []byte
}

// A Written is where one blob ended up.
type Written struct {
	CID       string
	ObjectRef sia.ObjectRef
	SlabID    sia.SlabID
	Bytes     int
}

// A Store writes and reads objects through the Sia boundary.
//
// It exposes no way to write a single object. That is the point: a slab is
// billed whole and cannot be extended afterwards, so a lone record costs a
// slab. Making the batch the only unit means the expensive mistake cannot be
// made by accident from a layer above.
type Store struct {
	across   boundary
	ledger   SlabLedger
	observer func(Event)
}

// New builds a store over an authorized Sia client and the ledger it writes its
// slabs into.
//
// The ledger is a parameter rather than something a caller may attach later
// because a write that cannot record its slabs must not pin them, and a store
// assembled in two steps can be used in between.
func New(client *sia.Client, ledger SlabLedger) *Store {
	return &Store{across: siaBoundary{client: client}, ledger: ledger}
}

// A SlabsWritten names the slabs one batch has been uploaded into, before any
// of them is pinned.
type SlabsWritten struct {
	// Slabs are the distinct slabs the batch occupies. A packed batch normally
	// has exactly one however many records ride in it.
	Slabs []sia.SlabID
	// Records is how many records the batch carries and Bytes is the payload
	// they amount to, which is what a ledger records against each slab.
	Records int
	Bytes   int64
}

// A SlabLedger records the slabs a write has created.
//
// It is called once the upload has finalized, which is when the slab ids first
// exist, and before anything is pinned. That order is the whole of it. A slab
// is billed from the moment it is pinned and a ledger of some kind is the only
// thing that can find it again afterwards, so a slab pinned before it is
// recorded is storage the account pays for that nothing can reach. An interrupt
// in that window leaves exactly this, and pinning the objects of a large batch
// holds the window open for seconds.
//
// What the order costs is a ledger that can name a slab which was never pinned,
// when the pin is what failed. That is the cheap direction: releasing a slab
// the indexer does not have already counts as success, so a later reclamation
// drops the entry, while storage nobody recorded is found only by hand.
type SlabLedger func(SlabsWritten) error

// errNoLedger refuses a write that has nowhere to record what it pins.
//
// Nothing in the product can reach it, since the ledger is a constructor
// argument. It is a refusal rather than a silent write because the failure it
// would otherwise produce is invisible: a flush that worked perfectly and left
// a slab billed to the account with no record anywhere that it exists.
var errNoLedger = errors.New("this store has no ledger to record its slabs in, and a write that cannot be recorded must not be pinned")

// A boundary is the Sia side of a store, the calls it makes across the network.
//
// It is an interface with one production implementation, and the reason is the
// order of a write rather than substitutability. The ledger entry has to land
// between the upload and the pin, and checking that against a real indexer
// costs a pinned slab for every run, which is the very leak the order exists to
// close. Behind this interface the same order costs nothing to check.
type boundary interface {
	Upload(ctx context.Context, payloads [][]byte, opts ...sia.UploadOption) (*uploaded, error)
	PinSlabs(ctx context.Context, batch *uploaded) error
	PinObjects(ctx context.Context, batch *uploaded) error
	Download(ctx context.Context, ref sia.ObjectRef) ([]byte, error)
	SlabPayloadSize() (int64, error)
}

// An uploaded is one finalized upload, not yet pinned.
//
// The slab ids and the placements are read out of the batch as soon as it
// finalizes, at the one moment both are known, so that everything above works
// from values this package owns instead of from the SDK's own descriptors.
type uploaded struct {
	batch      *sia.Batch
	slabs      []sia.SlabID
	placements []sia.Placement
}

// siaBoundary is the production boundary, one method per client call.
type siaBoundary struct{ client *sia.Client }

func (b siaBoundary) Upload(ctx context.Context, payloads [][]byte, opts ...sia.UploadOption) (*uploaded, error) {
	batch, err := b.client.UploadPacked(ctx, payloads, opts...)
	if err != nil {
		return nil, err
	}
	return &uploaded{batch: batch, slabs: batch.Slabs(), placements: batch.Placements()}, nil
}

func (b siaBoundary) PinSlabs(ctx context.Context, batch *uploaded) error {
	return b.client.PinSlabs(ctx, batch.batch)
}

func (b siaBoundary) PinObjects(ctx context.Context, batch *uploaded) error {
	return b.client.PinObjects(ctx, batch.batch)
}

func (b siaBoundary) Download(ctx context.Context, ref sia.ObjectRef) ([]byte, error) {
	return b.client.Download(ctx, ref)
}

func (b siaBoundary) SlabPayloadSize() (int64, error) { return b.client.SlabPayloadSize() }

// An EventKind names the stage a write has reached.
type EventKind int

const (
	// Uploading covers writing the shards, the one stage with a count.
	Uploading EventKind = iota
	// PinningSlabs registers the slabs with the indexer, once per batch.
	PinningSlabs
	// PinningObjects records where each record sits, which is what makes it
	// retrievable.
	PinningObjects
)

// An Event is a write reporting where it has got to, for a caller that shows it.
type Event struct {
	Kind EventKind
	// Done and Total count shard uploads while Uploading. Total is known before
	// the first shard is written and never changes, so a percentage of it is
	// exact rather than an estimate.
	Done, Total int64
}

// Observe registers a function called as a write moves through its stages.
//
// It may be called from several goroutines at once, because shards are uploaded
// in parallel, and it must not block: holding one shard's goroutine up costs
// the whole upload's throughput. A nil function reports nothing, which is the
// default and exactly today's behaviour.
func (s *Store) Observe(fn func(Event)) { s.observer = fn }

func (s *Store) report(event Event) {
	if s.observer != nil {
		s.observer(event)
	}
}

// A Flush is the outcome of writing one batch.
type Flush struct {
	Written []Written
	Slabs   []sia.SlabID
	// Durations of the three phases, kept apart because they scale with
	// different things: upload with bytes, slab pinning with slabs, object
	// pinning with records.
	UploadFor    time.Duration
	PinSlabsFor  time.Duration
	PinObjectFor time.Duration
}

// Bytes reports the payload written across the batch.
func (f Flush) Bytes() int {
	var total int
	for _, w := range f.Written {
		total += w.Bytes
	}
	return total
}

// PutBatch writes a batch of blobs and makes them retrievable.
//
// The two pinning steps are separate calls rather than one because the SDK's
// combined form repeats the slab half once per record, and a packed batch has
// exactly one slab to register however many records ride in it.
//
// The slabs reach the ledger between the upload and the first of those pins,
// and nothing is pinned if that fails. See SlabLedger: every later step here
// can be interrupted, and each one that is leaves a billed slab behind.
func (s *Store) PutBatch(ctx context.Context, blobs []Blob) (Flush, error) {
	if len(blobs) == 0 {
		return Flush{}, nil
	}
	if s.ledger == nil {
		return Flush{}, errNoLedger
	}
	payloads := make([][]byte, len(blobs))
	var payloadBytes int64
	for i, blob := range blobs {
		payloads[i] = blob.Payload
		payloadBytes += int64(len(blob.Payload))
	}

	start := time.Now()
	batch, err := s.across.Upload(ctx, payloads, s.uploadProgress(payloadBytes)...)
	if err != nil {
		return Flush{}, fmt.Errorf("upload batch of %d: %w", len(blobs), err)
	}
	flush := Flush{UploadFor: time.Since(start)}

	if err := s.ledger(SlabsWritten{
		Slabs:   batch.slabs,
		Records: len(blobs),
		Bytes:   payloadBytes,
	}); err != nil {
		return Flush{}, fmt.Errorf("record %d slab(s) before pinning them: %w", len(batch.slabs), err)
	}

	start = time.Now()
	s.report(Event{Kind: PinningSlabs})
	if err := s.across.PinSlabs(ctx, batch); err != nil {
		return Flush{}, err
	}
	flush.PinSlabsFor = time.Since(start)

	start = time.Now()
	s.report(Event{Kind: PinningObjects})
	if err := s.across.PinObjects(ctx, batch); err != nil {
		return Flush{}, err
	}
	flush.PinObjectFor = time.Since(start)

	flush.Written = make([]Written, len(batch.placements))
	for i, placement := range batch.placements {
		flush.Written[i] = Written{
			CID:       blobs[i].CID,
			ObjectRef: placement.Ref,
			SlabID:    placement.Slab,
			Bytes:     placement.Bytes,
		}
	}
	flush.Slabs = batch.slabs
	return flush, nil
}

// uploadProgress counts the shards of one write, so far and in total.
//
// The count is capped and never handed backwards: the reports arrive from
// several goroutines and can overtake each other, and a percentage that jumps
// back is read as something having gone wrong.
func (s *Store) uploadProgress(payloadBytes int64) []sia.UploadOption {
	if s.observer == nil {
		return nil
	}
	total := sia.ShardUploads(payloadBytes)
	s.report(Event{Kind: Uploading, Total: total})
	return []sia.UploadOption{sia.WithShardProgress(s.countShards(total))}
}

func (s *Store) countShards(total int64) func(sia.ShardUploaded) {
	var done atomic.Int64
	return func(sia.ShardUploaded) {
		s.report(Event{Kind: Uploading, Done: min(done.Add(1), total), Total: total})
	}
}

// Get fetches a stored payload by object ref.
func (s *Store) Get(ctx context.Context, ref sia.ObjectRef) ([]byte, error) {
	return s.across.Download(ctx, ref)
}

// SlabPayloadSize reports how many payload bytes one slab holds. A flush is
// billed for all of them whether it fills them or not.
func (s *Store) SlabPayloadSize() (int64, error) { return s.across.SlabPayloadSize() }
