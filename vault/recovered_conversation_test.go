package vault

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// listedConversations is the conversations a device lists, each with the title
// it is listed under.
func listedConversations(t *testing.T, v *Vault) map[record.ID]string {
	t.Helper()
	rows, err := v.ListSessions(local.SessionQuery{})
	if err != nil {
		t.Fatalf("list the conversations: %v", err)
	}
	listed := make(map[record.ID]string, len(rows))
	for _, row := range rows {
		listed[row.ID] = row.Title
	}
	return listed
}

// A conversation recovered with the vault is listed, under the title a rebuilt
// head is given, and opens with its whole transcript, on a device that held
// nothing of the vault before.
//
// A recovery put back every chunk of the transcript and rebuilt no head for it.
// The head is what lists a conversation and what resume and open load, so the
// conversation was on the device and could be neither found nor opened there
// until a hydrate run afterwards rebuilt it.
func TestARecoveredConversationIsListedAndOpens(t *testing.T) {
	ctx := context.Background()
	writer := OpenWithLedgerForTest(t, t.TempDir())
	saved := saveExchange(t, writer, "Tidepool gauge survey", "survey")
	written, err := writer.LoadSession(ctx, LoadSessionRequest{ID: saved.ID, Transcript: true})
	if err != nil {
		t.Fatalf("open the conversation on the device that saved it: %v", err)
	}
	network := standIn(t, FlushToNetworkForTest(t, writer))

	recovered := OpenWithLedgerForTest(t, t.TempDir())
	network.recoverInto(recovered)

	// A rebuilt head is titled by the first thing the user said.
	opening := written.Messages[0].Parts[0].Text
	if listed := listedConversations(t, recovered); len(listed) != 1 || listed[saved.ID] != opening {
		t.Errorf("the recovered device lists %v, want the conversation %s listed as %q", listed, saved.ID, opening)
	}
	page, err := recovered.Browse(BrowseRequest{Kinds: []record.Kind{record.KindSession}})
	if err != nil {
		t.Fatalf("browse the conversations: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].ID != saved.ID || page.Rows[0].Label != opening {
		t.Errorf("browse lists %+v, want the conversation %s labelled %q", page.Rows, saved.ID, opening)
	}

	loaded, err := recovered.LoadSession(ctx, LoadSessionRequest{ID: saved.ID, Transcript: true})
	if err != nil {
		t.Fatalf("open the recovered conversation: %v", err)
	}
	if !reflect.DeepEqual(loaded.Messages, written.Messages) {
		t.Errorf("the recovered conversation opens with\n%+v\nwant the transcript that was saved\n%+v",
			loaded.Messages, written.Messages)
	}
}

// A conversation recovered with the vault is searchable by meaning exactly when
// a memory recovered with it is: both when the recovery embeds what it restores,
// and neither when it is told not to. Each has the metadata that ranks it by its
// words either way.
func TestARecoveredConversationIsSearchableByMeaningWhenARecoveredMemoryIs(t *testing.T) {
	writer := OpenWithLedgerForTest(t, t.TempDir())
	memory := rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	saved := saveExchange(t, writer, "Tidepool gauge survey", "survey")
	network := standIn(t, FlushToNetworkForTest(t, writer))

	for _, embed := range []bool{true, false} {
		t.Run(fmt.Sprintf("embed=%t", embed), func(t *testing.T) {
			recovered := OpenWithLedgerForTest(t, t.TempDir())
			if _, err := recovered.recoverOver(context.Background(), RecoveryRequest{Embed: embed},
				network.walk, network.read); err != nil {
				t.Fatalf("recover: %v", err)
			}
			for _, r := range []struct {
				name string
				id   record.ID
			}{{"memory", memory}, {"conversation", saved.ID}} {
				if got := recovered.index.Has(r.id); got != embed {
					t.Errorf("the recovered %s has a search vector: %t, want %t", r.name, got, embed)
				}
				if _, err := recovered.local.RankingMetaFor(r.id); err != nil {
					t.Errorf("the recovered %s has no metadata to rank it by its words: %v", r.name, err)
				}
			}
		})
	}
}

// A recovery's report carries the rebuild's: the conversations whose transcripts
// it put back, which of them it rebuilt a head for and which it kept the head of,
// and field by field where each rebuilt head's values came from, as a hydrate's
// report does.
func TestARecoveryReportsTheConversationsItRebuilt(t *testing.T) {
	writer := OpenWithLedgerForTest(t, t.TempDir())
	saved := saveExchange(t, writer, "Tidepool gauge survey", "survey")
	network := standIn(t, FlushToNetworkForTest(t, writer))

	rebuilt := network.recoverInto(OpenWithLedgerForTest(t, t.TempDir())).Rebuild
	if rebuilt.Sessions != 1 || rebuilt.Stored != 1 || rebuilt.Skipped != 0 || len(rebuilt.Heads) != 1 {
		t.Fatalf("a recovery onto a new device reports %d conversation(s), %d head(s) rebuilt and %d kept, "+
			"%d described; want the one conversation rebuilt", rebuilt.Sessions, rebuilt.Stored, rebuilt.Skipped,
			len(rebuilt.Heads))
	}
	head := rebuilt.Heads[0]
	if head.Session == nil || head.Session.ID != saved.ID || head.Existing {
		t.Fatalf("the report describes %+v, want the head rebuilt for %s", head, saved.ID)
	}
	// The recovery embeds what it restores, so the rebuilt head was given a
	// vector here as well.
	for field, want := range map[string]FieldOrigin{
		"title": OriginSynthesised, "summary": OriginLost, "chunks": OriginDerived,
		"created": OriginObserved, "embedding": OriginSynthesised,
	} {
		if got := head.Origins[field]; got != want {
			t.Errorf("the report says the rebuilt head's %s is %q, want %q", field, got, want)
		}
	}

	kept := network.recoverInto(writer).Rebuild
	if kept.Stored != 0 || kept.Skipped != 1 {
		t.Errorf("a recovery on the device that saved the conversation reports %d head(s) rebuilt and %d kept, "+
			"want the one it holds kept", kept.Stored, kept.Skipped)
	}
}

// A recovery on a device that still holds a conversation's head keeps that head,
// as a hydrate does, rather than replace it with one rebuilt from the
// transcript, which has lost the title, the summary and the tags it was saved
// with.
func TestARecoveryKeepsAHeadThisDeviceHolds(t *testing.T) {
	writer := OpenWithLedgerForTest(t, t.TempDir())
	saved := saveExchange(t, writer, "Tidepool gauge survey", "survey")
	network := standIn(t, FlushToNetworkForTest(t, writer))
	written, err := writer.Session(saved.ID)
	if err != nil {
		t.Fatalf("read the head that was written: %v", err)
	}

	network.recoverInto(writer)

	kept, err := writer.Session(saved.ID)
	if err != nil {
		t.Fatalf("read the head after the recovery: %v", err)
	}
	if !reflect.DeepEqual(kept, written) {
		t.Errorf("the recovery replaced the head this device held:\n got %+v\nwant %+v", kept, written)
	}
	if listed := listedConversations(t, writer); listed[saved.ID] != written.Title {
		t.Errorf("the conversation is listed as %q, want the title it was saved with, %q",
			listed[saved.ID], written.Title)
	}
}

// A recovery interrupted while it gives a rebuilt conversation its search vector
// leaves the conversation whole or not there at all, and keeps every record it
// put back before the interrupt, as an interrupt anywhere else in a recovery
// does. The next recovery rebuilds the conversation whole.
//
// A rebuild keeps every head this device already holds, so a head stored before
// its vector was made would be kept without one by every later recovery and
// hydrate: listed, and never found by meaning.
func TestARecoveryInterruptedWhileItEmbedsAConversationLeavesItWholeOrAbsent(t *testing.T) {
	writer := OpenWithLedgerForTest(t, t.TempDir())
	memory := rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	saved := saveExchange(t, writer, "Tidepool gauge survey", "survey")
	network := standIn(t, FlushToNetworkForTest(t, writer))

	embedder := counting()
	recovered := openWith(t, t.TempDir(), embedder)
	ctx, interrupt := context.WithCancel(context.Background())
	defer interrupt()
	// A rebuilt head is titled by the first thing the user said, and the title
	// is all a rebuilt head has to embed.
	opening := exchange("survey", true)[0].Parts[0].Text
	embedder.before = func(_ int, text string) error {
		if text != opening {
			return nil
		}
		interrupt()
		return ctx.Err()
	}

	_, err := recovered.recoverOver(ctx, RecoveryRequest{Embed: true}, network.walk, network.read)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a recovery interrupted while it embedded the conversation reported %v, want the interrupt", err)
	}
	if _, err := recovered.local.RankingMetaFor(memory); err != nil {
		t.Errorf("the memory recovered before the interrupt is gone: %v", err)
	}
	for _, chunk := range chunkIDsOf(saved.Chunks) {
		if _, err := recovered.local.GetBody(chunk); err != nil {
			t.Errorf("chunk %s, recovered before the interrupt, is gone: %v", chunk, err)
		}
	}
	_, headErr := recovered.Session(saved.ID)
	_, metaErr := recovered.local.RankingMetaFor(saved.ID)
	head, meta, vector := headErr == nil, metaErr == nil, recovered.index.Has(saved.ID)
	if head != meta || head != vector {
		t.Errorf("the interrupt left the conversation with a head: %t, ranking metadata: %t, a search vector: %t; "+
			"want all three or none", head, meta, vector)
	}

	embedder.before = nil
	if _, err := recovered.recoverOver(context.Background(), RecoveryRequest{Embed: true},
		network.walk, network.read); err != nil {
		t.Fatalf("recover again: %v", err)
	}
	if _, listed := listedConversations(t, recovered)[saved.ID]; !listed {
		t.Error("the next recovery does not list the conversation")
	}
	if !recovered.index.Has(saved.ID) {
		t.Error("the next recovery left the conversation without a search vector")
	}
}
