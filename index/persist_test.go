package index_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/steven3002/sennit/index"
	"github.com/steven3002/sennit/record"
)

// xorSealer stands in for the vault's AEAD. The persistence being tested is
// about base-plus-delta mechanics, and a real cipher would only make the test
// depend on key derivation. It does invert, so a decode failure is a real one.
type xorSealer struct{ key byte }

func (s xorSealer) Seal(plaintext []byte) ([]byte, error) {
	out := make([]byte, len(plaintext))
	for i, b := range plaintext {
		out[i] = b ^ s.key
	}
	return out, nil
}

func (s xorSealer) Open(sealed []byte) ([]byte, error) { return s.Seal(sealed) }

func entry(t *testing.T, seed byte, dim int) index.Entry {
	t.Helper()
	var id record.ID
	id[record.IDSize-1] = seed
	vector := make([]float32, dim)
	for i := range vector {
		vector[i] = float32(seed)*0.01 + float32(i)*0.001
	}
	return index.Entry{ID: id, Model: "bge-small-en-v1.5-fp32", Dim: dim, Vector: vector}
}

func openStore(t *testing.T, dir string) *index.Store {
	t.Helper()
	store, err := index.OpenStore(dir, xorSealer{key: 0x5a})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func byID(entries []index.Entry) map[record.ID]index.Entry {
	out := make(map[record.ID]index.Entry, len(entries))
	for _, e := range entries {
		out[e.ID] = e
	}
	return out
}

func TestVectorsSurviveAProcessRestart(t *testing.T) {
	dir := t.TempDir()
	want := []index.Entry{entry(t, 1, 8), entry(t, 2, 8), entry(t, 3, 8)}

	store := openStore(t, dir)
	if err := store.Append(want...); err != nil {
		t.Fatalf("append: %v", err)
	}
	store.Close()

	// A fresh Store over the same directory is what a second process sees.
	reopened := openStore(t, dir)
	got, err := reopened.Hydrate()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("hydrated %d entries, want %d", len(got), len(want))
	}
	found := byID(got)
	for _, e := range want {
		back, ok := found[e.ID]
		if !ok {
			t.Fatalf("%s did not survive the restart", e.ID)
		}
		if back.Model != e.Model || back.Dim != e.Dim {
			t.Fatalf("%s came back as model %q dim %d", e.ID, back.Model, back.Dim)
		}
		for i := range e.Vector {
			if back.Vector[i] != e.Vector[i] {
				t.Fatalf("%s dimension %d came back as %v, want %v", e.ID, i, back.Vector[i], e.Vector[i])
			}
		}
	}
}

// Hydration is base plus replay, and the replay wins. A record written, folded
// into a base, then written again has to come back as the second value.
func TestReplayOverTheBaseWins(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)

	first := entry(t, 1, 4)
	if err := store.Compact([]index.Entry{first}); err != nil {
		t.Fatalf("compact: %v", err)
	}
	updated := first
	updated.Vector = []float32{9, 9, 9, 9}
	if err := store.Append(updated); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := store.Hydrate()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("hydrated %d entries, want 1, a rewritten record was duplicated", len(got))
	}
	if got[0].Vector[0] != 9 {
		t.Fatalf("hydrated the base's value %v, want the delta's", got[0].Vector[0])
	}
}

// Compaction must be lossless: what hydrates before folding must hydrate after.
func TestCompactionIsLossless(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)

	var entries []index.Entry
	for i := range 40 {
		entries = append(entries, entry(t, byte(i+1), 16))
	}
	if err := store.Append(entries...); err != nil {
		t.Fatalf("append: %v", err)
	}
	before, err := store.Hydrate()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}

	if err := store.Compact(before); err != nil {
		t.Fatalf("compact: %v", err)
	}
	after, err := store.Hydrate()
	if err != nil {
		t.Fatalf("hydrate after compaction: %v", err)
	}

	if len(after) != len(before) {
		t.Fatalf("compaction changed the entry count from %d to %d", len(before), len(after))
	}
	was, now := byID(before), byID(after)
	for id, e := range was {
		got, ok := now[id]
		if !ok {
			t.Fatalf("compaction dropped %s", id)
		}
		if !bytes.Equal(floatBytes(e.Vector), floatBytes(got.Vector)) {
			t.Fatalf("compaction changed the vector for %s", id)
		}
	}

	stats := store.Stats()
	if stats.DeltaBytes != 0 {
		t.Fatalf("delta holds %d bytes after compaction", stats.DeltaBytes)
	}
	if stats.BaseBytes == 0 {
		t.Fatalf("base is empty after compaction")
	}
	if stats.Compactions != 1 {
		t.Fatalf("recorded %d compactions, want 1", stats.Compactions)
	}
}

