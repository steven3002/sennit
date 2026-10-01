package mcp_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/store/packer"
	"github.com/steven3002/sennit/vault"
)

// vaultCounts reads what sennit://vault says this vault holds.
func vaultCounts(t *testing.T, session *sdk.ClientSession) mcp.VaultDetail {
	t.Helper()
	read, err := session.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: mcp.VaultURI})
	if err != nil {
		t.Fatalf("read %s: %v", mcp.VaultURI, err)
	}
	var detail mcp.VaultDetail
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &detail); err != nil {
		t.Fatalf("parse %s: %v", mcp.VaultURI, err)
	}
	return detail
}

// answeredWith lists the addresses a recall answered with.
func answeredWith(out mcp.RecallOut) []string {
	uris := make([]string, len(out.Results))
	for i, hit := range out.Results {
		uris[i] = hit.URI
	}
	return uris
}

// A recall in the server that forgot a conversation still answers, and does not
// answer with the conversation; the vault's own counts drop by what went.
//
// Forgetting a conversation took its vector off the disk and left it in the
// searchable index the server loaded when it started. Every recall that ranked
// the conversation afterwards failed outright, and at the default limit that was
// every recall in a small vault, whatever it asked. The vault resource went on
// counting the conversation as searchable, and its transcript as owed to the
// network.
func TestRecallStillAnswersAfterAConversationIsForgotten(t *testing.T) {
	session, _ := serve(t)

	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Tidepool gauge survey",
		Summary:  "Went through which unit each Tidepool gauge reads in.",
		Messages: conversation("survey"),
	})
	harbour := storeMemory(t, session, mcp.RememberIn{
		Statement: "The harbour gauge at Tidepool reads in centimetres.",
		Context:   "From the Tidepool field guide, written to be recalled after a conversation is forgotten.",
		Type:      "fact",
		Tags:      []string{"harbour", "gauge"},
	})
	river := storeMemory(t, session, mcp.RememberIn{
		Statement: "The river gauge at Tidepool reads in millimetres.",
		Context:   "From the Tidepool field guide, written to be recalled after a conversation is forgotten.",
		Type:      "fact",
		Tags:      []string{"river", "gauge"},
	})
	before := vaultCounts(t, session)

	var forgotten mcp.ForgetOut
	call(t, session, "forget", mcp.ForgetIn{URI: saved.URI, Confirm: true}, &forgotten)
	if !forgotten.Removed {
		t.Fatal("forget reported removing nothing")
	}

	after := vaultCounts(t, session)
	if after.Indexed != before.Indexed-1 {
		t.Errorf("the vault counts %d record(s) searchable by meaning after the forget, want %d",
			after.Indexed, before.Indexed-1)
	}
	if after.Pending != before.Pending-saved.Chunks {
		t.Errorf("the vault counts %d record(s) owed to the network after the forget, want %d",
			after.Pending, before.Pending-saved.Chunks)
	}
	if after.Sessions != before.Sessions-1 {
		t.Errorf("the vault counts %d conversation(s) after the forget, want %d", after.Sessions, before.Sessions-1)
	}

	for _, question := range []string{
		"which unit does each Tidepool gauge read in",
		"what does the harbour gauge read in",
		"what does the river gauge read in",
	} {
		var out mcp.RecallOut
		call(t, session, "recall", mcp.RecallIn{Query: question}, &out)
		answered := answeredWith(out)
		if slices.Contains(answered, saved.URI) {
			t.Errorf("%q answered with the forgotten conversation", question)
		}
		if !slices.Contains(answered, harbour) || !slices.Contains(answered, river) {
			t.Errorf("%q lost a memory that was not forgotten: %v", question, answered)
		}
	}

	// A search scoped to conversations finds none, and says the two memories
	// were all it left out. The forgotten conversation's vector was still a
	// candidate there, counted as one more record outside the scope.
	var scoped mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: "Tidepool gauge survey", Scope: []string{"session"}}, &scoped)
	if len(scoped.Results) != 0 || scoped.ScopeExcluded != 2 {
		t.Errorf("a search scoped to conversations answered %v and left out %d candidate(s), want nothing "+
			"and the 2 memories", answeredWith(scoped), scoped.ScopeExcluded)
	}
}

