package vault_test

import (
	"strings"
	"testing"

	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/vault"
)

// ferryQuery is a question that shares no word with anything writerNetwork
// stores.
const ferryQuery = "which ferry leaves first on sundays"

// A device restored at metadata depth says, in every answer that depends on a
// search vector, that the records it holds have none, and names the hydrate
// that gives them one. It never calls the vault empty.
//
// Such a device holds every record and no search vector. A recall found a
// record only by a word it shared with the question and said nothing of the
// vectors, and a question that shared no word with any record was told that the
// vault held nothing searchable, with every record on the device. The vault
// resource counted the records and none of them indexed, and said nothing of
// what that meant.
func TestADeviceRestoredAtMetadataDepthSaysItsRecordsHaveNoSearchVector(t *testing.T) {
	_, network := writerNetwork(t)
	device := vault.OpenWithLedgerForTest(t, t.TempDir())
	vault.RestoreForTest(t, device, network, vault.HydrateMetadata)
	session := serveDevice(t, device)

	const command = "`sennit hydrate --depth index`"
	says := func(t *testing.T, answer, text string, names, unnamed []string) {
		t.Helper()
		if !strings.Contains(text, "no search vector") || !strings.Contains(text, command) {
			t.Errorf("%s does not say the records here have no search vector, or does not name %s: %q",
				answer, command, text)
		}
		for _, name := range names {
			if !strings.Contains(text, name) {
				t.Errorf("%s does not name %q: %q", answer, name, text)
			}
		}
		for _, name := range unnamed {
			if strings.Contains(text, name) {
				t.Errorf("%s names %q, which the scope leaves out: %q", answer, name, text)
			}
		}
		if strings.Contains(text, "holds nothing") {
			t.Errorf("%s calls the vault empty: %q", answer, text)
		}
	}
	everything := []string{"2 memory record(s)", "1 stored conversation(s)"}

	for _, c := range []struct {
		name string
		in   mcp.RecallIn
		// first is the statement the answer leads with, and empty when it
		// should have no results.
		first string
		// named is what the hint counts, and unnamed what it must not.
		named, unnamed []string
	}{
		{name: "a question no record shares a word with", in: mcp.RecallIn{Query: ferryQuery},
			named: everything},
		{name: "a question that shares words with the memories", in: mcp.RecallIn{Query: gaugeQuery},
			first: harbourGauge, named: everything},
		{name: "that question scoped to memories",
			in:    mcp.RecallIn{Query: gaugeQuery, Scope: []string{"memory"}},
			first: harbourGauge, named: []string{"2 memory record(s)"}, unnamed: []string{"conversation"}},
		{name: "that question scoped to conversations",
			in:    mcp.RecallIn{Query: gaugeQuery, Scope: []string{"session"}},
			named: []string{"1 stored conversation(s)"}, unnamed: []string{"memory record"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var found mcp.RecallOut
			text := callTool(t, session, "recall", c.in, &found)
			if found.Searched != 0 {
				t.Errorf("recall searched %d vector(s) on a device holding none", found.Searched)
			}
			switch {
			case c.first == "" && len(found.Results) != 0:
				t.Errorf("recall found %d record(s), want none", len(found.Results))
			case c.first != "" && (len(found.Results) == 0 || found.Results[0].Title != c.first):
				t.Errorf("recall answered %+v, want %q first", found.Results, c.first)
			}
			says(t, "recall", found.Hint, c.named, c.unnamed)
			if !strings.Contains(text, found.Hint) {
				t.Errorf("the text a model reads does not carry the recall hint: %q", text)
			}
		})
	}

	t.Run("the vault resource", func(t *testing.T) {
		detail := vaultResource(t, session)
		if records, indexed := detail["records"], detail["indexed"]; records != 3.0 || indexed != 0.0 {
			t.Fatalf("the vault resource counts %v record(s) and %v indexed, want the 3 restored and none",
				records, indexed)
		}
		search, _ := detail["search"].(string)
		says(t, "the vault resource", search, everything, nil)
	})
}
