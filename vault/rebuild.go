package vault

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// A FieldOrigin says where one field of a rebuilt session head came from.
//
// It exists because a rebuilt head is not the head that was written, and the
// difference is not uniform across the record: some fields come back exactly,
// some are recomputed and agree by construction, some are read out of the
// transcript and only usually agree, and some are gone. A rebuild that reported
// only success would let a portability claim be made about fields that were
// invented here.
type FieldOrigin string

const (
	// OriginChunk is a field a chunk carries in its own right, so the value is
	// the one that was written.
	OriginChunk FieldOrigin = "chunk"
	// OriginDerived is computed from the transcript and equals what the head
	// held by construction: a count, an ordering, a boundary.
	OriginDerived FieldOrigin = "derived"
	// OriginObserved is read out of the messages rather than out of the head.
	// The two normally agree and nothing guarantees that they do.
	OriginObserved FieldOrigin = "observed"
	// OriginReverse is restored from another record's edge back into this
	// session instead of from the session's own edge outward.
	OriginReverse FieldOrigin = "reverse-edge"
	// OriginSynthesised is invented here because the record cannot be stored
	// without a value.
	OriginSynthesised FieldOrigin = "synthesised"
	// OriginLost is carried by nothing that reaches the network.
	OriginLost FieldOrigin = "lost"
)

// HeadFields lists every field of a session head in a stable order, so a report
// covers the whole record rather than the parts a rebuild happened to fill.
var HeadFields = []string{
	"id", "schema", "schemaVersion", "version",
	"title", "summary", "tags", "project",
	"created", "updated", "archived",
	"agent", "agentRef", "models", "counts.messages", "counts.chunks",
	"counts.bytes", "counts.tokens", "counts.durationMs",
	"kind", "lineage", "headMessage", "preservedTail", "chunks",
	"links.memories", "embedding",
}

// A RebuiltHead is one reconstructed session and an account of where each of
// its fields came from.
type RebuiltHead struct {
	Session *record.Session
	// Origins maps each name in HeadFields to how this rebuild came by it.
	Origins map[string]FieldOrigin
	// Chunks and Messages are what the reconstruction read.
	Chunks, Messages int
	// Existing reports that this device already held a head for the session, so
	// the rebuilt one was not stored.
	Existing bool
}

// A RebuildRequest tunes a session rebuild.
type RebuildRequest struct {
	// Overwrite replaces heads this device already holds. Off by default: a
	// rebuilt head is strictly poorer than a written one, so a vault that still
	// has the real head must not have it replaced by a reconstruction.
	Overwrite bool
	// Embed gives each rebuilt head a search vector. Without it the session is
	// present and loadable but not findable by meaning.
	Embed bool
}

// A RebuildReport is what a session rebuild recovered and at what fidelity.
type RebuildReport struct {
	// Chunks is how many transcript chunks the rebuild read, and Sessions how
	// many distinct conversations they belonged to.
	Chunks, Sessions int
	// Stored is how many heads were written to this device; Skipped counts the
	// sessions this device already held a head for.
	Stored, Skipped int
	// Gaps counts sessions whose chunk sequence has a hole in it, which means
	// part of the transcript is missing rather than the whole of it.
	Gaps int
	// Unreadable counts catalogued chunks that could not be read at all. They
	// are skipped rather than fatal: one chunk that will not come back should
	// cost its own messages and not every conversation in the vault.
	Unreadable int
	// Heads are the reconstructions, in the order the sessions were first seen.
	Heads []RebuiltHead
	// Origins is the field verdict across every head: the weakest origin any
	// head reported for that field. It is what a portability claim has to be
	// written against.
	Origins map[string]FieldOrigin

	Elapsed time.Duration
}

