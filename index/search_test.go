package index_test

import (
	"errors"
	"math"
	"testing"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/record"
)

func unit(values ...float32) []float32 {
	var norm float64
	for _, v := range values {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	out := make([]float32, len(values))
	for n, v := range values {
		out[n] = float32(float64(v) / norm)
	}
	return out
}

func TestSearchRanksByCosine(t *testing.T) {
	idx := index.New("test", 3)
	near, _ := record.NewID()
	middle, _ := record.NewID()
	far, _ := record.NewID()

	for _, entry := range []struct {
		id     record.ID
		vector []float32
	}{
		{near, unit(1, 0.1, 0)},
		{middle, unit(1, 1, 0)},
		{far, unit(0, 0, 1)},
	} {
		if err := idx.Add(entry.id, "test", entry.vector); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	matches, err := idx.Search(unit(1, 0, 0), 3)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("got %d matches, want 3", len(matches))
	}
	if matches[0].ID != near || matches[1].ID != middle || matches[2].ID != far {
		t.Fatalf("ranked %s, %s, %s", matches[0].ID, matches[1].ID, matches[2].ID)
	}
	for n := 1; n < len(matches); n++ {
		if matches[n].Score > matches[n-1].Score {
			t.Fatalf("scores are not descending at position %d", n)
		}
	}
}

func TestSearchHonoursLimit(t *testing.T) {
	idx := index.New("test", 2)
	for range 10 {
		id, _ := record.NewID()
		if err := idx.Add(id, "test", unit(1, 1)); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	matches, err := idx.Search(unit(1, 0), 4)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) != 4 {
		t.Fatalf("got %d matches, want 4", len(matches))
	}
}

func TestAddReplacesAnExistingVector(t *testing.T) {
	idx := index.New("test", 2)
	id, _ := record.NewID()

	if err := idx.Add(id, "test", unit(1, 0)); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := idx.Add(id, "test", unit(0, 1)); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if idx.Len() != 1 {
		t.Fatalf("index holds %d vectors after two adds of one record", idx.Len())
	}
	matches, err := idx.Search(unit(0, 1), 1)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if matches[0].Score < 0.99 {
		t.Fatalf("the replacement vector was not stored: score %v", matches[0].Score)
	}
}

// A removed record has to leave the search, and whatever is moved into the gap
// it leaves must still be found by its own vector. Every record is removed in
// turn, so the run makes both a removal that moves another vector into the gap
// and one from the end, which moves nothing.
func TestRemoveTakesAVectorOutOfTheSearch(t *testing.T) {
	idx := index.New("test", 3)
	vectors := [][]float32{unit(1, 0, 0), unit(0, 1, 0), unit(0, 0, 1)}
	ids := make([]record.ID, len(vectors))
	for n := range ids {
		ids[n], _ = record.NewID()
		if err := idx.Add(ids[n], "test", vectors[n]); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	for n := range ids {
		if !idx.Remove(ids[n]) {
			t.Fatalf("removing vector %d reported that the index did not hold it", n)
		}
		if idx.Remove(ids[n]) {
			t.Fatalf("removing vector %d a second time reported a second removal", n)
		}
		if idx.Has(ids[n]) || idx.Len() != len(ids)-n-1 {
			t.Fatalf("after removing vector %d the index holds %d, the removed one among them: %v",
				n, idx.Len(), idx.Has(ids[n]))
		}
		matches, err := idx.Search(vectors[n], len(ids))
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		for _, match := range matches {
			if match.ID == ids[n] {
				t.Fatalf("removed vector %d still answers a search", n)
			}
		}
		for rest := n + 1; rest < len(ids); rest++ {
			matches, err := idx.Search(vectors[rest], 1)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(matches) != 1 || matches[0].ID != ids[rest] || matches[0].Score < 0.99 {
				t.Fatalf("after removing vector %d, vector %d no longer finds itself: %+v", n, rest, matches)
			}
		}
	}

	// A record that was removed can be added back.
	if err := idx.Add(ids[0], "test", vectors[0]); err != nil {
		t.Fatalf("add back: %v", err)
	}
	matches, err := idx.Search(vectors[0], 1)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) != 1 || matches[0].ID != ids[0] {
		t.Fatalf("a record added back after its removal is not found: %+v", matches)
	}
}

func TestSearchRejectsAMismatchedQuery(t *testing.T) {
	idx := index.New("test", 384)
	if _, err := idx.Search(unit(1, 0), 1); err == nil {
		t.Fatal("a 2-dimension query was accepted by a 384-dimension index")
	}
}

// Two models embed into different spaces, so a cosine between their vectors is
// a number with no meaning. It is also a perfectly valid number, which is what
// makes this failure silent: the ranking would simply be wrong. The index
// refuses the vector rather than trusting every caller to have checked.
func TestIndexRefusesAVectorFromAnotherModel(t *testing.T) {
	idx := index.New("bge-small-en-v1.5-fp32", 3)
	id, _ := record.NewID()

	if err := idx.Add(id, "all-MiniLM-L6-v2", unit(1, 0, 0)); err == nil {
		t.Fatal("a vector from another model was accepted into the index")
	} else if !errors.Is(err, index.ErrForeignModel) {
		t.Fatalf("rejected with %v, want an ErrForeignModel", err)
	}
	if idx.Len() != 0 {
		t.Fatalf("index holds %d vectors after a rejected add", idx.Len())
	}

	if err := idx.Add(id, "bge-small-en-v1.5-fp32", unit(1, 0, 0)); err != nil {
		t.Fatalf("a vector from the index's own model was rejected: %v", err)
	}
	if idx.Len() != 1 {
		t.Fatalf("index holds %d vectors, want 1", idx.Len())
	}
}

// A vector of the wrong width is a different mistake with the same consequence.
func TestIndexRefusesAVectorOfTheWrongWidth(t *testing.T) {
	idx := index.New("test", 3)
	id, _ := record.NewID()
	if err := idx.Add(id, "test", unit(1, 0)); err == nil {
		t.Fatal("a two-dimension vector was accepted into a three-dimension index")
	}
}
