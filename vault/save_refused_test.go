package vault

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// A save refused after it has queued its turns takes them back out, so neither
// the next flush nor a device restoring the vault finds them.
//
// A conversation's turns are sealed and queued before its head is checked,
// embedded and written. A save refused at any of those steps used to leave its
// chunks queued: the next flush wrote them to the network, and a device
// restoring the vault rebuilt a conversation around them that the caller had
// been told was never saved.
func TestASaveRefusedAfterQueueingItsTurnsLeavesNoneOfThem(t *testing.T) {
	for _, refused := range []struct {
		name    string
		request SaveSessionRequest
	}{
		{"a new conversation with no title", SaveSessionRequest{
			Summary:  "Went through which unit each Tidepool gauge reads in.",
			Messages: exchange("untitled", true),
		}},
		{"a summary that cannot be embedded", SaveSessionRequest{
			Title:    "Tidepool gauge survey",
			Summary:  "Unembeddable, as the embedder is about to fail on it.",
			Messages: exchange("unembedded", true),
		}},
	} {
		t.Run(refused.name, func(t *testing.T) {
			embedder := &hookedEmbedder{Stub: embedtest.NewStub(), marker: "Unembeddable"}
			embedder.during = func() error { return errors.New("the embedder failed") }
			v := hookedVault(t, embedder)
			defer v.Close()
			kept := rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")

			if _, err := v.SaveSession(context.Background(), refused.request); err == nil {
				t.Fatal("the save was not refused, so this checks nothing about a refusal")
			}
			if queued := queuedNow(t, v); !slices.Equal(queued, []record.ID{kept}) {
				t.Errorf("the next flush would write %v, want only the memory %s", queued, kept)
			}
			held, err := v.local.BodyIDsOfKind(record.KindChunk)
			if err != nil {
				t.Fatalf("list the chunks on the device: %v", err)
			}
			if len(held) != 0 {
				t.Errorf("the device still holds %d chunk(s) of a save it refused", len(held))
			}
			if held, err := v.CountSessions(); err != nil || held != 0 {
				t.Errorf("the device holds %d conversation(s) after a refused save, want none: %v", held, err)
			}

			fresh := ledgerVault(t, t.TempDir())
			defer fresh.Close()
			if report := hydrateFrom(t, v, fresh); report.Sessions != 0 {
				t.Errorf("a device restoring the vault rebuilt %d conversation(s) from a refused save", report.Sessions)
			}
		})
	}
}

// An append refused because another writer moved the head on first takes its
// own turns back out of the queue, so a device restoring the vault rebuilds the
// conversation the head describes and not one with the refused turns spliced
// into it.
//
// The refused turns were a chunk numbered for the head the loser read, which is
// the number the winner's chunk took. Left queued, it reached the network with
// the next flush, and a restoring device, which orders a transcript by those
// numbers alone, put turns the loser had been told were not stored into the
// middle of the conversation.
func TestAnAppendThatLosesARaceLeavesNoneOfItsTurnsBehind(t *testing.T) {
	ctx := context.Background()
	embedder := &hookedEmbedder{Stub: embedtest.NewStub(), marker: "loser"}
	v := hookedVault(t, embedder)
	defer v.Close()

	saved := saveExchange(t, v, "Tidepool gauge survey", "first")
	embedder.during = func() error {
		// Another writer appends between the loser's read of the head and its
		// write of it.
		_, err := v.SaveSession(ctx, SaveSessionRequest{ID: saved.ID, Messages: exchange("winner", true)})
		return err
	}
	_, err := v.SaveSession(ctx, SaveSessionRequest{
		ID:       saved.ID,
		Summary:  "The loser's account of the survey, written against a head that has moved on.",
		Messages: exchange("loser", true),
	})
	if !errors.Is(err, local.ErrStaleHead) {
		t.Fatalf("an append against a head that moved on returned %v, want ErrStaleHead", err)
	}
	head, err := v.Session(saved.ID)
	if err != nil {
		t.Fatalf("read the head: %v", err)
	}
	if head.Counts.Messages != 8 {
		t.Fatalf("the head counts %d turn(s), want the first part's and the winner's 8", head.Counts.Messages)
	}

	fresh := ledgerVault(t, t.TempDir())
	defer fresh.Close()
	report := hydrateFrom(t, v, fresh)
	if report.Gaps != 0 {
		t.Errorf("the restoring device found %d conversation(s) with a hole or a repeat in the sequence", report.Gaps)
	}
	loaded, err := fresh.LoadSession(ctx, LoadSessionRequest{ID: saved.ID, Transcript: true})
	if err != nil {
		t.Fatalf("open the rebuilt conversation: %v", err)
	}
	var turns []string
	for _, message := range loaded.Messages {
		turns = append(turns, message.ID)
		if strings.HasPrefix(message.ID, "loser") {
			t.Errorf("the rebuilt conversation holds the refused turn %s", message.ID)
		}
	}
	if len(turns) != 8 {
		t.Errorf("the rebuilt conversation holds %d turn(s), want 8: %v", len(turns), turns)
	}
}

