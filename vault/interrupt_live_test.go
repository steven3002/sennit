package vault_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store"
	"github.com/steven3002/sennit/store/packer"
	"github.com/steven3002/sennit/store/reclaim"
	"github.com/steven3002/sennit/vault"
)

// ProbeHomeEnv is the vault directory the interrupted flush probe writes to.
//
// It is a variable of its own rather than the vault's usual one, and the probe
// skips without it, because this is the one test here that pins storage it has
// to release itself. Naming the directory explicitly is what stops it running
// against a vault somebody cares about, and the directory is deliberately not a
// temporary one: its ledger is the only local record of what the run pinned, so
// it has to survive a failure part way through, including the failure of the
// release.
const ProbeHomeEnv = "SENNIT_INTERRUPT_PROBE_HOME"

// ProbeRecordsEnv overrides how many records the probe writes per flush.
//
// The count is what sets the width of the window the probe aims at: objects are
// pinned PinConcurrency at a time, so the pinning step lasts about one round
// trip per sixteen records. Raising it widens the window at no extra storage
// cost, since the records still pack into one slab.
const ProbeRecordsEnv = "SENNIT_INTERRUPT_PROBE_RECORDS"

// probeRecords is the default count. At sixteen pins in flight this is twenty
// round trips of pinning, measured between three and ten seconds, which is wide
// enough to interrupt part way through with room on both sides.
const probeRecords = 320

