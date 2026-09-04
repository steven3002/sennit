package vault

import (
	"context"
	"fmt"
	"time"

	"github.com/steven3002/sennit/record"
)

// A LoadSessionRequest asks for a stored conversation.
//
// What it does not have is a mode that restores an agent's own state. Three
// mechanisms exist in the wild and they are not interchangeable: replaying the
// transcript works on any agent, a checkpoint of a runtime's state works only
// inside that runtime, and a summary with a preserved tail is lossy by design.
// Only the first is portable, and portability across agents is the whole reason
// a session is stored at all. So this loads, and a caller resumes by replaying
// what it gets.
type LoadSessionRequest struct {
	ID record.ID
	// Transcript reads the messages. Without it only the head is returned,
	// which is what a list or a preview wants and costs no chunk reads.
	Transcript bool
	// From starts the transcript at a message id, for continuing a replay that
	// was cut short. An id that is not in the session is an error rather than
	// an empty result: silently returning nothing would look like a session
	// that had lost its transcript.
	From string
	// Limit caps how many messages are returned. Zero means all of them.
	Limit int
	// IncludeSubagents inlines the sub-agent sessions instead of naming them.
	//
	// Off by default. A sub-agent transcript is a session in its own right and
	// can be as large as its parent, so inlining every one of them turns a
	// bounded read into an unbounded one. A load that omitted them entirely
	// would be worse still, it would return an incomplete account of what
	// happened, so they are always named.
	IncludeSubagents bool
}

// A LoadedSession is a conversation as it was stored.
type LoadedSession struct {
	Session *record.Session
	// Messages is the transcript, in order, when it was asked for.
	Messages []record.Message
	// More reports that the limit cut the transcript short, and NextFrom is the
	// message to continue from.
	More     bool
	NextFrom string
	// Subagents are the sessions this one delegated to, as references unless
	// they were asked to be inlined.
	Subagents []Subagent
	// ChunksRead is how many chunks this load fetched, and ReadFor how long
	// the whole load took.
	ChunksRead int
	ReadFor    time.Duration
}

// A Subagent is a delegated run, named or inlined.
type Subagent struct {
	// ID, Title and Kind are always present: a reference has to be enough to
	// decide whether to fetch the thing it points at.
	ID       record.ID
	Title    string
	Kind     record.SessionKind
	Messages int
	// Session and Transcript are populated only when the load asked for the
	// sub-agents to be inlined.
	Session    *record.Session
	Transcript []record.Message
}

// Inlined reports whether the sub-agent's own record came back with it.
func (s Subagent) Inlined() bool { return s.Session != nil }

// LoadSession reads a session and, when asked, its transcript.
//
// Reading the transcript fetches only the chunks the request actually needs.
// The head records each chunk's first and last message and how many it holds,
// so a load that starts part way through a conversation skips every chunk
// before it without opening one.
func (v *Vault) LoadSession(ctx context.Context, req LoadSessionRequest) (LoadedSession, error) {
	start := time.Now()
	session, err := v.Session(req.ID)
	if err != nil {
		return LoadedSession{}, err
	}
	loaded := LoadedSession{Session: session}

	if req.Transcript {
		before := v.sessionStats.ChunkReads.Load()
		messages, more, next, err := v.transcript(ctx, session, req)
		if err != nil {
			return LoadedSession{}, err
		}
		loaded.Messages, loaded.More, loaded.NextFrom = messages, more, next
		loaded.ChunksRead = int(v.sessionStats.ChunkReads.Load() - before)
	}

	for _, child := range session.Lineage.Children {
		subagent, err := v.subagent(ctx, child, req)
		if err != nil {
			return LoadedSession{}, err
		}
		loaded.Subagents = append(loaded.Subagents, subagent)
	}
	loaded.ReadFor = time.Since(start)
	return loaded, nil
}

// transcript reads the messages a request asked for.
func (v *Vault) transcript(ctx context.Context, session *record.Session, req LoadSessionRequest) ([]record.Message, bool, string, error) {
	start := startChunk(session, req.From)

	var out []record.Message
	started := req.From == ""
	for _, ref := range session.Chunks[start:] {
		if req.Limit > 0 && len(out) >= req.Limit {
			return out, true, ref.First, nil
		}
		chunk, err := v.chunk(ctx, ref.ID)
		if err != nil {
			return nil, false, "", fmt.Errorf("session %s chunk %d: %w", session.ID, ref.Seq, err)
		}
		for i := range chunk.Messages {
			if !started {
				if chunk.Messages[i].ID != req.From {
					continue
				}
				started = true
			}
			if req.Limit > 0 && len(out) >= req.Limit {
				return out, true, chunk.Messages[i].ID, nil
			}
			out = append(out, chunk.Messages[i])
		}
	}
	if !started {
		return nil, false, "", fmt.Errorf("session %s holds no message %s", session.ID, req.From)
	}
	return out, false, "", nil
}

