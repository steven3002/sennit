package mcp_test

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
)

// Pass mark 1, the half that does not need a person. A real MCP client
// connects, lists the tools, and calls remember and recall successfully.
//
// The client here is the SDK a production host is built on, doing the same
// handshake over the same JSON. What it cannot answer is whether a host's user
// interface renders any of it, which is why docs/host-checks.md exists.
func TestAClientConnectsListsTheToolsAndUsesThem(t *testing.T) {
	session, _ := serve(t)

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	byName := map[string]*sdk.Tool{}
	for _, tool := range listed.Tools {
		byName[tool.Name] = tool
	}
	for _, tool := range mcp.Tools {
		listedTool, ok := byName[tool.Name]
		if !ok {
			t.Fatalf("the server does not offer %q", tool.Name)
		}
		if listedTool.Description != tool.Description {
			t.Errorf("%q is served with a description other than the one that was reviewed", tool.Name)
		}
		// Without an output schema a host doing code-mode falls back to `any`,
		// and structured content stops being worth returning.
		if listedTool.OutputSchema == nil {
			t.Errorf("%q has no outputSchema", tool.Name)
		}
		if listedTool.Annotations == nil {
			t.Errorf("%q has no annotations, so it inherits destructive and open-world by default",
				tool.Name)
		}
	}
	if len(byName) != len(mcp.Tools) {
		t.Errorf("the server offers %d tools, the reviewed surface has %d", len(byName), len(mcp.Tools))
	}

	uri := storeMemory(t, session, mcp.RememberIn{
		Statement: "Tidepool's reading rollup runs hourly, at ten past the hour.",
		Context:   "Settled while working out why the dashboard lagged the sensors by an hour.",
		Type:      "fact",
		Tags:      []string{"rollup", "schedule"},
	})

	var recalled mcp.RecallOut
	result := call(t, session, "recall", mcp.RecallIn{
		Query: "when does the reading rollup run?",
		Tags:  []string{"rollup"},
	}, &recalled)
	if len(recalled.Results) == 0 {
		t.Fatal("recall found nothing that remember had just stored")
	}
	if recalled.Results[0].URI != uri {
		t.Fatalf("recall ranked %s first, want the record just written (%s)", recalled.Results[0].URI, uri)
	}
	// Both halves, on every call: structured content for a host, a mirrored text
	// block for a model.
	if resultText(result) == "" {
		t.Error("recall returned structured content with no mirrored text block")
	}
	if len(links(result)) == 0 {
		t.Error("recall returned no resource links, so the addresses are dead ends")
	}
}

// Pass mark 2. A memory written in one session is recalled in a later one.
//
// The two sessions are separate server instances over separate vault handles on
// one directory, which is what a host restarting its MCP servers actually does.
func TestAMemoryWrittenInOneSessionIsRecalledInALaterOne(t *testing.T) {
	home := t.TempDir()

	first := connect(t, mcp.New(openVault(t, home)))
	uri := storeMemory(t, first, mcp.RememberIn{
		Statement: "Ana prefers review comments phrased as questions rather than as instructions.",
		Context:   "Said during the retro after the March release, about how code review felt.",
		Type:      "preference",
		Tags:      []string{"review", "ana"},
	})
	first.Close()

	second := connect(t, mcp.New(openVault(t, home)))
	var recalled mcp.RecallOut
	call(t, second, "recall", mcp.RecallIn{
		Query: "how does Ana like review comments written?",
		Tags:  []string{"review"},
	}, &recalled)

	if len(recalled.Results) == 0 {
		t.Fatal("a later session recalled nothing that an earlier one wrote")
	}
	if recalled.Results[0].URI != uri {
		t.Fatalf("the later session ranked %s first, want %s", recalled.Results[0].URI, uri)
	}

	// And the record is readable, not merely rankable.
	var opened mcp.OpenOut
	call(t, second, "open", mcp.OpenIn{URI: uri}, &opened)
	if !strings.Contains(opened.Content, "phrased as questions") {
		t.Fatalf("the record opened in a later session does not hold what was written: %s", opened.Content)
	}
}

