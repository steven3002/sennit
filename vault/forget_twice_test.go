package vault_test

import (
	"context"
	"testing"

	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// A memory and a conversation that reached the network are each forgotten twice
// through the server, and the second forget of each succeeds, says there was
// nothing at that address, and that nothing was removed.
//
// Forgetting a record that has reached the network marks its catalog entry
// removed rather than leaving no entry at all, and that mark is what the second
// forget meets. It is here rather than beside the protocol's other tests because
// a record reaches the network only through the network this package stands in
// for.
func TestARecordThatReachedTheNetworkIsForgottenTwiceWithoutAFailure(t *testing.T) {
	writer := vault.OpenWithLedgerForTest(t, t.TempDir())
	memory := remember(t, writer, harbourGauge, record.TypeFact, "harbour", "gauge")
	saved, err := writer.SaveSession(context.Background(), vault.SaveSessionRequest{
		Title:    "Tidepool station survey",
		Summary:  "Counted the stations reporting hourly.",
		Messages: conversation("survey"),
	})
	if err != nil {
		t.Fatalf("save the conversation: %v", err)
	}
	if flushed := vault.FlushToNetworkForTest(t, writer); len(flushed) != 1+len(saved.Chunks) {
		t.Fatalf("the network holds %d object(s), want the memory and %d transcript chunk(s)",
			len(flushed), len(saved.Chunks))
	}
	session := serveDevice(t, writer)

	for _, uri := range []string{mcp.URI(record.KindMemory, memory), mcp.URI(record.KindSession, saved.ID)} {
		var first, again mcp.ForgetOut
		callTool(t, session, "forget", mcp.ForgetIn{URI: uri, Confirm: true}, &first)
		if !first.Removed {
			t.Fatalf("the first forget of %s reported removing nothing", uri)
		}
		callTool(t, session, "forget", mcp.ForgetIn{URI: uri, Confirm: true}, &again)
		if want := (mcp.ForgetOut{URI: uri, Note: "There was nothing at that address; nothing was removed."}); again != want {
			t.Errorf("forgetting %s again answered %+v, want %+v", uri, again, want)
		}
	}
}
