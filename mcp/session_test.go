package mcp_test

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
)

// saveConversation stores a conversation through the protocol.
func saveConversation(t *testing.T, session *sdk.ClientSession, in mcp.SaveSessionIn) mcp.SaveSessionOut {
	t.Helper()
	var out mcp.SaveSessionOut
	call(t, session, "save_session", in, &out)
	if out.URI == "" {
		t.Fatal("save_session returned no address")
	}
	return out
}

// A conversation survives the protocol with its tool correlation and its
// provider fields intact.
//
// It is the same property the record package asserts, re-asserted at the
// boundary an agent actually reaches, because the two places a portable
// transcript can be flattened are the schema and the wire, and the schema had
// already been proved.
func TestAConversationSurvivesTheProtocolWithItsToolCallsCorrelated(t *testing.T) {
	session, _ := serve(t)

	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Tide station survey",
		Summary:  "Counted the stations reporting hourly and found the registry stale.",
		Tags:     []string{"stations", "registry"},
		Messages: conversation("wire"),
		Agent:    mcp.AgentIn{Name: "sennit-test", Version: "0"},
	})
	if saved.Messages != 4 {
		t.Fatalf("save_session stored %d turns, want 4", saved.Messages)
	}

	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: saved.Transcript}, &opened)
	detail, ok := opened.Detail.(map[string]any)
	if !ok {
		t.Fatalf("a transcript came back as %T", opened.Detail)
	}
	messages, ok := detail["messages"].([]any)
	if !ok || len(messages) != 4 {
		t.Fatalf("the transcript holds %d turn(s), want 4", len(messages))
	}

	// The correlation id, on both halves, after a full round trip through JSON.
	if !strings.Contains(opened.Content, "toolu_wire") {
		t.Fatal("the tool correlation id did not survive the protocol")
	}
	if strings.Count(opened.Content, "toolu_wire") < 2 {
		t.Fatal("the correlation id is on one half of the exchange only, so nothing links them")
	}
	// And a provider field this build has never heard of.
	if !strings.Contains(opened.Content, "/home/u/tidepool") {
		t.Fatal("a provider field in the ext bag was dropped on the way through")
	}
	// The signature on a reasoning block, without which the block is not
	// replayable.
	if !strings.Contains(opened.Content, "sig_wire") {
		t.Fatal("a reasoning block's signature was dropped")
	}
}

// Appending sends only the new turns, and the head is what changes.
func TestAppendingAConversationSendsOnlyTheNewTurns(t *testing.T) {
	session, _ := serve(t)

	first := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Rollup schedule",
		Summary:  "Working out when the rollup runs.",
		Messages: conversation("first"),
	})
	second := saveConversation(t, session, mcp.SaveSessionIn{
		Session:  first.URI,
		Summary:  "Settled that the rollup runs hourly at ten past.",
		Messages: conversation("second"),
	})

	if second.URI != first.URI {
		t.Fatalf("an append minted a new conversation: %s then %s", first.URI, second.URI)
	}
	if second.Version <= first.Version {
		t.Fatalf("an append left the version at %d", second.Version)
	}
	if second.Messages != 4 {
		t.Fatalf("the append reported %d turns, want the 4 it sent", second.Messages)
	}

	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: first.URI}, &opened)
	detail := opened.Detail.(map[string]any)
	if messages := detail["messages"].(float64); messages != 8 {
		t.Fatalf("the conversation holds %v turns after two saves of four, want 8", messages)
	}
	// The title survived a save that did not restate it; the summary was
	// replaced by the one that did.
	if title := detail["title"].(string); title != "Rollup schedule" {
		t.Fatalf("an append that did not restate the title changed it to %q", title)
	}
	if summary := detail["summary"].(string); !strings.Contains(summary, "ten past") {
		t.Fatalf("an append that restated the summary did not change it: %q", summary)
	}
}

