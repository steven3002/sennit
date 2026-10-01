package local_test

import (
	"errors"
	"testing"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// A head written back against the version its writer read is refused once the
// head has been forgotten, and nothing is written.
//
// The write was an insert whenever the row was missing, whatever version it was
// conditioned on, so a writer that read a head just before a forget took it put
// the conversation back: the head returned, naming a transcript the forget had
// removed.
func TestAHeadWrittenBackAfterItWasForgottenIsRefused(t *testing.T) {
	store := openStore(t)
	session := head(t, store, sessionID(t, 21), "first", record.SessionMain, record.Now())

	if err := store.ForgetSessionHead(session.ID); err != nil {
		t.Fatalf("forget the head: %v", err)
	}
	session.Version = 2
	session.Title = "an append that read the head before it was forgotten"
	body, err := record.MarshalSession(session)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.PutSessionHead(session, body, 1); !errors.Is(err, local.ErrNotFound) {
		t.Fatalf("writing back a forgotten head returned %v, want ErrNotFound", err)
	}
	if _, err := store.GetSessionHead(session.ID); !errors.Is(err, local.ErrNotFound) {
		t.Errorf("the forgotten head is back: %v", err)
	}
	if held, err := store.CountSessions(); err != nil || held != 0 {
		t.Errorf("the device holds %d session(s), want none: %v", held, err)
	}
}

// A head is forgotten only at the version the forget read. One that has moved
// on is left in place and the forget is told; one already gone is no error.
func TestAHeadIsForgottenOnlyAtTheVersionThatWasRead(t *testing.T) {
	store := openStore(t)
	session := head(t, store, sessionID(t, 22), "first", record.SessionMain, record.Now())

	// Another writer appends after the forget read version 1.
	session.Version = 2
	body, err := record.MarshalSession(session)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.PutSessionHead(session, body, 1); err != nil {
		t.Fatalf("append: %v", err)
	}

	if err := store.ForgetSessionHeadAt(session.ID, 1); !errors.Is(err, local.ErrStaleHead) {
		t.Fatalf("forgetting a head that moved on returned %v, want ErrStaleHead", err)
	}
	if _, err := store.GetSessionHead(session.ID); err != nil {
		t.Fatalf("the head that moved on was forgotten anyway: %v", err)
	}
	if err := store.ForgetSessionHeadAt(session.ID, 2); err != nil {
		t.Fatalf("forget the head at the version it is at: %v", err)
	}
	if _, err := store.GetSessionHead(session.ID); !errors.Is(err, local.ErrNotFound) {
		t.Errorf("the head is still there: %v", err)
	}
	if err := store.ForgetSessionHeadAt(session.ID, 2); err != nil {
		t.Errorf("forgetting a head that is already gone returned %v", err)
	}
}
