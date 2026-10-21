package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// rebuildPhrase opens the vaults these tests rebuild conversations in. Nothing
// here reaches the network, so any valid phrase will do.
const rebuildPhrase = "abandon abandon abandon abandon abandon abandon " +
	"abandon abandon abandon abandon abandon about"

// A transcript is one conversation as the tests below save it.
type transcript struct {
	// opening is the first thing the user says, which is what a rebuilt head is
	// titled by.
	opening string
	// dated is whether its turns record when they were said, and model whether
	// they record the model that answered.
	dated, model bool
	// kept leaves its head on the device, so the rebuild reconstructs it to
	// report on and does not store it.
	kept bool
}

// rebuiltFromTranscripts saves each conversation, drops the heads not kept, and
// returns what a rebuild of the heads from their transcripts reports, embedding
// what it stores when embed is set, as a hydrate at index depth does.
func rebuiltFromTranscripts(t *testing.T, embed bool, transcripts ...transcript) vault.RebuildReport {
	t.Helper()
	ctx := context.Background()
	v, err := vault.Open(ctx, vault.Options{
		Home: t.TempDir(), Phrase: rebuildPhrase, Embedder: embedtest.NewStub(), Offline: true,
	})
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	t.Cleanup(func() { v.Close() })

	start := time.Date(2025, time.March, 14, 9, 0, 0, 0, time.UTC)
	for n, c := range transcripts {
		messages := []record.Message{
			{ID: fmt.Sprintf("c%d-0", n), Role: record.RoleUser,
				Parts: []record.Part{{Type: record.PartText, Text: c.opening}}},
			{ID: fmt.Sprintf("c%d-1", n), Parent: fmt.Sprintf("c%d-0", n), Role: record.RoleAssistant,
				Parts: []record.Part{{Type: record.PartText, Text: "Here is what the vault holds on that."}}},
		}
		for i := range messages {
			if c.dated {
				messages[i].Created = record.At(start.Add(time.Duration(10*n+i) * time.Minute))
			}
			if c.model && messages[i].Role == record.RoleAssistant {
				messages[i].Meta.Model = "answering-model"
			}
		}
		saved, err := v.SaveSession(ctx, vault.SaveSessionRequest{
			Title: c.opening, Summary: "What the conversation settled.", Messages: messages,
		})
		if err != nil {
			t.Fatalf("save %q: %v", c.opening, err)
		}
		if !c.kept {
			if err := v.ForgetSessionHead(saved.ID); err != nil {
				t.Fatalf("drop the head of %q: %v", c.opening, err)
			}
		}
	}
	report, err := v.RebuildSessions(ctx, vault.RebuildRequest{Embed: embed})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	return report
}

// hydrated is a hydrate's report around a rebuild, at the depth the rebuild was
// run for.
func hydrated(depth vault.HydrateDepth, rebuilt vault.RebuildReport) vault.HydrateReport {
	return vault.HydrateReport{Depth: depth, Sessions: rebuilt.Stored, Rebuild: rebuilt}
}

// unrecoverable is the fields the hydrate summary calls not recoverable for
// every conversation it rebuilt.
func unrecoverable(t *testing.T, report vault.HydrateReport) []string {
	t.Helper()
	note, ok := rebuiltHeads(report)
	if !ok {
		t.Fatal("conversations were rebuilt and the summary says nothing of them")
	}
	explained := strings.Join(note.Explanation, " ")
	_, lost, found := strings.Cut(explained, "These are not recoverable: ")
	if !found {
		return nil
	}
	return strings.Split(strings.TrimSuffix(lost, "."), ", ")
}

// reconstructed is the fields the hydrate summary says were made up here.
func reconstructed(t *testing.T, report vault.HydrateReport) []string {
	t.Helper()
	note, _ := rebuiltHeads(report)
	explained := strings.Join(note.Explanation, " ")
	_, invented, found := strings.Cut(explained, "so these were reconstructed here: ")
	if !found {
		return nil
	}
	invented, _, _ = strings.Cut(invented, ". ")
	return strings.Split(strings.TrimSuffix(invented, "."), ", ")
}

// A field only some of the rebuilt conversations lost is said of those alone,
// and they are named, rather than said of every conversation rebuilt.
//
// The summary was written from the rebuild's verdict across every head, which
// keeps the weakest origin any of them reported. One conversation whose turns
// record no time made created and updated not recoverable for all four, three
// of which had them back from their turns.
func TestHydrateSaysWhichConversationsLostAFieldTheOthersGotBack(t *testing.T) {
	undated := "Which of the Tidepool gauges report hourly?"
	report := hydrated(vault.HydrateMetadata, rebuiltFromTranscripts(t, false,
		transcript{opening: "Why does every flush bill a whole slab?", dated: true},
		transcript{opening: "How long may a record wait in the queue?", dated: true},
		transcript{opening: undated},
		transcript{opening: "Can a repack run while the server is up?", dated: true},
	))

	if lost := unrecoverable(t, report); slices.Contains(lost, "created") || slices.Contains(lost, "updated") {
		t.Errorf("created and updated are called not recoverable for all four conversations, "+
			"when one lost them: %v", lost)
	}
	notes := lostBySome(report)
	if len(notes) != 1 {
		t.Fatalf("%d warning(s) about fields some conversations lost, want 1: %+v", len(notes), notes)
	}
	if want := "created and updated are not recoverable for 1 of the 4 conversations"; notes[0].Text != want {
		t.Errorf("the warning says %q, want %q", notes[0].Text, want)
	}
	if !slices.Equal(notes[0].Data, []string{undated}) {
		t.Errorf("the warning names %q, want the one undated conversation, %q", notes[0].Data, undated)
	}
}

