package reclaim_test

import (
	"testing"

	"github.com/steven3002/sennit/store/reclaim"
)

// A batch fills as many slabs as its bytes come to, rounded up, since a packed
// upload lays its objects end to end.
func TestSlabsForABatch(t *testing.T) {
	t.Parallel()

	const slab = 40 << 20
	for _, c := range []struct {
		name  string
		bytes int64
		want  int
	}{
		{"nothing to write", 0, 0},
		{"one byte", 1, 1},
		{"sixty small records", 60 * 900, 1},
		{"exactly one slab", slab, 1},
		{"one byte over a slab", slab + 1, 2},
		{"exactly three slabs", 3 * slab, 3},
	} {
		if got := reclaim.SlabsFor(c.bytes, slab); got != c.want {
			t.Errorf("%s: %d B fills %d slab(s), want %d", c.name, c.bytes, got, c.want)
		}
	}
	if got := reclaim.SlabsFor(1, 0); got != 0 {
		t.Errorf("a slab of no size holds nothing, got %d", got)
	}
}

// An account past half full can still afford a repack, because what a repack
// needs is room for the slabs it writes and not for the ones it reads from.
//
// This is the account repack exists for. Its live records are a small part of
// what it is billed for, spread across many partly filled slabs, and they fit
// in one. Asked for as much room as the account already held, it was refused
// from the moment it passed half of its quota.
func TestAnAccountPastHalfFullCanStillAffordARepack(t *testing.T) {
	t.Parallel()

	const slab = 40 << 20
	const tier = 100 * slab

	// 80 slabs pinned of 100, and the records in them fit in one.
	check := reclaim.Watermark(account(80*slab, tier), 1, slab)
	if !check.Due {
		t.Errorf("an account at %.0f%% is past the watermark", 100*check.Used)
	}
	if check.Headroom != slab {
		t.Errorf("a repack that writes one slab needs %d B free, got %d B", slab, check.Headroom)
	}
	if !check.Affordable {
		t.Errorf("20 slabs are free and the repack writes one, so it is affordable: needs %d B", check.Headroom)
	}

	// It stops being affordable only when what it writes no longer fits.
	if check := reclaim.Watermark(account(tier-slab/2, tier), 1, slab); check.Affordable {
		t.Errorf("half a slab is free and the repack writes a whole one: needs %d B", check.Headroom)
	}
	if check := reclaim.Watermark(account(80*slab, tier), 21, slab); check.Affordable {
		t.Errorf("20 slabs are free and the repack writes 21: needs %d B", check.Headroom)
	}
}
