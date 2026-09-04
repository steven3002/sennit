package mcp_test

import (
	"strings"
	"testing"

	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
)

func TestParseRoundTripsEveryAddressableForm(t *testing.T) {
	id, err := record.NewID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}
	for uri, want := range map[string]mcp.Form{
		mcp.VaultURI:                    mcp.FormVault,
		mcp.GuideURI:                    mcp.FormGuide,
		mcp.URI(record.KindMemory, id):  mcp.FormMemory,
		mcp.URI(record.KindSession, id): mcp.FormSession,
		mcp.TranscriptURI(id):           mcp.FormTranscript,
	} {
		address, err := mcp.Parse(uri)
		if err != nil {
			t.Fatalf("parse %s: %v", uri, err)
		}
		if address.Form != want {
			t.Fatalf("%s parsed as %s, want %s", uri, address.Form, want)
		}
		if address.URI != uri {
			t.Fatalf("%s came back as %s", uri, address.URI)
		}
		if want != mcp.FormVault && want != mcp.FormGuide && address.ID != id {
			t.Fatalf("%s resolved to record %s, want %s", uri, address.ID, id)
		}
	}
}

func TestParseRejectsMalformedAddresses(t *testing.T) {
	id, _ := record.NewID()
	for _, uri := range []string{
		"",
		"https://example.com/memory/" + id.String(),
		"sennit://memory",
		"sennit://skill/" + id.String(),
		"sennit://memory/not-an-id",
		"sennit://memory/" + id.String() + "ff",
		// A memory has no addressable parts, and a session's only one is the
		// transcript. Accepting either silently would give a caller an address
		// that resolves to something it did not ask for.
		"sennit://memory/" + id.String() + "/transcript",
		"sennit://session/" + id.String() + "/messages",
		"sennit://vault/" + id.String(),
	} {
		if _, err := mcp.Parse(uri); err == nil {
			t.Fatalf("%q was accepted", uri)
		}
	}
}

// I3, as a property rather than a promise: everything this server can resolve is
// named in the instructions.
//
// A host fetches resources/list at connect and keeps it to itself, so
// registering an address does not make it reachable, the model never sees the
// listing, and it will correctly refuse to guess a URI it was never told about.
// The failure is silent, which is why it is asserted here.
func TestEveryAddressableFormIsNamedInTheInstructions(t *testing.T) {
	for _, uri := range append(append([]string{}, mcp.Fixed...), mcp.Templates...) {
		if !strings.Contains(mcp.Instructions, uri) {
			t.Errorf("the instructions never name %s, so a model cannot reach it", uri)
		}
	}
	for _, tool := range mcp.Tools {
		if !strings.Contains(mcp.Instructions, tool.Name) {
			t.Errorf("the instructions never name the %q tool", tool.Name)
		}
	}
	if !strings.Contains(mcp.Instructions, mcp.ResumePrompt) {
		t.Errorf("the instructions never name the %q prompt", mcp.ResumePrompt)
	}
}
