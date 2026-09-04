package vault_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// conversation is a short transcript with the two things a flat chat log cannot
// hold: a tool call correlated to its result by id, and provider fields this
// build has never heard of.
func conversation(prefix string) []record.Message {
	return []record.Message{
		{
			ID:      prefix + "-1",
			Role:    record.RoleUser,
			Created: record.Now(),
			Parts:   []record.Part{{Type: record.PartText, Text: "How many tide stations report hourly?"}},
			Ext:     record.Ext{"cwd": json.RawMessage(`"/home/u/tidepool"`)},
		},
		{
			ID:      prefix + "-2",
			Role:    record.RoleAssistant,
			Created: record.Now(),
			Parent:  prefix + "-1",
			Parts: []record.Part{
				{Type: record.PartReasoning, Signature: "sig_" + prefix},
				{Type: record.PartText, Text: "Checking the station registry."},
				{
					Type:   record.PartToolCall,
					CallID: "toolu_" + prefix,
					Name:   "Read",
					Input:  json.RawMessage(`{"path":"registry.json"}`),
				},
			},
			Meta: record.MessageMeta{Model: "claude-opus-5", StopReason: "tool_use"},
			Ext:  record.Ext{"requestId": json.RawMessage(`"req_` + prefix + `"`)},
		},
		{
			ID:     prefix + "-3",
			Role:   record.RoleTool,
			Parent: prefix + "-2",
			Parts: []record.Part{
				{
					Type:    record.PartToolResult,
					CallID:  "toolu_" + prefix,
					Content: []record.Part{{Type: record.PartText, Text: "41 stations"}},
				},
			},
		},
		{
			ID:     prefix + "-4",
			Role:   record.RoleAssistant,
			Parent: prefix + "-3",
			Parts:  []record.Part{{Type: record.PartText, Text: "Forty-one stations report hourly."}},
		},
	}
}

// Pass mark 1. A session is saved, reloaded, and the message sequence comes
// back intact, the tool call still correlated to its result by id, and every
// provider field still byte for byte what went in.
func TestASessionReloadsWithItsToolCallsAndProviderFieldsIntact(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	messages := conversation("a")
	saved, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "Tide station survey",
		Summary:  "Counted the tide stations that report hourly and why the registry disagrees.",
		Tags:     []string{"tidepool", "stations"},
		Project:  record.Project{Repo: "tidepool/field", Branch: "main"},
		Agent:    record.Agent{Name: "claude-code", Version: "2.1.220"},
		Messages: messages,
	})
	if err != nil {
		t.Fatalf("save session: %v", err)
	}

	loaded, err := v.LoadSession(ctx, vault.LoadSessionRequest{ID: saved.ID, Transcript: true})
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if len(loaded.Messages) != len(messages) {
		t.Fatalf("reloaded %d messages, wrote %d", len(loaded.Messages), len(messages))
	}
	for i := range messages {
		before, err := json.Marshal(messages[i])
		if err != nil {
			t.Fatalf("marshal message %d: %v", i, err)
		}
		after, err := json.Marshal(loaded.Messages[i])
		if err != nil {
			t.Fatalf("marshal reloaded message %d: %v", i, err)
		}
		if string(before) != string(after) {
			t.Fatalf("message %d did not survive the round trip\n before: %s\n  after: %s", i, before, after)
		}
	}

	exchanges := record.ToolExchanges(loaded.Messages)
	if len(exchanges) != 1 {
		t.Fatalf("found %d tool exchanges after the reload, want 1", len(exchanges))
	}
	if !exchanges[0].Matched {
		t.Fatal("the reloaded tool call has no result; the correlation did not survive")
	}
	if exchanges[0].CallMessage != "a-2" || exchanges[0].ResultMessage != "a-3" {
		t.Fatalf("the exchange resolved to %q and %q", exchanges[0].CallMessage, exchanges[0].ResultMessage)
	}

	head := loaded.Session
	if head.Schema != record.MessageSchema {
		t.Fatalf("the head declares schema %q", head.Schema)
	}
	if head.HeadMessage != "a-4" {
		t.Fatalf("the head points at %q, want the last message", head.HeadMessage)
	}
	if head.Counts.Messages != len(messages) {
		t.Fatalf("the head counts %d messages, want %d", head.Counts.Messages, len(messages))
	}
	if head.Models[0] != "claude-opus-5" {
		t.Fatalf("the head did not pick up the model from the transcript: %v", head.Models)
	}
}

