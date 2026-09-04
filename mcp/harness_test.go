package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

const testPhrase = "abandon abandon abandon abandon abandon abandon " +
	"abandon abandon abandon abandon abandon about"

// vaultForServing opens an offline vault over a directory, with no model.
//
// These tests are about the protocol surface, not about which vector a record
// gets, so the stub embedder is the right one: cosine over it is term overlap,
// which is enough to assert that a record is found by its own words and
// explicitly not enough to measure retrieval quality.
//
// It takes no *testing.T because the subprocess that serves over real stdio has
// none.
func vaultForServing(home string) (*vault.Vault, error) {
	return vault.Open(context.Background(), vault.Options{
		Home:     home,
		Phrase:   testPhrase,
		Embedder: embedtest.NewStub(),
		Offline:  true,
	})
}

func openVault(t *testing.T, home string) *vault.Vault {
	t.Helper()
	v, err := vaultForServing(home)
	if err != nil {
		t.Fatalf("open vault at %s: %v", home, err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

// connect runs a real MCP client against a server over the same vault.
//
// It is an in-process transport and a real client: the same SDK a production
// host is built on, doing the same handshake and the same JSON round trip. What
// it does not test is a host's user interface, which is the part that needs a
// person.
func connect(t *testing.T, server *mcp.Server) *sdk.ClientSession {
	t.Helper()
	clientSide, serverSide := sdk.NewInMemoryTransports()

	serverSession, err := server.Connect(context.Background(), serverSide)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { serverSession.Wait() })

	client := sdk.NewClient(&sdk.Implementation{Name: "sennit-test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), clientSide, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// serve opens a vault in a fresh directory and connects a client to it.
func serve(t *testing.T) (*sdk.ClientSession, *vault.Vault) {
	t.Helper()
	v := openVault(t, t.TempDir())
	return connect(t, mcp.New(v)), v
}

// call invokes a tool and decodes its structured content into out.
//
// It fails on a tool error, so a test that means to exercise one uses callRaw.
func call[T any](t *testing.T, session *sdk.ClientSession, name string, args any, out *T) *sdk.CallToolResult {
	t.Helper()
	result := callRaw(t, session, name, args)
	if result.IsError {
		t.Fatalf("%s reported an error: %s", name, resultText(result))
	}
	decodeStructured(t, name, result, out)
	return result
}

// decodeStructured reads a result's structuredContent, which is the half a host
// doing code-mode consumes.
func decodeStructured[T any](t *testing.T, name string, result *sdk.CallToolResult, out *T) {
	t.Helper()
	if result.StructuredContent == nil {
		t.Fatalf("%s returned no structuredContent", name)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("%s structuredContent: %v", name, err)
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		t.Fatalf("%s structuredContent did not fit its own schema: %v", name, err)
	}
}

func callRaw(t *testing.T, session *sdk.ClientSession, name string, args any) *sdk.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name: name, Arguments: args,
	})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result
}

// resultText is the mirrored text block, which is the half a model reads.
func resultText(result *sdk.CallToolResult) string {
	for _, content := range result.Content {
		if text, ok := content.(*sdk.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

// links are the resource links a result carried.
func links(result *sdk.CallToolResult) []*sdk.ResourceLink {
	var out []*sdk.ResourceLink
	for _, content := range result.Content {
		if link, ok := content.(*sdk.ResourceLink); ok {
			out = append(out, link)
		}
	}
	return out
}

// storeMemory writes one memory through the protocol and returns its address.
func storeMemory(t *testing.T, session *sdk.ClientSession, in mcp.RememberIn) string {
	t.Helper()
	var out mcp.RememberOut
	call(t, session, "remember", in, &out)
	if out.URI == "" {
		t.Fatal("remember returned no address")
	}
	return out.URI
}

// conversation is a short transcript with the two things a flat chat log cannot
// hold: a tool call correlated to its result by id, and provider fields this
// build has never heard of.
func conversation(prefix string) []record.Message {
	return []record.Message{
		{
			ID:      prefix + "-1",
			Role:    record.RoleUser,
			Created: record.Now(),
			Parts:   []record.Part{{Type: record.PartText, Text: "How many tide stations report hourly?"}},
			Ext:     record.Ext{"cwd": json.RawMessage(`"/home/u/tidepool"`)},
		},
		{
			ID:      prefix + "-2",
			Role:    record.RoleAssistant,
			Created: record.Now(),
			Parent:  prefix + "-1",
			Parts: []record.Part{
				{Type: record.PartReasoning, Text: "The registry is authoritative.", Signature: "sig_" + prefix},
				{Type: record.PartToolCall, CallID: "toolu_" + prefix, Name: "Read",
					Input: json.RawMessage(`{"path":"registry.json"}`)},
			},
			Meta: record.MessageMeta{Model: "claude-opus-5", StopReason: "tool_use"},
		},
		{
			ID:     prefix + "-3",
			Role:   record.RoleTool,
			Parent: prefix + "-2",
			Parts: []record.Part{{
				Type: record.PartToolResult, CallID: "toolu_" + prefix,
				Content: []record.Part{{Type: record.PartText, Text: "41 stations"}},
			}},
		},
		{
			ID:     prefix + "-4",
			Role:   record.RoleAssistant,
			Parent: prefix + "-3",
			Parts:  []record.Part{{Type: record.PartText, Text: "Forty-one stations report hourly."}},
		},
	}
}