// TestLiveAnInterruptedFlushLeavesASlabTheLedgerNamesAndReclaimReleases
// measures what a flush cancelled part way through pinning its objects leaves
// on the account, and then releases all of it.
//
// It replaces the probe that measured the narrower case, a cancel between the
// slab pin and the first object pin. That probe required the interrupt to leave
// exactly one slab this device's ledger did not name, which is the state the
// write path no longer produces, so it could not be made to pass by a change of
// expectations: what it asserted is gone.
//
// What it costs while it runs is three slabs, 120 MiB, and what it costs when
// it finishes is nothing. Each of the three is released here, by this device's
// own ledger, through the ordinary sweep. No account-wide release is used and
// none is needed: every slab the probe pins is in its ledger before it can be
// billed, which is the whole of what is being measured.
//
// The cancel is aimed with the vault's own progress reports and a stopwatch.
// The store reports each pinning step immediately before running it and the
// vault gives both the one phase name, so the second PhasePin of a flush is the
// moment the objects start being pinned. The first flush measures how long that
// step takes on this network, and the second is cancelled a third of the way
// into it.
//
// Every slab is identified by id rather than by counting, and the account is
// read before and after each step.
func TestLiveAnInterruptedFlushLeavesASlabTheLedgerNamesAndReclaimReleases(t *testing.T) {
	if os.Getenv(LiveEnv) == "" {
		t.Skipf("set %s=1 to run against a real indexer", LiveEnv)
	}
	home := os.Getenv(ProbeHomeEnv)
	if home == "" {
		t.Skipf("set %s to a throwaway directory; this test pins storage and releases it again",
			ProbeHomeEnv)
	}
	records := probeRecords
	if raw := os.Getenv(ProbeRecordsEnv); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			t.Fatalf("%s=%q is not a record count", ProbeRecordsEnv, raw)
		}
		records = parsed
	}

	phrase, err := keys.ReadPhrase(nil)
	if err != nil {
		t.Fatalf("recovery phrase: %v", err)
	}
	appKey, err := keys.AppKeyFromEnv()
	if err != nil {
		t.Fatalf("app key: %v", err)
	}

	// Two contexts. The flush's own is the one the probe cancels; everything
	// before and after it has to keep working, and an account read on a
	// cancelled context would report nothing.
	ctx, cancelAll := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancelAll()

	shot := &interrupter{}
	v, err := vault.Open(ctx, vault.Options{
		Home:    home,
		Phrase:  phrase,
		AppKey:  appKey,
		Indexer: vault.DefaultIndexer(),
		// The stub embedder, because nothing here is about vectors and the real
		// model costs several hundred megabytes and a third of a second per
		// record. The records written are real, sealed and on Sia either way.
		Embedder: embedtest.NewStub(),
		// Manual flushes only. The probe decides when each flush happens, and a
		// cadence that fired part way through writing the queue would pin a
		// slab the probe never asked for.
		Flush:      packer.Policy{MaxBytes: store.DefaultSlabPayloadSize},
		OnProgress: shot.report,
	})
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	defer v.Close()

	// What one slab is billed as, read from the network rather than assumed,
	// because every figure below moves in that unit.
	slab, err := v.SlabPayloadSize()
	if err != nil {
		t.Fatalf("slab payload size: %v", err)
	}
	slabBytes := uint64(slab)
	t.Logf("one slab holds %d payload byte(s) (%s)", slab, mib(slabBytes))

	baseline, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("account before: %v", err)
	}
	t.Logf("BASELINE account: %d B pinned (%s) of %d B",
		baseline.PinnedData, mib(baseline.PinnedData), baseline.MaxPinnedData)
	baselineSlabs := readUnledgered(ctx, t, v, "before anything was written")
	if tracked, err := v.TrackedSlabs(); err != nil {
		t.Fatalf("tracked slabs: %v", err)
	} else if len(tracked) != 0 {
		t.Fatalf("this vault's ledger already holds %d slab(s); the probe needs a directory "+
			"that has never written anything", len(tracked))
	}

	// Everything the probe pins is released at the end, from this vault's own
	// ledger. It runs whatever happened above it, because the cost of not
	// running it is a slab billed for as long as nobody notices.
	defer releaseEverythingThisProbePinned(t, v, baseline, baselineSlabs)

	// The first flush, uncancelled. It is what the queue of the second flush
	// will need a home for, and it times the pinning step on this network.
	writeRecords(ctx, t, v, records, "landed")
	landed, err := v.Flush(ctx)
	if err != nil {
		t.Fatalf("the first flush: %v", err)
	}
	t.Logf("FLUSH 1 %d object(s) over %d slab(s) in upload %v, pin slabs %v, pin objects %v",
		len(landed.Written), len(landed.Slabs), landed.UploadFor, landed.PinSlabsFor, landed.PinObjectFor)
	for _, id := range landed.Slabs {
		t.Logf("LANDED SLAB %s", id)
	}
	afterLanded, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("account after the first flush: %v", err)
	}
	t.Logf("account after flush 1: %d B pinned (%s), a change of %+d B",
		afterLanded.PinnedData, mib(afterLanded.PinnedData),
		int64(afterLanded.PinnedData)-int64(baseline.PinnedData))
	ledgerBefore := trackedByID(t, v, "after the first flush")

	// The second flush, cancelled a third of the way through pinning its
	// objects. Some are pinned by then and some are not, which is the state
	// being measured.
	writeRecords(ctx, t, v, records, "interrupted")
	if queued := v.Pending(); queued != records {
		t.Fatalf("%d record(s) queued before the cancelled flush, want %d", queued, records)
	}
	flushCtx, interrupt := context.WithCancel(ctx)
	defer interrupt()
	aim := landed.PinObjectFor / 3
	shot.arm(interrupt, aim, t)
	t.Logf("aiming the cancel %v into the object pinning, from the %v the first flush took",
		aim, landed.PinObjectFor)

	flushed, err := v.Flush(flushCtx)
	if err == nil {
		t.Fatalf("the flush was cancelled %v into pinning %d objects and still succeeded: %d object(s) over %v",
			aim, records, len(flushed.Written), flushed.Slabs)
	}
	t.Logf("the cancelled flush failed with: %v", err)
	pins, firedAt := shot.state()
	if pins < 2 {
		t.Fatalf("the flush reported %d pinning phase(s), want at least 2, so the cancel did not land "+
			"in the object pinning", pins)
	}
	t.Logf("the cancel was armed at %s and the flush returned %v after it",
		firedAt.UTC().Format(time.RFC3339), time.Since(firedAt))

	if queued := v.Pending(); queued != records {
		t.Errorf("%d record(s) queued after the cancelled flush, want the %d handed back", queued, records)
	}

	// What the device knows. This is the change: the slab the interrupt left
	// behind is in the ledger, so it is this device's to release.
	ledgerAfter := trackedByID(t, v, "after the cancelled flush")
	var stranded []reclaim.Slab
	for id, slab := range ledgerAfter {
		if _, existed := ledgerBefore[id]; !existed {
			stranded = append(stranded, slab)
		}
	}
	for id := range ledgerBefore {
		if _, still := ledgerAfter[id]; !still {
			t.Errorf("slab %s left this device's ledger during the cancelled flush", id)
		}
	}
	if len(stranded) != 1 {
		t.Fatalf("the cancelled flush added %d slab(s) to this device's ledger, want exactly 1: %+v",
			len(stranded), stranded)
	}
	probe := stranded[0]
	t.Logf("PROBE SLAB %s is in this device's ledger, %d record(s) of %d byte(s), origin %q",
		probe.ID, probe.Records, probe.Bytes, probe.Origin)
	if !probe.Releasable() {
		t.Errorf("the probe's slab is filed as %q, so a sweep would leave it alone", probe.Origin)
	}

	// What the account knows. Pinning is synchronous at the indexer, so the
	// first reading should already show the slab; the few re-reads are there so
	// that a slow update is reported as slow rather than as no change at all.
	afterProbe := settledAccount(ctx, t, v, afterLanded.PinnedData)
	t.Logf("account after the cancelled flush: %d B pinned (%s), a change of %+d B",
		afterProbe.PinnedData, mib(afterProbe.PinnedData),
		int64(afterProbe.PinnedData)-int64(afterLanded.PinnedData))

	// The two readings status makes about such a slab, and both have to be
	// right. It is not another installation's, and it does not hold nothing.
	for _, slab := range readUnledgered(ctx, t, v, "after the cancelled flush") {
		if slab.ID == probe.ID {
			t.Errorf("slab %s is the probe's own and status reports it as another installation's", slab.ID)
		}
	}
	orphans, err := v.Orphans(ctx, vault.ReclaimOptions{})
	if err != nil {
		t.Fatalf("orphans after the cancelled flush: %v", err)
	}
	holdsObjects := true
	for _, orphan := range orphans {
		t.Logf("ORPHAN after the cancelled flush: %s", orphan.ID)
		if orphan.ID == probe.ID {
			holdsObjects = false
		}
	}
	if !holdsObjects {
		t.Errorf("the probe's slab %s holds nothing, so the cancel landed before any object was "+
			"pinned; raise %s to widen the window", probe.ID, ProbeRecordsEnv)
	}

	// The release, by the two commands the acceptance names. A flush first,
	// because a reclaim refuses while anything is queued, and then the ordinary
	// reclaim, which is bounded by this device's ledger and now reaches the
	// stranded slab.
	settled, err := v.Flush(ctx)
	if err != nil {
		t.Fatalf("the flush that clears the queue: %v", err)
	}
	t.Logf("FLUSH 2 %d object(s) over %d slab(s)", len(settled.Written), len(settled.Slabs))
	for _, id := range settled.Slabs {
		t.Logf("SETTLED SLAB %s", id)
		if id == probe.ID {
			t.Errorf("the second flush landed in the stranded slab %s, which cannot be extended", id)
		}
	}
	if queued := v.Pending(); queued != 0 {
		t.Fatalf("%d record(s) still queued after the flush", queued)
	}
	beforeSweep, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("account before the sweep: %v", err)
	}

	sweep, err := v.Reclaim(ctx, vault.ReclaimOptions{})
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	t.Logf("RECLAIM saw %d object(s), deleted %d, released %d slab(s), %d B before, %d B after, freed %d B",
		sweep.ObjectsSeen, sweep.ObjectsDeleted, sweep.SlabsReleased,
		sweep.Before.PinnedData, sweep.After.PinnedData, sweep.Freed())

	// The count of objects the sweep deleted is the measurement of how far into
	// the pinning the cancel landed: they are exactly the objects the
	// interrupted flush had pinned, since nothing else on this device's slabs
	// is uncatalogued.
	if sweep.ObjectsDeleted < 1 || sweep.ObjectsDeleted >= records {
		t.Errorf("the sweep deleted %d object(s) of a batch of %d; want part of one batch, "+
			"which is what an interrupt part way through pinning leaves", sweep.ObjectsDeleted, records)
	}
	if sweep.SlabsReleased != 1 {
		t.Errorf("the sweep released %d slab(s), want exactly the stranded one", sweep.SlabsReleased)
	}
	for id := range trackedByID(t, v, "after the sweep") {
		if id == probe.ID {
			t.Errorf("slab %s is still in this device's ledger after the sweep", id)
		}
	}
	if sweep.After.PinnedData != beforeSweep.PinnedData-slabBytes {
		t.Errorf("the account reads %d B after the sweep and read %d B before it, want one slab less",
			sweep.After.PinnedData, beforeSweep.PinnedData)
	}
}

