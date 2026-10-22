package vault

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/steven3002/sennit/sia"
)

// What the vault hands a repack and a release of slabs that hold nothing:
// the slabs its removals name while a forgotten copy's deletion is owed, and
// the slabs it names for the records it holds.
//
// A repack keeps the first, so the sweep after it deletes the copy and then
// releases the slab. A release of orphans keeps the second. Both follow the
// catalog as it changes, so a slab is handed over for as long as its reason
// lasts and no longer.
func TestTheVaultNamesTheSlabsAReclaimMustKeep(t *testing.T) {
	v := OpenWithLedgerForTest(t, t.TempDir())
	kept := rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	forgotten := rememberFact(t, v, "The river gauge at Tidepool reads in millimetres.", "river")
	network := standIn(t, FlushToNetworkForTest(t, v))
	held := []sia.SlabID{probeSlab}

	if owed := v.slabsOwedADeletion(); len(owed) != 0 {
		t.Errorf("before any forget the vault owes a deletion on %v", owed)
	}
	if named := slices.Collect(maps.Keys(v.slabsNamed())); !slices.Equal(named, held) {
		t.Errorf("the vault names %v for the records it holds, want %v", named, held)
	}

	if err := v.Forget(forgotten); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if owed := v.slabsOwedADeletion(); !slices.Equal(owed, held) {
		t.Errorf("after the forget the vault owes a deletion on %v, want %v", owed, held)
	}
	if named := slices.Collect(maps.Keys(v.slabsNamed())); !slices.Equal(named, held) {
		t.Errorf("the vault names %v for the record it still holds, want %v", named, held)
	}

	if _, err := v.deleteForgotten(context.Background(), network.deleteObjects); err != nil {
		t.Fatalf("delete the forgotten copy: %v", err)
	}
	if owed := v.slabsOwedADeletion(); len(owed) != 0 {
		t.Errorf("after the copy was deleted the vault still owes a deletion on %v", owed)
	}

	if err := v.Forget(kept); err != nil {
		t.Fatalf("forget the other: %v", err)
	}
	if named := v.slabsNamed(); len(named) != 0 {
		t.Errorf("with no record held the vault still names %v", named)
	}
}
