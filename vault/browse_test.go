package vault_test

import (
	"context"
	"testing"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// remember stores one memory and fails the test if it will not store.
func remember(t *testing.T, v *vault.Vault, statement string, kind record.Type, tags ...string) record.ID {
	t.Helper()
	stored, err := v.Remember(context.Background(), vault.RememberRequest{
		Statement: statement,
		Context:   "Written by the browse tests to give the listing something to order.",
		Type:      kind,
		Tags:      tags,
	})
	if err != nil {
		t.Fatalf("remember %q: %v", statement, err)
	}
	return stored.ID
}

func TestBrowseListsNewestFirstAndPagesWithAStableCursor(t *testing.T) {
	v := offlineVault(t)

	const total = 7
	written := make([]record.ID, 0, total)
	for i := range total {
		written = append(written, remember(t, v,
			"Statement number "+string(rune('a'+i))+" about the tide registry.", record.TypeFact, "tides"))
	}

	var seen []record.ID
	var cursor local.Cursor
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("the listing never reached its last page")
		}
		page, err := v.Browse(vault.BrowseRequest{Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatalf("browse: %v", err)
		}
		for _, row := range page.Rows {
			seen = append(seen, row.ID)
		}
		if page.NextCursor == "" {
			if len(page.Rows) == 0 && pages > 0 {
				t.Fatal("the last page was empty rather than short")
			}
			break
		}
		cursor = page.NextCursor
	}

	if len(seen) != total {
		t.Fatalf("paging returned %d records, the vault holds %d", len(seen), total)
	}
	unique := make(map[record.ID]bool, len(seen))
	for _, id := range seen {
		if unique[id] {
			t.Fatalf("record %s was returned on two pages", id)
		}
		unique[id] = true
	}
	for _, id := range written {
		if !unique[id] {
			t.Fatalf("record %s was never listed", id)
		}
	}
}

// A browse filter excludes, which is the opposite of what a recall filter does,
// and the two must not be confused for each other.
func TestBrowseFiltersExcludeWhereRecallFiltersOnlyPrefer(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	remember(t, v, "The rollup runs hourly at ten past.", record.TypeFact, "rollup", "schedule")
	remember(t, v, "The team prefers short commit subjects.", record.TypePreference, "style")
	remember(t, v, "Latency fell because the cache stopped missing.", record.TypeInsight, "latency")

	page, err := v.Browse(vault.BrowseRequest{Types: []record.Type{record.TypePreference}})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Type != record.TypePreference {
		t.Fatalf("a type filter returned %d row(s), want the one preference", len(page.Rows))
	}

	tagged, err := v.Browse(vault.BrowseRequest{Tags: []string{"rollup"}})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(tagged.Rows) != 1 {
		t.Fatalf("a tag filter returned %d row(s), want 1", len(tagged.Rows))
	}

	// A tag no record carries empties a browse and must not empty a recall.
	missing, err := v.Browse(vault.BrowseRequest{Tags: []string{"nothing-carries-this"}})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(missing.Rows) != 0 {
		t.Fatalf("a tag nothing carries returned %d row(s)", len(missing.Rows))
	}
	recalled, err := v.Recall(ctx, recall.Request{
		Query:  "what runs hourly",
		Filter: recall.Filter{Tags: []string{"nothing-carries-this"}},
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(recalled.Hits) == 0 {
		t.Fatal("the same wrong tag emptied a recall, which is the failure soft filtering exists to prevent")
	}
}

func TestBrowseNamesWhatItLists(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	const statement = "The registry lists forty-one hourly stations."
	remember(t, v, statement, record.TypeFact, "stations")
	saved, err := v.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "Tide station survey",
		Summary:  "Counted the stations that report hourly.",
		Tags:     []string{"stations"},
		Messages: conversation("browse"),
	})
	if err != nil {
		t.Fatalf("save session: %v", err)
	}

	page, err := v.Browse(vault.BrowseRequest{})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	labels := map[record.Kind]string{}
	for _, row := range page.Rows {
		labels[row.Kind] = row.Label
	}
	if labels[record.KindMemory] != statement {
		t.Fatalf("a memory is labelled %q, want its statement", labels[record.KindMemory])
	}
	if labels[record.KindSession] != "Tide station survey" {
		t.Fatalf("a session is labelled %q, want its title", labels[record.KindSession])
	}
	// A chunk is storage, not something to browse: it holds the transcript the
	// session names and has no standing of its own in the address space.
	for _, row := range page.Rows {
		if row.Kind == record.KindChunk {
			t.Fatalf("browse listed a transcript chunk (%s) as though it were a record", row.ID)
		}
	}
	_ = saved
}

// A record replaced by a later one is held back by default and reachable on
// request, exactly as recall holds it back.
func TestBrowseHidesSupersededVersionsUnlessAsked(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	old := remember(t, v, "The rollup runs every four hours.", record.TypeFact, "rollup")
	if _, err := v.Remember(ctx, vault.RememberRequest{
		Statement:  "The rollup runs hourly, at ten past the hour.",
		Context:    "Corrected after the schedule changed in August.",
		Type:       record.TypeFact,
		Tags:       []string{"rollup"},
		Supersedes: &old,
	}); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	current, err := v.Browse(vault.BrowseRequest{})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	for _, row := range current.Rows {
		if row.ID == old {
			t.Fatal("a superseded record was listed as current")
		}
	}

	history, err := v.Browse(vault.BrowseRequest{IncludeSuperseded: true})
	if err != nil {
		t.Fatalf("browse history: %v", err)
	}
	var found bool
	for _, row := range history.Rows {
		if row.ID == old {
			found, _ = true, row
			if !row.Superseded {
				t.Fatal("the replaced record was listed without saying it had been replaced")
			}
		}
	}
	if !found {
		t.Fatal("history did not reach the replaced record")
	}
}

func TestBrowseRefusesACursorItDidNotIssue(t *testing.T) {
	v := offlineVault(t)
	if _, err := v.Browse(vault.BrowseRequest{Cursor: "not-a-cursor-this-vault-minted"}); err == nil {
		t.Fatal("a forged cursor was accepted")
	}
}
