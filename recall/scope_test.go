package recall

import (
	"testing"

	"github.com/steven3002/sennit/record"
)

// mixed builds a pool of alternating memories and sessions with the metadata a
// scope selects on.
func mixed(t *testing.T, n int) ([]candidate, Described) {
	t.Helper()
	candidates := pool(t, n)
	known := Described{Meta: map[record.ID]Meta{}}
	for i, entry := range candidates {
		if i%2 == 0 {
			known.Meta[entry.ID] = Meta{Kind: record.KindMemory, Type: record.TypeFact, Tags: []string{"sia"}}
			continue
		}
		known.Meta[entry.ID] = Meta{Kind: record.KindSession, Tags: []string{"sia"}}
	}
	return candidates, known
}

// A scope is a hard selector and is meant to be. It is the one thing in the
// ranking that shortens an answer, which is why it is only ever set explicitly
// by a caller naming a container and is never inferred from a query.
func TestAScopeSelectsOneKindAndReportsWhatItRemoved(t *testing.T) {
	candidates, known := mixed(t, 20)

	all, dropped := rank(candidates, known, Request{}, 20)
	if len(all) != 20 || dropped.outOfScope != 0 {
		t.Fatalf("an unscoped ranking returned %d records and excluded %d", len(all), dropped.outOfScope)
	}

	sessions, dropped := rank(candidates, known, Request{Scope: []record.Kind{record.KindSession}}, 20)
	if len(sessions) != 10 {
		t.Fatalf("scoping to sessions returned %d records, want 10", len(sessions))
	}
	if dropped.outOfScope != 10 {
		t.Fatalf("the scope reported excluding %d records, want 10", dropped.outOfScope)
	}
	for _, entry := range sessions {
		if known.Meta[entry.ID].Kind != record.KindSession {
			t.Fatal("a scope selecting sessions returned something else")
		}
	}

	// Selecting both kinds is the same as selecting neither, so a caller that
	// enumerates every kind is not penalised for being explicit.
	both, dropped := rank(candidates, known,
		Request{Scope: []record.Kind{record.KindMemory, record.KindSession}}, 20)
	if len(both) != 20 || dropped.outOfScope != 0 {
		t.Fatalf("scoping to both kinds returned %d records and excluded %d", len(both), dropped.outOfScope)
	}
}

// The scope and the filter are separate mechanisms, and the difference is the
// point: a filter is a guess and must never cost an answer, a scope is a
// statement and must be honoured. Applying both, a wrong filter still costs
// nothing inside the selected scope.
func TestAWrongFilterInsideAScopeStillCostsNothing(t *testing.T) {
	candidates, known := mixed(t, 20)
	scope := []record.Kind{record.KindSession}

	plain, _ := rank(candidates, known, Request{Scope: scope}, 5)
	wrong, _ := rank(candidates, known, Request{
		Scope:  scope,
		Filter: Filter{Tags: []string{"knitting"}, Types: []record.Type{record.TypeProfile}},
	}, 5)

	if len(wrong) != len(plain) {
		t.Fatalf("a wrong filter inside a scope returned %d records against %d", len(wrong), len(plain))
	}
	for i := range wrong {
		if wrong[i].ID != plain[i].ID {
			t.Fatal("a filter that matched nothing still reordered the scoped results")
		}
	}
}

// A record whose class this device does not know cannot satisfy an explicit
// scope, so it is excluded, and counted, because that exclusion is the one the
// caller might need to see.
func TestACandidateWithNoMetadataIsOutOfScopeAndCounted(t *testing.T) {
	candidates, known := mixed(t, 4)
	orphan := candidates[0].ID
	delete(known.Meta, orphan)

	scoped, dropped := rank(candidates, known, Request{Scope: []record.Kind{record.KindMemory}}, 10)
	for _, entry := range scoped {
		if entry.ID == orphan {
			t.Fatal("a candidate with no known class satisfied an explicit scope")
		}
	}
	if dropped.outOfScope != 3 {
		t.Fatalf("the scope excluded %d records, want 3, two sessions and the unknown one", dropped.outOfScope)
	}

	// Unscoped, the same record still ranks: not knowing a record's metadata is
	// a gap in this device's index, never evidence about the record.
	unscoped, _ := rank(candidates, known, Request{}, 10)
	var present bool
	for _, entry := range unscoped {
		present = present || entry.ID == orphan
	}
	if !present {
		t.Fatal("a candidate with no metadata was dropped from an unscoped ranking")
	}
}
