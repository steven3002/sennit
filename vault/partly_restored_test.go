package vault_test

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/vault"
)

// partlyRestored is a device that has restored one of the writer's memories,
// searchable by meaning, and knows of the other memory and the conversation
// only from its catalog. A hydrate at catalog depth followed by one at index
// depth that was stopped after its first record leaves a device like this.
//
// It returns the device and the statement of the memory it restored.
func partlyRestored(t *testing.T, memories, network []vault.StoredObject) (*vault.Vault, string) {
	t.Helper()
	device := vault.OpenWithLedgerForTest(t, t.TempDir())
	vault.RestoreForTest(t, device, network, vault.HydrateCatalog)
	vault.RestoreForTest(t, device, memories[:1], vault.HydrateIndex)

	page, err := device.Browse(vault.BrowseRequest{})
	if err != nil {
		t.Fatalf("browse the partly restored device: %v", err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("the partly restored device lists %d record(s), want the 1 memory it restored", len(page.Rows))
	}
	return device, page.Rows[0].Label
}

// otherGauge is the writer's memory that is not the one given.
func otherGauge(statement string) string {
	if statement == harbourGauge {
		return riverGauge
	}
	return harbourGauge
}

// A resume by topic that finds no conversation gives the answer a resume that
// names no conversation gives, on every kind of device.
//
// It used to say the vault held no conversation to resume, on a device that
// had never restored the vault and on one that had restored only its catalog,
// though the network held a conversation. A resume that names nothing already
// says what this device has restored, and a topic is searched for only among
// the conversations this device holds, so on a device holding none the two have
// nothing to tell them apart and give one answer. A device that holds the
// conversation still resumes it by its topic.
func TestAResumeByTopicThatFindsNoConversationAnswersAsAResumeThatNamesNone(t *testing.T) {
	memories, network := writerNetwork(t)

	catalogued := vault.OpenWithLedgerForTest(t, t.TempDir())
	vault.RestoreForTest(t, catalogued, network, vault.HydrateCatalog)
	partly, _ := partlyRestored(t, memories, network)
	// A device restored while the vault held only its memories knows of no
	// conversation, so as far as it can tell the vault holds none.
	early := vault.OpenWithLedgerForTest(t, t.TempDir())
	vault.RestoreForTest(t, early, memories, vault.HydrateIndex)

	// The same topic reaches a resume however the host fills the arguments:
	// one word to an argument, in the order they are declared, or by name.
	topics := map[string]map[string]string{
		"resume tidepool station survey": {"session": "tidepool", "topic": "station", "turns": "survey"},
		"resume with the topic by name":  {"topic": "tidepool station survey"},
	}

	for _, tc := range []struct {
		name   string
		device *vault.Vault
	}{
		{"a device that never hydrated", vault.OpenWithLedgerForTest(t, t.TempDir())},
		{"a device hydrated at catalog depth", catalogued},
		{"a partly restored device", partly},
		{"a device restored before the conversation was saved", early},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := serveDevice(t, tc.device)
			want := resumeRefusal(t, session, nil)
			if want == "" {
				t.Fatal("a resume that names nothing went ahead on a device holding no conversation")
			}
			if recent := resumeRefusal(t, session, map[string]string{"session": mcp.ResumeRecent}); recent != want {
				t.Fatalf("the two resumes that name nothing disagree: %q and %q", want, recent)
			}
			for way, arguments := range topics {
				if got := resumeRefusal(t, session, arguments); got != want {
					t.Errorf("%s answered %q, want what a resume that names nothing answers: %q", way, got, want)
				}
			}
		})
	}

	t.Run("a device that restored the vault", func(t *testing.T) {
		restored := vault.OpenWithLedgerForTest(t, t.TempDir())
		vault.RestoreForTest(t, restored, network, vault.HydrateIndex)
		session := serveDevice(t, restored)
		for way, arguments := range topics {
			prompt, err := session.GetPrompt(context.Background(), &sdk.GetPromptParams{
				Name: mcp.ResumePrompt, Arguments: arguments,
			})
			if err != nil {
				t.Fatalf("%s on a device holding the conversation: %v", way, err)
			}
			// A rebuilt head takes its title from the first thing the user
			// said, so the conversation is known here by how it was chosen.
			if !strings.Contains(prompt.Description, `the closest match for "tidepool station survey"`) {
				t.Errorf("%s resumed %q, want the conversation the topic found", way, prompt.Description)
			}
		}
	})
}

