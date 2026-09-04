package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/steven3002/sennit/embed"
	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/seal"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
	"github.com/steven3002/sennit/store/reclaim"
)

// ErrWrongPhrase reports that the recovery phrase does not match the one this
// vault was created with.
var ErrWrongPhrase = errors.New("recovery phrase does not match this vault")

// A Vault is the public API. It orchestrates every operation and owns nothing
// that another package could own instead.
type Vault struct {
	opts      Options
	sealer    *seal.Sealer
	local     *local.Store
	manifest  *manifest.Manifest
	index     *index.Index
	embedder  embed.Vectorizer
	client    *sia.Client
	store     *store.Store
	packer    *packer.Packer
	reclaimer *reclaim.Reclaimer
	vectors   *index.Store
	health    IndexHealth
	// ownsEmbedder records whether this vault loaded the model itself, which is
	// what decides whether closing the vault may close it.
	ownsEmbedder bool
	// ready records that the indexer has been seen able to accept a write, so
	// the check that gates the first one is not repeated on every later one.
	ready        atomic.Bool
	sessionStats sessionCounters
	// offlineBecause records that the vault wanted a connection and could not
	// get one. It is distinct from the caller asking to be offline, and it is
	// reported rather than swallowed: silently working from the device is right
	// for a read and wrong to leave unsaid before a write.
	offlineBecause error
}

// sessionCounters are read counts a session claim is asserted against.
type sessionCounters struct {
	HeadReads  atomic.Int64
	ChunkReads atomic.Int64
	ChunkBytes atomic.Int64
}

// Open prepares a vault for use.
//
// The recovery phrase is consumed here and not retained: every key the process
// needs is derived up front, so the phrase itself has no reason to survive the
// call.
func Open(ctx context.Context, opts Options) (*Vault, error) {
	opts.applyDefaults()
	if err := os.MkdirAll(opts.Home, 0o700); err != nil {
		return nil, fmt.Errorf("prepare vault directory %s: %w", opts.Home, err)
	}

	seed, err := keys.SeedFromPhrase(opts.Phrase)
	if err != nil {
		return nil, err
	}
	hierarchy, err := keys.Derive(seed)
	seed.Wipe()
	if err != nil {
		return nil, err
	}
	defer hierarchy.Wipe()

	v := &Vault{opts: opts}
	if err := v.openLocal(hierarchy); err != nil {
		v.Close()
		return nil, err
	}
	if err := v.openIndex(ctx); err != nil {
		v.Close()
		return nil, err
	}
	if !opts.Offline {
		if err := v.connect(ctx); err != nil {
			// An indexer that cannot be reached degrades the vault instead of
			// refusing to open it. Everything a read needs, the query
			// embedding, the vector search, the device's own copy of a record,
			// is local, so a network outage was turning a working local search
			// into a hard failure. A write is queued exactly as it is when the
			// caller asked to be offline, and the queue is what owes it to the
			// network later.
			//
			// Only unreachability degrades. A refusal is a different thing: an
			// unauthorized installation needs the user to do something, and
			// carrying on quietly would hide the one message that says what.
			if !errors.Is(err, sia.ErrIndexerUnreachable) {
				v.Close()
				return nil, err
			}
			v.offlineBecause = err
		}
	}
	// The packer exists whether or not there is a connection. Offline it can
	// only queue, but queueing is the part that must not be skipped: a record
	// written with no connection is owed to the network exactly as much as one
	// written with a connection, and the earlier design silently owed nothing.
	if err := v.openPacker(); err != nil {
		v.Close()
		return nil, err
	}
	return v, nil
}

func (v *Vault) openLocal(hierarchy keys.Hierarchy) error {
	sealer, err := seal.New(hierarchy.Record, hierarchy.Content)
	if err != nil {
		return err
	}
	v.sealer = sealer

	store, err := local.Open(v.opts.dbPath())
	if err != nil {
		return err
	}
	v.local = store

	if err := v.checkPhrase(hierarchy); err != nil {
		return err
	}

	manifestSealer, err := seal.New(hierarchy.Manifest, hierarchy.Content)
	if err != nil {
		return err
	}
	log, err := manifest.OpenLog(v.opts.manifestDir(), manifestSealer)
	if err != nil {
		return err
	}
	catalog, err := manifest.Load(log)
	if err != nil {
		return err
	}
	v.manifest = catalog
	return nil
}

// checkPhrase stores a derived check value on first open and compares it
// afterwards, so a mistyped phrase reports itself instead of quietly opening a
// second, empty vault over the first one's files.
func (v *Vault) checkPhrase(hierarchy keys.Hierarchy) error {
	const key = "phraseCheck"
	want := fmt.Sprintf("%x", hierarchy.Check)

	got, err := v.local.GetMeta(key)
	if errors.Is(err, local.ErrNotFound) {
		return v.local.PutMeta(key, want)
	}
	if err != nil {
		return err
	}
	if got != want {
		return ErrWrongPhrase
	}
	return nil
}