// releaseEverythingThisProbePinned returns the account to the figure it started
// at, and says so by id.
//
// It forgets every record and sweeps with the empty-catalog authorisation,
// which is the same release the repack measurements use. It is bounded by this
// vault's ledger, so it cannot touch a slab this probe did not pin: the
// baseline slabs are not in it, and a sweep only releases what is.
func releaseEverythingThisProbePinned(t *testing.T, v *vault.Vault, baseline sia.Account,
	baselineSlabs map[sia.SlabID]reclaim.Unledgered) {
	t.Helper()
	ctx := context.Background()

	if queued := v.Pending(); queued > 0 {
		// A reclaim refuses while anything is queued, and flushing here would
		// pin another slab to release it from. Say what is left and stop.
		t.Errorf("%d record(s) are still queued, so the release cannot run. Run `sennit flush` "+
			"and then `sennit reclaim --release-all` in %q, then read the account back", queued, ProbeHomeEnv)
		return
	}
	for _, entry := range v.Entries() {
		if err := v.Forget(entry.ID); err != nil {
			t.Errorf("forget %s: %v", entry.ID, err)
			return
		}
	}
	sweep, err := v.Reclaim(ctx, vault.ReclaimOptions{ReleaseAll: true})
	if err != nil {
		t.Errorf("the release sweep: %v", err)
		return
	}
	t.Logf("RELEASE deleted %d object(s), released %d slab(s), %d B before, %d B after, freed %d B",
		sweep.ObjectsDeleted, sweep.SlabsReleased, sweep.Before.PinnedData, sweep.After.PinnedData, sweep.Freed())

	account, err := v.Account(ctx)
	if err != nil {
		t.Errorf("account after the release: %v", err)
		return
	}
	t.Logf("FINAL account: %d B pinned (%s), baseline was %d B (%s)",
		account.PinnedData, mib(account.PinnedData), baseline.PinnedData, mib(baseline.PinnedData))
	if account.PinnedData != baseline.PinnedData {
		t.Errorf("the account reads %d B and started at %d B, a difference of %+d B: "+
			"keep %q and its ledger until that is resolved",
			account.PinnedData, baseline.PinnedData,
			int64(account.PinnedData)-int64(baseline.PinnedData), ProbeHomeEnv)
	}
	after := readUnledgered(ctx, t, v, "after the release")
	for id := range baselineSlabs {
		if _, still := after[id]; !still {
			t.Errorf("slab %s was billed before the probe ran and is not billed now", id)
		}
	}
	for id := range after {
		if _, expected := baselineSlabs[id]; !expected {
			t.Errorf("slab %s is billed to the account and was not there before the probe ran", id)
		}
	}
}

