package main

import (
	"testing"
	"time"

	"github.com/steven3002/sennit/cmd/sennit/internal/ui"
	"github.com/steven3002/sennit/store/reclaim"
)

// repackGoneScreens is what `sennit reclaim --repack` puts on screen when it
// leaves records where they are because their copies on Sia have been deleted,
// by the file each approved screen is kept in. Each is put together the way
// runReclaim puts it together.
func repackGoneScreens(t *testing.T) map[string]string {
	t.Helper()
	screens := map[string]string{}

	// Some records moved and one was left behind, with its slab.
	packed := reclaim.Repack{
		Records: make([]reclaim.Moved, 59), Gone: make([]reclaim.Held, 1),
		SlabsBefore: 4, SlabsAfter: 2, Peak: 5,
		Before: account(200*mib, 46*gib), After: account(120*mib, 46*gib),
	}
	sweep := reclaim.Sweep{ObjectsSeen: 59, Before: packed.After, After: packed.After}
	quota := reclaimQuota(packed, sweep, nil)
	s := ended(t, 80, 35700*time.Millisecond)
	s.out.done(ui.MarkSuccess, "Released "+humanBytes(quota.freed))
	s.out.report([][2]string{repackRow(packed), sweptRow(sweep, true), quotaRow(quota.before, quota.after, quota.freed)})
	if note, ok := goneNote(packed); ok {
		s.out.note(note)
	}
	screens["reclaim-repack-gone.txt"] = s.text()

	// Every record was left behind, so the repack wrote and released nothing.
	packed = reclaim.Repack{
		Gone: make([]reclaim.Held, 3), SlabsBefore: 2, SlabsAfter: 2, Peak: 2,
		Before: account(80*mib, 46*gib), After: account(80*mib, 46*gib),
	}
	sweep = reclaim.Sweep{Before: packed.After, After: packed.After}
	quota = reclaimQuota(packed, sweep, nil)
	s = ended(t, 80, 21400*time.Millisecond)
	s.out.done(ui.MarkSuccess, "Nothing to release")
	s.out.report([][2]string{repackRow(packed), sweptRow(sweep, false), quotaRow(quota.before, quota.after, quota.freed)})
	if note, ok := goneNote(packed); ok {
		s.out.note(note)
	}
	screens["reclaim-repack-all-gone.txt"] = s.text()
	return screens
}

// What a repack says when it leaves records where they are.
//
// A repack used to stop at the first record whose copy on Sia another device
// had deleted, with nothing moved and nothing released, so the screen had no
// such record to speak of.
func TestWhatARepackSaysOfRecordsItLeftBehind(t *testing.T) {
	for name, got := range repackGoneScreens(t) {
		if want := fixture(t, name); got != want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, want)
		}
	}
}

// The quota a reclaim reports counts a repack that moved some records and left
// others, and counts nothing for one that moved none: it released nothing.
func TestTheQuotaCountsOnlyWhatARepackMoved(t *testing.T) {
	sweep := reclaim.Sweep{Before: account(120*mib, 46*gib), After: account(80*mib, 46*gib)}

	some := reclaim.Repack{Records: make([]reclaim.Moved, 2), Gone: make([]reclaim.Held, 1),
		Before: account(200*mib, 46*gib), After: account(120*mib, 46*gib)}
	if quota := reclaimQuota(some, sweep, nil); quota.before != some.Before || quota.freed != 120*mib {
		t.Errorf("a repack that moved some records is counted from %d with %d freed, want from %d with %d",
			quota.before.PinnedData, quota.freed, some.Before.PinnedData, 120*mib)
	}

	none := reclaim.Repack{Gone: make([]reclaim.Held, 3), Before: account(120*mib, 46*gib), After: account(120*mib, 46*gib)}
	if quota := reclaimQuota(none, sweep, nil); quota.before != sweep.Before || quota.freed != 40*mib {
		t.Errorf("a repack that moved nothing is counted from %d with %d freed, want the sweep's own window, "+
			"from %d with %d", quota.before.PinnedData, quota.freed, sweep.Before.PinnedData, 40*mib)
	}
}

// What a repack says of the records it left behind agrees with their count,
// and one is the count that catches a fixed plural.
func TestWhatARepackSaysOfRecordsItLeftAgreesWithItsCount(t *testing.T) {
	one, ok := goneNote(reclaim.Repack{Gone: make([]reclaim.Held, 1)})
	if !ok || len(one.Explanation) != 1 {
		t.Fatalf("a record left behind is said as %#v", one)
	}
	two, _ := goneNote(reclaim.Repack{Gone: make([]reclaim.Held, 2)})
	for _, c := range []struct{ name, got, want string }{
		{"one record", one.Text, "1 record was left where it is, because its copy on Sia has been deleted"},
		{"two records", two.Text, "2 records were left where they are, because their copies on Sia have been deleted"},
		{"what happens to one", one.Explanation[0], "A device that forgets a record deletes its copy, and forgets " +
			"are not shared between devices. It stays on this device, and keeps its storage, until it is forgotten here too."},
		{"what happens to two", two.Explanation[0], "A device that forgets a record deletes its copy, and forgets " +
			"are not shared between devices. Each stays on this device, and keeps its storage, until it is forgotten here too."},
		{"a row with nothing moved", repackRow(reclaim.Repack{Gone: make([]reclaim.Held, 2)})[1], "nothing moved"},
		{"a row with nothing to move", repackRow(reclaim.Repack{})[1], "nothing to move"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("got %q, want %q", c.got, c.want)
			}
		})
	}
	if _, said := goneNote(reclaim.Repack{Records: make([]reclaim.Moved, 3)}); said {
		t.Error("a repack that left nothing behind says it did")
	}
}