func (v *Vault) openIndex(ctx context.Context) error {
	if err := v.openEmbedder(ctx); err != nil {
		return err
	}
	v.index = index.New(v.opts.Model.Name, v.opts.Model.Dim)

	vectors, err := index.OpenStore(v.opts.indexDir(), v.sealer)
	if err != nil {
		return err
	}
	v.vectors = vectors

	stored, err := vectors.Hydrate()
	if err != nil {
		return err
	}
	usable, health := classifyVectors(v.opts.Model.Name, v.opts.Model.Dim, stored)
	for _, vector := range usable {
		if err := v.index.Add(vector.ID, vector.Model, vector.Vector); err != nil {
			return err
		}
	}
	v.health = health
	return nil
}

// openEmbedder settles which model this vault embeds with.
//
// A supplied embedder wins and its identity replaces the configured one, so the
// name stamped on every vector is always the name of what produced it. Silently
// keeping the configured name would put two models' vectors in one index under
// one label, which is the single failure the model stamp exists to make
// visible.
func (v *Vault) openEmbedder(ctx context.Context) error {
	if v.opts.Embedder != nil {
		v.embedder = v.opts.Embedder
		v.opts.Model = v.opts.Embedder.Model()
		return nil
	}
	v.ownsEmbedder = true
	dir, err := v.opts.Model.Fetch(ctx, v.opts.ModelDir)
	if err != nil {
		return err
	}
	embedder, err := embed.Open(ctx, v.opts.Model, dir)
	if err != nil {
		return err
	}
	v.embedder = embedder
	v.opts.Model.Dim = embedder.Dim()
	return nil
}

// classifyVectors separates the vectors this index can search from the ones it
// cannot, and counts what it left behind.
//
// A vector from another model belongs to a different space, so scoring it
// against this query would be meaningless rather than merely worse. It is
// counted rather than passed over in silence: a vault that quietly searches a
// tenth of itself looks exactly like a vault with poor recall, and the two want
// completely different responses.
func classifyVectors(model string, dim int, stored []index.Entry) ([]index.Entry, IndexHealth) {
	health := IndexHealth{Model: model, Dim: dim, Foreign: map[string]int{}}
	usable := make([]index.Entry, 0, len(stored))
	for _, vector := range stored {
		if vector.Model != model || vector.Dim != dim {
			health.Foreign[vector.Model]++
			continue
		}
		usable = append(usable, vector)
		health.Indexed++
	}
	return usable, health
}

// An IndexHealth reports what the index was built from.
//
// It exists because the interesting failure here is silent. Vectors from two
// models cannot be compared, but comparing them produces a number rather than an
// error, so an index that has been half re-embedded ranks confidently and
// wrongly. Counting what was left out is what turns that into something a user
// can be told.
type IndexHealth struct {
	// Model and Dim are what this index accepts.
	Model string
	Dim   int
	// Indexed is how many vectors are searchable.
	Indexed int
	// Foreign counts stored vectors held under some other model, by model name.
	Foreign map[string]int
}

// Mixed reports whether the device holds vectors this index cannot search.
func (h IndexHealth) Mixed() bool { return len(h.Foreign) > 0 }

// Stale reports how many records are on the device but absent from the index.
func (h IndexHealth) Stale() int {
	var n int
	for _, count := range h.Foreign {
		n += count
	}
	return n
}

// IndexHealth reports what the search index was built from, including any
// vectors it had to leave out.
func (v *Vault) IndexHealth() IndexHealth { return v.health }

func (v *Vault) connect(ctx context.Context) error {
	client, err := sia.Connect(sia.Config{Indexer: v.opts.Indexer, AppKey: v.opts.AppKey})
	if err != nil {
		return err
	}
	v.client = client
	v.store = store.New(client)
	v.reclaimer = reclaim.New(client, v.local)
	_ = ctx
	return nil
}

// openPacker prepares the write queue.
//
// The flush policy needs the slab size, which only the network knows, so an
// offline vault falls back to the measured default. It affects nothing that
// matters offline: the byte trigger is the one deadline that never fires in
// practice, and a flush cannot happen without a connection anyway.
func (v *Vault) openPacker() error {
	policy := v.opts.Flush
	if policy == (packer.Policy{}) {
		slabBytes := int64(store.DefaultSlabPayloadSize)
		if v.store != nil {
			measured, err := v.store.SlabPayloadSize()
			if err != nil {
				return err
			}
			slabBytes = measured
		}
		policy = packer.DefaultPolicy(slabBytes)
	}

	queue, err := packer.New(v.store, v.local, policy)
	if err != nil {
		return err
	}
	v.packer = queue
	v.packer.OnFlush(v.recordFlush)
	return nil
}

