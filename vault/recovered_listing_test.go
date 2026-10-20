package vault

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/steven3002/sennit/record"
)

// A vault rebuilt by recover lists its memories in the order they were stored,
// each dated by its own creation time, whatever order the account's objects came
// back in.
//
// A recovery files each memory's metadata as it reaches the memory, as a hydrate
// does, and the walk of the account's objects does not reach them in the order
// they were stored. Dated by when the recovery reached each one, the memories
// were listed in that order, every date the recovery's. Here the network hands
// them back in the reverse of the order they were stored.
func TestARecoveredVaultListsMemoriesInTheOrderTheyWereStoredWithTheirOwnDates(t *testing.T) {
	ctx := context.Background()
	writer := OpenWithLedgerForTest(t, t.TempDir())
	gauges := []string{"harbour", "river", "estuary"}
	stored := make([]record.ID, 0, len(gauges))
	created := make(map[record.ID]record.Time, len(gauges))
	for _, gauge := range gauges {
		// Times are kept to the millisecond, so memories stored inside one
		// would tie.
		time.Sleep(3 * time.Millisecond)
		result, err := writer.Remember(ctx, RememberRequest{
			Statement: "The " + gauge + " gauge at Tidepool reports every hour.",
			Context:   "Stored by the listing tests, one gauge at a time.",
			Type:      record.TypeFact,
			Tags:      []string{"gauge"},
		})
		if err != nil {
			t.Fatalf("remember the %s gauge: %v", gauge, err)
		}
		stored = append(stored, result.ID)
		created[result.ID] = result.Stored
	}
	// The recovery runs later than anything was stored, so a date it gave a
	// memory is told apart from the memory's own.
	time.Sleep(3 * time.Millisecond)

	network := FlushToNetworkForTest(t, writer)
	slices.Reverse(network)
	recovered := OpenWithLedgerForTest(t, t.TempDir())
	recoverFrom(t, recovered, network)

	page, err := recovered.Browse(BrowseRequest{})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	listed := make([]record.ID, 0, len(page.Rows))
	for _, row := range page.Rows {
		listed = append(listed, row.ID)
		if want := created[row.ID]; !row.Created.Equal(want.Time) {
			t.Errorf("memory %s is listed as created %s, and it was stored at %s", row.ID, row.Created, want)
		}
	}
	newestFirst := slices.Clone(stored)
	slices.Reverse(newestFirst)
	if !slices.Equal(listed, newestFirst) {
		t.Errorf("the recovered vault lists %v, want %v, the last one stored first", listed, newestFirst)
	}
}
