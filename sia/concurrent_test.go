package sia

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestInFlightRunsEveryIndexOnce(t *testing.T) {
	const n = 500
	var mu sync.Mutex
	seen := make(map[int]int, n)

	if err := inFlight(t.Context(), n, PinConcurrency, func(_ context.Context, i int) error {
		mu.Lock()
		defer mu.Unlock()
		seen[i]++
		return nil
	}); err != nil {
		t.Fatalf("inFlight: %v", err)
	}
	if len(seen) != n {
		t.Fatalf("ran %d of %d indexes", len(seen), n)
	}
	for i, count := range seen {
		if count != 1 {
			t.Fatalf("index %d ran %d times", i, count)
		}
	}
}

func TestInFlightRespectsTheLimit(t *testing.T) {
	const limit = 4
	var current, peak atomic.Int64

	if err := inFlight(t.Context(), 200, limit, func(_ context.Context, _ int) error {
		running := current.Add(1)
		defer current.Add(-1)
		for {
			was := peak.Load()
			if running <= was || peak.CompareAndSwap(was, running) {
				break
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("inFlight: %v", err)
	}
	if peak.Load() > limit {
		t.Fatalf("%d calls were in flight at once, limit is %d", peak.Load(), limit)
	}
}

func TestInFlightStopsAtTheFirstFailure(t *testing.T) {
	wanted := errors.New("indexer refused the pin")
	var calls atomic.Int64

	err := inFlight(t.Context(), 10_000, 2, func(_ context.Context, i int) error {
		calls.Add(1)
		if i == 3 {
			return wanted
		}
		return nil
	})
	if !errors.Is(err, wanted) {
		t.Fatalf("got %v, want the underlying failure", err)
	}
	// The point of stopping is to stop: a batch that cannot finish should not
	// keep spending round trips on the indexer.
	if calls.Load() > 1_000 {
		t.Fatalf("%d calls ran after the failure", calls.Load())
	}
}

func TestInFlightReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := inFlight(ctx, 32, 4, func(context.Context, int) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestInFlightHandlesAnEmptyBatch(t *testing.T) {
	if err := inFlight(t.Context(), 0, PinConcurrency, func(context.Context, int) error {
		t.Fatal("an empty batch ran work")
		return nil
	}); err != nil {
		t.Fatalf("inFlight: %v", err)
	}
}

// A batch whose first calls succeeded and whose later ones were cancelled still
// fails as a whole, and the work that succeeded stays done.
//
// This is the shape of an interrupted object pin, which is the expensive one:
// every pin that landed before the interrupt has put an object on a slab the
// account is already billed for, and the caller is told only that the batch
// failed. Nothing here can undo them, and nothing should: the caller knows
// what the batch was for, and this is why the write records its slabs before
// it starts pinning.
func TestInFlightFailsTheBatchAfterEarlierCallsSucceeded(t *testing.T) {
	const (
		total  = 320
		cutoff = 32
	)
	var pinned atomic.Int64

	err := inFlight(t.Context(), total, PinConcurrency, func(_ context.Context, i int) error {
		if pinned.Load() >= cutoff {
			// What the real call returns once the flush's context is cancelled.
			return fmt.Errorf("pin object %d of %d: %w", i+1, total, context.Canceled)
		}
		pinned.Add(1)
		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want the cancelled pins", err)
	}
	if done := pinned.Load(); done < cutoff || done >= total {
		t.Fatalf("%d of %d pins succeeded, want a batch that is part way through", done, total)
	}
	t.Logf("%d of %d objects were pinned and the batch failed", pinned.Load(), total)
}
