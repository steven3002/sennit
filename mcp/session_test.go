package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

	// `recent`: the most recent conversation, with nothing asked. It is the one
	// word that carries on in a different agent without choosing.
	got, err := session.GetPrompt(ctx, &sdk.GetPromptParams{
		Name:      mcp.ResumePrompt,
		Arguments: map[string]string{"session": mcp.ResumeRecent},
	})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if !strings.Contains(got.Description, "Why the dashboard lagged") {
		t.Errorf("the prompt does not say what it is resuming: %q", got.Description)
	}

	var texts []string
	embedded := map[string]string{}
	for _, message := range got.Messages {
		switch content := message.Content.(type) {
		case *sdk.TextContent:
			texts = append(texts, content.Text)
		case *sdk.EmbeddedResource:
			embedded[content.Resource.URI] = content.Resource.Text
		}
	}
	if len(texts) != 3 {
		t.Fatalf("the prompt returned %d text message(s), want the framing, the turns and the closing", len(texts))
	}
	framing, transcript, closing := texts[0], texts[1], texts[2]

	// The closing is the last thing the model reads, and it asks for a short
	// answer and a wait. A host sends the result to the model as soon as the
	// user runs the prompt, so an instruction to carry on would start work the
	// user has not asked for.
	if last, ok := got.Messages[len(got.Messages)-1].Content.(*sdk.TextContent); !ok || last.Text != closing {
		t.Error("the closing is not the last message, so the model reads the turns after it")
	}
	if !strings.Contains(closing, "wait for the user") {
		t.Errorf("the closing does not ask the model to wait for the user: %q", closing)
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

// saveInOrder stores conversations oldest first and returns their addresses in
// the same order.
//
// Each is saved a moment after the one before. A head's timestamp is kept to the
// millisecond, two saves inside one would tie, and a tie is broken by id, which
// is random, so "most recent" would stop having one answer.
func saveInOrder(t *testing.T, session *sdk.ClientSession, conversations ...mcp.SaveSessionIn) []string {
	t.Helper()
	uris := make([]string, 0, len(conversations))
	for i, in := range conversations {
		if i > 0 {
			time.Sleep(2 * time.Millisecond)
		}
		uris = append(uris, saveConversation(t, session, in).URI)
	}
	return uris
}

// A choice is one conversation as the dialog offers it.
type choice struct {
	Const string `json:"const"`
	Title string `json:"title"`
}

// choicesIn reads the conversations a dialog offers, in the order it offers them.
func choicesIn(t *testing.T, params *sdk.ElicitParams) []choice {
	t.Helper()
	encoded, err := json.Marshal(params.RequestedSchema)
	if err != nil {
		t.Fatalf("encode the dialog's schema: %v", err)
	}
	var schema struct {
		Properties map[string]struct {
			OneOf []choice `json:"oneOf"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatalf("read the dialog's schema: %v", err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "conversation" {
		t.Fatalf("the dialog requires %v, want the one choice of conversation", schema.Required)
	}
	return schema.Properties["conversation"].OneOf
}

// pick answers a dialog with the conversation whose label starts with title.
func pick(t *testing.T, params *sdk.ElicitParams, title string) string {
	t.Helper()
	for _, offered := range choicesIn(t, params) {
		if strings.HasPrefix(offered.Title, title) {
			return offered.Const
		}
	}
	t.Fatalf("the dialog does not offer %q", title)
	return ""
}

// updatedDay is the day a list should show for a conversation: when it last
// changed, in this machine's time zone, with the year only if it is not this
// one.
func updatedDay(t *testing.T, session *sdk.ClientSession, uri string) string {
	t.Helper()
	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: uri}, &opened)
	updated, _ := opened.Detail.(map[string]any)["updated"].(string)
	parsed, err := time.Parse(time.RFC3339, updated)
	if err != nil {
		t.Fatalf("%s was last updated at %q: %v", uri, updated, err)
	}
	day := parsed.Local()
	if day.Year() != time.Now().Year() {
		return day.Format("Jan 2 2006")
	}
	return day.Format("Jan 2")
}

// A resume that names nothing puts the recent conversations to the user as a
// dialog, and resumes the one they pick, which need not be the newest.
//
// This client is on the 2026-07-28 revision and never sends initialize. It
// declares that it can show a dialog on every request, the way a stateless host
// does, and it retries the prompt with the answer itself.
func TestResumingWithNothingNamedResumesTheConversationTheUserPicks(t *testing.T) {
	ctx := context.Background()
	var asked []*sdk.ElicitParams
	session := connectWith(t, mcp.New(openVault(t, t.TempDir())), &sdk.ClientOptions{
		ElicitationHandler: func(_ context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
			asked = append(asked, req.Params)
			return &sdk.ElicitResult{Action: "accept", Content: map[string]any{
				"conversation": pick(t, req.Params, "Harbour survey"),
			}}, nil
		},
	})
	if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
		t.Fatalf("the client negotiated %s, so this is not the path it is meant to cover", got)
	}

	saved := saveInOrder(t, session,
		mcp.SaveSessionIn{
			Title:    "Harbour survey",
			Summary:  "Surveyed the harbour approaches and found silting at the east.",
			Messages: conversation("harbour"),
			Agent:    mcp.AgentIn{Name: "codex", Version: "0.9"},
		},
		mcp.SaveSessionIn{
			Title:    "Rollup schedule",
			Summary:  "Settled that the rollup runs hourly at ten past.",
			Messages: conversation("rollup"),
		},
	)
	harbour, rollup := saved[0], saved[1]

	got, err := session.GetPrompt(ctx, &sdk.GetPromptParams{Name: mcp.ResumePrompt})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}

	// One dialog, with both conversations newest first, each by its title, its
	// agent and the day it last changed.
	if len(asked) != 1 {
		t.Fatalf("the prompt asked %d time(s), want once", len(asked))
	}
	if !strings.Contains(asked[0].Message, "Showing all 2.") {
		t.Errorf("the dialog does not say how much of the vault it shows: %q", asked[0].Message)
	}
	choices := choicesIn(t, asked[0])
	if len(choices) != 2 || choices[0].Const != rollup || choices[1].Const != harbour {
		t.Fatalf("the dialog offered %+v, want the two conversations, newest first", choices)
	}
	if want := "Harbour survey · codex · " + updatedDay(t, session, harbour); choices[1].Title != want {
		t.Errorf("a conversation is labelled %q, want %q", choices[1].Title, want)
	}
	// A head that does not know its agent is labelled without one, not with a
	// blank where the agent would be.
	if want := "Rollup schedule · " + updatedDay(t, session, rollup); choices[0].Title != want {
		t.Errorf("a conversation with no agent is labelled %q, want %q", choices[0].Title, want)
	}

	// The pick is resumed, and not the newest, exactly as a resume by address
	// would resume it.
	if !strings.Contains(got.Description, "Harbour survey") {
		t.Fatalf("the prompt resumed something other than the pick: %q", got.Description)
	}
	framing, _ := got.Messages[0].Content.(*sdk.TextContent)
	if framing == nil || !strings.Contains(framing.Text,
		"How it was chosen: picked by the user from a list of recent conversations.") {
		t.Error("the framing does not say the conversation was picked from a list")
	}
	head, _ := got.Messages[1].Content.(*sdk.EmbeddedResource)
	if head == nil || head.Resource.URI != harbour {
		t.Errorf("the head embedded is not the pick's, want %s", harbour)
	}
	closing, _ := got.Messages[len(got.Messages)-1].Content.(*sdk.TextContent)
	if closing == nil || !strings.Contains(closing.Text, "wait for the user") {
		t.Error("a picked conversation does not end on the same closing as any other resume")
	}
}

// Closing the list resumes nothing, and says so without spending a model turn:
// the prompt fails with a sentence for the user that names the one-word way to
// resume the latest conversation instead.
//
// The list is shown even when there is only one conversation to choose, so the
// command behaves the same whatever the vault holds.
func TestClosingTheListResumesNothingAndSaysHowToResumeTheLatest(t *testing.T) {
	for _, action := range []string{"decline", "cancel"} {
		t.Run(action, func(t *testing.T) {
			var asked []string
			session := connectWith(t, mcp.New(openVault(t, t.TempDir())), &sdk.ClientOptions{
				ElicitationHandler: func(_ context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
					asked = append(asked, req.Params.Message)
					return &sdk.ElicitResult{Action: action}, nil
				},
			})
			saveConversation(t, session, mcp.SaveSessionIn{
				Title:    "Harbour survey",
				Summary:  "Surveyed the harbour approaches and found silting at the east.",
				Messages: conversation("harbour"),
			})

			got, err := session.GetPrompt(context.Background(), &sdk.GetPromptParams{Name: mcp.ResumePrompt})
			if err == nil {
				t.Fatalf("a list closed with %s resumed %q anyway", action, got.Description)
			}
			if len(asked) != 1 || !strings.Contains(asked[0], "Showing the only one.") {
				t.Errorf("one conversation was not offered as a list of one: %q", asked)
			}
			if !strings.Contains(err.Error(), "nothing was resumed") {
				t.Errorf("the refusal does not say that nothing was resumed: %v", err)
			}
			// In the form Claude Code accepts typed out in full, which is not the
			// name its menu shows.
			if !strings.Contains(err.Error(), "/mcp__sennit__resume recent") {
				t.Errorf("the refusal does not say how to resume the most recent one: %v", err)
			}
		})
	}
}

// A client that cannot show a dialog gets the same list as text, with an
// instruction to put the choice to the user, and nothing is resumed yet.
func TestAClientWithoutDialogsIsGivenTheListAndNothingIsResumed(t *testing.T) {
	session, _ := serve(t)
	ctx := context.Background()

	long := "A conversation whose title runs on well past anything a list can show on one line, " +
		"because an agent wrote it and nothing bounds how long it is"
	saved := saveInOrder(t, session,
		mcp.SaveSessionIn{
			Title:    "Harbour survey",
			Summary:  "Surveyed the harbour approaches and found silting at the east.",
			Messages: conversation("harbour"),
			Agent:    mcp.AgentIn{Name: "codex", Version: "0.9"},
		},
		mcp.SaveSessionIn{
			Title:    long,
			Summary:  "Written to give the list a title too long to show whole.",
			Messages: conversation("long"),
		},
	)

	got, err := session.GetPrompt(ctx, &sdk.GetPromptParams{Name: mcp.ResumePrompt})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	var texts []string
	for _, message := range got.Messages {
		text, ok := message.Content.(*sdk.TextContent)
		if !ok {
			t.Fatalf("the list carried a %T; nothing is embedded before the user has chosen", message.Content)
		}
		texts = append(texts, text.Text)
	}
	if len(texts) != 2 {
		t.Fatalf("the list came back as %d message(s), want the list and its closing", len(texts))
	}
	list, closing := texts[0], texts[1]

	if strings.Contains(list, "Resume this conversation") {
		t.Error("a resume that names nothing resumed a conversation without asking")
	}
	if !strings.Contains(list, "These are all 2 it holds") {
		t.Errorf("the list does not say how much of the vault it shows: %.200q", list)
	}
	// Each entry names its conversation the way the dialog does and gives the
	// address the model opens once the user has chosen.
	if !strings.Contains(list, "Harbour survey · codex · "+updatedDay(t, session, saved[0])) {
		t.Errorf("the list does not label a conversation by title, agent and date: %q", list)
	}
	for _, uri := range saved {
		if !strings.Contains(list, uri) {
			t.Errorf("the list does not give %s, so the choice cannot be opened", uri)
		}
	}
	if strings.Contains(list, long) || !strings.Contains(list, "A conversation whose title runs on") ||
		!strings.Contains(list, "… · ") {
		t.Errorf("a long title was not shortened and marked as cut: %q", list)
	}

	// Its own closing, which hands the choice to the user.
	if !strings.Contains(closing, "ask which conversation to resume") ||
		!strings.Contains(closing, "Do not pick one yourself") || !strings.Contains(closing, "`open`") {
		t.Errorf("the closing does not leave the choice to the user: %q", closing)
	}
	if strings.Contains(closing, "That is everything brought back") {
		t.Error("the list ends on a resume's closing, as though something had been resumed")
	}
}

// `recent` resumes the newest conversation and asks nothing, even of a client
// that could show a dialog, in whatever case it was typed.
func TestRecentResumesTheNewestWithoutAsking(t *testing.T) {
	ctx := context.Background()
	session := connectWith(t, mcp.New(openVault(t, t.TempDir())), &sdk.ClientOptions{
		ElicitationHandler: func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
			t.Error("recent asked the user which conversation to resume")
			return &sdk.ElicitResult{Action: "cancel"}, nil
		},
	})
	saveInOrder(t, session,
		mcp.SaveSessionIn{
			Title:    "Harbour survey",
			Summary:  "Surveyed the harbour approaches and found silting at the east.",
			Messages: conversation("harbour"),
		},
		mcp.SaveSessionIn{
			Title:    "Rollup schedule",
			Summary:  "Settled that the rollup runs hourly at ten past.",
			Messages: conversation("rollup"),
		},
	)

	for _, word := range []string{"recent", "RECENT", "Recent"} {
		got, err := session.GetPrompt(ctx, &sdk.GetPromptParams{
			Name:      mcp.ResumePrompt,
			Arguments: map[string]string{"session": word},
		})
		if err != nil {
			t.Fatalf("resume %s: %v", word, err)
		}
		if !strings.Contains(got.Description, "Rollup schedule") ||
			!strings.Contains(got.Description, "the most recent conversation") {
			t.Errorf("resume %s resumed %q, want the most recent conversation", word, got.Description)
		}
	}

	// An empty vault has nothing to list and nothing recent, and says so the
	// same way either way it is asked.
	empty := connect(t, mcp.New(openVault(t, t.TempDir())))
	for _, arguments := range []map[string]string{nil, {"session": mcp.ResumeRecent}} {
		_, err := empty.GetPrompt(ctx, &sdk.GetPromptParams{Name: mcp.ResumePrompt, Arguments: arguments})
		if err == nil || !strings.Contains(err.Error(), "no conversations yet") {
			t.Errorf("an empty vault asked with %v answered %v", arguments, err)
		}
	}
}

// The list offers the most recent conversations and no more, says how many it
// leaves out, and resumes only what it offered.
//
// This client hands the dialog back instead of answering it, so the test can
// answer with things a real dialog would never let a user pick. On the
// 2026-07-28 revision the client sends the answer back itself, and nothing but
// this server checks it.
func TestTheListOffersTheRecentOnesAndResumesOnlyWhatItOffered(t *testing.T) {
	ctx := context.Background()
	session := connectWith(t, mcp.New(openVault(t, t.TempDir())), &sdk.ClientOptions{
		Capabilities:   &sdk.ClientCapabilities{Elicitation: &sdk.ElicitationCapabilities{}},
		MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true},
	})

	long := strings.Repeat("A title an agent let run on and on, ", 4)
	conversations := []mcp.SaveSessionIn{{
		Title:    "The oldest conversation",
		Summary:  "Saved first, so a full list leaves it out.",
		Messages: conversation("oldest"),
	}}
	for i := range mcp.ResumeChoices {
		in := mcp.SaveSessionIn{
			Title:    fmt.Sprintf("Conversation %d", i),
			Summary:  "One of enough conversations to fill the list.",
			Messages: conversation(fmt.Sprintf("c%d", i)),
		}
		if i == mcp.ResumeChoices-1 {
			in.Title, in.Agent = long, mcp.AgentIn{Name: "claude-code", Version: "2.1.283"}
		}
		conversations = append(conversations, in)
	}
	saved := saveInOrder(t, session, conversations...)
	oldest, leastRecentOffered := saved[0], saved[1]

	asked, err := session.GetPrompt(ctx, &sdk.GetPromptParams{Name: mcp.ResumePrompt})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if !asked.NeedsInput() || len(asked.Messages) != 0 {
		t.Fatalf("a resume that names nothing answered with %d message(s) instead of asking",
			len(asked.Messages))
	}
	request, ok := asked.InputRequests["conversation"].(*sdk.ElicitParams)
	if !ok || len(asked.InputRequests) != 1 {
		t.Fatalf("the prompt asked for %+v, want one dialog", asked.InputRequests)
	}

	choices := choicesIn(t, request)
	if len(choices) != mcp.ResumeChoices {
		t.Fatalf("the list offers %d conversations, want %d", len(choices), mcp.ResumeChoices)
	}
	want := fmt.Sprintf("Showing the %d most recent of %d.", mcp.ResumeChoices, mcp.ResumeChoices+1)
	if !strings.Contains(request.Message, want) || !strings.Contains(request.Message, "ask your agent to find it") {
		t.Errorf("the dialog does not say what it leaves out and where to find it: %q", request.Message)
	}
	var clipped bool
	for _, offered := range choices {
		if offered.Const == oldest {
			t.Error("the list offered a conversation older than the ones it shows")
		}
		// Every label fits what a dialog shows whole, so the agent and the date
		// at its end are never what gets cut.
		if n := utf8.RuneCountInString(offered.Title); n > mcp.ChoiceWidth {
			t.Errorf("a choice is %d characters, over the %d a dialog shows: %q",
				n, mcp.ChoiceWidth, offered.Title)
		}
		if strings.HasPrefix(offered.Title, "A title an agent") {
			title, about, _ := strings.Cut(offered.Title, " · ")
			clipped = strings.HasSuffix(title, "…") && strings.HasPrefix(about, "claude-code · ")
		}
	}
	if !clipped {
		t.Errorf("a %d-character title was not shortened to leave room for its agent and date",
			utf8.RuneCountInString(long))
	}

	answer := func(action, value, state string) (*sdk.GetPromptResult, error) {
		return session.GetPrompt(ctx, &sdk.GetPromptParams{
			Name: mcp.ResumePrompt,
			InputResponses: sdk.InputResponseMap{"conversation": &sdk.ElicitResult{
				Action: action, Content: map[string]any{"conversation": value},
			}},
			RequestState: state,
		})
	}

	// A conversation this vault holds but the list did not offer is refused, and
	// so is an answer that is not an address at all.
	for _, value := range []string{oldest, "The oldest conversation", ""} {
		got, err := answer("accept", value, asked.RequestState)
		if err == nil {
			t.Errorf("an answer of %q resumed %q", value, got.Description)
			continue
		}
		if !strings.Contains(err.Error(), "not one it offered") {
			t.Errorf("an answer of %q was refused for another reason: %v", value, err)
		}
	}

	// One it offered is resumed, and a client that dropped the state it was sent
	// is held to the same list read again.
	for _, state := range []string{asked.RequestState, ""} {
		got, err := answer("accept", leastRecentOffered, state)
		if err != nil {
			t.Fatalf("an offered answer with state %.40q: %v", state, err)
		}
		if !strings.Contains(got.Description, "Conversation 0") {
			t.Errorf("an offered answer resumed %q, want Conversation 0", got.Description)
		}
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