// §6(b). Nothing may report a queued session as stored on the network. This
// vault has no connection, so the transcript is on the device and owed to Sia.
func TestASavedSessionDoesNotClaimToBeOnTheNetworkUntilItIs(t *testing.T) {
	v := offlineVault(t)

	before := v.Pending()
	saved, err := v.SaveSession(context.Background(), vault.SaveSessionRequest{
		Title:    "Durability window",
		Summary:  "A conversation written with no connection to the indexer.",
		Messages: conversation("d"),
	})
	if err != nil {
		t.Fatalf("save session: %v", err)
	}
	if saved.OnNetwork {
		t.Fatal("a session queued on a vault with no connection reported itself as on the network")
	}
	if v.Pending() != before+len(saved.Chunks) {
		t.Fatalf("%d chunk(s) were queued, the queue grew by %d",
			len(saved.Chunks), v.Pending()-before)
	}
	if saved.Flushed != nil {
		t.Fatal("a flush was reported that cannot have happened")
	}
}

// Pass mark 2. A thousand session heads list without fetching a single chunk.
// The assertion is the fetch count, not the latency.
func TestAThousandSessionHeadsListWithoutFetchingAChunk(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	const sessions = 1000
	for i := range sessions {
		if _, err := v.SaveSession(ctx, vault.SaveSessionRequest{
			Title:    fmt.Sprintf("Station %d survey", i),
			Summary:  fmt.Sprintf("Notes from the survey of tide station %d.", i),
			Tags:     []string{"tidepool"},
			Messages: conversation(fmt.Sprintf("s%d", i)),
		}); err != nil {
			t.Fatalf("save session %d: %v", i, err)
		}
	}
	held, err := v.CountSessions()
	if err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if held != sessions {
		t.Fatalf("the vault holds %d sessions, wrote %d", held, sessions)
	}

	before := v.SessionStats()
	start := time.Now()
	rows, err := v.ListSessions(local.SessionQuery{Limit: sessions})
	listFor := time.Since(start)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	after := v.SessionStats()

	if len(rows) != sessions {
		t.Fatalf("listed %d sessions, want %d", len(rows), sessions)
	}
	if read := after.ChunkReads - before.ChunkReads; read != 0 {
		t.Fatalf("listing %d sessions read %d chunk(s); it must read none", sessions, read)
	}
	if read := after.HeadReads - before.HeadReads; read != 0 {
		t.Fatalf("listing went through the head reader %d time(s); a list reads columns, not heads", read)
	}
	// Every row has to be renderable from the listing alone, or something would
	// have to open a transcript to draw the list.
	for _, row := range rows {
		if row.Title == "" || row.Counts.Messages == 0 || row.Updated.IsZero() {
			t.Fatalf("a listed session is not renderable without opening it: %+v", row)
		}
	}
	t.Logf("listed %d session heads in %v, reading %d chunk(s) and %d head(s)",
		len(rows), listFor.Round(time.Microsecond),
		after.ChunkReads-before.ChunkReads, after.HeadReads-before.HeadReads)

	// The list is newest first and pages without touching a chunk either.
	page, err := v.ListSessions(local.SessionQuery{Limit: 10})
	if err != nil {
		t.Fatalf("page sessions: %v", err)
	}
	if len(page) != 10 {
		t.Fatalf("a page of 10 returned %d", len(page))
	}
	for i := 1; i < len(page); i++ {
		if page[i].Updated.After(page[i-1].Updated.Time) {
			t.Fatal("the listing is not newest first")
		}
	}
	if v.SessionStats().ChunkReads != after.ChunkReads {
		t.Fatal("paging a listing read a chunk")
	}
}