// A memory recorded against a conversation this vault does not hold is refused
// before any of it is written: whether the conversation was forgotten or never
// stored, or the source names no conversation at all.
//
// The memory was written in full, its body, its vector, its ranking rows and
// its place in the queue, and only then was the conversation looked for and the
// write refused. The caller was told the memory was not stored when it was, and
// a retry stored a second copy. Forgetting a conversation and then recording
// something learned in it is an ordinary way to get there.
func TestAMemoryFromAConversationThatIsNotHereIsRefusedWithNothingStored(t *testing.T) {
	never, err := record.NewID()
	if err != nil {
		t.Fatalf("record id: %v", err)
	}
	for _, source := range []struct {
		name string
		// session is what the memory names as the conversation it came from.
		session func(t *testing.T, v *Vault) string
		// absent is whether the refusal is ErrNoSession, as against the source
		// not being a conversation's id at all.
		absent bool
	}{
		{"a forgotten conversation", func(t *testing.T, v *Vault) string {
			saved := saveExchange(t, v, "Tidepool gauge survey", "forgotten")
			if err := v.ForgetSession(saved.ID); err != nil {
				t.Fatalf("forget the conversation: %v", err)
			}
			return saved.ID.String()
		}, true},
		{"a conversation never stored", func(*testing.T, *Vault) string { return never.String() }, true},
		{"a source that is not a conversation's id", func(*testing.T, *Vault) string { return "survey-1" }, false},
	} {
		t.Run(source.name, func(t *testing.T) {
			v := ledgerVault(t, t.TempDir())
			defer v.Close()
			named := source.session(t, v)
			indexed := v.IndexHealth().Indexed

			_, err := v.Remember(context.Background(), RememberRequest{
				Statement: "The east gauge at Tidepool was recalibrated in July.",
				Context:   "Recorded against a conversation this vault does not hold.",
				Type:      record.TypeFact,
				Tags:      []string{"calibration"},
				Source:    record.Source{SessionID: named, Span: "survey-1..survey-2"},
			})
			if err == nil {
				t.Fatal("a memory naming a conversation this vault does not hold was stored")
			}
			if source.absent && !errors.Is(err, ErrNoSession) {
				t.Errorf("the refusal is %v, want ErrNoSession", err)
			}
			held, err := v.local.BodyIDsOfKind(record.KindMemory)
			if err != nil {
				t.Fatalf("list the memories on the device: %v", err)
			}
			if len(held) != 0 {
				t.Errorf("the refused memory is on the device: %v", held)
			}
			if queued := queuedNow(t, v); len(queued) != 0 {
				t.Errorf("the refused memory is queued for the network: %v", queued)
			}
			counts, err := v.CountRecords()
			if err != nil {
				t.Fatalf("count records: %v", err)
			}
			if counts.ByKind[record.KindMemory] != 0 {
				t.Errorf("the device holds ranking rows for %d memory(ies), want none", counts.ByKind[record.KindMemory])
			}
			if got := v.IndexHealth().Indexed; got != indexed {
				t.Errorf("the index reports %d searchable vector(s) after the refusal, want %d", got, indexed)
			}
			if answered := recallIDs(t, v, "when was the east gauge recalibrated"); len(answered) != 0 {
				t.Errorf("recall answers with %v, want nothing", answered)
			}
		})
	}
}

// Memories recorded against one conversation at the same time are each linked
// to it, and none is refused because of the others.
//
// A memory is linked by reading its conversation's head, adding the memory and
// writing the head back against the version read. Of several at once, every
// link whose read another write had overtaken was refused, after the memory it
// was linking had been stored, so an agent recording what it learned in a
// conversation, several memories in parallel as agents do, was told some were
// not stored when they were.
func TestMemoriesRecordedAgainstOneConversationAtOnceAreAllLinked(t *testing.T) {
	for round := range 4 {
		v := ledgerVault(t, t.TempDir())
		saved := saveExchange(t, v, "Tidepool gauge survey", fmt.Sprintf("linked-%d", round))
		memories := make([]record.ID, 8)
		for i := range memories {
			id, err := record.NewID()
			if err != nil {
				t.Fatalf("record id: %v", err)
			}
			memories[i] = id
		}

		refused := make([]error, len(memories))
		var linking sync.WaitGroup
		for i := range memories {
			linking.Add(1)
			go func() {
				defer linking.Done()
				refused[i] = v.LinkMemory(saved.ID, memories[i])
			}()
		}
		linking.Wait()

		for i, err := range refused {
			if err != nil {
				t.Errorf("round %d: linking memory %d of %d was refused: %v", round, i+1, len(memories), err)
			}
		}
		head, err := v.Session(saved.ID)
		if err != nil {
			t.Fatalf("read the head: %v", err)
		}
		for _, id := range memories {
			if !slices.Contains(head.Links.Memories, id) {
				t.Errorf("round %d: the conversation does not link memory %s", round, id)
			}
		}
		if err := v.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
}
