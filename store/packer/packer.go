package packer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/store"
)

// ErrOffline reports that there is no connection to write a flush through.
// Records are still queued, and a later online run writes them.
var ErrOffline = errors.New("packer has no connection to the network")

// A Queued record is one sealed body awaiting a flush.
type Queued struct {
	ID   record.ID
	Kind record.Kind
	Blob store.Blob
	// Since is when the record was queued, which is when its durability window
	// opened.
	Since time.Time
}

// A Packer accumulates sealed records and writes them as one batch.
//
// Between a record being queued and a flush completing, that record exists only
// on this device. The window is a deliberate trade for the slab economics, but
// it is a real one: nothing above may report a queued record as stored on the
// network, and the queue itself is on disk so that the window is bounded by the
// flush cadence rather than by how long the process happens to live.
type Packer struct {
	mu     sync.Mutex
	store  *store.Store
	queue  *local.Store
	policy Policy
	owner  string
	state  local.QueueState

	onFlush func(Result) error

	stop chan struct{}
	done chan struct{}
}

// New builds a packer that queues to the device and writes through the store.
//
// A nil store leaves the packer able to queue but not to flush, which is what
// an offline vault needs: the record is still durable here and still owed to
// the network, and the next connected run settles the debt.
func New(s *store.Store, queue *local.Store, policy Policy) (*Packer, error) {
	if queue == nil {
		return nil, errors.New("packer needs the device store to hold its queue")
	}
	owner, err := newOwner()
	if err != nil {
		return nil, err
	}
	p := &Packer{store: s, queue: queue, policy: policy, owner: owner}
	if p.state, err = queue.QueueState(); err != nil {
		return nil, err
	}
	return p, nil
}

// OnFlush registers a callback invoked after each successful flush, before the
// flush returns. Its error is propagated: a batch that reached the network but
// could not be catalogued is a problem the caller has to hear about, since the
// bytes are now billed and nothing points at them.
func (p *Packer) OnFlush(fn func(Result) error) { p.onFlush = fn }

// A Result reports what one flush wrote.
type Result struct {
	Reason  Reason
	Records []Queued
	Flush   store.Flush
}

// Empty reports whether the flush wrote nothing.
func (r Result) Empty() bool { return len(r.Records) == 0 }

// Add queues a sealed record and flushes if the policy says it is due.
//
// The record is on disk before this returns, so a process that exits here has
// deferred the write rather than lost it.
func (p *Packer) Add(ctx context.Context, item Queued) (Result, error) {
	if item.Since.IsZero() {
		item.Since = time.Now()
	}
	if err := p.queue.Enqueue(local.QueuedBlob{
		ID:       item.ID,
		Kind:     item.Kind,
		CID:      item.Blob.CID,
		Payload:  item.Blob.Payload,
		QueuedAt: item.Since,
	}); err != nil {
		return Result{}, err
	}

	p.mu.Lock()
	p.state.Records++
	p.state.Bytes += int64(len(item.Blob.Payload))
	if p.state.Oldest.IsZero() {
		p.state.Oldest = item.Since
	}
	p.state.Newest = item.Since
	reason, due := p.policy.due(p.state, time.Now())
	p.mu.Unlock()

	if !due || p.store == nil {
		return Result{}, nil
	}
	return p.flush(ctx, reason)
}

// Due reports whether the queue has become flushable through the passage of
// time alone.
func (p *Packer) Due(now time.Time) (Reason, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.policy.due(p.state, now)
}

// Pending reports how many records are queued but not yet on the network.
func (p *Packer) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state.Records
}

// PendingBytes reports the sealed size of what is queued. It counts ciphertext
// as it will be written, envelope and framing included, because that is what
// fills a slab; the plaintext figure would understate it.
func (p *Packer) PendingBytes() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state.Bytes
}

// Oldest reports when the longest-waiting queued record was sealed, which is
// how far back the durability window currently reaches.
func (p *Packer) Oldest() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state.Oldest
}

// Flush writes whatever is queued.
func (p *Packer) Flush(ctx context.Context) (Result, error) { return p.flush(ctx, ReasonManual) }

func (p *Packer) flush(ctx context.Context, reason Reason) (Result, error) {
	if p.store == nil {
		return Result{}, ErrOffline
	}

	claimed, err := p.queue.ClaimQueued(p.owner, p.policy.claimTimeout(), 0)
	if err != nil {
		return Result{}, err
	}
	if len(claimed) == 0 {
		return Result{}, nil
	}

	batch := make([]Queued, len(claimed))
	blobs := make([]store.Blob, len(claimed))
	ids := make([]record.ID, len(claimed))
	for i, item := range claimed {
		batch[i] = Queued{
			ID:    item.ID,
			Kind:  item.Kind,
			Blob:  store.Blob{CID: item.CID, Payload: item.Payload},
			Since: item.QueuedAt,
		}
		blobs[i], ids[i] = batch[i].Blob, item.ID
	}

	flushed, err := p.store.PutBatch(ctx, blobs)
	if err != nil {
		// The records are still only on this device, so hand them back to the
		// queue rather than dropping them on the floor.
		if release := p.queue.ReleaseQueued(ids); release != nil {
			return Result{}, errors.Join(err, release)
		}
		return Result{}, err
	}

	result := Result{Reason: reason, Records: batch, Flush: flushed}
	if p.onFlush != nil {
		if err := p.onFlush(result); err != nil {
			// The bytes are on the network but nothing points at them yet.
			// Leaving the claim in place keeps the records recoverable by a
			// retry rather than losing them to a bookkeeping failure.
			return result, err
		}
	}
	if err := p.queue.DropQueued(ids); err != nil {
		return result, err
	}
	return result, p.refresh()
}

func (p *Packer) refresh() error {
	state, err := p.queue.QueueState()
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = state
	return nil
}

func newOwner() (string, error) {
	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("packer identity: %w", err)
	}
	return hex.EncodeToString(token), nil
}
