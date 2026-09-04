package vault

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
	"github.com/steven3002/sennit/sia"
)

// A HydrateDepth says how much of a vault is put back before the call returns.
//
// The stages exist because their costs are three orders of magnitude apart and
// only the last one is slow. Cataloguing is bounded by the number of stored
// objects, which packing keeps small; decrypting and filing is microseconds a
// record; re-embedding is hundreds of milliseconds a record and is the whole
// reason a new device cannot simply be made to wait.
type HydrateDepth string

const (
	// HydrateCatalog rebuilds the map from record ids to storage locations and
	// nothing else. Every record is addressable and none is held: a read finds
	// the catalog entry and fetches the body from the network on demand.
	HydrateCatalog HydrateDepth = "catalog"
	// HydrateMetadata additionally restores each record to this device with the
	// metadata that ranks it, and rebuilds the session heads. Everything except
	// search by meaning works, and no model has been loaded.
	HydrateMetadata HydrateDepth = "metadata"
	// HydrateIndex additionally gives every record a search vector, which is the
	// expensive stage and the one a device can run in the background.
	HydrateIndex HydrateDepth = "index"
)

// hydrateRank orders the depths so that each stage can ask whether it is
// included.
var hydrateRank = map[HydrateDepth]int{HydrateCatalog: 0, HydrateMetadata: 1, HydrateIndex: 2}

func (d HydrateDepth) includes(other HydrateDepth) bool {
	return hydrateRank[d] >= hydrateRank[other]
}

// Valid reports whether d is a depth this build knows.
func (d HydrateDepth) Valid() bool { _, ok := hydrateRank[d]; return ok }

// A HydrateRequest tunes a hydration.
type HydrateRequest struct {
	// Depth is how far to go. Empty means HydrateMetadata, which is the point at
	// which a vault is usable for everything except search by meaning.
	Depth HydrateDepth
	// OnRecord is called as each record is catalogued, so a long hydration can
	// report progress rather than appearing to hang.
	OnRecord func(record.ID, int)
}

// A HydrateReport is what a hydration recovered and what each stage cost.
type HydrateReport struct {
	Depth HydrateDepth

	// Objects, Frames and Bytes are what the network held and what came back.
	// Bytes is ciphertext, which is the figure a transfer is actually billed and
	// timed on.
	Objects, Frames int
	Bytes           int64
	// Records is how many were catalogued, Bodies how many were restored to this
	// device, Sessions how many heads were rebuilt, Embedded how many were given
	// a vector.
	Records, Bodies, Sessions, Embedded int
	// Slabs is how many the vault took responsibility for again. It is billed
	// whether or not anything on this device remembers pinning it.
	Slabs int
	// Foreign counts frames that did not decrypt under this vault's key: an
	// account may hold objects this vault did not write. Damaged counts objects
	// that stopped parsing part way, and Unreadable those that would not open at
	// all.
	Foreign, Damaged, Unreadable int

	// WalkFor is the network stage, RebuildFor the session reconstruction and
	// EmbedFor the index. They are reported apart because they are the three
	// costs a new device is actually choosing between.
	WalkFor, RebuildFor, EmbedFor time.Duration
	Elapsed                       time.Duration

	// Rebuild is the session reconstruction's own report, including the field by
	// field account of what a rebuilt head does and does not carry.
	Rebuild RebuildReport
}

// PerRecord reports what hydrating cost per record, which is the figure that
// scales.
func (r HydrateReport) PerRecord() time.Duration {
	if r.Records == 0 {
		return 0
	}
	return r.Elapsed / time.Duration(r.Records)
}

// Hydrate reconstructs a vault on a machine that has never seen it, from the
// recovery phrase and the indexer alone.
//
// It is the same mechanism as Recover and a different promise. Recover puts a
// vault back; Hydrate puts back as much of one as the caller is willing to wait
// for, and says what each stage cost. A device with ten thousand records cannot
// be asked to re-embed them before it answers its first question, that is over
// an hour, so the stages exist to make the vault useful before it is complete.
//
// What it does not do is fetch the catalog. There is no catalog on the network
// to fetch: the manifest and the search index are device-local by decision, so
// what a second device gets is the records themselves and a rebuild of
// everything that pointed at them. The cost of that decision is the whole of the
// index stage, and it is reported rather than hidden.
func (v *Vault) Hydrate(ctx context.Context, req HydrateRequest) (HydrateReport, error) {
	if v.client == nil {
		return HydrateReport{}, errOffline
	}
	if req.Depth == "" {
		req.Depth = HydrateMetadata
	}
	if !req.Depth.Valid() {
		return HydrateReport{}, fmt.Errorf("hydrate depth %q is not one of %s, %s, %s",
			req.Depth, HydrateCatalog, HydrateMetadata, HydrateIndex)
	}

	start := time.Now()
	report := HydrateReport{Depth: req.Depth}
	slabs := make(map[sia.SlabID]struct{})

	walkStart := time.Now()
	stats, err := v.client.WalkObjectsStats(ctx, func(object sia.StoredObject) error {
		report.Objects++

		payload, err := v.client.ReadObject(object)
		if err != nil {
			return fmt.Errorf("read object %s: %w", object.Ref, err)
		}
		report.Bytes += int64(len(payload))

		frames, parseErr := seal.Frames(payload)
		if parseErr != nil {
			report.Damaged++
		}
		report.Frames += len(frames)

		for _, frame := range frames {
			switch err := v.hydrateFrame(ctx, frame, object, req, &report); {
			case errors.Is(err, seal.ErrCorrupt):
				report.Foreign++
			case err != nil:
				return err
			default:
				report.Records++
				slabs[object.Slab] = struct{}{}
				if req.OnRecord != nil {
					req.OnRecord(frame.ID, report.Records)
				}
			}
		}
		return nil
	})
	report.WalkFor = time.Since(walkStart)
	report.Unreadable = len(stats.Unreadable)
	report.Slabs = len(slabs)
	if err != nil {
		report.Elapsed = time.Since(start)
		return report, err
	}

	// The heads come last because a head is assembled out of chunks that the
	// walk has only just finished restoring, and because a memory's edge back
	// into a session is only readable once the memory is here.
	if req.Depth.includes(HydrateMetadata) {
		rebuildStart := time.Now()
		rebuilt, err := v.RebuildSessions(ctx, RebuildRequest{Embed: req.Depth.includes(HydrateIndex)})
		report.RebuildFor = time.Since(rebuildStart)
		if err != nil {
			report.Elapsed = time.Since(start)
			return report, err
		}
		report.Rebuild = rebuilt
		report.Sessions = rebuilt.Stored
	}

	report.Elapsed = time.Since(start)
	return report, nil
}