// writeRecords queues n records, each small enough that the whole batch packs
// into one slab.
func writeRecords(ctx context.Context, t *testing.T, v *vault.Vault, n int, batch string) {
	t.Helper()
	start := time.Now()
	for i := range n {
		if _, err := v.Remember(ctx, vault.RememberRequest{
			Statement: fmt.Sprintf(
				"Station %d of the %s batch reports that a slab is billed from the moment it is pinned.", i+1, batch),
			Context: "Written by the probe that measures what a flush interrupted while pinning leaves behind.",
			Type:    record.TypeFact,
			Tags:    []string{"sennit", "interrupted-flush", batch},
		}); err != nil {
			t.Fatalf("remember %d of %d in the %s batch: %v", i+1, n, batch, err)
		}
	}
	t.Logf("queued %d record(s) in the %s batch in %v", n, batch, time.Since(start))
}

// An interrupter cancels a flush a set time into the step that pins its
// objects.
//
// The arming is explicit because one vault runs several flushes here and only
// one of them is meant to be interrupted. The progress callback can be reached
// from several goroutines while shards upload, so the state is held under a
// lock even though the pinning reports themselves arrive on one.
type interrupter struct {
	mu      sync.Mutex
	armed   bool
	after   time.Duration
	cancel  context.CancelFunc
	pins    int
	firedAt time.Time
	log     *testing.T
}

func (s *interrupter) arm(cancel context.CancelFunc, after time.Duration, t *testing.T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armed, s.after, s.cancel, s.pins, s.log = true, after, cancel, 0, t
}

func (s *interrupter) state() (int, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pins, s.firedAt
}

