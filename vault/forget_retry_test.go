package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
)

// refuse makes the device's database refuse one kind of write, the way a disk
// error stops a write part way, and returns what lifts the refusal. event is
// when the refusal applies, in a trigger's words.
//
// It is a trigger in the vault's own database file, written through a
// connection of the test's own, so the call under test meets the failure on
// exactly the statement that would fail and on no other.
func refuse(t *testing.T, v *Vault, event string) (lift func()) {
	t.Helper()
	db, err := sql.Open("sqlite", v.opts.dbPath()+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatalf("open the device's database: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER refuse_write ` + event +
		` BEGIN SELECT RAISE(ABORT, 'the disk refused the write'); END`); err != nil {
		db.Close()
		t.Fatalf("make the device refuse %s: %v", event, err)
	}
	return func() {
		t.Helper()
		if _, err := db.Exec(`DROP TRIGGER refuse_write`); err != nil {
			t.Fatalf("lift the refusal: %v", err)
		}
		db.Close()
	}
}

// refuseDeletes makes the device refuse to delete one record's rows from one
// table.
func refuseDeletes(t *testing.T, v *Vault, table string, id record.ID) (lift func()) {
	t.Helper()
	return refuse(t, v, fmt.Sprintf(`BEFORE DELETE ON %s WHEN OLD.record_id = '%s'`, table, id))
}

// memoryGone checks that a memory has left every place this device kept it: the
// catalog, the queue, its body, its ranking rows, and both halves of the index.
func memoryGone(t *testing.T, v *Vault, id record.ID) {
	t.Helper()
	if _, err := v.manifest.Lookup(id); !errors.Is(err, manifest.ErrNotFound) {
		t.Errorf("the catalog still names the memory: %v", err)
	}
	if slices.Contains(queuedNow(t, v), id) {
		t.Error("the memory is still queued for the network")
	}
	if _, err := v.local.GetBody(id); !errors.Is(err, local.ErrNotFound) {
		t.Errorf("the body is still on the device: %v", err)
	}
	if _, err := v.local.RankingMetaFor(id); !errors.Is(err, local.ErrNotFound) {
		t.Errorf("the ranking metadata is still on the device: %v", err)
	}
	if v.index.Has(id) {
		t.Error("the searchable index still holds the memory's vector")
	}
	if persisted(t, v, id) {
		t.Error("the persisted index still holds the memory's vector")
	}
}

// A forget that stops part way is finished by forgetting the memory again,
// whether or not the memory had reached the network, and at whichever step it
// stopped.
//
// The catalog entry goes first, so a forget that failed after it, on a disk
// error, left the body, the vector or the ranking rows behind and no catalog
// entry. Forgetting the memory again found no queued record and no catalog
// entry, and refused it with "no manifest entry", so what the first forget left
// stayed for good. A body left behind was still recalled and opened. Ranking
// rows left on their own were worse: every recall that matched the memory's
// words failed with "no manifest entry" too, in that process and the next.
//
// The entry is gone from the moment the first forget fails. That is the order
// that keeps a half forgotten memory from coming back: a read that misses the
// device looks the record up in the catalog and, online, fetches it from the
// network and keeps it here.
func TestAForgetThatStoppedPartWayIsFinishedByForgettingAgain(t *testing.T) {
	for _, c := range []struct {
		name string
		// flushed is whether the memory had reached the network, and so the
		// catalog, before it was forgotten.
		flushed bool
		// table is where the first forget is stopped: the body, the first thing
		// it removes from the device, or the ranking rows, the last.
		table string
		// body and vector are whether the stopped forget leaves them on the
		// device. It always leaves the ranking rows.
		body, vector bool
	}{
		{name: "on the network, stopped at its body", flushed: true, table: "bodies", body: true, vector: true},
		{name: "still queued, stopped at its body", table: "bodies", body: true, vector: true},
		{name: "on the network, stopped at its ranking rows", flushed: true, table: "record_meta"},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := ledgerVault(t, t.TempDir())
			defer v.Close()

			statement := "The harbour gauge at Tidepool reads in centimetres."
			forgotten := rememberFact(t, v, statement, "harbour")
			kept := rememberFact(t, v, "The river gauge at Tidepool reads in millimetres.", "river")
			if c.flushed {
				if written := flushQueue(t, v); !written[forgotten] || !written[kept] {
					t.Fatalf("the flush wrote %d of the 2 memories", len(written))
				}
			}

			lift := refuseDeletes(t, v, c.table, forgotten)
			err := v.Forget(forgotten)
			if err == nil || !strings.Contains(err.Error(), "the disk refused the write") {
				t.Fatalf("the forget the disk stopped returned %v", err)
			}
			if _, err := v.manifest.Lookup(forgotten); !errors.Is(err, manifest.ErrNotFound) {
				t.Errorf("the stopped forget left the catalog naming the memory: %v", err)
			}
			if _, err := v.local.GetBody(forgotten); (err == nil) != c.body {
				t.Fatalf("the stopped forget left the body %v, want %v: %v", err == nil, c.body, err)
			}
			if v.index.Has(forgotten) != c.vector {
				t.Fatalf("the stopped forget left the vector %v, want %v", v.index.Has(forgotten), c.vector)
			}
			if _, err := v.local.RankingMetaFor(forgotten); err != nil {
				t.Fatalf("the stopped forget took the ranking rows, which go last: %v", err)
			}
			lift()

			if err := v.Forget(forgotten); err != nil {
				t.Fatalf("forgetting the memory again returned %v, want it finished", err)
			}
			memoryGone(t, v, forgotten)
			// In the forgotten memory's own words, which its ranking rows match.
			answered := recallIDs(t, v, "what does the harbour gauge at Tidepool read in")
			if slices.Contains(answered, forgotten) {
				t.Error("recall still answers with the forgotten memory")
			}
			if !slices.Contains(answered, kept) {
				t.Error("recall lost the memory that was not forgotten")
			}
			if written := flushQueue(t, v); written[forgotten] {
				t.Error("the next flush wrote the forgotten memory to the network")
			}
		})
	}
}

