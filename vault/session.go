package vault

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
)

// A SaveSessionRequest is one conversation, or the next part of one.
type SaveSessionRequest struct {
	// ID appends to a session that already exists. Left zero, a new session is
	// created and its id is returned.
	ID record.ID

	// Title, Summary and Tags are the head's searchable surface. On an append
	// each is applied only when supplied, so a caller adding turns does not
	// have to restate what it already said.
	Title   string
	Summary string
	Tags    []string
	Project record.Project
	Agent   record.Agent
	Models  []string

	// Kind defaults to a main session.
	Kind     record.SessionKind
	AgentRef *record.AgentRef
	// Lineage is set when the session is created and ignored afterwards: a
	// conversation's parentage does not change once it has one.
	Lineage record.Lineage

	// Messages are appended to the transcript in the order given.
	Messages []record.Message
	// PreservedTail names the messages kept verbatim beside the summary, for a
	// caller that wants a cheap resume. It never replaces the transcript.
	PreservedTail []string
	// TokensIn, TokensOut and DurationMS accumulate onto the head's counts.
	TokensIn, TokensOut int
	DurationMS          int64

	// Archived marks the session as put away, and Unarchive takes it back out.
	Archived, Unarchive bool

	// Durable writes this call's chunks to the network before returning,
	// instead of leaving them to the ordinary flush cadence.
	//
	// It is off by default and that is a deliberate decision rather than an
	// oversight. Every flush mints a whole slab whatever it holds, so forcing
	// one per append would bill forty mebibytes for a turn, a conversation
	// appended to two hundred times would consume eight gibibytes of a
	// forty-six gibibyte allowance, for a transcript of a few hundred
	// kilobytes. The ordinary cadence leaves a window of up to an hour in which
	// the newest turns exist on this device alone; the result says so, and
	// nothing above may present a queued session as stored on the network.
	Durable bool
}

// A SaveSessionResult reports what was written.
type SaveSessionResult struct {
	ID      record.ID
	Version int64
	// Chunks are the chunks this call created. Earlier chunks are untouched.
	Chunks []record.ChunkRef
	// Messages and Bytes are what this call appended.
	Messages int
	Bytes    int64

	// OnNetwork reports whether every chunk this call wrote reached Sia before
	// it returned. When it is false the transcript is durable on this device
	// only.
	OnNetwork bool
	// Flushed describes the write, when one happened.
	Flushed *store.Flush

	EmbedFor, SealFor, FlushFor time.Duration
}

// ErrNoSession reports that the vault holds no such session.
var ErrNoSession = errors.New("no such session")

// SaveSession stores a conversation, or appends to one that already exists.
//
// The shape is a small head plus ordered immutable chunks. An append writes new
// chunks and rewrites the head; it never touches a chunk that already exists,
// so the cost of a turn is the size of the turn and not the size of the
// conversation. That is what makes a session that has grown to hundreds of
// megabytes still cheap to add a sentence to, and it is the reason a thousand
// of them can be listed without reading one.
func (v *Vault) SaveSession(ctx context.Context, req SaveSessionRequest) (SaveSessionResult, error) {
	session, from, err := v.openSession(req)
	if err != nil {
		return SaveSessionResult{}, err
	}
	if err := validateAppend(session, req.Messages); err != nil {
		return SaveSessionResult{}, err
	}

	groups, err := record.SplitMessages(req.Messages, record.ChunkTargetBytes)
	if err != nil {
		return SaveSessionResult{}, err
	}

	var result SaveSessionResult
	owed := make(map[string]bool, len(groups))
	for _, messages := range groups {
		written, err := v.writeChunk(ctx, session, messages)
		if err != nil {
			return SaveSessionResult{}, err
		}
		result.SealFor += written.sealFor
		result.Chunks = append(result.Chunks, written.ref)
		result.Messages += written.ref.N
		result.Bytes += int64(written.ref.Bytes)
		owed[written.cid] = true
		// Queueing a chunk can itself trip the flush policy, so what has
		// already reached the network is taken from the queue's own answer
		// rather than assumed.
		settle(owed, &written.flushed.Flush)

		session.Chunks = append(session.Chunks, written.ref)
		session.Counts.Messages += written.ref.N
		session.Counts.Bytes += int64(written.ref.Bytes)
	}

	applyHeadUpdates(session, req)
	if err := session.Validate(); err != nil {
		return SaveSessionResult{}, err
	}

	start := time.Now()
	vector, err := v.embedder.EmbedOne(ctx, session.IndexText())
	if err != nil {
		return SaveSessionResult{}, err
	}
	result.EmbedFor = time.Since(start)

	if err := v.storeHead(session, vector, from); err != nil {
		return SaveSessionResult{}, err
	}
	if from == 0 && session.Lineage.ParentSession != nil {
		if err := v.adoptChild(*session.Lineage.ParentSession, session.ID); err != nil {
			return SaveSessionResult{}, err
		}
	}

	result.ID, result.Version = session.ID, session.Version
	if req.Durable && len(owed) > 0 {
		start = time.Now()
		flushed, err := v.Flush(ctx)
		result.FlushFor = time.Since(start)
		if err != nil {
			return result, err
		}
		result.Flushed = flushed
		settle(owed, flushed)
	}
	result.OnNetwork = len(owed) == 0
	return result, nil
}

