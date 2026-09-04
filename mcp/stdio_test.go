package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/mcp"
)

// The two pass marks that cannot be answered in one process.
//
// A protocol version is negotiated on the wire, so the only honest way to test
// the downgrade is to write a legacy handshake into a real process's stdin and
// read what comes back. And "two clients against one vault" is a statement about
// two operating-system processes sharing a directory: each client launches its
// own server, which is why the device store had to be multi-process safe from
// the first commit rather than as a later hardening pass.
//
// Both are served by re-executing this test binary as a server, which is a real
// process over the real stdio transport and adds nothing to the product.

// serverModeEnv makes this test binary run as a server instead of as tests.
const serverModeEnv = "SENNIT_TEST_SERVE_HOME"

func TestMain(m *testing.M) {
	if home := os.Getenv(serverModeEnv); home != "" {
		os.Exit(serveOverStdio(home))
	}
	os.Exit(m.Run())
}

// serveOverStdio runs the MCP server over this process's stdin and stdout and
// reports the status to exit with.
//
// Nothing but protocol traffic reaches stdout, which is the contract stdio
// imposes and the one a stray print breaks silently.
func serveOverStdio(home string) int {
	v, err := vaultForServing(home)
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

// A client is one server subprocess spoken to in raw JSON-RPC.
//
// Raw rather than through the SDK client on purpose: the SDK negotiates the
// newest revision it knows, and the whole question here is what happens when a
// client that only speaks an older one connects, which is every client actually
// shipping.
type client struct {
	t    *testing.T
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	next int
}

func spawn(t *testing.T, home string) *client {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestMain")
	cmd.Env = append(os.Environ(), serverModeEnv+"="+home)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	c := &client{t: t, cmd: cmd, in: stdin, out: bufio.NewReaderSize(stdout, 1<<20)}
	t.Cleanup(func() {
		stdin.Close()
		cmd.Wait()
	})
	return c
}

// rpc sends one request and returns its result, failing on a protocol error.
func (c *client) rpc(method string, params any) json.RawMessage {
	c.t.Helper()
	result, rpcErr := c.try(method, params)
	if rpcErr != nil {
		c.t.Fatalf("%s: %s", method, rpcErr)
	}
	return result
}

func (c *client) try(method string, params any) (json.RawMessage, json.RawMessage) {
	c.t.Helper()
	c.next++
	request := map[string]any{"jsonrpc": "2.0", "id": c.next, "method": method}
	if params != nil {
		request["params"] = params
	}
	c.send(request)

	for {
		var response struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		line := c.readLine()
		if err := json.Unmarshal(line, &response); err != nil {
			c.t.Fatalf("%s: server wrote a line that is not JSON-RPC: %.200q", method, line)
		}
		// A notification the server sent on its own is skipped rather than
		// mistaken for the answer.
		if response.ID == nil || *response.ID != c.next {
			continue
		}
		return response.Result, response.Error
	}
}

func (c *client) notify(method string, params any) {
	c.t.Helper()
	request := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		request["params"] = params
	}
	c.send(request)
}

func (c *client) send(request map[string]any) {
	c.t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		c.t.Fatalf("encode request: %v", err)
	}
	if _, err := c.in.Write(append(encoded, '\n')); err != nil {
		c.t.Fatalf("write request: %v", err)
	}
}

func (c *client) readLine() []byte {
	c.t.Helper()
	line, err := c.out.ReadBytes('\n')
	if err != nil {
		c.t.Fatalf("read from server: %v (partial line %.200q)", err, line)
	}
	return line
}

// legacyVersion is what every client actually tested speaks, a revision behind
// the one this server is built against.
const legacyVersion = "2025-11-25"

// handshake performs the legacy initialize exchange and returns the result.
func (c *client) handshake(version string) map[string]any {
	c.t.Helper()
	raw := c.rpc("initialize", map[string]any{
		"protocolVersion": version,
		"clientInfo":      map[string]any{"name": "legacy-probe", "version": "1"},
		"capabilities":    map[string]any{},
	})
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		c.t.Fatalf("initialize result: %v", err)
	}
	c.notify("notifications/initialized", nil)
	return result
}

