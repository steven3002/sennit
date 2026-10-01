package vault

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/store/packer"
	"github.com/steven3002/sennit/store/reclaim"
)

// saveExchange stores one short conversation in a single save.
//
// A single save, because the index counts a conversation once for every save of
// it, and a count asserted against one that was appended to would be measuring
// that rather than the forget.
func saveExchange(t *testing.T, v *Vault, title, prefix string) SaveSessionResult {
	t.Helper()
	saved, err := v.SaveSession(context.Background(), SaveSessionRequest{
		Title:    title,
		Summary:  "Went through which unit each Tidepool gauge reads in.",
		Messages: exchange(prefix, true),
	})
	if err != nil {
		t.Fatalf("save the %s conversation: %v", prefix, err)
	}
	return saved
}

// appendExchange adds a second part to a conversation and returns the chunks it
// wrote.
func appendExchange(t *testing.T, v *Vault, id record.ID, prefix string) []record.ChunkRef {
	t.Helper()
	appended, err := v.SaveSession(context.Background(), SaveSessionRequest{
		ID:       id,
		Messages: exchange(prefix, true),
	})
	if err != nil {
		t.Fatalf("append the %s part: %v", prefix, err)
	}
	return appended.Chunks
}

// chunkIDsOf lists the record ids of the chunks one or more saves wrote.
func chunkIDsOf(refs ...[]record.ChunkRef) []record.ID {
	var out []record.ID
	for _, list := range refs {
		for _, ref := range list {
			out = append(out, ref.ID)
		}
	}
	return out
}

// queuedNow lists what the next flush would write, without disturbing it.
//
// It claims what is free to claim, which is everything no flush in progress
// holds, reads the ids and hands every one of them straight back.
func queuedNow(t *testing.T, v *Vault) []record.ID {
	t.Helper()
	claimed, err := v.local.ClaimQueued("a look at the queue", packer.DefaultClaimTimeout, 0)
	if err != nil {
		t.Fatalf("claim the queue: %v", err)
	}
	ids := make([]record.ID, len(claimed))
	for i, blob := range claimed {
		ids[i] = blob.ID
	}
	if err := v.local.ReleaseQueued(ids); err != nil {
		t.Fatalf("hand the queue back: %v", err)
	}
	return ids
}

// persisted reports whether the index on disk holds a vector for a record.
func persisted(t *testing.T, v *Vault, id record.ID) bool {
	t.Helper()
	stored, err := v.vectors.Hydrate()
	if err != nil {
		t.Fatalf("read the persisted index: %v", err)
	}
	return slices.ContainsFunc(stored, func(entry index.Entry) bool { return entry.ID == id })
}

// checkGone checks that a conversation has left every place this device kept
// it: the head, the ranking rows, both halves of the index, and each chunk's
// body, catalog entry and place in the queue.
func checkGone(t *testing.T, v *Vault, id record.ID, chunks []record.ID) {
	t.Helper()
	if _, err := v.Session(id); !errors.Is(err, ErrNoSession) {
		t.Errorf("the head is still on the device: %v", err)
	}
	if _, err := v.local.RankingMetaFor(id); !errors.Is(err, local.ErrNotFound) {
		t.Errorf("the ranking metadata is still on the device: %v", err)
	}
	if v.index.Has(id) {
		t.Error("the searchable index still holds the conversation's vector")
	}
	if persisted(t, v, id) {
		t.Error("the persisted index still holds the conversation's vector")
	}
	queued := queuedNow(t, v)
	for _, chunk := range chunks {
		if _, err := v.local.GetBody(chunk); !errors.Is(err, local.ErrNotFound) {
			t.Errorf("chunk %s is still on the device: %v", chunk, err)
		}
		if _, err := v.manifest.Lookup(chunk); !errors.Is(err, manifest.ErrNotFound) {
			t.Errorf("chunk %s is still in the catalog: %v", chunk, err)
		}
		if slices.Contains(queued, chunk) {
			t.Errorf("chunk %s is still queued for the next flush", chunk)
		}
	}
}