// The trigger is the catalog's ratio: deltas may reach a quarter of the base,
// with a floor so a small index does not rewrite itself on every write.
func TestCompactionTriggersAtTheRatio(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)

	// Below the floor nothing is due however lopsided the two files are.
	if err := store.Append(entry(t, 1, 8)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if store.DueForCompaction() {
		t.Fatal("compaction came due below the floor")
	}

	// A base large enough that the floor is no longer the binding constraint.
	var base []index.Entry
	for i := range 400 {
		base = append(base, entry(t, byte(i%250+1), 384))
	}
	if err := store.Compact(base); err != nil {
		t.Fatalf("compact: %v", err)
	}
	baseBytes := store.Stats().BaseBytes

	// Append until the deltas pass a quarter of the base, checking that the
	// trigger fires on the crossing rather than before it.
	for i := range 400 {
		if err := store.Append(entry(t, byte(i%250+1), 384)); err != nil {
			t.Fatalf("append: %v", err)
		}
		stats := store.Stats()
		due := store.DueForCompaction()
		want := stats.DeltaBytes >= index.MinCompactBytes &&
			float64(stats.DeltaBytes) > index.CompactRatio*float64(baseBytes)
		if due != want {
			t.Fatalf("at delta %d of base %d, due=%v want=%v", stats.DeltaBytes, baseBytes, due, want)
		}
		if due {
			return
		}
	}
	t.Fatal("compaction never came due")
}

// Write amplification is the reason for the shape. Appending must not rewrite
// what is already in the base.
func TestAppendingDoesNotRewriteTheBase(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)

	var base []index.Entry
	for i := range 200 {
		base = append(base, entry(t, byte(i%250+1), 384))
	}
	if err := store.Compact(base); err != nil {
		t.Fatalf("compact: %v", err)
	}
	after := store.Stats()

	if err := store.Append(entry(t, 251, 384)); err != nil {
		t.Fatalf("append: %v", err)
	}
	grew := store.Stats().Written - after.Written
	if grew > 4<<10 {
		t.Fatalf("adding one vector wrote %d bytes; the base was rewritten", grew)
	}
}

// Damage stops the replay rather than failing it. The entries before the break
// are intact, and the records behind the lost ones can be re-embedded.
func TestATruncatedDeltaKeepsWhatCameBefore(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	if err := store.Append(entry(t, 1, 8), entry(t, 2, 8), entry(t, 3, 8)); err != nil {
		t.Fatalf("append: %v", err)
	}
	store.Close()

	path := filepath.Join(dir, index.DeltaName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read delta: %v", err)
	}
	if err := os.WriteFile(path, raw[:len(raw)-12], 0o600); err != nil {
		t.Fatalf("truncate delta: %v", err)
	}

	got, err := openStore(t, dir).Hydrate()
	if err != nil {
		t.Fatalf("hydrate after truncation: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("recovered %d entries from a truncated delta, want the 2 before the break", len(got))
	}
}

func floatBytes(values []float32) []byte {
	out := make([]byte, 0, 4*len(values))
	for _, v := range values {
		out = binary.LittleEndian.AppendUint32(out, uint32(v*1000))
	}
	return out
}

// Forgetting a record is an append, not an edit, because the file is
// append-only. What matters is that it does not come back.
func TestARemovedVectorDoesNotHydrate(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)

	keep, drop := entry(t, 1, 8), entry(t, 2, 8)
	if err := store.Append(keep, drop); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Remove(drop.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	got, err := store.Hydrate()
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if len(got) != 1 || got[0].ID != keep.ID {
		t.Fatalf("hydrated %d entries, want only the surviving one", len(got))
	}

	// And it stays gone once folded into a base and reopened.
	if err := store.Compact(got); err != nil {
		t.Fatalf("compact: %v", err)
	}
	store.Close()
	again, err := openStore(t, dir).Hydrate()
	if err != nil {
		t.Fatalf("hydrate after compaction: %v", err)
	}
	if len(again) != 1 || again[0].ID != keep.ID {
		t.Fatalf("a removed vector came back after compaction: %d entries", len(again))
	}
}
