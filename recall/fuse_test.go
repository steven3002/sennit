package recall

import (
	"testing"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/record"
)

func ids(t *testing.T, bytes ...byte) []record.ID {
	t.Helper()
	out := make([]record.ID, len(bytes))
	for i, b := range bytes {
		out[i] = id(t, b)
	}
	return out
}

func order(fused []candidate) []record.ID {
	out := make([]record.ID, len(fused))
	for i, entry := range fused {
		out[i] = entry.ID
	}
	return out
}

// With nothing from the lexical pass, fusion must leave the vector pass's order
// exactly as it found it. Anything else would mean a vault whose lexical index
// is empty ranks differently from one that has no lexical index at all.
func TestFusionWithoutLexicalHitsPreservesTheVectorOrder(t *testing.T) {
	dense := matches(t, 10)
	fused := fuse(dense, nil, DefaultLexicalWeight)

	if len(fused) != len(dense) {
		t.Fatalf("fusion returned %d candidates for %d matches", len(fused), len(dense))
	}
	for i := range dense {
		if fused[i].ID != dense[i].ID {
			t.Fatalf("position %d holds %s, want %s", i, fused[i].ID, dense[i].ID)
		}
		if fused[i].Lexical != 0 {
			t.Fatalf("%s was credited %v by a lexical pass that returned nothing",
				fused[i].ID, fused[i].Lexical)
		}
	}
	for i := 1; i < len(fused); i++ {
		if fused[i].Score >= fused[i-1].Score {
			t.Fatalf("fused scores are not descending at position %d", i)
		}
	}
}

// A record only the lexical pass found still has to reach the ranking. The
// queries a term index rescues are exactly the ones where the vector pass put
// the answer outside its own pool, so intersecting the two passes would discard
// the whole reason for having a second one.
func TestFusionAdmitsARecordOnlyTheLexicalPassFound(t *testing.T) {
	dense := matches(t, 5)
	lexicalOnly := id(t, 99)
	fused := fuse(dense, []record.ID{lexicalOnly}, 1.0)

	var found bool
	for _, entry := range fused {
		if entry.ID == lexicalOnly {
			found = true
			if entry.Similarity != 0 {
				t.Fatalf("a record the vector pass never scored reports similarity %v", entry.Similarity)
			}
			if entry.Lexical == 0 {
				t.Fatal("a lexical-only record was admitted with no lexical contribution")
			}
		}
	}
	if !found {
		t.Fatal("a record found only by the lexical pass never reached the ranking")
	}
	if len(fused) != len(dense)+1 {
		t.Fatalf("fusion returned %d candidates, want %d", len(fused), len(dense)+1)
	}
}

// The weight is a dial, and both of its ends have to mean what they say.
func TestLexicalWeightBoundsAreDenseOnlyAndFullParity(t *testing.T) {
	dense := matches(t, 6)
	// The lexical pass disagrees with the vector pass completely.
	lexical := ids(t, 6, 5, 4, 3, 2, 1)

	none := fuse(dense, lexical, 0)
	for i := range dense {
		if none[i].ID != dense[i].ID {
			t.Fatalf("weight 0 reordered position %d", i)
		}
		if none[i].Lexical != 0 {
			t.Fatalf("weight 0 credited %v to the lexical pass", none[i].Lexical)
		}
	}

	// At parity the two passes very nearly cancel on a reversed list: a record
	// at rank r in one sits at rank n+1-r in the other. They do not cancel
	// exactly, because 1/(k+r) is convex and the middle ranks lose slightly
	// less than the ends, but the ordering the vector pass had is flattened to
	// almost nothing. That is what giving the lexical pass an equal vote means.
	spread := func(fused []candidate) float32 {
		lo, hi := fused[0].Score, fused[0].Score
		for _, entry := range fused {
			lo, hi = min(lo, entry.Score), max(hi, entry.Score)
		}
		return hi - lo
	}
	denseSpread, paritySpread := spread(none), spread(fuse(dense, lexical, 1.0))
	if paritySpread*10 >= denseSpread {
		t.Fatalf("at parity a reversed lexical list left a score spread of %v against the vector pass's %v",
			paritySpread, denseSpread)
	}
}