// A conversation forgotten in a running process leaves that process's searches,
// whether or not it had reached the network.
//
// Its vector came off the disk and stayed in the searchable index, which is
// loaded once when the vault opens. The next recall that ranked it, whatever the
// question, found neither a head nor a catalog entry to read it from and failed
// outright: in a vault of two memories, a question about either of them failed
// at the default limit of five.
func TestAForgottenConversationLeavesSearchInTheSameProcess(t *testing.T) {
	for _, flushed := range []bool{true, false} {
		name := "queued"
		if flushed {
			name = "flushed"
		}
		t.Run(name, func(t *testing.T) {
			v := ledgerVault(t, t.TempDir())
			defer v.Close()

			harbour := rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
			river := rememberFact(t, v, "The river gauge at Tidepool reads in millimetres.", "river")
			saved := saveExchange(t, v, "Tidepool gauge survey", "survey")
			if flushed {
				written := flushQueue(t, v)
				if !written[saved.Chunks[0].ID] {
					t.Fatal("the flush did not write the conversation")
				}
			}
			indexed := v.IndexHealth().Indexed

			if err := v.ForgetSession(saved.ID); err != nil {
				t.Fatalf("forget the conversation: %v", err)
			}
			checkGone(t, v, saved.ID, chunkIDsOf(saved.Chunks))
			if got := v.IndexHealth().Indexed; got != indexed-1 {
				t.Errorf("the index reports %d searchable vector(s) after the forget, want %d", got, indexed-1)
			}

			for _, question := range []string{
				"what does the Tidepool gauge read in",
				"which unit does the harbour gauge read in",
				"which unit does the river gauge read in",
			} {
				answered := recallIDs(t, v, question)
				if slices.Contains(answered, saved.ID) {
					t.Errorf("%q still answers with the forgotten conversation", question)
				}
				if !slices.Contains(answered, harbour) || !slices.Contains(answered, river) {
					t.Errorf("%q lost a memory that was not forgotten: %v", question, answered)
				}
			}
		})
	}
}

// A conversation forgotten before its first flush is not written by that flush,
// and a device that restores the vault afterwards does not bring it back.
//
// Its transcript chunks held their sealed payloads in the upload queue, and
// forgetting the conversation left them there. The next flush uploaded and
// catalogued them, and a device restoring the vault rebuilt the conversation
// from them, every turn of it, titled with its first.
func TestAConversationForgottenBeforeItsFlushDoesNotComeBackOnAnotherDevice(t *testing.T) {
	writer := ledgerVault(t, t.TempDir())
	defer writer.Close()

	kept := rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	saved := saveExchange(t, writer, "Tidepool gauge survey", "forgotten")
	chunks := chunkIDsOf(saved.Chunks)
	if pending := writer.Pending(); pending != 1+len(chunks) {
		t.Fatalf("%d record(s) queued, want the memory and %d chunk(s): nothing here has been flushed",
			pending, len(chunks))
	}

	if err := writer.ForgetSession(saved.ID); err != nil {
		t.Fatalf("forget a conversation still waiting for its flush: %v", err)
	}
	if pending := writer.Pending(); pending != 1 {
		t.Errorf("%d record(s) queued after the forget, want only the memory", pending)
	}

	fresh := ledgerVault(t, t.TempDir())
	defer fresh.Close()
	report := hydrateFrom(t, writer, fresh)
	if report.Sessions != 0 || report.Stored != 0 {
		t.Errorf("the restoring device rebuilt %d conversation(s) from %d chunk(s), want none",
			report.Stored, report.Chunks)
	}
	if _, err := fresh.Session(saved.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("the restoring device holds the forgotten conversation: %v", err)
	}
	for _, chunk := range chunks {
		if _, err := writer.manifest.Lookup(chunk); !errors.Is(err, manifest.ErrNotFound) {
			t.Errorf("the flush catalogued chunk %s of the forgotten conversation: %v", chunk, err)
		}
	}
	// Without this, the checks above would pass on a flush that wrote nothing.
	if _, err := fresh.local.RankingMetaFor(kept); err != nil {
		t.Errorf("the restoring device did not restore the memory that was kept: %v", err)
	}
}

