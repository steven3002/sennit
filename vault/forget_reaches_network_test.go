package vault

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/sia"
)

// A standInNetwork is the test's network: the objects flushes put there, in the
// order they put them, and every deletion asked of it.
//
// It answers a restore's walk and read and a deletion the way the indexer does:
// a deleted object is listed and read no more, and deleting one that is already
// gone counts as done.
type standInNetwork struct {
	t       *testing.T
	objects []StoredObject
	gone    map[sia.ObjectRef]bool
	// asked is every object a deletion named, in order.
	asked []sia.ObjectRef
	// refusing, when set, is what every deletion fails with.
	refusing error
}

func standIn(t *testing.T, flushes ...[]StoredObject) *standInNetwork {
	t.Helper()
	n := &standInNetwork{t: t, gone: map[sia.ObjectRef]bool{}}
	for _, flushed := range flushes {
		n.objects = append(n.objects, flushed...)
	}
	return n
}

func (n *standInNetwork) walk(_ context.Context, fn func(sia.StoredObject) error) (sia.WalkStats, error) {
	var stats sia.WalkStats
	for _, stored := range n.objects {
		if n.gone[stored.Object.Ref] {
			stats.Deleted++
			continue
		}
		stats.Live++
		if err := fn(stored.Object); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func (n *standInNetwork) read(object sia.StoredObject) ([]byte, error) {
	for _, stored := range n.objects {
		if stored.Object.Ref == object.Ref && !n.gone[object.Ref] {
			return stored.Payload, nil
		}
	}
	return nil, fmt.Errorf("object %s not found", object.Ref)
}

func (n *standInNetwork) deleteObjects(_ context.Context, refs []sia.ObjectRef) error {
	if n.refusing != nil {
		return n.refusing
	}
	for _, ref := range refs {
		n.asked = append(n.asked, ref)
		n.gone[ref] = true
	}
	return nil
}

// holds reports whether the network still lists an object.
func (n *standInNetwork) holds(ref sia.ObjectRef) bool {
	return slices.ContainsFunc(n.objects, func(stored StoredObject) bool { return stored.Object.Ref == ref }) &&
		!n.gone[ref]
}

// hydrateInto runs a whole hydrate of v over what the network holds.
func (n *standInNetwork) hydrateInto(v *Vault, depth HydrateDepth) HydrateReport {
	n.t.Helper()
	report, err := v.hydrateOver(context.Background(), HydrateRequest{Depth: depth}, n.walk, n.read)
	if err != nil {
		n.t.Fatalf("hydrate to depth %s: %v", depth, err)
	}
	return report
}

// recoverInto runs a whole recovery of v over what the network holds.
func (n *standInNetwork) recoverInto(v *Vault) RecoveryReport {
	n.t.Helper()
	report, err := v.recoverOver(context.Background(), RecoveryRequest{Embed: true}, n.walk, n.read)
	if err != nil {
		n.t.Fatalf("recover: %v", err)
	}
	return report
}

// objectOf is where a device's catalog says a record is on the network, read
// while the record is still catalogued.
func objectOf(t *testing.T, v *Vault, id record.ID) sia.ObjectRef {
	t.Helper()
	entry, err := v.manifest.Lookup(id)
	if err != nil {
		t.Fatalf("%s is not catalogued: %v", id, err)
	}
	ref, err := sia.ParseObjectRef(entry.ObjectRef)
	if err != nil {
		t.Fatalf("the catalogued object of %s: %v", id, err)
	}
	return ref
}

// stillForgotten checks that a device keeps a record forgotten: nothing of it is
// catalogued, held, or answered by recall, and the removal is still in the
// catalog, which is what owes the network the deletion of its copy.
func stillForgotten(t *testing.T, v *Vault, id record.ID, query string) {
	t.Helper()
	if _, err := v.manifest.Lookup(id); !errors.Is(err, manifest.ErrNotFound) {
		t.Errorf("the catalog names the forgotten record again: %v", err)
	}
	if _, removed := v.manifest.Removed(id); !removed {
		t.Error("the catalog has lost the removal, and with it the deletion it owes the network")
	}
	if _, err := v.local.GetBody(id); !errors.Is(err, local.ErrNotFound) {
		t.Errorf("the forgotten record is held on this device again: %v", err)
	}
	if _, err := v.local.RankingMetaFor(id); !errors.Is(err, local.ErrNotFound) {
		t.Errorf("the forgotten record is listed on this device again: %v", err)
	}
	if answered := recallIDs(t, v, query); slices.Contains(answered, id) {
		t.Errorf("recall answers %q with the forgotten record", query)
	}
}

// A hydrate on the device that forgot a memory, before anything has deleted the
// memory's copy on the network, leaves the memory out at every depth.
//
// The copy stays on the network until it is deleted, and a hydrate catalogued
// every record it found there. The entry it appended replaced the removal, so
// the forgotten memory was back, recalled, listed and opened, and the catalog no
// longer said that its copy was owed a deletion.
func TestAHydrateOnTheDeviceThatForgotAMemoryDoesNotBringItBack(t *testing.T) {
	writer := OpenWithLedgerForTest(t, t.TempDir())
	kept := rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	forgotten := rememberFact(t, writer, "The river gauge at Tidepool reads in millimetres.", "river")
	network := standIn(t, FlushToNetworkForTest(t, writer))
	if err := writer.Forget(forgotten); err != nil {
		t.Fatalf("forget: %v", err)
	}

	for _, depth := range []HydrateDepth{HydrateCatalog, HydrateMetadata, HydrateIndex} {
		t.Run(string(depth), func(t *testing.T) {
			report := network.hydrateInto(writer, depth)
			if report.Records != 1 || report.Forgotten != 1 {
				t.Errorf("the hydrate restored %d record(s) and left out %d, want the kept memory restored "+
					"and the forgotten one left out", report.Records, report.Forgotten)
			}
			stillForgotten(t, writer, forgotten, "what does the river gauge read in")
			if got := writer.ForgottenOnNetwork(); got != 1 {
				t.Errorf("%d forgotten record(s) are owed a deletion on the network, want 1", got)
			}
			if _, err := writer.manifest.Lookup(kept); err != nil {
				t.Errorf("the kept memory is not catalogued: %v", err)
			}
		})
	}
}

// A recovery on the device that forgot a memory leaves the memory out too, for
// the same reason: it catalogued and restored every record the network held.
func TestARecoverOnTheDeviceThatForgotAMemoryDoesNotBringItBack(t *testing.T) {
	writer := OpenWithLedgerForTest(t, t.TempDir())
	kept := rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	forgotten := rememberFact(t, writer, "The river gauge at Tidepool reads in millimetres.", "river")
	network := standIn(t, FlushToNetworkForTest(t, writer))
	if err := writer.Forget(forgotten); err != nil {
		t.Fatalf("forget: %v", err)
	}

	report := network.recoverInto(writer)
	if report.Recovered != 1 || report.Forgotten != 1 {
		t.Errorf("the recovery restored %d record(s) and left out %d, want the kept memory restored and "+
			"the forgotten one left out", report.Recovered, report.Forgotten)
	}
	stillForgotten(t, writer, forgotten, "what does the river gauge read in")
	if _, err := writer.local.RankingMetaFor(kept); err != nil {
		t.Errorf("the kept memory was not recovered: %v", err)
	}
}

// A conversation forgotten on this device is not rebuilt by a hydrate or a
// recovery on it, from the transcript still on the network.
//
// Its head never reaches the network, so what came back was its transcript: the
// chunks were catalogued and restored again, and the hydrate then rebuilt the
// conversation around them.
func TestARestoreOnTheDeviceThatForgotAConversationDoesNotRebuildIt(t *testing.T) {
	writer := OpenWithLedgerForTest(t, t.TempDir())
	kept := rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	saved := saveExchange(t, writer, "Tidepool gauge survey", "forgotten")
	chunks := chunkIDsOf(saved.Chunks)
	network := standIn(t, FlushToNetworkForTest(t, writer))
	if err := writer.ForgetSession(saved.ID); err != nil {
		t.Fatalf("forget the conversation: %v", err)
	}

	hydrated := network.hydrateInto(writer, HydrateIndex)
	if hydrated.Forgotten != len(chunks) || hydrated.Sessions != 0 {
		t.Errorf("the hydrate left out %d chunk(s) and rebuilt %d conversation(s), want all %d chunk(s) "+
			"left out and nothing rebuilt", hydrated.Forgotten, hydrated.Sessions, len(chunks))
	}
	checkGone(t, writer, saved.ID, chunks)

	recovered := network.recoverInto(writer)
	if recovered.Forgotten != len(chunks) {
		t.Errorf("the recovery left out %d chunk(s), want all %d", recovered.Forgotten, len(chunks))
	}
	checkGone(t, writer, saved.ID, chunks)
	if answered := recallIDs(t, writer, "which unit does each Tidepool gauge read in"); slices.Contains(answered, saved.ID) {
		t.Error("recall answers with the forgotten conversation")
	}
	if got := writer.ForgottenOnNetwork(); got != len(chunks) {
		t.Errorf("%d forgotten record(s) are owed a deletion on the network, want the %d chunk(s)", got, len(chunks))
	}
	if _, err := writer.local.RankingMetaFor(kept); err != nil {
		t.Errorf("the kept memory was lost: %v", err)
	}
}

// The deletion a forget owes the network deletes the forgotten memory's copy and
// nothing else, so a device that restores the vault afterwards does not get the
// memory back. Until it runs, one does.
//
// That window is what forget's answer has to describe, so it is shown here as
// well: a device that restored before the deletion has the memory, and keeps it,
// because a forget is not shared between devices.
func TestTheDeletionAForgetOwesTheNetworkKeepsTheMemoryFromAnyLaterRestore(t *testing.T) {
	writer := OpenWithLedgerForTest(t, t.TempDir())
	kept := rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	forgotten := rememberFact(t, writer, "The river gauge at Tidepool reads in millimetres.", "river")
	network := standIn(t, FlushToNetworkForTest(t, writer))
	copyOfForgotten, copyOfKept := objectOf(t, writer, forgotten), objectOf(t, writer, kept)
	if err := writer.Forget(forgotten); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if removal := writer.Removal(forgotten); removal != (Removal{OnNetwork: 1}) {
		t.Errorf("before the deletion the forget has reached %+v, want its copy still on the network", removal)
	}

	early := OpenWithLedgerForTest(t, t.TempDir())
	network.hydrateInto(early, HydrateMetadata)
	if _, err := early.manifest.Lookup(forgotten); err != nil {
		t.Fatalf("a device restoring before the deletion did not find the memory, so the window this "+
			"describes is not there: %v", err)
	}
	// Both objects have been read from the network, so both locations are
	// cached, the way a read caches them.
	for _, ref := range []sia.ObjectRef{copyOfForgotten, copyOfKept} {
		if err := writer.local.PutLocation(ref.String(), []string{string(probeSlab)}, []byte("location")); err != nil {
			t.Fatalf("cache the location of %s: %v", ref, err)
		}
	}

	deleted, err := writer.deleteForgotten(context.Background(), network.deleteObjects)
	if err != nil {
		t.Fatalf("delete the forgotten memory's copy: %v", err)
	}
	if deleted != 1 || !slices.Equal(network.asked, []sia.ObjectRef{copyOfForgotten}) {
		t.Errorf("the deletion deleted %d copy(ies) and asked the network for %v, want the forgotten "+
			"memory's alone, %s", deleted, network.asked, copyOfForgotten)
	}
	if !network.holds(copyOfKept) {
		t.Error("the kept memory's copy is gone from the network")
	}
	if _, err := writer.local.GetLocation(copyOfForgotten.String()); !errors.Is(err, local.ErrNotFound) {
		t.Errorf("the deleted copy's location is still cached: %v", err)
	}
	if _, err := writer.local.GetLocation(copyOfKept.String()); err != nil {
		t.Errorf("the kept copy's cached location went with the deletion: %v", err)
	}
	if got := writer.ForgottenOnNetwork(); got != 0 {
		t.Errorf("%d forgotten record(s) are still owed a deletion, want none", got)
	}
	if removal := writer.Removal(forgotten); removal != (Removal{Deleted: 1}) {
		t.Errorf("after the deletion the forget has reached %+v, want its copy deleted", removal)
	}
	if _, removed := writer.manifest.Removed(forgotten); !removed {
		t.Error("the deletion took the removal out of the catalog, so a restore here would catalogue the record")
	}

	hydrated := OpenWithLedgerForTest(t, t.TempDir())
	network.hydrateInto(hydrated, HydrateMetadata)
	recovered := OpenWithLedgerForTest(t, t.TempDir())
	network.recoverInto(recovered)
	for name, device := range map[string]*Vault{"hydrated": hydrated, "recovered": recovered} {
		if _, err := device.manifest.Lookup(forgotten); !errors.Is(err, manifest.ErrNotFound) {
			t.Errorf("a device %s after the deletion has the forgotten memory: %v", name, err)
		}
		if _, err := device.manifest.Lookup(kept); err != nil {
			t.Errorf("a device %s after the deletion does not have the kept memory: %v", name, err)
		}
	}
	if _, err := early.manifest.Lookup(forgotten); err != nil {
		t.Errorf("the device that restored before the deletion lost its copy, which no forget reaches: %v", err)
	}
}

// Every chunk of a forgotten conversation that reached the network has its copy
// deleted, and a device restoring afterwards rebuilds no conversation from what
// is left. A chunk the conversation still had queued never reached the network,
// and there is nothing of it to delete.
func TestTheDeletionAForgetOwesTheNetworkTakesAConversationsTranscript(t *testing.T) {
	writer := OpenWithLedgerForTest(t, t.TempDir())
	kept := rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	saved := saveExchange(t, writer, "Tidepool gauge survey", "flushed")
	flushed := chunkIDsOf(saved.Chunks)
	network := standIn(t, FlushToNetworkForTest(t, writer))
	queued := chunkIDsOf(appendExchange(t, writer, saved.ID, "queued"))

	if err := writer.ForgetSession(saved.ID); err != nil {
		t.Fatalf("forget the conversation: %v", err)
	}
	if removal := writer.Removal(append(slices.Clone(flushed), queued...)...); removal != (Removal{OnNetwork: len(flushed)}) {
		t.Errorf("the forget has reached %+v, want the %d flushed chunk(s) still on the network and the "+
			"queued one(s) nowhere", removal, len(flushed))
	}

	deleted, err := writer.deleteForgotten(context.Background(), network.deleteObjects)
	if err != nil {
		t.Fatalf("delete the forgotten transcript's copies: %v", err)
	}
	if deleted != len(flushed) || len(network.asked) != len(flushed) {
		t.Errorf("the deletion deleted %d copy(ies) and asked the network for %d, want the %d flushed chunk(s)",
			deleted, len(network.asked), len(flushed))
	}

	fresh := OpenWithLedgerForTest(t, t.TempDir())
	report := network.hydrateInto(fresh, HydrateMetadata)
	if report.Sessions != 0 {
		t.Errorf("a device restoring after the deletion rebuilt %d conversation(s), want none", report.Sessions)
	}
	if _, err := fresh.Session(saved.ID); !errors.Is(err, ErrNoSession) {
		t.Errorf("a device restoring after the deletion holds the forgotten conversation: %v", err)
	}
	if _, err := fresh.local.RankingMetaFor(kept); err != nil {
		t.Errorf("a device restoring after the deletion did not restore the kept memory: %v", err)
	}
}

// A forget made while the vault could not reach the network is still owed to it
// after the vault is closed and opened again, and after the catalog compacts,
// and the next run that reaches the network deletes the copy.
func TestAForgetMadeOfflineIsStillOwedToTheNetworkAfterARestart(t *testing.T) {
	home := t.TempDir()
	writer := OpenWithLedgerForTest(t, home)
	forgotten := rememberFact(t, writer, "The river gauge at Tidepool reads in millimetres.", "river")
	network := standIn(t, FlushToNetworkForTest(t, writer))
	copyOfForgotten := objectOf(t, writer, forgotten)
	if err := writer.Forget(forgotten); err != nil {
		t.Fatalf("forget: %v", err)
	}
	writer.Close()

	reopened := OpenWithLedgerForTest(t, home)
	if err := reopened.manifest.Compact(); err != nil {
		t.Fatalf("compact the catalog: %v", err)
	}
	reopened.Close()
	again := OpenWithLedgerForTest(t, home)
	if got := again.ForgottenOnNetwork(); got != 1 {
		t.Fatalf("after a restart and a compaction %d forgotten record(s) are owed a deletion, want 1", got)
	}

	if deleted, err := again.deleteForgotten(context.Background(), network.deleteObjects); err != nil || deleted != 1 {
		t.Fatalf("the next run that reached the network deleted %d copy(ies): %v", deleted, err)
	}
	if network.holds(copyOfForgotten) {
		t.Error("the forgotten memory's copy is still on the network")
	}
	again.Close()
	last := OpenWithLedgerForTest(t, home)
	if got := last.ForgottenOnNetwork(); got != 0 {
		t.Errorf("after the deletion and a restart %d forgotten record(s) are owed a deletion, want none", got)
	}
	if _, removed := last.manifest.Removed(forgotten); !removed {
		t.Error("the removal did not survive the restart")
	}
}

// A deletion the network refuses leaves the copy owed, and a later run deletes
// it.
func TestADeletionTheNetworkRefusesIsLeftForALaterRun(t *testing.T) {
	writer := OpenWithLedgerForTest(t, t.TempDir())
	forgotten := rememberFact(t, writer, "The river gauge at Tidepool reads in millimetres.", "river")
	network := standIn(t, FlushToNetworkForTest(t, writer))
	copyOfForgotten := objectOf(t, writer, forgotten)
	if err := writer.Forget(forgotten); err != nil {
		t.Fatalf("forget: %v", err)
	}

	network.refusing = errors.New("the indexer did not answer")
	deleted, err := writer.deleteForgotten(context.Background(), network.deleteObjects)
	if err == nil || !strings.Contains(err.Error(), "the indexer did not answer") {
		t.Errorf("a refused deletion reported %v, want the refusal", err)
	}
	if deleted != 0 || writer.ForgottenOnNetwork() != 1 || !network.holds(copyOfForgotten) {
		t.Errorf("a refused deletion counted %d deleted and left %d owed, want none deleted and the one owed",
			deleted, writer.ForgottenOnNetwork())
	}

	network.refusing = nil
	if deleted, err := writer.deleteForgotten(context.Background(), network.deleteObjects); err != nil || deleted != 1 {
		t.Errorf("the later run deleted %d copy(ies): %v", deleted, err)
	}
	if network.holds(copyOfForgotten) {
		t.Error("the forgotten memory's copy is still on the network")
	}
}

// A device that restored the vault deletes the copy of a record it forgot even
// though the copy is in storage another device wrote, and releases none of that
// storage. The device that wrote the record keeps its own copy, and its reclaim
// still accepts its catalog as the keep-set, though the catalog names an object
// that is no longer on the network.
func TestARestoredDeviceDeletesWhatItForgotFromStorageAnotherDeviceWrote(t *testing.T) {
	writer := OpenWithLedgerForTest(t, t.TempDir())
	kept := rememberFact(t, writer, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	forgotten := rememberFact(t, writer, "The river gauge at Tidepool reads in millimetres.", "river")
	network := standIn(t, FlushToNetworkForTest(t, writer))
	copyOfForgotten, copyOfKept := objectOf(t, writer, forgotten), objectOf(t, writer, kept)

	restored := OpenWithLedgerForTest(t, t.TempDir())
	network.hydrateInto(restored, HydrateMetadata)
	slabs, err := restored.reclaimer.Tracked()
	if err != nil {
		t.Fatalf("read the restored device's ledger: %v", err)
	}
	if len(slabs) != 1 || slabs[0].Releasable() {
		t.Fatalf("the restored device's ledger holds %+v, want the writer's one slab, not its own to release", slabs)
	}

	if err := restored.Forget(forgotten); err != nil {
		t.Fatalf("forget on the restored device: %v", err)
	}
	deleted, err := restored.deleteForgotten(context.Background(), network.deleteObjects)
	if err != nil || deleted != 1 {
		t.Fatalf("the restored device deleted %d copy(ies): %v", deleted, err)
	}
	if !slices.Equal(network.asked, []sia.ObjectRef{copyOfForgotten}) || !network.holds(copyOfKept) {
		t.Errorf("the restored device asked the network to delete %v, want the forgotten record's copy alone",
			network.asked)
	}
	after, err := restored.reclaimer.Tracked()
	if err != nil {
		t.Fatalf("read the restored device's ledger again: %v", err)
	}
	if !slices.Equal(after, slabs) {
		t.Errorf("the deletion changed the restored device's ledger from %+v to %+v", slabs, after)
	}

	fresh := OpenWithLedgerForTest(t, t.TempDir())
	network.hydrateInto(fresh, HydrateMetadata)
	if _, err := fresh.manifest.Lookup(forgotten); !errors.Is(err, manifest.ErrNotFound) {
		t.Errorf("a device restoring afterwards has the forgotten record: %v", err)
	}

	// The writer is told nothing, since forgets are not shared. It still reads
	// the record from its own copy, and its catalog still names the object.
	if found, tier, err := writer.Fetch(context.Background(), forgotten); err != nil || found.Memory == nil {
		t.Errorf("the writer no longer reads its own copy (%s): %v", tier, err)
	}
	live, err := writer.live(false)
	if err != nil {
		t.Fatalf("the writer's keep-set: %v", err)
	}
	if _, named := live.Objects[copyOfForgotten]; !named {
		t.Error("the writer's keep-set no longer names the object its catalog points at")
	}
	pinned, err := writer.reclaimer.Tracked()
	if err != nil {
		t.Fatalf("read the writer's ledger: %v", err)
	}
	if err := live.Check(len(pinned)); err != nil {
		t.Errorf("the writer's reclaim would refuse its keep-set: %v", err)
	}
}

// An object a record this vault still holds is catalogued at is not deleted
// for a removal that names it as well, and the removal stays owed and says why.
//
// Every writer here puts one record in an object, so the two can meet only in a
// catalog this build did not write. Deleting the object would take the record
// that is kept with it.
func TestAnObjectARecordStillNeedsIsNeverDeleted(t *testing.T) {
	v := OpenWithLedgerForTest(t, t.TempDir())
	kept := rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour")
	forgotten := rememberFact(t, v, "The river gauge at Tidepool reads in millimetres.", "river")
	flushQueue(t, v)
	entry, err := v.manifest.Lookup(kept)
	if err != nil {
		t.Fatalf("look up the kept record: %v", err)
	}
	entry.ObjectRef = objectOf(t, v, forgotten).String()
	if err := v.manifest.Append(entry); err != nil {
		t.Fatalf("catalogue the kept record in the forgotten one's object: %v", err)
	}
	if err := v.Forget(forgotten); err != nil {
		t.Fatalf("forget: %v", err)
	}

	network := standIn(t)
	deleted, err := v.deleteForgotten(context.Background(), network.deleteObjects)
	if err == nil || !strings.Contains(err.Error(), kept.String()) {
		t.Errorf("a removal sharing its object with a kept record reported %v, want it named", err)
	}
	if deleted != 0 || len(network.asked) != 0 {
		t.Errorf("the deletion deleted %d copy(ies) and asked the network for %v, want nothing", deleted, network.asked)
	}
	if got := v.ForgottenOnNetwork(); got != 1 {
		t.Errorf("%d forgotten record(s) are owed a deletion, want the one still on the network", got)
	}
}

// A memory forgotten before its first flush leaves nothing on the network, and
// nothing is owed to it: the next run asks the network for nothing.
func TestAMemoryForgottenBeforeItsFlushLeavesNothingToDelete(t *testing.T) {
	v := OpenWithLedgerForTest(t, t.TempDir())
	forgotten := rememberFact(t, v, "The river gauge at Tidepool reads in millimetres.", "river")
	if err := v.Forget(forgotten); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if got := v.ForgottenOnNetwork(); got != 0 {
		t.Errorf("%d forgotten record(s) are owed a deletion, want none", got)
	}
	if removal := v.Removal(forgotten); removal != (Removal{}) {
		t.Errorf("the forget has reached %+v, want nothing of it ever on the network", removal)
	}
	network := standIn(t)
	if deleted, err := v.deleteForgotten(context.Background(), network.deleteObjects); err != nil || deleted != 0 {
		t.Errorf("the next run deleted %d copy(ies): %v", deleted, err)
	}
	if len(network.asked) != 0 {
		t.Errorf("the next run asked the network to delete %v", network.asked)
	}
}
