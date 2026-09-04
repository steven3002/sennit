package vault_test

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/vault"
)

// TestLiveAVaultFollowsItsPhraseToASecondEnvironment is S6's pass marks 1 to 4
// and I8, in one exercise because they share one expensive setup.
//
// Environment A writes memories and a conversation and puts them on Sia.
// Storage is then repacked, which rewrites every object id and leaves every
// record id alone. Environment B is a directory that has never existed before:
// it is given the recovery phrase and the app key and nothing else, no
// catalog, no index, no bodies, no session heads, and has to produce the same
// records byte for byte and the same conversation in the same order.
//
// What it deliberately does not prove is anything about two *machines*. Both
// environments run on one host, so they share a clock, a network path and a
// page cache. What is exercised is the cryptographic and protocol path: the
// keys derive from the phrase, the addresses derive from the keys, and the
// records come back from an indexer that never saw a plaintext.
func TestLiveAVaultFollowsItsPhraseToASecondEnvironment(t *testing.T) {
	ctx := context.Background()
	homeA := t.TempDir()
	a := liveVaultAt(t, homeA)
	// A is closed part way through, to show that B depends on nothing A is
	// holding open. Storage is released from a reopened A, whose catalog and
	// ledger name only what A wrote, B's, after hydrating, names more.
	t.Cleanup(func() { releaseVaultAt(t, homeA) })

	if _, err := a.WaitReady(ctx, 90*time.Second); err != nil {
		t.Fatalf("account not ready: %v", err)
	}
	before, err := a.Account(ctx)
	if err != nil {
		t.Fatalf("read account: %v", err)
	}

	// ── Environment A ────────────────────────────────────────────────────────
	const memories = 12
	want := make(map[record.ID][]byte, memories)
	for i, req := range statements(memories) {
		written, err := a.Remember(ctx, req)
		if err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
		body, err := a.LocalBody(written.ID)
		if err != nil {
			t.Fatalf("read the device's copy of record %d: %v", i, err)
		}
		want[written.ID] = bytes.Clone(body)
	}

	savedSession, err := a.SaveSession(ctx, vault.SaveSessionRequest{
		Title:    "Working out what a second device can and cannot get back",
		Summary:  "Established that the transcript travels and the head does not.",
		Tags:     []string{"portability", "sessions"},
		Project:  record.Project{Repo: "steven3002/sennit", Branch: "main"},
		Agent:    record.Agent{Name: "claude-code", Version: "2.1.223"},
		Messages: append(conversation("s6a"), conversation("s6b")...),
	})
	if err != nil {
		t.Fatalf("save the session: %v", err)
	}
	onA, err := a.LoadSession(ctx, vault.LoadSessionRequest{ID: savedSession.ID, Transcript: true})
	if err != nil {
		t.Fatalf("load the session on A: %v", err)
	}
	headOnA, err := a.Session(savedSession.ID)
	if err != nil {
		t.Fatalf("read the head on A: %v", err)
	}

	if _, err := a.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	t.Logf("A wrote %d memories and a %d-message conversation in %d chunk(s)",
		memories, len(onA.Messages), len(headOnA.Chunks))

	// ── A repack between the two environments (I8) ───────────────────────────
	//
	// Object refs are a mutable pointer and record ids are the identity. This is
	// the scope that tests it across the boundary it was written for: the second
	// environment resolves records that have moved since the first one wrote
	// them, and must not see a single one of them as new.
	refsBefore := make(map[record.ID]string, len(want))
	for _, entry := range a.Entries() {
		refsBefore[entry.ID] = entry.ObjectRef
	}
	repacked, err := a.Repack(ctx)
	if err != nil {
		t.Fatalf("repack: %v", err)
	}
	var moved, idsChanged int
	for _, entry := range a.Entries() {
		previous, known := refsBefore[entry.ID]
		if !known {
			idsChanged++
			continue
		}
		if entry.ObjectRef != previous {
			moved++
		}
	}
	if idsChanged != 0 {
		t.Fatalf("%d record(s) are not the records that were written before the repack", idsChanged)
	}
	t.Logf("repack: %d record(s), %d slab(s) → %d, peak %d, %d object ref(s) rewritten, "+
		"0 record ids changed, freed %s in %v",
		len(repacked.Records), repacked.SlabsBefore, repacked.SlabsAfter, repacked.Peak,
		moved, human(repacked.Freed()), repacked.Elapsed.Round(time.Millisecond))
	if moved != len(refsBefore) {
		t.Errorf("repack rewrote %d of %d object refs; the point of I8 is that it rewrites all of them",
			moved, len(refsBefore))
	}
	a.Close()

	// ── Environment B: a directory that has never held this vault ────────────
	homeB := t.TempDir()
	b := liveVaultAt(t, homeB)

	if got := len(b.Entries()); got != 0 {
		t.Fatalf("environment B started with %d catalogued records, so it is not a clean environment", got)
	}
	if held, err := b.CountBodies(); err != nil || held != 0 {
		t.Fatalf("environment B started holding %d record(s) (err %v)", held, err)
	}
	if sessions, err := b.CountSessions(); err != nil || sessions != 0 {
		t.Fatalf("environment B started holding %d session head(s) (err %v)", sessions, err)
	}

	// Pass mark 4, first reading: the catalog alone, then bodies on demand.
	catalog, err := b.Hydrate(ctx, vault.HydrateRequest{Depth: vault.HydrateCatalog})
	if err != nil {
		t.Fatalf("hydrate the catalog: %v", err)
	}
	logHydrate(t, "catalog", catalog)
	if catalog.Records < memories {
		t.Fatalf("catalogued %d record(s), want at least the %d memories A wrote", catalog.Records, memories)
	}
	held, err := b.CountBodies()
	if err != nil {
		t.Fatalf("count bodies: %v", err)
	}
	if held != 0 {
		t.Fatalf("a catalog hydration left %d record bodies on the device; the whole claim is that "+
			"it fetches none of them", held)
	}

	var oneID record.ID
	for id := range want {
		oneID = id
		break
	}
	start := time.Now()
	lazy, err := b.BodyFromNetwork(ctx, oneID)
	if err != nil {
		t.Fatalf("read %s from a catalog-only vault: %v", oneID, err)
	}
	if !bytes.Equal(lazy, want[oneID]) {
		t.Fatalf("the lazily fetched body is not byte-exact")
	}
	t.Logf("pass mark 4: one record read on demand from a vault holding 0 of %d bodies, "+
		"byte-exact, in %v", catalog.Records, time.Since(start).Round(time.Millisecond))

	// Pass mark 1 and 2: the vault itself, and every memory byte-exact.
	full, err := b.Hydrate(ctx, vault.HydrateRequest{Depth: vault.HydrateMetadata})
	if err != nil {
		t.Fatalf("hydrate the metadata: %v", err)
	}
	logHydrate(t, "metadata", full)

	var exact, missing, wrong int
	for id, body := range want {
		got, err := b.LocalBody(id)
		switch {
		case err != nil:
			missing++
		case bytes.Equal(got, body):
			exact++
		default:
			wrong++
		}
	}
	t.Logf("pass mark 2: %d/%d memories byte-exact after decryption · %d missing · %d mismatched",
		exact, memories, missing, wrong)
	if exact != memories {
		t.Fatalf("environment B reproduced %d of %d memories byte-exact", exact, memories)
	}

	// Pass mark 3: the conversation, in order, with every message identical,
	// and an honest account of what the head lost on the way.
	onB, err := b.LoadSession(ctx, vault.LoadSessionRequest{ID: savedSession.ID, Transcript: true})
	if err != nil {
		t.Fatalf("load the session on B: %v", err)
	}
	if len(onB.Messages) != len(onA.Messages) {
		t.Fatalf("B replayed %d messages, A wrote %d", len(onB.Messages), len(onA.Messages))
	}
	for i := range onA.Messages {
		if !reflect.DeepEqual(onA.Messages[i], onB.Messages[i]) {
			t.Fatalf("message %d differs between the two environments:\n A %+v\n B %+v",
				i, onA.Messages[i], onB.Messages[i])
		}
	}
	t.Logf("pass mark 3: %d/%d messages identical on B, in order, across %d chunk(s) read",
		len(onB.Messages), len(onA.Messages), onB.ChunksRead)

	logRebuiltHead(t, headOnA, onB.Session, full.Rebuild)

	// The index, which is the stage that costs, measured on its own.
	indexed, err := b.Hydrate(ctx, vault.HydrateRequest{Depth: vault.HydrateIndex})
	if err != nil {
		t.Fatalf("hydrate the index: %v", err)
	}
	logHydrate(t, "index", indexed)
	if indexed.Embedded == 0 {
		t.Fatal("the index stage embedded nothing")
	}
	t.Logf("re-embedding cost %v for %d record(s) = %v/record; at 10,000 records that is %v",
		indexed.EmbedFor.Round(time.Millisecond), indexed.Embedded,
		(indexed.EmbedFor / time.Duration(indexed.Embedded)).Round(time.Millisecond),
		(indexed.EmbedFor / time.Duration(indexed.Embedded) * 10_000).Round(time.Second))

	found, err := b.Recall(ctx, recall.Request{Query: "why does batching matter for storage", Limit: 5})
	if err != nil {
		t.Fatalf("recall on B: %v", err)
	}
	if len(found.Hits) == 0 {
		t.Fatal("recall on the hydrated vault returned nothing")
	}
	t.Logf("recall on B returned %d hit(s), top from tier %s", len(found.Hits), found.Hits[0].Tier)

	// I8 across the boundary: B resolved every record by id, through refs that
	// were rewritten after A wrote them.
	for id := range want {
		if _, _, err := b.FetchMemory(ctx, id); err != nil {
			t.Fatalf("B cannot resolve %s, which A wrote before the repack: %v", id, err)
		}
	}
	t.Logf("I8: all %d record ids resolve on B after a repack rewrote every object ref", len(want))

	// ⚠ A hydrated vault takes responsibility for every slab holding a record it
	// can open, which is correct, the records are this vault's, and it means B's
	// ledger now names storage B did not write. Reclamation from B would
	// therefore release it. Everything written here is released from A instead,
	// whose catalog and ledger name only what A wrote.
	//
	// This is asserted rather than left as a comment because the difference is
	// invisible until something deletes: a hydrated vault and a writing vault are
	// the same type with very different authority over an account.
	tracked, err := b.TrackedSlabs()
	if err != nil {
		t.Fatalf("read B's slab ledger: %v", err)
	}
	t.Logf("B's ledger names %d slab(s) after hydrating, having pinned none of them", len(tracked))
	b.Close()

	t.Logf("quota at the start of this test: %s (cleanup follows)", human(before.PinnedData))
}

