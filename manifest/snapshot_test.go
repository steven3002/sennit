package manifest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steven3002/sennit/manifest"
	"github.com/steven3002/sennit/record"
)

// A snapshot must be a faithful replacement for the log it folds in: the
// catalog is the only thing that turns a record id into a location, and a
// compaction that lost an entry would cost the record.
func TestCompactionPreservesEveryEntry(t *testing.T) {
	dir := t.TempDir()
	catalog := openCatalog(t, dir)

	const count = 400
	want := make(map[record.ID]string, count)
	for i := range count {
		e := entry(t, fmt.Sprintf("object-%d", i))
		if err := catalog.Append(e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		want[e.ID] = e.ObjectRef
	}
	if err := catalog.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	catalog.Close()

	reopened := openCatalog(t, dir)
	if reopened.Len() != count {
		t.Fatalf("catalog holds %d entries after compaction, want %d", reopened.Len(), count)
	}
	for id, ref := range want {
		got, err := reopened.Lookup(id)
		if err != nil {
			t.Fatalf("lookup %s: %v", id, err)
		}
		if got.ObjectRef != ref {
			t.Fatalf("record %s resolves to %q, want %q", id, got.ObjectRef, ref)
		}
	}
}

// A snapshot holds one line per record, not one per write. Repack rewrites
// every entry, so a catalog that kept every version would grow with the number
// of repacks rather than with the number of records.
func TestCompactionDropsSupersededEntries(t *testing.T) {
	dir := t.TempDir()
	catalog := openCatalog(t, dir)

	written := entry(t, "before")
	for _, ref := range []string{"before", "after-one-repack", "after-two-repacks"} {
		moved := written
		moved.ObjectRef = ref
		if err := catalog.Append(moved); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := catalog.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}

	stats := catalog.Stats()
	if stats.Records != 1 {
		t.Fatalf("catalog holds %d records for one record id", stats.Records)
	}
	if stats.LogBytes != 0 {
		t.Fatalf("the log holds %d bytes after compaction, want none", stats.LogBytes)
	}
	single := stats.SnapshotBytes
	if single == 0 {
		t.Fatal("compaction wrote an empty snapshot")
	}

	got, err := catalog.Lookup(written.ID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ObjectRef != "after-two-repacks" {
		t.Fatalf("the snapshot kept %q, want the newest location", got.ObjectRef)
	}
}

// Compaction renames a finished snapshot into place before emptying the log,
// so the only state a crash can leave is a log holding entries the snapshot
// already has. Replaying those twice must land on the same catalog.
func TestReplayToleratesEntriesTheSnapshotAlreadyHas(t *testing.T) {
	dir := t.TempDir()
	catalog := openCatalog(t, dir)

	const count = 50
	ids := make([]record.ID, 0, count)
	for i := range count {
		e := entry(t, fmt.Sprintf("object-%d", i))
		if err := catalog.Append(e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		ids = append(ids, e.ID)
	}
	log, err := os.ReadFile(filepath.Join(dir, manifest.LogName))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if err := catalog.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	catalog.Close()

	// Put the log back exactly as a crash between the rename and the truncate
	// would have left it.
	if err := os.WriteFile(filepath.Join(dir, manifest.LogName), log, 0o600); err != nil {
		t.Fatalf("restore log: %v", err)
	}

	reopened := openCatalog(t, dir)
	if reopened.Len() != count {
		t.Fatalf("a replay over a duplicated log holds %d records, want %d", reopened.Len(), count)
	}
	for _, id := range ids {
		if _, err := reopened.Lookup(id); err != nil {
			t.Fatalf("lookup %s: %v", id, err)
		}
	}
}

// The reason the catalog is log-structured at all: a monolithic catalog is
// rewritten whole on every append, so its total write volume grows with the
// square of the record count. This measures both curves on the same entries.
func TestWriteVolumeStaysSubLinear(t *testing.T) {
	counts := []int{500, 1000, 2000, 4000}

	type point struct {
		records                   int
		logStructured, monolithic int64
		compactions               int
	}
	points := make([]point, 0, len(counts))
	var entryBytes int64

	for _, count := range counts {
		catalog := openCatalog(t, t.TempDir())

		for i := range count {
			e := entry(t, fmt.Sprintf("object-%d", i))
			before := catalog.Stats().Written
			if err := catalog.Append(e); err != nil {
				t.Fatalf("append %d: %v", i, err)
			}
			if entryBytes == 0 {
				entryBytes = catalog.Stats().Written - before
			}
		}
		stats := catalog.Stats()

		// A monolithic catalog rewrites every entry it holds on every append,
		// which is the sum of one to N entries.
		monolithic := entryBytes * int64(count) * int64(count+1) / 2
		points = append(points, point{count, stats.Written, monolithic, stats.Compactions})
		catalog.Close()
	}

	t.Log("records   log-structured        monolithic      ratio   compactions   bytes/record")
	for _, p := range points {
		t.Logf("%7d   %14d   %15d   %8.1fx   %11d   %12.0f",
			p.records, p.logStructured, p.monolithic,
			float64(p.monolithic)/float64(p.logStructured), p.compactions,
			float64(p.logStructured)/float64(p.records))
	}

	// Sub-linear total volume means the bytes written per record are bounded by
	// something that does not depend on how many records there are. With the
	// log written once and each snapshot a fixed multiple larger than the last,
	// the snapshots form a geometric series and the bound is a constant number
	// of entry lines per record. A monolith has no such bound: its per-record
	// cost is half the record count.
	ceiling := float64(entryBytes) * (1 + (1+manifest.CompactRatio)/manifest.CompactRatio)
	for _, p := range points {
		perRecord := float64(p.logStructured) / float64(p.records)
		if perRecord > ceiling {
			t.Fatalf("at %d records the catalog wrote %.0f bytes per record, above the %.0f the compaction ratio bounds it to",
				p.records, perRecord, ceiling)
		}
	}
	t.Logf("bytes written per record stays under %.0f, which is %.1f entry lines and independent of the record count",
		ceiling, ceiling/float64(entryBytes))

	// The gap against a monolith must widen with the record count. That is the
	// difference between a cost that grows with N and one that grows with N².
	first, last := points[0], points[len(points)-1]
	firstGap := float64(first.monolithic) / float64(first.logStructured)
	lastGap := float64(last.monolithic) / float64(last.logStructured)
	if lastGap <= firstGap {
		t.Fatalf("the advantage over a monolith went from %.1fx at %d records to %.1fx at %d: the curves are not diverging",
			firstGap, first.records, lastGap, last.records)
	}
	t.Logf("advantage over a monolithic catalog widens from %.0fx at %d records to %.0fx at %d",
		firstGap, first.records, lastGap, last.records)
}

// The compaction trigger is a ratio, so it needs no tuning as a vault grows:
// the interval between snapshots widens with the catalog instead of staying
// fixed, which is what turns the total cost from quadratic into linear.
//
// It only binds once a quarter of the snapshot exceeds the floor. Below that
// the floor is what fires, at even intervals, so the ratio is measured on the
// snapshots above it.
func TestCompactionFiresOnTheRatioNotACount(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	ratioBinds := int64(manifest.MinCompactBytes / manifest.CompactRatio)

	var lastSnapshot int64
	var ratios []float64
	for i := range 20_000 {
		if err := catalog.Append(entry(t, fmt.Sprintf("object-%d", i))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		snapshot := catalog.Stats().SnapshotBytes
		if snapshot != lastSnapshot && lastSnapshot > ratioBinds {
			ratios = append(ratios, float64(snapshot)/float64(lastSnapshot))
		}
		lastSnapshot = snapshot
	}
	if len(ratios) < 3 {
		t.Fatalf("only %d compactions above the floor in 20,000 appends", len(ratios))
	}

	// Each snapshot should be about a quarter larger than the one before.
	for i, ratio := range ratios {
		if ratio < 1.1 || ratio > 1.45 {
			t.Fatalf("snapshot %d grew by %.2fx, want roughly the %.2f compaction ratio",
				i+1, ratio, 1+manifest.CompactRatio)
		}
	}
	t.Logf("%d compactions above the floor, each snapshot %.2f-%.2fx the last, against a %.2f ratio",
		len(ratios), minOf(ratios), maxOf(ratios), 1+manifest.CompactRatio)
}

// A catalog holding a handful of records must not rewrite itself on every
// append: a quarter of almost nothing is almost nothing.
func TestSmallCatalogsDoNotCompact(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	for i := range 20 {
		if err := catalog.Append(entry(t, fmt.Sprintf("object-%d", i))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if got := catalog.Stats().Compactions; got != 0 {
		t.Fatalf("a 20 record catalog compacted %d times", got)
	}
}

func minOf(values []float64) float64 {
	out := values[0]
	for _, v := range values {
		out = min(out, v)
	}
	return out
}

func maxOf(values []float64) float64 {
	out := values[0]
	for _, v := range values {
		out = max(out, v)
	}
	return out
}
