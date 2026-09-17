package vault

import (
	"context"
	"fmt"
	"time"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
)

// A RememberRequest is one memory to store.
type RememberRequest struct {
	Statement string
	Context   string
	Type      record.Type
	Tags      []string
	Source    record.Source
	// Supersedes names a record this one replaces. The older record is kept and
	// stays reachable as history; it simply stops being the current answer.
	Supersedes *record.ID
	// Importance and Confidence are the caller's own judgement. The vault runs
	// no model and does not infer them.
	Importance float64
	Confidence float64
	// Links point at related records. They are provenance and navigation, not
	// an input to ranking: expanding along them was measured across a hundred
	// and nine configurations and never beat the unexpanded baseline.
	Links []string
	// ValidFrom and ValidUntil are when the statement is true of the world, as
	// opposed to when the vault learned it.
	ValidFrom  *record.Time
	ValidUntil *record.Time
	// Entities are the typed things the statement refers to.
	Entities []record.Entity
	// Keywords are extra lexical handles on the statement.
	Keywords []string
}

// A RememberResult reports what was stored.
type RememberResult struct {
	ID     record.ID
	CID    seal.CID
	Stored record.Time
	// OnNetwork reports whether the record reached Sia before this call
	// returned. When it is false the record is durable on this device only, and
	// nothing may present that as being stored on the network.
	OnNetwork bool
	// Flushed describes the write, when one happened.
	Flushed *store.Flush

	// Neighbours are the records already in the vault closest to this one, so
	// the caller can decide whether it has just added a fact, restated one, or
	// contradicted one. The vault does not decide that: it runs no model, and
	// the caller has the conversation this record came from.
	Neighbours []Neighbour
	// Conflicts are the neighbours close enough to be the same statement again.
	// They are the subset of Neighbours marked Conflict, repeated here because
	// they are the ones worth acting on.
	Conflicts []Neighbour
	// Tags reports how well the supplied tags separate this record from the rest
	// of the vault.
	Tags TagAdvice
	// LinkedSession is the conversation this memory was recorded against, when
	// the write named one. The edge now exists in both directions.
	LinkedSession *record.ID

	// EmbedFor, SealFor and FlushFor are what the write actually spent.
	EmbedFor time.Duration
	SealFor  time.Duration
	FlushFor time.Duration
	// AdviseFor is what finding the neighbours and judging the tags cost.
	AdviseFor time.Duration
}

