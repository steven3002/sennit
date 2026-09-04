package vault_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
	"github.com/steven3002/sennit/vault"
)

// The child half of the under-load measurement runs from the same test binary,
// so that the concurrency is two processes rather than two goroutines. One
// process per client is how this system is actually used: every protocol client
// launches its own, and the catalog they share is an append-only log whose
// mutex is in-process only.
const (
	loadHomeEnv    = "SENNIT_TEST_LOAD_HOME"
	loadBatchEnv   = "SENNIT_TEST_LOAD_BATCH"
	loadFlushesEnv = "SENNIT_TEST_LOAD_FLUSHES"
)

// releaseByLedger returns everything a repack measurement pinned.
//
// It forgets the catalog and then sweeps with the empty keep-set stated
// explicitly, because the point of these measurements is to disturb the catalog
// and a cleanup that depended on the catalog being intact would strand slabs
// exactly when it was needed most. The sweep is still bounded by the ledger,
// which is SQLite and survives what the catalog may not.
func releaseByLedger(t *testing.T, home string) {
	t.Helper()
	v, err := openLiveVaultAt(t, home)
	if err != nil {
		t.Errorf("reopen %s to release its storage: %v", home, err)
		return
	}
	defer v.Close()

	for _, entry := range v.Entries() {
		if err := v.Forget(entry.ID); err != nil {
			t.Errorf("forget %s: %v", entry.ID, err)
		}
	}
	sweep, err := v.Reclaim(context.Background(), vault.ReclaimOptions{ReleaseAll: true})
	if err != nil {
		t.Errorf("cleanup sweep: %v", err)
		return
	}
	t.Logf("cleanup: deleted %d object(s), released %d slab(s), freed %s",
		sweep.ObjectsDeleted, sweep.SlabsReleased, human(sweep.Freed()))
}

func openLiveVaultAt(t *testing.T, home string) (*vault.Vault, error) {
	t.Helper()
	phrase, err := keys.ReadPhrase(nil)
	if err != nil {
		return nil, fmt.Errorf("recovery phrase: %w", err)
	}
	appKey, err := keys.AppKeyFromEnv()
	if err != nil {
		return nil, fmt.Errorf("app key: %w", err)
	}
	return vault.Open(context.Background(), vault.Options{
		Home:    home,
		Phrase:  phrase,
		AppKey:  appKey,
		Indexer: vault.DefaultIndexer(),
	})
}