// settle strikes off the chunks a flush reports having written.
//
// What is left is what this device still owes the network, and it is the only
// thing OnNetwork is allowed to be derived from: a session with one chunk still
// queued is not on the network, however many of its chunks are.
func settle(owed map[string]bool, flush *store.Flush) {
	if flush == nil {
		return
	}
	for _, written := range flush.Written {
		delete(owed, written.CID)
	}
}

// openSession resolves the head being written to, creating one if the request
// names none. It returns the version the head was read at, which is what the
// write back is conditioned on; zero means it is being created.
func (v *Vault) openSession(req SaveSessionRequest) (*record.Session, int64, error) {
	if !req.ID.IsZero() {
		session, err := v.Session(req.ID)
		if err != nil {
			return nil, 0, err
		}
		from := session.Version
		session.Version++
		session.Embedding = v.embedding()
		return session, from, nil
	}

	id, err := record.NewID()
	if err != nil {
		return nil, 0, err
	}
	kind := req.Kind
	if kind == "" {
		kind = record.SessionMain
	}
	now := record.Now()
	return &record.Session{
		ID:            id,
		Schema:        record.MessageSchema,
		SchemaVersion: record.SchemaVersion,
		Version:       1,
		Kind:          kind,
		AgentRef:      req.AgentRef,
		Lineage:       req.Lineage,
		Created:       now,
		Updated:       now,
		Embedding:     v.embedding(),
	}, 0, nil
}

// embedding names the model this vault's vectors come from.
func (v *Vault) embedding() record.Embedding {
	return record.Embedding{Model: v.opts.Model.Name, Dim: v.opts.Model.Dim}
}

// applyHeadUpdates folds a request's head fields into the session.
//
// Absent fields leave what is already there. An append is normally a few turns
// and a refreshed summary, and requiring the caller to restate the title, the
// project and the tags every time would make dropping one of them the ordinary
// mistake.
func applyHeadUpdates(session *record.Session, req SaveSessionRequest) {
	if req.Title != "" {
		session.Title = req.Title
	}
	if req.Summary != "" {
		session.Summary = req.Summary
	}
	if len(req.Tags) > 0 {
		session.Tags = local.NormalizeTags(req.Tags)
	}
	if !req.Project.Empty() {
		session.Project = req.Project
	}
	if req.Agent.Name != "" {
		session.Agent = req.Agent
	}
	if req.AgentRef != nil {
		session.AgentRef = req.AgentRef
	}
	for _, model := range req.Models {
		session.Models = appendUnique(session.Models, model)
	}
	for i := range req.Messages {
		if model := req.Messages[i].Meta.Model; model != "" {
			session.Models = appendUnique(session.Models, model)
		}
	}
	if len(req.PreservedTail) > 0 {
		session.PreservedTail = req.PreservedTail
	}
	if n := len(req.Messages); n > 0 {
		session.HeadMessage = req.Messages[n-1].ID
	}
	session.Counts.Chunks = len(session.Chunks)
	session.Counts.TokensIn += req.TokensIn
	session.Counts.TokensOut += req.TokensOut
	session.Counts.DurationMS += req.DurationMS
	switch {
	case req.Archived:
		session.Archived = true
	case req.Unarchive:
		session.Archived = false
	}
	session.Updated = record.Now()
}

// validateAppend checks a batch before any of it is sealed.
//
// Uniqueness is checked within the batch and against the boundaries the head
// already knows, which catches the mistake that actually happens, a caller
// resending turns it has already saved, without reading back the transcript.
// It does not prove uniqueness across the whole session; doing that would mean
// fetching every chunk on every append, which is the cost this shape exists to
// avoid.
func validateAppend(session *record.Session, messages []record.Message) error {
	if len(messages) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(messages))
	for _, chunk := range session.Chunks {
		seen[chunk.First], seen[chunk.Last] = true, true
	}
	delete(seen, "")

	for i := range messages {
		if err := messages[i].Validate(); err != nil {
			return err
		}
		if seen[messages[i].ID] {
			return fmt.Errorf("%w: message %s is already in session %s",
				record.ErrInvalid, messages[i].ID, session.ID)
		}
		seen[messages[i].ID] = true
	}
	return nil
}

