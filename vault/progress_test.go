package vault_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/steven3002/sennit/embed"
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

// A first run has no model, and fetching it is the longest wait the run meets.
// The caller is told the download has begun before the hub is asked for
// anything, which is what puts the line on screen while the bytes arrive. When
// the download fails, what the caller gets is the file that could not be had
// and why, since that is the only part of the failure anyone can act on.
func TestAFirstRunSaysItIsDownloadingTheModelAndAFailureSaysWhy(t *testing.T) {
	config := []byte(`{"max_position_embeddings": 512}`)
	digest := sha256.Sum256(config)
	model := embed.Model{
		Name:     "tiny-fp32",
		Repo:     "tidepool/tiny",
		Revision: "0123456789abcdef0123456789abcdef01234567",
		OnnxFile: "onnx/model.onnx",
		Dim:      8,
		Files: []embed.File{
			{Path: "config.json", Size: int64(len(config)), SHA256: hex.EncodeToString(digest[:])},
			{Path: "onnx/model.onnx", Size: 4096, SHA256: strings.Repeat("0", 64)},
		},
	}

	var events recorder
	var toldFirst atomic.Bool
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if phases := events.phases(); len(phases) > 0 && phases[len(phases)-1] == vault.PhaseModelFetch {
			toldFirst.Store(true)
		}
		if strings.HasSuffix(r.URL.Path, "/config.json") {
			w.Write(config)
			return
		}
		http.NotFound(w, r)
	}))
	defer hub.Close()
	t.Setenv(embed.EndpointEnv, hub.URL)

	models := t.TempDir()
	_, err := vault.Open(context.Background(), vault.Options{
		Home: t.TempDir(), Phrase: testPhrase, Model: model, ModelDir: models, Offline: true,
		OnProgress: events.record,
	})
	if err == nil {
		t.Fatal("a vault opened on a model the hub does not have")
	}
	for _, want := range []string{"onnx/model.onnx", "404", "tidepool/tiny"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q:\n%v", want, err)
		}
	}
	if got := events.phases(); !same(got, vault.PhaseUnlock, vault.PhaseModelFetch) {
		t.Errorf("opening reported %v, want the unlock and then the download it stopped in", got)
	}
	if !toldFirst.Load() {
		t.Error("the hub was asked for a file before the caller had been told a download was starting")
	}
	if model.Present(models) {
		t.Error("the model is present after a download that failed")
	}
}
