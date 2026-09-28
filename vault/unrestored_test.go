package vault_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// These are the answers a device gives through the protocol when it has nothing
// to show, asked of devices in each state a hydrate leaves. They are here rather
// than beside the rest of the protocol's tests because a hydrated device can
// only be built inside this package, from the network the test stands in for.

// The two memories writerNetwork stores, and words that find the first.
const (
	harbourGauge = "The harbour gauge at Tidepool reads in centimetres."
	riverGauge   = "The river gauge at Tidepool reads in millimetres."
	gaugeQuery   = "what does the harbour gauge at Tidepool read in"
)

// writerNetwork has a writer store two memories and then a conversation, each
// flushed to the network the test stands in for, and returns the network after
// the memories and after the conversation as well.
func writerNetwork(t *testing.T) (memories, everything []vault.StoredObject) {
	t.Helper()
	writer := vault.OpenWithLedgerForTest(t, t.TempDir())
	remember(t, writer, harbourGauge, record.TypeFact, "harbour", "gauge")
	remember(t, writer, riverGauge, record.TypeFact, "river", "gauge")
	memories = vault.FlushToNetworkForTest(t, writer)

	if _, err := writer.SaveSession(context.Background(), vault.SaveSessionRequest{
		Title:    "Tidepool station survey",
		Summary:  "Counted the stations reporting hourly.",
		Messages: conversation("survey"),
	}); err != nil {
		t.Fatalf("save the conversation: %v", err)
	}
	transcript := vault.FlushToNetworkForTest(t, writer)
	if len(memories) != 2 || len(transcript) != 1 {
		t.Fatalf("the network holds %d memory object(s) and %d transcript object(s), want 2 and 1",
			len(memories), len(transcript))
	}
	return memories, append(append([]vault.StoredObject{}, memories...), transcript...)
}

