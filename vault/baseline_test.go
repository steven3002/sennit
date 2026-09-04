package vault_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// TestLiveBaseline records what each stage of a write and a read costs, so a
// later change can be compared against a number rather than an impression.
func TestLiveBaseline(t *testing.T) {
	v := liveVault(t)
	ctx := context.Background()

	const samples = 10
	var embedMS, sealMS, localMS []float64

	for n := range samples {
		written, err := v.Remember(ctx, vault.RememberRequest{
			Statement: fmt.Sprintf("Baseline record %d: the packer budgets in ciphertext bytes, including the per-record envelope.", n),
			Context:   "Written by the walking skeleton's baseline measurement.",
			Type:      record.TypeFact,
			Tags:      []string{"baseline"},
		})
		if err != nil {
			t.Fatalf("remember %d: %v", n, err)
		}
		embedMS = append(embedMS, ms(written.EmbedFor))
		sealMS = append(sealMS, ms(written.SealFor))

		start := time.Now()
		if _, err := v.LocalBody(written.ID); err != nil {
			t.Fatalf("read the device's copy: %v", err)
		}
		localMS = append(localMS, ms(time.Since(start)))
	}

	start := time.Now()
	flushed, err := v.Flush(ctx)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if flushed == nil {
		t.Fatal("the flush wrote nothing")
	}
	flushFor := time.Since(start)

	var searchMS, queryEmbedMS, networkMS []float64
	queries := []string{
		"how is storage billed",
		"what does the write path cost",
		"when does data leave the device",
		"how are records protected",
		"what makes a record findable",
	}
	for _, query := range queries {
		result, err := v.Recall(ctx, recall.Request{Query: query, Limit: 5})
		if err != nil {
			t.Fatalf("recall %q: %v", query, err)
		}
		queryEmbedMS = append(queryEmbedMS, ms(result.EmbedFor))
		searchMS = append(searchMS, ms(result.SearchFor))

		start := time.Now()
		if _, err := v.BodyFromNetwork(ctx, result.Hits[0].Memory.ID); err != nil {
			t.Fatalf("read from the network: %v", err)
		}
		networkMS = append(networkMS, ms(time.Since(start)))
	}

	t.Logf("write path over %d records", samples)
	report(t, "  embed (statement+context)", embedMS)
	report(t, "  seal (zstd + AEAD + frame)", sealMS)
	report(t, "  read back from this device", localMS)
	t.Logf("  flush of %d records: %v total, upload %v, pin slabs %v, pin objects %v, %d bytes over %d slab(s)",
		len(flushed.Written), flushFor, flushed.UploadFor, flushed.PinSlabsFor, flushed.PinObjectFor,
		flushed.Bytes(), len(flushed.Slabs))
	t.Logf("  pin objects works out at %.0f ms per record", ms(flushed.PinObjectFor)/float64(len(flushed.Written)))

	t.Logf("read path over %d queries against %d vectors", len(queries), samples)
	report(t, "  embed (query)", queryEmbedMS)
	report(t, "  search (cosine over the matrix)", searchMS)
	report(t, "  fetch from the network + decrypt", networkMS)
}

func report(t *testing.T, label string, samples []float64) {
	t.Helper()
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	var total float64
	for _, v := range sorted {
		total += v
	}
	t.Logf("%-34s n=%2d  min %8.2f ms  p50 %8.2f ms  p95 %8.2f ms  max %8.2f ms  mean %8.2f ms",
		label, len(sorted), sorted[0], percentile(sorted, 0.50), percentile(sorted, 0.95),
		sorted[len(sorted)-1], total/float64(len(sorted)))
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	return sorted[i]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