// TestLiveRepackUnderLoad is G3's first unknown.
//
// Repack has only ever been measured on an idle account. This runs one while a
// second process is writing to the same vault, which is the state an automatic
// repack would meet: it is triggered by a watermark rather than by a user, so
// nothing arranges for the vault to be quiet first.
//
// What it asserts is not a latency. It is that the records repack moved are
// still readable from where it says it put them, that the records written
// underneath it survive, and that the account is left holding what the report
// claims.
func TestLiveRepackUnderLoad(t *testing.T) {
	if os.Getenv(loadHomeEnv) != "" {
		runConcurrentWriter(t)
		return
	}
	requireLive(t)

	home := t.TempDir()
	v, err := openLiveVaultAt(t, home)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	ctx := context.Background()
	if _, err := v.WaitReady(ctx, 90*time.Second); err != nil {
		v.Close()
		t.Fatalf("account not ready: %v", err)
	}

	before, err := v.Account(ctx)
	if err != nil {
		v.Close()
		t.Fatalf("account: %v", err)
	}
	t.Logf("account before: %s of %s pinned", human(before.PinnedData), human(before.MaxPinnedData))

	// The vault is closed and the storage released from the ledger at the end,
	// whatever happens in between.
	closed := false
	defer func() {
		if !closed {
			v.Close()
		}
		releaseByLedger(t, home)
	}()

	// Three flushes, three partly-filled slabs: the state repack exists for. A
	// slab can never be extended, so each flush strands one no matter how few
	// records it held.
	const (
		batches   = 3
		perBatch  = 4
		childRuns = 2
	)
	original := make(map[record.ID][]byte)
	for batch := range batches {
		for i := range perBatch {
			written, err := v.Remember(ctx, vault.RememberRequest{
				Statement: fmt.Sprintf(
					"Batch %d record %d: repack under concurrent load is G3's first unmeasured variable.", batch, i),
				Context: "Written to occupy a partly-filled slab before a repack runs over it.",
				Type:    record.TypeFact,
				Tags:    []string{"g3", fmt.Sprintf("batch-%d", batch)},
			})
			if err != nil {
				t.Fatalf("remember batch %d record %d: %v", batch, i, err)
			}
			body, err := v.LocalBody(written.ID)
			if err != nil {
				t.Fatalf("read the device's copy of %s: %v", written.ID, err)
			}
			original[written.ID] = append([]byte(nil), body...)
		}
		if _, err := v.Flush(ctx); err != nil {
			t.Fatalf("flush batch %d: %v", batch, err)
		}
	}

	slabs, err := v.TrackedSlabs()
	if err != nil {
		t.Fatalf("tracked slabs: %v", err)
	}
	t.Logf("staged %d record(s) over %d slab(s)", len(original), len(slabs))
	for _, slab := range slabs {
		t.Logf("  staged slab %s (%d record(s))", slab.ID, slab.Records)
	}
	if len(slabs) < batches {
		t.Fatalf("%d flush(es) produced %d slab(s); the measurement needs one per flush", batches, len(slabs))
	}

	// The idle arm runs the identical path with no second process, so that a
	// difference between the two arms is attributable to the load and not to
	// anything else about the measurement.
	idle := os.Getenv("SENNIT_TEST_IDLE_ARM") != ""

	// The writer starts first and keeps going for the whole repack, so the
	// overlap is real rather than a race that may or may not happen.
	child := exec.Command(os.Args[0], "-test.run", "^TestLiveRepackUnderLoad$", "-test.v")
	child.Env = append(os.Environ(),
		loadHomeEnv+"="+home,
		loadBatchEnv+"="+strconv.Itoa(perBatch),
		loadFlushesEnv+"="+strconv.Itoa(childRuns))
	var childOut bytes.Buffer
	child.Stdout, child.Stderr = &childOut, &childOut
	childDone := make(chan error, 1)
	if idle {
		t.Log("IDLE ARM: no concurrent writer")
		childDone <- nil
	} else {
		if err := child.Start(); err != nil {
			t.Fatalf("start the concurrent writer: %v", err)
		}
		go func() { childDone <- child.Wait() }()
	}

	repackStart := time.Now()
	packed, repackErr := v.Repack(ctx)
	repackFor := time.Since(repackStart)

	childErr := <-childDone
	t.Logf("concurrent writer output:\n%s", childOut.String())

	if repackErr != nil {
		after, slabErr := v.TrackedSlabs()
		t.Logf("ledger after the failure (%v): %d slab(s)", slabErr, len(after))
		for _, slab := range after {
			t.Logf("  ledger still lists %s", slab.ID)
		}
		if orphans, err := v.Orphans(ctx, vault.ReclaimOptions{ReleaseAll: true}); err == nil {
			t.Logf("the indexer holds %d slab(s) with nothing on them: %v", len(orphans), orphans)
		}
		t.Fatalf("repack failed after %s (idle=%v): %v", repackFor, idle, repackErr)
	}
	t.Logf("REPACK UNDER LOAD: %d record(s), %d slab(s) into %d, peak %d, in %s",
		len(packed.Records), packed.SlabsBefore, packed.SlabsAfter, packed.Peak, repackFor)
	t.Logf("  read %s · write %s · retire %s · freed %s",
		packed.ReadFor, packed.WriteFor, packed.RetireFor, human(packed.Freed()))
	if childErr != nil {
		t.Errorf("the concurrent writer failed: %v", childErr)
	}

	// Every record repack claims to have moved has to be readable from the
	// location it now claims, out of the network rather than off this device.
	for id, want := range original {
		got, err := v.BodyFromNetwork(ctx, id)
		if err != nil {
			t.Fatalf("read %s from where repack put it: %v", id, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("record %s is not byte-exact after a repack under load", id)
		}
	}
	t.Logf("all %d repacked record(s) read back byte-exact from the network", len(original))

	// The catalog is the thing two processes were appending to at once. It is
	// reopened here as a third process would find it on disk, because the
	// in-memory copy cannot show a lost write.
	v.Close()
	closed = true

	reopened, err := openLiveVaultAt(t, home)
	if err != nil {
		t.Fatalf("reopen the vault the two processes shared: %v", err)
	}
	defer reopened.Close()

	entries := reopened.Entries()
	survived := 0
	for _, entry := range entries {
		if _, staged := original[entry.ID]; staged {
			survived++
		}
	}
	expectedConcurrent := perBatch * childRuns
	if idle {
		expectedConcurrent = 0
	}
	t.Logf("CATALOG AFTER: %d entries on disk, %d of %d staged records, %d others "+
		"(the writer added %d)", len(entries), survived, len(original), len(entries)-survived, expectedConcurrent)
	if survived != len(original) {
		t.Errorf("the catalog lost %d of %d staged records to the concurrent write",
			len(original)-survived, len(original))
	}
	if concurrent := len(entries) - survived; concurrent != expectedConcurrent {
		t.Errorf("the catalog holds %d concurrently-written record(s), want %d", concurrent, expectedConcurrent)
	}
}

// runConcurrentWriter is the second process: it writes and flushes into the
// same vault while the first is repacking it.
func runConcurrentWriter(t *testing.T) {
	home := os.Getenv(loadHomeEnv)
	perFlush, err := strconv.Atoi(os.Getenv(loadBatchEnv))
	if err != nil {
		t.Fatalf("bad %s: %v", loadBatchEnv, err)
	}
	flushes, err := strconv.Atoi(os.Getenv(loadFlushesEnv))
	if err != nil {
		t.Fatalf("bad %s: %v", loadFlushesEnv, err)
	}

	v, err := openLiveVaultAt(t, home)
	if err != nil {
		t.Fatalf("writer: open %s: %v", home, err)
	}
	defer v.Close()

	ctx := context.Background()
	for round := range flushes {
		for i := range perFlush {
			if _, err := v.Remember(ctx, vault.RememberRequest{
				Statement: fmt.Sprintf(
					"Concurrent round %d record %d: written by a second process while a repack was in flight.", round, i),
				Context: "Written to measure what a repack does when the vault is not idle.",
				Type:    record.TypeFact,
				Tags:    []string{"g3", "concurrent"},
			}); err != nil {
				t.Fatalf("writer: remember round %d record %d: %v", round, i, err)
			}
		}
		start := time.Now()
		if _, err := v.Flush(ctx); err != nil {
			t.Fatalf("writer: flush round %d: %v", round, err)
		}
		t.Logf("writer: flushed %d record(s) in %s while the repack ran", perFlush, time.Since(start))
	}
}

// TestLiveRepackInterrupted is G3's second unknown.
//
// Crash-safety through a repack is a design claim: the new slabs are pinned and
// tracked before the catalog is updated, and the old objects are deleted only
// after, so an interruption is supposed to leave storage that costs money and
// no storage that has been lost. Nothing had tested it.
//
// The interruption is a real one. A child process repacks a known, ledgered set
// of slabs and is killed part way; the parent then reopens the vault and asks
// what survived and what it can get back.
func TestLiveRepackInterrupted(t *testing.T) {
	if os.Getenv(repackChildEnv) != "" {
		runDoomedRepack(t)
		return
	}
	requireLive(t)

	home := t.TempDir()
	v, err := openLiveVaultAt(t, home)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	ctx := context.Background()
	if _, err := v.WaitReady(ctx, 90*time.Second); err != nil {
		v.Close()
		t.Fatalf("account not ready: %v", err)
	}
	defer releaseByLedger(t, home)

	const batches, perBatch = 3, 4
	staged := make(map[record.ID][]byte)
	for batch := range batches {
		for i := range perBatch {
			written, err := v.Remember(ctx, vault.RememberRequest{
				Statement: fmt.Sprintf(
					"Interrupt batch %d record %d: repack crash-safety was a design claim until this ran.", batch, i),
				Context: "Written to be repacked by a process that will not survive the repack.",
				Type:    record.TypeFact,
				Tags:    []string{"g3", "interrupt"},
			})
			if err != nil {
				t.Fatalf("remember: %v", err)
			}
			body, err := v.LocalBody(written.ID)
			if err != nil {
				t.Fatalf("local body: %v", err)
			}
			staged[written.ID] = append([]byte(nil), body...)
		}
		if _, err := v.Flush(ctx); err != nil {
			t.Fatalf("flush batch %d: %v", batch, err)
		}
	}

	// The ledger before the interruption. Everything the recovery is checked
	// against is written down first, so that "what was stranded" is a
	// comparison rather than a guess.
	beforeSlabs, err := v.TrackedSlabs()
	if err != nil {
		t.Fatalf("tracked slabs: %v", err)
	}
	beforeAccount, err := v.Account(ctx)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	beforeRefs := make(map[record.ID]string, len(staged))
	for _, entry := range v.Entries() {
		beforeRefs[entry.ID] = entry.ObjectRef
	}
	t.Logf("LEDGERED BEFORE: %d record(s) over %d slab(s), account holds %s",
		len(staged), len(beforeSlabs), human(beforeAccount.PinnedData))
	for _, slab := range beforeSlabs {
		t.Logf("  slab %s pinned %s, %d record(s)", slab.ID, slab.PinnedAt.Format(time.RFC3339), slab.Records)
	}
	v.Close()

	// The child is killed inside the write phase: late enough that new slabs
	// are being pinned, early enough that the catalog has not been updated and
	// the old objects have not been deleted. That window is the one the design
	// claim is about.
	//
	// The delay is measured from the repack's own start, not from the process
	// launch. Opening a vault costs several seconds of host discovery that vary
	// run to run, and timing the kill from launch put a first attempt entirely
	// past the operation it was meant to interrupt.
	killAfter := interruptDelay()
	child := exec.Command(os.Args[0], "-test.run", "^TestLiveRepackInterrupted$", "-test.v")
	child.Env = append(os.Environ(), repackChildEnv+"="+home)
	pipe, err := child.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	child.Stderr = child.Stdout
	if err := child.Start(); err != nil {
		t.Fatalf("start the repack that will be killed: %v", err)
	}

	var childOut bytes.Buffer
	started := make(chan time.Time, 1)
	go func() {
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			line := scanner.Text()
			childOut.WriteString(line + "\n")
			if strings.Contains(line, repackStartMarker) {
				select {
				case started <- time.Now():
				default:
				}
			}
		}
	}()

	select {
	case at := <-started:
		t.Logf("the repack began at %s; killing it %s in", at.Format(time.RFC3339Nano), killAfter)
	case <-time.After(2 * time.Minute):
		child.Process.Kill()
		t.Fatalf("the child never reached its repack:\n%s", childOut.String())
	}
	time.Sleep(killAfter)
	if err := child.Process.Kill(); err != nil {
		t.Fatalf("kill the repack: %v", err)
	}
	waitErr := child.Wait()
	t.Logf("INTERRUPTED %s into the repack: %v\n%s", killAfter, waitErr, childOut.String())
	if strings.Contains(childOut.String(), "SURVIVED") {
		t.Fatalf("the kill landed outside the repack; move %s so it falls inside the write phase",
			interruptDelayEnv)
	}

	// What a new process finds. This is the whole measurement: a vault that was
	// repacked half way, opened by something that was not there when it
	// happened.
	after, err := openLiveVaultAt(t, home)
	if err != nil {
		t.Fatalf("reopen after the interruption: %v", err)
	}
	defer after.Close()

	afterSlabs, err := after.TrackedSlabs()
	if err != nil {
		t.Fatalf("tracked slabs after: %v", err)
	}
	afterAccount, err := after.Account(ctx)
	if err != nil {
		t.Fatalf("account after: %v", err)
	}
	t.Logf("AFTER THE INTERRUPTION: ledger lists %d slab(s), account holds %s (was %s over %d slab(s))",
		len(afterSlabs), human(afterAccount.PinnedData), human(beforeAccount.PinnedData), len(beforeSlabs))

	var moved, unmoved int
	for _, entry := range after.Entries() {
		if was, staged := beforeRefs[entry.ID]; staged {
			if entry.ObjectRef == was {
				unmoved++
			} else {
				moved++
			}
		}
	}
	t.Logf("  catalog: %d record(s) still at their old location, %d relocated before the kill", unmoved, moved)

	// No data loss is the claim that matters. Every record must still be
	// readable, whichever side of the interruption its catalog entry fell on.
	lost := 0
	for id, want := range staged {
		got, err := after.BodyFromNetwork(ctx, id)
		if err != nil {
			lost++
			t.Errorf("record %s is unreadable after an interrupted repack: %v", id, err)
			continue
		}
		if !bytes.Equal(got, want) {
			lost++
			t.Errorf("record %s came back changed after an interrupted repack", id)
		}
	}
	if lost == 0 {
		t.Logf("  NO DATA LOSS: all %d record(s) read back byte-exact from the network", len(staged))
	}

	// The cost of the interruption is whatever it left pinned that nothing
	// points at. Recovering it is an ordinary sweep, and that is the claim.
	sweep, err := after.Reclaim(ctx, vault.ReclaimOptions{})
	if err != nil {
		t.Fatalf("sweep after the interruption: %v", err)
	}
	recovered, err := after.Account(ctx)
	if err != nil {
		t.Fatalf("account after the sweep: %v", err)
	}
	t.Logf("RECOVERY: an ordinary sweep deleted %d object(s), released %d slab(s), freed %s; "+
		"account now holds %s against %s before the repack",
		sweep.ObjectsDeleted, sweep.SlabsReleased, human(sweep.Freed()),
		human(recovered.PinnedData), human(beforeAccount.PinnedData))
	if recovered.PinnedData > beforeAccount.PinnedData {
		t.Errorf("the interruption cost %s of quota that a sweep did not get back",
			human(recovered.PinnedData-beforeAccount.PinnedData))
	}
}