// A heavier weight has to move a lexically-matched record further up, or the
// dial is not connected to anything.
func TestAHeavierLexicalWeightPromotesAMatchedRecord(t *testing.T) {
	dense := matches(t, 20)
	buried := dense[15].ID

	positionOf := func(weight float32) int {
		fused := fuse(dense, []record.ID{buried}, weight)
		ranked, _ := rank(fused, Described{}, Request{}, len(fused))
		for i, entry := range ranked {
			if entry.ID == buried {
				return i
			}
		}
		t.Fatalf("record vanished at weight %v", weight)
		return -1
	}

	light, heavy := positionOf(0.01), positionOf(1.0)
	if !(heavy < light) {
		t.Fatalf("a lexical match sat at position %d under weight 1.0 and %d under 0.01", heavy, light)
	}
}

// Fusion must not depend on map iteration order, or two runs over one vault
// would disagree and every measured number would be noise.
func TestFusionIsDeterministic(t *testing.T) {
	dense := matches(t, 30)
	lexical := ids(t, 30, 1, 17, 4, 22, 9, 3)

	want := order(fuse(dense, lexical, DefaultLexicalWeight))
	for range 50 {
		got := order(fuse(dense, lexical, DefaultLexicalWeight))
		if len(got) != len(want) {
			t.Fatalf("fusion returned %d candidates, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("fusion differed between runs at position %d", i)
			}
		}
	}
}

// A record the lexical pass returns more than once must be counted once, at its
// best position, or a duplicate would buy a record a second vote.
func TestADuplicateLexicalHitIsCountedOnce(t *testing.T) {
	dense := matches(t, 4)
	repeated := dense[2].ID
	once := fuse(dense, []record.ID{repeated}, 1.0)
	twice := fuse(dense, []record.ID{repeated, repeated}, 1.0)

	for i := range once {
		if once[i].Score != twice[i].Score {
			t.Fatalf("a repeated lexical hit changed %s from %v to %v",
				once[i].ID, once[i].Score, twice[i].Score)
		}
	}
}

// Fusion sits beneath the filter, and the filter still may not remove anything.
// The invariant is measured here on the fused pool because that is what the
// filter now sees.
func TestFusionDoesNotLetTheFilterDropRecords(t *testing.T) {
	dense := matches(t, 12)
	lexical := ids(t, 40, 41, 42)
	fused := fuse(dense, lexical, DefaultLexicalWeight)

	known := Described{Meta: map[record.ID]Meta{}}
	for _, entry := range fused {
		known.Meta[entry.ID] = Meta{Type: record.TypeFact, Tags: []string{"sia"}}
	}
	wrong := Filter{Tags: []string{"knitting"}, Types: []record.Type{record.TypeDoc}}

	for _, k := range []int{1, 5, 10, 15, 100} {
		plain, _ := rank(fused, known, Request{}, k)
		filtered, _ := rank(fused, known, Request{Filter: wrong}, k)
		if len(plain) != len(filtered) {
			t.Fatalf("at k=%d a wrong filter returned %d results against %d",
				k, len(filtered), len(plain))
		}
		if want := min(k, len(fused)); len(plain) != want {
			t.Fatalf("at k=%d fusion returned %d results, want %d", k, len(plain), want)
		}
	}
}

// The index is unchanged by fusion: a match list arriving from the vector pass
// keeps its cosine on the way through, because that is what a caller reads to
// see how much of a hit was earned by meaning.
func TestFusionCarriesCosineThrough(t *testing.T) {
	dense := []index.Match{
		{ID: id(t, 1), Score: 0.81},
		{ID: id(t, 2), Score: 0.42},
	}
	fused := fuse(dense, []record.ID{id(t, 2)}, DefaultLexicalWeight)
	for i, want := range []float32{0.81, 0.42} {
		if fused[i].Similarity != want {
			t.Fatalf("position %d reports similarity %v, want %v", i, fused[i].Similarity, want)
		}
	}
}
