// Package store reads and writes content-addressed objects.
package store

import (
	"context"
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
	client   *sia.Client
	observer func(Event)
}

// New builds a store over an authorized Sia client.
func New(client *sia.Client) *Store { return &Store{client: client} }

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
func (s *Store) PutBatch(ctx context.Context, blobs []Blob) (Flush, error) {
	if len(blobs) == 0 {
		return Flush{}, nil
	}
	payloads := make([][]byte, len(blobs))
	var payloadBytes int64
	for i, blob := range blobs {
		payloads[i] = blob.Payload
		payloadBytes += int64(len(blob.Payload))
	}

	start := time.Now()
	batch, err := s.client.UploadPacked(ctx, payloads, s.uploadProgress(payloadBytes)...)
	if err != nil {
		return Flush{}, fmt.Errorf("upload batch of %d: %w", len(blobs), err)
	}
	flush := Flush{UploadFor: time.Since(start)}

	start = time.Now()
	s.report(Event{Kind: PinningSlabs})
	if err := s.client.PinSlabs(ctx, batch); err != nil {
		return Flush{}, err
	}
	flush.PinSlabsFor = time.Since(start)

	start = time.Now()
	s.report(Event{Kind: PinningObjects})
	if err := s.client.PinObjects(ctx, batch); err != nil {
		return Flush{}, err
	}
	flush.PinObjectFor = time.Since(start)

	placements := batch.Placements()
	flush.Written = make([]Written, len(placements))
	for i, placement := range placements {
		flush.Written[i] = Written{
			CID:       blobs[i].CID,
			ObjectRef: placement.Ref,
			SlabID:    placement.Slab,
			Bytes:     placement.Bytes,
		}
	}
	flush.Slabs = batch.Slabs()
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
	return s.client.Download(ctx, ref)
}

// SlabPayloadSize reports how many payload bytes one slab holds. A flush is
// billed for all of them whether it fills them or not.
func (s *Store) SlabPayloadSize() (int64, error) { return s.client.SlabPayloadSize() }
