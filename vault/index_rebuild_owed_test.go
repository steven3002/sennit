package vault

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/keys"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/seal"
)

// recordSealer is the sealer a vault opened with ledgerPhrase seals its index
// with, for a test that reads the index's files with no vault open.
func recordSealer(t *testing.T) *seal.Sealer {
	t.Helper()
	seed, err := keys.SeedFromPhrase(ledgerPhrase)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	hierarchy, err := keys.Derive(seed)
	if err != nil {
		t.Fatalf("derive keys: %v", err)
	}
	sealer, err := seal.New(hierarchy.Record, hierarchy.Content)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	t.Cleanup(sealer.Close)
	return sealer
}

// owedOnDisk reports, with no vault open, whether the device under home records
// a rebuild of its index as owed, and what a read of the index's files meets.
func owedOnDisk(t *testing.T, home string) (bool, index.Damage) {
	t.Helper()
	store, err := local.Open(filepath.Join(home, dbFile))
	if err != nil {
		t.Fatalf("open the device's store: %v", err)
	}
	defer store.Close()
	_, err = store.GetMeta(indexRebuildKey)
	owed := err == nil
	if err != nil && !errors.Is(err, local.ErrNotFound) {
		t.Fatalf("read the device's settings: %v", err)
	}

	vectors, err := index.OpenStore(home, recordSealer(t))
	if err != nil {
		t.Fatalf("open the index: %v", err)
	}
	defer vectors.Close()
	var damage index.Damage
	if err := vectors.Read(func(r index.Reading) error {
		damage = r.Damage
		return nil
	}); err != nil {
		t.Fatalf("read the index: %v", err)
	}
	return owed, damage
}

// restoredAndDamaged is a device that restored writerNetwork at metadata depth,
// wrote three memories of its own, and then had the second of their vectors
// damaged. It returns the device's home and every record it holds.
func restoredAndDamaged(t *testing.T) (string, []record.ID) {
	t.Helper()
	network, restored, conversation := writerNetwork(t)
	home := t.TempDir()
	device := ledgerVault(t, home)
	RestoreForTest(t, device, network, HydrateMetadata)
	written := threeFacts(t, device)
	device.Close()
	damageIndexRecord(t, home, index.DeltaName, 2, garbleRecord)
	return home, append(append(restored, conversation), written...)
}

// A rebuild cut short, by an interrupt, a crash or a host that stopped waiting
// for its server to start, is finished by the next open.
//
// The rebuild compacts the index once it has read past the damage, and a
// compaction in any process does the same, so by the time the run stops the
// damage that called for the rebuild is no longer on the disk. What the next
// open goes on is the setting that says a rebuild is owed, which only a finished
// rebuild clears. Nothing embedded before the stop is embedded again.
func TestARebuildCutShortIsFinishedByTheNextOpen(t *testing.T) {
	home, held := restoredAndDamaged(t)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	interrupted := counting()
	interrupted.before = func(n int, _ string) error {
		if n == 1 {
			stop()
			return ctx.Err()
		}
		return nil
	}
	if v, err := Open(ctx, Options{Home: home, Phrase: ledgerPhrase, Embedder: interrupted, Offline: true}); err == nil {
		v.Close()
		t.Fatal("an open interrupted part way through its rebuild returned a vault")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("an open interrupted part way through its rebuild failed with %v, want the interrupt", err)
	}
	owed, damage := owedOnDisk(t, home)
	if !owed {
		t.Error("an interrupted rebuild left nothing on the device to say it is owed")
	}
	if damage.Found() {
		t.Errorf("the damage is still on the disk once the rebuild compacted the index: %+v", damage)
	}

	embedder := counting()
	v := openWith(t, home, embedder)
	searchable(t, v, "records, once the interrupted rebuild was finished", held...)
	if got := len(embedder.embedded()); got != 3 {
		t.Errorf("the next open embedded %d record(s), want the 3 the interrupted one did not reach", got)
	}
	want := IndexRepair{Ran: true, Embedded: 3}
	if got := v.IndexRepair(); got != want {
		t.Errorf("the next open reports %+v, want %+v", got, want)
	}
	if rebuildOwed(t, v) {
		t.Error("the finished rebuild is still recorded as owed")
	}
}

// Damage a vault already open meets as it compacts is taken off the disk by
// that compaction, with the vector it held. The vault searching in memory loses
// nothing, so it goes on serving, and records that a rebuild is owed; the next
// open rebuilds what the compaction could not read.
func TestDamageAnOpenVaultMeetsIsRebuiltByTheNextOpen(t *testing.T) {
	home := t.TempDir()
	running := ledgerVault(t, home)
	ids := threeFacts(t, running)
	if err := running.CompactIndex(); err != nil {
		t.Fatalf("compact the index: %v", err)
	}
	damageIndexRecord(t, home, index.BaseName, 2, garbleRecord)
	if err := running.CompactIndex(); err != nil {
		t.Fatalf("compact the damaged index: %v", err)
	}
	searchable(t, running, "memories, in the vault that was running", ids...)
	if !rebuildOwed(t, running) {
		t.Error("a compaction that read past damage left nothing to say a rebuild is owed")
	}
	if repair := running.IndexRepair(); repair.Ran {
		t.Errorf("the running vault reports a rebuild it did not run: %+v", repair)
	}
	running.Close()

	embedder := counting()
	v := openWith(t, home, embedder)
	searchable(t, v, "memories, in the next open", ids...)
	want := IndexRepair{Ran: true, Embedded: 1}
	if got := v.IndexRepair(); got != want {
		t.Errorf("the next open reports %+v, want %+v", got, want)
	}
}

// slowly is a model that takes a while over each record, long enough that two
// rebuilds started together are embedding at the same time.
func slowly() *countingEmbedder {
	e := counting()
	e.before = func(int, string) error {
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	return e
}

// Two processes can open the same damaged vault at once, a server and a
// command. Both rebuild, and between them the same records may be embedded
// twice, which lands on the same vector. Neither loses a vector, neither leaves
// the files unreadable, and once both are done the vault holds one vector for
// every record and nothing owed.
func TestTwoProcessesRebuildingOneIndexLoseNothing(t *testing.T) {
	home, held := restoredAndDamaged(t)

	var (
		opening sync.WaitGroup
		mu      sync.Mutex
		opened  []*Vault
	)
	embedders := []*countingEmbedder{slowly(), slowly()}
	for _, embedder := range embedders {
		opening.Add(1)
		go func() {
			defer opening.Done()
			v, err := Open(context.Background(), Options{Home: home, Phrase: ledgerPhrase, Embedder: embedder, Offline: true})
			if err != nil {
				t.Errorf("open beside another process rebuilding the same index: %v", err)
				return
			}
			mu.Lock()
			opened = append(opened, v)
			mu.Unlock()
		}()
	}
	opening.Wait()
	for _, v := range opened {
		searchable(t, v, "records, in one of the two processes", held...)
		v.Close()
	}
	if total := len(embedders[0].embedded()) + len(embedders[1].embedded()); total < 4 {
		t.Errorf("the two rebuilds embedded %d record(s) between them, fewer than the 4 that had no vector", total)
	}

	owed, damage := owedOnDisk(t, home)
	if owed {
		t.Error("a rebuild is still owed once both processes have finished theirs")
	}
	if damage.Found() {
		t.Errorf("the index's files are damaged once both processes have rebuilt them: %+v", damage)
	}
	v := openWith(t, home, counting())
	searchable(t, v, "records, once both processes were done", held...)
	if got := v.index.Len(); got != len(held) {
		t.Errorf("the index holds %d vector(s), want one for each of the %d records", got, len(held))
	}
}