// A transcript pages, and the page names where to continue.
func TestATranscriptPagesAndSaysWhereToContinue(t *testing.T) {
	session, _ := serve(t)
	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "A conversation with eight turns",
		Summary:  "Long enough to page.",
		Messages: append(conversation("p1"), conversation("p2")...),
	})

	var page mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: saved.Transcript, Limit: 3}, &page)
	detail := page.Detail.(map[string]any)
	if messages := detail["messages"].([]any); len(messages) != 3 {
		t.Fatalf("a limit of 3 returned %d turns", len(messages))
	}
	if more, _ := detail["more"].(bool); !more {
		t.Fatal("a page that cut the transcript short did not say so")
	}
	next, _ := detail["nextFrom"].(string)
	if next == "" {
		t.Fatal("a short page named no place to continue from")
	}
	if total := detail["total"].(float64); total != 8 {
		t.Fatalf("a page of a transcript reports its total as %v, want 8", total)
	}

	var rest mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: saved.Transcript, From: next}, &rest)
	restDetail := rest.Detail.(map[string]any)
	if messages := restDetail["messages"].([]any); len(messages) != 5 {
		t.Fatalf("continuing from %s returned %d turns, want the remaining 5", next, len(messages))
	}
}

// §6(a)'s decision, asserted where an agent reaches it: a scope excludes and a
// filter does not.
//
// The two are separate arguments on one tool, and this is what stops the harder
// of the two being used as if it were the softer. Getting it wrong in either
// direction has a measured cost: an agent that cannot list its conversations, or
// one wrong tag emptying a result set.
func TestScopeExcludesWhereAFilterOnlyPrefers(t *testing.T) {
	session, _ := serve(t)

	storeMemory(t, session, mcp.RememberIn{
		Statement: "The station registry lists four stations that stopped reporting in June.",
		Context:   "Found while counting hourly reporters against the sensor feed.",
		Type:      "fact",
		Tags:      []string{"stations", "registry"},
	})
	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Station registry survey",
		Summary:  "Counted the stations reporting hourly and found the registry stale.",
		Tags:     []string{"stations", "registry"},
		Messages: conversation("scoped"),
	})

	const query = "what did we find about the station registry?"

	var unscoped mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: query}, &unscoped)
	kinds := map[string]int{}
	for _, hit := range unscoped.Results {
		kinds[hit.Kind]++
	}
	if kinds["memory"] == 0 || kinds["session"] == 0 {
		t.Fatalf("an unscoped recall returned only %v; one ranked list should hold both classes", kinds)
	}
	if unscoped.ScopeExcluded != 0 {
		t.Errorf("an unscoped recall reported %d exclusions", unscoped.ScopeExcluded)
	}

	var scoped mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: query, Scope: []string{"session"}}, &scoped)
	for _, hit := range scoped.Results {
		if hit.Kind != "session" {
			t.Errorf("a scope of session returned a %s", hit.Kind)
		}
	}
	if len(scoped.Results) == 0 || scoped.Results[0].URI != saved.URI {
		t.Fatalf("a scoped recall did not return the conversation")
	}
	// The one mechanism here that can shorten an answer says so, rather than
	// leaving a caller to infer it from a short list.
	if scoped.ScopeExcluded == 0 {
		t.Error("a scope removed records and did not report how many")
	}
	if !strings.Contains(scoped.Hint, "excludes") {
		t.Errorf("a scoped result does not explain that scope excludes: %q", scoped.Hint)
	}

	// A scope the vault does not recognise is refused rather than ignored: it is
	// not a guess, so silently dropping it would answer a different question.
	result := callRaw(t, session, "recall", mcp.RecallIn{Query: query, Scope: []string{"skill"}})
	if !result.IsError {
		t.Error("an unrecognised scope was silently ignored")
	}
	// Where an unrecognised *tag* costs nothing at all, because that one is.
	var guessed mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: query, Tags: []string{"no-such-tag"}}, &guessed)
	if len(guessed.Results) != len(unscoped.Results) {
		t.Errorf("a tag nothing carries changed the result count from %d to %d",
			len(unscoped.Results), len(guessed.Results))
	}
}