// A writtenChunk is one sealed chunk and what queueing it cost.
type writtenChunk struct {
	ref     record.ChunkRef
	cid     string
	sealFor time.Duration
	flushed packer.Result
}

// writeChunk seals one chunk and queues it for the network.
func (v *Vault) writeChunk(ctx context.Context, session *record.Session, messages []record.Message) (writtenChunk, error) {
	id, err := record.NewID()
	if err != nil {
		return writtenChunk{}, err
	}
	chunk := &record.Chunk{
		ID:            id,
		Kind:          record.KindChunk,
		Schema:        record.MessageSchema,
		SchemaVersion: record.SchemaVersion,
		Session:       session.ID,
		Seq:           len(session.Chunks),
		Messages:      messages,
	}
	if err := chunk.Validate(); err != nil {
		return writtenChunk{}, err
	}
	body, err := record.MarshalChunk(chunk)
	if err != nil {
		return writtenChunk{}, err
	}

	start := time.Now()
	cid, err := v.sealer.CID(body)
	if err != nil {
		return writtenChunk{}, err
	}
	sealed, err := v.sealer.Seal(body)
	if err != nil {
		return writtenChunk{}, err
	}
	framed, err := seal.FrameRecord(record.KindChunk, id, sealed)
	if err != nil {
		return writtenChunk{}, err
	}
	sealFor := time.Since(start)

	if err := v.local.PutBody(id, record.KindChunk, body); err != nil {
		return writtenChunk{}, err
	}
	// Through the packer like every other record. A chunk uploaded on its own
	// would take a whole slab for a quarter of a mebibyte, and the failure is
	// silent: everything works and it costs a hundred and sixty times what it
	// should.
	flushed, err := v.packer.Add(ctx, packer.Queued{
		ID:   id,
		Kind: record.KindChunk,
		Blob: store.Blob{CID: cid.String(), Payload: framed},
	})
	if err != nil {
		return writtenChunk{}, err
	}

	return writtenChunk{
		ref: record.ChunkRef{
			ID:    id,
			Seq:   chunk.Seq,
			First: messages[0].ID,
			Last:  messages[len(messages)-1].ID,
			N:     len(messages),
			Bytes: len(body),
			CID:   cid.String(),
		},
		cid:     cid.String(),
		sealFor: sealFor,
		flushed: flushed,
	}, nil
}

// storeHead writes a head and everything that has to agree with it.
//
// The vector and the ranking metadata are what make a session findable at all,
// and they are written here rather than beside the chunks because they describe
// the head: the summary is what is embedded, and the transcript never is.
func (v *Vault) storeHead(session *record.Session, vector []float32, from int64) error {
	head, err := record.MarshalSession(session)
	if err != nil {
		return err
	}
	if err := v.local.PutSessionHead(session, head, from); err != nil {
		return err
	}
	if err := v.putVector(session.ID, vector); err != nil {
		return err
	}
	return v.local.PutRankingMeta(sessionRankingMeta(session))
}

// adoptChild records a sub-agent run on its parent.
//
// The edge is denormalised onto the parent so that loading a conversation with
// the work it delegated is one lookup. Both directions are stored because both
// are asked: a child knows its parent from its own lineage, and a parent that
// had to scan every session in the vault to find its children would make the
// ordinary load the expensive one.
func (v *Vault) adoptChild(parent, child record.ID) error {
	session, err := v.session(parent)
	if errors.Is(err, local.ErrNotFound) {
		return fmt.Errorf("%w: %s, named as the parent of %s", ErrNoSession, parent, child)
	}
	if err != nil {
		return err
	}
	for _, existing := range session.Lineage.Children {
		if existing == child {
			return nil
		}
	}
	session.Lineage.Children = append(session.Lineage.Children, child)
	return v.reviseHead(session)
}

