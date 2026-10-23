package recall

import (
	"testing"

	"github.com/steven3002/sennit/record"
)

// What a scoped recall says the scope removed is what rank drops for the scope
// from the pool an unscoped recall draws.
//
// That count is taken on the unscoped pool without ranking it, so it asks rank's
// two questions again, in rank's order, and this keeps the two in step. A
// replaced version is counted as held back and not as outside the scope as well,
// a record this device holds no metadata for is outside every scope, and asking
// for history counts a replaced version outside the scope as outside it.
func TestTheScopeIsCountedOnAnUnscopedPoolAsRankWouldDropIt(t *testing.T) {
	candidates, known := mixed(t, 12)
	// One replaced memory and one replaced session, and a session whose
	// metadata this device does not hold.
	known.Superseded = map[record.ID]bool{candidates[1].ID: true, candidates[2].ID: true}
	delete(known.Meta, candidates[5].ID)

	for _, req := range []Request{
		{Scope: []record.Kind{record.KindMemory}},
		{Scope: []record.Kind{record.KindSession}},
		{Scope: []record.Kind{record.KindMemory}, IncludeSuperseded: true},
		{Scope: []record.Kind{record.KindSession}, IncludeSuperseded: true},
		{Scope: []record.Kind{record.KindMemory, record.KindSession}},
	} {
		_, dropped := rank(candidates, known, req, len(candidates))
		if got := outsideScope(candidates, known, req); got != dropped.outOfScope {
			t.Errorf("scope %v, history %v: counted %d outside the scope, rank drops %d for it",
				req.Scope, req.IncludeSuperseded, got, dropped.outOfScope)
		}
	}

	// The counts the cases above agree on, stated, so that agreeing on a wrong
	// number does not pass: six memories and six sessions, two of them replaced
	// and one with no metadata.
	for _, c := range []struct {
		scope   record.Kind
		history bool
		want    int
	}{
		// The four sessions that are neither replaced nor missing their
		// metadata, and the one with no metadata, which is of no class.
		{record.KindMemory, false, 5},
		// Five memories, the replaced one held back, and the session with no
		// metadata.
		{record.KindSession, false, 6},
		{record.KindMemory, true, 6},
		{record.KindSession, true, 7},
	} {
		req := Request{Scope: []record.Kind{c.scope}, IncludeSuperseded: c.history}
		if got := outsideScope(candidates, known, req); got != c.want {
			t.Errorf("scope %s, history %v: counted %d outside the scope, want %d", c.scope, c.history, got, c.want)
		}
	}
}
