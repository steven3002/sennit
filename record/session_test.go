package record_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steven3002/sennit/record"
)

// The whole point of the message schema is that it survives being written down
// and read back. A vendor field this build has never heard of has to come back
// byte for byte, because the alternative, dropping what cannot be named, is
// the documented lossiness of every cross-agent converter that exists.
func TestAMessageRoundTripsIncludingFieldsThisBuildDoesNotUnderstand(t *testing.T) {
	original := record.Message{
		ID:      "msg_01",
		Role:    record.RoleAssistant,
		Created: record.Now(),
		Parent:  "msg_00",
		Parts: []record.Part{
			{Type: record.PartReasoning, Text: "", Signature: "sig_abc"},
			{Type: record.PartText, Text: "Reading the file now."},
			{
				Type:   record.PartToolCall,
				CallID: "toolu_42",
				Name:   "Read",
				Input:  json.RawMessage(`{"path":"/etc/hosts","limit":10}`),
				Ext:    record.Ext{"cache_control": json.RawMessage(`{"type":"ephemeral"}`)},
			},
		},
		Meta: record.MessageMeta{
			Model:      "claude-opus-5",
			StopReason: "tool_use",
			Usage:      record.Usage{InputTokens: 1200, OutputTokens: 64},
			Skill:      "dataviz",
		},
		Ext: record.Ext{
			"isSidechain":  json.RawMessage(`true`),
			"requestId":    json.RawMessage(`"req_0199"`),
			"providerOnly": json.RawMessage(`{"nested":{"deeply":[1,2,3]}}`),
		},
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded record.Message
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	again, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(again) != string(encoded) {
		t.Fatalf("a message did not survive a round trip\n before: %s\n  after: %s", encoded, again)
	}

	// The reasoning block is empty and must still be there: its position in the
	// sequence is information even when its text is withheld.
	if len(decoded.Parts) != 3 || decoded.Parts[0].Type != record.PartReasoning {
		t.Fatalf("the empty reasoning part did not survive: %+v", decoded.Parts)
	}
	if decoded.Parts[0].Signature != "sig_abc" {
		t.Fatal("the reasoning signature was dropped, so the block cannot be replayed")
	}
	if string(decoded.Ext["providerOnly"]) != `{"nested":{"deeply":[1,2,3]}}` {
		t.Fatalf("a provider field was altered: %s", decoded.Ext["providerOnly"])
	}
	if string(decoded.Parts[2].Input) != `{"path":"/etc/hosts","limit":10}` {
		t.Fatalf("a tool call's input was altered: %s", decoded.Parts[2].Input)
	}
}

// A tool call and its result are correlated by an explicit id and by nothing
// else. Adjacency is not a link: an agent may issue several calls before any of
// them returns.
func TestToolCallsCorrelateToResultsByIdRatherThanByPosition(t *testing.T) {
	messages := []record.Message{
		{ID: "m1", Role: record.RoleAssistant, Parts: []record.Part{
			{Type: record.PartToolCall, CallID: "call_a", Name: "Read"},
			{Type: record.PartToolCall, CallID: "call_b", Name: "Grep"},
		}},
		{ID: "m2", Role: record.RoleTool, Parts: []record.Part{
			{Type: record.PartToolResult, CallID: "call_b", Content: []record.Part{
				{Type: record.PartText, Text: "two matches"},
			}},
		}},
		{ID: "m3", Role: record.RoleTool, Parts: []record.Part{
			{Type: record.PartToolResult, CallID: "call_a", IsError: true, Content: []record.Part{
				{Type: record.PartText, Text: "no such file"},
			}},
		}},
	}

	exchanges := record.ToolExchanges(messages)
	if len(exchanges) != 2 {
		t.Fatalf("found %d exchanges, want 2", len(exchanges))
	}
	byCall := map[string]record.ToolExchange{}
	for _, exchange := range exchanges {
		byCall[exchange.CallID] = exchange
	}

	a, b := byCall["call_a"], byCall["call_b"]
	if !a.Matched || a.ResultMessage != "m3" {
		t.Fatalf("call_a resolved to %q, want m3", a.ResultMessage)
	}
	if !b.Matched || b.ResultMessage != "m2" {
		t.Fatalf("call_b resolved to %q, want m2", b.ResultMessage)
	}
	if a.Name != "Read" || b.Name != "Grep" {
		t.Fatalf("the calls were mixed up: %q and %q", a.Name, b.Name)
	}
	if !a.Result.IsError {
		t.Fatal("an error result was not carried through as one")
	}
}

// A call whose result is outside the range examined is reported unmatched
// rather than silently dropped, because that is how a caller notices that a
// slice of a transcript has cut an exchange in half.
func TestAnUnansweredToolCallIsReportedRatherThanHidden(t *testing.T) {
	exchanges := record.ToolExchanges([]record.Message{
		{ID: "m1", Role: record.RoleAssistant, Parts: []record.Part{
			{Type: record.PartToolCall, CallID: "call_a", Name: "Read"},
		}},
	})
	if len(exchanges) != 1 || exchanges[0].Matched {
		t.Fatalf("an unanswered call was not reported as unanswered: %+v", exchanges)
	}
}

// The correlation id is the one field whose absence cannot be repaired later,
// so it is refused at the boundary rather than after the chunk is sealed.
func TestAToolPartWithoutACorrelationIdIsRefused(t *testing.T) {
	for _, part := range []record.Part{
		{Type: record.PartToolCall, Name: "Read"},
		{Type: record.PartToolResult},
	} {
		message := record.Message{ID: "m1", Role: record.RoleAssistant, Parts: []record.Part{part}}
		err := message.Validate()
		if err == nil {
			t.Fatalf("a %s part with no callId was accepted", part.Type)
		}
		if !strings.Contains(err.Error(), "callId") {
			t.Fatalf("the refusal did not name the missing field: %v", err)
		}
	}
}

// A part type outside this build's vocabulary is stored rather than refused.
//
// Found by running the schema against a real agent's stored transcripts: a tool
// result may contain a part type this build has never met, and a closed
// vocabulary costs the whole message rather than the one part, which is the
// opposite of what a portable record is for. The correlation id stays mandatory,
// because that one is unrecoverable; a type nobody recognises is not.
func TestAPartTypeThisBuildDoesNotKnowIsStoredRatherThanRefused(t *testing.T) {
	unknown := record.Part{
		Type: "toolReference",
		Ext:  record.Ext{"toolName": json.RawMessage(`"Bash"`)},
	}
	message := record.Message{
		ID:    "m1",
		Role:  record.RoleUser,
		Parts: []record.Part{{Type: record.PartToolResult, CallID: "c1", Content: []record.Part{unknown}}},
	}
	if err := message.Validate(); err != nil {
		t.Fatalf("a part type outside the vocabulary was refused: %v", err)
	}
	if unknown.Type.Valid() {
		t.Fatal("an unrecognised part type reports itself as one this build renders")
	}

	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reloaded record.Message
	if err := json.Unmarshal(encoded, &reloaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := reloaded.Parts[0].Content[0]
	if got.Type != unknown.Type || string(got.Ext["toolName"]) != `"Bash"` {
		t.Fatalf("the unrecognised part came back as %+v, want %+v", got, unknown)
	}
}

// A part with no discriminator at all is still refused: nothing, including a
// later build that does know the vocabulary, can interpret it.
func TestAPartWithNoTypeIsRefused(t *testing.T) {
	message := record.Message{ID: "m1", Role: record.RoleUser, Parts: []record.Part{{Text: "hello"}}}
	err := message.Validate()
	if err == nil {
		t.Fatal("a part with no type was accepted")
	}
	if !strings.Contains(err.Error(), "no type") {
		t.Fatalf("the refusal did not say what was missing: %v", err)
	}
}

// Chunking splits on size and never inside a message: half a tool result is not
// a smaller tool result.
func TestChunkingSplitsOnSizeAndNeverInsideAMessage(t *testing.T) {
	const target = 2048
	messages := make([]record.Message, 40)
	for i := range messages {
		messages[i] = record.Message{
			ID:    "m" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Role:  record.RoleUser,
			Parts: []record.Part{{Type: record.PartText, Text: strings.Repeat("x", 200)}},
		}
	}

	groups, err := record.SplitMessages(messages, target)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(groups) < 2 {
		t.Fatalf("40 messages of 200 bytes each fitted in %d chunk(s) of %d bytes", len(groups), target)
	}

	var seen int
	for _, group := range groups {
		if len(group) == 0 {
			t.Fatal("an empty chunk was produced")
		}
		encoded, err := json.Marshal(group)
		if err != nil {
			t.Fatalf("measure group: %v", err)
		}
		// One message may exceed the target on its own; several may not.
		if len(group) > 1 && len(encoded) > target+len(group)*8 {
			t.Fatalf("a chunk of %d messages is %d bytes against a target of %d",
				len(group), len(encoded), target)
		}
		seen += len(group)
	}
	if seen != len(messages) {
		t.Fatalf("chunking produced %d messages from %d", seen, len(messages))
	}
}

// A message larger than the target gets a chunk of its own rather than being cut.
func TestAnOversizedMessageGetsItsOwnChunk(t *testing.T) {
	groups, err := record.SplitMessages([]record.Message{
		{ID: "small", Role: record.RoleUser, Parts: []record.Part{{Type: record.PartText, Text: "hello"}}},
		{ID: "huge", Role: record.RoleTool, Parts: []record.Part{
			{Type: record.PartToolResult, CallID: "c1", Content: []record.Part{
				{Type: record.PartText, Text: strings.Repeat("y", 4096)},
			}},
		}},
	}, 512)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("produced %d chunks, want 2", len(groups))
	}
	if len(groups[1]) != 1 || groups[1][0].ID != "huge" {
		t.Fatalf("the oversized message was not given its own chunk: %+v", groups[1])
	}
}

// A span is the edge from a fact back to the turns that produced it, so it has
// to survive the single string a memory's source carries it in.
func TestASpanRoundTripsThroughItsStoredForm(t *testing.T) {
	for _, tc := range []struct {
		stored string
		want   record.Span
	}{
		{"", record.Span{}},
		{"msg_7", record.Span{First: "msg_7", Last: "msg_7"}},
		{"msg_7..msg_9", record.Span{First: "msg_7", Last: "msg_9"}},
		{" msg_7 .. msg_9 ", record.Span{First: "msg_7", Last: "msg_9"}},
	} {
		got := record.ParseSpan(tc.stored)
		if got != tc.want {
			t.Fatalf("ParseSpan(%q) = %+v, want %+v", tc.stored, got, tc.want)
		}
		if again := record.ParseSpan(got.String()); again != tc.want {
			t.Fatalf("%q did not survive a round trip: %+v", tc.stored, again)
		}
	}
}

// A span selects the turns between its ends, and selects nothing at all when
// its first turn is missing: an unresolvable provenance edge should say so
// rather than return a plausible neighbourhood.
func TestASpanSelectsBetweenItsEndsAndRefusesToGuess(t *testing.T) {
	messages := []record.Message{
		{ID: "m1"}, {ID: "m2"}, {ID: "m3"}, {ID: "m4"},
	}
	got := record.NewSpan("m2", "m3").Select(messages)
	if len(got) != 2 || got[0].ID != "m2" || got[1].ID != "m3" {
		t.Fatalf("selected %+v", got)
	}
	if got := record.NewSpan("absent", "m3").Select(messages); got != nil {
		t.Fatalf("a span whose first turn is absent selected %+v", got)
	}
}

// The session vocabulary is closed and locked here, because a flat list of
// sessions conflates three things that are not interchangeable.
func TestTheSessionKindVocabularyIsClosed(t *testing.T) {
	want := []record.SessionKind{"main", "subagent", "background"}
	if len(record.SessionKinds) != len(want) {
		t.Fatalf("the vocabulary holds %d kinds, want %d", len(record.SessionKinds), len(want))
	}
	for i, kind := range want {
		if record.SessionKinds[i] != kind {
			t.Fatalf("kind %d is %q, want %q", i, record.SessionKinds[i], kind)
		}
	}
	if record.SessionKind("agent").Valid() {
		t.Fatal("an unknown session kind validated")
	}
}

// A session is embedded on its title, summary and tags, and on nothing else.
// Embedding a transcript is a large cost for a poor result and is the one thing
// the summary exists to avoid.
func TestASessionIsIndexedOnItsSummaryAndNotItsTranscript(t *testing.T) {
	session := &record.Session{
		Title:   "Sia round trip in Go",
		Summary: "Wrote the encrypt-upload-fetch-decrypt loop and measured it.",
		Tags:    []string{"sia", "go"},
		Chunks: []record.ChunkRef{
			{ID: record.ID{1}, Seq: 0, N: 200, Bytes: 190000},
		},
	}
	indexed := session.IndexText()
	for _, want := range []string{"Sia round trip in Go", "encrypt-upload-fetch-decrypt", "sia go"} {
		if !strings.Contains(indexed, want) {
			t.Fatalf("the indexed text is missing %q: %q", want, indexed)
		}
	}
	if len(indexed) > record.MaxTitleBytes+record.MaxSummaryBytes+512 {
		t.Fatalf("the indexed text is %d bytes; it should be the head's surface only", len(indexed))
	}
	if session.Messages() != 200 || session.ChunkBytes() != 190000 {
		t.Fatalf("the head does not report its transcript's size: %d messages, %d bytes",
			session.Messages(), session.ChunkBytes())
	}
}

// A sub-agent run without a parent is a contradiction: the class exists to
// record containment.
func TestASubagentSessionWithoutAParentIsRefused(t *testing.T) {
	session := &record.Session{
		ID: record.ID{9}, Schema: record.MessageSchema, SchemaVersion: record.SchemaVersion,
		Version: 1, Kind: record.SessionSubagent, Title: "delegated work",
	}
	if err := session.Validate(); err == nil {
		t.Fatal("a sub-agent session with no parent was accepted")
	}
}