// A conversation part of whose transcript a flush is already writing is refused
// with nothing removed and nothing withdrawn, and is forgotten like any other
// once that flush has landed.
//
// A conversation is several records. The flush holding one of them has its
// payload and catalogues it when it lands, whatever the queue says by then, so
// the forget has to wait for it. Taking the other chunks out of the queue first
// would leave a conversation that is neither forgotten nor whole, with turns
// that never reach the network. Each order is tried: the flush holding the
// first part is what a flush during an append produces, and the flush holding
// only the later part is the order a chunk by chunk withdrawal gets wrong.
func TestAConversationAFlushIsPartlyWritingIsRefusedWithNothingRemoved(t *testing.T) {
	for _, held := range []string{"first", "last"} {
		t.Run("the flush holds the "+held+" part", func(t *testing.T) {
			v := ledgerVault(t, t.TempDir())
			defer v.Close()

			saved := saveExchange(t, v, "Tidepool gauge survey", "first")
			var inFlight []local.QueuedBlob
			var appended []record.ChunkRef
			switch held {
			case "first":
				// A flush claims the queue, and the conversation gains a turn
				// while that flush is still writing.
				claimed, err := v.local.ClaimQueued("a flush under way", packer.DefaultClaimTimeout, 0)
				if err != nil {
					t.Fatalf("claim the queue: %v", err)
				}
				inFlight = claimed
				appended = appendExchange(t, v, saved.ID, "second")
			case "last":
				appended = appendExchange(t, v, saved.ID, "second")
				claimed, err := v.local.ClaimQueued("a flush under way", packer.DefaultClaimTimeout, 0)
				if err != nil {
					t.Fatalf("claim the queue: %v", err)
				}
				// The flush keeps the later part and hands the earlier back.
				if err := v.local.ReleaseQueued(chunkIDsOf(saved.Chunks)); err != nil {
					t.Fatalf("hand the first part back: %v", err)
				}
				for _, blob := range claimed {
					if blob.ID == appended[0].ID {
						inFlight = append(inFlight, blob)
					}
				}
			}
			if len(inFlight) != 1 {
				t.Fatalf("the flush holds %d chunk(s), want 1", len(inFlight))
			}
			chunks := chunkIDsOf(saved.Chunks, appended)
			free := slices.DeleteFunc(slices.Clone(chunks), func(id record.ID) bool { return id == inFlight[0].ID })

			err := v.ForgetSession(saved.ID)
			if !errors.Is(err, local.ErrClaimed) {
				t.Fatalf("forgetting a conversation a flush is partly writing returned %v, want ErrClaimed", err)
			}
			if pending := v.Pending(); pending != len(chunks) {
				t.Errorf("%d chunk(s) queued after the refusal, want all %d", pending, len(chunks))
			}
			if queued := queuedNow(t, v); !slices.Equal(queued, free) {
				t.Errorf("the next flush would write %v, want the chunk no flush holds, %v", queued, free)
			}
			if _, err := v.Session(saved.ID); err != nil {
				t.Errorf("the refused forget removed the head: %v", err)
			}
			for _, chunk := range chunks {
				if _, err := v.local.GetBody(chunk); err != nil {
					t.Errorf("the refused forget removed chunk %s: %v", chunk, err)
				}
			}
			if !v.index.Has(saved.ID) {
				t.Error("the refused forget removed the vector")
			}
			if _, err := v.local.RankingMetaFor(saved.ID); err != nil {
				t.Errorf("the refused forget removed the ranking metadata: %v", err)
			}

			land(t, v, inFlight)
			if err := v.ForgetSession(saved.ID); err != nil {
				t.Fatalf("forget once the flush has landed: %v", err)
			}
			checkGone(t, v, saved.ID, chunks)
			if pending := v.Pending(); pending != 0 {
				t.Errorf("%d record(s) queued after the forget, want none", pending)
			}
		})
	}
}