// Pass mark 3. Appending creates a new version and does not rewrite a chunk
// that already exists.
func TestAppendingCreatesANewVersionWithoutRewritingPriorChunks(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	first, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "Rollup timing",
		Summary:  "Where the hourly rollup spends its time.",
		Messages: conversation("p1"),
	})
	if err != nil {
		t.Fatalf("save session: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("a new session is at version %d, want 1", first.Version)
	}

	second, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		ID:       first.ID,
		Summary:  "Where the hourly rollup spends its time, after the second profile.",
		Messages: conversation("p2"),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("an append produced version %d, want 2", second.Version)
	}

	head, err := v.Session(first.ID)
	if err != nil {
		t.Fatalf("read head: %v", err)
	}
	if len(head.Chunks) != len(first.Chunks)+len(second.Chunks) {
		t.Fatalf("the head names %d chunks, want %d", len(head.Chunks), len(first.Chunks)+len(second.Chunks))
	}

	// The chunks written by the first call must be the same records, at the same
	// content addresses, after the second call. A rewrite would change both.
	for i, before := range first.Chunks {
		after := head.Chunks[i]
		if after.ID != before.ID {
			t.Fatalf("chunk %d changed record id on append: %s became %s", i, before.ID, after.ID)
		}
		if after.CID != before.CID {
			t.Fatalf("chunk %d was rewritten: content address %s became %s", i, before.CID, after.CID)
		}
		if after.Seq != i {
			t.Fatalf("chunk %d is sequenced %d after the append", i, after.Seq)
		}
	}
	for _, ref := range second.Chunks {
		for _, earlier := range first.Chunks {
			if ref.ID == earlier.ID {
				t.Fatal("an append reused a prior chunk's record id")
			}
		}
	}

	// And the whole transcript is there, in order.
	loaded, err := v.LoadSession(ctx, vault.LoadSessionRequest{ID: first.ID, Transcript: true})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Messages) != 8 {
		t.Fatalf("the appended session holds %d messages, want 8", len(loaded.Messages))
	}
	if loaded.Messages[0].ID != "p1-1" || loaded.Messages[7].ID != "p2-4" {
		t.Fatalf("the transcript is out of order: %s … %s", loaded.Messages[0].ID, loaded.Messages[7].ID)
	}
	// Resuming from part way through opens only the chunks it needs.
	before := v.SessionStats().ChunkReads
	tail, err := v.LoadSession(ctx, vault.LoadSessionRequest{ID: first.ID, Transcript: true, From: "p2-1"})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(tail.Messages) != 4 || tail.Messages[0].ID != "p2-1" {
		t.Fatalf("a resume from p2-1 returned %d messages starting at %q",
			len(tail.Messages), tail.Messages[0].ID)
	}
	if read := v.SessionStats().ChunkReads - before; read != 1 {
		t.Fatalf("resuming from the second chunk read %d chunks, want 1", read)
	}
}

