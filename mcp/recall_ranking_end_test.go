package mcp_test

import (
	"strings"
	"testing"

	"github.com/steven3002/sennit/mcp"
)

// An empty page after a cursor says that the ranking has ended, and never that
// nothing matched.
//
// A cursor continues one ranking from where the last page stopped, and a page
// comes back empty after it once the earlier pages have returned everything the
// ranking holds. The hints for an empty answer were written for a first page,
// so such a page said the vault did not hold what the earlier pages had just
// returned, or that nothing in the scope matched, or nothing current did. Each
// of those is what the counts for the whole ranking say on a page that holds
// none of it, and each sends a model to tell the user the answer is not there.
func TestAnEmptyPageAfterACursorSaysTheRankingEnded(t *testing.T) {
	session, _ := serve(t)
	gauge := func(letter, supersedes string) string {
		return storeMemory(t, session, mcp.RememberIn{
			Statement:  "Gauge reading " + letter + " at the north pier.",
			Context:    "One of several readings written to give the ranking something to page through.",
			Type:       "fact",
			Tags:       []string{"gauge"},
			Supersedes: supersedes,
		})
	}
	// Five readings, one of which replaces another, so four are current, and a
	// conversation, which a scope of memory leaves out.
	replaced := gauge("a", "")
	gauge("b", replaced)
	gauge("c", "")
	gauge("d", "")
	gauge("e", "")
	saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "North pier gauge survey",
		Summary:  "Read every gauge at the north pier and compared them.",
		Messages: conversation("pier"),
	})

	const query = "north pier gauge readings"
	for name, in := range map[string]mcp.RecallIn{
		// Four current readings and the conversation, with the replaced reading
		// held back.
		"a ranking that held a replaced version back": {Query: query, Limit: 5},
		// The four current readings, with the conversation removed.
		"a ranking scoped to memories": {Query: query, Limit: 2, Scope: []string{"memory"}},
		// All five readings and the conversation, with nothing held back.
		"a ranking that held nothing back": {Query: query, Limit: 3, IncludeSuperseded: true},
	} {
		t.Run(name, func(t *testing.T) {
			var page mcp.RecallOut
			var text string
			for pages := 0; ; pages++ {
				if pages > 6 {
					t.Fatal("the ranking never reached a page without a cursor")
				}
				// Decoded into a fresh value each time, so a field the last page
				// leaves out is not read from the page before it.
				page = mcp.RecallOut{}
				text = resultText(call(t, session, "recall", in, &page))
				if page.NextCursor == "" {
					break
				}
				if len(page.Results) != in.Limit {
					t.Fatalf("a page with a cursor held %d result(s), want %d", len(page.Results), in.Limit)
				}
				in.Cursor = page.NextCursor
			}
			if in.Cursor == "" || len(page.Results) != 0 {
				t.Fatalf("the last page held %d result(s) after cursor %q, want an empty page after a cursor",
					len(page.Results), in.Cursor)
			}

			if !strings.Contains(page.Hint, "ends here") || !strings.Contains(page.Hint, "without a cursor") {
				t.Errorf("the empty page does not say the ranking ended and how to start it again: %q", page.Hint)
			}
			for _, wrong := range []string{"does not hold", "Nothing current matched", "Nothing in scope", "holds nothing"} {
				if strings.Contains(page.Hint, wrong) {
					t.Errorf("the empty page after a cursor says %q: %q", wrong, page.Hint)
				}
			}
			if !strings.Contains(text, page.Hint) {
				t.Errorf("the text a model reads does not carry the hint: %q", text)
			}
		})
	}
}