// The resume prompt: the demo as a first-class primitive.
//
// It finds the conversation, frames what is being handed over, embeds the head
// and the memories drawn from it, and replays the turns. What it cannot prove
// from here is that a host renders it as a slash command, that is M12, and it
// needs a person at an interactive session.
func TestTheResumePromptReturnsTheConversationAndWhatWasLearnedInIt(t *testing.T) {
	session, _ := serve(t)
	ctx := context.Background()

	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Why the dashboard lagged",
		Summary:  "Traced the dashboard's hour-long lag to the rollup schedule.",
		Tags:     []string{"dashboard", "rollup"},
		Messages: conversation("resume"),
		Agent:    mcp.AgentIn{Name: "claude-code", Version: "2.1.220"},
	})
	memory := storeMemory(t, session, mcp.RememberIn{
		Statement: "Tidepool's reading rollup runs hourly, at ten past the hour.",
		Context:   "Settled while tracing why the dashboard lagged the sensors by an hour.",
		Type:      "fact",
		Tags:      []string{"rollup", "schedule"},
		Session:   saved.URI,
	})

	listed, err := session.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	if len(listed.Prompts) != 1 || listed.Prompts[0].Name != mcp.ResumePrompt {
		t.Fatalf("the server offers prompts %+v, want just %q", listed.Prompts, mcp.ResumePrompt)
	}

	// No arguments at all: the most recent conversation. This is the demo, one
	// keystroke in a different agent.
	got, err := session.GetPrompt(ctx, &sdk.GetPromptParams{Name: mcp.ResumePrompt})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if !strings.Contains(got.Description, "Why the dashboard lagged") {
		t.Errorf("the prompt does not say what it is resuming: %q", got.Description)
	}

	var framing, transcript string
	embedded := map[string]string{}
	for _, message := range got.Messages {
		switch content := message.Content.(type) {
		case *sdk.TextContent:
			if framing == "" {
				framing = content.Text
			} else {
				transcript = content.Text
			}
		case *sdk.EmbeddedResource:
			embedded[content.Resource.URI] = content.Resource.Text
		}
	}

	if !strings.Contains(framing, "Traced the dashboard's hour-long lag") {
		t.Error("the framing does not carry the summary, which is what covers the turns it omits")
	}
	if !strings.Contains(framing, saved.Transcript) {
		t.Error("the framing does not name the transcript's address, so a model cannot read the rest")
	}
	if !strings.Contains(framing, "claude-code") {
		t.Error("the framing does not say which agent the conversation happened in")
	}
	if _, ok := embedded[saved.URI]; !ok {
		t.Errorf("the conversation's head was not embedded; got %v", keysOf(embedded))
	}
	if _, ok := embedded[memory]; !ok {
		t.Errorf("the memory drawn from the conversation was not embedded; got %v", keysOf(embedded))
	}
	if !strings.Contains(transcript, "Forty-one stations report hourly") {
		t.Errorf("the turns were not replayed: %.200q", transcript)
	}
	// A tool exchange is replayed as an exchange, not as a gap.
	if !strings.Contains(transcript, "called Read") || !strings.Contains(transcript, "41 stations") {
		t.Errorf("a tool call and its result did not survive the replay: %.400q", transcript)
	}

	// The embedded head is byte-identical to what `open` returns for the same
	// address, because it is the same resolver.
	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: saved.URI}, &opened)
	if embedded[saved.URI] != opened.Content {
		t.Error("the prompt embedded a different rendering of the head from the one `open` returns")
	}
}