// Pass mark 3. `open` and `resources/read` return identical content for the
// same address, the shared-resolver test.
//
// It is asserted over every addressable form rather than over one, because the
// failure this exists to prevent was exactly a form that one door knew about and
// the other did not: an `open` tool that resolved only the record store answered
// "no record" for a fixed resource sitting in the server's own listing.
func TestOpenAndResourcesReadReturnIdenticalContent(t *testing.T) {
	session, _ := serve(t)

	memory := storeMemory(t, session, mcp.RememberIn{
		Statement: "The station registry disagrees with the sensor feed on four stations.",
		Context:   "Found while counting hourly reporters; the registry lists retired stations.",
		Type:      "fact",
		Tags:      []string{"stations", "registry"},
	})
	var saved mcp.SaveSessionOut
	call(t, session, "save_session", mcp.SaveSessionIn{
		Title:    "Tide station survey",
		Summary:  "Counted the stations reporting hourly and found the registry stale.",
		Tags:     []string{"stations"},
		Messages: conversation("shared"),
	}, &saved)

	for _, uri := range []string{
		mcp.VaultURI,
		mcp.GuideURI,
		memory,
		saved.URI,
		saved.Transcript,
	} {
		var opened mcp.OpenOut
		call(t, session, "open", mcp.OpenIn{URI: uri}, &opened)

		read, err := session.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("resources/read %s: %v", uri, err)
		}
		if len(read.Contents) != 1 {
			t.Fatalf("resources/read %s returned %d contents, want 1", uri, len(read.Contents))
		}
		if read.Contents[0].Text != opened.Content {
			t.Errorf("the two doors into %s disagree:\n  open:           %.120q\n  resources/read: %.120q",
				uri, opened.Content, read.Contents[0].Text)
		}
		if read.Contents[0].MIMEType != opened.MIMEType {
			t.Errorf("%s is %q through open and %q through resources/read",
				uri, opened.MIMEType, read.Contents[0].MIMEType)
		}
		if read.Contents[0].URI != uri {
			t.Errorf("resources/read %s answered for %s", uri, read.Contents[0].URI)
		}
	}
}

// Pass mark 4. Every resource the instructions mention is reachable, and every
// resource the server registers is mentioned. The model never has to guess.
//
// Both directions matter and they fail differently. An address named but not
// reachable sends the model to a dead end; an address reachable but not named is
// invisible, because a host fetches the resource listing at connect and keeps it
// to itself.
func TestEveryRegisteredAddressIsNamedAndEveryNamedAddressResolves(t *testing.T) {
	session, _ := serve(t)
	ctx := context.Background()

	listed, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	templates, err := session.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	if len(listed.Resources) != len(mcp.Fixed) {
		t.Errorf("the server registers %d fixed resources, the address space names %d",
			len(listed.Resources), len(mcp.Fixed))
	}
	if len(templates.ResourceTemplates) != len(mcp.Templates) {
		t.Errorf("the server registers %d templates, the address space names %d",
			len(templates.ResourceTemplates), len(mcp.Templates))
	}

	for _, resource := range listed.Resources {
		if !strings.Contains(mcp.Instructions, resource.URI) {
			t.Errorf("%s is registered but never named in the instructions, so the model cannot reach it",
				resource.URI)
		}
		if _, err := session.ReadResource(ctx, &sdk.ReadResourceParams{URI: resource.URI}); err != nil {
			t.Errorf("%s is registered and does not resolve: %v", resource.URI, err)
		}
	}
	for _, template := range templates.ResourceTemplates {
		if !strings.Contains(mcp.Instructions, template.URITemplate) {
			t.Errorf("%s is registered but never named in the instructions", template.URITemplate)
		}
	}

	// A template is not enough on its own: Claude Code was observed calling
	// resources/list and prompts/list at connect and never
	// resources/templates/list, so a templated address is reachable through the
	// tools or not at all. Every template is exercised against a real instance.
	memory := storeMemory(t, session, mcp.RememberIn{
		Statement: "The dashboard reads from the rollup table, never from the raw feed.",
		Context:   "Decided when the raw feed's late arrivals kept changing yesterday's chart.",
		Type:      "insight",
		Tags:      []string{"dashboard", "rollup"},
	})
	var saved mcp.SaveSessionOut
	call(t, session, "save_session", mcp.SaveSessionIn{
		Title:    "Dashboard source of truth",
		Summary:  "Settled that the dashboard reads the rollup table.",
		Messages: conversation("reachable"),
	}, &saved)

	for template, instance := range map[string]string{
		mcp.MemoryTemplate:     memory,
		mcp.SessionTemplate:    saved.URI,
		mcp.TranscriptTemplate: saved.Transcript,
	} {
		if _, err := session.ReadResource(ctx, &sdk.ReadResourceParams{URI: instance}); err != nil {
			t.Errorf("%s resolves nothing at %s: %v", template, instance, err)
		}
	}
}