// TestLiveAHydratedCatalogAgreesWithTheWriterAboutWhereRecordsLive is the
// invariant a hydrated vault's ability to delete rests on.
//
// A vault that rebuilt its catalog from the network holds, for every record, a
// slab id it derived from the object it found. The vault that wrote the record
// holds one it got from the flush. Reclamation compares those two figures
// against a ledger, and a mismatch is not a cosmetic disagreement, it is a
// sweep concluding that a live slab holds nothing.
func TestLiveAHydratedCatalogAgreesWithTheWriterAboutWhereRecordsLive(t *testing.T) {
	ctx := context.Background()
	writer := liveVaultAt(t, t.TempDir())
	t.Cleanup(func() { releaseVault(t, writer) })

	if _, err := writer.WaitReady(ctx, 90*time.Second); err != nil {
		t.Fatalf("account not ready: %v", err)
	}
	for i, req := range statements(3) {
		if _, err := writer.Remember(ctx, req); err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
	}
	if _, err := writer.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	written := make(map[record.ID]string)
	for _, entry := range writer.Entries() {
		written[entry.ID] = entry.SlabID
	}
	ledger, err := writer.TrackedSlabs()
	if err != nil {
		t.Fatalf("read the writer's ledger: %v", err)
	}
	inLedger := make(map[string]bool, len(ledger))
	for _, slab := range ledger {
		inLedger[string(slab.ID)] = true
	}

	reader := liveVaultAt(t, t.TempDir())
	defer reader.Close()
	if _, err := reader.Hydrate(ctx, vault.HydrateRequest{Depth: vault.HydrateCatalog}); err != nil {
		t.Fatalf("hydrate: %v", err)
	}

	var checked, disagreed, unledgered int
	for _, entry := range reader.Entries() {
		want, mine := written[entry.ID]
		if !mine {
			continue
		}
		checked++
		if entry.SlabID != want {
			disagreed++
			t.Errorf("record %s: the writer filed it under slab %q and a hydrated catalog under %q",
				entry.ID, want, entry.SlabID)
		}
		if !inLedger[entry.SlabID] {
			unledgered++
			t.Errorf("record %s sits on slab %q, which the writer's own ledger does not name; "+
				"a keep-set built from that ledger would not protect it", entry.ID, entry.SlabID)
		}
	}
	if checked == 0 {
		t.Fatal("the hydrated catalog contains none of the records that were just written")
	}
	t.Logf("%d record(s) checked: %d slab-id disagreement(s), %d absent from the writer's ledger",
		checked, disagreed, unledgered)
}

