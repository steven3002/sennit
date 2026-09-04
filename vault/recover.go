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

// A RecoveryReport is what a rebuild found and what it cost.
type RecoveryReport struct {
	// Objects and Frames count what the indexer held and what parsed out of it.
	Objects, Frames int
	// Recovered is how many records were decrypted and catalogued.
	Recovered int
	// Foreign counts frames that did not decrypt under this vault's key. They
	// are not errors: an account may hold objects this vault did not write.
	Foreign int
	// Damaged counts objects that stopped parsing part way. The records before
	// the damage are still recovered, which is what the framing is for.
	Damaged int
	// Unreadable counts objects the indexer returned that could not be opened
	// at all.
	Unreadable int
	// Embedded is how many records were given a fresh vector.
	Embedded int

	Elapsed time.Duration
}

// A RecoveryRequest tunes a rebuild.
type RecoveryRequest struct {
	// Embed regenerates the search vectors as records are recovered. Without
	// it the records are all present and readable but not yet findable by
	// meaning, and a later pass has to supply the vectors.
	Embed bool
	// OnRecord is called for each record as it is recovered, so a long rebuild
	// can report progress.
	OnRecord func(record.ID, int)
}

// Recover rebuilds this vault from the seed and the indexer alone.
//
// It is what makes the catalog a performance index rather than a correctness
// requirement. Every stored blob carries its own record identity next to its
// ciphertext, so the account's objects are sufficient on their own: enumerate
// them, parse the frames, decrypt with the key the phrase derives, and the
// vault is back without any of the state this device used to hold.
//
// Damage is bounded rather than fatal at every level. An object that will not
// open is skipped, an object that stops parsing yields the records before the
// break, and a frame that does not decrypt under this key is somebody else's.
func (v *Vault) Recover(ctx context.Context, req RecoveryRequest) (RecoveryReport, error) {
	if v.client == nil {
		return RecoveryReport{}, errOffline
	}
	start := time.Now()
	var report RecoveryReport

	stats, err := v.client.WalkObjectsStats(ctx, func(object sia.StoredObject) error {
		report.Objects++

		payload, err := v.client.ReadObject(object)
		if err != nil {
			return fmt.Errorf("read object %s: %w", object.Ref, err)
		}
		frames, parseErr := seal.Frames(payload)
		if parseErr != nil {
			report.Damaged++
		}
		report.Frames += len(frames)

		for _, frame := range frames {
			recovered, err := v.recoverFrame(ctx, frame, object, req)
			switch {
			case errors.Is(err, seal.ErrCorrupt):
				report.Foreign++
			case err != nil:
				return err
			default:
				report.Recovered++
				if recovered {
					report.Embedded++
				}
				if req.OnRecord != nil {
					req.OnRecord(frame.ID, report.Recovered)
				}
			}
		}
		return nil
	})
	report.Unreadable = len(stats.Unreadable)
	report.Elapsed = time.Since(start)
	if err != nil {
		return report, err
	}
	return report, nil
}

// recoverFrame restores one framed record, reporting whether it was embedded.
func (v *Vault) recoverFrame(ctx context.Context, frame seal.Frame, object sia.StoredObject, req RecoveryRequest) (bool, error) {
	body, err := v.sealer.Open(frame.Sealed)
	if err != nil {
		// A frame that will not decrypt under this vault's key belongs to
		// something else. The account is addressed by the app key and the
		// records by the phrase, and the two are not the same secret.
		return false, err
	}
	if frame.Kind == record.KindChunk {
		return false, v.recoverChunk(frame, object, body)
	}
	memory, err := record.Unmarshal(body)
	if err != nil {
		return false, fmt.Errorf("record %s: %w", frame.ID, err)
	}
	if memory.ID != frame.ID {
		return false, fmt.Errorf("record %s: the stored blob identifies itself as %s", memory.ID, frame.ID)
	}

	if err := v.local.PutBody(frame.ID, frame.Kind, body); err != nil {
		return false, err
	}
	// Ranking metadata is restored whether or not vectors are, because it costs
	// one row and the alternative is a vault whose supersessions are invisible.
	// A recovered record that has been replaced would otherwise come back as the
	// current answer until something re-embedded it.
	if err := v.local.PutRankingMeta(rankingMeta(memory)); err != nil {
		return false, err
	}
	cid, err := v.sealer.CID(body)
	if err != nil {
		return false, err
	}
	if err := v.manifest.Append(manifest.Entry{
		ID:        frame.ID,
		Kind:      frame.Kind,
		Type:      memory.Type,
		Version:   memory.Version,
		CID:       cid,
		ObjectRef: object.Ref.String(),
		SlabID:    string(object.Slab),
		Bytes:     len(frame.Sealed),
		Tags:      memory.Tags,
		WrittenAt: memory.UpdatedAt,
	}); err != nil {
		return false, err
	}
	// The slab is this vault's responsibility again: it is billed whether or
	// not anything on this device remembers pinning it.
	if err := v.reclaimer.Track(object.Slab, 1, int64(len(frame.Sealed))); err != nil {
		return false, err
	}

	if !req.Embed {
		return false, nil
	}
	vector, err := v.embedder.EmbedOne(ctx, memory.IndexText())
	if err != nil {
		return false, fmt.Errorf("re-embed %s: %w", frame.ID, err)
	}
	if err := v.putVector(frame.ID, vector); err != nil {
		return false, err
	}
	return true, nil
}

// recoverChunk restores one transcript chunk.
//
// A chunk carries no vector and no ranking metadata: it is never searched, and
// the summary that is searched lives on the head. The head itself is not
// recoverable this way at all, it is the one record that is never packed into a
// slab, so a vault rebuilt from the indexer alone gets its transcripts back and
// not the sessions that name them. Each chunk records the session it belongs to,
// so what a rebuild recovers is enough to reconstruct a head, which nothing does
// yet.
func (v *Vault) recoverChunk(frame seal.Frame, object sia.StoredObject, body []byte) error {
	chunk, err := record.UnmarshalChunk(body)
	if err != nil {
		return fmt.Errorf("chunk %s: %w", frame.ID, err)
	}
	if chunk.ID != frame.ID {
		return fmt.Errorf("chunk %s: the stored blob identifies itself as %s", chunk.ID, frame.ID)
	}
	if err := v.local.PutBody(frame.ID, frame.Kind, body); err != nil {
		return err
	}
	cid, err := v.sealer.CID(body)
	if err != nil {
		return err
	}
	if err := v.manifest.Append(manifest.Entry{
		ID:        frame.ID,
		Kind:      frame.Kind,
		Version:   1,
		CID:       cid,
		ObjectRef: object.Ref.String(),
		SlabID:    string(object.Slab),
		Bytes:     len(frame.Sealed),
		WrittenAt: record.Now(),
	}); err != nil {
		return err
	}
	return v.reclaimer.Track(object.Slab, 1, int64(len(frame.Sealed)))
}