// A conversation that is partly on the network and partly still queued loses
// both halves: the catalog entries of what was written and the queue rows of
// what was not.
func TestAPartlyFlushedConversationIsForgottenFromTheCatalogAndTheQueue(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	saved := saveExchange(t, v, "Tidepool gauge survey", "first")
	if written := flushQueue(t, v); len(written) != len(saved.Chunks) {
		t.Fatalf("the flush wrote %d record(s), want the first part's %d chunk(s)", len(written), len(saved.Chunks))
	}
	appended := appendExchange(t, v, saved.ID, "second")
	chunks := chunkIDsOf(saved.Chunks, appended)

	if err := v.ForgetSession(saved.ID); err != nil {
		t.Fatalf("forget a conversation that is partly on the network: %v", err)
	}
	checkGone(t, v, saved.ID, chunks)
	if written := flushQueue(t, v); len(written) != 0 {
		t.Errorf("the flush after the forget wrote %d record(s), want none", len(written))
	}
}

// A forget that fails part way can be finished by forgetting the conversation
// again, and a third forget finds nothing there.
//
// The head is what lists the chunks, so it is the last thing to go: a forget
// that stopped after taking the transcript and before the head leaves the head
// to say what is left. The failure here is the index refusing its write, the
// step after the transcript and before the head, and it leaves nothing a search
// trips over in between.
func TestAForgetThatFailsPartWayIsFinishedByForgettingAgain(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	saved := saveExchange(t, v, "Tidepool gauge survey", "first")
	flushQueue(t, v)
	appended := appendExchange(t, v, saved.ID, "second")
	chunks := chunkIDsOf(saved.Chunks, appended)

	if err := v.vectors.Close(); err != nil {
		t.Fatalf("close the persisted index: %v", err)
	}
	if err := v.ForgetSession(saved.ID); err == nil {
		t.Fatal("the forget succeeded with the persisted index closed")
	}
	if _, err := v.Session(saved.ID); err != nil {
		t.Fatalf("a forget that failed part way took the head, and with it the list of what is left: %v", err)
	}
	if _, err := v.Recall(context.Background(), recall.Request{Query: "what does the Tidepool gauge read in", Limit: 5}); err != nil {
		t.Errorf("a recall between the failed forget and the next one failed: %v", err)
	}

	reopened, err := index.OpenStore(v.opts.indexDir(), v.sealer)
	if err != nil {
		t.Fatalf("reopen the persisted index: %v", err)
	}
	v.vectors = reopened
	if err := v.ForgetSession(saved.ID); err != nil {
		t.Fatalf("forget the conversation again: %v", err)
	}
	checkGone(t, v, saved.ID, chunks)

	if err := v.ForgetSession(saved.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("forgetting it a third time returned %v, want ErrNoSession", err)
	}
}

