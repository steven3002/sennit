package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// namedLinks fails unless every resource link a result carries has a name and
// an address, and returns the links.
//
// It looks twice: at the links as the client decoded them, and at the JSON they
// travel as. The second is what a host validates, and it is where the failure
// shows. The SDK leaves an empty name out of the JSON altogether, the
// specification makes a link's name required, and a strict host rejects the
// whole result over one link, so a call that did what it was asked reads as one
// that failed.
func namedLinks(t *testing.T, tool string, result *sdk.CallToolResult) []*sdk.ResourceLink {
	t.Helper()
	found := links(result)
	for _, link := range found {
		if strings.TrimSpace(link.Name) == "" {
			t.Errorf("%s returned a link to %q with no name", tool, link.URI)
		}
	}
	encoded, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("%s content: %v", tool, err)
	}
	var blocks []map[string]any
	if err := json.Unmarshal(encoded, &blocks); err != nil {
		t.Fatalf("%s content: %v", tool, err)
	}
	for i, block := range blocks {
		if block["type"] != "resource_link" {
			continue
		}
		for _, field := range []string{"uri", "name"} {
			if value, ok := block[field].(string); !ok || strings.TrimSpace(value) == "" {
				t.Errorf("%s content[%d] is a resource_link whose required %q is %v on the wire",
					tool, i, field, block[field])
			}
		}
	}
	return found
}

// linksSaid renders links for a failure message, each as its address and name.
func linksSaid(found []*sdk.ResourceLink) string {
	said := make([]string, len(found))
	for i, link := range found {
		said[i] = fmt.Sprintf("%s named %q", link.URI, link.Name)
	}
	return "[" + strings.Join(said, ", ") + "]"
}

// A save that does not restate a conversation's title names the link it returns
// by the title the conversation has.
//
// Found in Claude Code: a save_session that only changed a conversation's tags
// was reported as a malformed result, because its link had no name, after the
// tags had changed. An append that sends only the new turns is the ordinary
// case, and it was answered the same way, so an agent told the save failed
// would save again.
func TestASaveThatDoesNotRestateTheTitleNamesItsLinkByTheStoredOne(t *testing.T) {
	session, _ := serve(t)
	const title = "Tide station survey"

	first := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    title,
		Summary:  "Counted the stations reporting hourly and found the registry stale.",
		Messages: conversation("first"),
	})

	for _, tc := range []struct {
		name string
		in   mcp.SaveSessionIn
	}{
		{"tags only", mcp.SaveSessionIn{Session: first.URI, Tags: []string{"stations", "registry"}}},
		{"an append", mcp.SaveSessionIn{Session: first.URI, Messages: conversation("second")}},
		{"a new summary", mcp.SaveSessionIn{Session: first.URI, Summary: "Settled on the registry as the source."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out mcp.SaveSessionOut
			result := call(t, session, "save_session", tc.in, &out)
			found := namedLinks(t, "save_session", result)
			if len(found) != 1 || found[0].URI != first.URI {
				t.Fatalf("save_session linked %s, want the one conversation %s", linksSaid(found), first.URI)
			}
			if found[0].Name != title {
				t.Errorf("the link is named %q, want the conversation's title %q", found[0].Name, title)
			}
		})
	}

	// A save that does restate the title names the link by the new one.
	var out mcp.SaveSessionOut
	result := call(t, session, "save_session", mcp.SaveSessionIn{Session: first.URI, Title: "Tide registry audit"}, &out)
	if found := namedLinks(t, "save_session", result); len(found) != 1 || found[0].Name != "Tide registry audit" {
		t.Errorf("a save that renamed the conversation linked %s", linksSaid(found))
	}
}

// A record this device has nothing to call is linked by its address.
//
// A listing reads a memory's label from its body, and a device can hold the
// listing's row and not the body: a forget cut off between the two leaves
// exactly that. The row is listed with an empty label, as browse documents, and
// its link still needs a name.
func TestALinkToARecordWithNothingToCallItIsNamedByItsAddress(t *testing.T) {
	session, v := serve(t)
	uri := storeMemory(t, session, mcp.RememberIn{
		Statement: "The tide registry lists forty-one stations that report hourly.",
		Context:   "Counted during the tide station survey.",
		Type:      "fact",
		Tags:      []string{"stations", "registry"},
	})
	address, err := mcp.Parse(uri)
	if err != nil {
		t.Fatalf("parse %s: %v", uri, err)
	}
	if err := v.ForgetLocally(address.ID); err != nil {
		t.Fatalf("drop the body: %v", err)
	}

	var listed mcp.BrowseOut
	result := call(t, session, "browse", mcp.BrowseIn{}, &listed)
	if len(listed.Rows) != 1 || listed.Rows[0].Label != "" {
		t.Fatalf("browse listed %+v, want the one memory with no label, so this checks nothing", listed.Rows)
	}
	found := namedLinks(t, "browse", result)
	if len(found) != 1 || found[0].URI != uri {
		t.Fatalf("browse linked %s, want %s", linksSaid(found), uri)
	}
	if found[0].Name != uri {
		t.Errorf("a record with nothing to call it is linked as %q, want its address %q", found[0].Name, uri)
	}
}

