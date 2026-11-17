package vault_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/internal/siatest"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// These are a running server's answers after another process on the same
// device has changed the vault: restored it, forgotten a record the server had
// found, or flushed a memory the server stored. The server runs in a process of
// its own, as a host runs it, and the change is made from the test's process,
// through a vault of its own over the same directory, as a command run in a
// terminal makes it.

const (
	// serveHomeEnv makes this test binary serve the vault in the directory it
	// names over stdio, instead of running tests.
	serveHomeEnv = "SENNIT_TEST_VAULT_SERVE_HOME"
	// serveIndexerEnv is the indexer that server connects to. Unset, the server
	// has no connection.
	serveIndexerEnv = "SENNIT_TEST_VAULT_SERVE_INDEXER"
)

func TestMain(m *testing.M) {
	if home := os.Getenv(serveHomeEnv); home != "" {
		os.Exit(serveOverStdio(home))
	}
	os.Exit(m.Run())
}

// serveOverStdio runs a server over the vault in home on this process's stdin
// and stdout, and reports the status to exit with.
func serveOverStdio(home string) int {
	opts := vault.Options{Home: home, Phrase: testPhrase, Embedder: embedtest.NewStub(), Offline: true}
	if indexer := os.Getenv(serveIndexerEnv); indexer != "" {
		key, err := keys.AppKeyFromEnv()
		if err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			return 1
		}
		opts.Offline, opts.Indexer, opts.AppKey = false, indexer, key
	}
	v, err := vault.Open(context.Background(), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	defer v.Close()
	if err := mcp.New(v).Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	return 0
}

// serveElsewhere starts a server over the vault in home, in a process of its
// own, and connects a client to it. The server connects to indexer with key, or
// has no connection when indexer is empty.
func serveElsewhere(t *testing.T, home, indexer string, key keys.AppKey) *sdk.ClientSession {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), serveHomeEnv+"="+home)
	if indexer != "" {
		cmd.Env = append(cmd.Env, serveIndexerEnv+"="+indexer, keys.AppKeyEnv+"="+hex.EncodeToString(key))
	}
	cmd.Stderr = os.Stderr
	client := sdk.NewClient(&sdk.Implementation{Name: "sennit-test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("start a server in a process of its own: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// askedIndexer stands in for an indexer, as siatest's does, and keeps what each
// request asked of it, so a test can say the server asked it nothing.
func askedIndexer(t *testing.T) (indexer string, key keys.AppKey, asked func() []string) {
	t.Helper()
	target, key := siatest.IndexerWithoutHosts(t)
	to, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse the indexer's address %s: %v", target, err)
	}
	proxy := httputil.NewSingleHostReverseProxy(to)
	var (
		mu       sync.Mutex
		requests []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server.URL, key, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(requests)
	}
}

// callToolRaw invokes a tool and returns its result, a tool error included, and
// the text a model reads.
func callToolRaw(t *testing.T, session *sdk.ClientSession, name string, args any) (*sdk.CallToolResult, string) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	for _, content := range result.Content {
		if block, ok := content.(*sdk.TextContent); ok {
			return result, block.Text
		}
	}
	return result, ""
}

// idAt reads the record id out of an address.
func idAt(t *testing.T, uri string) record.ID {
	t.Helper()
	address, err := mcp.Parse(uri)
	if err != nil {
		t.Fatalf("parse %s: %v", uri, err)
	}
	return address.ID
}

// noSharedWords is a question that shares no word with anything writerNetwork
// stores, so only a search by meaning can answer it.
const noSharedWords = "which instrument measures water level"

