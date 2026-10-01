package vault

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	// A new run is refused before any of it is written when the conversation it
	// names is not there to adopt it. Looked for only once the run was stored,
	// a missing one refused a save that had already kept the whole run, so the
	// caller was told it was not stored when it was, and a retry stored another.
	if parent := session.Lineage.ParentSession; from == 0 && parent != nil {
		if _, err := v.session(*parent); errors.Is(err, local.ErrNotFound) {
			return SaveSessionResult{}, fmt.Errorf("%w: %s, named as the parent of a new run", ErrNoSession, *parent)
		} else if err != nil {
			return SaveSessionResult{}, err
		}
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
			return SaveSessionResult{}, v.abandon(err, result.Chunks)
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
		return SaveSessionResult{}, v.abandon(err, result.Chunks)
	}

	start := time.Now()
	vector, err := v.embedder.EmbedOne(ctx, session.IndexText())
	if err != nil {
		return SaveSessionResult{}, v.abandon(err, result.Chunks)
	}
	result.EmbedFor = time.Since(start)

	if err := v.putHead(session, from); err != nil {
		return SaveSessionResult{}, v.abandon(err, result.Chunks)
	}
	// The head names the new chunks from here on, so nothing below takes them
	// back. A failure leaves a conversation that is stored and not yet findable
	// by its latest summary, which the next save of it puts right.
	if err := v.indexHead(session, vector); err != nil {
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

// abandon takes back the chunks a save wrote before it stopped, and returns why
// it stopped.
//
// A save seals and queues its chunks before it writes the head that names them,
// because a head must never name a chunk that is not there. Until that head is
// written the chunks are named by nothing, and each one still records the
// conversation it belongs to. Left queued, the next flush writes them to the
// network, and a device rebuilding the vault from there puts a conversation
// back together around them: turns the caller was told were not stored, spliced
// into a conversation that has moved on without them, or a conversation the
// user has forgotten, brought back. So they leave the queue and the device, and
// the catalog too when a flush the queue set off has already written one.
//
// A chunk a flush in progress has claimed cannot be taken back, since that
// flush has its payload, and it is reported alongside the reason the save
// stopped rather than passed over.
func (v *Vault) abandon(cause error, chunks []record.ChunkRef) error {
	var errs []error
	for _, chunk := range chunks {
		if err := v.discardChunk(chunk.ID); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return cause
	}
	return errors.Join(append([]error{cause}, errs...)...)
}

// discardChunk takes one chunk no head names out of the queue, the catalog and
// the device.
func (v *Vault) discardChunk(id record.ID) error {
	if _, err := v.packer.Withdraw(id); err != nil {
		return fmt.Errorf("take back chunk %s: %w", id, err)
	}
	if err := v.manifest.Remove(id); err != nil && !errors.Is(err, manifest.ErrNotFound) {
		return fmt.Errorf("take back chunk %s: %w", id, err)
	}
	if err := v.local.ForgetBody(id); err != nil {
		return fmt.Errorf("take back chunk %s: %w", id, err)
	}
	return nil
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
		// Add queues before it runs the flush it can set off, so a failed flush
		// leaves this chunk queued, and the save it belongs to is failing.
		if discarded := v.discardChunk(id); discarded != nil {
			return writtenChunk{}, errors.Join(err, discarded)
		}
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

// putHead writes a head, conditioned on the version it was read at, zero for
// one being created.
//
// A head that has gone since it was read was forgotten in between, and writing
// it back would bring the conversation back with it, naming a transcript the
// forget took. That is refused as ErrNoSession, which is what the writer would
// have been told had it read the head a moment later.
func (v *Vault) putHead(session *record.Session, from int64) error {
	head, err := record.MarshalSession(session)
	if err != nil {
		return err
	}
	err = v.local.PutSessionHead(session, head, from)
	if errors.Is(err, local.ErrNotFound) {
		return fmt.Errorf("%w: %s, forgotten since it was read", ErrNoSession, session.ID)
	}
	return err
}

// indexHead writes what makes a stored head findable: its vector and its
// ranking metadata.
//
// They are written beside the head rather than beside the chunks because they
// describe the head: the summary is what is embedded, and the transcript never
// is.
func (v *Vault) indexHead(session *record.Session, vector []float32) error {
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
	err := v.reviseEdges(parent, func(session *record.Session) bool {
		if slices.Contains(session.Lineage.Children, child) {
			return false
		}
		session.Lineage.Children = append(session.Lineage.Children, child)
		return true
	})
	if errors.Is(err, local.ErrNotFound) {
		return fmt.Errorf("%w: %s, named as the parent of %s", ErrNoSession, parent, child)
	}
	return err
}

// disownChild removes a sub-agent from its parent.
//
// A parent whose child has been forgotten is missing nothing it can act on, and
// a load that failed because of a reference to a session nobody holds would
// make one deletion cost a second, larger record.
func (v *Vault) disownChild(parent, child record.ID) error {
	err := v.reviseEdges(parent, func(session *record.Session) bool {
		kept := make([]record.ID, 0, len(session.Lineage.Children))
		for _, existing := range session.Lineage.Children {
			if existing != child {
				kept = append(kept, existing)
			}
		}
		if len(kept) == len(session.Lineage.Children) {
			return false
		}
		session.Lineage.Children = kept
		return true
	})
	// A parent that is not there, or was forgotten after it was read, leaves
	// nothing to take the child off.
	if errors.Is(err, local.ErrNotFound) || errors.Is(err, ErrNoSession) {
		return nil
	}
	return err
}

// headRevisions is how many times an edge is written onto a head that other
// writers keep moving on, before the writer is told.
//
// Each refusal means another write landed in between, and the head is read
// afresh each time, so among writers that each change a head once, none is
// refused more often than there are others. The calls an agent makes at once
// against one conversation are a handful, well under this.
const headRevisions = 16

// reviseEdges changes the edges a session head holds and writes it back.
//
// An edge is one id added to a list on the head or taken off it: a memory drawn
// from the conversation, or a run it delegated. Adding or removing one commutes
// with whatever else another writer did to the head, so a head that moved on
// since it was read is read again and the change made to it as it now is. An
// append is refused in the same position instead, because the turns it writes
// were chosen against the head it read, and an edge depends on nothing. Without
// this, two memories recorded against one conversation at once, which an agent
// saving what it learned does as a matter of course, refused one of the two
// after it had been stored.
//
// change reports whether it changed the head, and a head that already says
// what the change would make it say is not written again.
func (v *Vault) reviseEdges(id record.ID, change func(*record.Session) bool) error {
	for attempt := 1; ; attempt++ {
		session, err := v.session(id)
		if err != nil {
			return err
		}
		if !change(session) {
			return nil
		}
		err = v.reviseHead(session)
		if errors.Is(err, local.ErrStaleHead) && attempt < headRevisions {
			continue
		}
		return err
	}
}

// reviseHead writes back a head that was read, changed and is being stored
// again, conditioned on nothing else having changed it in between, and on its
// not having been forgotten.
func (v *Vault) reviseHead(session *record.Session) error {
	from := session.Version
	session.Version++
	session.Updated = record.Now()
	return v.putHead(session, from)
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

// CountMatchingSessions reports how many sessions ListSessions would return for
// this query across all of its pages, so a caller showing one page can say how
// many there are.
func (v *Vault) CountMatchingSessions(query local.SessionQuery) (int, error) {
	return v.local.CountMatchingSessions(query)
}

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
//
// The transcript leaves the upload queue before anything else is touched, all
// of it or none of it. A chunk waiting for a flush holds its sealed payload in
// the queue, so a conversation forgotten everywhere else would be written to
// the network by the next flush, catalogued, and rebuilt from those chunks by
// the next device to restore the vault. A chunk a flush in progress has already
// claimed refuses the whole forget with nothing removed, as Forget does for a
// memory: that flush has the payload and catalogues the chunk whatever happens
// here, and withdrawing the others first would leave a conversation neither
// forgotten nor whole. A chunk that never reached the network has no catalog
// entry, and that is nothing to remove rather than a reason to stop.
//
// The vector leaves the searchable index as well as the disk. The index is
// loaded once, when the vault opens, and a vector left in it goes on ranking a
// conversation this process can no longer read, which fails the recall that
// ranks it.
//
// The head goes last, because the head is what lists the chunks. A forget that
// fails part way leaves it in place, every step before it can be repeated, and
// forgetting the conversation again finishes the job. It is deleted only at the
// version read here: a writer that appended in between left a head naming turns
// this forget never saw, and deleting it would strand them in the queue, so the
// forget stops instead and a second one takes them too.
//
// The runs a conversation delegated are conversations of their own and are not
// forgotten with it. Each keeps naming the conversation it came from, and
// nothing reads that edge in a way that needs the conversation to be there.
func (v *Vault) ForgetSession(id record.ID) error {
	session, err := v.Session(id)
	if err != nil {
		return err
	}
	chunks := make([]record.ID, len(session.Chunks))
	for i, chunk := range session.Chunks {
		chunks[i] = chunk.ID
	}
	if _, err := v.packer.WithdrawAll(chunks); err != nil {
		if errors.Is(err, local.ErrClaimed) {
			return fmt.Errorf("forget %s: part of its transcript: %w, so nothing was removed: "+
				"forget it again once that flush has finished", id, local.ErrClaimed)
		}
		return err
	}
	for _, chunk := range chunks {
		if err := v.manifest.Remove(chunk); err != nil && !errors.Is(err, manifest.ErrNotFound) {
			return err
		}
		if err := v.local.ForgetBody(chunk); err != nil {
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
	if err := v.removeVector(id); err != nil {
		return err
	}
	if err := v.local.ForgetRankingMeta(id); err != nil {
		return err
	}
	if err := v.local.ForgetSessionHeadAt(id, session.Version); err != nil {
		if errors.Is(err, local.ErrStaleHead) {
			return fmt.Errorf("forget %s: %w while it was being forgotten, and what that writer added "+
				"is still stored: forget it again to remove the rest", id, local.ErrStaleHead)
		}
		return err
	}
	return nil
}