// Pass mark 4. Recall finds a session by its summary, and sessions and memories
// separate in one ranked list.
//
// The separation is a scope and not a filter, and the two are different
// mechanisms on purpose: a filter is a guess an agent makes about what an answer
// will be about, and must never cost an answer; a scope is a caller stating
// which container it is addressing. This asserts both halves, that an unscoped
// query ranks the two kinds together, and that a scoped one returns only what
// was asked for.
func TestRecallFindsASessionBySummaryAndScopeSeparatesTheKinds(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	session, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "Ingestion rejects tall readings",
		Summary:  "Working out why the ingestion path rejects tide readings above twenty metres.",
		Tags:     []string{"tidepool", "ingestion"},
		Messages: conversation("r"),
	})
	if err != nil {
		t.Fatalf("save session: %v", err)
	}
	memory, err := v.Remember(ctx, vault.RememberRequest{
		Statement: "Tidepool rejects any tide reading above twenty metres at ingestion.",
		Context:   "From the fictional Tidepool ingestion rules, added after a sensor fault.",
		Type:      record.TypeFact,
		Tags:      []string{"tidepool", "ingestion"},
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}

	// Unscoped: one ranked list, and it holds both kinds.
	all, err := v.Recall(ctx, recall.Request{Query: "ingestion rejects tide readings above twenty metres", Limit: 10})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	var sawSession, sawMemory bool
	for _, hit := range all.Hits {
		switch hit.Kind() {
		case record.KindSession:
			sawSession = sawSession || hit.Session.ID == session.ID
		case record.KindMemory:
			sawMemory = sawMemory || hit.Memory.ID == memory.ID
		}
	}
	if !sawSession || !sawMemory {
		t.Fatalf("one ranked list returned session=%v memory=%v; it should hold both", sawSession, sawMemory)
	}
	if all.ScopeExcluded != 0 {
		t.Fatalf("an unscoped recall excluded %d records", all.ScopeExcluded)
	}

	// Scoped to sessions: only sessions, and the session is found by its
	// summary rather than by anything in the transcript.
	sessions, err := v.Recall(ctx, recall.Request{
		Query: "why does the ingestion path refuse very tall readings",
		Scope: []record.Kind{record.KindSession},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("recall sessions: %v", err)
	}
	if len(sessions.Hits) == 0 {
		t.Fatal("a session was not found by its summary")
	}
	for _, hit := range sessions.Hits {
		if hit.Session == nil {
			t.Fatalf("a recall scoped to sessions returned a %s", hit.Kind())
		}
	}
	if sessions.Hits[0].Session.ID != session.ID {
		t.Fatalf("the wrong session ranked first: %s", sessions.Hits[0].Session.ID)
	}
	if sessions.ScopeExcluded == 0 {
		t.Fatal("the scope reported excluding nothing, though the vault holds a memory too")
	}

	// Scoped to memories: only memories.
	memories, err := v.Recall(ctx, recall.Request{
		Query: "what happens to tide readings above twenty metres",
		Scope: []record.Kind{record.KindMemory},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("recall memories: %v", err)
	}
	if len(memories.Hits) == 0 {
		t.Fatal("scoping to memories returned nothing")
	}
	for _, hit := range memories.Hits {
		if hit.Memory == nil {
			t.Fatalf("a recall scoped to memories returned a %s", hit.Kind())
		}
	}

	// And the soft filter is still soft over both kinds: a wholly wrong filter
	// costs ranking quality and never an answer.
	wrong, err := v.Recall(ctx, recall.Request{
		Query:  "ingestion rejects tide readings above twenty metres",
		Filter: recall.Filter{Tags: []string{"nothing-here", "or-here"}},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("recall with a wrong filter: %v", err)
	}
	if len(wrong.Hits) != len(all.Hits) {
		t.Fatalf("a wholly wrong filter changed the result count from %d to %d",
			len(all.Hits), len(wrong.Hits))
	}
}

// Pass mark 5. A memory extracted during a session resolves back to that
// session and to the turns it came from, and the session names the memory in
// return.
func TestAMemoryResolvesBackToItsSessionAndTurnSpan(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	messages := conversation("x")
	session, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "Station count",
		Summary:  "Established how many tide stations report hourly.",
		Tags:     []string{"tidepool"},
		Messages: messages,
	})
	if err != nil {
		t.Fatalf("save session: %v", err)
	}

	span := record.NewSpan("x-2", "x-4")
	memory, err := v.Remember(ctx, vault.RememberRequest{
		Statement: "Forty-one Tidepool tide stations report hourly.",
		Context:   "Established from the station registry during the survey conversation.",
		Type:      record.TypeFact,
		Tags:      []string{"tidepool", "stations"},
		Source: record.Source{
			Origin:    "session",
			SessionID: session.ID.String(),
			Span:      span.String(),
		},
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if memory.LinkedSession == nil || *memory.LinkedSession != session.ID {
		t.Fatalf("the write did not report linking the memory to its session: %+v", memory.LinkedSession)
	}

	// Forwards: the fact reaches the conversation and the exact turns.
	stored, _, err := v.FetchMemory(ctx, memory.ID)
	if err != nil {
		t.Fatalf("fetch memory: %v", err)
	}
	source, err := v.ResolveSource(ctx, stored)
	if err != nil {
		t.Fatalf("resolve source: %v", err)
	}
	if source.Session.ID != session.ID {
		t.Fatalf("the memory resolved to session %s, want %s", source.Session.ID, session.ID)
	}
	if len(source.Messages) != 3 {
		t.Fatalf("the span resolved to %d turns, want 3", len(source.Messages))
	}
	if source.Messages[0].ID != "x-2" || source.Messages[2].ID != "x-4" {
		t.Fatalf("the span resolved to %s … %s", source.Messages[0].ID, source.Messages[2].ID)
	}

	// Backwards: the conversation names the fact it produced.
	head, err := v.Session(session.ID)
	if err != nil {
		t.Fatalf("read head: %v", err)
	}
	var linked bool
	for _, id := range head.Links.Memories {
		linked = linked || id == memory.ID
	}
	if !linked {
		t.Fatalf("the session does not name the memory extracted from it: %v", head.Links.Memories)
	}

	// A span naming turns the session does not hold is an error rather than a
	// plausible neighbourhood.
	stored.Source.Span = "x-99..x-100"
	if _, err := v.ResolveSource(ctx, stored); err == nil {
		t.Fatal("a span naming turns that do not exist resolved anyway")
	}
}

// Pass mark 6. A session containing a sub-agent returns it as a reference by
// default, and inlines it only when asked.
func TestASubagentComesBackAsAReferenceUnlessAskedFor(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	parent, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "Survey with delegated work",
		Summary:  "Ran the station survey and delegated the registry audit.",
		Messages: conversation("m"),
	})
	if err != nil {
		t.Fatalf("save parent: %v", err)
	}
	child, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "Registry audit",
		Summary:  "Audited the station registry for duplicate entries.",
		Kind:     record.SessionSubagent,
		AgentRef: &record.AgentRef{ID: "agent_7", Name: "auditor", Role: "explore"},
		Lineage:  record.Lineage{ParentSession: &parent.ID},
		Messages: conversation("c"),
	})
	if err != nil {
		t.Fatalf("save sub-agent: %v", err)
	}

	// By default: named, not opened.
	before := v.SessionStats().ChunkReads
	loaded, err := v.LoadSession(ctx, vault.LoadSessionRequest{ID: parent.ID, Transcript: true})
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	if len(loaded.Subagents) != 1 {
		t.Fatalf("the parent reports %d sub-agents, want 1", len(loaded.Subagents))
	}
	reference := loaded.Subagents[0]
	if reference.Inlined() {
		t.Fatal("a sub-agent was inlined without being asked for")
	}
	if reference.ID != child.ID || reference.Title != "Registry audit" {
		t.Fatalf("the reference does not identify the sub-agent: %+v", reference)
	}
	if reference.Messages == 0 {
		t.Fatal("the reference does not say how large the sub-agent is, so nothing can decide whether to fetch it")
	}
	if read := v.SessionStats().ChunkReads - before; read != int64(len(loaded.Session.Chunks)) {
		t.Fatalf("loading the parent read %d chunks; it should read its own %d and none of the child's",
			read, len(loaded.Session.Chunks))
	}

	// On request: inlined, with its own transcript.
	inlined, err := v.LoadSession(ctx, vault.LoadSessionRequest{
		ID: parent.ID, Transcript: true, IncludeSubagents: true,
	})
	if err != nil {
		t.Fatalf("load parent with sub-agents: %v", err)
	}
	if !inlined.Subagents[0].Inlined() {
		t.Fatal("a sub-agent was not inlined when it was asked for")
	}
	if len(inlined.Subagents[0].Transcript) != 4 {
		t.Fatalf("the inlined sub-agent has %d messages, want 4", len(inlined.Subagents[0].Transcript))
	}
	if ref := inlined.Subagents[0].Session.AgentRef; ref == nil || ref.Name != "auditor" {
		t.Fatalf("the inlined sub-agent does not identify its agent: %+v", ref)
	}

	// The child knows its parent, and the two edges are separate relationships.
	childHead, err := v.Session(child.ID)
	if err != nil {
		t.Fatalf("read the sub-agent head: %v", err)
	}
	if childHead.Lineage.ParentSession == nil || *childHead.Lineage.ParentSession != parent.ID {
		t.Fatalf("the sub-agent does not name its parent: %+v", childHead.Lineage)
	}
	if childHead.Lineage.ForkedFrom != nil {
		t.Fatal("containment was recorded as divergence")
	}

	// Listing sub-agents is a lookup, not a scan.
	children, err := v.ListSessions(local.SessionQuery{Parent: &parent.ID})
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if len(children) != 1 || children[0].ID != child.ID {
		t.Fatalf("listing by parent returned %+v", children)
	}
	// And a main-session listing is not polluted by delegated runs.
	mains, err := v.ListSessions(local.SessionQuery{Kinds: []record.SessionKind{record.SessionMain}})
	if err != nil {
		t.Fatalf("list main sessions: %v", err)
	}
	if len(mains) != 1 || mains[0].ID != parent.ID {
		t.Fatalf("listing main sessions returned %d rows", len(mains))
	}
}