// Pass mark 6. A wrong or unknown tag returns ranked results, never an empty
// set.
//
// This is I9 asserted at the protocol boundary rather than in the ranker. It is
// the single measured cliff in this system: the same wrong filter applied as an
// exclusion returned nothing at all for ten queries in fifty-nine, so the
// property has to hold where an agent can actually reach it.
func TestAWrongTagRanksWorseAndNeverReturnsNothing(t *testing.T) {
	session, _ := serve(t)

	for _, memory := range []mcp.RememberIn{
		{
			Statement: "Tidepool's reading rollup runs hourly, at ten past the hour.",
			Context:   "Settled while working out why the dashboard lagged the sensors.",
			Type:      "fact", Tags: []string{"rollup", "schedule"},
		},
		{
			Statement: "The rollup writes to a table the dashboard reads, never the raw feed.",
			Context:   "Decided after late arrivals kept rewriting yesterday's chart.",
			Type:      "insight", Tags: []string{"rollup", "dashboard"},
		},
		{
			Statement: "Ana prefers review comments phrased as questions.",
			Context:   "Said in the retro after the March release.",
			Type:      "preference", Tags: []string{"review"},
		},
	} {
		storeMemory(t, session, memory)
	}

	const query = "when does the rollup run?"
	var unfiltered, correct, wrong, unknown mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: query}, &unfiltered)
	call(t, session, "recall", mcp.RecallIn{Query: query, Tags: []string{"rollup", "schedule"}}, &correct)
	call(t, session, "recall", mcp.RecallIn{Query: query, Tags: []string{"review"}}, &wrong)
	call(t, session, "recall", mcp.RecallIn{
		Query: query,
		Tags:  []string{"no-record-carries-this", "nor-this"},
		Types: []string{"profile"},
	}, &unknown)

	if len(unfiltered.Results) == 0 {
		t.Fatal("the unfiltered query found nothing, so this test proves nothing")
	}
	for name, out := range map[string]mcp.RecallOut{"a wrong tag": wrong, "an unknown tag": unknown} {
		if len(out.Results) != len(unfiltered.Results) {
			t.Errorf("%s changed the result count from %d to %d; a filter must only ever reorder",
				name, len(unfiltered.Results), len(out.Results))
		}
		if len(out.Results) == 0 {
			t.Errorf("%s emptied the result set", name)
		}
	}
	// A tag no record carries cannot move anything, so the ranking is the
	// unfiltered one exactly.
	for i := range unknown.Results {
		if unknown.Results[i].URI != unfiltered.Results[i].URI {
			t.Errorf("a tag no record carries reordered the results at position %d", i+1)
		}
	}
	if correct.Results[0].Boost <= 0 {
		t.Error("a correct filter moved nothing, so filtering is not reaching the ranker")
	}
}

// Returning nothing is a success with a hint, and never a protocol or tool
// error. A model that receives an error learns to stop asking; one that receives
// an empty answer with a reason can act on it.
func TestAnEmptyResultIsASuccessWithAHint(t *testing.T) {
	session, _ := serve(t)

	result := callRaw(t, session, "recall", mcp.RecallIn{Query: "what did we decide about tides?"})
	if result.IsError {
		t.Fatalf("an empty recall was reported as an error: %s", resultText(result))
	}
	var out mcp.RecallOut
	decodeStructured(t, "recall", result, &out)
	if len(out.Results) != 0 {
		t.Fatalf("an empty vault returned %d result(s)", len(out.Results))
	}
	if out.Hint == "" {
		t.Fatal("an empty result carried no hint, so a model has nothing to act on")
	}
	// The hint must not send an agent away from supplying tags: the filter
	// cannot have caused this, and teaching otherwise is how brittle filters get
	// written.
	if strings.Contains(strings.ToLower(out.Hint), "remove your tags") {
		t.Error("the empty-result hint blames the filter, which cannot empty a result")
	}
}