// RebuildSessions reconstructs session heads from transcript chunks alone.
//
// A head is the one record this build never packs into a slab, so a device that
// has lost its store finds the conversations on the network and not the sessions
// naming them. Each chunk records the session it belongs to and its position in
// the transcript, which is enough to put the conversation back in order, and
// deliberately not enough to put the head back. What the head held about the
// conversation rather than about its messages, a reader wrote and nothing on the
// network copied.
//
// The report is therefore the point of this function as much as the heads are.
// It says field by field which values came back, which were recomputed, which
// were read out of the messages and which were invented here, so that a claim
// about a rebuilt session can be made about the fields that survived rather than
// about the record as a whole.
func (v *Vault) RebuildSessions(ctx context.Context, req RebuildRequest) (RebuildReport, error) {
	start := time.Now()
	report := RebuildReport{Origins: map[string]FieldOrigin{}}

	groups, order, err := v.groupChunksBySession(ctx, &report)
	if err != nil {
		return report, err
	}
	report.Sessions = len(order)

	// The reverse edges are gathered once for the whole rebuild rather than per
	// session: a memory names the conversation it came from, so the vault's
	// memories are read in one pass and every session finds its own links in the
	// result.
	backlinks, err := v.memoryBacklinks()
	if err != nil {
		return report, err
	}

	for _, sessionID := range order {
		head, err := v.rebuildHead(ctx, sessionID, groups[sessionID], backlinks[sessionID], req)
		if err != nil {
			return report, err
		}
		if hasSequenceGap(groups[sessionID]) {
			report.Gaps++
		}
		if head.Existing {
			report.Skipped++
		} else {
			report.Stored++
		}
		report.Heads = append(report.Heads, head)
		mergeOrigins(report.Origins, head.Origins)
	}

	report.Elapsed = time.Since(start)
	return report, nil
}

// groupChunksBySession reads every catalogued chunk and files it under the
// session it names.
//
// Ordering is by the chunk's own sequence number and never by the order the
// catalog holds them in: the catalog reflects when a record was written or
// recovered, and a rebuild that trusted it would reorder a transcript whose
// chunks came back from the network in a different order than they were made.
func (v *Vault) groupChunksBySession(ctx context.Context, report *RebuildReport) (map[record.ID][]*record.Chunk, []record.ID, error) {
	groups := make(map[record.ID][]*record.Chunk)
	var order []record.ID

	ids, err := v.chunkIDs()
	if err != nil {
		return nil, nil, err
	}
	for _, id := range ids {
		chunk, err := v.chunk(ctx, id)
		if err != nil {
			// Damage is bounded here for the same reason it is bounded in a
			// recovery: the alternative is that one chunk nobody can read costs
			// every conversation in the vault. What it does cost is recorded,
			// and the hole it leaves is reported by the sequence check below.
			report.Unreadable++
			continue
		}
		if chunk.Session.IsZero() {
			// A chunk that names no session cannot be attributed to one. It is
			// refused at write time, so this is a record from a build that did
			// not require it rather than an ordinary case.
			continue
		}
		if _, seen := groups[chunk.Session]; !seen {
			order = append(order, chunk.Session)
		}
		groups[chunk.Session] = append(groups[chunk.Session], chunk)
		report.Chunks++
	}

	for _, chunks := range groups {
		sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].Seq < chunks[j].Seq })
	}
	return groups, order, nil
}

// chunkIDs lists every transcript chunk this vault can reach, from the catalog
// and from the device, in a stable order.
//
// Both sources are consulted because they are populated at different moments. A
// hydrate that has only rebuilt the catalog knows of chunks it does not hold; a
// vault that has queued chunks but not flushed them holds chunks the catalog has
// never heard of. Either alone would silently rebuild part of a conversation.
func (v *Vault) chunkIDs() ([]record.ID, error) { return v.recordIDs(record.KindChunk) }