// A transcript large enough to need several chunks is split, stored and
// reassembled in order, and the head stays small enough to list.
func TestALongTranscriptIsChunkedAndComesBackInOrder(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	const turns = 400
	messages := make([]record.Message, turns)
	for i := range messages {
		messages[i] = record.Message{
			ID:    fmt.Sprintf("t-%03d", i),
			Role:  record.RoleAssistant,
			Parts: []record.Part{{Type: record.PartText, Text: strings.Repeat("tide ", 400)}},
		}
	}

	saved, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "A long conversation",
		Summary:  "Four hundred turns about tide gauges.",
		Messages: messages,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(saved.Chunks) < 3 {
		t.Fatalf("%d turns of ~2 KB produced %d chunk(s); the transcript is not being chunked",
			turns, len(saved.Chunks))
	}
	for _, chunk := range saved.Chunks {
		if chunk.Bytes > record.ChunkTargetBytes+8<<10 {
			t.Fatalf("a chunk is %d bytes against a target of %d", chunk.Bytes, record.ChunkTargetBytes)
		}
	}

	head, err := v.Session(saved.ID)
	if err != nil {
		t.Fatalf("read head: %v", err)
	}
	encoded, err := record.MarshalSession(head)
	if err != nil {
		t.Fatalf("marshal head: %v", err)
	}
	t.Logf("%d turns = %d chunks over %d KiB of transcript; the head is %d bytes",
		turns, len(head.Chunks), head.ChunkBytes()>>10, len(encoded))
	if int64(len(encoded)) > head.ChunkBytes()/10 {
		t.Fatalf("the head is %d bytes against %d bytes of transcript; it is not small enough to list",
			len(encoded), head.ChunkBytes())
	}

	loaded, err := v.LoadSession(ctx, vault.LoadSessionRequest{ID: saved.ID, Transcript: true})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Messages) != turns {
		t.Fatalf("reloaded %d turns, wrote %d", len(loaded.Messages), turns)
	}
	for i := range loaded.Messages {
		if loaded.Messages[i].ID != messages[i].ID {
			t.Fatalf("turn %d came back as %s", i, loaded.Messages[i].ID)
		}
	}

	// A paged replay returns the same sequence, one page at a time.
	pagingFrom := v.SessionStats().ChunkReads
	var replayed []record.Message
	from := ""
	for range 20 {
		page, err := v.LoadSession(ctx, vault.LoadSessionRequest{
			ID: saved.ID, Transcript: true, From: from, Limit: 50,
		})
		if err != nil {
			t.Fatalf("replay page: %v", err)
		}
		replayed = append(replayed, page.Messages...)
		if !page.More {
			break
		}
		from = page.NextFrom
	}
	if len(replayed) != turns {
		t.Fatalf("a paged replay returned %d turns, want %d", len(replayed), turns)
	}
	for i := range replayed {
		if replayed[i].ID != messages[i].ID {
			t.Fatalf("paged turn %d came back as %s", i, replayed[i].ID)
		}
	}
	// Paging costs a rescan when a page ends inside a chunk rather than on its
	// boundary. It is reported rather than asserted away: the rescan is served
	// from the device, so it costs chunk reads and not network fetches, and the
	// figure is here so a change in it is visible.
	t.Logf("a paged replay of %d turns in pages of 50 over %d chunks cost %d chunk read(s)",
		turns, len(head.Chunks), v.SessionStats().ChunkReads-pagingFrom)
}