// serveDevice connects a real MCP client to a server over one device's vault.
func serveDevice(t *testing.T, v *vault.Vault) *sdk.ClientSession {
	t.Helper()
	clientSide, serverSide := sdk.NewInMemoryTransports()
	serverSession, err := mcp.New(v).Connect(context.Background(), serverSide)
	if err != nil {
		t.Fatalf("connect the server: %v", err)
	}
	t.Cleanup(func() { serverSession.Wait() })

	client := sdk.NewClient(&sdk.Implementation{Name: "sennit-test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), clientSide, nil)
	if err != nil {
		t.Fatalf("connect the client: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// callTool invokes a tool, decodes its structured content into out, and returns
// the text a model reads.
func callTool[T any](t *testing.T, session *sdk.ClientSession, name string, args any, out *T) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	var text string
	for _, content := range result.Content {
		if block, ok := content.(*sdk.TextContent); ok {
			text = block.Text
			break
		}
	}
	if result.IsError {
		t.Fatalf("%s reported an error: %s", name, text)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("%s structured content: %v", name, err)
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		t.Fatalf("%s structured content did not fit its own schema: %v", name, err)
	}
	return text
}

// resumeRefusal asks for a resume and returns what it refused with, or nothing
// when it resumed or offered a list.
func resumeRefusal(t *testing.T, session *sdk.ClientSession, arguments map[string]string) string {
	t.Helper()
	_, err := session.GetPrompt(context.Background(), &sdk.GetPromptParams{
		Name: mcp.ResumePrompt, Arguments: arguments,
	})
	if err == nil {
		return ""
	}
	return err.Error()
}

// vaultResource reads sennit://vault as the fields a model reads.
func vaultResource(t *testing.T, session *sdk.ClientSession) map[string]any {
	t.Helper()
	read, err := session.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: mcp.VaultURI})
	if err != nil {
		t.Fatalf("read %s: %v", mcp.VaultURI, err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &detail); err != nil {
		t.Fatalf("%s is not JSON: %v", mcp.VaultURI, err)
	}
	return detail
}

// resumeBothWays is the two resumes that name no conversation: the list, and
// the most recent.
var resumeBothWays = map[string]map[string]string{
	"resume":        nil,
	"resume recent": {"session": mcp.ResumeRecent},
}

// A device that has not restored the vault does not call it empty.
//
// Everything below reads what this device holds. A device that never hydrated
// holds nothing the network does, and one that hydrated at catalog depth holds
// only where the records are, because the hydrate stopped before restoring any
// of them. The writer's records are on the network here, so an answer that said
// the vault held nothing would be false. Each one says instead that this device
// has not restored the vault, and names the command that does.
func TestADeviceThatHasNotRestoredTheVaultDoesNotCallItEmpty(t *testing.T) {
	_, network := writerNetwork(t)

	catalogued := vault.OpenWithLedgerForTest(t, t.TempDir())
	vault.RestoreForTest(t, catalogued, network, vault.HydrateCatalog)

	// Nothing on a device that never hydrated tells it from the first device of
	// a vault just created, so what it says has to be true of that vault too. A
	// device that has catalogued records knows the vault is not new.
	const createdHere = "If this vault was just created here, there is nothing to restore"
	devices := []struct {
		name   string
		device *vault.Vault
		// missing is what the answers say this device has not restored, and
		// conversations is what a resume says of it. A resume is short of
		// conversations and not of memories, so it leaves the memories out.
		missing, conversations []string
		// fresh says whether the answers allow for a vault just created here.
		fresh bool
	}{
		{"a device that never hydrated", vault.OpenWithLedgerForTest(t, t.TempDir()),
			[]string{"any record of this vault"}, []string{"any record of this vault"}, true},
		{"a device hydrated at catalog depth", catalogued,
			[]string{"2 memory record(s)", "at least one stored conversation"},
			[]string{"at least one stored conversation"}, false},
	}
	for _, tc := range devices {
		t.Run(tc.name, func(t *testing.T) {
			session := serveDevice(t, tc.device)
			says := func(answer, text, command string, names ...string) {
				t.Helper()
				if !strings.Contains(text, "has not restored") || !strings.Contains(text, command) {
					t.Errorf("%s does not say this device has not restored the vault, or name %s: %q",
						answer, command, text)
				}
				for _, name := range names {
					if !strings.Contains(text, name) {
						t.Errorf("%s does not name %q: %q", answer, name, text)
					}
				}
				for _, empty := range []string{"This vault holds nothing", "this vault holds no conversations"} {
					if strings.Contains(text, empty) {
						t.Errorf("%s calls the vault empty: %q", answer, text)
					}
				}
				if strings.Contains(text, createdHere) != tc.fresh {
					t.Errorf("%s allows for a vault just created here: %t, want %t: %q",
						answer, !tc.fresh, tc.fresh, text)
				}
			}

			var listed mcp.BrowseOut
			text := callTool(t, session, "browse", mcp.BrowseIn{}, &listed)
			if len(listed.Rows) != 0 {
				t.Fatalf("browse listed %d row(s) on a device holding none", len(listed.Rows))
			}
			says("browse", listed.Hint, "`sennit hydrate`", tc.missing...)
			if !strings.Contains(text, listed.Hint) {
				t.Errorf("the text a model reads does not carry the browse hint: %q", text)
			}

			var found mcp.RecallOut
			text = callTool(t, session, "recall", mcp.RecallIn{Query: gaugeQuery}, &found)
			if len(found.Results) != 0 {
				t.Fatalf("recall found %d record(s) on a device holding none", len(found.Results))
			}
			says("recall", found.Hint, "`sennit hydrate --depth index`", tc.missing...)
			if !strings.Contains(text, found.Hint) {
				t.Errorf("the text a model reads does not carry the recall hint: %q", text)
			}

			for way, arguments := range resumeBothWays {
				refusal := resumeRefusal(t, session, arguments)
				if refusal == "" {
					t.Fatalf("%s resumed something on a device holding no conversation", way)
				}
				says(way, refusal, "`sennit hydrate`", tc.conversations...)
			}

			detail := vaultResource(t, session)
			if records, _ := detail["records"].(float64); records != 0 {
				t.Errorf("the vault resource counts %v record(s) on a device holding none", records)
			}
			restore, _ := detail["restore"].(string)
			says("the vault resource", restore, "`sennit hydrate`", tc.missing...)
		})
	}
}

// A device that restored the vault answers as it always has: from what it holds
// when it holds something, and in the words that call the vault empty once what
// it restored is gone.
func TestADeviceThatRestoredTheVaultKeepsItsAnswers(t *testing.T) {
	memories, network := writerNetwork(t)

	t.Run("holding what it restored", func(t *testing.T) {
		restored := vault.OpenWithLedgerForTest(t, t.TempDir())
		vault.RestoreForTest(t, restored, network, vault.HydrateIndex)
		session := serveDevice(t, restored)

		var listed mcp.BrowseOut
		callTool(t, session, "browse", mcp.BrowseIn{}, &listed)
		if len(listed.Rows) != 3 || listed.Hint != "" {
			t.Errorf("browse listed %d row(s) with hint %q, want the 2 memories and the conversation "+
				"and no hint", len(listed.Rows), listed.Hint)
		}
		var found mcp.RecallOut
		callTool(t, session, "recall", mcp.RecallIn{Query: gaugeQuery}, &found)
		if len(found.Results) == 0 || found.Results[0].Title != harbourGauge {
			t.Errorf("recall answered %+v, want the harbour gauge first", found.Results)
		}
		for way, arguments := range resumeBothWays {
			if refusal := resumeRefusal(t, session, arguments); refusal != "" {
				t.Errorf("%s refused on a device holding a conversation: %s", way, refusal)
			}
		}
		detail := vaultResource(t, session)
		if records, _ := detail["records"].(float64); records != 3 {
			t.Errorf("the vault resource counts %v record(s), want 3", records)
		}
		if restore, present := detail["restore"]; present {
			t.Errorf("the vault resource says what a restored device lacks: %v", restore)
		}
	})

	t.Run("after forgetting what it restored", func(t *testing.T) {
		emptied := vault.OpenWithLedgerForTest(t, t.TempDir())
		vault.RestoreForTest(t, emptied, memories, vault.HydrateIndex)
		session := serveDevice(t, emptied)

		var listed mcp.BrowseOut
		callTool(t, session, "browse", mcp.BrowseIn{}, &listed)
		if len(listed.Rows) != 2 {
			t.Fatalf("browse listed %d row(s) after restoring the 2 memories", len(listed.Rows))
		}
		for _, row := range listed.Rows {
			var forgotten mcp.ForgetOut
			callTool(t, session, "forget", mcp.ForgetIn{URI: row.URI, Confirm: true}, &forgotten)
		}

		callTool(t, session, "browse", mcp.BrowseIn{}, &listed)
		if want := "This vault holds nothing to list yet. Records arrive through `remember` and " +
			"`save_session`."; len(listed.Rows) != 0 || listed.Hint != want {
			t.Errorf("browse listed %d row(s) with hint %q, want none and %q", len(listed.Rows), listed.Hint, want)
		}
		var found mcp.RecallOut
		callTool(t, session, "recall", mcp.RecallIn{Query: gaugeQuery}, &found)
		if want := "This vault holds nothing searchable yet. Use `remember` to store the first " +
			"record."; len(found.Results) != 0 || found.Hint != want {
			t.Errorf("recall found %d record(s) with hint %q, want none and %q", len(found.Results), found.Hint, want)
		}
		for way, arguments := range resumeBothWays {
			want := "this vault holds no conversations yet. They are stored with `save_session`"
			if refusal := resumeRefusal(t, session, arguments); !strings.Contains(refusal, want) {
				t.Errorf("%s answered %q, want %q", way, refusal, want)
			}
		}
		if restore, present := vaultResource(t, session)["restore"]; present {
			t.Errorf("the vault resource says what a restored device lacks: %v", restore)
		}
	})
}
