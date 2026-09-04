// Command agent drives a Sennit MCP server the way a host would.
//
// It exists for the two-machine demo, where something has to play the part of
// the agent on machine A: write a memory, save a conversation, and leave. A host
// would do this through its own client; this is the same protocol traffic
// without a host's user interface in the way.
//
// It is deliberately the Go SDK. The second half of the demo reads the same
// vault with the TypeScript SDK, and the claim being demonstrated is that the
// two see the same memory, which is only worth showing if they are genuinely
// different implementations.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	binary := flag.String("server", "", "path to the sennit-mcp binary (required)")
	statement := flag.String("remember", "", "a memory to store")
	memoryContext := flag.String("context", "", "what makes the memory resolvable once it is out of this conversation")
	tags := flag.String("tags", "", "comma-separated tags")
	title := flag.String("session", "", "title of a conversation to save")
	summary := flag.String("summary", "", "the conversation's summary, which is what a search over sessions reads")
	forgetAll := flag.Bool("forget-all", false,
		"remove every record this vault holds, so a demo run does not leave storage billing")
	flag.Parse()

	if *binary == "" {
		fmt.Fprintln(os.Stderr, "agent: -server is required")
		os.Exit(2)
	}
	if err := run(context.Background(), *binary, options{
		statement: *statement, context: *memoryContext, tags: *tags,
		title: *title, summary: *summary, forgetAll: *forgetAll,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	statement, context, tags string
	title, summary           string
	forgetAll                bool
}

func run(ctx context.Context, binary string, opts options) error {
	client := sdk.NewClient(&sdk.Implementation{Name: "sennit-demo-agent", Version: "1.0.0"}, nil)

	// The server inherits this process's environment, which is where both
	// secrets live. Neither is ever an argument: a flag lands in the process
	// table and in shell history, and this command runs on camera.
	session, err := client.Connect(ctx, &sdk.CommandTransport{Command: exec.Command(binary)}, nil)
	if err != nil {
		return fmt.Errorf("connect to the server: %w", err)
	}
	defer session.Close()

	initialized := session.InitializeResult()
	fmt.Printf("  connected to %s over stdio, protocol %s\n",
		initialized.ServerInfo.Name, initialized.ProtocolVersion)

	if opts.statement != "" {
		result, err := call(ctx, session, "remember", map[string]any{
			"statement": opts.statement,
			"context":   opts.context,
			"type":      "fact",
			"tags":      splitTags(opts.tags),
		})
		if err != nil {
			return err
		}
		fmt.Printf("  remembered: %s\n", firstLine(result))
	}

	if opts.title != "" {
		result, err := call(ctx, session, "save_session", map[string]any{
			"title":    opts.title,
			"summary":  opts.summary,
			"tags":     splitTags(opts.tags),
			"messages": demoTranscript(),
			"durable":  true,
		})
		if err != nil {
			return err
		}
		fmt.Printf("  saved the conversation: %s\n", firstLine(result))
	}

	if opts.forgetAll {
		return forgetEverything(ctx, session)
	}
	return nil
}

// forgetEverything removes what a demo run stored.
//
// It exists so the demo can be run repeatedly without filling the account: every
// flush mints a whole slab whatever it holds, so a demo that left its records
// behind would cost forty mebibytes a run and the tier would be gone in a
// fortnight of rehearsals.
func forgetEverything(ctx context.Context, session *sdk.ClientSession) error {
	listed, err := call(ctx, session, "browse", map[string]any{"limit": 200})
	if err != nil {
		return err
	}
	addresses := vaultAddresses(listed)
	for _, address := range addresses {
		if _, err := call(ctx, session, "forget", map[string]any{
			"uri": address, "confirm": true,
		}); err != nil {
			return err
		}
	}
	fmt.Printf("  forgot %d record(s)\n", len(addresses))
	return nil
}

// vaultAddresses picks the record addresses out of a listing's text.
//
// The listing is rendered for a reader and the addresses are in it verbatim,
// which is the point of returning them: an address is what every other tool
// takes, so a caller never has to construct one.
func vaultAddresses(result *sdk.CallToolResult) []string {
	var out []string
	seen := make(map[string]bool)
	for _, content := range result.Content {
		text, ok := content.(*sdk.TextContent)
		if !ok {
			continue
		}
		for field := range strings.FieldsSeq(text.Text) {
			field = strings.TrimRight(field, ".,)")
			if !strings.HasPrefix(field, "sennit://") || seen[field] {
				continue
			}
			seen[field] = true
			out = append(out, field)
		}
	}
	return out
}

func call(ctx context.Context, session *sdk.ClientSession, name string, args map[string]any) (*sdk.CallToolResult, error) {
	start := time.Now()
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", name, err)
	}
	if result.IsError {
		return nil, fmt.Errorf("%s: %s", name, firstLine(result))
	}
	_ = start
	return result, nil
}

func firstLine(result *sdk.CallToolResult) string {
	for _, content := range result.Content {
		text, ok := content.(*sdk.TextContent)
		if !ok {
			continue
		}
		for line := range strings.SplitSeq(text.Text, "\n") {
			if strings.TrimSpace(line) != "" {
				return strings.TrimSpace(line)
			}
		}
	}
	return "(no text)"
}

func splitTags(list string) []string {
	var out []string
	for tag := range strings.SplitSeq(list, ",") {
		if trimmed := strings.TrimSpace(tag); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// demoTranscript is a short conversation with the two things a flat chat log
// cannot carry: a tool call correlated to its result by id, and a provider field
// this build has never heard of. Both have to survive the journey for the demo
// to be about a session rather than about a string.
func demoTranscript() []map[string]any {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	return []map[string]any{
		{
			"id": "demo-1", "role": "user", "created": now,
			"parts": []map[string]any{{
				"type": "text",
				"text": "Before I switch machines: what did we decide about the flush cadence?",
			}},
		},
		{
			"id": "demo-2", "role": "assistant", "created": now, "parent": "demo-1",
			"parts": []map[string]any{
				{"type": "reasoning", "text": "Check the decision log before answering."},
				{"type": "text", "text": "Looking it up."},
				{
					"type": "toolCall", "callId": "call-1", "name": "recall",
					"input": json.RawMessage(`{"query":"flush cadence"}`),
				},
			},
			"meta": map[string]any{"model": "claude-opus-5", "stopReason": "tool_use"},
			"ext":  map[string]any{"requestId": "req-demo-2"},
		},
		{
			"id": "demo-3", "role": "tool", "parent": "demo-2",
			"parts": []map[string]any{{
				"type": "toolResult", "callId": "call-1",
				"content": []map[string]any{{
					"type": "text",
					"text": "Five minutes idle, one hour cap. The cap bounds the durability window, not cost.",
				}},
			}},
		},
		{
			"id": "demo-4", "role": "assistant", "created": now, "parent": "demo-3",
			"parts": []map[string]any{{
				"type": "text",
				"text": "Five minutes idle with a one-hour cap, and the cap is there to bound how long " +
					"a turn exists only on your device, not to save money. Repack is what saves money.",
			}},
			"meta": map[string]any{"model": "claude-opus-5"},
		},
	}
}