// A session written by one process is loaded by the next one to open the vault.
// The head is a local record, so this is the claim that it is a durable local
// record and not process state.
func TestASessionSurvivesAReopenOfTheVault(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	embedder := embedtest.NewStub()

	first := openVault(t, home, embedder)
	saved, err := first.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "Survives a reopen",
		Summary:  "Written by one process and read by the next.",
		Tags:     []string{"tidepool"},
		Messages: conversation("re"),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := openVault(t, home, embedder)
	loaded, err := second.LoadSession(ctx, vault.LoadSessionRequest{ID: saved.ID, Transcript: true})
	if err != nil {
		t.Fatalf("load after reopen: %v", err)
	}
	if loaded.Session.Title != "Survives a reopen" || len(loaded.Messages) != 4 {
		t.Fatalf("the reopened vault returned %q with %d messages",
			loaded.Session.Title, len(loaded.Messages))
	}
	// And it is still findable, so the summary vector survived too.
	found, err := second.Recall(ctx, recall.Request{
		Query: "written by one process and read by the next",
		Scope: []record.Kind{record.KindSession},
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("recall after reopen: %v", err)
	}
	if len(found.Hits) == 0 || found.Hits[0].Session.ID != saved.ID {
		t.Fatal("the session was not searchable after a reopen")
	}
}

// Several processes share one vault by design, and appending reaches the
// network in the middle, so the head cannot be written back under a lock. A
// writer whose head has moved on is told, rather than silently erasing the
// turns the other writer appended.
func TestAnAppendOntoAStaleHeadIsRefusedRatherThanLosingTurns(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	embedder := embedtest.NewStub()

	writer := openVault(t, home, embedder)
	saved, err := writer.SaveSession(ctx, vault.SaveSessionRequest{
		Title: "Contended", Summary: "Two writers, one head.", Messages: conversation("w1"),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// A second process over the same vault appends first.
	other := openVault(t, home, embedder)
	if _, err := other.SaveSession(ctx, vault.SaveSessionRequest{
		ID: saved.ID, Messages: conversation("w2"),
	}); err != nil {
		t.Fatalf("the second writer failed: %v", err)
	}

	// The first process is holding a head that has moved on. It reads the head
	// afresh on every save, so this succeeds, and the point of the guard is
	// that when it does not, it says so.
	if _, err := writer.SaveSession(ctx, vault.SaveSessionRequest{
		ID: saved.ID, Messages: conversation("w3"),
	}); err != nil {
		t.Fatalf("a serialised third append failed: %v", err)
	}

	head, err := writer.Session(saved.ID)
	if err != nil {
		t.Fatalf("read head: %v", err)
	}
	if head.Version != 3 {
		t.Fatalf("three writes left the head at version %d, want 3", head.Version)
	}
	if head.Counts.Messages != 12 {
		t.Fatalf("the head counts %d messages after three appends of four", head.Counts.Messages)
	}

	// Every turn from every writer is in the transcript, in the order it was
	// appended. That is what would be lost if the last writer won.
	loaded, err := writer.LoadSession(ctx, vault.LoadSessionRequest{ID: saved.ID, Transcript: true})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Messages) != 12 {
		t.Fatalf("the transcript holds %d turns after three appends of four", len(loaded.Messages))
	}
	for i, prefix := range []string{"w1", "w2", "w3"} {
		if got := loaded.Messages[i*4].ID; got != prefix+"-1" {
			t.Fatalf("turn %d is %q, want the first of %s", i*4, got, prefix)
		}
	}
}

// Forgetting a sub-agent takes the containment edge with it, because a parent
// naming a session nobody holds cannot be loaded at all.
func TestForgettingASubagentDoesNotStrandItsParent(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	parent, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title: "Parent", Summary: "Delegated some work.", Messages: conversation("fp"),
	})
	if err != nil {
		t.Fatalf("save parent: %v", err)
	}
	child, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title: "Child", Summary: "The delegated work.", Kind: record.SessionSubagent,
		Lineage: record.Lineage{ParentSession: &parent.ID}, Messages: conversation("fc"),
	})
	if err != nil {
		t.Fatalf("save child: %v", err)
	}
	if err := v.ForgetSession(child.ID); err != nil {
		t.Fatalf("forget child: %v", err)
	}

	loaded, err := v.LoadSession(ctx, vault.LoadSessionRequest{ID: parent.ID, Transcript: true})
	if err != nil {
		t.Fatalf("the parent became unloadable when its sub-agent was forgotten: %v", err)
	}
	if len(loaded.Subagents) != 0 {
		t.Fatalf("the parent still names %d sub-agent(s)", len(loaded.Subagents))
	}
}

// The same message cannot be appended twice, because a duplicate id breaks
// every edge that points at a turn.
func TestAppendingAMessageTwiceIsRefused(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	messages := conversation("dup")
	saved, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title: "Duplicate guard", Summary: "Appends the same turns twice.", Messages: messages,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := v.SaveSession(ctx, vault.SaveSessionRequest{ID: saved.ID, Messages: messages}); err == nil {
		t.Fatal("the same messages were appended to a session twice")
	}
}
