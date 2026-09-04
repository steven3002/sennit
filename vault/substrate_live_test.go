package vault_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/recall"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/vault"
)

// liveVaultAt opens a live vault in a caller-chosen directory, so a test can
// close one and reopen the same files as a second process would find them.
func liveVaultAt(t *testing.T, home string) *vault.Vault {
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
	v, err := vault.Open(context.Background(), vault.Options{
		Home:    home,
		Phrase:  phrase,
		AppKey:  appKey,
		Indexer: vault.DefaultIndexer(),
	})
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	// Registered before any release, so it runs after one: cleanups unwind in
	// reverse, and releasing storage needs the vault still open.
	t.Cleanup(func() { v.Close() })
	return v
}

// releaseVaultAt reopens a vault directory and releases what it holds.
//
// It exists for tests that close a vault before they finish. A cleanup that
// captured the closed handle would panic on a nil catalog and never reach the
// reclamation, which turns a test bug into a slab that bills forever.
func releaseVaultAt(t *testing.T, home string) {
	t.Helper()
	phrase, err := keys.ReadPhrase(nil)
	if err != nil {
		t.Errorf("recovery phrase: %v", err)
		return
	}
	appKey, err := keys.AppKeyFromEnv()
	if err != nil {
		t.Errorf("app key: %v", err)
		return
	}
	v, err := vault.Open(context.Background(), vault.Options{
		Home:    home,
		Phrase:  phrase,
		AppKey:  appKey,
		Indexer: vault.DefaultIndexer(),
	})
	if err != nil {
		t.Errorf("reopen %s to release its storage: %v", home, err)
		return
	}
	defer v.Close()
	releaseVault(t, v)
}

// releaseVault returns everything a test wrote. It runs whatever happened, so
// a failure costs a report rather than a slab left billing forever.
func releaseVault(t *testing.T, v *vault.Vault) {
	t.Helper()
	for _, entry := range v.Entries() {
		if err := v.Forget(entry.ID); err != nil {
			t.Errorf("forget %s: %v", entry.ID, err)
			return
		}
	}
	sweep, err := v.Reclaim(context.Background(), vault.ReclaimOptions{})
	if err != nil {
		t.Errorf("reclaim: %v", err)
		return
	}
	t.Logf("cleanup: released %d slab(s) and %d object(s), freed %s",
		sweep.SlabsReleased, sweep.ObjectsDeleted, human(sweep.Freed()))
}

func statements(n int) []vault.RememberRequest {
	out := make([]vault.RememberRequest, n)
	for i := range out {
		out[i] = vault.RememberRequest{
			Statement: fmt.Sprintf(
				"Record %d: a slab is billed whole and can never be extended, so batching is a correctness concern.", i),
			Context: fmt.Sprintf(
				"Written as record %d of a packed batch, to measure what a full slab's worth of records costs.", i),
			Type: record.TypeFact,
			Tags: []string{"substrate", fmt.Sprintf("batch-%d", i%8)},
		}
	}
	return out
}

