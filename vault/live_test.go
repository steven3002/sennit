package vault_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// LiveEnv opts a run in to writing to a real indexer. It is off by default so
// the ordinary test run touches no network and spends no storage quota.
const LiveEnv = "SENNIT_LIVE"

func liveVault(t *testing.T) *vault.Vault {
	t.Helper()
	if os.Getenv(LiveEnv) == "" {
		t.Skipf("set %s=1 to run against a real indexer", LiveEnv)
	}
	phrase, err := keys.ReadPhrase(nil)
	if err != nil {
		t.Fatalf("recovery phrase: %v", err)
	}
	appKey, err := keys.AppKeyFromEnv()
	if err != nil {
		t.Fatalf("app key: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)

	v, err := vault.Open(ctx, vault.Options{
		Home:    t.TempDir(),
		Phrase:  phrase,
		AppKey:  appKey,
		Indexer: vault.DefaultIndexer(),
	})
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

// TestLiveRoundTrip is the end-to-end claim: a memory written through the
// vault reaches Sia, comes back byte for byte, and is unreadable in between.
func TestLiveRoundTrip(t *testing.T) {
	v := liveVault(t)
	ctx := context.Background()

	const statement = "Repack rewrites every object id, so nothing may key on one as an identity."
	const recordContext = "Recorded while proving the walking skeleton against a live indexer."

	written, err := v.Remember(ctx, vault.RememberRequest{
		Statement: statement,
		Context:   recordContext,
		Type:      record.TypeFact,
		Tags:      []string{"sia", "storage"},
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	// Remember returns once the record is durable on this device. Reaching the
	// network is a separate, later event under the standing flush cadence, and
	// the result says which of the two has happened.
	if written.OnNetwork {
		t.Fatal("a single record flushed immediately; the standing cadence should have held it")
	}
	if v.Pending() != 1 {
		t.Fatalf("%d records are queued, want 1", v.Pending())
	}
	t.Logf("id=%s cid=%s embed=%v seal=%v", written.ID, written.CID, written.EmbedFor, written.SealFor)

	start := time.Now()
	flushed, err := v.Flush(ctx)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if flushed == nil {
		t.Fatal("the flush wrote nothing")
	}
	t.Logf("flushed %d byte(s) in %d object(s) over %d slab(s) in %v (upload %v, pin slabs %v, pin objects %v)",
		flushed.Bytes(), len(flushed.Written), len(flushed.Slabs), time.Since(start),
		flushed.UploadFor, flushed.PinSlabsFor, flushed.PinObjectFor)

	local, err := v.LocalBody(written.ID)
	if err != nil {
		t.Fatalf("read the device's copy: %v", err)
	}
	fetched, err := v.BodyFromNetwork(ctx, written.ID)
	if err != nil {
		t.Fatalf("read from the network: %v", err)
	}
	if !bytes.Equal(local, fetched) {
		t.Fatalf("round trip is not byte-exact: %d bytes stored, %d recovered", len(local), len(fetched))
	}

	// What sits on Sia must be ciphertext. The SDK's own payload cipher is
	// unauthenticated, so this checks the vault's own envelope, not the SDK's.
	stored, err := v.StoredBytes(ctx, written.ID)
	if err != nil {
		t.Fatalf("read the stored bytes: %v", err)
	}
	for _, plaintext := range []string{statement, recordContext, "statement", "sia", "storage", "fact", "memory"} {
		if bytes.Contains(stored, []byte(plaintext)) {
			t.Fatalf("the stored object holds %q in the clear", plaintext)
		}
	}
	t.Logf("%d byte record stored as %d bytes; no plaintext present", len(local), len(stored))
}

// TestLiveRecallIsSemantic checks that a query sharing no words with a record
// still finds it ahead of unrelated records, which is what separates this from
// a substring search.
func TestLiveRecallIsSemantic(t *testing.T) {
	v := liveVault(t)
	ctx := context.Background()

	const target = "Rotating the app key invalidates every sealed object, so a rotation needs a re-seal migration."
	corpus := []vault.RememberRequest{
		{
			Statement: target,
			Context:   "Noted while working out the key hierarchy.",
			Type:      record.TypeInsight,
			Tags:      []string{"keys"},
		},
		{
			Statement: "Session transcripts are chunked at 256 KiB because that is the download chunk size.",
			Context:   "Settled during the sessions design.",
			Type:      record.TypeFact,
			Tags:      []string{"sessions"},
		},
		{
			Statement: "The weekly grant check-in is on Thursday and needs the latency numbers to be real.",
			Context:   "Standing commitment for the funding milestone.",
			Type:      record.TypeProfile,
			Tags:      []string{"grant"},
		},
		{
			Statement: "Test fixtures live under testdata and are regenerated rather than edited by hand.",
			Context:   "Convention agreed for the repository.",
			Type:      record.TypePreference,
			Tags:      []string{"testing"},
		},
		{
			Statement: "A partially filled slab can never be extended, so every flush mints a new one.",
			Context:   "Measured across eight live experiments.",
			Type:      record.TypeFact,
			Tags:      []string{"storage"},
		},
	}

	var targetID record.ID
	for n, req := range corpus {
		written, err := v.Remember(ctx, req)
		if err != nil {
			t.Fatalf("remember %d: %v", n, err)
		}
		if n == 0 {
			targetID = written.ID
		}
	}
	// One flush for the whole batch: packed records share a slab, and the slab
	// is what is billed.
	flushed, err := v.Flush(ctx)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if flushed == nil {
		t.Fatal("the flush wrote nothing")
	}
	if len(flushed.Written) != len(corpus) {
		t.Fatalf("flush wrote %d objects for %d records", len(flushed.Written), len(corpus))
	}
	if len(flushed.Slabs) != 1 {
		t.Fatalf("%d records occupied %d slabs, want 1", len(corpus), len(flushed.Slabs))
	}
	t.Logf("%d records packed into %d slab: %d bytes, upload %v, pin slabs %v, pin objects %v",
		len(corpus), len(flushed.Slabs), flushed.Bytes(),
		flushed.UploadFor, flushed.PinSlabsFor, flushed.PinObjectFor)

	const query = "what happens to old data if we change credentials"
	for _, word := range []string{"rotat", "app key", "seal", "migration", "invalid"} {
		if strings.Contains(query, word) {
			t.Fatalf("the query shares %q with the record, which makes this a substring test", word)
		}
	}

	// The bar is the returned set, not the first position. A dense pass over
	// statement and context is measured to place the right record in the top
	// few far more reliably than it places it first, and asserting rank one
	// would be asserting something this pipeline is known not to deliver.
	const limit = 3
	result, err := v.Recall(ctx, recall.Request{Query: query, Limit: limit})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.Hits) == 0 {
		t.Fatal("recall returned nothing")
	}

	rank := -1
	for n, hit := range result.Hits {
		t.Logf("  %d. %.4f  %s", n+1, hit.Score, hit.Memory.Statement)
		if hit.Memory.ID == targetID {
			rank = n + 1
		}
	}
	if rank < 0 {
		t.Fatalf("the target record is not in the top %d for %q", limit, query)
	}
	t.Logf("target ranked %d of %d against %d vectors; embed=%v search=%v fetch=%v",
		rank, limit, result.Searched, result.EmbedFor, result.SearchFor, result.FetchFor)
}