// Forgetting a conversation that delegated runs says the runs are still stored,
// and names them; each still opens, resumes, is recalled and listed, and can be
// forgotten in its turn.
//
// Opening a conversation lists its runs as part of it, and the forget used to
// report only that the conversation and its transcript were gone, which an
// agent reads as the runs having gone too. They are conversations of their own
// and stay, and once the conversation that listed them is gone, the answer to
// the forget is the one place left that says where they are.
func TestForgettingAConversationSaysTheRunsItDelegatedAreStillStored(t *testing.T) {
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
	// A run is saved through the Go SDK, which is where an agent that delegates
	// work records it.
	run, err := v.SaveSession(context.Background(), vault.SaveSessionRequest{
		Title:    "Registry audit",
		Summary:  "Audited the Tidepool station registry for duplicate gauges.",
		Kind:     record.SessionSubagent,
		AgentRef: &record.AgentRef{ID: "agent_7", Name: "auditor", Role: "explore"},
		Lineage:  record.Lineage{ParentSession: &address.ID},
		Messages: conversation("run"),
	})
	if err != nil {
		t.Fatalf("save the run: %v", err)
	}
	runURI := mcp.URI(record.KindSession, run.ID)

	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: parent.URI}, &opened)
	if !strings.Contains(opened.Content, runURI) {
		t.Fatalf("the conversation does not list its run, so this checks nothing about one: %s", opened.Content)
	}

	var forgotten mcp.ForgetOut
	result := call(t, session, "forget", mcp.ForgetIn{URI: parent.URI, Confirm: true}, &forgotten)
	if !forgotten.Removed {
		t.Fatal("forget reported removing nothing")
	}
	for _, said := range []string{forgotten.Note, resultText(result)} {
		if !strings.Contains(said, runURI) || !strings.Contains(said, "not removed") {
			t.Errorf("the forget does not say the run it delegated is still stored: %q", said)
		}
	}

	call(t, session, "open", mcp.OpenIn{URI: runURI}, &opened)
	if !strings.Contains(opened.Content, "Registry audit") {
		t.Errorf("the run opens as %q", opened.Content)
	}
	if framing, turns := resumeByAddress(t, session, runURI); !strings.Contains(framing, "Registry audit") ||
		!strings.Contains(turns, "Forty-one stations") {
		t.Errorf("the run resumes as %q with turns %q", framing, turns)
	}
	var recalled mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: "the Tidepool station registry audit"}, &recalled)
	if answered := answeredWith(recalled); !slices.Contains(answered, runURI) || slices.Contains(answered, parent.URI) {
		t.Errorf("recall answered %v, want the run and not the conversation it came from", answered)
	}
	var listed mcp.BrowseOut
	call(t, session, "browse", mcp.BrowseIn{}, &listed)
	var rows []string
	for _, row := range listed.Rows {
		rows = append(rows, row.URI)
	}
	if !slices.Equal(rows, []string{runURI}) {
		t.Errorf("browse lists %v, want the run alone", rows)
	}

	call(t, session, "forget", mcp.ForgetIn{URI: runURI, Confirm: true}, &forgotten)
	if !forgotten.Removed || strings.Contains(forgotten.Note, "run(s)") {
		t.Errorf("forgetting the run answered %+v", forgotten)
	}
	call(t, session, "forget", mcp.ForgetIn{URI: runURI, Confirm: true}, &forgotten)
	if forgotten.Removed {
		t.Error("forgetting the run twice claimed to remove something twice")
	}
}

// A forget that meets a flush writing part of the conversation says nothing was
// removed and to forget it again, and the conversation is untouched until then.
func TestAForgetAFlushIsPartlyWritingSaysNothingWasRemoved(t *testing.T) {
	home := t.TempDir()
	v := openVault(t, home)
	session := connect(t, mcp.New(v))

	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Tidepool gauge survey",
		Summary:  "Went through which unit each Tidepool gauge reads in.",
		Messages: conversation("survey"),
	})
	// A flush in another process over the same vault claims the queue.
	device, err := local.Open(filepath.Join(home, "vault.db"))
	if err != nil {
		t.Fatalf("open the device store as a second process would: %v", err)
	}
	defer device.Close()
	claimed, err := device.ClaimQueued("a flush under way", packer.DefaultClaimTimeout, 0)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("claim the queue: %d record(s), %v", len(claimed), err)
	}

	result := callRaw(t, session, "forget", mcp.ForgetIn{URI: saved.URI, Confirm: true})
	if !result.IsError {
		t.Fatal("a conversation a flush is writing was forgotten anyway")
	}
	for _, want := range []string{"nothing was removed", "forget it again"} {
		if !strings.Contains(resultText(result), want) {
			t.Errorf("the refusal does not say %q: %s", want, resultText(result))
		}
	}
	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: saved.Transcript}, &opened)
	if !strings.Contains(opened.Content, "Forty-one stations") {
		t.Errorf("the refused forget took part of the transcript: %s", opened.Content)
	}

	// The flush gives up and hands its claim back, as a failed flush does.
	ids := make([]record.ID, len(claimed))
	for i, blob := range claimed {
		ids[i] = blob.ID
	}
	if err := device.ReleaseQueued(ids); err != nil {
		t.Fatalf("hand the claim back: %v", err)
	}
	var forgotten mcp.ForgetOut
	call(t, session, "forget", mcp.ForgetIn{URI: saved.URI, Confirm: true}, &forgotten)
	if !forgotten.Removed {
		t.Error("forgetting the conversation once the flush had let go reported removing nothing")
	}
	if pending := vaultCounts(t, session).Pending; pending != 0 {
		t.Errorf("%d record(s) are still owed to the network after the forget, want none", pending)
	}
}