// A server that was running on a device holding nothing searches what another
// process then restored there, by meaning, and counts it, without a restart.
//
// The server read the catalog and the search index once, as it opened, and
// answered from them until it restarted: by shared words alone, with nothing
// searched by meaning, and a question that shared no word with any record was
// told the vault held nothing searchable. sennit://vault counted nothing
// indexed.
func TestARunningServerSearchesWhatAnotherProcessRestored(t *testing.T) {
	_, network := writerNetwork(t)
	home := t.TempDir()
	session := serveElsewhere(t, home, "", nil)
	if before := vaultResource(t, session); before["indexed"] != float64(0) {
		t.Fatalf("the device holds nothing yet, and the server counts %v records indexed", before["indexed"])
	}

	// A hydrate at index depth, run in a terminal while the server is up.
	restorer := vault.OpenWithLedgerForTest(t, home)
	vault.RestoreForTest(t, restorer, network, vault.HydrateIndex)
	if err := restorer.Close(); err != nil {
		t.Fatalf("close the restoring vault: %v", err)
	}

	var byMeaning mcp.RecallOut
	callTool(t, session, "recall", mcp.RecallIn{Query: noSharedWords}, &byMeaning)
	if byMeaning.LexicalHits != 0 {
		t.Fatalf("%q shares a word with %d record(s), so it does not ask for a search by meaning",
			noSharedWords, byMeaning.LexicalHits)
	}
	if byMeaning.Searched != 3 || len(byMeaning.Results) == 0 {
		t.Errorf("a question that shares no word with any record searched %d record(s) by meaning and found %d, "+
			"want all 3 restored searched and an answer; hint %q",
			byMeaning.Searched, len(byMeaning.Results), byMeaning.Hint)
	}
	if strings.Contains(byMeaning.Hint, "holds nothing searchable") {
		t.Errorf("the server calls the restored vault empty: %q", byMeaning.Hint)
	}

	var gauge mcp.RecallOut
	callTool(t, session, "recall", mcp.RecallIn{Query: gaugeQuery}, &gauge)
	if len(gauge.Results) == 0 || gauge.Results[0].Title != harbourGauge {
		t.Errorf("recall for the harbour gauge answered %+v, want it first", gauge.Results)
	}

	detail := vaultResource(t, session)
	for field, want := range map[string]float64{"indexed": 3, "records": 3, "memories": 2, "sessions": 1} {
		if detail[field] != want {
			t.Errorf("sennit://vault counts %s as %v, want %v", field, detail[field], want)
		}
	}
	if note, ok := detail["restore"]; ok {
		t.Errorf("sennit://vault says the device has not restored the vault: %v", note)
	}
}

// After another process forgets a memory a running server has found, the server
// neither returns it, nor fails on it, nor reads it back from the network.
//
// The server's catalog still named the memory and its search index still held
// its vector. Its next recall ranked the memory and failed to read it, and open
// failed the same way. A server with a connection went to the network for it,
// where its copy stays until the storage is reclaimed, and would have kept what
// it read on the device again.
func TestARunningServerLetsGoOfAMemoryAnotherProcessForgot(t *testing.T) {
	_, network := writerNetwork(t)
	home := t.TempDir()
	restorer := vault.OpenWithLedgerForTest(t, home)
	vault.RestoreForTest(t, restorer, network, vault.HydrateIndex)
	if err := restorer.Close(); err != nil {
		t.Fatalf("close the restoring vault: %v", err)
	}

	indexer, key, asked := askedIndexer(t)
	session := serveElsewhere(t, home, indexer, key)
	var found mcp.RecallOut
	callTool(t, session, "recall", mcp.RecallIn{Query: gaugeQuery}, &found)
	if len(found.Results) == 0 || found.Results[0].Title != harbourGauge {
		t.Fatalf("the server did not find the harbour gauge before it was forgotten: %+v", found.Results)
	}
	harbour := found.Results[0].URI
	before := asked()

	// A second agent's server, or a command, forgets it on the same device.
	forgetter := vault.OpenWithLedgerForTest(t, home)
	if err := forgetter.Forget(idAt(t, harbour)); err != nil {
		t.Fatalf("forget the harbour gauge from another process: %v", err)
	}
	if err := forgetter.Close(); err != nil {
		t.Fatalf("close the forgetting vault: %v", err)
	}

	result, text := callToolRaw(t, session, "recall", mcp.RecallIn{Query: gaugeQuery})
	if result.IsError {
		t.Errorf("recall after another process forgot the harbour gauge failed: %s", text)
	} else if strings.Contains(text, harbour) {
		t.Errorf("recall still returns the forgotten harbour gauge: %s", text)
	}
	opened, text := callToolRaw(t, session, "open", mcp.OpenIn{URI: harbour})
	if !opened.IsError || !strings.Contains(text, "no record at that address") {
		t.Errorf("open of the forgotten harbour gauge answered %q, want that there is no record there", text)
	}

	var listed mcp.BrowseOut
	callTool(t, session, "browse", mcp.BrowseIn{}, &listed)
	for _, row := range listed.Rows {
		if row.URI == harbour {
			t.Errorf("browse still lists the forgotten harbour gauge")
		}
	}
	detail := vaultResource(t, session)
	for field, want := range map[string]float64{"indexed": 2, "memories": 1} {
		if detail[field] != want {
			t.Errorf("sennit://vault counts %s as %v, want %v", field, detail[field], want)
		}
	}
	if after := asked(); len(after) != len(before) {
		t.Errorf("the server asked the network for what another process forgot: %q", after[len(before):])
	}
}