const repackChildEnv = "SENNIT_TEST_DOOMED_REPACK"

// interruptDelay is when the child is killed, measured from its launch.
//
// It has to land inside the write phase, which starts after the vault opens and
// after every record has been read back. The default is set from the phases
// S2 measured (25.9 s for a 4-slab repack, most of it read and write) and is
// overridable so the window can be moved without a rebuild.
func interruptDelay() time.Duration {
	if raw := os.Getenv(interruptDelayEnv); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			return d
		}
	}
	return 5 * time.Second
}

const (
	interruptDelayEnv = "SENNIT_TEST_INTERRUPT_AFTER"
	// repackStartMarker is printed by the child immediately before it calls
	// Repack, so the parent can time the kill from the operation rather than
	// from the process.
	repackStartMarker = "doomed repack: starting"
)

// runDoomedRepack is the child: it repacks and is not expected to return.
func runDoomedRepack(t *testing.T) {
	home := os.Getenv(repackChildEnv)
	v, err := openLiveVaultAt(t, home)
	if err != nil {
		t.Fatalf("doomed repack: open %s: %v", home, err)
	}
	defer v.Close()

	t.Logf("%s at %s", repackStartMarker, time.Now().Format(time.RFC3339Nano))
	packed, err := v.Repack(context.Background())
	if err != nil {
		t.Fatalf("doomed repack: %v", err)
	}
	t.Logf("doomed repack: SURVIVED, moved %d record(s) in %s; the kill landed outside the operation",
		len(packed.Records), packed.Elapsed)
}

// requireLive skips a measurement that would otherwise spend real storage.
func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv(LiveEnv) == "" {
		t.Skipf("set %s=1 to run against a real indexer", LiveEnv)
	}
}

// pinnedAbove reports how much more quota an account holds than a baseline.
func pinnedAbove(now, baseline sia.Account) uint64 {
	if now.PinnedData <= baseline.PinnedData {
		return 0
	}
	return now.PinnedData - baseline.PinnedData
}