func (s *interrupter) report(p vault.Progress) {
	if p.Phase != vault.PhasePin {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pins++
	// The first pinning report comes before the slab is pinned, the second
	// before the objects are. Only the second is the window.
	if !s.armed || s.pins != 2 {
		return
	}
	s.armed = false
	s.firedAt = time.Now()
	if s.log != nil {
		s.log.Logf("the objects are being pinned: cancelling in %v", s.after)
	}
	go func(after time.Duration, cancel context.CancelFunc) {
		time.Sleep(after)
		cancel()
	}(s.after, s.cancel)
}

// TestLiveReadWhatTheAccountIsBilledFor reads, and changes nothing.
//
// It lists the account's quota, every slab the account is billed for that a
// fresh ledger does not name, and every slab that holds nothing at all, each by
// id. It is what a release of orphaned storage is checked against immediately
// before it runs and immediately after, because an orphan count is only good at
// the moment it is taken. It opens a vault in a temporary directory, so it has
// no ledger of its own to confuse the reading and nothing it could write to.
func TestLiveReadWhatTheAccountIsBilledFor(t *testing.T) {
	v := liveVault(t)
	ctx := context.Background()

	account, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	t.Logf("account: %d B pinned (%s) of %d B", account.PinnedData, mib(account.PinnedData), account.MaxPinnedData)
	readUnledgered(ctx, t, v, "now")

	// Zero options: the empty-walk guard stays on, and nothing is released.
	orphans, err := v.Orphans(ctx, vault.ReclaimOptions{})
	if err != nil {
		t.Fatalf("orphans: %v", err)
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].ID < orphans[j].ID })
	t.Logf("orphans, slabs holding nothing: %d", len(orphans))
	for _, orphan := range orphans {
		t.Logf("ORPHAN %s", orphan.ID)
	}
}

// readUnledgered logs and returns every slab the account is billed for that
// this vault's ledger does not name.
func readUnledgered(ctx context.Context, t *testing.T, v *vault.Vault, when string) map[sia.SlabID]reclaim.Unledgered {
	t.Helper()
	slabs, err := v.Unledgered(ctx)
	if err != nil {
		t.Fatalf("unledgered slabs %s: %v", when, err)
	}
	out := make(map[sia.SlabID]reclaim.Unledgered, len(slabs))
	var empty int
	for _, slab := range sortedUnledgered(slabs) {
		out[slab.ID] = slab
		if slab.Empty {
			empty++
		}
		t.Logf("UNLEDGERED %s %s holds nothing: %t", when, slab.ID, slab.Empty)
	}
	t.Logf("unledgered %s: %d slab(s), %d holding nothing", when, len(slabs), empty)
	return out
}

func sortedUnledgered(slabs []reclaim.Unledgered) []reclaim.Unledgered {
	out := append([]reclaim.Unledgered(nil), slabs...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// trackedByID logs and returns every slab in this device's ledger.
func trackedByID(t *testing.T, v *vault.Vault, when string) map[sia.SlabID]reclaim.Slab {
	t.Helper()
	tracked, err := v.TrackedSlabs()
	if err != nil {
		t.Fatalf("tracked slabs %s: %v", when, err)
	}
	out := make(map[sia.SlabID]reclaim.Slab, len(tracked))
	for _, slab := range tracked {
		out[slab.ID] = slab
		t.Logf("LEDGER %s %s, %d record(s) of %d byte(s), origin %q",
			when, slab.ID, slab.Records, slab.Bytes, slab.Origin)
	}
	t.Logf("ledger %s: %d slab(s)", when, len(tracked))
	return out
}

// settledAccount reads the account, re-reading a few times if nothing has
// changed yet, and logs every reading it takes.
func settledAccount(ctx context.Context, t *testing.T, v *vault.Vault, from uint64) sia.Account {
	t.Helper()
	var account sia.Account
	for attempt := 1; attempt <= 6; attempt++ {
		var err error
		if account, err = v.Account(ctx); err != nil {
			t.Fatalf("account after: %v", err)
		}
		t.Logf("account reading %d: %d B pinned (%s)", attempt, account.PinnedData, mib(account.PinnedData))
		if account.PinnedData != from {
			return account
		}
		time.Sleep(5 * time.Second)
	}
	return account
}

func mib(b uint64) string { return fmt.Sprintf("%.2f MiB", float64(b)/(1<<20)) }