// The capabilities are stated rather than inherited: nothing is advertised that
// this server does not implement.
//
// The SDK advertises logging by default, and a capability advertised without a
// handler is a claim a client will act on and then find false, measured once
// already, when a subscribe capability set by hand answered method-not-found.
func TestNoCapabilityIsAdvertisedWithoutAHandler(t *testing.T) {
	session, _ := serve(t)
	initialized := session.InitializeResult()

	capabilities := initialized.Capabilities
	if capabilities.Logging != nil {
		t.Error("the server advertises logging, which it does not implement and which is deprecated")
	}
	if capabilities.Resources != nil && capabilities.Resources.Subscribe {
		t.Error("the server advertises resource subscriptions and has no subscribe handler")
	}
	if capabilities.Completions != nil {
		t.Error("the server advertises completions and has no completion handler")
	}
	for name, present := range map[string]bool{
		"tools":     capabilities.Tools != nil,
		"resources": capabilities.Resources != nil,
		"prompts":   capabilities.Prompts != nil,
	} {
		if !present {
			t.Errorf("the server does not advertise %s, which it does implement", name)
		}
	}
	if initialized.Instructions != mcp.Instructions {
		t.Error("the instructions a client receives are not the ones under review")
	}
}

// A user's own records must not be cacheable by anything but the client that
// asked for them.
//
// The SDK stamps `cacheScope: "public"` on every list and read, and it does so
// after the handler returns, so a handler cannot override it, and middleware is
// the only place that can. For a store whose whole claim is that nobody else can
// read it, shipping "public" on a listing of one user's memories is wrong.
func TestUserContentIsNotAdvertisedAsPubliclyCacheable(t *testing.T) {
	session, _ := serve(t)
	ctx := context.Background()

	read, err := session.ReadResource(ctx, &sdk.ReadResourceParams{URI: mcp.VaultURI})
	if err != nil {
		t.Fatalf("read vault: %v", err)
	}
	if read.CacheScope != "private" {
		t.Errorf("resources/read is cacheable as %q, want private", read.CacheScope)
	}
	listed, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	if listed.CacheScope != "private" {
		t.Errorf("resources/list is cacheable as %q, want private", listed.CacheScope)
	}
}

// An address that does not resolve is a tool error through `open` and a protocol
// error through resources/read, and both name the address.
//
// The two channels are deliberately different. The model is the party that can
// act on a bad address, by finding a real one, so `open` returns something it can
// read; resources/read is host plumbing and its caller wants the protocol's own
// not-found code.
func TestAnAddressThatResolvesToNothingSaysSoThroughBothDoors(t *testing.T) {
	session, _ := serve(t)
	id, err := record.NewID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}
	missing := mcp.URI(record.KindMemory, id)

	result := callRaw(t, session, "open", mcp.OpenIn{URI: missing})
	if !result.IsError {
		t.Fatal("open invented an answer for an address holding nothing")
	}
	if !strings.Contains(resultText(result), "recall") {
		t.Errorf("the refusal does not tell the model how to find a real address: %s", resultText(result))
	}

	if _, err := session.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: missing}); err == nil {
		t.Fatal("resources/read invented an answer for an address holding nothing")
	}
}

// A server with no vault still serves the protocol and still reads the guide,
// and every tool says what is wrong.
//
// An unconfigured server that refuses the connection tells the user nothing: the
// host reports a failed launch and they are left guessing. This is the shape the
// specification asks for, not onboarded is a tool execution error, not a
// protocol one.
func TestAServerWithNoVaultExplainsItselfRatherThanRefusingToStart(t *testing.T) {
	session := connect(t, mcp.Unopened(context.Canceled))

	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("a server with no vault would not list its tools: %v", err)
	}
	guide, err := session.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: mcp.GuideURI})
	if err != nil {
		t.Fatalf("a server with no vault would not serve its guide: %v", err)
	}
	if guide.Contents[0].Text != mcp.Guide {
		t.Error("the guide served without a vault is not the guide")
	}

	result := callRaw(t, session, "recall", mcp.RecallIn{Query: "anything"})
	if !result.IsError {
		t.Fatal("a server with no vault answered a recall")
	}
	if !strings.Contains(resultText(result), "vault") {
		t.Errorf("the refusal does not say what is missing: %s", resultText(result))
	}
}