func human(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Pass mark 1 and 2, and the write-path measurement.
//
// A thousand records must occupy one slab, the billed data must match the
// measured quantum, and pulling one record back out of that shared slab must
// be byte-exact. The pinning figure is the one S2 exists to move: S1 pinned
// objects one at a time at 163 ms each.
func TestLiveThousandRecordsOccupyOneSlab(t *testing.T) {
	v := liveVaultAt(t, t.TempDir())
	ctx := context.Background()

	if _, err := v.WaitReady(ctx, 90*time.Second); err != nil {
		t.Fatalf("account not ready: %v", err)
	}
	before, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	t.Cleanup(func() { releaseVault(t, v) })

	const count = 1000
	corpus := statements(count)
	ids := make([]record.ID, count)

	writeStart := time.Now()
	for i, req := range corpus {
		written, err := v.Remember(ctx, req)
		if err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
		if written.OnNetwork {
			t.Fatalf("record %d flushed on its own; the standing cadence should have held it", i)
		}
		ids[i] = written.ID
	}
	localFor := time.Since(writeStart)

	if v.Pending() != count {
		t.Fatalf("%d records queued, want %d", v.Pending(), count)
	}
	queuedBytes := v.PendingBytes()

	flushStart := time.Now()
	flushed, err := v.Flush(ctx)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	flushFor := time.Since(flushStart)
	if flushed == nil {
		t.Fatal("the flush wrote nothing")
	}

	if len(flushed.Written) != count {
		t.Fatalf("flush wrote %d objects for %d records", len(flushed.Written), count)
	}
	if len(flushed.Slabs) != 1 {
		t.Fatalf("%d records occupied %d slabs, want 1", count, len(flushed.Slabs))
	}

	after, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	billed := after.PinnedData - before.PinnedData
	slabBytes, err := v.SlabPayloadSize()
	if err != nil {
		t.Fatalf("slab size: %v", err)
	}

	perPin := flushed.PinObjectFor / time.Duration(count)
	t.Logf("%d records: %s queued (ciphertext, envelope and framing included), %s written",
		count, human(uint64(queuedBytes)), human(uint64(flushed.Bytes())))
	t.Logf("device writes %v total (%v each); flush %v = upload %v + pin slabs %v + pin objects %v",
		localFor, localFor/count, flushFor, flushed.UploadFor, flushed.PinSlabsFor, flushed.PinObjectFor)
	t.Logf("object pinning: %v per record at c=%d, against 163 ms sequential; %.1f records/s",
		perPin, sia.PinConcurrency, float64(count)/flushed.PinObjectFor.Seconds())
	t.Logf("billed %s for %s of payload (%.0fx); one slab is %s",
		human(billed), human(uint64(flushed.Bytes())), float64(billed)/float64(flushed.Bytes()), human(uint64(slabBytes)))

	if billed != uint64(slabBytes) {
		t.Fatalf("billed %d bytes, want exactly one slab of %d", billed, slabBytes)
	}
	if perPin >= 163*time.Millisecond {
		t.Fatalf("object pinning is %v per record, no better than the sequential baseline of 163 ms", perPin)
	}

	// Pass mark 2: one record out of the shared slab, read from the network
	// rather than from this device, byte for byte.
	for _, i := range []int{0, count / 2, count - 1} {
		local, err := v.LocalBody(ids[i])
		if err != nil {
			t.Fatalf("read the device's copy of record %d: %v", i, err)
		}
		fetched, err := v.BodyFromNetwork(ctx, ids[i])
		if err != nil {
			t.Fatalf("read record %d from the network: %v", i, err)
		}
		if !bytes.Equal(local, fetched) {
			t.Fatalf("record %d is not byte-exact out of the shared slab", i)
		}
	}
	t.Logf("selective reads out of the shared slab are byte-exact at positions 0, %d and %d",
		count/2, count-1)

	stats := v.ManifestStats()
	t.Logf("catalog: %s snapshot + %s log after %d records, %d compaction(s), %s ever written",
		human(uint64(stats.SnapshotBytes)), human(uint64(stats.LogBytes)),
		stats.Records, stats.Compactions, human(uint64(stats.Written)))
}

// Pass mark 3: the catalog is a performance index, not a correctness
// requirement. Delete it, and the vault must come back from the phrase and the
// indexer alone.
func TestLiveRecoveryWithoutTheManifest(t *testing.T) {
	home := t.TempDir()
	v := liveVaultAt(t, home)
	ctx := context.Background()

	if _, err := v.WaitReady(ctx, 90*time.Second); err != nil {
		t.Fatalf("account not ready: %v", err)
	}

	const count = 120
	want := make(map[record.ID][]byte, count)
	for i, req := range statements(count) {
		written, err := v.Remember(ctx, req)
		if err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
		body, err := v.LocalBody(written.ID)
		if err != nil {
			t.Fatalf("read the device's copy of record %d: %v", i, err)
		}
		want[written.ID] = bytes.Clone(body)
	}
	if _, err := v.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	v.Close()

	// Everything this device knew: the catalog and the working copy. What is
	// left is the recovery phrase and whatever the indexer holds.
	for _, name := range []string{manifest.LogName, manifest.SnapshotName} {
		path := filepath.Join(home, name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("delete %s: %v", name, err)
		}
		t.Logf("deleted %s", name)
	}
	rebuilt := t.TempDir()

	second := liveVaultAt(t, rebuilt)
	t.Cleanup(func() { releaseVault(t, second) })

	if got := len(second.Entries()); got != 0 {
		t.Fatalf("the rebuilt vault started with %d catalogued records, so this is not a recovery", got)
	}

	report, err := second.Recover(ctx, vault.RecoveryRequest{Embed: false})
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	t.Logf("walked %d object(s), parsed %d frame(s), recovered %d record(s) in %v",
		report.Objects, report.Frames, report.Recovered, report.Elapsed)
	t.Logf("skipped %d frame(s) this phrase does not open, %d damaged object(s), %d unreadable",
		report.Foreign, report.Damaged, report.Unreadable)

	var exact, missing, wrong int
	for id, body := range want {
		recovered, err := second.LocalBody(id)
		switch {
		case err != nil:
			missing++
		case bytes.Equal(recovered, body):
			exact++
		default:
			wrong++
		}
	}
	t.Logf("recovered byte-exact %d/%d · missing %d · mismatched %d", exact, count, missing, wrong)
	if exact != count {
		t.Fatalf("recovery rebuilt %d of %d records byte-exact", exact, count)
	}

	// The rebuilt catalog has to be usable, not merely present: a record must
	// resolve to a location the vault can read from.
	for id := range want {
		if _, err := second.BodyFromNetwork(ctx, id); err != nil {
			t.Fatalf("the rebuilt catalog cannot locate %s: %v", id, err)
		}
		break
	}
	t.Log("the rebuilt catalog resolves records to locations that read back")
}

// Pass mark 5: reclaim frees exactly what it should and nothing that is still
// live, including the case that makes it interesting, a slab shared between
// records that are being dropped and records that are not.
func TestLiveReclaimFreesExactlyTheDeadSlabs(t *testing.T) {
	v := liveVaultAt(t, t.TempDir())
	ctx := context.Background()

	if _, err := v.WaitReady(ctx, 90*time.Second); err != nil {
		t.Fatalf("account not ready: %v", err)
	}
	slabBytes, err := v.SlabPayloadSize()
	if err != nil {
		t.Fatalf("slab size: %v", err)
	}
	baseline, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	t.Cleanup(func() { releaseVault(t, v) })

	// Two flushes, so there are two slabs: one that will keep a live record and
	// one that will not.
	const perFlush = 20
	kept := make([]record.ID, 0, perFlush)
	corpus := statements(perFlush * 2)
	for i, req := range corpus[:perFlush] {
		written, err := v.Remember(ctx, req)
		if err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
		kept = append(kept, written.ID)
	}
	first, err := v.Flush(ctx)
	if err != nil {
		t.Fatalf("first flush: %v", err)
	}
	for i, req := range corpus[perFlush:] {
		if _, err := v.Remember(ctx, req); err != nil {
			t.Fatalf("remember %d: %v", perFlush+i, err)
		}
	}
	second, err := v.Flush(ctx)
	if err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if len(first.Slabs) != 1 || len(second.Slabs) != 1 {
		t.Fatalf("expected one slab per flush, got %d and %d", len(first.Slabs), len(second.Slabs))
	}
	if first.Slabs[0] == second.Slabs[0] {
		t.Fatal("two flushes landed in one slab, which the slab model says cannot happen")
	}

	twoSlabs, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if billed := twoSlabs.PinnedData - baseline.PinnedData; billed != 2*uint64(slabBytes) {
		t.Fatalf("two flushes billed %s, want two slabs of %s", human(billed), human(uint64(slabBytes)))
	}

	// Half the records in the second slab go. The slab is still shared with the
	// other half, so nothing may be released yet: that is the case where a naive
	// implementation frees a slab out from under live records.
	catalog := v.Entries()
	var dropped int
	for _, entry := range catalog {
		if entry.SlabID == string(second.Slabs[0]) && dropped < perFlush/2 {
			if err := v.Forget(entry.ID); err != nil {
				t.Fatalf("forget %s: %v", entry.ID, err)
			}
			dropped++
		}
	}
	partial, err := v.Reclaim(ctx, vault.ReclaimOptions{})
	if err != nil {
		t.Fatalf("reclaim after dropping half a slab: %v", err)
	}
	t.Logf("dropped %d of %d records from a shared slab: released %d slab(s), freed %s",
		dropped, perFlush, partial.SlabsReleased, human(partial.Freed()))
	if partial.Freed() != 0 {
		t.Fatalf("dropping half a shared slab freed %s; a slab is billed whole and was still in use",
			human(partial.Freed()))
	}

	// Now the rest of that slab. It holds nothing live, so exactly one slab of
	// quota must come back, and the other slab's records must all survive.
	for _, entry := range v.Entries() {
		if entry.SlabID == string(second.Slabs[0]) {
			if err := v.Forget(entry.ID); err != nil {
				t.Fatalf("forget %s: %v", entry.ID, err)
			}
		}
	}
	full, err := v.Reclaim(ctx, vault.ReclaimOptions{})
	if err != nil {
		t.Fatalf("reclaim after emptying a slab: %v", err)
	}
	t.Logf("emptied the slab: released %d slab(s), freed %s", full.SlabsReleased, human(full.Freed()))
	if full.Freed() != uint64(slabBytes) {
		t.Fatalf("emptying one slab freed %s, want exactly %s", human(full.Freed()), human(uint64(slabBytes)))
	}

	survivors := 0
	for _, id := range kept {
		local, err := v.LocalBody(id)
		if err != nil {
			t.Fatalf("survivor %s is not on this device: %v", id, err)
		}
		fetched, err := v.BodyFromNetwork(ctx, id)
		if err != nil {
			t.Fatalf("survivor %s cannot be read back: %v", id, err)
		}
		if !bytes.Equal(local, fetched) {
			t.Fatalf("survivor %s is not byte-exact after a reclaim", id)
		}
		survivors++
	}
	t.Logf("survivors %d/%d byte-exact after reclaiming the slab beside them", survivors, len(kept))
}

// A vault must never release storage it did not pin.
//
// An account can hold objects this vault knows nothing about: another vault
// under the same app key, an older tool, a marker somebody left deliberately.
// Reasoning from "everything the catalog does not name" would delete all of
// them, and deletion here is not recoverable, the object entry is the only
// thing that turns a slab into readable bytes. So the ledger of what this vault
// pinned is what bounds a sweep, and this is the test that says so.
//
// This is a regression test. An earlier version of the sweep reasoned from the
// account listing, and it destroyed a marker object that had been left pinned
// on purpose to measure durability over time.
func TestLiveReclaimLeavesForeignStorageAlone(t *testing.T) {
	v := liveVaultAt(t, t.TempDir())
	ctx := context.Background()

	if _, err := v.WaitReady(ctx, 90*time.Second); err != nil {
		t.Fatalf("account not ready: %v", err)
	}

	// Something on the account that this vault did not write and has no
	// catalogue entry for, standing in for anything a sweep might trample.
	appKey, err := keys.AppKeyFromEnv()
	if err != nil {
		t.Fatalf("app key: %v", err)
	}
	client, err := sia.Connect(sia.Config{AppKey: appKey, Indexer: vault.DefaultIndexer()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	payloads := [][]byte{[]byte("storage this vault did not write and must not delete")}
	foreign, err := client.UploadPacked(ctx, payloads)
	if err != nil {
		t.Fatalf("write foreign storage: %v", err)
	}
	if err := client.PinSlabs(ctx, foreign); err != nil {
		t.Fatalf("pin foreign slab: %v", err)
	}
	if err := client.PinObjects(ctx, foreign); err != nil {
		t.Fatalf("pin foreign object: %v", err)
	}
	foreignRef := foreign.Placements()[0].Ref
	t.Cleanup(func() {
		if err := client.DeleteObjects(context.Background(), []sia.ObjectRef{foreignRef}); err != nil {
			t.Errorf("delete foreign object: %v", err)
		}
		for _, slabID := range foreign.Slabs() {
			if err := client.UnpinSlab(context.Background(), slabID); err != nil {
				t.Errorf("release foreign slab %s: %v", slabID, err)
			}
		}
	})

	// This vault writes and then forgets everything it wrote, which is the
	// state in which a sweep is most tempted to over-reach.
	for i, req := range statements(5) {
		if _, err := v.Remember(ctx, req); err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
	}
	if _, err := v.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	t.Cleanup(func() { releaseVault(t, v) })
	for _, entry := range v.Entries() {
		if err := v.Forget(entry.ID); err != nil {
			t.Fatalf("forget %s: %v", entry.ID, err)
		}
	}

	sweep, err := v.Reclaim(ctx, vault.ReclaimOptions{})
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	t.Logf("swept %d object(s), deleted %d, released %d slab(s), freed %s",
		sweep.ObjectsSeen, sweep.ObjectsDeleted, sweep.SlabsReleased, human(sweep.Freed()))

	if _, err := client.Download(ctx, foreignRef); err != nil {
		t.Fatalf("a reclaim destroyed storage this vault did not pin: %v", err)
	}
	t.Log("storage this vault did not pin survived a reclaim that released everything it did")
}

// Pass mark 6: repack rewrites every object id, and nothing keyed on a record
// id may notice. A union or a catalog keyed on storage location would see the
// entire vault turn into new records here.
func TestLiveRepackRewritesObjectIdsAndNothingBreaks(t *testing.T) {
	v := liveVaultAt(t, t.TempDir())
	ctx := context.Background()

	if _, err := v.WaitReady(ctx, 90*time.Second); err != nil {
		t.Fatalf("account not ready: %v", err)
	}
	t.Cleanup(func() { releaseVault(t, v) })

	// Several small flushes, which is the shape repack exists to fix: each one
	// mints a slab it can never fill.
	const flushes, perFlush = 4, 15
	bodies := make(map[record.ID][]byte, flushes*perFlush)
	before := make(map[record.ID]string, flushes*perFlush)

	corpus := statements(flushes * perFlush)
	for f := range flushes {
		for i := range perFlush {
			written, err := v.Remember(ctx, corpus[f*perFlush+i])
			if err != nil {
				t.Fatalf("remember: %v", err)
			}
			body, err := v.LocalBody(written.ID)
			if err != nil {
				t.Fatalf("read the device's copy: %v", err)
			}
			bodies[written.ID] = bytes.Clone(body)
		}
		if _, err := v.Flush(ctx); err != nil {
			t.Fatalf("flush %d: %v", f, err)
		}
	}
	for _, entry := range v.Entries() {
		before[entry.ID] = entry.ObjectRef
	}

	packed, err := v.Repack(ctx)
	if err != nil {
		t.Fatalf("repack: %v", err)
	}
	t.Logf("repack: %d record(s), %d slab(s) into %d, transient peak %d, in %v",
		len(packed.Records), packed.SlabsBefore, packed.SlabsAfter, packed.Peak, packed.Elapsed)
	t.Logf("  read %v · write %v · retire %v · freed %s",
		packed.ReadFor, packed.WriteFor, packed.RetireFor, human(packed.Freed()))

	if packed.SlabsAfter >= packed.SlabsBefore {
		t.Fatalf("repack went from %d slabs to %d", packed.SlabsBefore, packed.SlabsAfter)
	}

	var moved, same int
	for _, entry := range v.Entries() {
		was, known := before[entry.ID]
		if !known {
			t.Fatalf("repack introduced a record id the vault did not have: %s", entry.ID)
		}
		if was == entry.ObjectRef {
			same++
			continue
		}
		moved++
	}
	t.Logf("object ids: %d of %d rewritten, %d unchanged", moved, len(before), same)
	if moved != len(before) {
		t.Fatalf("repack left %d object ids unchanged; the catalog is out of step with storage", same)
	}
	if got := len(v.Entries()); got != len(before) {
		t.Fatalf("the catalog holds %d records after a repack, want %d: something keyed on a location",
			got, len(before))
	}

	// Every record still resolves, under the same id it always had, to bytes
	// that match what was written.
	var exact int
	for id, body := range bodies {
		fetched, err := v.BodyFromNetwork(ctx, id)
		if err != nil {
			t.Fatalf("record %s does not resolve after a repack: %v", id, err)
		}
		if !bytes.Equal(fetched, body) {
			t.Fatalf("record %s is not byte-exact after a repack", id)
		}
		exact++
	}
	t.Logf("%d/%d records byte-exact under their original record ids after every object id changed",
		exact, len(bodies))
}

// Pass mark 7, and the read hierarchy.
//
// L1 is this device's own copy, L2 a cached location, L3 the indexer. The
// interesting case is a stale L2: the SDK's slab reader crashes the process
// rather than failing when a cached host set has decayed too far, so an entry
// that can no longer be trusted has to become a re-fetch and not a fault.
func TestLiveReadHierarchyAndStaleCache(t *testing.T) {
	home := t.TempDir()
	v := liveVaultAt(t, home)
	ctx := context.Background()

	if _, err := v.WaitReady(ctx, 90*time.Second); err != nil {
		t.Fatalf("account not ready: %v", err)
	}
	t.Cleanup(func() { releaseVault(t, v) })

	const count = 25
	ids := make([]record.ID, 0, count)
	for i, req := range statements(count) {
		written, err := v.Remember(ctx, req)
		if err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
		ids = append(ids, written.ID)
	}
	if _, err := v.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// L1: the device wrote these, so it still holds them.
	start := time.Now()
	for _, id := range ids {
		if _, _, err := v.Fetch(ctx, id); err != nil {
			t.Fatalf("L1 fetch: %v", err)
		}
	}
	l1 := time.Since(start) / count

	// L3: drop the device's copies, so the read has to reach the indexer.
	for _, id := range ids {
		if err := v.ForgetLocally(id); err != nil {
			t.Fatalf("forget %s locally: %v", id, err)
		}
	}
	start = time.Now()
	for _, id := range ids {
		if _, tier, err := v.Fetch(ctx, id); err != nil {
			t.Fatalf("L3 fetch: %v", err)
		} else if tier != recall.TierNetwork {
			t.Fatalf("a record with no local copy and no cached location was served by %q", tier)
		}
	}
	l3 := time.Since(start) / count

	// L2: the locations are cached now, so dropping the bodies again must be
	// served without asking the indexer where anything is.
	for _, id := range ids {
		if err := v.ForgetLocally(id); err != nil {
			t.Fatalf("forget %s locally: %v", id, err)
		}
	}
	start = time.Now()
	var cached int
	for _, id := range ids {
		_, tier, err := v.Fetch(ctx, id)
		if err != nil {
			t.Fatalf("L2 fetch: %v", err)
		}
		if tier == recall.TierCached {
			cached++
		}
	}
	l2 := time.Since(start) / count

	t.Logf("read hierarchy over %d records: L1 %v · L2 %v · L3 %v", count, l1, l2, l3)
	t.Logf("%d of %d reads were served from the cached location tier", cached, count)
	if cached != count {
		t.Fatalf("only %d of %d reads used a cached location; the tier is not being exercised", cached, count)
	}
	if l2 >= l3 {
		t.Fatalf("a cached location read took %v against %v for a cold one, so the tier buys nothing", l2, l3)
	}

	// The cache has to be affordable. Every object in a flush shares one sector
	// list, so keeping a copy per object would cost more than the data.
	size, err := v.CacheSize()
	if err != nil {
		t.Fatalf("cache size: %v", err)
	}
	t.Logf("location cache: %s for %d object(s) over %d slab(s) = %.0f B/object (%s object half, %s slab half)",
		human(uint64(size.Total())), size.Objects, size.Slabs, size.PerObject(),
		human(uint64(size.ObjectBytes)), human(uint64(size.SlabBytes)))
	if size.PerObject() > 1000 {
		t.Fatalf("the location cache costs %.0f B per object; it is not deduplicated by slab",
			size.PerObject())
	}

	// Pass mark 7. A cached location that no longer describes reachable storage
	// must degrade to a re-fetch. The failure being guarded against is not a
	// slow read: it is an index out of range in a goroutine the caller cannot
	// recover, which takes the process with it.
	//
	// The cache is reached through the device store rather than through the
	// vault, because being able to damage a cache from the public API would be
	// a worse thing to have than this test is worth.
	stale := v.Entries()[0]
	device, err := local.Open(filepath.Join(home, "vault.db"))
	if err != nil {
		t.Fatalf("open the device store: %v", err)
	}
	if err := device.PutSlabMeta(stale.SlabID, []byte("a sector list that no longer describes anything")); err != nil {
		t.Fatalf("damage the cached slab metadata: %v", err)
	}
	if err := device.Close(); err != nil {
		t.Fatalf("close the device store: %v", err)
	}
	if err := v.ForgetLocally(stale.ID); err != nil {
		t.Fatalf("forget %s locally: %v", stale.ID, err)
	}

	start = time.Now()
	found, tier, err := v.Fetch(ctx, stale.ID)
	refetch := time.Since(start)
	if err != nil {
		t.Fatalf("a stale cached location failed the read instead of falling back: %v", err)
	}
	if found.Memory == nil {
		t.Fatal("the re-fetch returned no record")
	}
	if tier != recall.TierNetwork {
		t.Fatalf("a stale cached location was served by %q; it should have fallen through to the indexer", tier)
	}
	t.Logf("a stale cached location degraded to a re-fetch in %v, and the process is still running", refetch)

	stats, err := v.ReadStats()
	if err != nil {
		t.Fatalf("read statistics: %v", err)
	}
	for _, stat := range stats {
		t.Logf("  %-8s %4d served, mean %v, %d miss(es)", stat.Tier, stat.Reads, stat.Mean(), stat.Misses)
	}
}