// callTool invokes a tool and returns its structured content.
func (c *client) callTool(name string, args any) map[string]any {
	c.t.Helper()
	raw := c.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	var result struct {
		StructuredContent map[string]any `json:"structuredContent"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		c.t.Fatalf("%s result: %v", name, err)
	}
	if result.IsError {
		var text string
		if len(result.Content) > 0 {
			text = result.Content[0].Text
		}
		c.t.Fatalf("%s reported an error: %s", name, text)
	}
	if result.StructuredContent == nil {
		c.t.Fatalf("%s returned no structuredContent", name)
	}
	if len(result.Content) == 0 {
		c.t.Fatalf("%s returned no mirrored content block", name)
	}
	return result.StructuredContent
}

// Pass mark 5. The server works when a client negotiates it down to
// 2025-11-25, and nothing on this surface depends on a 2026-07-28 mechanism.
//
// This is not a hypothetical. The published revision is 2026-07-28 and every
// real client measured speaks 2025-11-25, the TypeScript SDK, the official
// Inspector, and the production chat host all sit a revision behind, and Claude
// Desktop, Cursor and VS Code are all built on that SDK. A surface that needed
// the newer revision would be a demo that does not run.
func TestTheServerWorksWhenNegotiatedDownToTheRevisionRealClientsSpeak(t *testing.T) {
	server := spawn(t, t.TempDir())
	result := server.handshake(legacyVersion)

	if got := result["protocolVersion"]; got != legacyVersion {
		t.Fatalf("the server answered a %s client at %v", legacyVersion, got)
	}
	instructions, _ := result["instructions"].(string)
	if instructions != mcp.Instructions {
		t.Error("a legacy client did not receive the instructions, which is the only thing " +
			"telling the model the address space exists")
	}
	capabilities, _ := result["capabilities"].(map[string]any)
	if _, advertised := capabilities["logging"]; advertised {
		t.Error("the server advertises logging to a legacy client")
	}

	// The whole surface, over the legacy revision.
	server.rpc("tools/list", map[string]any{})
	server.rpc("resources/list", map[string]any{})
	server.rpc("resources/templates/list", map[string]any{})
	server.rpc("prompts/list", map[string]any{})

	stored := server.callTool("remember", map[string]any{
		"statement": "The rollup runs hourly, at ten past the hour.",
		"context":   "Settled while working out why the dashboard lagged the sensors.",
		"type":      "fact",
		"tags":      []string{"rollup", "schedule"},
	})
	uri, _ := stored["uri"].(string)
	if uri == "" {
		t.Fatal("remember returned no address over the legacy revision")
	}

	recalled := server.callTool("recall", map[string]any{
		"query": "when does the rollup run?",
		"tags":  []string{"rollup"},
	})
	results, _ := recalled["results"].([]any)
	if len(results) == 0 {
		t.Fatal("recall found nothing over the legacy revision")
	}

	// Reading, through both doors, on the legacy revision.
	server.rpc("resources/read", map[string]any{"uri": mcp.GuideURI})
	server.rpc("resources/read", map[string]any{"uri": uri})
	opened := server.callTool("open", map[string]any{"uri": uri})
	if content, _ := opened["content"].(string); !strings.Contains(content, "ten past the hour") {
		t.Fatalf("open returned something else over the legacy revision: %.120q", content)
	}

	// And the prompt, which is the demo.
	var prompts struct {
		Prompts []struct {
			Name string `json:"name"`
		} `json:"prompts"`
	}
	if err := json.Unmarshal(server.rpc("prompts/list", map[string]any{}), &prompts); err != nil {
		t.Fatalf("prompts/list: %v", err)
	}
	if len(prompts.Prompts) != 1 || prompts.Prompts[0].Name != mcp.ResumePrompt {
		t.Fatalf("a legacy client sees prompts %+v, want just %q", prompts.Prompts, mcp.ResumePrompt)
	}
}

// currentVersion is the published revision this server is built against.
const currentVersion = "2026-07-28"

// The server serves the current revision too, but only on the path that
// revision defines, and an `initialize` handshake can never reach it.
//
// ⚠ This is sharper than "real clients lag", and it was found by probing rather
// than assumed. `initialize` is deprecated in 2026-07-28, so go-sdk v1.7.0 caps
// every `initialize` handshake at 2025-11-25 **whatever version the client
// claims**: a client that sends `initialize` with protocolVersion 2026-07-28 is
// answered at 2025-11-25. The current revision is reachable only through
// `server/discover` with per-request `_meta`, which is the stateless path.
//
// The consequence for this project is the useful part. Every shipping client
// uses `initialize`, so they are on 2025-11-25 **by construction and not merely
// by lag**, pass mark 5 is not a happy accident about version strings, it is
// the only path those clients have.
func TestTheCurrentRevisionIsReachableOnlyOnItsOwnStatelessPath(t *testing.T) {
	server := spawn(t, t.TempDir())

	// The legacy handshake is capped, even when it asks for the current
	// revision by name.
	if got := server.handshake(currentVersion)["protocolVersion"]; got != legacyVersion {
		t.Fatalf("an initialize handshake claiming %s was answered at %v, want the %s cap",
			currentVersion, got, legacyVersion)
	}

	// The stateless path answers on the current revision, with no initialize at
	// all, and carries the same instructions.
	var discovered struct {
		SupportedVersions []string `json:"supportedVersions"`
		Instructions      string   `json:"instructions"`
		Capabilities      struct {
			Logging any `json:"logging"`
		} `json:"capabilities"`
	}
	raw := server.rpc("server/discover", map[string]any{
		"_meta": map[string]any{
			sdk.MetaKeyProtocolVersion:    currentVersion,
			sdk.MetaKeyClientInfo:         map[string]any{"name": "stateless-probe", "version": "1"},
			sdk.MetaKeyClientCapabilities: map[string]any{},
		},
	})
	if err := json.Unmarshal(raw, &discovered); err != nil {
		t.Fatalf("server/discover: %v", err)
	}
	if len(discovered.SupportedVersions) == 0 || discovered.SupportedVersions[0] != currentVersion {
		t.Fatalf("server/discover advertises %v, want %s first",
			discovered.SupportedVersions, currentVersion)
	}
	if discovered.Instructions != mcp.Instructions {
		t.Error("the stateless path does not carry the instructions")
	}
	if discovered.Capabilities.Logging != nil {
		t.Error("the stateless path advertises logging")
	}
}

// Pass mark 7. Two MCP clients run concurrently against one vault without
// corruption.
//
// Two operating-system processes, each with its own server and its own vault
// handle over one directory, writing at the same time. This is the ordinary case
// rather than an exotic one: every host launches its own copy of a stdio server,
// so a user with two editors open has two of these running by lunchtime.
func TestTwoClientsRunConcurrentlyAgainstOneVault(t *testing.T) {
	home := t.TempDir()
	first, second := spawn(t, home), spawn(t, home)
	first.handshake(legacyVersion)
	second.handshake(legacyVersion)

	const each = 12
	written := make([][]string, 2)
	var wait sync.WaitGroup
	for i, server := range []*client{first, second} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for n := range each {
				stored := server.callTool("remember", map[string]any{
					"statement": fmt.Sprintf("Client %d observed reading %d at the north pier gauge.", i, n),
					"context": fmt.Sprintf("Recorded by client %d during the concurrent write test, "+
						"to prove two servers can share one vault.", i),
					"type": "fact",
					"tags": []string{"gauge", fmt.Sprintf("client-%d", i)},
				})
				uri, _ := stored["uri"].(string)
				if uri == "" {
					t.Errorf("client %d write %d returned no address", i, n)
					return
				}
				written[i] = append(written[i], uri)
			}
		}()
	}
	wait.Wait()
	if t.Failed() {
		t.Fatal("a concurrent write failed; the checks below would only add noise")
	}

	// Every record from both clients is present, exactly once, and readable
	// through either server.
	all := append(append([]string{}, written[0]...), written[1]...)
	if len(all) != 2*each {
		t.Fatalf("%d records were written, want %d", len(all), 2*each)
	}
	seen := map[string]bool{}
	for _, uri := range all {
		if seen[uri] {
			t.Fatalf("two writes were given the same address %s", uri)
		}
		seen[uri] = true
	}

	for i, server := range []*client{first, second} {
		page := server.callTool("browse", map[string]any{"tags": []string{"gauge"}, "limit": 200})
		rows, _ := page["rows"].([]any)
		if len(rows) != 2*each {
			t.Errorf("client %d sees %d of the %d records both clients wrote", i, len(rows), 2*each)
		}
		found := map[string]bool{}
		for _, row := range rows {
			if entry, ok := row.(map[string]any); ok {
				uri, _ := entry["uri"].(string)
				found[uri] = true
			}
		}
		for _, uri := range all {
			if !found[uri] {
				t.Errorf("client %d cannot see %s, written by the other client", i, uri)
			}
		}
		// And each record still opens, which is the difference between a
		// listing that survived and data that did.
		for _, uri := range all {
			opened := server.callTool("open", map[string]any{"uri": uri})
			if content, _ := opened["content"].(string); !strings.Contains(content, "north pier gauge") {
				t.Errorf("client %d read %s back as %.80q", i, uri, content)
				break
			}
		}
	}
}

// Two clients appending to one conversation is the case that used to lose turns
// silently, and the loser is now told rather than overwriting the winner.
func TestTwoClientsAppendingToOneConversationNeverLoseTurns(t *testing.T) {
	home := t.TempDir()
	first, second := spawn(t, home), spawn(t, home)
	first.handshake(legacyVersion)
	second.handshake(legacyVersion)

	saved := first.callTool("save_session", map[string]any{
		"title":    "Concurrent append",
		"summary":  "Two clients appending to one stored conversation at the same time.",
		"messages": messagesJSON("a", 2),
	})
	uri, _ := saved["uri"].(string)
	if uri == "" {
		t.Fatal("save_session returned no address")
	}

	// Both append from the same starting version. One wins; the other is told
	// its head moved, and nothing is silently erased.
	type outcome struct {
		conflict bool
		err      string
	}
	results := make([]outcome, 2)
	var wait sync.WaitGroup
	for i, server := range []*client{first, second} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			raw := server.rpc("tools/call", map[string]any{
				"name": "save_session",
				"arguments": map[string]any{
					"session":  uri,
					"messages": messagesJSON(fmt.Sprintf("append-%d", i), 2),
				},
			})
			var result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Errorf("append %d: %v", i, err)
				return
			}
			if result.IsError && len(result.Content) > 0 {
				results[i] = outcome{conflict: true, err: result.Content[0].Text}
			}
		}()
	}
	wait.Wait()

	// Whatever the interleaving, the conversation is still loadable and still
	// holds the turns of whichever writer won.
	opened := first.callTool("open", map[string]any{"uri": uri})
	detail, _ := opened["detail"].(map[string]any)
	messages, _ := detail["messages"].(float64)
	if messages < 4 {
		t.Fatalf("the conversation holds %v turns after two appends, want at least 4", messages)
	}
	for i, result := range results {
		if result.conflict && !strings.Contains(result.err, "another") &&
			!strings.Contains(result.err, "Another") {
			t.Errorf("append %d failed for a reason other than the race: %s", i, result.err)
		}
	}
}

// messagesJSON builds n turns as the tool's own input shape.
func messagesJSON(prefix string, n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := range n {
		out = append(out, map[string]any{
			"id":      fmt.Sprintf("%s-%d", prefix, i),
			"role":    "user",
			"created": time.Now().UTC().Format(time.RFC3339),
			"parts": []map[string]any{
				{"type": "text", "text": fmt.Sprintf("Turn %d from %s.", i, prefix)},
			},
		})
	}
	return out
}
