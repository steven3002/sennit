package index_test

import (
	"math/rand/v2"
	"testing"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/record"
)

// A search confined to some records finds the nearest of them however many
// records outside them are nearer, and returns the nearest of every record
// beside it.
//
// Search's k nearest here are all records outside the confinement, so nothing
// inside is left in what it returns. SearchWithin keeps the records outside out
// of its heap instead, and reaches the one record inside past all of them.
func TestASearchWithinSomeRecordsReachesPastNearerRecordsOutsideThem(t *testing.T) {
	const k = 5
	idx := index.New("test", 2)
	for n := range k + 1 {
		id, _ := record.NewID()
		if err := idx.Add(id, "test", unit(1, 0.01*float32(n+1))); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	inside, _ := record.NewID()
	if err := idx.Add(inside, "test", unit(0.1, 1)); err != nil {
		t.Fatalf("add: %v", err)
	}
	within := func(id record.ID) bool { return id == inside }

	query := unit(1, 0)
	nearest, err := idx.Search(query, k)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, match := range nearest {
		if within(match.ID) {
			t.Fatal("the record inside is among Search's nearest, so nothing here is nearer than it")
		}
	}

	among, all, err := idx.SearchWithin(query, k, within)
	if err != nil {
		t.Fatalf("search within: %v", err)
	}
	if len(among) != 1 || among[0].ID != inside {
		t.Fatalf("the search within found %+v, want only the record inside", among)
	}
	if !same(all, nearest) {
		t.Fatalf("the nearest of every record came back as %+v, Search returns %+v", all, nearest)
	}
}

// The nearest of every record that SearchWithin returns is exactly what Search
// returns, in the same order and with the same scores, at every depth. That
// includes ties: each heap is kept as Search keeps its own, so two records that
// score the same come back in the order Search gives them.
func TestASearchWithinReturnsWhatSearchReturnsForEveryRecord(t *testing.T) {
	random := rand.New(rand.NewPCG(7, 77))
	idx := index.New("test", 3)
	var ids []record.ID
	for n := range 200 {
		id, _ := record.NewID()
		vector := unit(random.Float32()-0.5, random.Float32()-0.5, random.Float32()-0.5)
		if n%10 == 0 {
			// Every tenth record has the same vector, so their scores tie.
			vector = unit(1, 1, 1)
		}
		if err := idx.Add(id, "test", vector); err != nil {
			t.Fatalf("add: %v", err)
		}
		ids = append(ids, id)
	}
	everyThird := map[record.ID]bool{}
	for n, id := range ids {
		if n%3 == 0 {
			everyThird[id] = true
		}
	}

	for _, query := range [][]float32{unit(1, 0, 0), unit(1, 1, 1), unit(-1, 0.5, 0.2)} {
		for _, k := range []int{1, 7, 50, 200, 500} {
			want, err := idx.Search(query, k)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			among, all, err := idx.SearchWithin(query, k, func(id record.ID) bool { return everyThird[id] })
			if err != nil {
				t.Fatalf("search within: %v", err)
			}
			if !same(all, want) {
				t.Fatalf("k=%d: the nearest of every record differs from what Search returns", k)
			}

			// The confined half is the nearest of the records inside, best
			// first, and only those.
			var inside []index.Match
			everything, _ := idx.Search(query, len(ids))
			for _, match := range everything {
				if everyThird[match.ID] {
					inside = append(inside, match)
				}
			}
			if len(inside) > k {
				inside = inside[:k]
			}
			if len(among) != len(inside) {
				t.Fatalf("k=%d: the search within found %d, want %d", k, len(among), len(inside))
			}
			for n := range among {
				if !everyThird[among[n].ID] || among[n].Score != inside[n].Score {
					t.Fatalf("k=%d: at %d the search within found %+v, want a record inside scoring %v",
						k, n, among[n], inside[n].Score)
				}
			}
		}
	}
}

// Confining a search to every record is the same search, and confining it to
// none finds nothing while still returning the nearest of every record.
func TestASearchWithinEveryRecordOrNoneIsTheSearchOrNothing(t *testing.T) {
	idx := index.New("test", 2)
	for n := range 12 {
		id, _ := record.NewID()
		if err := idx.Add(id, "test", unit(1, float32(n))); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	query := unit(1, 0)
	want, err := idx.Search(query, 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	among, all, err := idx.SearchWithin(query, 5, func(record.ID) bool { return true })
	if err != nil {
		t.Fatalf("search within every record: %v", err)
	}
	if !same(among, want) || !same(all, want) {
		t.Fatalf("a search within every record found %+v and %+v, Search returns %+v", among, all, want)
	}

	among, all, err = idx.SearchWithin(query, 5, func(record.ID) bool { return false })
	if err != nil {
		t.Fatalf("search within no record: %v", err)
	}
	if len(among) != 0 || !same(all, want) {
		t.Fatalf("a search within no record found %+v and %+v, want nothing and %+v", among, all, want)
	}
}

// SearchWithin refuses what Search refuses, and asks for nothing when k is not
// positive.
func TestASearchWithinRefusesAMismatchedQueryAndAnEmptyDepth(t *testing.T) {
	idx := index.New("test", 3)
	id, _ := record.NewID()
	if err := idx.Add(id, "test", unit(1, 0, 0)); err != nil {
		t.Fatalf("add: %v", err)
	}
	everything := func(record.ID) bool { return true }
	if _, _, err := idx.SearchWithin(unit(1, 0), 1, everything); err == nil {
		t.Error("a 2-dimension query was accepted by a 3-dimension index")
	}
	among, all, err := idx.SearchWithin(unit(1, 0, 0), 0, everything)
	if err != nil || among != nil || all != nil {
		t.Errorf("a search within at depth 0 answered %v, %v, %v, want nothing", among, all, err)
	}
}

func same(a, b []index.Match) bool {
	if len(a) != len(b) {
		return false
	}
	for n := range a {
		if a[n] != b[n] {
			return false
		}
	}
	return true
}
