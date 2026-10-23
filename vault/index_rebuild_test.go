package vault

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/steven3002/sennit/embed/embedtest"
	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/store/reclaim"
)

// A countingEmbedder is the stub embedder, keeping each text it is asked to
// embed. before, when it is set, runs ahead of each embedding with how many
// there have been so far, and an error from it is the embedding's.
type countingEmbedder struct {
	*embedtest.Stub
	mu     sync.Mutex
	texts  []string
	before func(n int, text string) error
}

func counting() *countingEmbedder { return &countingEmbedder{Stub: embedtest.NewStub()} }

func (e *countingEmbedder) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	n, before := len(e.texts), e.before
	e.mu.Unlock()
	if before != nil {
		if err := before(n, text); err != nil {
			return nil, err
		}
	}
	e.mu.Lock()
	e.texts = append(e.texts, text)
	e.mu.Unlock()
	return e.Stub.EmbedOne(ctx, text)
}

// embedded is every text embedded so far.
func (e *countingEmbedder) embedded() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.texts)
}

// openWith opens the vault under home as the next process to use it does, with
// e as its model, and closes it when the test ends.
func openWith(t *testing.T, home string, e *countingEmbedder) *Vault {
	t.Helper()
	v, err := Open(context.Background(), Options{Home: home, Phrase: ledgerPhrase, Embedder: e, Offline: true})
	if err != nil {
		t.Fatalf("open the vault: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	v.reclaimer = reclaim.New(nil, v.local)
	return v
}

// indexRecords splits one of the index's files under home into its records,
// each with its length in front, the way the index frames them.
func indexRecords(t *testing.T, home, name string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var out [][]byte
	for at := 0; at < len(raw); {
		size := int(binary.LittleEndian.Uint32(raw[at:]))
		out = append(out, raw[at:at+4+size])
		at += 4 + size
	}
	return out
}

// damageIndexRecord rewrites the nth record of one of the index's files under
// home, counting from one, with what change makes of it.
func damageIndexRecord(t *testing.T, home, name string, n int, change func([]byte) []byte) {
	t.Helper()
	recs := indexRecords(t, home, name)
	if n > len(recs) {
		t.Fatalf("%s holds %d record(s), so there is no record %d to damage", name, len(recs), n)
	}
	recs[n-1] = change(bytes.Clone(recs[n-1]))
	if err := os.WriteFile(filepath.Join(home, name), bytes.Join(recs, nil), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// garbleRecord changes one byte of a record's sealed body, which keeps its
// length whole and stops it opening.
func garbleRecord(rec []byte) []byte {
	rec[len(rec)/2] ^= 0xff
	return rec
}

// misframeRecord gives a record a length that claims more than any file holds.
func misframeRecord(rec []byte) []byte {
	binary.LittleEndian.PutUint32(rec, 0x7fffffff)
	return rec
}

// rebuildOwed reports whether the device's store says a rebuild of the search
// index is owed.
func rebuildOwed(t *testing.T, v *Vault) bool {
	t.Helper()
	_, err := v.local.GetMeta(indexRebuildKey)
	if errors.Is(err, local.ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("read the device's settings: %v", err)
	}
	return true
}

// damageOnDisk is what a read of the index's files under the vault meets now.
func damageOnDisk(t *testing.T, v *Vault) index.Damage {
	t.Helper()
	var damage index.Damage
	if err := v.vectors.Read(func(r index.Reading) error {
		damage = r.Damage
		return nil
	}); err != nil {
		t.Fatalf("read the index: %v", err)
	}
	return damage
}

// searchable fails the test for each id the vault's index cannot search.
func searchable(t *testing.T, v *Vault, what string, ids ...record.ID) {
	t.Helper()
	var lost int
	for _, id := range ids {
		if !v.index.Has(id) {
			lost++
		}
	}
	if lost > 0 {
		t.Errorf("%d of the %d %s cannot be found by meaning", lost, len(ids), what)
	}
}

// threeFacts stores three memories in the vault and returns their ids, in the
// order their vectors reach the index.
func threeFacts(t *testing.T, v *Vault) []record.ID {
	t.Helper()
	return []record.ID{
		rememberFact(t, v, "The harbour gauge at Tidepool reads in centimetres.", "harbour"),
		rememberFact(t, v, "The river gauge at Tidepool reads in millimetres.", "river"),
		rememberFact(t, v, "The tide tables at Tidepool are printed every spring.", "tides"),
	}
}

// The delta damaged in the middle. A record whose length is whole and whose
// contents do not open was the end of the read, so the vector after it was
// dropped with it, and the vault opened searching without either and said
// nothing. The read steps over the damage now, the vault gives the one record
// whose vector was lost a vector again before it opens, and it says so.
func TestAVaultWhoseIndexDeltaIsDamagedInTheMiddleSearchesEveryRecordAgain(t *testing.T) {
	home := t.TempDir()
	writer := ledgerVault(t, home)
	ids := threeFacts(t, writer)
	writer.Close()
	damageIndexRecord(t, home, index.DeltaName, 2, garbleRecord)

	embedder := counting()
	var phases []Progress
	v, err := Open(context.Background(), Options{
		Home: home, Phrase: ledgerPhrase, Embedder: embedder, Offline: true,
		OnProgress: func(p Progress) {
			if p.Phase == PhaseIndexRebuild {
				phases = append(phases, p)
			}
		},
	})
	if err != nil {
		t.Fatalf("a vault whose index was damaged in the middle would not open: %v", err)
	}
	defer v.Close()

	searchable(t, v, "memories, one of them behind the damage", ids...)
	if got := embedder.embedded(); len(got) != 1 || !strings.Contains(got[0], "river gauge") {
		t.Errorf("the open embedded %q, want only the memory whose vector was damaged", got)
	}
	want := IndexRepair{Ran: true, Found: index.Damage{Skipped: 1}, Embedded: 1}
	if got := v.IndexRepair(); got != want {
		t.Errorf("the vault reports %+v, want %+v", got, want)
	}
	if len(phases) != 2 || phases[0] != (Progress{Phase: PhaseIndexRebuild, Total: 1, Unit: UnitRecords}) ||
		phases[1] != (Progress{Phase: PhaseIndexRebuild, Done: 1, Total: 1, Unit: UnitRecords}) {
		t.Errorf("the rebuild reported %+v, want it starting with 1 record to embed and then that 1 done", phases)
	}
	if rebuildOwed(t, v) {
		t.Error("a finished rebuild is still recorded as owed")
	}
	if damage := damageOnDisk(t, v); damage.Found() {
		t.Errorf("the damage is still on the disk after the rebuild: %+v", damage)
	}
	v.Close()

	again := counting()
	next := openWith(t, home, again)
	if got := next.IndexRepair(); got != (IndexRepair{}) {
		t.Errorf("the open after a finished rebuild reports %+v", got)
	}
	if got := again.embedded(); len(got) != 0 {
		t.Errorf("the open after a finished rebuild embedded %d record(s)", len(got))
	}
	searchable(t, next, "memories, once reopened", ids...)
}

// The base damaged in the middle, in both of the ways a record can be. One whose
// contents do not open is stepped over, and only its vector is lost. One whose
// length cannot be true takes the rest of the base with it, and the vault gives
// every record that lost its vector one again.
func TestAVaultWhoseIndexBaseIsDamagedInTheMiddleSearchesEveryRecordAgain(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage func([]byte) []byte
		found  func(index.Damage) bool
		lost   int
	}{
		{"a record that does not open", garbleRecord, func(d index.Damage) bool { return d == index.Damage{Skipped: 1} }, 1},
		{"a length that cannot be true", misframeRecord, func(d index.Damage) bool { return d.Skipped == 0 && d.Lost > 0 }, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			writer := ledgerVault(t, home)
			ids := threeFacts(t, writer)
			if err := writer.CompactIndex(); err != nil {
				t.Fatalf("compact the index: %v", err)
			}
			writer.Close()
			damageIndexRecord(t, home, index.BaseName, 2, c.damage)

			embedder := counting()
			v := openWith(t, home, embedder)
			searchable(t, v, "memories, behind and after the damage", ids...)
			if got := len(embedder.embedded()); got != c.lost {
				t.Errorf("the open embedded %d record(s), want the %d whose vector was lost", got, c.lost)
			}
			repair := v.IndexRepair()
			if !repair.Ran || !c.found(repair.Found) || repair.Embedded != c.lost {
				t.Errorf("the vault reports %+v", repair)
			}
		})
	}
}

// A length damaged in the middle of the delta stops its framing, and the store
// cuts the delta back to it as it opens, as it does for an append a crash cut
// off, which took the records after it with it and said nothing. The cut is
// damage when whole records go with it, and the vault rebuilds what it took.
func TestALengthDamagedInTheMiddleOfTheDeltaIsRebuiltPastTheCut(t *testing.T) {
	home := t.TempDir()
	writer := ledgerVault(t, home)
	ids := threeFacts(t, writer)
	writer.Close()
	damageIndexRecord(t, home, index.DeltaName, 2, misframeRecord)

	embedder := counting()
	v := openWith(t, home, embedder)
	searchable(t, v, "memories, behind and after the damaged length", ids...)
	if got := len(embedder.embedded()); got != 2 {
		t.Errorf("the open embedded %d record(s), want the 2 the cut took", got)
	}
	if repair := v.IndexRepair(); !repair.Ran || repair.Found.Lost == 0 || repair.Embedded != 2 {
		t.Errorf("the vault reports %+v", repair)
	}
}

// writerNetwork is what a writer device puts on the network the test stands in
// for: two memories and a conversation.
func writerNetwork(t *testing.T) ([]StoredObject, []record.ID, record.ID) {
	t.Helper()
	writer := ledgerVault(t, t.TempDir())
	defer writer.Close()
	memories := []record.ID{
		rememberFact(t, writer, "The lighthouse at Tidepool was relit in March.", "lighthouse"),
		rememberFact(t, writer, "The ferry to Tidepool leaves on the hour.", "ferry"),
	}
	saved, err := writer.SaveSession(context.Background(), SaveSessionRequest{
		Title:    "Tidepool station survey",
		Summary:  "Counted the stations reporting hourly.",
		Messages: exchange("survey", false),
	})
	if err != nil {
		t.Fatalf("save the conversation: %v", err)
	}
	return FlushToNetworkForTest(t, writer), memories, saved.ID
}

// Every record the device holds without a vector is embedded once the index is
// found damaged, not only the one the damage lost. That takes in the memories
// and the conversation a hydrate at metadata depth restored without a vector on
// purpose.
func TestEveryRecordWithoutAVectorIsEmbeddedOnceTheIndexIsFoundDamaged(t *testing.T) {
	network, restored, conversation := writerNetwork(t)
	home := t.TempDir()
	device := ledgerVault(t, home)
	RestoreForTest(t, device, network, HydrateMetadata)
	local := threeFacts(t, device)
	device.Close()
	damageIndexRecord(t, home, index.DeltaName, 2, garbleRecord)

	embedder := counting()
	v := openWith(t, home, embedder)
	searchable(t, v, "memories restored without a vector", restored...)
	searchable(t, v, "conversations restored without a vector", conversation)
	searchable(t, v, "memories written here", local...)
	if got := len(embedder.embedded()); got != 4 {
		t.Errorf("the open embedded %d record(s), want the 3 restored without a vector and the 1 damaged", got)
	}
	if repair := v.IndexRepair(); repair.Embedded != 4 {
		t.Errorf("the vault reports %+v, want 4 embedded", repair)
	}
}

// An open with nothing damaged embeds nothing new and rewrites nothing, on a
// device that holds every vector and on one restored at metadata depth that
// holds none.
func TestAnOpenWithNothingDamagedEmbedsNothingAndRewritesNothing(t *testing.T) {
	restoredHome := t.TempDir()
	network, _, _ := writerNetwork(t)
	restored := ledgerVault(t, restoredHome)
	RestoreForTest(t, restored, network, HydrateMetadata)
	restored.Close()

	writtenHome := t.TempDir()
	written := ledgerVault(t, writtenHome)
	threeFacts(t, written)
	if err := written.CompactIndex(); err != nil {
		t.Fatalf("compact the index: %v", err)
	}
	rememberFact(t, written, "The pier at Tidepool was rebuilt in stone.", "pier")
	written.Close()

	for name, home := range map[string]string{
		"restored at metadata depth": restoredHome,
		"written here":               writtenHome,
	} {
		t.Run(name, func(t *testing.T) {
			before := indexFiles(t, home)
			embedder := counting()
			v := openWith(t, home, embedder)
			if got := embedder.embedded(); len(got) != 0 {
				t.Errorf("the open embedded %d record(s)", len(got))
			}
			if got := v.IndexRepair(); got != (IndexRepair{}) {
				t.Errorf("the open reports a rebuild: %+v", got)
			}
			if rebuildOwed(t, v) {
				t.Error("the open recorded a rebuild as owed")
			}
			v.Close()
			if after := indexFiles(t, home); !sameFiles(before, after) {
				t.Errorf("the open rewrote the index's files")
			}
		})
	}
}

// indexFiles is what the index's two files under home hold, absent ones left
// out.
func indexFiles(t *testing.T, home string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
	for _, name := range []string{index.BaseName, index.DeltaName} {
		raw, err := os.ReadFile(filepath.Join(home, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out[name] = raw
	}
	return out
}

func sameFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for name, raw := range a {
		if !bytes.Equal(raw, b[name]) {
			return false
		}
	}
	return true
}
