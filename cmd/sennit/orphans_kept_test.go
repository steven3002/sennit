package main

import (
	"testing"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/store/reclaim"
)

// orphansKeptScreen is what `sennit reclaim --orphans` puts on screen when it
// keeps a slab that holds nothing because a record on this device still names
// it, put together the way runReclaim puts it together.
func orphansKeptScreen(t *testing.T) string {
	t.Helper()
	sweep := reclaim.Sweep{ObjectsSeen: 6, Before: account(160*mib, 46*gib), After: account(160*mib, 46*gib)}
	released := reclaim.Sweep{SlabsReleased: 1, SlabsKept: 1, Before: sweep.After, After: account(120*mib, 46*gib)}
	quota := reclaimQuota(reclaim.Repack{}, sweep, &released)

	s := ended(t, 80, 24300*time.Millisecond)
	s.out.done(ui.MarkSuccess, "Released "+humanBytes(quota.freed))
	rows := [][2]string{sweptRow(sweep, false), orphansRow(2, 1, released.SlabsReleased)}
	if released.SlabsKept > 0 {
		rows = append(rows, keptRow(released.SlabsKept))
	}
	s.out.report(append(rows, quotaRow(quota.before, quota.after, quota.freed)))
	return s.text()
}

// What a release of slabs that hold nothing says of one it kept.
//
// It used to release every one of them, the slab a record here still names
// among them, so there was nothing kept to speak of.
func TestWhatAReleaseOfOrphansSaysOfASlabItKept(t *testing.T) {
	if got, want := orphansKeptScreen(t), fixture(t, "reclaim-orphans-kept.txt"); got != want {
		t.Errorf("reclaim-orphans-kept.txt:\n got %q\nwant %q", got, want)
	}
}

// The line for kept slabs agrees with its count, and one is the count that
// catches a fixed plural.
func TestTheKeptLineAgreesWithItsCount(t *testing.T) {
	for _, c := range []struct {
		slabs int
		want  string
	}{
		{1, "1 slab that holds nothing, because a record on this device still names it"},
		{2, "2 slabs that hold nothing, because a record on this device still names them"},
	} {
		if row := keptRow(c.slabs); row[0] != "Kept" || row[1] != c.want {
			t.Errorf("%d kept slab(s) read %q %q, want \"Kept\" %q", c.slabs, row[0], row[1], c.want)
		}
	}
}
