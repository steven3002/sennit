package mcp_test

import (
	"strings"
	"testing"

	"github.com/steven3002/sennit/mcp"
)

// A new summary for a conversation whose turns have not reached the network
// does not tell the agent the conversation is on the network.
//
// It was found in a host check. An agent saved a conversation while the vault
// could not reach the network, then saved it again with a new summary and no
// turns, and the answer said "written to the network" with onNetwork true. The
// agent repeated it to the user, and the vault's own count still had the first
// save's turns waiting.
func TestANewSummaryForAQueuedConversationDoesNotSayItIsOnTheNetwork(t *testing.T) {
	session, _ := serve(t)

	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Host check",
		Summary:  "Saved this conversation while the vault could not reach the network.",
		Messages: conversation("host"),
	})
	if saved.OnNetwork {
		t.Fatal("a conversation saved with no connection said it was on the network")
	}

	var again mcp.SaveSessionOut
	answer := call(t, session, "save_session", mcp.SaveSessionIn{
		Session: saved.URI,
		Summary: "Saved it again with a new summary and no turns.",
	}, &again)
	if again.OnNetwork {
		t.Error("the answer says the conversation reached the network, and its turns are still queued")
	}
	if strings.Contains(again.Durability, "written to the network") {
		t.Errorf("the durability line says the conversation was written to the network: %s", again.Durability)
	}
	if !strings.Contains(again.Durability, "Stored on this device") {
		t.Errorf("the durability line does not say where the conversation is: %s", again.Durability)
	}
	if text := resultText(answer); strings.Contains(text, "written to the network") || !strings.Contains(text, again.Durability) {
		t.Errorf("the text a model reads does not carry the durability line, or says the network has it: %s", text)
	}
	if counts := vaultCounts(t, session); counts.Pending != saved.Chunks {
		t.Errorf("the vault counts %d record(s) owed to the network, want the %d piece(s) the first save wrote",
			counts.Pending, saved.Chunks)
	}
}

// A durable save of a new summary, by a server that cannot reach the network,
// says the conversation is stored on this device and that only the write to the
// network failed, as a durable save of new turns does.
//
// Durable asks for the conversation to be on the network. A new summary alone
// ran no flush, since it had written nothing, and answered that the
// conversation had been written to the network.
func TestADurableNewSummaryThatCannotReachTheNetworkSaysTheConversationIsStored(t *testing.T) {
	session, _ := serve(t)

	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Host check",
		Summary:  "Saved this conversation while the vault could not reach the network.",
		Messages: conversation("host"),
	})
	const summary = "Saved it again with a new summary, asking for it on the network now."
	result := callRaw(t, session, "save_session", mcp.SaveSessionIn{
		Session: saved.URI,
		Summary: summary,
		Durable: true,
	})
	if !result.IsError {
		t.Fatalf("a server with no connection answered a durable save as done: %s", resultText(result))
	}
	for _, want := range []string{saved.URI, "stored on this device", "Do not save it again"} {
		if !strings.Contains(resultText(result), want) {
			t.Errorf("the answer does not say %q: %s", want, resultText(result))
		}
	}
	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: saved.URI}, &opened)
	if detail, ok := opened.Detail.(map[string]any); !ok || detail["summary"] != summary {
		t.Errorf("the conversation does not carry the summary the failed write followed: %v", opened.Detail)
	}
}
