package mcp_test

import (
	"fmt"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/recall"
)

// harbourTopic is what the crowding records match on every word, and the
// record asked for on one.
const harbourTopic = "tidepool harbour gauge survey"

// pastAPool is how many crowding records there are: one more than a recall's
// pool holds, so on their own they fill it.
const pastAPool = recall.DefaultCandidates + 1

// inUnscopedPool is how many records of a class are in the pool an unscoped
// recall of the topic draws. A recall scoped to the other class reports them
// as what its scope removed, since that count is taken on the unscoped pool.
func inUnscopedPool(t *testing.T, session *sdk.ClientSession, class string) int {
	t.Helper()
	other := map[string]string{"memory": "session", "session": "memory"}[class]
	var out mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: harbourTopic, Scope: []string{other}}, &out)
	return out.ScopeExcluded
}

// A recall scoped to conversations, and a resume by topic, find the vault's one
// conversation past more memories nearer the topic than a recall's pool holds.
//
// Both used to draw their candidates from every record and apply the scope
// afterwards. The memories filled the pool, the scope removed all of them, and
// the recall answered with nothing while the resume said the vault held no
// conversation to resume. The candidates are now drawn from the conversations,
// and the count of what the scope removed is still taken on the pool an
// unscoped recall draws, so the hint names every memory in it.
func TestAScopedRecallAndAResumeByTopicFindAConversationPastAPoolOfNearerMemories(t *testing.T) {
	session, _ := serve(t)
	for n := range pastAPool {
		storeMemory(t, session, mcp.RememberIn{
			Statement: fmt.Sprintf("The tidepool harbour gauge survey logged reading %d.", n),
			Context:   "Logged at the north pier, one of many readings written to fill a recall's pool.",
			Type:      "fact",
		})
	}
	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Pier inspection",
		Summary:  "Inspected the gauge housing on the pier.",
		Messages: conversation("pier"),
	})

	if held := inUnscopedPool(t, session, "session"); held != 0 {
		t.Fatalf("an unscoped recall's pool holds %d conversation(s), so nothing here crowds one out", held)
	}
	var unscoped mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: harbourTopic, Limit: mcp.MaxLimit}, &unscoped)

	t.Run("a recall scoped to conversations", func(t *testing.T) {
		var scoped mcp.RecallOut
		text := resultText(call(t, session, "recall",
			mcp.RecallIn{Query: harbourTopic, Scope: []string{"session"}}, &scoped))
		if len(scoped.Results) != 1 || scoped.Results[0].URI != saved.URI {
			t.Fatalf("a recall scoped to conversations answered %v, want the one conversation %s",
				answeredWith(scoped), saved.URI)
		}
		if scoped.ScopeExcluded != unscoped.Considered {
			t.Errorf("the scope reports removing %d candidate(s), want the %d an unscoped recall draws, "+
				"all of them memories", scoped.ScopeExcluded, unscoped.Considered)
		}
		removed := fmt.Sprintf("The scope session removed %d candidate(s).", unscoped.Considered)
		if !strings.Contains(scoped.Hint, removed) || !strings.Contains(text, removed) {
			t.Errorf("the answer does not say %q: hint %q", removed, scoped.Hint)
		}
	})

	t.Run("a resume by topic", func(t *testing.T) {
		prompt := resumeWith(t, session, map[string]string{"topic": harbourTopic})
		for _, want := range []string{
			`Resuming "Pier inspection"`,
			fmt.Sprintf("the closest match for %q", harbourTopic),
		} {
			if !strings.Contains(prompt.Description, want) {
				t.Errorf("the resume says %q, want it to say %q", prompt.Description, want)
			}
		}
	})
}

// A recall scoped to memories finds the vault's one memory past more
// conversations nearer the topic than a recall's pool holds: the same crowding
// the other way round.
func TestARecallScopedToMemoriesFindsOnePastAPoolOfNearerConversations(t *testing.T) {
	session, _ := serve(t)
	for n := range pastAPool {
		saveConversation(t, session, mcp.SaveSessionIn{
			Title:    fmt.Sprintf("Tidepool harbour gauge survey %d", n),
			Summary:  "Surveyed the tidepool harbour gauge, one of many conversations written to fill a recall's pool.",
			Messages: conversation(fmt.Sprintf("survey-%d", n)),
		})
	}
	memory := storeMemory(t, session, mcp.RememberIn{
		Statement: "The gauge housing on the pier was inspected.",
		Context:   "Checked from the north pier.",
		Type:      "fact",
	})

	if held := inUnscopedPool(t, session, "memory"); held != 0 {
		t.Fatalf("an unscoped recall's pool holds %d memory, so nothing here crowds it out", held)
	}
	var unscoped mcp.RecallOut
	call(t, session, "recall", mcp.RecallIn{Query: harbourTopic, Limit: mcp.MaxLimit}, &unscoped)

	var scoped mcp.RecallOut
	text := resultText(call(t, session, "recall",
		mcp.RecallIn{Query: harbourTopic, Scope: []string{"memory"}}, &scoped))
	if len(scoped.Results) != 1 || scoped.Results[0].URI != memory {
		t.Fatalf("a recall scoped to memories answered %v, want the one memory %s", answeredWith(scoped), memory)
	}
	if scoped.ScopeExcluded != unscoped.Considered {
		t.Errorf("the scope reports removing %d candidate(s), want the %d an unscoped recall draws, "+
			"all of them conversations", scoped.ScopeExcluded, unscoped.Considered)
	}
	removed := fmt.Sprintf("The scope memory removed %d candidate(s).", unscoped.Considered)
	if !strings.Contains(scoped.Hint, removed) || !strings.Contains(text, removed) {
		t.Errorf("the answer does not say %q: hint %q", removed, scoped.Hint)
	}
}