// A conversation this device rebuilt from the network is forgotten like one it
// saved, including when a chunk it names is catalogued and not held here.
//
// A rebuilt head was never queued and may carry a vector of its own, given it
// when the rebuild embedded it, and some of its chunks may be reachable only
// through the catalog.
func TestAConversationRebuiltFromTheNetworkIsForgotten(t *testing.T) {
	writer := ledgerVault(t, t.TempDir())
	defer writer.Close()
	rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	saved := saveExchange(t, writer, "Tidepool gauge survey", "rebuilt")
	network := FlushToNetworkForTest(t, writer)

	fresh := ledgerVault(t, t.TempDir())
	defer fresh.Close()
	RestoreForTest(t, fresh, network, HydrateIndex)
	if _, err := fresh.Session(saved.ID); err != nil {
		t.Fatalf("the restoring device did not rebuild the conversation: %v", err)
	}
	chunks := chunkIDsOf(saved.Chunks)
	// One chunk is left to the catalog alone, the way a device holds one it
	// located on the network and never read.
	if err := fresh.ForgetLocally(chunks[0]); err != nil {
		t.Fatalf("drop the chunk's body: %v", err)
	}
	indexed := fresh.IndexHealth().Indexed
	if !fresh.index.Has(saved.ID) {
		t.Fatal("the rebuilt conversation has no vector, so this checks nothing about one")
	}

	if err := fresh.ForgetSession(saved.ID); err != nil {
		t.Fatalf("forget a rebuilt conversation: %v", err)
	}
	checkGone(t, fresh, saved.ID, chunks)
	if got := fresh.IndexHealth().Indexed; got != indexed-1 {
		t.Errorf("the index reports %d searchable vector(s) after the forget, want %d", got, indexed-1)
	}
	if answered := recallIDs(t, fresh, "what does the Tidepool gauge read in"); slices.Contains(answered, saved.ID) {
		t.Error("recall still answers with the forgotten conversation")
	}
	if restoration := restoration(t, fresh); !restoration.Complete() {
		t.Errorf("the device reports %d chunk(s) it has not restored, which are the forgotten ones",
			restoration.Chunks)
	}
	report, err := fresh.RebuildSessions(context.Background(), RebuildRequest{})
	if err != nil {
		t.Fatalf("rebuild again: %v", err)
	}
	if report.Sessions != 0 {
		t.Errorf("a second rebuild found %d conversation(s), want none", report.Sessions)
	}
}

// A run a forgotten conversation delegated stays, and everything that reads a
// conversation still reads it: it opens with its transcript, it is recalled and
// listed, it takes an append, and it can be forgotten in its turn.
//
// A run is a conversation of its own, stored apart from the one that delegated
// it, and forgetting that one does not forget its runs. The run keeps naming the
// conversation it came from, which nothing reads in a way that needs that
// conversation to be there. What the forget takes of the parent is only the
// parent's: its transcript leaves the queue, and the run's stays in it.
func TestARunOutlivesTheConversationThatDelegatedIt(t *testing.T) {
	ctx := context.Background()
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	memory := rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	parent := saveExchange(t, v, "Tidepool gauge survey", "parent")
	run, err := v.SaveSession(ctx, SaveSessionRequest{
		Title:    "Registry audit",
		Summary:  "Audited the Tidepool station registry for duplicate gauges.",
		Kind:     record.SessionSubagent,
		AgentRef: &record.AgentRef{ID: "agent_7", Name: "auditor"},
		Lineage:  record.Lineage{ParentSession: &parent.ID},
		Messages: exchange("run", true),
	})
	if err != nil {
		t.Fatalf("save the run: %v", err)
	}

	if err := v.ForgetSession(parent.ID); err != nil {
		t.Fatalf("forget the conversation that delegated the run: %v", err)
	}
	checkGone(t, v, parent.ID, chunkIDsOf(parent.Chunks))
	queued := queuedNow(t, v)
	for _, want := range append(chunkIDsOf(run.Chunks), memory) {
		if !slices.Contains(queued, want) {
			t.Errorf("record %s is no longer queued, and only the forgotten conversation's should have gone", want)
		}
	}

	loaded, err := v.LoadSession(ctx, LoadSessionRequest{ID: run.ID, Transcript: true, IncludeSubagents: true})
	if err != nil {
		t.Fatalf("open the run: %v", err)
	}
	if len(loaded.Messages) != 4 {
		t.Errorf("the run opened with %d turn(s), want 4", len(loaded.Messages))
	}
	if parentOf := loaded.Session.Lineage.ParentSession; parentOf == nil || *parentOf != parent.ID {
		t.Errorf("the run no longer names the conversation it came from: %+v", loaded.Session.Lineage)
	}
	if answered := recallIDs(t, v, "the Tidepool station registry audit"); !slices.Contains(answered, run.ID) {
		t.Errorf("recall no longer finds the run: %v", answered)
	}
	listed, err := v.Browse(BrowseRequest{Kinds: []record.Kind{record.KindSession}})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(listed.Rows) != 1 || listed.Rows[0].ID != run.ID || listed.Rows[0].Label != "Registry audit" {
		t.Errorf("browse lists %+v, want the run alone", listed.Rows)
	}
	more := appendExchange(t, v, run.ID, "more")

	if err := v.ForgetSession(run.ID); err != nil {
		t.Fatalf("forget the run after the conversation it came from: %v", err)
	}
	checkGone(t, v, run.ID, chunkIDsOf(run.Chunks, more))
}