// A running server forgets a memory it stored and another process then flushed
// to the network, and the catalog every process reads says the memory is gone.
//
// The server read the catalog as it opened, before the flush catalogued the
// memory, so it found the memory neither queued nor catalogued. It took the
// memory off the device without marking it removed in the catalog, which kept
// naming it: the next process to open the vault counted it as a record still to
// restore, a restore brought it back, and nothing owed the network the deletion
// of its copy.
func TestARunningServerForgetsAMemoryAnotherProcessFlushed(t *testing.T) {
	home := t.TempDir()
	session := serveElsewhere(t, home, "", nil)
	var stored mcp.RememberOut
	callTool(t, session, "remember", mcp.RememberIn{
		Statement: harbourGauge,
		Context:   "From the Tidepool field guide, stored by the server and flushed by another process.",
		Type:      "fact",
		Tags:      []string{"harbour", "gauge"},
	}, &stored)
	id := idAt(t, stored.URI)

	// sennit flush, run in a terminal while the server is up.
	flusher := vault.OpenWithLedgerForTest(t, home)
	if flushed := vault.FlushToNetworkForTest(t, flusher); len(flushed) != 1 {
		t.Fatalf("the flush wrote %d object(s), want the 1 memory the server stored", len(flushed))
	}
	if err := flusher.Close(); err != nil {
		t.Fatalf("close the flushing vault: %v", err)
	}

	result, text := callToolRaw(t, session, "forget", mcp.ForgetIn{URI: stored.URI, Confirm: true})
	if result.IsError {
		t.Fatalf("forgetting the flushed memory failed: %s", text)
	}
	var forgotten mcp.ForgetOut
	decodeInto(t, result, &forgotten)
	if !forgotten.Removed || !strings.Contains(forgotten.Note, "is not deleted yet") {
		t.Errorf("the forget answered %+v, want the memory removed and its copy on Sia owed a deletion", forgotten)
	}
	if strings.Contains(forgotten.Note, "had not reached Sia") {
		t.Errorf("the forget says the flushed memory never reached Sia: %q", forgotten.Note)
	}

	// What the next process to open the vault reads.
	next := vault.OpenWithLedgerForTest(t, home)
	if removal := next.Removal(id); removal.OnNetwork != 1 {
		t.Errorf("the catalog on the device marks the memory %+v, want it removed with its copy still on Sia", removal)
	}
	for _, entry := range next.Entries() {
		if entry.ID == id {
			t.Errorf("the catalog on the device still names the forgotten memory as held")
		}
	}
	restoration, err := next.Restoration()
	if err != nil {
		t.Fatalf("read what the device has restored: %v", err)
	}
	if !restoration.Complete() {
		t.Errorf("the next process counts %+v still to restore, want nothing", restoration)
	}
}

// decodeInto reads a tool result's structured content into out.
func decodeInto[T any](t *testing.T, result *sdk.CallToolResult, out *T) {
	t.Helper()
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("structured content: %v", err)
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		t.Fatalf("structured content did not fit its own schema: %v", err)
	}
}

