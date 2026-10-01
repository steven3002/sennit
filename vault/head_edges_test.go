package vault

import (
	"errors"
	"slices"
	"testing"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// An edge written onto a head that another writer moved on in the meantime is
// written again onto the head as it now is, keeping what the other writer
// wrote; and a head that never stops moving is reported rather than written
// over.
func TestAnEdgeWrittenOntoAHeadThatMovedOnIsWrittenAgain(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()
	saved := saveExchange(t, v, "Tidepool gauge survey", "edges")
	ids := make([]record.ID, 3)
	for i := range ids {
		id, err := record.NewID()
		if err != nil {
			t.Fatalf("record id: %v", err)
		}
		ids[i] = id
	}

	attempts := 0
	err := v.reviseEdges(saved.ID, func(session *record.Session) bool {
		attempts++
		if attempts == 1 {
			// Another writer links a memory between this read and this write.
			if err := v.LinkMemory(saved.ID, ids[0]); err != nil {
				t.Fatalf("the other writer's link: %v", err)
			}
		}
		session.Links.Memories = append(session.Links.Memories, ids[1])
		return true
	})
	if err != nil {
		t.Fatalf("write an edge onto a head that moved on: %v", err)
	}
	if attempts != 2 {
		t.Errorf("the edge was applied %d time(s), want twice: to the head read and to the head as it became", attempts)
	}
	head, err := v.Session(saved.ID)
	if err != nil {
		t.Fatalf("read the head: %v", err)
	}
	if !slices.Equal(head.Links.Memories, ids[:2]) {
		t.Errorf("the head links %v, want the other writer's memory and then this one, %v", head.Links.Memories, ids[:2])
	}

	attempts = 0
	err = v.reviseEdges(saved.ID, func(session *record.Session) bool {
		attempts++
		// Another writer moves the head on every time.
		moved, err := v.session(saved.ID)
		if err != nil {
			t.Fatalf("read the head: %v", err)
		}
		if err := v.reviseHead(moved); err != nil {
			t.Fatalf("the other writer's write: %v", err)
		}
		session.Links.Memories = append(session.Links.Memories, ids[2])
		return true
	})
	if !errors.Is(err, local.ErrStaleHead) {
		t.Errorf("a head that never stopped moving returned %v, want ErrStaleHead", err)
	}
	if attempts != headRevisions {
		t.Errorf("the edge was tried %d time(s), want %d", attempts, headRevisions)
	}
}