// A statement that opens on a blank line is called by its first line of text.
//
// A statement is refused only when it holds nothing but whitespace, so one that
// starts with a line break is stored, and it was then called by its first line,
// which is empty: a remember answered with a nameless link, rejected by a strict
// host after the memory was stored, and an agent told it failed would store it
// twice.
func TestAStatementThatOpensOnABlankLineIsCalledByItsFirstLineOfText(t *testing.T) {
	session, _ := serve(t)
	const statement = "The tide registry lists forty-one stations that report hourly."

	var stored mcp.RememberOut
	result := call(t, session, "remember", mcp.RememberIn{
		Statement: "\n\n  " + statement + "\nCounted by hand.",
		Context:   "Counted during the tide station survey.",
		Type:      "fact",
		Tags:      []string{"stations", "registry"},
	}, &stored)
	if found := namedLinks(t, "remember", result); len(found) != 1 || found[0].Name != statement {
		t.Errorf("remember linked %s, want one link named %q", linksSaid(found), statement)
	}

	var recalled mcp.RecallOut
	result = call(t, session, "recall", mcp.RecallIn{Query: "how many tide stations report hourly"}, &recalled)
	if len(recalled.Results) != 1 || recalled.Results[0].Title != statement {
		t.Errorf("recall answered %+v, want the memory titled %q", recalled.Results, statement)
	}
	if found := namedLinks(t, "recall", result); len(found) != 1 || found[0].Name != statement {
		t.Errorf("recall linked %s, want one link named %q", linksSaid(found), statement)
	}

	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: stored.URI}, &opened)
	if opened.Title != statement {
		t.Errorf("open titles the memory %q, want %q", opened.Title, statement)
	}
}

// Every link any tool returns is named, across every kind of record and every
// address open reads.
func TestEveryLinkAToolReturnsIsNamed(t *testing.T) {
	session, v := serve(t)

	parent := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Tidepool gauge survey",
		Summary:  "Went through which unit each Tidepool gauge reads in.",
		Messages: conversation("parent"),
	})
	address, err := mcp.Parse(parent.URI)
	if err != nil {
		t.Fatalf("parse %s: %v", parent.URI, err)
	}
	run, err := v.SaveSession(context.Background(), vault.SaveSessionRequest{
		Title:    "Registry audit",
		Summary:  "Audited the Tidepool station registry for duplicate gauges.",
		Kind:     record.SessionSubagent,
		Lineage:  record.Lineage{ParentSession: &address.ID},
		Messages: conversation("run"),
	})
	if err != nil {
		t.Fatalf("save the run: %v", err)
	}
	old := storeMemory(t, session, mcp.RememberIn{
		Statement: "Tidepool gauges read in feet.",
		Context:   "From the gauge survey, before the metric switch.",
		Type:      "fact",
		Tags:      []string{"gauges", "units"},
		Session:   parent.URI,
	})
	var replaced mcp.RememberOut
	result := call(t, session, "remember", mcp.RememberIn{
		Statement:  "Tidepool gauges read in metres since the metric switch.",
		Context:    "The gauge survey found every unit had been replaced.",
		Type:       "fact",
		Tags:       []string{"gauges", "units"},
		Supersedes: old,
		Session:    parent.URI,
	}, &replaced)
	namedLinks(t, "remember", result)

	var recalled mcp.RecallOut
	namedLinks(t, "recall", call(t, session, "recall", mcp.RecallIn{
		Query: "which unit do Tidepool gauges read in", IncludeSuperseded: true, Limit: 10,
	}, &recalled))
	var listed mcp.BrowseOut
	namedLinks(t, "browse", call(t, session, "browse", mcp.BrowseIn{IncludeSuperseded: true}, &listed))
	var saved mcp.SaveSessionOut
	namedLinks(t, "save_session", call(t, session, "save_session",
		mcp.SaveSessionIn{Session: parent.URI, Messages: conversation("later")}, &saved))

	for _, uri := range []string{
		mcp.VaultURI, mcp.GuideURI, old, replaced.URI, parent.URI, parent.Transcript,
		mcp.URI(record.KindSession, run.ID),
	} {
		var opened mcp.OpenOut
		found := namedLinks(t, "open "+uri, call(t, session, "open", mcp.OpenIn{URI: uri}, &opened))
		if len(found) == 0 {
			t.Errorf("open %s returned no links, so this checks nothing about them", uri)
		}
	}
}

// The guard on a link's name gives a link with nothing to call it the address it
// links to, and leaves a real name alone.
func TestALinkIsNeverBuiltWithoutAName(t *testing.T) {
	const uri = "sennit://memory/0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name, want string
	}{
		{"", uri},
		{"  \t\n", uri},
		{"Tide station survey", "Tide station survey"},
	} {
		link := mcp.ResourceLinkForTest(uri, tc.name, "a memory", "text/markdown")
		if link.Name != tc.want {
			t.Errorf("a link given the name %q is named %q, want %q", tc.name, link.Name, tc.want)
		}
		if link.URI != uri || link.Description != "a memory" || link.MIMEType != "text/markdown" {
			t.Errorf("the guard changed more than the name: %+v", link)
		}
	}
}