// hydrateFrame restores one framed record to the requested depth.
func (v *Vault) hydrateFrame(ctx context.Context, frame seal.Frame, object sia.StoredObject, req HydrateRequest, report *HydrateReport) error {
	// The body is decrypted at every depth, including the shallowest. It is
	// microseconds, and it is the only thing that distinguishes this vault's
	// records from another installation's on the same account: the account is
	// addressed by the app key and the records by the phrase, and the two are
	// not the same secret.
	body, err := v.sealer.Open(frame.Sealed)
	if err != nil {
		return err
	}
	cid, err := v.sealer.CID(body)
	if err != nil {
		return err
	}

	entry, err := catalogEntry(frame, object, body, cid)
	if err != nil {
		return err
	}
	if err := v.manifest.Append(entry); err != nil {
		return err
	}
	// Adopted, not tracked. The record is this vault's and the slab is not:
	// another installation pinned it and is still pointing at it, so this device
	// takes on reading and accounting for the slab without taking on the right
	// to release it.
	if err := v.reclaimer.Adopt(object.Slab, 1, int64(len(frame.Sealed))); err != nil {
		return err
	}
	if !req.Depth.includes(HydrateMetadata) {
		return nil
	}

	if err := v.local.PutBody(frame.ID, frame.Kind, body); err != nil {
		return err
	}
	report.Bodies++

	if frame.Kind == record.KindChunk {
		// A chunk carries no ranking metadata and no vector: it is never
		// searched, and the summary that is searched belongs to the head.
		return nil
	}
	memory, err := record.Unmarshal(body)
	if err != nil {
		return fmt.Errorf("record %s: %w", frame.ID, err)
	}
	if err := v.local.PutRankingMeta(rankingMeta(memory)); err != nil {
		return err
	}
	if !req.Depth.includes(HydrateIndex) {
		return nil
	}

	embedStart := time.Now()
	vector, err := v.embedder.EmbedOne(ctx, memory.IndexText())
	if err != nil {
		return fmt.Errorf("re-embed %s: %w", frame.ID, err)
	}
	report.EmbedFor += time.Since(embedStart)
	if err := v.putVector(frame.ID, vector); err != nil {
		return err
	}
	report.Embedded++
	return nil
}

// catalogEntry builds the catalog line for one recovered record.
//
// A chunk and a memory carry different things worth cataloguing, and the
// difference is not cosmetic: a chunk parsed as a memory decodes successfully,
// because both are JSON and the decoder is tolerant, and then fails an identity
// check that takes the whole rebuild with it.
func catalogEntry(frame seal.Frame, object sia.StoredObject, body []byte, cid seal.CID) (manifest.Entry, error) {
	entry := manifest.Entry{
		ID:        frame.ID,
		Kind:      frame.Kind,
		Version:   1,
		CID:       cid,
		ObjectRef: object.Ref.String(),
		SlabID:    string(object.Slab),
		Bytes:     len(frame.Sealed),
		WrittenAt: record.At(object.UpdatedAt),
	}
	if frame.Kind == record.KindChunk {
		chunk, err := record.UnmarshalChunk(body)
		if err != nil {
			return entry, fmt.Errorf("chunk %s: %w", frame.ID, err)
		}
		if chunk.ID != frame.ID {
			return entry, fmt.Errorf("chunk %s: the stored blob identifies itself as %s", frame.ID, chunk.ID)
		}
		return entry, nil
	}

	memory, err := record.Unmarshal(body)
	if err != nil {
		return entry, fmt.Errorf("record %s: %w", frame.ID, err)
	}
	if memory.ID != frame.ID {
		return entry, fmt.Errorf("record %s: the stored blob identifies itself as %s", frame.ID, memory.ID)
	}
	entry.Type, entry.Version, entry.Tags = memory.Type, memory.Version, memory.Tags
	entry.WrittenAt = memory.UpdatedAt
	return entry, nil
}