// startChunk finds the first chunk a replay has to open.
//
// The head records the message ids at each chunk's two ends, so a replay
// resuming from a chunk boundary skips every chunk before it without reading
// one, which is the case that matters, because it is where a previous page
// stopped when the page size and the chunk size agree.
//
// A message strictly inside a chunk cannot be located from the boundaries, and
// the walk starts at the beginning. That costs a rescan rather than a refetch:
// a chunk read from the network is kept on the device, so the second pass over
// it is local. Making every resume O(1) would mean either putting every message
// id in the head, which is the transcript again, or making the resume cursor an
// offset, which stops being a stable handle the moment a session is edited.
func startChunk(session *record.Session, from string) int {
	if from == "" {
		return 0
	}
	for i, ref := range session.Chunks {
		if ref.First == from || ref.Last == from {
			return i
		}
	}
	// The message is strictly inside a chunk, and the boundaries cannot say
	// which. Starting at the beginning is the only answer available without
	// having already read the transcript, and the walk through the chunks
	// settles it, including the case where the message is not there at all.
	return 0
}

// subagent resolves one delegated run, as a reference or inlined.
func (v *Vault) subagent(ctx context.Context, id record.ID, req LoadSessionRequest) (Subagent, error) {
	child, err := v.Session(id)
	if err != nil {
		return Subagent{}, err
	}
	subagent := Subagent{
		ID:       child.ID,
		Title:    child.Title,
		Kind:     child.Kind,
		Messages: child.Counts.Messages,
	}
	if !req.IncludeSubagents {
		return subagent, nil
	}
	subagent.Session = child
	if !req.Transcript {
		return subagent, nil
	}
	messages, _, _, err := v.transcript(ctx, child, LoadSessionRequest{ID: child.ID, Transcript: true})
	if err != nil {
		return Subagent{}, err
	}
	subagent.Transcript = messages
	return subagent, nil
}

// A SourceSpan is the part of a conversation a memory came from.
type SourceSpan struct {
	Session *record.Session
	// Messages are the turns the memory was extracted from. Empty when the
	// memory names a session but no span within it.
	Messages []record.Message
	// Span is the span as it was recorded.
	Span record.Span
}

// ResolveSource resolves a memory's provenance to the conversation it came
// from and the turns within it.
//
// This is the edge that makes "why do I believe this" answerable. A memory
// carries the session id and the span it was extracted from; the session
// carries the memory in its links. One step in either direction.
func (v *Vault) ResolveSource(ctx context.Context, memory *record.Memory) (SourceSpan, error) {
	if memory.Source.SessionID == "" {
		return SourceSpan{}, fmt.Errorf("memory %s names no session", memory.ID)
	}
	id, err := record.ParseID(memory.Source.SessionID)
	if err != nil {
		return SourceSpan{}, fmt.Errorf("memory %s: %w", memory.ID, err)
	}
	session, err := v.Session(id)
	if err != nil {
		return SourceSpan{}, err
	}
	out := SourceSpan{Session: session, Span: record.ParseSpan(memory.Source.Span)}
	if out.Span.Empty() {
		return out, nil
	}

	messages, _, _, err := v.transcript(ctx, session, LoadSessionRequest{ID: id, Transcript: true})
	if err != nil {
		return SourceSpan{}, err
	}
	out.Messages = out.Span.Select(messages)
	if len(out.Messages) == 0 {
		return SourceSpan{}, fmt.Errorf("memory %s names span %s of session %s, and the session holds no such turns",
			memory.ID, memory.Source.Span, id)
	}
	return out, nil
}

// LinkMemory records on a session that a memory came out of it.
//
// The edge is stored on both records because both are asked. A memory reaches
// its conversation through its own source; a conversation that had to search
// every memory in the vault to find the ones it produced would make the
// interesting direction the expensive one.
func (v *Vault) LinkMemory(sessionID, memoryID record.ID) error {
	session, err := v.Session(sessionID)
	if err != nil {
		return err
	}
	for _, existing := range session.Links.Memories {
		if existing == memoryID {
			return nil
		}
	}
	session.Links.Memories = append(session.Links.Memories, memoryID)
	return v.reviseHead(session)
}