// A memory a remember stored part of before it failed can be forgotten.
//
// A disk error after the body, the vector and the ranking rows are written and
// before the record is queued leaves a memory on the device with no queue row
// and no catalog entry, recalled and opened like any other. Forget refused it as
// an id with nothing behind it, so nothing could remove it. The remember is still
// reported as failed with that much stored, which this leaves as it was.
func TestAMemoryARememberLeftHalfStoredCanBeForgotten(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	lift := refuse(t, v, `BEFORE INSERT ON queue`)
	_, err := v.Remember(context.Background(), RememberRequest{
		Statement: "The harbour gauge at Tidepool reads in centimetres.",
		Context:   "From the fictional Tidepool field guide, written while the disk failed.",
		Type:      record.TypeFact,
		Tags:      []string{"harbour"},
	})
	lift()
	if err == nil || !strings.Contains(err.Error(), "the disk refused the write") {
		t.Fatalf("a remember whose queueing the disk refused returned %v", err)
	}
	held, err := v.local.BodyIDsOfKind(record.KindMemory)
	if err != nil || len(held) != 1 {
		t.Fatalf("the failed remember left %d memory body(ies) on the device, want the 1 it stored: %v",
			len(held), err)
	}
	half := held[0]
	if !v.index.Has(half) {
		t.Fatal("the failed remember left no vector, so this checks nothing about one")
	}

	if err := v.Forget(half); err != nil {
		t.Fatalf("forgetting what the failed remember stored returned %v", err)
	}
	memoryGone(t, v, half)
}

// What forgetting again accepts is what a forget that stopped part way leaves of
// a memory, and nothing else: an id this vault never held, and a transcript
// chunk's id with no catalog entry, are refused as they always were, with
// nothing removed.
//
// A missing catalog entry is accepted when the device still holds the memory's
// body or its ranking rows. Each is read with the kind it was stored under, so a
// chunk's body is not taken for a memory's, and neither is a conversation's
// ranking rows, which TestForgetRefusesAnIDWithNoMemoryBehindIt checks.
func TestForgettingAgainAcceptsOnlyWhatAStoppedForgetLeaves(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	never, err := record.NewID()
	if err != nil {
		t.Fatalf("record id: %v", err)
	}
	if err := v.Forget(never); !errors.Is(err, manifest.ErrNotFound) {
		t.Errorf("forgetting an id this vault never held returned %v, want ErrNotFound", err)
	}

	saved := saveExchange(t, v, "Tidepool gauge survey", "survey")
	chunk := saved.Chunks[0].ID
	if written := flushQueue(t, v); !written[chunk] {
		t.Fatal("the flush did not write the conversation's chunk")
	}
	if err := v.manifest.Remove(chunk); err != nil {
		t.Fatalf("take the chunk out of the catalog: %v", err)
	}
	if err := v.Forget(chunk); !errors.Is(err, manifest.ErrNotFound) {
		t.Errorf("forgetting a chunk's id as a memory returned %v, want ErrNotFound", err)
	}
	if _, err := v.local.GetBody(chunk); err != nil {
		t.Errorf("the refused forget removed the chunk's body: %v", err)
	}
}
