package local_test

import (
	"testing"
	"time"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
)

// A head written over an existing one is listed with the creation time the new
// head carries, so the listing and the head it was copied from agree.
//
// Every listing column was rewritten from the new head except created_at, which
// kept the first head's time. Nothing displays the column yet, but a listing
// that ordered or filtered by it would have dated the conversation by a head the
// device no longer holds.
func TestAHeadWrittenOverAnotherIsListedWithTheCreationTimeItCarries(t *testing.T) {
	store := openStore(t)
	first := record.At(time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC))
	session := head(t, store, sessionID(t, 51), "Tidepool gauge survey", record.SessionMain, first)

	// The head that replaces it dates the conversation by its first turn rather
	// than by when it was first saved, as a rebuilt head does.
	session.Version = 2
	session.Created = record.At(time.Date(2025, time.March, 14, 9, 0, 0, 0, time.UTC))
	session.Updated = record.At(time.Date(2025, time.March, 14, 9, 3, 0, 0, time.UTC))
	body, err := record.MarshalSession(session)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.PutSessionHead(session, body, 1); err != nil {
		t.Fatalf("write the head over the first: %v", err)
	}

	rows, err := store.ListSessions(local.SessionQuery{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("listed %d session(s), want 1", len(rows))
	}
	if !rows[0].Created.Equal(session.Created.Time) {
		t.Errorf("the head is listed as created %s, want %s, the time the head it holds carries",
			rows[0].Created, session.Created)
	}
	if !rows[0].Updated.Equal(session.Updated.Time) {
		t.Errorf("the head is listed as updated %s, want %s", rows[0].Updated, session.Updated)
	}
}
