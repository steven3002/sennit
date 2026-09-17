package vault_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/store/reclaim"
	"github.com/steven3002/sennit/vault"
)

// ProbeHomeEnv is the vault directory the interrupted flush probe writes to.
//
// It is a variable of its own rather than the vault's usual one, and the probe
// skips without it, because this is the one test here that deliberately leaves
// storage pinned. Naming the directory explicitly is what stops it running
// against a vault somebody cares about, and the directory is not a temporary
// one, because its ledger is the only local record of what the run pinned and
// it has to outlive the test.
const ProbeHomeEnv = "SENNIT_INTERRUPT_PROBE_HOME"

// TestLiveAnInterruptedFlushBetweenTheTwoPinsLeavesTheSlabUnrecorded measures
// what a flush cancelled between pinning the slab and pinning its objects
// leaves on the account.
//
// It costs one slab of quota, pinned and not released by this test. Reading
// the code says the slab stays pinned while nothing on the device records it;
// this is the run that says whether that is what happens.
//
// The cancellation is aimed with the vault's own progress reports rather than
// with a hook added for the purpose. The store reports each pinning stage
// immediately before running it, and the vault gives both the one phase name,
// so the second PhasePin of a flush is the moment the slab is pinned and the
// objects are not.
//
// The slab is identified by id rather than by counting. The account is read for
// every slab this vault's ledger does not name before the write and again after
// it, and the probe's slab is the difference, so a slab pinned by anything else
// in the meantime cannot be mistaken for it.
//
// It fails against the code as it stands, and the failure is the finding: the
// account grows by one slab, the slab holds nothing, and this vault's ledger
// does not record it. Releasing it needs the account-wide release, so do it the
// way it was first done. Use a second vault directory that has never written
// anything and has nothing queued, because this one keeps the record queued and
// a release refuses while it does. Read the account with
// TestLiveReadWhatTheAccountIsBilledFor, and run `sennit reclaim --orphans` from
// that directory only if exactly one slab holds nothing and it is the one this
// test logged. Keep this directory until the account reads back at the figure
// it had before the run.
func TestLiveAnInterruptedFlushBetweenTheTwoPinsLeavesTheSlabUnrecorded(t *testing.T) {
	if os.Getenv(LiveEnv) == "" {
		t.Skipf("set %s=1 to run against a real indexer", LiveEnv)
	}
	home := os.Getenv(ProbeHomeEnv)
	if home == "" {
		t.Skipf("set %s to a throwaway directory; this test pins a slab and does not release it",
			ProbeHomeEnv)
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
	ctx, cancelAll := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancelAll()
	flushCtx, interrupt := context.WithCancel(ctx)
	defer interrupt()

	// The second PhasePin is the window. The first is reported before the
	// slabs are pinned, the second after they are and before the objects are,
	// so cancelling on the second lands between the two calls.
	var pins int
	v, err := vault.Open(ctx, vault.Options{
		Home:    home,
		Phrase:  phrase,
		AppKey:  appKey,
		Indexer: vault.DefaultIndexer(),
		OnProgress: func(p vault.Progress) {
			if p.Phase != vault.PhasePin {
				return
			}
			pins++
			if pins == 2 {
				t.Log("the slab is pinned and the objects are not: interrupting here")
				interrupt()
			}
		},
	})
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	defer v.Close()

	before, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("account before: %v", err)
	}
	t.Logf("account before: %d B pinned (%s) of %d B", before.PinnedData, mib(before.PinnedData), before.MaxPinnedData)
	unledgeredBefore := readUnledgered(ctx, t, v, "before the write")

	if _, err := v.Remember(ctx, vault.RememberRequest{
		Statement: "An interrupted flush is the one failure whose cost is billed rather than lost.",
		Context:   "Written by the probe that measures what a cancelled flush leaves on the account.",
		Type:      record.TypeFact,
		Tags:      []string{"sennit", "interrupted-flush"},
	}); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if queued := v.Pending(); queued != 1 {
		t.Fatalf("%d record(s) queued before the flush, want 1", queued)
	}

	flushed, err := v.Flush(flushCtx)
	if err == nil {
		t.Fatalf("the flush was cancelled between the two pins and still succeeded: %+v", flushed)
	}
	t.Logf("the cancelled flush failed with: %v", err)
	if pins != 2 {
		t.Fatalf("the flush reported %d pinning phase(s), want 2, so the cancel did not land in the window", pins)
	}

	// What the device knows. The record is owed to the network again, and the
	// question is whether the slab it already paid for is written down.
	if queued := v.Pending(); queued != 1 {
		t.Errorf("%d record(s) queued after the cancelled flush, want the record handed back", queued)
	}
	tracked, err := v.TrackedSlabs()
	if err != nil {
		t.Fatalf("tracked slabs: %v", err)
	}
	t.Logf("slabs in this device's ledger after the cancelled flush: %d", len(tracked))
	for _, slab := range tracked {
		t.Logf("  ledger holds %s", slab.ID)
	}

	// What the account knows. Pinning is synchronous at the indexer, so the
	// first reading should already show the slab; the few re-reads are there so
	// that a slow update is reported as slow rather than as no change at all.
	after := settledAccount(ctx, t, v, before.PinnedData)
	t.Logf("account after: %d B pinned (%s), a change of %+d B",
		after.PinnedData, mib(after.PinnedData), int64(after.PinnedData)-int64(before.PinnedData))
	unledgeredAfter := readUnledgered(ctx, t, v, "after the cancelled flush")

	var added []reclaim.Unledgered
	for _, slab := range unledgeredAfter {
		if _, existed := unledgeredBefore[slab.ID]; !existed {
			added = append(added, slab)
		}
	}
	for id := range unledgeredBefore {
		if _, still := unledgeredAfter[id]; !still {
			t.Errorf("slab %s was billed before the write and is not billed after it", id)
		}
	}
	for _, slab := range sortedUnledgered(added) {
		t.Logf("PROBE SLAB %s holds nothing: %t", slab.ID, slab.Empty)
	}
	if len(added) != 1 {
		t.Fatalf("the write added %d unledgered slab(s), want exactly 1", len(added))
	}
	probe := added[0]

	switch {
	case !probe.Empty:
		t.Errorf("the probe's slab %s holds an object: the cancel did not stop the objects being pinned", probe.ID)
	case after.PinnedData > before.PinnedData && len(tracked) == 0:
		t.Errorf("the account grew by %d B and this device's ledger records no slab: "+
			"slab %s is billed, holds nothing, and only an account-wide release can reach it",
			after.PinnedData-before.PinnedData, probe.ID)
	case after.PinnedData > before.PinnedData:
		t.Logf("the account grew by %d B and the ledger records %d slab(s)",
			after.PinnedData-before.PinnedData, len(tracked))
	default:
		t.Logf("the account did not grow, so the cancelled flush pinned nothing")
	}
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
