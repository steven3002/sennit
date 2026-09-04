package vault_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/steven3002/sennit/embed"
	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

const testPhrase = "abandon abandon abandon abandon abandon abandon " +
	"abandon abandon abandon abandon abandon about"

// offlineVault opens a vault with no network and no model.
//
// Almost everything here is about what the vault does with a vector rather than
// about which vector it is, that a record is searchable the moment it is
// written, that a supersession hides the record it replaced, that a restatement
// is recognised, that the index survives a restart. None of that is a property
// of the embedding model, and loading one for it costs three quarters of a
// gigabyte in a package that runs beside others.
func offlineVault(t *testing.T) *vault.Vault {
	t.Helper()
	return openVault(t, t.TempDir(), embedtest.NewStub())
}

func openVault(t *testing.T, home string, embedder embed.Vectorizer) *vault.Vault {
	t.Helper()
	v, err := vault.Open(context.Background(), vault.Options{
		Home:     home,
		Phrase:   testPhrase,
		Embedder: embedder,
		Offline:  true,
	})
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

// modelVault opens a vault with the real embedder, for the one measurement
// whose whole subject is what embedding costs.
//
// It takes the shared model slot, so this package and the recall regression
// never hold a model at the same time.
func modelVault(t *testing.T) *vault.Vault {
	t.Helper()
	embedder, release, err := embedtest.OpenModel(context.Background())
	if err != nil {
		t.Skipf("%v", err)
	}
	t.Cleanup(release)
	return openVault(t, t.TempDir(), embedder)
}

func write(t *testing.T, v *vault.Vault, n int, tags []string) vault.RememberResult {
	t.Helper()
	result, err := v.Remember(context.Background(), vault.RememberRequest{
		Statement: fmt.Sprintf("Tidepool's station %d reports every hour on the hour.", n),
		Context:   "From the fictional Tidepool field guide, used to exercise the write path.",
		Type:      record.TypeFact,
		Tags:      tags,
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	return result
}

// The interactivity budget, measured where the user actually waits.
//
// S2 measured the device write at 398 ms/record and attributed almost all of it
// to embedding. S3 adds two things to that path, a nearest-neighbour search
// over vectors already in memory, and a tag-frequency lookup, so the figure to
// report is what those cost on top.
func TestWritePathStaysInteractive(t *testing.T) {
	// The real model, because the whole claim is a ratio against what embedding
	// costs and a stub would put the numerator over nothing.
	v := modelVault(t)

	const records = 25
	var embedFor, sealFor, adviseFor, total time.Duration
	for i := range records {
		start := time.Now()
		result := write(t, v, i, []string{"tidepool", "stations"})
		total += time.Since(start)
		embedFor += result.EmbedFor
		sealFor += result.SealFor
		adviseFor += result.AdviseFor
	}

	mean := func(d time.Duration) time.Duration { return (d / records).Round(time.Microsecond) }
	t.Logf("device write over %d records: %v total = %v embed + %v seal + %v advise + the rest",
		records, mean(total), mean(embedFor), mean(sealFor), mean(adviseFor))
	t.Logf("advise is %.1f%% of the write", 100*float64(adviseFor)/float64(total))

	// The advice is only worth having if it is free next to the embedding it
	// sits behind. If it ever stops being, it belongs off the write path.
	if adviseFor > embedFor/4 {
		t.Errorf("neighbour search and tag advice took %v against %v of embedding; that is no longer free",
			mean(adviseFor), mean(embedFor))
	}
}

// Recall has to see a record the moment the write returns, and it has to still
// see it after the process that wrote it is gone.
func TestAWrittenRecordIsSearchableAndSurvivesAReopen(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	// One embedder across both opens, because the claim under test is that the
	// vectors outlive the vault and a second embedder would make it the weaker
	// claim that they can be recomputed.
	embedder := embedtest.NewStub()
	open := func() *vault.Vault { return openVault(t, home, embedder) }

	first := open()
	stored := write(t, first, 1, []string{"tidepool", "stations"})
	// Searchable before anything has been flushed or reopened.
	found, err := first.Recall(ctx, recallRequest("when does the station report"))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(found.Hits) == 0 || found.Hits[0].Memory.ID != stored.ID {
		t.Fatal("a record just written was not the first result for its own subject")
	}
	indexed := first.IndexHealth().Indexed
	first.Close()

	// The vault is gone; the vectors are not. This is the base-plus-delta claim.
	second := open()
	defer second.Close()
	if got := second.IndexHealth().Indexed; got != indexed {
		t.Fatalf("the reopened vault holds %d vector(s), the first held %d", got, indexed)
	}
	again, err := second.Recall(ctx, recallRequest("when does the station report"))
	if err != nil {
		t.Fatalf("recall after reopen: %v", err)
	}
	if len(again.Hits) == 0 || again.Hits[0].Memory.ID != stored.ID {
		t.Fatal("the record was not found after a reopen; the index did not survive")
	}
	if stats := second.VectorStats(); stats.BaseBytes+stats.DeltaBytes == 0 {
		t.Fatal("the persisted index is empty")
	}
}

// A supersession has to work through the whole vault, not only the ranker.
func TestSupersessionThroughTheVault(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	old, err := v.Remember(ctx, vault.RememberRequest{
		Statement: "Tidepool runs on three application servers in a single region.",
		Context:   "From the fictional Tidepool deployment topology document.",
		Type:      record.TypeFact,
		Tags:      []string{"tidepool", "deployment"},
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	replacement, err := v.Remember(ctx, vault.RememberRequest{
		Statement:  "Tidepool runs on five application servers across two regions.",
		Context:    "From the fictional Tidepool deployment topology document, after the second region.",
		Type:       record.TypeFact,
		Tags:       []string{"tidepool", "deployment"},
		Supersedes: &old.ID,
	})
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}

	current, err := v.Recall(ctx, recallRequest("how many application servers are there"))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	for _, hit := range current.Hits {
		if hit.Memory.ID == old.ID {
			t.Fatal("the superseded record came back as a current answer")
		}
	}
	if current.SupersededHidden != 1 {
		t.Fatalf("%d records were held back, want 1", current.SupersededHidden)
	}

	history := recallRequest("how many application servers are there")
	history.IncludeSuperseded = true
	past, err := v.Recall(ctx, history)
	if err != nil {
		t.Fatalf("recall history: %v", err)
	}
	var foundOld, foundNew bool
	for _, hit := range past.Hits {
		switch hit.Memory.ID {
		case old.ID:
			foundOld = true
		case replacement.ID:
			foundNew = true
		}
	}
	if !foundOld || !foundNew {
		t.Fatalf("history returned old=%v new=%v, want both", foundOld, foundNew)
	}

	// Superseding something the vault does not hold is a mistake, not a no-op:
	// it would hide a record that nothing had replaced.
	missing, _ := record.NewID()
	if _, err := v.Remember(ctx, vault.RememberRequest{
		Statement:  "A statement replacing nothing at all.",
		Context:    "Exercising the supersession guard.",
		Type:       record.TypeFact,
		Supersedes: &missing,
	}); err == nil {
		t.Fatal("a supersession naming an unknown record was accepted")
	}
}

// A record without context is refused at the boundary, so the largest single
// contributor to whether it can ever be found again cannot be skipped.
func TestTheVaultRefusesARecordWithoutContext(t *testing.T) {
	v := offlineVault(t)
	if _, err := v.Remember(context.Background(), vault.RememberRequest{
		Statement: "A statement with nothing to place it.",
		Type:      record.TypeFact,
		Tags:      []string{"tidepool"},
	}); err == nil {
		t.Fatal("a record without context was stored")
	}
}

// The write path tells the caller when a tag cannot narrow anything, which is
// the measured single-domain failure caught at the only moment it is cheap.
func TestTheWritePathReportsATagThatCannotNarrowAnything(t *testing.T) {
	v := offlineVault(t)

	// A vault where everything carries one tag is exactly the shape in which
	// filtering has nothing to prefer.
	var last vault.RememberResult
	for i := range 40 {
		last = write(t, v, i, []string{"tidepool"})
	}

	if last.Tags.Records < 30 {
		t.Fatalf("the vault holds %d records, too few for tag frequency to mean anything", last.Tags.Records)
	}
	if len(last.Tags.Tags) != 1 {
		t.Fatalf("advice covers %d tags, want 1", len(last.Tags.Tags))
	}
	if !last.Tags.Tags[0].TooCommon {
		t.Fatalf("a tag on %d of %d records was not reported as too common",
			last.Tags.Tags[0].Records, last.Tags.Records)
	}
	if !last.Tags.NeedsNarrowerTags() {
		t.Fatal("a record whose only tag is on the whole vault was not flagged")
	}

	// A specific tag alongside it is what the advice is asking for.
	better := write(t, v, 99, []string{"tidepool", "station-4417-firmware"})
	if better.Tags.NeedsNarrowerTags() {
		t.Fatal("a record carrying a specific tag was still flagged as unnarrowable")
	}
}

// Near-duplicates are surfaced so the caller can decide rather than blindly
// appending a second copy of something already stored.
func TestTheWritePathSurfacesANearDuplicate(t *testing.T) {
	v := offlineVault(t)
	ctx := context.Background()

	statement := "Tidepool rejects any tide reading above twenty metres at ingestion."
	context_ := "From the fictional Tidepool ingestion rules, added after a sensor fault."
	if _, err := v.Remember(ctx, vault.RememberRequest{
		Statement: statement, Context: context_, Type: record.TypeFact,
		Tags: []string{"tidepool", "ingestion"},
	}); err != nil {
		t.Fatalf("remember: %v", err)
	}

	again, err := v.Remember(ctx, vault.RememberRequest{
		Statement: statement, Context: context_, Type: record.TypeFact,
		Tags: []string{"tidepool", "ingestion"},
	})
	if err != nil {
		t.Fatalf("remember again: %v", err)
	}
	if len(again.Conflicts) == 0 {
		t.Fatalf("storing the same statement twice reported no conflict; neighbours were %+v", again.Neighbours)
	}
	if again.Conflicts[0].Statement != statement {
		t.Fatalf("the conflict names %q", again.Conflicts[0].Statement)
	}
}

// recallRequest is the default shape a caller asks with.
func recallRequest(query string) recall.Request {
	return recall.Request{Query: query, Limit: 5}
}