// Resuming by topic finds the conversation without an address, and it is scoped
// to conversations because the caller named a container.
func TestResumingByTopicFindsTheConversationAndNeverAMemory(t *testing.T) {
	session, _ := serve(t)
	ctx := context.Background()

	storeMemory(t, session, mcp.RememberIn{
		Statement: "The harbour survey found silting at the eastern approach.",
		Context:   "Recorded from the harbour survey conversation in July.",
		Type:      "fact",
		Tags:      []string{"harbour", "survey"},
	})
	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Harbour survey",
		Summary:  "Surveyed the harbour approaches and found silting at the east.",
		Tags:     []string{"harbour", "survey"},
		Messages: conversation("harbour"),
	})

	got, err := session.GetPrompt(ctx, &sdk.GetPromptParams{
		Name:      mcp.ResumePrompt,
		Arguments: map[string]string{"topic": "harbour survey"},
	})
	if err != nil {
		t.Fatalf("get prompt by topic: %v", err)
	}
	if !strings.Contains(got.Description, "Harbour survey") {
		t.Fatalf("resuming by topic found something else: %q", got.Description)
	}
	var head bool
	for _, message := range got.Messages {
		if embedded, ok := message.Content.(*sdk.EmbeddedResource); ok && embedded.Resource.URI == saved.URI {
			head = true
		}
	}
	if !head {
		t.Error("resuming by topic did not embed the conversation it found")
	}

	// A topic that matches nothing well still resumes the nearest conversation,
	// because a ranked search always returns its best candidate, and it says
	// how well it matched rather than testing that against an invented cutoff.
	// One unanswerable query was ever measured, and one observation is not
	// enough to ship a threshold on; a wrong one would refuse to resume a
	// conversation the user can see is there.
	weak, err := session.GetPrompt(ctx, &sdk.GetPromptParams{
		Name:      mcp.ResumePrompt,
		Arguments: map[string]string{"topic": "a subject this vault has never held"},
	})
	if err != nil {
		t.Fatalf("resuming on a weak topic: %v", err)
	}
	if !strings.Contains(weak.Description, "closest match") {
		t.Errorf("a weak match does not present itself as one: %q", weak.Description)
	}
	if !strings.Contains(weak.Description, "similarity") {
		t.Errorf("a weak match does not report how well it matched: %q", weak.Description)
	}
	framing, _ := weak.Messages[0].Content.(*sdk.TextContent)
	if framing == nil || !strings.Contains(framing.Text, "closest match") {
		t.Error("the framing does not say how the conversation was chosen, so a user cannot " +
			"tell that this is not the one they meant")
	}

	// An empty vault is the one case that genuinely has nothing to resume, and
	// it says what to do instead.
	empty := connect(t, mcp.New(openVault(t, t.TempDir())))
	if _, err := empty.GetPrompt(ctx, &sdk.GetPromptParams{
		Name:      mcp.ResumePrompt,
		Arguments: map[string]string{"topic": "anything"},
	}); err == nil {
		t.Error("a vault holding no conversations resumed one anyway")
	}
}

// Progressive disclosure: a search returns snippets and addresses, and the whole
// record costs a second call the caller chooses to make.
func TestRecallReturnsSnippetsAndOpenReturnsTheWholeRecord(t *testing.T) {
	session, _ := serve(t)

	long := strings.Repeat("The north pier gauge reports every ten minutes and is the one the "+
		"rollup trusts when the two disagree. ", 8)
	uri := storeMemory(t, session, mcp.RememberIn{
		Statement: "The north pier gauge is authoritative when gauges disagree.",
		Context:   long,
		Type:      "insight",
		Tags:      []string{"gauge", "rollup"},
	})

	var recalled mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: "which gauge is authoritative?"}, &recalled)
	if len(recalled.Results) == 0 {
		t.Fatal("recall found nothing")
	}
	hit := recalled.Results[0]
	if !hit.Truncated {
		t.Error("a snippet of a record longer than the budget did not report being cut")
	}
	if len(hit.Snippet) > mcp.SnippetBytes+8 {
		t.Errorf("a snippet is %d bytes, over the %d-byte budget", len(hit.Snippet), mcp.SnippetBytes)
	}
	if hit.Detail != nil {
		t.Error("a concise result carried a whole record, which is what the snippet exists to avoid")
	}

	var full mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: "which gauge is authoritative?", Detail: "full"}, &full)
	if full.Results[0].Detail == nil {
		t.Error("asking for full detail returned a snippet")
	}

	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: uri}, &opened)
	if !strings.Contains(opened.Content, long) {
		t.Error("open did not return the whole context that the snippet had cut")
	}
}

