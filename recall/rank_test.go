package recall

import (
	"testing"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/record"
)

func id(t *testing.T, b byte) record.ID {
	t.Helper()
	var out record.ID
	out[record.IDSize-1] = b
	return out
}

// matches builds a descending-similarity candidate list, which is what the
// index hands the pipeline.
func matches(t *testing.T, n int) []index.Match {
	t.Helper()
	out := make([]index.Match, n)
	for i := range out {
		out[i] = index.Match{ID: id(t, byte(i+1)), Score: float32(1.0 - 0.01*float64(i))}
	}
	return out
}

// pool is what the filter actually sees: the fused pool, here with no lexical
// hits, so the order is the vector pass's own and these tests measure the filter
// and nothing else.
func pool(t *testing.T, n int) []candidate {
	t.Helper()
	return fuse(matches(t, n), nil, DefaultLexicalWeight)
}

// The property the whole design rests on. A filter may reorder; it may never
// change how many records come back. Hard filtering on one wrong tag was
// measured returning nothing at all for ten queries in fifty-nine, which is a
// user asking a question whose answer is in their vault and being told there is
// none.
func TestAWrongFilterNeverEmptiesTheResults(t *testing.T) {
	candidates := pool(t, 20)
	known := Described{Meta: map[record.ID]Meta{}}
	for _, match := range candidates {
		known.Meta[match.ID] = Meta{Type: record.TypeFact, Tags: []string{"sia", "storage"}}
	}

	unfiltered, _ := rank(candidates, known, Request{}, 5)
	wrong := Filter{Tags: []string{"knitting", "aviation"}, Types: []record.Type{record.TypeProfile}}
	filtered, _ := rank(candidates, known, Request{Filter: wrong}, 5)

	if len(filtered) != len(unfiltered) {
		t.Fatalf("a wrong filter returned %d results, unfiltered returns %d", len(filtered), len(unfiltered))
	}
	if len(filtered) != 5 {
		t.Fatalf("returned %d results, asked for 5", len(filtered))
	}
	// Nothing matched, so nothing was boosted and the order is similarity's.
	for i := range filtered {
		if filtered[i].ID != unfiltered[i].ID {
			t.Fatalf("a filter matching no record changed the order at position %d", i)
		}
		if filtered[i].Boost != 0 {
			t.Fatalf("a non-matching record was boosted by %v", filtered[i].Boost)
		}
	}
}

// Whatever the filter, the result count depends only on the pool and k.
func TestResultCountIsIndependentOfTheFilter(t *testing.T) {
	candidates := pool(t, 8)
	known := Described{Meta: map[record.ID]Meta{}}
	for i, match := range candidates {
		tags := []string{"sia"}
		if i%2 == 0 {
			tags = []string{"unrelated"}
		}
		known.Meta[match.ID] = Meta{Type: record.TypeFact, Tags: tags}
	}

	for _, filter := range []Filter{
		{},
		{Tags: []string{"sia"}},
		{Tags: []string{"nothing-carries-this"}},
		{Types: []record.Type{record.TypeCorrection}},
		{Tags: []string{"sia", "nothing-carries-this"}, Types: []record.Type{record.TypeDoc}},
	} {
		for _, k := range []int{1, 3, 5, 8, 20} {
			got, _ := rank(candidates, known, Request{Filter: filter}, k)
			want := min(k, len(candidates))
			if len(got) != want {
				t.Fatalf("filter %+v at k=%d returned %d results, want %d", filter, k, len(got), want)
			}
		}
	}
}

// A correct filter has to actually move something, or it is decoration.
func TestACorrectFilterPromotesAMatchingRecord(t *testing.T) {
	candidates := pool(t, 10)
	buried := candidates[7].ID
	known := Described{Meta: map[record.ID]Meta{}}
	for _, match := range candidates {
		known.Meta[match.ID] = Meta{Type: record.TypeFact, Tags: []string{"unrelated"}}
	}
	known.Meta[buried] = Meta{Type: record.TypeInsight, Tags: []string{"sia", "storage"}}

	before, _ := rank(candidates, known, Request{}, 5)
	for _, hit := range before {
		if hit.ID == buried {
			t.Fatal("the record under test was already in the top 5")
		}
	}

	filter := Filter{Tags: []string{"sia", "storage"}, Types: []record.Type{record.TypeInsight}}
	after, _ := rank(candidates, known, Request{Filter: filter}, 5)
	var found bool
	for _, hit := range after {
		if hit.ID == buried {
			found = true
		}
	}
	if !found {
		t.Fatal("a record matching every term of the filter was not promoted into the top 5")
	}
}