// Remember stores a memory.
//
// Validation, embedding and the write to this device all complete before the
// call returns; reaching the network is queued. That ordering is what makes the
// user's wait independent of the network, and it is also why the result says
// plainly whether the record has left the device yet.
func (v *Vault) Remember(ctx context.Context, req RememberRequest) (RememberResult, error) {
	id, err := record.NewID()
	if err != nil {
		return RememberResult{}, err
	}
	now := record.Now()
	memory := &record.Memory{
		ID:            id,
		Kind:          record.KindMemory,
		Type:          req.Type,
		SchemaVersion: record.SchemaVersion,
		Version:       1,
		Statement:     req.Statement,
		Context:       req.Context,
		Keywords:      req.Keywords,
		Tags:          local.NormalizeTags(req.Tags),
		Entities:      req.Entities,
		CreatedAt:     now,
		UpdatedAt:     now,
		ValidFrom:     req.ValidFrom,
		ValidUntil:    req.ValidUntil,
		Importance:    req.Importance,
		Confidence:    req.Confidence,
		Supersedes:    req.Supersedes,
		Links:         req.Links,
		Source:        req.Source,
		Embedding: record.Embedding{
			Model: v.opts.Model.Name,
			Dim:   v.opts.Model.Dim,
		},
	}
	if err := memory.Validate(); err != nil {
		return RememberResult{}, err
	}
	// A supersession has to name a record this vault actually holds. Accepting
	// a pointer to nothing would hide the replaced record from recall without
	// anything having replaced it.
	if memory.Supersedes != nil {
		if _, err := v.local.RankingMetaFor(*memory.Supersedes); err != nil {
			return RememberResult{}, fmt.Errorf("supersede %s: %w", memory.Supersedes, err)
		}
	}

	start := time.Now()
	v.progress(Progress{Phase: PhaseEmbed})
	vector, err := v.embedder.EmbedOne(ctx, memory.IndexText())
	if err != nil {
		return RememberResult{}, err
	}
	result := RememberResult{ID: id, Stored: now, EmbedFor: time.Since(start)}

	body, err := record.Marshal(memory)
	if err != nil {
		return RememberResult{}, err
	}

	start = time.Now()
	cid, err := v.sealer.CID(body)
	if err != nil {
		return RememberResult{}, err
	}
	sealed, err := v.sealer.Seal(body)
	if err != nil {
		return RememberResult{}, err
	}
	framed, err := seal.FrameRecord(record.KindMemory, id, sealed)
	if err != nil {
		return RememberResult{}, err
	}
	result.SealFor = time.Since(start)
	result.CID = cid

	if err := v.local.PutBody(id, record.KindMemory, body); err != nil {
		return RememberResult{}, err
	}
	if err := v.putVector(id, vector); err != nil {
		return RememberResult{}, err
	}
	if err := v.local.PutRankingMeta(rankingMeta(memory)); err != nil {
		return RememberResult{}, err
	}

	start = time.Now()
	flushed, err := v.packer.Add(ctx, packer.Queued{
		ID:   id,
		Kind: record.KindMemory,
		Blob: store.Blob{CID: cid.String(), Payload: framed},
	})
	if err != nil {
		return RememberResult{}, err
	}
	result.FlushFor = time.Since(start)

	// A memory that names the conversation it came from is recorded on that
	// conversation too. Both directions are stored because both are asked, and
	// the reverse edge has no other home: a session that had to scan every
	// memory in the vault to find the ones it produced would make the
	// interesting question the expensive one.
	if memory.Source.SessionID != "" {
		sessionID, err := record.ParseID(memory.Source.SessionID)
		if err != nil {
			return RememberResult{}, fmt.Errorf("memory %s source: %w", id, err)
		}
		if err := v.LinkMemory(sessionID, id); err != nil {
			return RememberResult{}, err
		}
		result.LinkedSession = &sessionID
	}

	start = time.Now()
	if result.Neighbours, err = v.neighbours(ctx, id, vector); err != nil {
		return RememberResult{}, err
	}
	for _, neighbour := range result.Neighbours {
		if neighbour.Conflict {
			result.Conflicts = append(result.Conflicts, neighbour)
		}
	}
	if result.Tags, err = v.tagAdvice(memory.Tags); err != nil {
		return RememberResult{}, err
	}
	result.AdviseFor = time.Since(start)

	for _, written := range flushed.Flush.Written {
		if written.CID == cid.String() {
			result.OnNetwork = true
			result.Flushed = &flushed.Flush
			break
		}
	}
	return result, nil
}

// Flush writes everything queued to the network.
func (v *Vault) Flush(ctx context.Context) (*store.Flush, error) {
	if err := v.awaitFirstWrite(ctx); err != nil {
		return nil, err
	}
	result, err := v.packer.Flush(ctx)
	if err != nil {
		return nil, err
	}
	if result.Empty() {
		return nil, nil
	}
	return &result.Flush, nil
}

// recordFlush catalogs a completed flush.
//
// The catalog entry and the slab ledger are written here rather than at queue
// time because neither is knowable until the network answers: an object ref
// only exists once the write lands, and a slab only becomes this vault's
// responsibility once something of ours is pinned in it.
func (v *Vault) recordFlush(result packer.Result) error {
	byCID := make(map[string]store.Written, len(result.Flush.Written))
	for _, written := range result.Flush.Written {
		byCID[written.CID] = written
	}
	for _, queued := range result.Records {
		written, ok := byCID[queued.Blob.CID]
		if !ok {
			return fmt.Errorf("catalog %s: the flush reported no object for it", queued.ID)
		}
		cid, err := seal.ParseCID(queued.Blob.CID)
		if err != nil {
			return fmt.Errorf("catalog %s: %w", queued.ID, err)
		}
		// The catalog carries the record's type and tags so that browsing and a
		// recovered vault agree with what recall already knows. A record whose
		// metadata cannot be read is still catalogued: losing the entry entirely
		// would cost the record's location, which is the one thing only the
		// catalog holds.
		entry := manifest.Entry{
			ID:        queued.ID,
			Kind:      queued.Kind,
			Version:   1,
			CID:       cid,
			ObjectRef: written.ObjectRef.String(),
			SlabID:    string(written.SlabID),
			Bytes:     written.Bytes,
			WrittenAt: record.Now(),
		}
		if meta, err := v.local.RankingMetaFor(queued.ID); err == nil {
			entry.Type = meta.Type
			entry.Tags = meta.Tags
		}
		if err := v.manifest.Append(entry); err != nil {
			return fmt.Errorf("catalog %s: %w", queued.ID, err)
		}
	}

	for _, slabID := range result.Flush.Slabs {
		if err := v.reclaimer.Track(slabID, len(result.Records), int64(result.Flush.Bytes())); err != nil {
			return fmt.Errorf("track slab %s: %w", slabID, err)
		}
	}
	return nil
}