// A server that reads the vault again while its own calls are in flight, and
// while another process goes on writing to the vault and compacting both of its
// files, fails no call and loses nothing either of them wrote.
//
// The server is in this process, so the race detector watches it read the files
// again and replace what it held under the calls running beside it. The other
// process is a vault of its own over the same directory.
func TestAServerReadingTheVaultAgainLosesNothingAndFailsNoCall(t *testing.T) {
	home := t.TempDir()
	server := vault.OpenWithLedgerForTest(t, home)
	session := serveDevice(t, server)
	other := vault.OpenWithLedgerForTest(t, home)

	done := make(chan struct{})
	var (
		wait sync.WaitGroup
		mu   sync.Mutex
		ours []string
	)
	for caller := range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for n := 0; ; n++ {
				select {
				case <-done:
					return
				default:
				}
				var stored mcp.RememberOut
				if err := callQuietly(session, "remember", mcp.RememberIn{
					Statement: fmt.Sprintf("Caller %d logged buoy %d at the harbour mouth.", caller, n),
					Context:   "Written by the server while another process wrote to the same vault.",
					Type:      "fact",
					Tags:      []string{"buoy"},
				}, &stored); err != nil {
					t.Errorf("caller %d, write %d: %v", caller, n, err)
					return
				}
				mu.Lock()
				ours = append(ours, stored.URI)
				mu.Unlock()
				var found mcp.RecallOut
				if err := callQuietly(session, "recall", mcp.RecallIn{Query: "buoy at the harbour mouth"}, &found); err != nil {
					t.Errorf("caller %d, recall %d: %v", caller, n, err)
					return
				}
				if _, err := session.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: mcp.VaultURI}); err != nil {
					t.Errorf("caller %d, read of %s %d: %v", caller, mcp.VaultURI, n, err)
					return
				}
			}
		}()
	}

	// The other process writes as a second agent or a hydrate would, flushes
	// what is queued, the server's records with its own, and compacts the index
	// and the catalog, which replaces the file each is read from.
	var theirs []record.ID
	for n := range 40 {
		theirs = append(theirs, remember(t, other,
			fmt.Sprintf("Another process logged gauge %d at the river bend.", n), record.TypeFact, "gauge"))
		switch n % 10 {
		case 4:
			vault.FlushToNetworkForTest(t, other)
		case 7:
			if err := other.CompactIndex(); err != nil {
				t.Fatalf("compact the index from the other process: %v", err)
			}
		case 9:
			vault.CompactCatalogForTest(t, other)
		}
	}
	close(done)
	wait.Wait()
	if t.Failed() {
		t.FailNow()
	}
	if len(ours) == 0 {
		t.Fatal("the server wrote nothing while the other process wrote")
	}

	// Everything either of them wrote is counted, searchable and readable
	// through the server.
	want := float64(len(ours) + len(theirs))
	detail := vaultResource(t, session)
	for _, field := range []string{"indexed", "records", "memories"} {
		if detail[field] != want {
			t.Errorf("sennit://vault counts %s as %v, want the %v records both processes wrote", field, detail[field], want)
		}
	}
	addresses := slices.Clone(ours)
	for _, id := range theirs {
		addresses = append(addresses, mcp.URI(record.KindMemory, id))
	}
	for _, uri := range addresses {
		if opened, text := callToolRaw(t, session, "open", mcp.OpenIn{URI: uri}); opened.IsError {
			t.Errorf("open %s through the server: %s", uri, text)
		}
	}

	// And the server's copy of the catalog names what the catalog on the device
	// names, which is what a process opening the vault now reads.
	fresh := vault.OpenWithLedgerForTest(t, home)
	if got, want := fresh.IndexHealth().Indexed, len(addresses); got != want {
		t.Errorf("a process opened now indexes %d records, want %d", got, want)
	}
	catalogued := func(v *vault.Vault) []string {
		var ids []string
		for _, entry := range v.Entries() {
			ids = append(ids, entry.ID.String())
		}
		slices.Sort(ids)
		return ids
	}
	if got, want := catalogued(server), catalogued(fresh); !slices.Equal(got, want) {
		t.Errorf("the server's catalog names %d records and the device's %d", len(got), len(want))
	}
}

// callQuietly invokes a tool and decodes its structured content into out, and
// returns what went wrong instead of failing the test, for a goroutine of its
// own.
func callQuietly[T any](session *sdk.ClientSession, name string, args any, out *T) error {
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err
	}
	if result.IsError {
		for _, content := range result.Content {
			if block, ok := content.(*sdk.TextContent); ok {
				return fmt.Errorf("%s reported an error: %s", name, block.Text)
			}
		}
		return fmt.Errorf("%s reported an error", name)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, out)
}
