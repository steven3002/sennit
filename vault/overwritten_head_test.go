package vault

import (
	"context"
	"testing"
	"time"

	"github.com/steven3002/sennit/local"
)

// A head rebuilt over the one this device saved is listed with the creation
// time the rebuilt head carries, which is the time open and recall give.
//
// The device that saved the conversation dated its head by the save. A rebuild
// asked to overwrite dates the head it writes by the first turn that records a
// time, and the listing went on giving the save's time beside a head that no
// longer said so.
func TestAHeadRebuiltOverTheSavedOneIsListedWithTheTimeItCarries(t *testing.T) {
	v := ledgerVault(t, t.TempDir())
	defer v.Close()

	saved := saveExchange(t, v, "Tidepool gauge survey", "survey")
	if _, err := v.RebuildSessions(context.Background(), RebuildRequest{Overwrite: true}); err != nil {
		t.Fatalf("rebuild the head over the saved one: %v", err)
	}
	rebuilt, err := v.Session(saved.ID)
	if err != nil {
		t.Fatalf("read the rebuilt head: %v", err)
	}
	// The first turn of exchange, which every rebuilt head here is dated by.
	firstTurn := time.Date(2025, time.March, 14, 9, 0, 0, 0, time.UTC)
	if !rebuilt.Created.Equal(firstTurn) {
		t.Fatalf("the rebuilt head was created %s, want its first turn's time %s, so this checks nothing",
			rebuilt.Created, firstTurn.Format(time.RFC3339))
	}

	rows, err := v.ListSessions(local.SessionQuery{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != saved.ID {
		t.Fatalf("listed %+v, want the one conversation", rows)
	}
	if !rows[0].Created.Equal(rebuilt.Created.Time) {
		t.Errorf("the conversation is listed as created %s, and its head says %s", rows[0].Created, rebuilt.Created)
	}
}