// A memory recorded against a conversation that has been forgotten is refused
// with an answer saying the conversation is not in this vault and nothing was
// stored, and nothing is: recall does not find it and the vault counts none.
//
// The memory used to be stored first and refused afterwards, so the agent was
// told it had not been stored when it had, and a retry stored a second copy.
func TestAMemoryFromAForgottenConversationIsRefusedWithNothingStored(t *testing.T) {
	session, _ := serve(t)

	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Tidepool gauge survey",
		Summary:  "Went through which unit each Tidepool gauge reads in.",
		Messages: conversation("survey"),
	})
	var forgotten mcp.ForgetOut
	call(t, session, "forget", mcp.ForgetIn{URI: saved.URI, Confirm: true}, &forgotten)

	statement := "The east gauge at Tidepool was recalibrated in July."
	result := callRaw(t, session, "remember", mcp.RememberIn{
		Statement: statement,
		Context:   "Recorded against the conversation it came from, which had just been forgotten.",
		Type:      "fact",
		Tags:      []string{"gauge", "calibration"},
		Session:   saved.URI,
		Span:      "survey-1..survey-2",
	})
	if !result.IsError {
		t.Fatal("a memory from a forgotten conversation was stored")
	}
	for _, want := range []string{saved.URI, "not a conversation in this vault", "nothing was stored"} {
		if !strings.Contains(resultText(result), want) {
			t.Errorf("the refusal does not say %q: %s", want, resultText(result))
		}
	}

	var recalled mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: "When was the east gauge recalibrated?"}, &recalled)
	for _, hit := range recalled.Results {
		if hit.Title == statement {
			t.Errorf("recall found the refused memory at %s", hit.URI)
		}
	}
	if counts := vaultCounts(t, session); counts.Memories != 0 || counts.Pending != 0 {
		t.Errorf("the vault counts %d memory(ies) and %d record(s) owed to the network, want none",
			counts.Memories, counts.Pending)
	}
}

// A save asked to reach the network at once, by a server that cannot reach it,
// says the conversation is stored on this device and where, and not to save it
// again.
//
// The conversation is stored and queued before the write to the network is
// tried, and the failed write was all the answer said. An agent told a save
// failed saves again, and a second save of a new conversation is a second
// conversation.
func TestADurableSaveThatCannotReachTheNetworkSaysTheConversationIsStored(t *testing.T) {
	session, _ := serve(t)

	result := callRaw(t, session, "save_session", mcp.SaveSessionIn{
		Title:    "Tidepool gauge survey",
		Summary:  "Went through which unit each Tidepool gauge reads in.",
		Messages: conversation("durable"),
		Durable:  true,
	})
	if !result.IsError {
		t.Fatal("a server with no connection reported writing a conversation to the network")
	}
	var listed mcp.BrowseOut
	call(t, session, "browse", mcp.BrowseIn{}, &listed)
	if len(listed.Rows) != 1 {
		t.Fatalf("the device lists %d record(s), want the conversation the save stored", len(listed.Rows))
	}
	stored := listed.Rows[0].URI
	for _, want := range []string{stored, "stored on this device", "Do not save it again"} {
		if !strings.Contains(resultText(result), want) {
			t.Errorf("the answer does not say %q: %s", want, resultText(result))
		}
	}
	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: stored}, &opened)
	if !strings.Contains(opened.Content, "Tidepool gauge survey") {
		t.Errorf("the conversation the answer names opens as %q", opened.Content)
	}
}
