package vault_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/internal/siatest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
	"github.com/steven3002/sennit/vault"
)

// hostlessVault opens a vault over an indexer that refuses every upload, for
// want of hosts to take one, with a queue that is due as soon as anything is in
// it. Every remember sets off a flush, and every flush fails.
func hostlessVault(t *testing.T, home string) *vault.Vault {
	t.Helper()
	indexer, key := siatest.IndexerWithoutHosts(t)
	v, err := vault.Open(context.Background(), vault.Options{
		Home:     home,
		Phrase:   testPhrase,
		Embedder: embedtest.NewStub(),
		Indexer:  indexer,
		AppKey:   key,
		Flush:    packer.Immediate(store.DefaultSlabPayloadSize),
	})
	if err != nil {
		t.Fatalf("open a vault over an indexer with no hosts: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

// refusedUploadMemory is the memory each test here writes while the network
// refuses its upload.
func refusedUploadMemory() vault.RememberRequest {
	return vault.RememberRequest{
		Statement: "The harbour gauge at Tidepool reads in centimetres.",
		Context:   "From the fictional Tidepool field guide, written while the network refused every upload.",
		Type:      record.TypeFact,
		Tags:      []string{"harbour", "gauge"},
	}
}

// refuseQueueInserts makes the vault's database at home refuse to queue
// anything, the way a disk error refuses a write, until the test ends.
//
// It is a trigger written through a connection of the test's own, so the
// remember under test meets the refusal on the one statement that queues the
// memory and on no other.
func refuseQueueInserts(t *testing.T, home string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, "vault.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatalf("open the device's database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TRIGGER refuse_queue_insert BEFORE INSERT ON queue
		BEGIN SELECT RAISE(ABORT, 'the disk refused the write'); END`); err != nil {
		t.Fatalf("make the device refuse to queue: %v", err)
	}
}

// A memory whose upload fails is stored once, and the result says why instead
// of failing.
//
// The memory was stored and queued before the flush it set off ran, and the
// flush's error was all the call returned. A caller told the write failed wrote
// it again, under a new id, and the next flush that worked put both on the
// network. The call now succeeds, says the upload failed and why, and leaves the
// memory queued for that flush to write once.
func TestAMemoryWhoseUploadFailsIsStoredOnceAndSaysWhy(t *testing.T) {
	v := hostlessVault(t, t.TempDir())

	stored, err := v.Remember(context.Background(), refusedUploadMemory())
	if err != nil {
		t.Fatalf("a memory stored on this device was answered with the failure of its upload: %v", err)
	}
	switch {
	case stored.FlushFailed == nil:
		t.Error("the result does not say the upload failed")
	case !strings.Contains(stored.FlushFailed.Error(), "not enough hosts"):
		t.Errorf("the result gives the failure as %q, want the network's refusal", stored.FlushFailed)
	}
	if stored.OnNetwork || stored.Flushed != nil {
		t.Error("the result says the memory reached the network, and its upload failed")
	}

	page, err := v.Browse(vault.BrowseRequest{})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].ID != stored.ID {
		t.Fatalf("the vault lists %d record(s), want the one memory the call stored", len(page.Rows))
	}
	if pending := v.Pending(); pending != 1 {
		t.Errorf("%d record(s) queued for the network, want the memory", pending)
	}

	// The next flush to reach a network able to take it writes the memory, and
	// only the memory.
	objects := vault.FlushToNetworkForTest(t, v)
	if len(objects) != 1 {
		t.Fatalf("the next flush wrote %d object(s), want the one memory", len(objects))
	}
	if entries := v.Entries(); len(entries) != 1 || entries[0].ID != stored.ID {
		t.Errorf("the catalog holds %d record(s) after that flush, want the one memory", len(entries))
	}
}

// A memory recorded against a conversation is linked to it, though its upload
// failed.
//
// The link is written after the flush, so when the flush's failure ended the
// call, the memory was stored and queued and its conversation never named it.
// The write now goes on past the failed flush and finishes.
func TestAMemoryWhoseUploadFailsIsStillLinkedToItsConversation(t *testing.T) {
	home := t.TempDir()
	offline := openVault(t, home, embedtest.NewStub())
	saved, err := offline.SaveSession(context.Background(), vault.SaveSessionRequest{
		Title:    "Tidepool gauge survey",
		Summary:  "Went through which unit each Tidepool gauge reads in.",
		Messages: conversation("survey"),
	})
	if err != nil {
		t.Fatalf("save the conversation: %v", err)
	}
	if err := offline.Close(); err != nil {
		t.Fatalf("close the vault: %v", err)
	}

	v := hostlessVault(t, home)
	req := refusedUploadMemory()
	req.Source = record.Source{SessionID: saved.ID.String(), Span: "survey-1..survey-2"}
	stored, err := v.Remember(context.Background(), req)
	if err != nil {
		t.Fatalf("a memory stored on this device was answered with the failure of its upload: %v", err)
	}
	if stored.LinkedSession == nil || *stored.LinkedSession != saved.ID {
		t.Errorf("the result names %v as the memory's conversation, want %s", stored.LinkedSession, saved.ID)
	}
	head, err := v.Session(saved.ID)
	if err != nil {
		t.Fatalf("read the conversation: %v", err)
	}
	if !slices.Contains(head.Links.Memories, stored.ID) {
		t.Errorf("the conversation links %v, want the memory recorded against it", head.Links.Memories)
	}
}

// A memory the queue refused still fails, though the queue was due for a flush.
//
// Only a flush that fails after the memory was queued leaves it stored and owed
// to the network. A memory the queue refused is owed to nothing, so a result
// that called it stored would be the one claim this call must not make.
func TestAMemoryTheQueueRefusedStillFails(t *testing.T) {
	home := t.TempDir()
	v := hostlessVault(t, home)
	refuseQueueInserts(t, home)

	_, err := v.Remember(context.Background(), refusedUploadMemory())
	if err == nil {
		t.Fatal("a memory the queue refused was reported stored")
	}
	var unflushed *packer.FlushError
	if errors.As(err, &unflushed) {
		t.Errorf("the queue's refusal was reported as a failed flush: %v", err)
	}
	if pending := v.Pending(); pending != 0 {
		t.Errorf("%d record(s) queued for the network, want none", pending)
	}
}

// An interrupted remember still fails, however far the flush it set off got.
//
// Its caller has stopped waiting, and an interrupt is not a network that
// refused. The memory is left as an interrupt has always left it: stored and
// queued, for a later flush.
func TestAnInterruptedRememberStillFails(t *testing.T) {
	v := hostlessVault(t, t.TempDir())
	interrupted, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := v.Remember(interrupted, refusedUploadMemory())
	if err == nil {
		t.Fatal("an interrupted remember reported success")
	}
	var unflushed *packer.FlushError
	if !errors.As(err, &unflushed) {
		t.Errorf("the interrupt ended the call before its flush: %v", err)
	}
	if pending := v.Pending(); pending != 1 {
		t.Errorf("%d record(s) queued for the network, want the memory the interrupt left", pending)
	}
}