// Forgetting a run takes the run's transcript out of the queue and leaves the
// conversation that delegated it as it was, still queued and no longer naming
// the run.
func TestForgettingARunLeavesTheConversationThatDelegatedItQueued(t *testing.T) {
	ctx := context.Background()
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	parent := saveExchange(t, v, "Tidepool gauge survey", "parent")
	run, err := v.SaveSession(ctx, SaveSessionRequest{
		Title:    "Registry audit",
		Summary:  "Audited the Tidepool station registry for duplicate gauges.",
		Kind:     record.SessionSubagent,
		Lineage:  record.Lineage{ParentSession: &parent.ID},
		Messages: exchange("run", true),
	})
	if err != nil {
		t.Fatalf("save the run: %v", err)
	}

	if err := v.ForgetSession(run.ID); err != nil {
		t.Fatalf("forget the run: %v", err)
	}
	checkGone(t, v, run.ID, chunkIDsOf(run.Chunks))
	if queued := queuedNow(t, v); !slices.Equal(queued, chunkIDsOf(parent.Chunks)) {
		t.Errorf("the next flush would write %v, want the conversation's own chunks, %v",
			queued, chunkIDsOf(parent.Chunks))
	}
	loaded, err := v.LoadSession(ctx, LoadSessionRequest{ID: parent.ID, Transcript: true})
	if err != nil {
		t.Fatalf("open the conversation that delegated the run: %v", err)
	}
	if len(loaded.Subagents) != 0 || len(loaded.Messages) != 4 {
		t.Errorf("the conversation opened with %d run(s) and %d turn(s), want none and 4",
			len(loaded.Subagents), len(loaded.Messages))
	}
}

// A run saved against a conversation that has been forgotten is refused, and
// nothing of it is stored.
//
// The run was written in full, its head, its vector and its queued transcript,
// before the conversation it named was looked for, and only then was the save
// refused. The caller was told the run was not stored when it was, and a retry
// stored another.
func TestARunForAForgottenConversationIsRefusedWithNothingStored(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	parent := saveExchange(t, v, "Tidepool gauge survey", "parent")
	if err := v.ForgetSession(parent.ID); err != nil {
		t.Fatalf("forget the conversation: %v", err)
	}
	indexed := v.IndexHealth().Indexed

	_, err := v.SaveSession(context.Background(), SaveSessionRequest{
		Title:    "Registry audit",
		Summary:  "Audited the Tidepool station registry for duplicate gauges.",
		Kind:     record.SessionSubagent,
		Lineage:  record.Lineage{ParentSession: &parent.ID},
		Messages: exchange("run", true),
	})
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("saving a run for a forgotten conversation returned %v, want ErrNoSession", err)
	}
	if held, err := v.CountSessions(); err != nil || held != 0 {
		t.Errorf("the device holds %d conversation(s) after the refusal, want none: %v", held, err)
	}
	if queued := queuedNow(t, v); len(queued) != 0 {
		t.Errorf("the refused run left %d record(s) queued for the network", len(queued))
	}
	if held, err := v.local.BodyIDsOfKind(record.KindChunk); err != nil || len(held) != 0 {
		t.Errorf("the refused run left %d chunk(s) on the device: %v", len(held), err)
	}
	if got := v.IndexHealth().Indexed; got != indexed {
		t.Errorf("the index reports %d searchable vector(s) after the refusal, want %d", got, indexed)
	}
}