// A recall says what this device has not restored, with its results as well as
// without them, on every kind of device a hydrate leaves.
//
// A search reads only what this device holds. On a device that has restored
// part of the vault it still finds the closest record it holds, and that answer
// looked the same as a whole vault's would: asked for the memory it had not
// restored, it returned the one it had, and nothing said the other was missing.
// Now every answer from a device that knows of records it has not restored
// names them and the command that restores them, and a device that holds
// everything it knows of answers as it did.
func TestARecallSaysWhatThisDeviceHasNotRestored(t *testing.T) {
	memories, network := writerNetwork(t)

	catalogued := vault.OpenWithLedgerForTest(t, t.TempDir())
	vault.RestoreForTest(t, catalogued, network, vault.HydrateCatalog)
	partly, held := partlyRestored(t, memories, network)
	restored := vault.OpenWithLedgerForTest(t, t.TempDir())
	vault.RestoreForTest(t, restored, network, vault.HydrateIndex)

	const command = "`sennit hydrate --depth index`"
	for _, tc := range []struct {
		name   string
		device *vault.Vault
		// query asks for the writer's memory this device does not hold, or for
		// the river gauge where it holds both or neither.
		query string
		// found is the statement the device answers with first, or nothing.
		found string
		// missing is what the hint says the device has not restored, and
		// nothing for a device that holds everything it knows of.
		missing []string
	}{
		{"a device that never hydrated", vault.OpenWithLedgerForTest(t, t.TempDir()), riverGauge, "",
			[]string{"has not restored any record of this vault"}},
		{"a device hydrated at catalog depth", catalogued, riverGauge, "",
			[]string{"2 memory record(s) and at least one stored conversation"}},
		{"a partly restored device", partly, otherGauge(held), held,
			[]string{"1 memory record(s) and at least one stored conversation"}},
		{"a device that restored the vault", restored, riverGauge, riverGauge, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := serveDevice(t, tc.device)
			var out mcp.RecallOut
			text := callTool(t, session, "recall", mcp.RecallIn{Query: tc.query}, &out)

			if tc.found == "" && len(out.Results) != 0 {
				t.Fatalf("recall found %d record(s) on a device holding none", len(out.Results))
			}
			if tc.found != "" && (len(out.Results) == 0 || out.Results[0].Title != tc.found) {
				t.Fatalf("recall answered %+v, want %q first", out.Results, tc.found)
			}
			if tc.missing == nil {
				if out.Hint != "" {
					t.Errorf("a device holding everything it knows of answered with the hint %q", out.Hint)
				}
				return
			}
			for _, name := range append([]string{"has not restored", command}, tc.missing...) {
				if !strings.Contains(out.Hint, name) {
					t.Errorf("the hint does not name %q: %q", name, out.Hint)
				}
			}
			if strings.Contains(out.Hint, "holds nothing") {
				t.Errorf("the hint calls the vault empty: %q", out.Hint)
			}
			if !strings.Contains(text, out.Hint) {
				t.Errorf("the text a model reads does not carry the hint: %q", text)
			}
		})
	}
}

// On a partly restored device, what a recall says it left out follows the
// scope, and an empty page after a cursor says it as well.
//
// A record the scope excludes would not have been returned had this device
// restored it, so the answer is not missing it: a search scoped to memories is
// short of the memories this device has not restored and not of the
// conversation, and one scoped to conversations the other way round.
func TestWhatAPartlyRestoredRecallLeavesOutFollowsTheScopeAndTheCursor(t *testing.T) {
	memories, network := writerNetwork(t)
	device, held := partlyRestored(t, memories, network)
	session := serveDevice(t, device)
	query := otherGauge(held)

	const (
		memory       = "1 memory record(s)"
		conversation = "at least one stored conversation"
	)
	// says checks that a hint names want and the command that restores it, and
	// does not say wrong.
	says := func(what, hint, want, wrong string) {
		t.Helper()
		if !strings.Contains(hint, want) || !strings.Contains(hint, "`sennit hydrate --depth index`") {
			t.Errorf("%s: the hint does not name %q and the command that restores it: %q", what, want, hint)
		}
		if strings.Contains(hint, wrong) {
			t.Errorf("%s: the hint says %q: %q", what, wrong, hint)
		}
	}

	var memoriesOnly mcp.RecallOut
	callTool(t, session, "recall", mcp.RecallIn{Query: query, Scope: []string{"memory"}}, &memoriesOnly)
	if len(memoriesOnly.Results) != 1 || memoriesOnly.Results[0].Title != held {
		t.Fatalf("a recall scoped to memories answered %+v, want the memory this device holds", memoriesOnly.Results)
	}
	says("scoped to memories", memoriesOnly.Hint, memory, "conversation")

	var conversationsOnly mcp.RecallOut
	callTool(t, session, "recall", mcp.RecallIn{Query: query, Scope: []string{"session"}}, &conversationsOnly)
	if len(conversationsOnly.Results) != 0 {
		t.Fatalf("a recall scoped to conversations found %d on a device holding none", len(conversationsOnly.Results))
	}
	if !strings.Contains(conversationsOnly.Hint, "Nothing in scope session matched") {
		t.Errorf("an empty scoped answer no longer says what the scope removed: %q", conversationsOnly.Hint)
	}
	says("scoped to conversations", conversationsOnly.Hint, conversation, "memory record")

	var first, past mcp.RecallOut
	callTool(t, session, "recall", mcp.RecallIn{Query: query, Limit: 1}, &first)
	if len(first.Results) != 1 || first.NextCursor == "" {
		t.Fatalf("a recall with a limit of 1 answered %d record(s) and cursor %q", len(first.Results), first.NextCursor)
	}
	callTool(t, session, "recall", mcp.RecallIn{Query: query, Limit: 1, Cursor: first.NextCursor}, &past)
	if len(past.Results) != 0 {
		t.Fatalf("the page after the only record this device holds listed %d", len(past.Results))
	}
	if !strings.Contains(past.Hint, "ends here") {
		t.Errorf("the empty page after the cursor does not say the ranking ended: %q", past.Hint)
	}
	says("past the cursor", past.Hint, memory+" and "+conversation, "does not hold")
}