// A ranked cursor continues its own query and refuses a different one.
func TestARankedCursorContinuesItsOwnQueryAndRefusesAnother(t *testing.T) {
	session, _ := serve(t)
	for i := range 6 {
		storeMemory(t, session, mcp.RememberIn{
			Statement: "Gauge reading " + string(rune('a'+i)) + " at the north pier.",
			Context:   "One of several readings written to give the ranking something to page.",
			Type:      "fact",
			Tags:      []string{"gauge"},
		})
	}

	const query = "north pier gauge readings"
	var first mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: query, Limit: 2}, &first)
	if len(first.Results) != 2 || first.NextCursor == "" {
		t.Fatalf("a limited recall returned %d result(s) and cursor %q",
			len(first.Results), first.NextCursor)
	}

	var second mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: query, Limit: 2, Cursor: first.NextCursor}, &second)
	if len(second.Results) == 0 {
		t.Fatal("continuing a ranking returned nothing")
	}
	for _, later := range second.Results {
		for _, earlier := range first.Results {
			if later.URI == earlier.URI {
				t.Errorf("%s appeared on both pages", later.URI)
			}
		}
	}

	// A cursor carried across a different query would silently skip that
	// query's best results, so it is refused where the caller can see it.
	result := callRaw(t, session, "recall", mcp.RecallIn{
		Query: "something else entirely", Cursor: first.NextCursor,
	})
	if !result.IsError {
		t.Error("a cursor from one query was honoured for another")
	}
	if !strings.Contains(resultText(result), "different query") {
		t.Errorf("the refusal does not say why: %s", resultText(result))
	}
}

// Forgetting a conversation takes its transcript and leaves the memories drawn
// from it alone.
func TestForgettingAConversationLeavesItsMemoriesStanding(t *testing.T) {
	session, _ := serve(t)

	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "A conversation to forget",
		Summary:  "Written so that forgetting it can be observed.",
		Messages: conversation("forget"),
	})
	memory := storeMemory(t, session, mcp.RememberIn{
		Statement: "The east gauge was recalibrated in July.",
		Context:   "Recorded during the conversation that is about to be forgotten.",
		Type:      "fact",
		Tags:      []string{"gauge", "calibration"},
		Session:   saved.URI,
	})

	if result := callRaw(t, session, "forget", mcp.ForgetIn{URI: saved.URI}); !result.IsError {
		t.Error("forget without confirmation removed a record")
	}

	var forgotten mcp.ForgetOut
	call(t, session, "forget", mcp.ForgetIn{URI: saved.URI, Confirm: true}, &forgotten)
	if !forgotten.Removed {
		t.Error("forget reported removing nothing")
	}

	if result := callRaw(t, session, "open", mcp.OpenIn{URI: saved.URI}); !result.IsError {
		t.Error("a forgotten conversation still opens")
	}
	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: memory}, &opened)
	if !strings.Contains(opened.Content, "recalibrated in July") {
		t.Error("forgetting a conversation took a memory drawn from it")
	}

	// Forgetting what is already gone succeeds, so a retry after a dropped
	// response does not look like a failure.
	var again mcp.ForgetOut
	call(t, session, "forget", mcp.ForgetIn{URI: saved.URI, Confirm: true}, &again)
	if again.Removed {
		t.Error("forgetting an address twice claimed to remove something twice")
	}
}

// Browse pages, and its cursor is opaque.
func TestBrowsePagesWithACursorThatIsNotAnOffset(t *testing.T) {
	session, _ := serve(t)
	for i := range 5 {
		storeMemory(t, session, mcp.RememberIn{
			Statement: "Reading " + string(rune('a'+i)) + " from the west gauge.",
			Context:   "Written to give the listing several rows to page through.",
			Type:      "fact",
			Tags:      []string{"west"},
		})
	}

	var first mcp.BrowseOut
	call(t, session, "browse", mcp.BrowseIn{Limit: 2}, &first)
	if len(first.Rows) != 2 || first.NextCursor == "" {
		t.Fatalf("a limit of 2 returned %d row(s) and cursor %q", len(first.Rows), first.NextCursor)
	}
	if _, err := record.ParseID(first.NextCursor); err == nil {
		t.Error("the cursor is a record id rather than an opaque handle")
	}

	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 6; page++ {
		var out mcp.BrowseOut
		call(t, session, "browse", mcp.BrowseIn{Limit: 2, Cursor: cursor}, &out)
		for _, row := range out.Rows {
			if seen[row.URI] {
				t.Fatalf("%s appeared on two pages", row.URI)
			}
			seen[row.URI] = true
		}
		if out.NextCursor == "" {
			break
		}
		cursor = out.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("paging saw %d of 5 records", len(seen))
	}

	result := callRaw(t, session, "browse", mcp.BrowseIn{Cursor: "not-a-cursor"})
	if !result.IsError {
		t.Error("a forged cursor was accepted")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}
