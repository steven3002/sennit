package vault_test

import (
	"context"
	"sync"
	"testing"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// A recorder collects phases the way a caller showing them would, from whatever
// goroutine reports them.
type recorder struct {
	mu       sync.Mutex
	progress []vault.Progress
}

func (r *recorder) record(p vault.Progress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, p)
}

func (r *recorder) phases() []vault.Phase {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []vault.Phase
	for _, p := range r.progress {
		if len(out) == 0 || out[len(out)-1] != p.Phase {
			out = append(out, p.Phase)
		}
	}
	return out
}

func same(got []vault.Phase, want ...vault.Phase) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// What a caller is told while a vault opens, writes and searches with no
// network: the phases in order, and nothing invented.
func TestProgressNamesThePhasesOfAnOfflineRun(t *testing.T) {
	var events recorder
	v, err := vault.Open(context.Background(), vault.Options{
		Home:       t.TempDir(),
		Phrase:     testPhrase,
		Embedder:   embedtest.NewStub(),
		Offline:    true,
		OnProgress: events.record,
	})
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	t.Cleanup(func() { v.Close() })

	// A supplied embedder loads no model, and an offline vault opens no
	// connection, so neither is claimed.
	if got := events.phases(); !same(got, vault.PhaseUnlock, vault.PhaseIndexLoad) {
		t.Errorf("opening reported %v", got)
	}

	events = recorder{}
	if _, err := v.Remember(context.Background(), vault.RememberRequest{
		Statement: "Sia bills a slab whole", Context: "Working out the packer's policy", Type: record.TypeFact,
	}); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if got := events.phases(); !same(got, vault.PhaseEmbed) {
		t.Errorf("remembering reported %v, and offline it can reach no further than embedding", got)
	}

	events = recorder{}
	if _, err := v.Recall(context.Background(), recall.Request{Query: "how is storage billed"}); err != nil {
		t.Fatalf("recall: %v", err)
	}
	if got := events.phases(); !same(got, vault.PhaseSearch) {
		t.Errorf("recalling reported %v", got)
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	if search := events.progress[0]; search.Total != 1 || search.Unit != vault.UnitRecords {
		t.Errorf("the search reported %+v, and the index holds one record", search)
	}
}

// The model download is a wait of its own, and it is reported only when it
// happens.
func TestNoModelPhaseWhenNoModelIsLoaded(t *testing.T) {
	var events recorder
	v, err := vault.Open(context.Background(), vault.Options{
		Home: t.TempDir(), Phrase: testPhrase, Embedder: embedtest.NewStub(), Offline: true,
		OnProgress: events.record,
	})
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	defer v.Close()
	for _, p := range events.phases() {
		if p == vault.PhaseModelFetch || p == vault.PhaseModelLoad {
			t.Errorf("reported %q with no model to load", p)
		}
	}
}