// Fields lost by different conversations are said in warnings of their own,
// each naming the conversations that lost them, in the order a head lists its
// fields.
func TestFieldsLostByDifferentConversationsAreSaidApart(t *testing.T) {
	report := hydrated(vault.HydrateMetadata, rebuiltFromTranscripts(t, false,
		transcript{opening: "Which of the Tidepool gauges report hourly?", model: true},
		transcript{opening: "Why does every flush bill a whole slab?", dated: true},
		transcript{opening: "How long may a record wait in the queue?", dated: true, model: true},
	))

	var said [][]string
	for _, note := range lostBySome(report) {
		said = append(said, append([]string{note.Text}, note.Data...))
	}
	want := [][]string{
		{"created and updated are not recoverable for 1 of the 3 conversations",
			"Which of the Tidepool gauges report hourly?"},
		{"models is not recoverable for 1 of the 3 conversations", "Why does every flush bill a whole slab?"},
	}
	if !slices.EqualFunc(said, want, slices.Equal) {
		t.Errorf("the warnings say\n%q\nwant\n%q", said, want)
	}
	if lost := unrecoverable(t, report); slices.Contains(lost, "models") || slices.Contains(lost, "created") {
		t.Errorf("a field some conversations got back is called not recoverable for all of them: %v", lost)
	}
}

// A field every rebuilt conversation lost is said once, in the summary, and no
// warning names them one by one.
func TestAFieldEveryRebuiltConversationLostIsSaidOnce(t *testing.T) {
	report := hydrated(vault.HydrateMetadata, rebuiltFromTranscripts(t, false,
		transcript{opening: "Which of the Tidepool gauges report hourly?"},
		transcript{opening: "Why does every flush bill a whole slab?"},
	))

	lost := unrecoverable(t, report)
	if !slices.Contains(lost, "created") || !slices.Contains(lost, "updated") {
		t.Errorf("neither conversation records a time, and the summary calls %v not recoverable", lost)
	}
	if notes := lostBySome(report); len(notes) != 0 {
		t.Errorf("%d warning(s) name conversations one by one for fields all of them lost: %+v", len(notes), notes)
	}
}

// A long list of conversations is cut short, and says how many it left out.
func TestAWarningNamesOnlySoManyConversations(t *testing.T) {
	var transcripts []transcript
	for i := range listedConversations + 2 {
		transcripts = append(transcripts, transcript{opening: fmt.Sprintf("Which gauge is number %d?", i)})
	}
	transcripts = append(transcripts, transcript{opening: "Why does every flush bill a whole slab?", dated: true})
	notes := lostBySome(hydrated(vault.HydrateMetadata, rebuiltFromTranscripts(t, false, transcripts...)))

	if len(notes) != 1 {
		t.Fatalf("%d warning(s), want 1: %+v", len(notes), notes)
	}
	if want := fmt.Sprintf("created and updated are not recoverable for %d of the %d conversations",
		listedConversations+2, listedConversations+3); notes[0].Text != want {
		t.Errorf("the warning says %q, want %q", notes[0].Text, want)
	}
	if len(notes[0].Data) != listedConversations+1 || notes[0].Data[listedConversations] != "and 2 more" {
		t.Errorf("the warning lists %q, want %d titles and then how many more", notes[0].Data, listedConversations)
	}
}

// A conversation whose head this device still held loses nothing in the
// summary of what a rebuild lost.
//
// A rebuild reconstructs every conversation it finds, to report on, and stores
// only those this device holds no head for. The summary counted the ones it
// stored and described all of them, so a conversation that kept its written
// head made created and updated not recoverable for one that got them back from
// its turns, and at index depth made the search vector not recoverable for a
// conversation that had just been given one.
func TestAConversationThisDeviceStillHeldLosesNothingInTheSummary(t *testing.T) {
	report := hydrated(vault.HydrateIndex, rebuiltFromTranscripts(t, true,
		transcript{opening: "Which of the Tidepool gauges report hourly?", kept: true},
		transcript{opening: "Why does every flush bill a whole slab?", dated: true},
	))
	if report.Sessions != 1 {
		t.Fatalf("the rebuild stored %d head(s), want the 1 this device did not hold", report.Sessions)
	}

	lost := unrecoverable(t, report)
	for _, field := range []string{"created", "updated", "embedding"} {
		if slices.Contains(lost, field) {
			t.Errorf("%s is called not recoverable for the one conversation rebuilt, which has it: %v", field, lost)
		}
	}
	if !slices.Contains(reconstructed(t, report), "embedding") {
		t.Errorf("the rebuilt conversation was given a search vector here, and the summary says only %v "+
			"were reconstructed", reconstructed(t, report))
	}
	if notes := lostBySome(report); len(notes) != 0 {
		t.Errorf("a conversation that kept its head is named among the ones that lost a field: %+v", notes)
	}
}