// StartFlushing runs the flush deadlines in the background until the vault is
// closed.
//
// It is opt-in because a short-lived command has nothing to gain from a timer:
// it flushes explicitly, or leaves the queue for the next run. A long-running
// host is the case the deadlines were written for.
func (v *Vault) StartFlushing(ctx context.Context, onError func(error)) {
	if v.packer != nil {
		v.packer.Start(ctx, onError)
	}
}

// Close releases everything the vault holds. Records still queued for a flush
// stay on this device and are written by a later run.
//
// Closing twice is a no-op rather than an error. A caller that defers a close
// and also closes explicitly, which is what any code path that wants to prove
// something survives the vault has to do, should not be reporting failures
// from the second one.
func (v *Vault) Close() error {
	var errs []error
	if v.packer != nil {
		v.packer.Stop()
		v.packer = nil
	}
	if v.manifest != nil {
		errs = append(errs, v.manifest.Close())
		v.manifest = nil
	}
	if v.vectors != nil {
		errs = append(errs, v.vectors.Close())
		v.vectors = nil
	}
	if v.embedder != nil && v.ownsEmbedder {
		errs = append(errs, v.embedder.Close())
		v.embedder = nil
	}
	if v.client != nil {
		errs = append(errs, v.client.Close())
		v.client = nil
	}
	if v.local != nil {
		errs = append(errs, v.local.Close())
		v.local = nil
	}
	if v.sealer != nil {
		v.sealer.Close()
		v.sealer = nil
	}
	return errors.Join(errs...)
}

// Online reports whether the vault has a connection to an indexer.
func (v *Vault) Online() bool { return v.client != nil }

// OfflineBecause reports why a vault that asked for a connection has none.
//
// It is nil both for a vault that is connected and for one the caller asked to
// be offline. Only an indexer that could not be reached sets it.
func (v *Vault) OfflineBecause() error { return v.offlineBecause }

// Indexer reports which indexer the vault is connected to.
func (v *Vault) Indexer() string {
	if v.client == nil {
		return ""
	}
	return v.client.Indexer()
}

// Account reports the vault's standing with the indexer.
func (v *Vault) Account(ctx context.Context) (sia.Account, error) {
	if v.client == nil {
		return sia.Account{}, errOffline
	}
	return v.client.Account(ctx)
}

// WaitReady blocks until the indexer can accept a write.
func (v *Vault) WaitReady(ctx context.Context, budget time.Duration) (sia.Account, error) {
	if v.client == nil {
		return sia.Account{}, errOffline
	}
	account, err := v.client.WaitReady(ctx, budget)
	if err == nil {
		v.ready.Store(true)
	}
	return account, err
}

// FirstWriteBudget is how long a vault waits for a freshly approved account to
// become usable before giving up.
//
// Funding host accounts was measured at about sixteen seconds. The budget is
// generously above it because the cost of waiting too long is a slow first run
// and the cost of not waiting is a failed one, reported as an error about hosts
// that gives the reader nothing to act on.
const FirstWriteBudget = 90 * time.Second

// awaitFirstWrite holds the first flush of a process until the indexer has
// funded enough host accounts to accept it.
//
// Approval and readiness are two different events, and the gap between them is
// the first thing a new device meets. Without this the very first write after
// onboarding fails, with an error that names hosts and does not say to wait,
// which is the shape of failure that makes a user conclude the product is broken
// rather than busy.
//
// It runs once. After the account has been seen ready it stays ready, and a
// per-flush round trip would be a network call on the path this system works
// hardest to keep short.
func (v *Vault) awaitFirstWrite(ctx context.Context) error {
	if v.client == nil || v.ready.Load() {
		return nil
	}
	if _, err := v.WaitReady(ctx, FirstWriteBudget); err != nil {
		return err
	}
	return nil
}

// Pending reports how many records are held on this device but not yet written
// to the network.
func (v *Vault) Pending() int {
	if v.packer == nil {
		return 0
	}
	return v.packer.Pending()
}

// PendingBytes reports the sealed size of what is queued, counted as it will be
// written: ciphertext with its envelope and framing, which is what a slab is
// actually filled with.
func (v *Vault) PendingBytes() int64 {
	if v.packer == nil {
		return 0
	}
	return v.packer.PendingBytes()
}

// Recall retrieves records by meaning.
func (v *Vault) Recall(ctx context.Context, req recall.Request) (recall.Result, error) {
	return recall.New(v.embedder, v.index, v, v, v.local).Run(ctx, req)
}

var errOffline = errors.New("vault is open offline")
