package mcp_test

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/internal/siatest"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
	"github.com/steven3002/sennit/vault"
)

// serveWithoutHosts connects a client to a server whose vault reaches an
// indexer with no hosts to upload to, and whose queue is due as soon as it
// holds anything. Every remember sets off a flush, and the network refuses
// every one.
func serveWithoutHosts(t *testing.T) *sdk.ClientSession {
	t.Helper()
	indexer, key := siatest.IndexerWithoutHosts(t)
	v, err := vault.Open(context.Background(), vault.Options{
		Home:     t.TempDir(),
		Phrase:   testPhrase,
		Embedder: embedtest.NewStub(),
		Indexer:  indexer,
		AppKey:   key,
		Flush:    packer.Immediate(store.DefaultSlabPayloadSize),
	})
	if err != nil {
		t.Fatalf("open a vault over an indexer with no hosts: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	return connect(t, mcp.New(v))
}

// An agent that takes the answer at its word stores a memory once when the
// upload its remember set off is refused, and the answer says where the memory
// is, why the upload failed, and not to store it again.
//
// The memory was stored and queued before the flush ran, and the flush's error
// was the whole answer. An agent told the write had failed wrote it again,
// under a new id, and both copies went to the network with the next flush that
// worked.
func TestAnAgentStoresAMemoryOnceWhenItsUploadIsRefused(t *testing.T) {
	session := serveWithoutHosts(t)
	in := mcp.RememberIn{
		Statement: "The harbour gauge at Tidepool reads in centimetres.",
		Context:   "From the Tidepool field guide, written while the network refused every upload.",
		Type:      "fact",
		Tags:      []string{"harbour", "gauge"},
	}

	// The agent writes the memory again only while the answer says the write
	// failed, which is what an agent does with a failure, and gives up after
	// three tries.
	var answer *sdk.CallToolResult
	for range 3 {
		if answer = callRaw(t, session, "remember", in); !answer.IsError {
			break
		}
	}
	var listed mcp.BrowseOut
	call(t, session, "browse", mcp.BrowseIn{}, &listed)
	if len(listed.Rows) != 1 {
		t.Fatalf("the vault holds %d copies of the memory, want one. The last answer: %s",
			len(listed.Rows), resultText(answer))
	}
	if answer.IsError {
		t.Fatalf("a memory stored on this device was answered as a failure: %s", resultText(answer))
	}

	var out mcp.RememberOut
	decodeStructured(t, "remember", answer, &out)
	if out.URI != listed.Rows[0].URI {
		t.Errorf("the answer names %s and the vault holds %s", out.URI, listed.Rows[0].URI)
	}
	if out.OnNetwork {
		t.Error("the answer says the memory reached the network, and its upload was refused")
	}
	for _, want := range []string{
		"Stored on this device", "still queued", "the upload this call set off failed",
		"not enough hosts", "`sennit flush`", "do not remember it again",
	} {
		if !strings.Contains(out.Durability, want) {
			t.Errorf("the durability line does not say %q: %s", want, out.Durability)
		}
	}
	if strings.Contains(out.Durability, "written to the network") {
		t.Errorf("the durability line says the memory was written to the network: %s", out.Durability)
	}
	if !strings.Contains(resultText(answer), out.Durability) {
		t.Errorf("the text a model reads does not carry the durability line: %s", resultText(answer))
	}
	if counts := vaultCounts(t, session); counts.Memories != 1 || counts.Pending != 1 {
		t.Errorf("the vault counts %d memory(ies) and %d record(s) owed to the network, want one of each",
			counts.Memories, counts.Pending)
	}
}