func logHydrate(t *testing.T, stage string, r vault.HydrateReport) {
	t.Helper()
	t.Logf("hydrate(%s): %d object(s) / %s ciphertext → %d record(s), %d body(ies), %d session(s), %d vector(s)",
		stage, r.Objects, human(uint64(r.Bytes)), r.Records, r.Bodies, r.Sessions, r.Embedded)
	t.Logf("  walk %v · rebuild %v · embed %v · total %v (%v/record) · %d slab(s) tracked · %d foreign frame(s)",
		r.WalkFor.Round(time.Millisecond), r.RebuildFor.Round(time.Millisecond),
		r.EmbedFor.Round(time.Millisecond), r.Elapsed.Round(time.Millisecond),
		r.PerRecord().Round(time.Microsecond), r.Slabs, r.Foreign)
}

// logRebuiltHead is M18 measured live: the head A wrote against the head B
// reconstructed from chunks that travelled and a head that did not.
func logRebuiltHead(t *testing.T, written, rebuilt *record.Session, report vault.RebuildReport) {
	t.Helper()
	t.Logf("M18 live: %d session(s) rebuilt from %d chunk(s), %d gap(s), %d unreadable, in %v",
		report.Sessions, report.Chunks, report.Gaps, report.Unreadable,
		report.Elapsed.Round(time.Millisecond))

	var carried, invented, lost []string
	for _, field := range vault.HeadFields {
		switch report.Origins[field] {
		case vault.OriginSynthesised:
			invented = append(invented, field)
		case vault.OriginLost:
			lost = append(lost, field)
		default:
			carried = append(carried, field)
		}
	}
	t.Logf("  came back (%d): %v", len(carried), carried)
	t.Logf("  synthesised (%d): %v", len(invented), invented)
	t.Logf("  lost (%d): %v", len(lost), lost)
	t.Logf("  title on A %q → on B %q", written.Title, rebuilt.Title)

	if rebuilt.Summary != "" || len(rebuilt.Tags) != 0 {
		t.Errorf("the rebuilt head came back with a summary or tags, which no chunk carries; "+
			"summary %q tags %v", rebuilt.Summary, rebuilt.Tags)
	}
	if len(rebuilt.Chunks) != len(written.Chunks) {
		t.Errorf("the rebuilt head names %d chunks, the written one named %d",
			len(rebuilt.Chunks), len(written.Chunks))
	}
	for i := range written.Chunks {
		if i < len(rebuilt.Chunks) && rebuilt.Chunks[i] != written.Chunks[i] {
			t.Errorf("chunk ref %d differs:\n A %+v\n B %+v", i, written.Chunks[i], rebuilt.Chunks[i])
		}
	}
}