func (v *Vault) recordIDs(kind record.Kind) ([]record.ID, error) {
	var out []record.ID
	seen := make(map[record.ID]bool)

	for _, entry := range v.manifest.Entries() {
		if entry.Kind != kind || seen[entry.ID] {
			continue
		}
		seen[entry.ID] = true
		out = append(out, entry.ID)
	}
	held, err := v.local.BodyIDsOfKind(kind)
	if err != nil {
		return nil, err
	}
	for _, id := range held {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// hasSequenceGap reports whether a session's chunks are missing one in the
// middle.
//
// It matters more than a short transcript does. A conversation whose last chunks
// never reached the network is visibly incomplete; one missing a chunk from the
// middle reads as continuous and is not.
func hasSequenceGap(chunks []*record.Chunk) bool {
	for i, chunk := range chunks {
		if chunk.Seq != i {
			return true
		}
	}
	return false
}

// memoryBacklinks collects, for each session, the memories that name it.
//
// This is the one head field with a second copy on the network. A memory
// records the conversation it was extracted from, so the edge the head lost
// outward can be walked back inward. It is not the same set: it holds every
// memory that named the session and not necessarily every memory the head
// listed, and a session with no memories is indistinguishable from one whose
// links were lost.
func (v *Vault) memoryBacklinks() (map[record.ID][]record.ID, error) {
	out := make(map[record.ID][]record.ID)
	ids, err := v.recordIDs(record.KindMemory)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		body, err := v.local.GetBody(id)
		if errors.Is(err, local.ErrNotFound) {
			// Only what the device already holds is read. A backlink is worth a
			// local lookup and not a network fetch of every memory in the vault:
			// the edge is a convenience that the transcript does not depend on.
			continue
		}
		if err != nil {
			return nil, err
		}
		memory, err := record.Unmarshal(body)
		if err != nil {
			return nil, fmt.Errorf("read memory %s: %w", id, err)
		}
		if memory.Source.SessionID == "" {
			continue
		}
		sessionID, err := record.ParseID(memory.Source.SessionID)
		if err != nil {
			continue
		}
		out[sessionID] = append(out[sessionID], memory.ID)
	}
	return out, nil
}

// rebuildHead reconstructs one session from its chunks.
func (v *Vault) rebuildHead(ctx context.Context, id record.ID, chunks []*record.Chunk, backlinks []record.ID, req RebuildRequest) (RebuiltHead, error) {
	if len(chunks) == 0 {
		return RebuiltHead{}, fmt.Errorf("session %s: no chunks to rebuild from", id)
	}

	built := RebuiltHead{Origins: map[string]FieldOrigin{}, Chunks: len(chunks)}
	for _, field := range HeadFields {
		built.Origins[field] = OriginLost
	}

	session := &record.Session{
		ID:            id,
		Schema:        chunks[0].Schema,
		SchemaVersion: chunks[0].SchemaVersion,
		Version:       1,
		Kind:          record.SessionMain,
	}
	// The id, the schema and the position of every chunk are the three things a
	// chunk was given so that it could be attributed without its head, and they
	// are exactly the three that come back.
	built.Origins["id"] = OriginChunk
	built.Origins["schema"] = OriginChunk
	built.Origins["schemaVersion"] = OriginChunk
	// A head's version counts revisions of the head. Nothing counts them but the
	// head, so a rebuild starts again from one.
	built.Origins["version"] = OriginSynthesised
	// A sub-agent run and a background run are head-only facts. Every rebuilt
	// session therefore looks like a conversation a user drove.
	built.Origins["kind"] = OriginSynthesised

	observed := observeTranscript(chunks)
	for position, chunk := range chunks {
		ref, err := v.chunkRef(chunk, position)
		if err != nil {
			return RebuiltHead{}, err
		}
		session.Chunks = append(session.Chunks, ref)
	}
	// The chunk list is the transcript, and rebuilding it is what this function
	// is for. Every field of a ChunkRef is a property of the chunk itself.
	built.Origins["chunks"] = OriginDerived
	built.Messages = observed.messages

	session.Counts = record.Counts{
		Messages:  observed.messages,
		Chunks:    len(chunks),
		Bytes:     observed.bytes,
		TokensIn:  observed.tokensIn,
		TokensOut: observed.tokensOut,
	}
	built.Origins["counts.messages"] = OriginDerived
	built.Origins["counts.chunks"] = OriginDerived
	built.Origins["counts.bytes"] = OriginDerived
	if observed.tokensIn > 0 || observed.tokensOut > 0 {
		// Token counts on the head were supplied by the caller; these are summed
		// from what the messages recorded. They are the same quantity measured by
		// two parties and are not required to agree.
		built.Origins["counts.tokens"] = OriginObserved
	}

	if !observed.first.IsZero() {
		session.Created, session.Updated = record.At(observed.first), record.At(observed.last)
		// When the first turn happened, not when the session was opened.
		built.Origins["created"] = OriginObserved
		built.Origins["updated"] = OriginObserved
	} else {
		now := record.Now()
		session.Created, session.Updated = now, now
		built.Origins["created"] = OriginSynthesised
		built.Origins["updated"] = OriginSynthesised
	}

	if len(observed.models) > 0 {
		session.Models = observed.models
		built.Origins["models"] = OriginObserved
	}
	if observed.headMessage != "" {
		session.HeadMessage = observed.headMessage
		built.Origins["headMessage"] = OriginDerived
	}

	links := mergeIDs(backlinks, observed.memoryRefs)
	if len(links) > 0 {
		session.Links.Memories = links
		built.Origins["links.memories"] = OriginReverse
	}

	// A head with no title cannot be stored at all, and a title is half of what
	// makes a session findable. There is no summariser in this system to write
	// one, the vault runs no model of its own, so the first thing the user
	// said is used, which is the best signal available and is not what the head
	// held.
	session.Title = synthesiseTitle(chunks, id)
	built.Origins["title"] = OriginSynthesised

	if err := session.Validate(); err != nil {
		return RebuiltHead{}, fmt.Errorf("rebuild session %s: %w", id, err)
	}
	built.Session = session

	if !req.Overwrite {
		if _, err := v.local.GetSessionHead(id); err == nil {
			built.Existing = true
			return built, nil
		} else if !errors.Is(err, local.ErrNotFound) {
			return RebuiltHead{}, err
		}
	}

	if err := v.storeRebuiltHead(ctx, session, req.Embed); err != nil {
		return RebuiltHead{}, err
	}
	if req.Embed {
		session.Embedding = v.embedding()
		built.Origins["embedding"] = OriginSynthesised
	}
	return built, nil
}

// storeRebuiltHead writes a reconstructed head over whatever this device holds.
func (v *Vault) storeRebuiltHead(ctx context.Context, session *record.Session, embed bool) error {
	if embed {
		session.Embedding = v.embedding()
	}
	head, err := record.MarshalSession(session)
	if err != nil {
		return err
	}
	// The expected version is taken from what is on the device rather than
	// assumed to be absent, so a rebuild can be re-run over its own output
	// instead of failing the second time.
	expect := int64(0)
	if existing, err := v.local.GetSessionHead(session.ID); err == nil {
		previous, err := record.UnmarshalSession(existing)
		if err != nil {
			return err
		}
		expect = previous.Version
		session.Version = previous.Version + 1
		if head, err = record.MarshalSession(session); err != nil {
			return err
		}
	} else if !errors.Is(err, local.ErrNotFound) {
		return err
	}

	if err := v.local.PutSessionHead(session, head, expect); err != nil {
		return err
	}
	if err := v.local.PutRankingMeta(sessionRankingMeta(session)); err != nil {
		return err
	}
	if !embed {
		return nil
	}
	vector, err := v.embedder.EmbedOne(ctx, session.IndexText())
	if err != nil {
		return fmt.Errorf("embed rebuilt session %s: %w", session.ID, err)
	}
	return v.putVector(session.ID, vector)
}

// chunkRef rebuilds what the head held about one chunk. Every field of it is a
// property of the chunk, which is why this half survives intact.
//
// The position is the chunk's place in the list being built and not the sequence
// number the chunk carries. The two are the same for a complete transcript and
// differ when one is missing: a head whose chunk list is not contiguous cannot
// be stored at all, so a conversation with a hole in it would otherwise be lost
// whole rather than short. The chunks keep their own original numbering, so what
// is missing stays visible in the records themselves.
func (v *Vault) chunkRef(chunk *record.Chunk, position int) (record.ChunkRef, error) {
	ref := record.ChunkRef{
		ID:  chunk.ID,
		Seq: position,
		N:   len(chunk.Messages),
	}
	if len(chunk.Messages) > 0 {
		ref.First = chunk.Messages[0].ID
		ref.Last = chunk.Messages[len(chunk.Messages)-1].ID
	}
	body, err := record.MarshalChunk(chunk)
	if err != nil {
		return record.ChunkRef{}, err
	}
	ref.Bytes = len(body)
	// The content address is keyed from the vault seed, so a second device
	// derives the same one from the same phrase. That is what lets a rebuilt
	// head carry the value a copy is verified against instead of an empty field.
	cid, err := v.sealer.CID(body)
	if err != nil {
		return record.ChunkRef{}, err
	}
	ref.CID = cid.String()
	return ref, nil
}

// transcriptFacts are what the messages themselves say about the conversation.
type transcriptFacts struct {
	messages    int
	bytes       int64
	tokensIn    int
	tokensOut   int
	first, last time.Time
	models      []string
	memoryRefs  []record.ID
	headMessage string
}

// observeTranscript reads out of the messages everything the head also held.
func observeTranscript(chunks []*record.Chunk) transcriptFacts {
	var facts transcriptFacts
	for _, chunk := range chunks {
		if body, err := record.MarshalChunk(chunk); err == nil {
			facts.bytes += int64(len(body))
		}
		for i := range chunk.Messages {
			message := &chunk.Messages[i]
			facts.messages++
			facts.tokensIn += message.Meta.Usage.InputTokens + message.Meta.Usage.CacheReadTokens
			facts.tokensOut += message.Meta.Usage.OutputTokens
			if model := message.Meta.Model; model != "" {
				facts.models = appendUnique(facts.models, model)
			}
			for _, ref := range message.Meta.MemoryRefs {
				facts.memoryRefs = append(facts.memoryRefs, ref)
			}
			if at := message.Created.Time; !at.IsZero() {
				if facts.first.IsZero() || at.Before(facts.first) {
					facts.first = at
				}
				if at.After(facts.last) {
					facts.last = at
				}
			}
			facts.headMessage = message.ID
		}
	}
	return facts
}

// TitleFromTranscript is how long a synthesised title is allowed to be. It is
// short enough to read in a list and long enough to tell two conversations
// apart.
const TitleFromTranscript = 72

// synthesiseTitle invents a title from the first thing the user said.
func synthesiseTitle(chunks []*record.Chunk, id record.ID) string {
	for _, chunk := range chunks {
		for i := range chunk.Messages {
			if chunk.Messages[i].Role != record.RoleUser {
				continue
			}
			if text := firstText(chunk.Messages[i].Parts); text != "" {
				return truncateTitle(text)
			}
		}
	}
	return "Recovered session " + id.String()[:8]
}

// firstText finds the first prose in a message's parts, descending into a tool
// result's own parts so that a transcript whose opening turn is an attachment
// still yields something readable.
func firstText(parts []record.Part) string {
	for i := range parts {
		if parts[i].Type == record.PartText {
			if text := strings.TrimSpace(parts[i].Text); text != "" {
				return text
			}
		}
		if nested := firstText(parts[i].Content); nested != "" {
			return nested
		}
	}
	return ""
}

func truncateTitle(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= TitleFromTranscript {
		return text
	}
	return strings.TrimSpace(string(runes[:TitleFromTranscript])) + "…"
}

// mergeIDs unions two id lists, keeping the order of the first.
func mergeIDs(first, second []record.ID) []record.ID {
	seen := make(map[record.ID]bool, len(first)+len(second))
	out := make([]record.ID, 0, len(first)+len(second))
	for _, list := range [][]record.ID{first, second} {
		for _, id := range list {
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// originRank orders the origins from strongest to weakest, so that a report over
// many heads can take the weakest answer any of them gave.
var originRank = map[FieldOrigin]int{
	OriginChunk:       0,
	OriginDerived:     1,
	OriginObserved:    2,
	OriginReverse:     3,
	OriginSynthesised: 4,
	OriginLost:        5,
}

// mergeOrigins folds one head's verdict into the report's, keeping the weakest.
//
// The weakest is the only safe summary. A claim written from the best case
// would be a claim about the luckiest session in the vault.
func mergeOrigins(into, from map[string]FieldOrigin) {
	for field, origin := range from {
		existing, seen := into[field]
		if !seen || originRank[origin] > originRank[existing] {
			into[field] = origin
		}
	}
}