// A hookedEmbedder runs a step of the test's own in the middle of a save.
//
// A save embeds the head's text after it has queued the new chunks and before
// it writes the head that names them, so embedding a text that carries the
// marker is the moment between the two: the window another writer, or a
// forget, can land in. The step runs once.
type hookedEmbedder struct {
	*embedtest.Stub
	marker string
	during func() error
}

func (e *hookedEmbedder) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	if e.during != nil && strings.Contains(text, e.marker) {
		during := e.during
		e.during = nil
		if err := during(); err != nil {
			return nil, err
		}
	}
	return e.Stub.EmbedOne(ctx, text)
}

// hookedVault opens an offline vault with a ledger attached, as ledgerVault
// does, over an embedder the test can reach into.
func hookedVault(t *testing.T, embedder *hookedEmbedder) *Vault {
	t.Helper()
	v, err := Open(context.Background(), Options{
		Home:     t.TempDir(),
		Phrase:   ledgerPhrase,
		Embedder: embedder,
		Offline:  true,
	})
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	v.reclaimer = reclaim.New(nil, v.local)
	return v
}

// An append under way when its conversation is forgotten does not bring the
// conversation back, and leaves none of its own turns behind to bring it back
// later.
//
// An append queues its chunks and then writes the head that names them,
// conditioned on the version it read. A head forgotten in between was written
// back anyway, as though the conversation were new: it returned naming a
// transcript the forget had just taken, with the append's vector and ranking
// rows beside it, and the appended turns went to the network with the next
// flush.
func TestAnAppendThatMeetsAForgetDoesNotBringTheConversationBack(t *testing.T) {
	embedder := &hookedEmbedder{Stub: embedtest.NewStub(), marker: "recalibrated"}
	v := hookedVault(t, embedder)
	defer v.Close()

	kept := rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	saved := saveExchange(t, v, "Tidepool gauge survey", "first")
	var forgot error
	embedder.during = func() error {
		forgot = v.ForgetSession(saved.ID)
		return nil
	}

	_, err := v.SaveSession(context.Background(), SaveSessionRequest{
		ID:       saved.ID,
		Summary:  "The north gauge was recalibrated against the harbour reference.",
		Messages: exchange("second", true),
	})
	if forgot != nil {
		t.Fatalf("forget the conversation while an append to it is under way: %v", forgot)
	}
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("an append whose conversation was forgotten under it returned %v, want ErrNoSession", err)
	}
	checkGone(t, v, saved.ID, chunkIDsOf(saved.Chunks))
	held, err := v.local.BodyIDsOfKind(record.KindChunk)
	if err != nil {
		t.Fatalf("list the chunks on the device: %v", err)
	}
	if len(held) != 0 {
		t.Errorf("the device still holds %d chunk(s), which the append wrote", len(held))
	}
	if queued := queuedNow(t, v); !slices.Equal(queued, []record.ID{kept}) {
		t.Errorf("the next flush would write %v, want only the memory %s", queued, kept)
	}
	if answered := recallIDs(t, v, "what does the Tidepool gauge read in"); slices.Contains(answered, saved.ID) {
		t.Error("recall answers with the forgotten conversation")
	}

	fresh := ledgerVault(t, t.TempDir())
	defer fresh.Close()
	if report := hydrateFrom(t, v, fresh); report.Sessions != 0 {
		t.Errorf("a device restoring the vault rebuilt %d conversation(s), want none", report.Sessions)
	}
}