// Partial credit is what makes one wrong tag survivable: matching two terms of
// three has to beat matching one.
func TestTagMatchingIsGraded(t *testing.T) {
	filter := Filter{Tags: []string{"sia", "storage", "latency"}}
	weights := DefaultWeights

	all := filter.boost(weights, Meta{Tags: []string{"sia", "storage", "latency"}})
	two := filter.boost(weights, Meta{Tags: []string{"sia", "storage"}})
	one := filter.boost(weights, Meta{Tags: []string{"sia"}})
	none := filter.boost(weights, Meta{Tags: []string{"knitting"}})

	if !(all > two && two > one && one > none) {
		t.Fatalf("boosts are not graded: all=%v two=%v one=%v none=%v", all, two, one, none)
	}
	if none != 0 {
		t.Fatalf("a record matching nothing was boosted by %v", none)
	}
	if all != weights.Tag {
		t.Fatalf("matching every tag scored %v, want the full tag weight %v", all, weights.Tag)
	}
}

// Tags are compared in one normalised form, so a filter and a record that mean
// the same tag agree however either was typed.
func TestTagMatchingIgnoresCaseAndSurroundingSpace(t *testing.T) {
	filter := Filter{Tags: []string{"  SIA "}}
	if got := filter.boost(DefaultWeights, Meta{Tags: []string{"sia"}}); got != DefaultWeights.Tag {
		t.Fatalf("boost was %v, want %v", got, DefaultWeights.Tag)
	}
}

// A record the device has no metadata for is ranked on similarity rather than
// dropped. Not knowing a record's tags says nothing about the record.
func TestUnknownRecordsAreRankedNotDropped(t *testing.T) {
	candidates := pool(t, 6)
	known := Described{Meta: map[record.ID]Meta{
		candidates[0].ID: {Type: record.TypeFact, Tags: []string{"sia"}},
	}}
	got, _ := rank(candidates, known, Request{Filter: Filter{Tags: []string{"sia"}}}, 6)
	if len(got) != 6 {
		t.Fatalf("returned %d results, want all 6 candidates", len(got))
	}
}

// Supersession removes the replaced version from the default answer and leaves
// it reachable when asked for.
func TestSupersededRecordsAreHiddenByDefaultAndReachableOnRequest(t *testing.T) {
	candidates := pool(t, 6)
	replaced := candidates[0].ID
	known := Described{Superseded: map[record.ID]bool{replaced: true}}

	current, dropped := rank(candidates, known, Request{}, 6)
	if dropped.superseded != 1 {
		t.Fatalf("hid %d records, want 1", dropped.superseded)
	}
	for _, hit := range current {
		if hit.ID == replaced {
			t.Fatal("a superseded record was returned by default recall")
		}
	}

	history, droppedNow := rank(candidates, known, Request{IncludeSuperseded: true}, 6)
	if droppedNow.superseded != 0 {
		t.Fatalf("hid %d records when history was asked for", droppedNow.superseded)
	}
	var found bool
	for _, hit := range history {
		if hit.ID == replaced {
			found = true
		}
	}
	if !found {
		t.Fatal("a superseded record was not retrievable as history")
	}
}

// Ranking is deterministic across runs, so a regression number means something.
func TestRankingIsStableAcrossRuns(t *testing.T) {
	candidates := pool(t, 12)
	known := Described{Meta: map[record.ID]Meta{}}
	for _, match := range candidates {
		known.Meta[match.ID] = Meta{Type: record.TypeFact, Tags: []string{"sia"}}
	}
	filter := Filter{Tags: []string{"sia"}}
	first, _ := rank(candidates, known, Request{Filter: filter}, 5)
	for range 20 {
		again, _ := rank(candidates, known, Request{Filter: filter}, 5)
		for i := range first {
			if first[i].ID != again[i].ID {
				t.Fatalf("ranking differed between runs at position %d", i)
			}
		}
	}
}