// disownChild removes a sub-agent from its parent.
//
// A parent whose child has been forgotten is missing nothing it can act on, and
// a load that failed because of a reference to a session nobody holds would
// make one deletion cost a second, larger record.
func (v *Vault) disownChild(parent, child record.ID) error {
	session, err := v.session(parent)
	if errors.Is(err, local.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	kept := make([]record.ID, 0, len(session.Lineage.Children))
	for _, existing := range session.Lineage.Children {
		if existing != child {
			kept = append(kept, existing)
		}
	}
	if len(kept) == len(session.Lineage.Children) {
		return nil
	}
	session.Lineage.Children = kept
	return v.reviseHead(session)
}

// reviseHead writes back a head that was read, changed and is being stored
// again, conditioned on nothing else having changed it in between.
func (v *Vault) reviseHead(session *record.Session) error {
	from := session.Version
	session.Version++
	session.Updated = record.Now()

	head, err := record.MarshalSession(session)
	if err != nil {
		return err
	}
	return v.local.PutSessionHead(session, head, from)
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

// session reads a head from the device.
//
// A head is never on the network in this build. It is the one record that
// changes after it is written, and a slab holds immutable content, so the head
// lives in the device's store and in the encrypted catalog beside it. The
// transcript is on Sia; the head that names it is here.
func (v *Vault) session(id record.ID) (*record.Session, error) {
	head, err := v.local.GetSessionHead(id)
	if err != nil {
		return nil, err
	}
	v.sessionStats.HeadReads.Add(1)
	return record.UnmarshalSession(head)
}

// Session reads one session head.
func (v *Vault) Session(id record.ID) (*record.Session, error) {
	session, err := v.session(id)
	if errors.Is(err, local.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", ErrNoSession, id)
	}
	return session, err
}

// ListSessions returns session heads, newest first.
//
// It reads the listing columns and nothing else. A list of a thousand
// conversations touches no chunk, which is the property the whole record shape
// exists for: a transcript can be hundreds of megabytes, and browsing must not
// depend on how large the things being browsed are.
func (v *Vault) ListSessions(query local.SessionQuery) ([]local.SessionRow, error) {
	return v.local.ListSessions(query)
}

// CountSessions reports how many sessions this device holds.
func (v *Vault) CountSessions() (int, error) { return v.local.CountSessions() }

// A SessionStats counts what reading sessions has cost.
//
// Chunk reads are counted separately from everything else because the claim
// they support is a count and not a duration: listing a thousand sessions is
// supposed to read zero transcripts, and that is a thing to assert rather than
// to time.
type SessionStats struct {
	HeadReads  int64
	ChunkReads int64
	// ChunkBytes is the plaintext read out of chunks.
	ChunkBytes int64
}

// SessionStats reports what reading sessions has cost this vault.
func (v *Vault) SessionStats() SessionStats {
	return SessionStats{
		HeadReads:  v.sessionStats.HeadReads.Load(),
		ChunkReads: v.sessionStats.ChunkReads.Load(),
		ChunkBytes: v.sessionStats.ChunkBytes.Load(),
	}
}

// chunk reads one transcript chunk through the ordinary read hierarchy.
func (v *Vault) chunk(ctx context.Context, id record.ID) (*record.Chunk, error) {
	kind, body, _, err := v.fetchBody(ctx, id)
	if err != nil {
		return nil, err
	}
	if kind != record.KindChunk {
		return nil, fmt.Errorf("record %s is a %s, not a transcript chunk", id, kind)
	}
	chunk, err := record.UnmarshalChunk(body)
	if err != nil {
		return nil, err
	}
	v.sessionStats.ChunkReads.Add(1)
	v.sessionStats.ChunkBytes.Add(int64(len(body)))
	return chunk, nil
}

// ForgetSession removes a session and its transcript from the vault.
//
// The chunks are tombstoned in the catalog rather than erased from storage: a
// slab is shared and billed whole, so the bytes come back only when nothing
// live is left in the slab and reclamation runs.
func (v *Vault) ForgetSession(id record.ID) error {
	session, err := v.Session(id)
	if err != nil {
		return err
	}
	for _, chunk := range session.Chunks {
		if err := v.local.ForgetBody(chunk.ID); err != nil {
			return err
		}
		if err := v.manifest.Remove(chunk.ID); err != nil && !errors.Is(err, manifest.ErrNotFound) {
			return err
		}
	}
	// A parent that still names a session nobody holds cannot be loaded at all,
	// so the containment edge goes with the session it points at.
	if parent := session.Lineage.ParentSession; parent != nil {
		if err := v.disownChild(*parent, id); err != nil {
			return err
		}
	}
	if err := v.local.ForgetRankingMeta(id); err != nil {
		return err
	}
	if err := v.vectors.Remove(id); err != nil {
		return err
	}
	return v.local.ForgetSessionHead(id)
}
